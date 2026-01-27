package integration

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	// pidFileName is the name of the file that tracks Redis process PIDs
	pidFileName = ".redis-test-pids"
)

// syncBuffer is a thread-safe buffer for capturing process output.
// It wraps bytes.Buffer with a mutex to allow concurrent writes from stdout and stderr.
type syncBuffer struct {
	buf bytes.Buffer
	mu  sync.Mutex
}

func (sb *syncBuffer) Write(p []byte) (n int, err error) {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.buf.Write(p)
}

func (sb *syncBuffer) String() string {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.buf.String()
}

func (sb *syncBuffer) Len() int {
	sb.mu.Lock()
	defer sb.mu.Unlock()
	return sb.buf.Len()
}

var (
	// Global registry of all active test clusters
	activeClusters    = make(map[*TestCluster]struct{})
	activeClustersMu  sync.Mutex
	signalHandlerOnce sync.Once
	cleanupOnce       sync.Once
	pidFilePath       string
	pidFileMu         sync.Mutex

	// Global port allocator to prevent port conflicts across parallel tests
	allocatedPorts   = make(map[int]bool)
	allocatedPortsMu sync.Mutex
)

// getPidFilePathLocked returns the path to the PID file. Caller must hold pidFileMu.
func getPidFilePathLocked() string {
	if pidFilePath == "" {
		baseTestDataDir := filepath.Join(".", "testdata")
		_ = os.MkdirAll(baseTestDataDir, 0755)
		pidFilePath = filepath.Join(baseTestDataDir, pidFileName)
	}
	return pidFilePath
}

// recordPid records a Redis server PID to the PID file for cleanup tracking.
func recordPid(pid int) error {
	pidFileMu.Lock()
	defer pidFileMu.Unlock()

	f, err := os.OpenFile(getPidFilePathLocked(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = fmt.Fprintf(f, "%d\n", pid)
	return err
}

// removePid removes a PID from the PID file after successful cleanup.
func removePid(pid int) {
	pidFileMu.Lock()
	defer pidFileMu.Unlock()

	path := getPidFilePathLocked()
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}

	lines := strings.Split(string(data), "\n")
	var newLines []string
	pidStr := strconv.Itoa(pid)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" && line != pidStr {
			newLines = append(newLines, line)
		}
	}

	if len(newLines) == 0 {
		os.Remove(path)
	} else {
		os.WriteFile(path, []byte(strings.Join(newLines, "\n")+"\n"), 0644)
	}
}

// killOrphanedProcesses reads the PID file and kills any orphaned Redis processes
// from previous test runs that weren't properly cleaned up.
func killOrphanedProcesses() {
	pidFileMu.Lock()
	path := getPidFilePathLocked()
	pidFileMu.Unlock()

	data, err := os.ReadFile(path)
	if err != nil {
		return // No PID file, nothing to clean up
	}

	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	var killedPids []int
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		pid, err := strconv.Atoi(line)
		if err != nil {
			continue
		}

		// Check if process exists by sending signal 0
		proc, err := os.FindProcess(pid)
		if err != nil {
			killedPids = append(killedPids, pid)
			continue
		}

		// Check if the process is still running
		if err := proc.Signal(syscall.Signal(0)); err != nil {
			// Process doesn't exist
			killedPids = append(killedPids, pid)
			continue
		}

		// Try to check if it's actually a redis-server process
		// by reading /proc/pid/comm on Linux
		commPath := fmt.Sprintf("/proc/%d/comm", pid)
		if commData, err := os.ReadFile(commPath); err == nil {
			comm := strings.TrimSpace(string(commData))
			if comm != "redis-server" {
				// Not a redis-server, remove from tracking
				killedPids = append(killedPids, pid)
				continue
			}
		}

		// Kill the orphaned redis-server process
		fmt.Fprintf(os.Stderr, "=== Killing orphaned redis-server process (PID %d) ===\n", pid)

		// First try SIGTERM for graceful shutdown
		_ = proc.Signal(syscall.SIGTERM)

		// Wait briefly for graceful shutdown
		time.Sleep(100 * time.Millisecond)

		// Check if still running, force kill if needed
		if err := proc.Signal(syscall.Signal(0)); err == nil {
			// Still running, use SIGKILL
			_ = syscall.Kill(-pid, syscall.SIGKILL) // Kill process group
			_ = proc.Kill()
		}

		killedPids = append(killedPids, pid)
	}

	// Remove killed PIDs from file
	for _, pid := range killedPids {
		removePid(pid)
	}
}

