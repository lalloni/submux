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

// TestUnsubscribe_AfterNodeFailure_DoesNotBlock verifies that Sub.Unsubscribe()
// returns promptly when the underlying event loop has already stopped due to a
// node failure, rather than blocking indefinitely.
//
// Strategy: subscribe to two channels on the same hashslot (same event loop).
// Stop the node, then unsubscribe the first — this triggers the event loop to
// crash (command fails). Then unsubscribe the second — the event loop is already
// dead, and the command sits in the buffer with nobody reading it.
// Before the fix: the second Unsubscribe blocks until the context deadline.
// After the fix: it returns immediately with an error.
func TestUnsubscribe_AfterNodeFailure_DoesNotBlock(t *testing.T) {
	t.Parallel()
	cluster := setupTestCluster(t, 3, 2)
	client := cluster.GetClusterClient()

	// No auto-resubscribe: we want the event loop to stay dead.
	subMux, err := submux.New(client,
		submux.WithTopologyPollInterval(100*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("Failed to create SubMux: %v", err)
	}
	defer subMux.Close()

	// Use Redis hashtag syntax so both channels map to the same hashslot
	// (and therefore the same node and event loop).
	tag := uniqueChannel("unsub-block")
	channelA := "{" + tag + "}a"
	channelB := "{" + tag + "}b"

	messagesA := make(chan string, 10)
	subA, err := subMux.SubscribeSync(context.Background(), []string{channelA}, func(ctx context.Context, msg *submux.Message) {
		if msg.Type == submux.MessageTypeMessage {
			messagesA <- msg.Payload
		}
	})
	if err != nil {
		t.Fatalf("Failed to subscribe channelA: %v", err)
	}

	messagesB := make(chan string, 10)
	subB, err := subMux.SubscribeSync(context.Background(), []string{channelB}, func(ctx context.Context, msg *submux.Message) {
		if msg.Type == submux.MessageTypeMessage {
			messagesB <- msg.Payload
		}
	})
	if err != nil {
		t.Fatalf("Failed to subscribe channelB: %v", err)
	}

	// Verify connectivity on both channels
	for _, ch := range []struct {
		name string
		msgs chan string
	}{{channelA, messagesA}, {channelB, messagesB}} {
		err = client.Publish(context.Background(), ch.name, "hello").Err()
		if err != nil {
			t.Fatalf("Failed to publish to %s: %v", ch.name, err)
		}
		select {
		case <-ch.msgs:
		case <-time.After(5 * time.Second):
			t.Fatalf("Timeout waiting for message on %s", ch.name)
		}
	}

	// Stop the master node for the shared hashslot
	hashslot := submux.Hashslot(channelA)
	masterNode, err := cluster.GetNodeForHashslot(hashslot)
	if err != nil {
		t.Fatalf("Failed to get master for hashslot %d: %v", hashslot, err)
	}
	t.Logf("Stopping master node %s (hashslot %d)", masterNode, hashslot)
	err = cluster.StopNode(masterNode)
	if err != nil {
		t.Fatalf("Failed to stop node: %v", err)
	}

	// Wait briefly for the node to be fully down
	time.Sleep(500 * time.Millisecond)

	// Unsubscribe A — this sends an UNSUBSCRIBE command to the event loop.
	// The event loop tries to send it to the dead Redis node, gets an error,
	// sends the error back on cmd.response, then sets connStateFailed and exits.
	t.Log("Unsubscribing channelA (triggers event loop crash)...")
	errA := subA.Unsubscribe(context.Background())
	t.Logf("channelA unsubscribe returned: %v", errA)

	// Give the event loop goroutine time to fully exit after processing the command
	time.Sleep(500 * time.Millisecond)

	// Unsubscribe B — the event loop is now dead. sendCommand succeeds (cmdCh has
	// buffer space, meta.done is not closed), but nobody reads from cmdCh.
	// Without the fix: blocks until ctx deadline (3s).
	// With the fix: returns immediately.
	t.Log("Unsubscribing channelB (event loop already dead)...")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	start := time.Now()
	errB := subB.Unsubscribe(ctx)
	elapsed := time.Since(start)

	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("BUG: Unsubscribe blocked for %v and hit deadline — event loop is dead but Unsubscribe did not return", elapsed)
	}

	t.Logf("channelB unsubscribe completed in %v (err: %v)", elapsed, errB)

	// After the fix, this should complete in well under 1 second
	if elapsed > 2*time.Second {
		t.Errorf("Unsubscribe took %v — too slow, likely hitting internal timeout instead of immediate detection", elapsed)
	}
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

	_, err = subMux.SubscribeSync(context.Background(), []string{channelName}, func(ctx context.Context, msg *submux.Message) {
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

	// Wait for cluster to detect replica failure and rebalance
	// Note: replica failure should NOT make cluster unhealthy (only reduce redundancy)
	// So we just wait a brief moment for the cluster to detect the disconnection
	t.Log("Waiting for cluster to detect replica failure...")
	time.Sleep(150 * time.Millisecond)

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
		case <-time.After(200 * time.Millisecond):
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

	_, err = subMux.SubscribeSync(context.Background(), []string{channelName}, func(ctx context.Context, msg *submux.Message) {
		if msg.Type == submux.MessageTypeMessage {
			messages <- msg
		}
	})
	if err != nil {
		t.Fatalf("Failed to subscribe: %v", err)
	}

	// Ensure cluster is healthy before chaos testing
	err = waitForClusterHealthy(t, client, 5*time.Second)
	if err != nil {
		t.Fatalf("Cluster not healthy before rolling restart test: %v", err)
	}

	// Ensure replicas are available (so cluster can tolerate restarts)
	err = waitForReplicasReady(t, client, 1, 5*time.Second)
	if err != nil {
		t.Fatalf("Replicas not ready before rolling restart test: %v", err)
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
			time.Sleep(time.Duration(200+rnd.Intn(300)) * time.Millisecond)

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
			time.Sleep(500 * time.Millisecond)

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
