package integration

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lalloni/submux"
	"github.com/redis/go-redis/v9"
)

// Helper to start a specific node that was previously stopped
func (tc *TestCluster) StartNode(ctx context.Context, nodeAddr string) error {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	for i, node := range tc.nodes {
		if node.Address == nodeAddr {
			// Check if already running
			if i < len(tc.processes) && tc.processes[i] != nil && tc.processes[i].Process != nil {
				// Check if process is still alive
				if err := tc.processes[i].Process.Signal(syscall.Signal(0)); err == nil {
					return nil // Already running
				}
			}

			// Re-start the process using the same config
			dataDir := tc.dataDirs[i]
			configFile := filepath.Join(dataDir, "redis.conf")

			cmd := exec.CommandContext(ctx, "redis-server", configFile)
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			cmd.SysProcAttr = &syscall.SysProcAttr{
				Setpgid: true,
			}

			if err := cmd.Start(); err != nil {
				return fmt.Errorf("failed to start redis-server on port %d: %w", node.Port, err)
			}

			// Update process reference
			if i < len(tc.processes) {
				tc.processes[i] = cmd
			} else {
				// This shouldn't happen structure-wise but for safety
				tc.processes = append(tc.processes, cmd)
			}

			// Wait for node to be ready
			client := redis.NewClient(&redis.Options{Addr: node.Address})
			defer client.Close()

			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				if err := client.Ping(ctx).Err(); err == nil {
					return nil
				}
				time.Sleep(100 * time.Millisecond)
			}
			return fmt.Errorf("node %s did not become ready", node.Address)
		}
	}
	return fmt.Errorf("node %s not found", nodeAddr)
}