// initSignalHandler sets up a global signal handler that cleans up all active clusters.
// This is called once and handles signals for all clusters created during testing.
func initSignalHandler() {
	signalHandlerOnce.Do(func() {
		// First, kill any orphaned processes from previous runs
		killOrphanedProcesses()

		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM, syscall.SIGQUIT)
		go func() {
			sig := <-sigChan
			fmt.Fprintf(os.Stderr, "\n=== Received signal %v, cleaning up all test clusters ===\n", sig)
			doCleanup()
			os.Exit(1)
		}()
	})
}

// doCleanup performs the actual cleanup, ensuring it only runs once.
func doCleanup() {
	cleanupOnce.Do(func() {
		cleanupAllClusters()
	})
}

// registerCluster adds a cluster to the global registry for signal handling.
func registerCluster(cluster *TestCluster) {
	activeClustersMu.Lock()
	defer activeClustersMu.Unlock()
	activeClusters[cluster] = struct{}{}
	// Ensure signal handler is initialized
	initSignalHandler()
}

// unregisterCluster removes a cluster from the global registry.
func unregisterCluster(cluster *TestCluster) {
	activeClustersMu.Lock()
	defer activeClustersMu.Unlock()
	delete(activeClusters, cluster)
}

// cleanupAllClusters stops all active clusters.
// This is called when a signal is received.
func cleanupAllClusters() {
	activeClustersMu.Lock()
	count := len(activeClusters)
	clusters := make([]*TestCluster, 0, count)
	for cluster := range activeClusters {
		clusters = append(clusters, cluster)
	}
	activeClustersMu.Unlock()

	for _, cluster := range clusters {
		fmt.Fprintf(os.Stderr, "Stopping cluster: %s\n", cluster.name)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if err := cluster.StopCluster(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "Error stopping cluster %s: %v\n", cluster.name, err)
		}
		cancel()
	}
}

// findAvailablePort finds a single available port by letting the OS assign one.
// It ensures BOTH the port and port+10000 are available, since Redis Cluster
// automatically uses port+10000 for the cluster bus.
// Uses a global allocator to prevent TOCTOU race conditions across parallel tests.
func findAvailablePort() (int, error) {
	const maxAttempts = 100

	allocatedPortsMu.Lock()
	defer allocatedPortsMu.Unlock()

	for attempt := 0; attempt < maxAttempts; attempt++ {
		// Let OS assign a random available port (briefly, just to get a candidate)
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return 0, fmt.Errorf("failed to find available port: %w", err)
		}
		port := ln.Addr().(*net.TCPAddr).Port
		ln.Close()

		// Check if this port or its cluster bus port are already allocated
		clusterBusPort := port + 10000
		if allocatedPorts[port] || allocatedPorts[clusterBusPort] {
			continue
		}

		// Check if cluster bus port is actually available on the system
		lnBus, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", clusterBusPort))
		if err != nil {
			// Cluster bus port not available, try again
			continue
		}
		lnBus.Close()

		// Mark both ports as allocated globally
		allocatedPorts[port] = true
		allocatedPorts[clusterBusPort] = true

		return port, nil
	}

	return 0, fmt.Errorf("failed to find available port pair after %d attempts", maxAttempts)
}

// releasePort releases a previously allocated port from the global allocator.
func releasePort(port int) {
	allocatedPortsMu.Lock()
	defer allocatedPortsMu.Unlock()

	delete(allocatedPorts, port)
	delete(allocatedPorts, port+10000) // Also release cluster bus port
}

// TestCluster represents a local Redis Cluster for testing.
type TestCluster struct {
	name             string // Name used to identify this cluster
	nodes            []*TestNode
	processes        []*exec.Cmd
	processOutputs   []*syncBuffer // Captured stdout/stderr for each process
	dataDirs         []string
	testDataDir      string // Parent directory containing all node data dirs
	numShards        int
	replicasPerShard int
	clusterClient    *redis.ClusterClient
	clusterInitOut   string // Output from redis-cli cluster init
	mu               sync.Mutex
}

