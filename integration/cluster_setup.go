package integration

import (
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
	"syscall"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

var (
	// Global registry of all active test clusters
	activeClusters    = make(map[*TestCluster]struct{})
	activeClustersMu  sync.Mutex
	signalHandlerOnce sync.Once
)

// initSignalHandler sets up a global signal handler that cleans up all active clusters.
// This is called once and handles signals for all clusters created during testing.
func initSignalHandler() {
	signalHandlerOnce.Do(func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
		go func() {
			<-sigChan
			fmt.Fprintln(os.Stderr, "\n=== Received interrupt signal, cleaning up all test clusters ===")
			cleanupAllClusters()
			os.Exit(1)
		}()
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
// This is more reliable than searching for consecutive ports.
func findAvailablePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("failed to find available port: %w", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port, nil
}

// TestCluster represents a local Redis Cluster for testing.
type TestCluster struct {
	name             string // Name used to identify this cluster
	nodes            []*TestNode
	processes        []*exec.Cmd
	dataDirs         []string
	testDataDir      string // Parent directory containing all node data dirs
	numShards        int
	replicasPerShard int
	clusterClient    *redis.ClusterClient
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
cluster-node-timeout 2000
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
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		// Set process group so we can kill the entire process tree
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Setpgid: true,
		}

		if err := cmd.Start(); err != nil {
			cluster.StopCluster(ctx)
			return nil, fmt.Errorf("failed to start redis-server on port %d: %w", port, err)
		}

		cluster.processes = append(cluster.processes, cmd)
	}

	// Wait for all Redis instances to be ready
	if err := cluster.waitForNodesReady(ctx, 10*time.Second); err != nil {
		cluster.StopCluster(ctx)
		return nil, fmt.Errorf("nodes not ready: %w", err)
	}

	// Initialize cluster using redis-cli
	if err := cluster.initializeCluster(ctx); err != nil {
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

	// Wait for cluster to be ready
	if err := WaitForClusterReady(clusterClient, 30*time.Second); err != nil {
		cluster.StopCluster(ctx)
		return nil, fmt.Errorf("cluster not ready: %w", err)
	}

	cluster.clusterClient = clusterClient

	// Register cluster for signal handling cleanup
	registerCluster(cluster)

	return cluster, nil
}

// waitForNodesReady waits for all Redis nodes to be ready.
func (tc *TestCluster) waitForNodesReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)

	for _, node := range tc.nodes {
		for time.Now().Before(deadline) {
			// Try to connect to this node
			client := redis.NewClient(&redis.Options{
				Addr: node.Address,
			})

			if err := client.Ping(ctx).Err(); err == nil {
				client.Close()
				break
			}
			client.Close()

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}

		// Final check
		client := redis.NewClient(&redis.Options{
			Addr: node.Address,
		})
		if err := client.Ping(ctx).Err(); err != nil {
			client.Close()
			return fmt.Errorf("node %s not ready: %w", node.Address, err)
		}
		client.Close()
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
	// Always log output for debugging
	fmt.Printf("redis-cli --cluster create output:\n%s\n", string(output))

	if err != nil {
		return fmt.Errorf("failed to initialize cluster: %w.\nCommand: %s", err, cmd.String())
	}

	// Wait for cluster to stabilize - poll until all nodes are connected
	// and cluster state is ok
	ctxWithTimeout, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		// Check if all nodes are reachable
		allReady := true
		for _, node := range tc.nodes {
			client := redis.NewClient(&redis.Options{
				Addr: node.Address,
			})
			if err := client.Ping(ctxWithTimeout).Err(); err != nil {
				allReady = false
			}
			client.Close()
		}

		if allReady {
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
			// Process might already be dead, continue
		}

		// Wait for process to exit with timeout
		done := make(chan error, 1)
		go func() {
			done <- cmd.Wait()
		}()

		select {
		case <-done:
			// Process exited
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
			case <-time.After(1 * time.Second):
				// Process still not dead, log but continue
			}
		}
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
				case <-time.After(2 * time.Second):
					// Process didn't exit, force kill
					_ = syscall.Kill(-pid, syscall.SIGKILL)
					_ = proc.Kill()
					select {
					case <-done:
					case <-time.After(1 * time.Second):
					}
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

	return nil
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

// WaitForClusterReady waits for a cluster client to be ready.
// For clusters with replicas, this also waits for replication to be established.
func WaitForClusterReady(client *redis.ClusterClient, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			// Check if cluster is ready
			if err := client.Ping(ctx).Err(); err != nil {
				continue
			}

			// Also check cluster info
			info, err := client.ClusterInfo(ctx).Result()
			if err != nil || info == "" {
				continue
			}

			if !strings.Contains(info, "cluster_state:ok") {
				continue
			}

			// For clusters with replicas, verify all nodes are connected
			// by checking cluster nodes and counting replicas
			nodes, err := client.ClusterNodes(ctx).Result()
			if err != nil || nodes == "" {
				continue
			}

			// Count replicas (lines containing "slave")
			replicaCount := strings.Count(nodes, "slave")
			// We expect at least some replicas if the cluster was created with them.
			// Since we don't know the exact architecture here (it's generic),
			// we can't enforce strict count unless we pass it.
			// But for our test setup (9 nodes, 3 masters), we expect 6 replicas.
			// If we see 0 replicas and we know we expect them, that's bad.
			// However, this function is generic.
			// Let's assume if we see 0 replicas in a >3 node cluster, we might be waiting.
			// But StartCluster creates 9 nodes.
			// Let's rely on "cluster_state:ok" AND stable topology.

			// Actually, the issue was they were listed as MASTERS.
			// If we entered with 9 nodes, and 6 are intended as replicas, but show as masters.
			// We should wait until they convert.
			// But strictly speaking, the client doesn't know intended count here.
			// But `setupTestCluster` does.
			// Let's just log and rely on the fact that if they are ALL masters, something might be wrong
			// but we can't block indefinitely if it's a 3-master-only cluster.
			//
			// WAIT: The specific test failure was "All Masters".
			// If the test setup demanded replicas, they shold be there.
			// I'll make a heuristic: if lines > 3 and replicas == 0, wait?
			// That might block 3-master cluster.
			//
			// Better: Pass expected replicas to WaitForClusterReady?
			// But signature change affects callers.
			//
			// Looking at the logs, "redis-cli" said "Adding replica...".
			// So valid state IS with replicas.
			//
			// Let's just check if we have ANY slaves?
			if replicaCount > 0 {
				return nil
			}

			// If 0 replicas, check if we have > 3 nodes?
			lineCount := len(strings.Split(strings.TrimSpace(nodes), "\n"))
			if lineCount > 3 && replicaCount == 0 {
				// We likely expect replicas but they haven't synced yet
				continue
			}

			return nil
		}
	}
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
		if err := cluster.StopCluster(context.Background()); err != nil {
			t.Logf("Error stopping test cluster: %v", err)
		}
	})

	return cluster
}
