package integration

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lalloni/submux"
	"github.com/redis/go-redis/v9"
)

// TestHashslotMigration verifies that we can detect hashslot migration signals.
// Use auto-resubscribe = false to verify the signal mechanism in isolation.
func TestHashslotMigration(t *testing.T) {
	t.Parallel()
	// Use isolated cluster to avoid interfering with other tests
	cluster := setupTestCluster(t, 6)
	client := cluster.GetClusterClient()

	subMux, err := submux.New(client,
		submux.WithAutoResubscribe(false),
		submux.WithTopologyPollInterval(100*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("Failed to create SubMux: %v", err)
	}
	defer subMux.Close()

	// Subscribe to a channel using Sharded PubSub (SSubscribe)
	// This ensures the subscription is tied to the specific shard/node
	messages := make(chan *submux.Message, 10)
	signalMessages := make(chan *submux.Message, 10)

	channelName := "migrate-test"
	_, err = subMux.SSubscribeSync(context.Background(), []string{channelName}, func(msg *submux.Message) {
		if msg.Type == submux.MessageTypeSignal {
			signalMessages <- msg
		} else {
			messages <- msg
		}
	})
	if err != nil {
		t.Fatalf("Failed to subscribe: %v", err)
	}

	// Get the hashslot for this channel
	hashslot := submux.Hashslot(channelName)
	t.Logf("Channel %q maps to hashslot %d", channelName, hashslot)

	// Find which node owns this hashslot
	sourceNode, err := cluster.GetNodeForHashslot(hashslot)
	if err != nil {
		t.Fatalf("Failed to get node for hashslot: %v", err)
	}
	t.Logf("Hashslot %d is currently owned by node %s", hashslot, sourceNode)

	// Pick a target node (different from source)
	var targetNode string
	for _, node := range cluster.GetNodes() {
		if node.Address != sourceNode {
			targetNode = node.Address
			break
		}
	}
	if targetNode == "" {
		t.Fatal("Could not find a different target node for migration")
	}

	// Publish a message before migration to verify connectivity
	pubClient := cluster.GetClusterClient()
	err = pubClient.SPublish(context.Background(), channelName, "before-migration").Err()
	if err != nil {
		t.Logf("SPublish failed: %v, trying Publish", err)
		err = pubClient.Publish(context.Background(), channelName, "before-migration").Err()
	}
	if err != nil {
		t.Fatalf("Failed to publish: %v", err)
	}

	// Wait for message
	select {
	case msg := <-messages:
		if msg.Payload != "before-migration" {
			t.Errorf("Expected 'before-migration', got %q", msg.Payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for message before migration")
	}

	// Perform actual migration
	t.Logf("Migrating hashslot %d from %s to %s", hashslot, sourceNode, targetNode)
	err = cluster.MigrateHashslot(context.Background(), hashslot, targetNode)
	if err != nil {
		t.Fatalf("Failed to migrate hashslot: %v", err)
	}

	// Wait for signal message indicating migration detection
	select {
	case sig := <-signalMessages:
		t.Logf("Received signal: %+v", sig.Signal)
		if sig.Signal.EventType != submux.EventMigration {
			t.Errorf("Expected event type 'migration', got %q", sig.Signal.EventType)
		}
		if sig.Signal.NewNode != targetNode {
			t.Errorf("Expected new node %q, got %q", targetNode, sig.Signal.NewNode)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Timeout waiting for migration signal")
	}
}

func TestAutoResubscribe(t *testing.T) {
	t.Parallel()
	cluster := setupTestCluster(t, 6)
	client := cluster.GetClusterClient()

	subMux, err := submux.New(client,
		submux.WithAutoResubscribe(true),
		submux.WithTopologyPollInterval(100*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("Failed to create SubMux: %v", err)
	}
	defer subMux.Close()

	channelName := "auto-resub-test"
	messages := make(chan *submux.Message, 10)

	_, err = subMux.SSubscribeSync(context.Background(), []string{channelName}, func(msg *submux.Message) {
		if msg.Type == submux.MessageTypeMessage || msg.Type == submux.MessageTypeSMessage {
			messages <- msg
		}
	})
	if err != nil {
		t.Fatalf("Failed to subscribe: %v", err)
	}

	// Identify nodes
	hashslot := submux.Hashslot(channelName)
	sourceNode, err := cluster.GetNodeForHashslot(hashslot)
	if err != nil {
		t.Fatalf("Failed to get node for hashslot: %v", err)
	}

	var targetNode string
	for _, node := range cluster.GetNodes() {
		if node.Address != sourceNode {
			targetNode = node.Address
			break
		}
	}

	// Verify initial connectivity
	err = client.SPublish(context.Background(), channelName, "initial").Err()
	if err != nil {
		err = client.Publish(context.Background(), channelName, "initial").Err()
	}
	if err != nil {
		t.Fatalf("Failed to publish: %v", err)
	}

	select {
	case msg := <-messages:
		if msg.Payload != "initial" {
			t.Errorf("Expected 'initial', got %q", msg.Payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for initial message")
	}

	// Migrate
	t.Logf("Migrating hashslot %d from %s to %s", hashslot, sourceNode, targetNode)
	err = cluster.MigrateHashslot(context.Background(), hashslot, targetNode)
	if err != nil {
		t.Fatalf("Failed to migrate hashslot: %v", err)
	}

	// Wait a bit for migration detection and resubscription
	time.Sleep(1 * time.Second)

	// Reload state to ensure client knows about new topology for correct routing
	client.ReloadState(context.Background())

	// Loop publish until received or timeout
	// This helps avoid transient timing issues or race conditions immediately after resubscribe
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	var received bool
	for !received {
		// Publish
		err = client.SPublish(context.Background(), channelName, "after-migration").Err()
		if err != nil {
			err = client.Publish(context.Background(), channelName, "after-migration").Err()
		}
		if err != nil {
			t.Logf("Failed to publish: %v", err)
		}

		select {
		case <-ctx.Done():
			t.Fatal("Timeout waiting for message after migration resubscribe (retried publish)")
		case msg := <-messages:
			if msg.Payload == "after-migration" {
				received = true
			} else {
				t.Logf("Ignored unexpected message: %s", msg.Payload)
			}
		case <-ticker.C:
			// Loop continues and republishes
		}
	}
}

func TestManualResubscribe(t *testing.T) {
	t.Parallel()
	cluster := setupTestCluster(t, 6)
	client := cluster.GetClusterClient()

	subMux, err := submux.New(client,
		submux.WithAutoResubscribe(false),
		submux.WithTopologyPollInterval(100*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("Failed to create SubMux: %v", err)
	}
	defer subMux.Close()

	channelName := "manual-resub-test"
	messages := make(chan *submux.Message, 10)
	signals := make(chan *submux.Message, 10)

	// Define callback to reuse
	callback := func(msg *submux.Message) {
		if msg.Type == submux.MessageTypeMessage || msg.Type == submux.MessageTypeSMessage {
			messages <- msg
		} else if msg.Type == submux.MessageTypeSignal {
			signals <- msg
		}
	}

	_, err = subMux.SSubscribeSync(context.Background(), []string{channelName}, callback)
	if err != nil {
		t.Fatalf("Failed to subscribe: %v", err)
	}

	// Identify nodes
	hashslot := submux.Hashslot(channelName)
	sourceNode, err := cluster.GetNodeForHashslot(hashslot)
	if err != nil {
		t.Fatalf("Failed to get node for hashslot: %v", err)
	}

	var targetNode string
	for _, node := range cluster.GetNodes() {
		if node.Address != sourceNode {
			targetNode = node.Address
			break
		}
	}

	// Migrate
	t.Logf("Migrating hashslot %d from %s to %s", hashslot, sourceNode, targetNode)
	err = cluster.MigrateHashslot(context.Background(), hashslot, targetNode)
	if err != nil {
		t.Fatalf("Failed to migrate hashslot: %v", err)
	}

	// Verify the cluster sees the new owner
	newNode, err := cluster.GetNodeForHashslot(hashslot)
	if err != nil {
		t.Fatalf("Failed to get node for hashslot after migration: %v", err)
	}
	t.Logf("Hashslot %d is now owned by node %s", hashslot, newNode)
	if newNode != targetNode {
		t.Errorf("Expected hashslot to be owned by %s, but got %s", targetNode, newNode)
	}

	// Wait for migration signal
	select {
	case <-signals:
		t.Log("Received migration signal")
	case <-time.After(10 * time.Second):
		t.Fatal("Timeout waiting for migration signal")
	}

	// Wait a bit to ensure we would have missed messages if we didn't resubscribe
	time.Sleep(500 * time.Millisecond)

	// Reload state to ensure client knows about new topology for correct routing
	client.ReloadState(context.Background())

	// Publish test message using SPublish to enforce routing to new owner
	err = client.SPublish(context.Background(), channelName, "missed-message").Err()
	if err != nil {
		err = client.Publish(context.Background(), channelName, "missed-message").Err()
	}
	if err != nil {
		t.Fatalf("Failed to publish: %v", err)
	}

	// Verify we DON'T receive it (because we didn't resubscribe and old connection is likely dead for this slot)
	select {
	case <-messages:
		t.Fatal("Received message that should have been missed (manual resubscribe verification)")
	case <-time.After(1 * time.Second):
		t.Log("Correctly did not receive message on stale connection")
	}

	// Manually resubscribe
	t.Log("Manually resubscribing...")
	_, err = subMux.SSubscribeSync(context.Background(), []string{channelName}, callback)
	if err != nil {
		t.Fatalf("Failed to manual subscribe: %v", err)
	}

	// Reload state again just to be safe
	client.ReloadState(context.Background())

	// Loop publish until received or timeout
	// This helps avoid transient timing issues or race conditions immediately after resubscribe
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	var caught bool
	for !caught {
		// Publish
		err = client.SPublish(context.Background(), channelName, "caught-message").Err()
		if err != nil {
			err = client.Publish(context.Background(), channelName, "caught-message").Err()
		}
		if err != nil {
			t.Logf("Failed to publish: %v", err)
		}

		select {
		case <-ctx.Done():
			t.Fatal("Timeout waiting for message after manual resubscribe (retried publish)")
		case msg := <-messages:
			if msg.Payload == "caught-message" {
				caught = true
			} else {
				t.Logf("Ignored unexpected message: %s", msg.Payload)
			}
		case <-ticker.C:
			// Loop continues and republishes
		}
	}
}

func TestNodeFailure_SubscriptionContinuation(t *testing.T) {
	t.Parallel()
	// Need at least 3 shards with replicas to survive a master failure
	cluster := setupTestCluster(t, 9)
	client := cluster.GetClusterClient()

	subMux, err := submux.New(client,
		submux.WithAutoResubscribe(true), // Should handle failovers / topology updates
		submux.WithTopologyPollInterval(100*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("Failed to create SubMux: %v", err)
	}
	defer subMux.Close()

	channelName := "failover-test"
	messages := make(chan *submux.Message, 10)

	_, err = subMux.SubscribeSync(context.Background(), []string{channelName}, func(msg *submux.Message) {
		if msg.Type == submux.MessageTypeMessage {
			messages <- msg
		}
	})
	if err != nil {
		t.Fatalf("Failed to subscribe: %v", err)
	}

	// Identify master node for this channel
	hashslot := submux.Hashslot(channelName)
	masterNodeAddr, err := cluster.GetNodeForHashslot(hashslot)
	if err != nil {
		t.Fatalf("Failed to get node for hashslot: %v", err)
	}
	t.Logf("Master node for hashslot %d is %s", hashslot, masterNodeAddr)

	// Publish initial message
	err = client.Publish(context.Background(), channelName, "initial").Err()
	if err != nil {
		t.Fatalf("Failed to publish initial: %v", err)
	}
	// Drain it
	<-messages

	// KILL the master node
	t.Logf("Stopping master node %s...", masterNodeAddr)
	err = cluster.StopNode(masterNodeAddr)
	if err != nil {
		t.Fatalf("Failed to stop node: %v", err)
	}

	// Wait for failover (cluster should promote replica)
	// Redis Cluster default timeout is 5s, so we need to wait at least that
	// Our test config sets cluster-node-timeout to 5000ms
	t.Log("Waiting for failover and cluster health...")

	// Wait for cluster specific state to become OK via another node
	ok := false
	for i := 0; i < 30; i++ {
		time.Sleep(1 * time.Second)

		// Get a living node client
		var checkClient *redis.Client
		for _, node := range cluster.GetNodes() {
			if node.Address != masterNodeAddr {
				checkClient = redis.NewClient(&redis.Options{Addr: node.Address})
				if err := checkClient.Ping(context.Background()).Err(); err == nil {
					break
				}
				checkClient.Close()
				checkClient = nil
			}
		}

		if checkClient != nil {
			info, _ := checkClient.ClusterInfo(context.Background()).Result()
			checkClient.Close()
			if strings.Contains(info, "cluster_state:ok") {
				ok = true
				t.Log("Cluster state is OK")
				break
			}
		}
	}

	if !ok {
		t.Skip("Cluster failed to recover to OK state in time (environment issue), skipping test")
	}

	// Find a healthy node to use as entry point for publishing
	var healthyNode string
	for _, node := range cluster.GetNodes() {
		if node.Address != masterNodeAddr {
			checkClient := redis.NewClient(&redis.Options{Addr: node.Address})
			if err := checkClient.Ping(context.Background()).Err(); err == nil {
				healthyNode = node.Address
				checkClient.Close()
				break
			}
			checkClient.Close()
		}
	}

	if healthyNode == "" {
		t.Fatalf("No healthy nodes found to publish to")
	}

	pubClient := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs: []string{healthyNode},
	})
	defer pubClient.Close()

	// Verify new master is elected and we can receive messages
	t.Log("Publishing messages until one is received...")

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	received := false
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for !received {
		select {
		case <-ctx.Done():
			t.Fatalf("Timeout waiting for message after failover")
		case <-ticker.C:
			// Ensure pub client has fresh state
			pubClient.ReloadState(context.Background())

			payload := fmt.Sprintf("post-failover-%d", time.Now().UnixNano())
			err := pubClient.Publish(context.Background(), channelName, payload).Err()
			if err != nil {
				t.Logf("Publish failed (expected during failover): %v", err)
				continue
			}

			// Check if we got it
			select {
			case msg := <-messages:
				t.Logf("Received message: %s", msg.Payload)
				received = true
				return
			default:
				// Keep trying
			}
		}
	}
}

func TestMultipleChannelsAcrossShards(t *testing.T) {
	t.Parallel()
	cluster := getSharedCluster(t)
	client := cluster.GetClusterClient()
	subMux, err := submux.New(client)
	if err != nil {
		t.Fatalf("Failed to create SubMux: %v", err)
	}
	defer subMux.Close()

	// Create channels that map to different hashslots (and potentially different shards)
	channels := []string{
		"shard1-channel",
		"shard2-channel",
		"shard3-channel",
		"channel-{same}",
		"channel-{same}-2", // Same hashslot due to hashtag
	}

	// Calculate hashslots
	hashslots := make(map[int][]string)
	for _, ch := range channels {
		slot := submux.Hashslot(ch)
		hashslots[slot] = append(hashslots[slot], ch)
		t.Logf("Channel %q -> hashslot %d", ch, slot)
	}

	received := make(map[string]bool)
	var mu sync.Mutex

	_, err = subMux.SubscribeSync(context.Background(), channels, func(msg *submux.Message) {
		if msg.Type == submux.MessageTypeMessage {
			mu.Lock()
			received[msg.Channel] = true
			mu.Unlock()
		}
	})
	if err != nil {
		t.Fatalf("Failed to subscribe: %v", err)
	}

	// Publish to all channels
	pubClient := cluster.GetClusterClient()
	for _, ch := range channels {
		err = pubClient.Publish(context.Background(), ch, "test").Err()
		if err != nil {
			t.Fatalf("Failed to publish to %s: %v", ch, err)
		}
	}

	// Wait for all messages
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		mu.Lock()
		allReceived := true
		for _, ch := range channels {
			if !received[ch] {
				allReceived = false
				break
			}
		}
		mu.Unlock()

		if allReceived {
			break
		}

		select {
		case <-ctx.Done():
			mu.Lock()
			defer mu.Unlock()
			for _, ch := range channels {
				if !received[ch] {
					t.Errorf("Did not receive message on channel %s", ch)
				}
			}
			return
		case <-ticker.C:
		}
	}

	// Verify all messages received
	mu.Lock()
	defer mu.Unlock()
	for _, ch := range channels {
		if !received[ch] {
			t.Errorf("Did not receive message on channel %s", ch)
		}
	}
}

func TestSubscriptionAfterTopologyChange(t *testing.T) {
	t.Parallel()
	cluster := getSharedCluster(t)
	client := cluster.GetClusterClient()
	subMux, err := submux.New(client)
	if err != nil {
		t.Fatalf("Failed to create SubMux: %v", err)
	}
	defer subMux.Close()

	// Subscribe to a channel
	messages := make(chan *submux.Message, 10)
	_, err = subMux.SubscribeSync(context.Background(), []string{"topology-test"}, func(msg *submux.Message) {
		messages <- msg
	})
	if err != nil {
		t.Fatalf("Failed to subscribe: %v", err)
	}

	// Publish and verify message received
	pubClient := cluster.GetClusterClient()
	err = pubClient.Publish(context.Background(), "topology-test", "initial").Err()
	if err != nil {
		t.Fatalf("Failed to publish: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	select {
	case msg := <-messages:
		if msg.Payload != "initial" {
			t.Errorf("Expected 'initial', got %q", msg.Payload)
		}
	case <-ctx.Done():
		t.Fatal("Timeout waiting for initial message")
	}

	// Force cluster topology refresh
	// This simulates a topology change request
	err = client.ClusterSlots(context.Background()).Err()
	if err != nil {
		t.Logf("ClusterSlots error (may be expected): %v", err)
	}

	// Subscribe to another channel after topology refresh
	_, err = subMux.SubscribeSync(context.Background(), []string{"topology-test-2"}, func(msg *submux.Message) {
		messages <- msg
	})
	if err != nil {
		t.Fatalf("Failed to subscribe to second channel: %v", err)
	}

	// Publish to both channels
	err = pubClient.Publish(context.Background(), "topology-test", "after-refresh").Err()
	if err != nil {
		t.Fatalf("Failed to publish: %v", err)
	}

	err = pubClient.Publish(context.Background(), "topology-test-2", "new-channel").Err()
	if err != nil {
		t.Fatalf("Failed to publish: %v", err)
	}

	// Wait for both messages
	received := make(map[string]bool)
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()

	for len(received) < 2 {
		select {
		case msg := <-messages:
			received[msg.Payload] = true
		case <-ctx2.Done():
			t.Errorf("Timeout waiting for messages. Received: %v", received)
			return
		}
	}

	if !received["after-refresh"] {
		t.Error("Did not receive message on original channel after topology refresh")
	}
	if !received["new-channel"] {
		t.Error("Did not receive message on new channel")
	}
}