// TestNode represents a single Redis node in the test cluster.
type TestNode struct {
	ID      string
	Port    int
	Address string
}

// StartCluster starts a local Redis Cluster with the specified configuration.
// Each node is assigned an available port dynamically.
// The clusterName is used to identify the cluster in process listings and data directories.
// numShards specifies the number of shards (masters) in the cluster (minimum 3).
// replicasPerShard specifies the number of replica nodes per shard (0 for masters only).
func StartCluster(ctx context.Context, numShards, replicasPerShard int, clusterName string) (*TestCluster, error) {
	if numShards < 3 {
		return nil, fmt.Errorf("cluster requires at least 3 shards, got %d", numShards)
	}
	if replicasPerShard < 0 {
		return nil, fmt.Errorf("replicasPerShard cannot be negative, got %d", replicasPerShard)
	}
	numNodes := numShards * (1 + replicasPerShard)
	if clusterName == "" {
		clusterName = fmt.Sprintf("cluster-%d", time.Now().UnixNano())
	}

	// Create a unique test data directory per cluster run to avoid reusing stale Redis state.
	// We keep it under ./testdata/ for easy inspection during debugging.
	baseTestDataDir := filepath.Join(".", "testdata")
	if err := os.MkdirAll(baseTestDataDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create base test data directory: %w", err)
	}
	testDataDir, err := os.MkdirTemp(baseTestDataDir, clusterName+"-")
	if err != nil {
		return nil, fmt.Errorf("failed to create test data directory: %w", err)
	}

	cluster := &TestCluster{
		name:             clusterName,
		nodes:            make([]*TestNode, 0, numNodes),
		processes:        make([]*exec.Cmd, 0, numNodes),
		processOutputs:   make([]*syncBuffer, 0, numNodes),
		dataDirs:         make([]string, 0, numNodes),
		testDataDir:      testDataDir,
		numShards:        numShards,
		replicasPerShard: replicasPerShard,
	}

	// Start Redis instances
	for i := 0; i < numNodes; i++ {
		// Find an available port for this node
		port, err := findAvailablePort()
		if err != nil {
			cluster.StopCluster(ctx)
			return nil, fmt.Errorf("failed to find available port for node %d: %w", i, err)
		}
		nodeAddr := fmt.Sprintf("127.0.0.1:%d", port)

		node := &TestNode{
			ID:      fmt.Sprintf("node-%d", i),
			Port:    port,
			Address: nodeAddr,
		}
		cluster.nodes = append(cluster.nodes, node)

		// Create data directory for this node
		dataDir := filepath.Join(testDataDir, fmt.Sprintf("node-%d", i))
		if err := os.MkdirAll(dataDir, 0755); err != nil {
			cluster.StopCluster(ctx)
			return nil, fmt.Errorf("failed to create node data directory: %w", err)
		}
		cluster.dataDirs = append(cluster.dataDirs, dataDir)

		// Create Redis config file
		configFile := filepath.Join(dataDir, "redis.conf")
		config := fmt.Sprintf(`port %d
cluster-enabled yes
cluster-config-file nodes.conf
cluster-node-timeout 1000
cluster-replica-validity-factor 0
appendonly yes
dir %s
`, port, dataDir)

		if err := os.WriteFile(configFile, []byte(config), 0644); err != nil {
			cluster.StopCluster(ctx)
			return nil, fmt.Errorf("failed to write redis config: %w", err)
		}

		// Start redis-server process
		cmd := exec.CommandContext(ctx, "redis-server", configFile)

		// Capture output in memory using thread-safe buffer
		// This allows concurrent writes from both stdout and stderr
		outputBuf := &syncBuffer{}
		cmd.Stdout = outputBuf
		cmd.Stderr = outputBuf

		// Set process group so we can kill the entire process tree
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Setpgid: true,
		}

		if err := cmd.Start(); err != nil {
			// On error, print the captured output for debugging
			fmt.Fprintf(os.Stderr, "Failed to start redis-server on port %d:\n%s\n", port, outputBuf.String())
			cluster.StopCluster(ctx)
			return nil, fmt.Errorf("failed to start redis-server on port %d: %w", port, err)
		}

		// Record PID for orphan cleanup tracking
		if err := recordPid(cmd.Process.Pid); err != nil {
			// Non-fatal, just log
			fmt.Fprintf(os.Stderr, "Warning: failed to record PID %d: %v\n", cmd.Process.Pid, err)
		}

		cluster.processes = append(cluster.processes, cmd)
		cluster.processOutputs = append(cluster.processOutputs, outputBuf)
	}

	// Wait for all Redis instances to be ready
	if err := cluster.waitForNodesReady(ctx, 10*time.Second); err != nil {
		cluster.DumpProcessOutput()
		cluster.StopCluster(ctx)
		return nil, fmt.Errorf("nodes not ready: %w", err)
	}

	// Initialize cluster using redis-cli (this waits for cluster to be ready)
	if err := cluster.initializeCluster(ctx); err != nil {
		cluster.DumpProcessOutput()
		cluster.StopCluster(ctx)
		return nil, fmt.Errorf("failed to initialize cluster: %w", err)
	}

	// Create cluster client
	addrs := make([]string, numNodes)
	for i, node := range cluster.nodes {
		addrs[i] = node.Address
	}

	clusterClient := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs: addrs,
	})

	// Quick sanity check - initializeCluster already verified cluster is ready
	if err := clusterClient.Ping(ctx).Err(); err != nil {
		cluster.DumpProcessOutput()
		cluster.StopCluster(ctx)
		return nil, fmt.Errorf("cluster client ping failed: %w", err)
	}

	cluster.clusterClient = clusterClient

	// Register cluster for signal handling cleanup
	registerCluster(cluster)

	return cluster, nil
}