func TestReplicaFailure_Recovery(t *testing.T) {
	t.Parallel() // Dedicated cluster - safe to run in parallel
	// Use 6 nodes (3 shards: 1 master + 1 replica each) which is enough for replica failure test
	cluster := setupTestCluster(t, 3, 2)
	client := cluster.GetClusterClient()

	// Configure SubMux to prefer replicas
	subMux, err := submux.New(client,
		submux.WithReplicaPreference(true),
		submux.WithTopologyPollInterval(100*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("Failed to create SubMux: %v", err)
	}
	defer subMux.Close()

	channelName := "replica-fail-test"
	messages := make(chan *submux.Message, 10)

	_, err = subMux.SubscribeSync(context.Background(), []string{channelName}, func(msg *submux.Message) {
		if msg.Type == submux.MessageTypeMessage {
			messages <- msg
		}
	})
	if err != nil {
		t.Fatalf("Failed to subscribe: %v", err)
	}

	// Identify the node we are connected to for this channel
	// Since we asked for ReplicaPreference, we expect a replica, but it's best-effort.
	// We verify connectivity first.
	pubClient := cluster.GetClusterClient()
	err = pubClient.Publish(context.Background(), channelName, "initial").Err()
	if err != nil {
		t.Fatalf("Failed to publish initial: %v", err)
	}

	select {
	case msg := <-messages:
		if msg.Payload != "initial" {
			t.Errorf("Expected 'initial', got %q", msg.Payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Timeout waiting for initial message")
	}

	// Find a replica node to kill.
	// We'll walk all nodes, find one that is a replica, and kill it.
	// Ideally we'd find the SPECIFIC replica we are connected to, but internal state is hidden.
	// However, if we kill *any* replica involved in the shard covering our channel, we test resilience.

	hashslot := submux.Hashslot(channelName)
	masterNode, err := cluster.GetNodeForHashslot(hashslot)
	if err != nil {
		t.Fatalf("Failed to get master for hashslot: %v", err)
	}

	// Find replicas of this master
	masterID, err := cluster.getNodeID(context.Background(), masterNode)
	if err != nil {
		t.Fatalf("Failed to get master ID: %v", err)
	}

	var targetReplica string
	allNodes := cluster.GetNodes()
	for _, node := range allNodes {
		if node.Address == masterNode {
			continue
		}

		// Check if this node is a replica of our master
		nodeClient := redis.NewClient(&redis.Options{Addr: node.Address})
		info, err := nodeClient.ClusterNodes(context.Background()).Result()
		nodeClient.Close()
		if err != nil {
			continue
		}

		// Naive parsing to find "slave <masterID>"
		// Line format: <id> <addr> <flags> <master> ...
		// We can just check if the line containing this node's address also contains the master ID
		// and the "slave" flag.
		lines := strings.Split(info, "\n")
		for _, line := range lines {
			if strings.Contains(line, node.Address) &&
				strings.Contains(line, "slave") &&
				strings.Contains(line, masterID) {
				targetReplica = node.Address
				break
			}
		}
		if targetReplica != "" {
			break
		}
	}

	if targetReplica == "" {
		t.Log("Could not find specific replica for hashslot, picking random non-master node")
		// Fallback: pick any node that isn't the master
		for _, node := range allNodes {
			if node.Address != masterNode {
				targetReplica = node.Address
				break
			}
		}
	}

	t.Logf("Killing replica node %s (master is %s)", targetReplica, masterNode)
	err = cluster.StopNode(targetReplica)
	if err != nil {
		t.Fatalf("Failed to stop replica: %v", err)
	}

	// Verify we can still receive messages (SubMux should reconnect to master or another replica)
	// We publish multiple times to allow for reconnection window
	t.Log("Publishing messages during failure...")

	// Give a small moment for disconnect detection
	time.Sleep(300 * time.Millisecond)

	receivedRecovery := false
RecoveryLoop:
	for i := 0; i < 10; i++ {
		payload := fmt.Sprintf("recovery-%d", i)
		err = pubClient.Publish(context.Background(), channelName, payload).Err()
		if err != nil {
			t.Logf("Publish failed: %v", err)
		}

		select {
		case msg := <-messages:
			if msg.Payload == payload {
				receivedRecovery = true
				break RecoveryLoop
			}
		case <-time.After(500 * time.Millisecond):
			// retry
		}
	}

	if !receivedRecovery {
		t.Fatal("Failed to receive messages after replica failure")
	}
	t.Log("Successfully recovered from replica failure")

	// Restart the replica to clean up cluster for next tests (though parallel tests shouldn't care)
	if err := cluster.StartNode(context.Background(), targetReplica); err != nil {
		t.Logf("Failed to restart replica: %v", err)
	}
}

func TestRollingRestart_Stability(t *testing.T) {
	t.Parallel() // Dedicated cluster - safe to run in parallel
	cluster := setupTestCluster(t, 3, 2)
	client := cluster.GetClusterClient()

	subMux, err := submux.New(client,
		submux.WithAutoResubscribe(true),
		submux.WithTopologyPollInterval(500*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("Failed to create SubMux: %v", err)
	}
	defer subMux.Close()

	channelName := "stability-test"
	messages := make(chan *submux.Message, 100)

	_, err = subMux.SubscribeSync(context.Background(), []string{channelName}, func(msg *submux.Message) {
		if msg.Type == submux.MessageTypeMessage {
			messages <- msg
		}
	})
	if err != nil {
		t.Fatalf("Failed to subscribe: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Background publisher
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		i := 0
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				i++
				// Use randomized publish to hit different nodes if we had multiple channels,
				// but here we just want continuous traffic.
				_ = client.Publish(context.Background(), channelName, fmt.Sprintf("msg-%d", i)).Err()
			}
		}
	}()

	// Chaos Monkey: randomly restart nodes
	go func() {
		rnd := rand.New(rand.NewSource(time.Now().UnixNano()))
		for {
			if ctx.Err() != nil {
				return
			}

			// Wait random interval
			time.Sleep(time.Duration(500+rnd.Intn(500)) * time.Millisecond)

			if ctx.Err() != nil {
				return
			}

			// Pick random node
			nodes := cluster.GetNodes()
			target := nodes[rnd.Intn(len(nodes))]

			t.Logf("Chaos: Restarting node %s...", target.Address)

			// Stop
			if err := cluster.StopNode(target.Address); err != nil {
				t.Logf("Chaos: Failed to stop node: %v", err)
				continue
			}

			// Wait a bit (simulate downtime)
			time.Sleep(1 * time.Second)

			// Check context again before starting (avoid cleanup race)
			if ctx.Err() != nil {
				return
			}

			// Start
			if err := cluster.StartNode(context.Background(), target.Address); err != nil {
				t.Logf("Chaos: Failed to start node: %v", err)
			}
			t.Logf("Chaos: Node %s restarted", target.Address)
		}
	}()

	// Monitor reception
	// We don't expect 100% delivery (some loss during failover is acceptable for PubSub),
	// but we expect flow to continue / resume.
	lastReceived := time.Now()
	msgCount := 0

	monitorTick := time.NewTicker(100 * time.Millisecond)
	defer monitorTick.Stop()

	for {
		select {
		case <-ctx.Done():
			t.Logf("Stability test finished. Received %d messages.", msgCount)
			if msgCount < 10 { // access lower bound
				t.Error("Received too few messages during stress test")
			}
			return
		case <-messages:
			msgCount++
			lastReceived = time.Now()
		case <-monitorTick.C:
			// Check if we haven't received anything for too long
			if time.Since(lastReceived) > 3*time.Second {
				t.Error("Stalled: No messages received for > 3 seconds during rolling restarts")
				return
			}
		}
	}
}