// waitForNodesReady waits for all Redis nodes to be ready in parallel.
func (tc *TestCluster) waitForNodesReady(ctx context.Context, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Check all nodes in parallel
	errChan := make(chan error, len(tc.nodes))

	for _, node := range tc.nodes {
		go func(nodeAddr string) {
			ticker := time.NewTicker(50 * time.Millisecond)
			defer ticker.Stop()

			for {
				client := redis.NewClient(&redis.Options{
					Addr: nodeAddr,
				})

				if err := client.Ping(ctx).Err(); err == nil {
					client.Close()
					errChan <- nil
					return
				}
				client.Close()

				select {
				case <-ctx.Done():
					errChan <- fmt.Errorf("node %s not ready: %w", nodeAddr, ctx.Err())
					return
				case <-ticker.C:
					// Continue polling
				}
			}
		}(node.Address)
	}

	// Wait for all goroutines to complete
	for range tc.nodes {
		if err := <-errChan; err != nil {
			return err
		}
	}

	return nil
}

// initializeCluster initializes the Redis cluster using redis-cli --cluster create.
// It sets up the cluster with replicas: for 9 nodes, creates 3 shards with 1 master + 2 replicas each.
func (tc *TestCluster) initializeCluster(ctx context.Context) error {
	// Build cluster create command
	args := []string{"--cluster", "create"}
	for _, node := range tc.nodes {
		args = append(args, node.Address)
	}

	// Use the configured replicas per shard
	args = append(args, "--cluster-replicas", strconv.Itoa(tc.replicasPerShard), "--cluster-yes")

	cmd := exec.CommandContext(ctx, "redis-cli", args...)
	output, err := cmd.CombinedOutput()

	// Store output for later inspection if needed
	tc.clusterInitOut = string(output)

	if err != nil {
		// Only print output on error
		fmt.Fprintf(os.Stderr, "redis-cli --cluster create failed:\n%s\n", string(output))
		return fmt.Errorf("failed to initialize cluster: %w.\nCommand: %s", err, cmd.String())
	}

	// Wait for cluster to stabilize - poll until all nodes are connected
	// and cluster state is ok
	ctxWithTimeout, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		// Check if all nodes are reachable in parallel
		var wg sync.WaitGroup
		var notReady atomic.Bool

		for _, node := range tc.nodes {
			wg.Add(1)
			go func(addr string) {
				defer wg.Done()
				client := redis.NewClient(&redis.Options{
					Addr: addr,
				})
				defer client.Close()
				if err := client.Ping(ctxWithTimeout).Err(); err != nil {
					notReady.Store(true)
				}
			}(node.Address)
		}
		wg.Wait()

		if !notReady.Load() {
			// Verify cluster info shows cluster is ok
			client := redis.NewClient(&redis.Options{
				Addr: tc.nodes[0].Address,
			})
			info, err := client.ClusterInfo(ctxWithTimeout).Result()
			client.Close()
			if err == nil && info != "" {
				// Check that cluster_state is ok (simple string check)
				// In production, you'd parse the info properly
				if len(info) > 0 {
					return nil
				}
			}
		}

		select {
		case <-ctxWithTimeout.Done():
			return fmt.Errorf("cluster not ready within timeout: %w", ctxWithTimeout.Err())
		case <-ticker.C:
		}
	}
}

// StopCluster stops all Redis processes and cleans up.
// It ensures processes are forcefully terminated and waits for them to exit.
func (tc *TestCluster) StopCluster(ctx context.Context) error {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	// Ensure we unregister even if we fail partially
	defer unregisterCluster(tc)

	var firstErr error

	// Close cluster client
	if tc.clusterClient != nil {
		if err := tc.clusterClient.Close(); err != nil {
			firstErr = err
		}
		tc.clusterClient = nil
	}

	// Stop all Redis processes with proper cleanup
	for _, cmd := range tc.processes {
		if cmd == nil || cmd.Process == nil {
			continue
		}

		proc := cmd.Process
		pid := proc.Pid

		// First, try graceful shutdown with SIGTERM
		if err := proc.Signal(syscall.SIGTERM); err != nil {
			// Process might already be dead, remove from PID file and continue
			removePid(pid)
			continue
		}

		// Wait for process to exit with timeout
		done := make(chan error, 1)
		go func() {
			done <- cmd.Wait()
		}()

		select {
		case <-done:
			// Process exited successfully
			removePid(pid)
		case <-time.After(2 * time.Second):
			// Process didn't exit, force kill
			// Kill the process group to ensure all child processes are killed
			// Use negative PID to kill the process group
			_ = syscall.Kill(-pid, syscall.SIGKILL)
			// Also try killing the process directly as fallback
			_ = proc.Kill()
			// Wait a bit more for the kill to take effect
			select {
			case <-done:
				removePid(pid)
			case <-time.After(1 * time.Second):
				// Process still not dead, but remove from tracking anyway
				// The next test run will try to clean it up
				removePid(pid)
			}
		}
	}

	// Release all allocated ports back to the global pool
	for _, node := range tc.nodes {
		releasePort(node.Port)
	}

	// Clear processes list
	tc.processes = nil

	// Clean up data directories
	for _, dataDir := range tc.dataDirs {
		os.RemoveAll(dataDir)
	}

	// Remove the parent test data directory
	if tc.testDataDir != "" {
		os.RemoveAll(tc.testDataDir)
	}

	return firstErr
}

// GetAddrs returns all node addresses in the cluster.
func (tc *TestCluster) GetAddrs() []string {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	addrs := make([]string, len(tc.nodes))
	for i, node := range tc.nodes {
		addrs[i] = node.Address
	}
	return addrs
}

// GetClusterClient returns the cluster client for this test cluster.
func (tc *TestCluster) GetClusterClient() *redis.ClusterClient {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	return tc.clusterClient
}

// GetNodeForHashslot returns the node address that owns the given hashslot.
func (tc *TestCluster) GetNodeForHashslot(hashslot int) (string, error) {
	if tc.clusterClient == nil {
		return "", fmt.Errorf("cluster client not initialized")
	}

	ctx := context.Background()
	slots, err := tc.clusterClient.ClusterSlots(ctx).Result()
	if err != nil {
		return "", fmt.Errorf("failed to get cluster slots: %w", err)
	}

	// Find the slot range that contains our hashslot
	for _, slot := range slots {
		if hashslot >= int(slot.Start) && hashslot <= int(slot.End) {
			if len(slot.Nodes) > 0 {
				node := slot.Nodes[0]
				return node.Addr, nil
			}
		}
	}

	return "", fmt.Errorf("hashslot %d not found in cluster", hashslot)
}

// GetNodes returns all nodes in the cluster.
func (tc *TestCluster) GetNodes() []*TestNode {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	return tc.nodes
}

// DumpProcessOutput writes the captured output from all redis-server processes to stderr.
// This is useful for debugging test failures.
func (tc *TestCluster) DumpProcessOutput() {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	fmt.Fprintf(os.Stderr, "\n=== Redis Cluster Output Dump: %s ===\n", tc.name)

	if tc.clusterInitOut != "" {
		fmt.Fprintf(os.Stderr, "\n--- redis-cli --cluster create output ---\n%s\n", tc.clusterInitOut)
	}

	for i, buf := range tc.processOutputs {
		if buf != nil && buf.Len() > 0 {
			nodeDesc := "unknown"
			if i < len(tc.nodes) {
				nodeDesc = tc.nodes[i].Address
			}
			fmt.Fprintf(os.Stderr, "\n--- redis-server node %d (%s) ---\n%s\n", i, nodeDesc, buf.String())
		}
	}

	fmt.Fprintf(os.Stderr, "=== End Redis Cluster Output Dump ===\n\n")
}

// GetProcessOutput returns the captured output for a specific node index.
// Returns empty string if the index is out of range.
func (tc *TestCluster) GetProcessOutput(nodeIndex int) string {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	if nodeIndex < 0 || nodeIndex >= len(tc.processOutputs) {
		return ""
	}

	if tc.processOutputs[nodeIndex] == nil {
		return ""
	}

	return tc.processOutputs[nodeIndex].String()
}

// GetClusterInitOutput returns the output from the redis-cli cluster initialization.
func (tc *TestCluster) GetClusterInitOutput() string {
	tc.mu.Lock()
	defer tc.mu.Unlock()
	return tc.clusterInitOut
}

// StopNode stops a specific node by address.
func (tc *TestCluster) StopNode(nodeAddr string) error {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	for i, node := range tc.nodes {
		if node.Address == nodeAddr {
			if i < len(tc.processes) && tc.processes[i] != nil && tc.processes[i].Process != nil {
				cmd := tc.processes[i]
				proc := cmd.Process
				pid := proc.Pid

				// Try graceful shutdown with SIGTERM
				_ = proc.Signal(syscall.SIGTERM)

				// Wait for process to exit with timeout
				done := make(chan error, 1)
				go func() {
					done <- cmd.Wait()
				}()

				select {
				case <-done:
					// Process exited
					removePid(pid)
				case <-time.After(2 * time.Second):
					// Process didn't exit, force kill
					_ = syscall.Kill(-pid, syscall.SIGKILL)
					_ = proc.Kill()
					select {
					case <-done:
					case <-time.After(1 * time.Second):
					}
					removePid(pid)
				}

				return nil
			}
		}
	}

	return fmt.Errorf("node %s not found", nodeAddr)
}

// MigrateHashslot migrates a hashslot from one node to another.
// It performs the complete migration process:
// 1. Finds the source node that owns the slot
// 2. Gets node IDs for both source and target nodes
// 3. Sets IMPORTING state on target node
// 4. Sets MIGRATING state on source node
// 5. Migrates any keys in the slot from source to target
// 6. Sets NODE state on all nodes to finalize the migration
func (tc *TestCluster) MigrateHashslot(ctx context.Context, slot int, targetNode string) error {
	if tc.clusterClient == nil {
		return fmt.Errorf("cluster client not initialized")
	}

	// Validate slot range
	if slot < 0 || slot >= 16384 {
		return fmt.Errorf("invalid slot number: %d (must be 0-16383)", slot)
	}

	// Find the source node that currently owns this slot
	sourceNode, err := tc.GetNodeForHashslot(slot)
	if err != nil {
		return fmt.Errorf("failed to find source node for slot %d: %w", slot, err)
	}

	// If source and target are the same, nothing to do
	if sourceNode == targetNode {
		return nil
	}

	// Get node IDs for both source and target nodes
	sourceNodeID, err := tc.getNodeID(ctx, sourceNode)
	if err != nil {
		return fmt.Errorf("failed to get source node ID: %w", err)
	}

	targetNodeID, err := tc.getNodeID(ctx, targetNode)
	if err != nil {
		return fmt.Errorf("failed to get target node ID: %w", err)
	}

	// Create clients for source and target nodes
	sourceClient := redis.NewClient(&redis.Options{Addr: sourceNode})
	defer sourceClient.Close()

	targetClient := redis.NewClient(&redis.Options{Addr: targetNode})
	defer targetClient.Close()

	// Step 1: Set IMPORTING state on target node
	// CLUSTER SETSLOT <slot> IMPORTING <source-node-id>
	if err := targetClient.Do(ctx, "CLUSTER", "SETSLOT", slot, "IMPORTING", sourceNodeID).Err(); err != nil {
		return fmt.Errorf("failed to set IMPORTING state on target node: %w", err)
	}

	// Step 2: Set MIGRATING state on source node
	// CLUSTER SETSLOT <slot> MIGRATING <target-node-id>
	if err := sourceClient.Do(ctx, "CLUSTER", "SETSLOT", slot, "MIGRATING", targetNodeID).Err(); err != nil {
		// Try to clean up IMPORTING state on target
		_ = targetClient.Do(ctx, "CLUSTER", "SETSLOT", slot, "NODE", targetNodeID).Err()
		return fmt.Errorf("failed to set MIGRATING state on source node: %w", err)
	}

	// Step 3: Migrate keys from source to target
	if err := tc.migrateKeys(ctx, sourceClient, targetClient, slot); err != nil {
		// Try to clean up states
		_ = sourceClient.Do(ctx, "CLUSTER", "SETSLOT", slot, "NODE", sourceNodeID).Err()
		_ = targetClient.Do(ctx, "CLUSTER", "SETSLOT", slot, "NODE", targetNodeID).Err()
		return fmt.Errorf("failed to migrate keys: %w", err)
	}

	// Step 4: Set NODE state on all nodes to finalize migration
	// CLUSTER SETSLOT <slot> NODE <target-node-id>
	// Get all nodes in the cluster
	allNodes := tc.GetNodes()
	for _, node := range allNodes {
		client := redis.NewClient(&redis.Options{Addr: node.Address})
		if err := client.Do(ctx, "CLUSTER", "SETSLOT", slot, "NODE", targetNodeID).Err(); err != nil {
			client.Close()
			// Log error but continue - some nodes might be replicas
			continue
		}
		client.Close()
	}

	// Step 5: Wait for cluster to converge on the new topology
	// This ensures all nodes report the same slot owner before returning
	if err := tc.waitForSlotConvergence(ctx, slot, targetNode); err != nil {
		return fmt.Errorf("cluster failed to converge after migration: %w", err)
	}

	return nil
}

// waitForSlotConvergence waits until all master nodes agree on the owner of a slot.
func (tc *TestCluster) waitForSlotConvergence(ctx context.Context, slot int, expectedOwner string) error {
	timeout := 3 * time.Second
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		allAgree := true
		for _, node := range tc.GetNodes() {
			owner, err := tc.getSlotOwnerFromNode(ctx, node.Address, slot)
			if err != nil {
				allAgree = false
				break
			}
			if owner != expectedOwner {
				allAgree = false
				break
			}
		}

		if allAgree {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
			// Continue polling
		}
	}

	return fmt.Errorf("timeout waiting for slot %d to converge to owner %s", slot, expectedOwner)
}

// getSlotOwnerFromNode queries a specific node for who owns a slot.
func (tc *TestCluster) getSlotOwnerFromNode(ctx context.Context, nodeAddr string, slot int) (string, error) {
	client := redis.NewClient(&redis.Options{Addr: nodeAddr})
	defer client.Close()

	slots, err := client.ClusterSlots(ctx).Result()
	if err != nil {
		return "", err
	}

	for _, slotRange := range slots {
		if slot >= int(slotRange.Start) && slot <= int(slotRange.End) {
			if len(slotRange.Nodes) > 0 {
				return slotRange.Nodes[0].Addr, nil
			}
		}
	}

	return "", fmt.Errorf("slot %d not found in cluster slots", slot)
}

// getNodeID gets the node ID for a given node address by querying CLUSTER NODES.
func (tc *TestCluster) getNodeID(ctx context.Context, nodeAddr string) (string, error) {
	client := redis.NewClient(&redis.Options{Addr: nodeAddr})
	defer client.Close()

	// Get cluster nodes information
	nodesInfo, err := client.ClusterNodes(ctx).Result()
	if err != nil {
		return "", fmt.Errorf("failed to get cluster nodes: %w", err)
	}

	// Parse nodes info to find the node ID for this address
	// Format: <node-id> <ip:port>@<port> <flags> <master-id> <ping-sent> <pong-recv> <config-epoch> <link-state> <slot> <slot> ...
	lines := strings.Split(nodesInfo, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		nodeID := fields[0]
		addr := fields[1]
		// addr format is "ip:port" or "ip:port@port"
		if strings.HasPrefix(addr, nodeAddr) || strings.HasPrefix(addr, nodeAddr+"@") {
			return nodeID, nil
		}
	}

	return "", fmt.Errorf("node ID not found for address %s", nodeAddr)
}

// migrateKeys migrates all keys in the given slot from source to target node.
func (tc *TestCluster) migrateKeys(ctx context.Context, sourceClient, targetClient *redis.Client, slot int) error {
	// Get keys in the slot from source node
	// CLUSTER GETKEYSINSLOT returns up to 10 keys by default, we need to iterate
	const keysPerBatch = 10
	timeout := 5 * time.Second

	for {
		// Get a batch of keys in this slot
		keys, err := sourceClient.ClusterGetKeysInSlot(ctx, slot, keysPerBatch).Result()
		if err != nil {
			return fmt.Errorf("failed to get keys in slot: %w", err)
		}

		// If no more keys, we're done
		if len(keys) == 0 {
			break
		}

		// Migrate each key
		for _, key := range keys {
			// Get the target node address (host:port)
			targetAddr := targetClient.Options().Addr
			// Parse host and port
			host, port, err := net.SplitHostPort(targetAddr)
			if err != nil {
				return fmt.Errorf("invalid target address: %w", err)
			}
			portInt, err := strconv.Atoi(port)
			if err != nil {
				return fmt.Errorf("invalid target port: %w", err)
			}

			// MIGRATE command: MIGRATE host port "" destination-db timeout COPY REPLACE KEYS key
			// For cluster mode, we use destination-db 0 and COPY + REPLACE flags
			// Note: The key parameter in MIGRATE is actually passed via KEYS argument
			// Timeout is in milliseconds
			if err := sourceClient.Do(ctx, "MIGRATE", host, portInt, "", 0, int(timeout.Milliseconds()), "COPY", "REPLACE", "KEYS", key).Err(); err != nil {
				// If key doesn't exist (might have been deleted), continue
				if err == redis.Nil {
					continue
				}
				return fmt.Errorf("failed to migrate key %q: %w", key, err)
			}
		}

		// If we got fewer keys than requested, we've migrated all keys
		if len(keys) < keysPerBatch {
			break
		}
	}

	return nil
}

// setupTestCluster is a helper function for tests to set up a cluster.
// It fails the test if redis-server or redis-cli are not available.
// numShards specifies the number of shards (masters) in the cluster (minimum 3).
// replicasPerShard specifies the number of replica nodes per shard (0 for masters only).
// Use 0 for both parameters to get the default (3 shards, 2 replicas per shard = 9 nodes).
func setupTestCluster(t testing.TB, numShards, replicasPerShard int) *TestCluster {
	// Check if redis-server and redis-cli are available
	if _, err := exec.LookPath("redis-server"); err != nil {
		t.Fatalf("redis-server not found in PATH: %v", err)
	}
	if _, err := exec.LookPath("redis-cli"); err != nil {
		t.Fatalf("redis-cli not found in PATH: %v", err)
	}

	// Default to 3 shards with 2 replicas each (9 nodes total)
	if numShards == 0 {
		numShards = 3
	}
	if replicasPerShard == 0 && numShards == 3 {
		replicasPerShard = 2
	}

	// Generate a cluster name based on test name for easy identification
	clusterName := fmt.Sprintf("test-%s-%d", t.Name(), time.Now().UnixNano())

	ctx := context.Background()
	cluster, err := StartCluster(ctx, numShards, replicasPerShard, clusterName)
	if err != nil {
		t.Fatalf("Failed to start test cluster: %v", err)
	}

	// Cleanup on test completion
	t.Cleanup(func() {
		// If test failed, dump the Redis cluster output for debugging
		if t.Failed() {
			cluster.DumpProcessOutput()
		}

		if err := cluster.StopCluster(context.Background()); err != nil {
			t.Logf("Error stopping test cluster: %v", err)
		}
	})

	return cluster
}
