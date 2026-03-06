package integration

import (
	"context"
	"fmt"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lalloni/submux"
	"github.com/redis/go-redis/v9"
)

// TestUnsubscribeWithCancelledContext verifies that even with a cancelled caller context,
// UNSUBSCRIBE reaches Redis — no messages arrive after.
func TestUnsubscribeWithCancelledContext(t *testing.T) {
	cluster := getSharedCluster(t)
	client := cluster.GetClusterClient()
	subMux, err := submux.New(client)
	if err != nil {
		t.Fatalf("Failed to create SubMux: %v", err)
	}
	defer subMux.Close()

	channel := uniqueChannel("unsub-cancelled-ctx")
	messages := make(chan *submux.Message, 10)

	sub, err := subMux.SubscribeSync(context.Background(), []string{channel}, func(ctx context.Context, msg *submux.Message) {
		if msg.Type == submux.MessageTypeMessage {
			messages <- msg
		}
	})
	if err != nil {
		t.Fatalf("Failed to subscribe: %v", err)
	}

	// Confirm receipt of a message
	pubClient := cluster.GetClusterClient()
	err = pubClient.Publish(context.Background(), channel, "before").Err()
	if err != nil {
		t.Fatalf("Failed to publish: %v", err)
	}

	select {
	case msg := <-messages:
		if msg.Payload != "before" {
			t.Fatalf("Expected 'before', got %q", msg.Payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for message before unsubscribe")
	}

	// Cancel a context, then unsubscribe with it
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	err = sub.Unsubscribe(cancelledCtx)
	// Error may be context.Canceled (acceptable) or nil
	if err != nil && err != context.Canceled {
		t.Fatalf("Unsubscribe returned unexpected error: %v", err)
	}

	// Publish again and assert no message received
	err = pubClient.Publish(context.Background(), channel, "after").Err()
	if err != nil {
		t.Fatalf("Failed to publish after unsubscribe: %v", err)
	}

	select {
	case msg := <-messages:
		t.Errorf("Received message after unsubscribe with cancelled context: %q", msg.Payload)
	case <-time.After(200 * time.Millisecond):
		// Expected: no message received
	}
}

// TestUnsubscribeConfirmationTiming verifies that after Unsubscribe(ctx) returns,
// immediately published messages are NOT delivered. Confirms the wait-for-Redis-confirmation fix.
func TestUnsubscribeConfirmationTiming(t *testing.T) {
	cluster := getSharedCluster(t)
	client := cluster.GetClusterClient()
	subMux, err := submux.New(client)
	if err != nil {
		t.Fatalf("Failed to create SubMux: %v", err)
	}
	defer subMux.Close()

	channel := uniqueChannel("unsub-confirm-timing")
	messages := make(chan *submux.Message, 20)

	sub, err := subMux.SubscribeSync(context.Background(), []string{channel}, func(ctx context.Context, msg *submux.Message) {
		if msg.Type == submux.MessageTypeMessage {
			messages <- msg
		}
	})
	if err != nil {
		t.Fatalf("Failed to subscribe: %v", err)
	}

	// Confirm initial message receipt
	pubClient := cluster.GetClusterClient()
	err = pubClient.Publish(context.Background(), channel, "initial").Err()
	if err != nil {
		t.Fatalf("Failed to publish: %v", err)
	}

	select {
	case msg := <-messages:
		if msg.Payload != "initial" {
			t.Fatalf("Expected 'initial', got %q", msg.Payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for initial message")
	}

	// Unsubscribe — must return nil
	err = sub.Unsubscribe(context.Background())
	if err != nil {
		t.Fatalf("Unsubscribe failed: %v", err)
	}

	// Immediately publish 5 messages (no delay)
	for i := range 5 {
		_ = pubClient.Publish(context.Background(), channel, "post-unsub-"+string(rune('0'+i))).Err()
	}

	// Wait 500ms, assert message channel is empty
	time.Sleep(500 * time.Millisecond)

	select {
	case msg := <-messages:
		t.Errorf("Received message after Unsubscribe returned: %q", msg.Payload)
	default:
		// Expected: no messages
	}
}

// TestCloseWaitsForInFlightCallbacks verifies that Close() blocks until currently
// executing callbacks complete (callbackWg.Wait()).
func TestCloseWaitsForInFlightCallbacks(t *testing.T) {
	cluster := getSharedCluster(t)
	client := cluster.GetClusterClient()

	subMux, err := submux.New(client,
		submux.WithCallbackWorkers(1),
		submux.WithCallbackQueueSize(1),
	)
	if err != nil {
		t.Fatalf("Failed to create SubMux: %v", err)
	}

	channel := uniqueChannel("close-waits-callbacks")
	var callbackCompleted atomic.Bool

	_, err = subMux.SubscribeSync(context.Background(), []string{channel}, func(ctx context.Context, msg *submux.Message) {
		if msg.Type == submux.MessageTypeMessage {
			time.Sleep(500 * time.Millisecond)
			callbackCompleted.Store(true)
		}
	})
	if err != nil {
		t.Fatalf("Failed to subscribe: %v", err)
	}

	// Publish a message to trigger the callback
	pubClient := cluster.GetClusterClient()
	err = pubClient.Publish(context.Background(), channel, "trigger").Err()
	if err != nil {
		t.Fatalf("Failed to publish: %v", err)
	}

	// Wait for callback to start executing
	time.Sleep(50 * time.Millisecond)

	// Call Close() and measure elapsed time
	start := time.Now()
	_ = subMux.Close()
	elapsed := time.Since(start)

	if elapsed < 300*time.Millisecond {
		t.Errorf("Close() returned too quickly (%v); expected it to wait for callback (>= 300ms)", elapsed)
	}
	if !callbackCompleted.Load() {
		t.Error("Callback did not complete before Close() returned")
	}
}

// TestCallbackPanicRecovery verifies that a panicking callback doesn't crash SubMux
// or prevent other subscriptions from receiving messages.
func TestCallbackPanicRecovery(t *testing.T) {
	cluster := getSharedCluster(t)
	client := cluster.GetClusterClient()
	subMux, err := submux.New(client)
	if err != nil {
		t.Fatalf("Failed to create SubMux: %v", err)
	}
	defer subMux.Close()

	channel := uniqueChannel("panic-recovery")
	var callback2Count atomic.Int32

	// Subscribe callback1 that panics
	_, err = subMux.SubscribeSync(context.Background(), []string{channel}, func(ctx context.Context, msg *submux.Message) {
		if msg.Type == submux.MessageTypeMessage {
			panic("intentional test panic")
		}
	})
	if err != nil {
		t.Fatalf("Failed to subscribe (callback1): %v", err)
	}

	// Subscribe callback2 that is well-behaved
	_, err = subMux.SubscribeSync(context.Background(), []string{channel}, func(ctx context.Context, msg *submux.Message) {
		if msg.Type == submux.MessageTypeMessage {
			callback2Count.Add(1)
		}
	})
	if err != nil {
		t.Fatalf("Failed to subscribe (callback2): %v", err)
	}

	pubClient := cluster.GetClusterClient()

	// Publish first message — callback1 panics, callback2 should still receive it
	err = pubClient.Publish(context.Background(), channel, "msg1").Err()
	if err != nil {
		t.Fatalf("Failed to publish msg1: %v", err)
	}

	// Wait for callback2 to process
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for callback2Count.Load() < 1 {
		select {
		case <-ctx.Done():
			t.Fatalf("Timeout waiting for callback2 to receive msg1; count=%d", callback2Count.Load())
		case <-ticker.C:
		}
	}

	// Publish second message to verify SubMux is still healthy
	err = pubClient.Publish(context.Background(), channel, "msg2").Err()
	if err != nil {
		t.Fatalf("Failed to publish msg2: %v", err)
	}

	ctx2, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	ticker2 := time.NewTicker(50 * time.Millisecond)
	defer ticker2.Stop()

	for callback2Count.Load() < 2 {
		select {
		case <-ctx2.Done():
			t.Fatalf("Timeout waiting for callback2 to receive msg2; count=%d", callback2Count.Load())
		case <-ticker2.C:
		}
	}

	if count := callback2Count.Load(); count != 2 {
		t.Errorf("Expected callback2 to be called 2 times, got %d", count)
	}
}

// TestMixedSubscriptionTypes verifies that SUBSCRIBE + PSUBSCRIBE + SSUBSCRIBE
// coexist on a single SubMux.
func TestMixedSubscriptionTypes(t *testing.T) {
	cluster := getSharedCluster(t)
	client := cluster.GetClusterClient()
	subMux, err := submux.New(client)
	if err != nil {
		t.Fatalf("Failed to create SubMux: %v", err)
	}
	defer subMux.Close()

	id := uniqueChannel("mixed")

	subChannel := "mixed-sub-" + id
	psubPattern := "mixed-psub-" + id + ":*"
	psubPublish := "mixed-psub-" + id + ":foo"
	ssubChannel := "mixed-ssub-" + id

	var mu sync.Mutex
	received := make(map[string]submux.MessageType)

	// SubscribeSync to regular channel
	_, err = subMux.SubscribeSync(context.Background(), []string{subChannel}, func(ctx context.Context, msg *submux.Message) {
		if msg.Type == submux.MessageTypeMessage {
			mu.Lock()
			received["sub"] = msg.Type
			mu.Unlock()
		}
	})
	if err != nil {
		t.Fatalf("Failed to SubscribeSync: %v", err)
	}

	// PSubscribeSync to pattern
	_, err = subMux.PSubscribeSync(context.Background(), []string{psubPattern}, func(ctx context.Context, msg *submux.Message) {
		if msg.Type == submux.MessageTypePMessage {
			mu.Lock()
			received["psub"] = msg.Type
			mu.Unlock()
		}
	})
	if err != nil {
		t.Fatalf("Failed to PSubscribeSync: %v", err)
	}

	// SSubscribeSync to sharded channel
	_, err = subMux.SSubscribeSync(context.Background(), []string{ssubChannel}, func(ctx context.Context, msg *submux.Message) {
		if msg.Type == submux.MessageTypeSMessage {
			mu.Lock()
			received["ssub"] = msg.Type
			mu.Unlock()
		}
	})
	if err != nil {
		t.Fatalf("Failed to SSubscribeSync: %v", err)
	}

	pubClient := cluster.GetClusterClient()

	// PUBLISH to regular channel
	err = pubClient.Publish(context.Background(), subChannel, "hello").Err()
	if err != nil {
		t.Fatalf("Failed to PUBLISH to %s: %v", subChannel, err)
	}

	// PUBLISH to match pattern
	err = pubClient.Publish(context.Background(), psubPublish, "hello").Err()
	if err != nil {
		t.Fatalf("Failed to PUBLISH to %s: %v", psubPublish, err)
	}

	// SPUBLISH to sharded channel
	err = pubClient.SPublish(context.Background(), ssubChannel, "hello").Err()
	if err != nil {
		t.Fatalf("Failed to SPUBLISH to %s: %v", ssubChannel, err)
	}

	// Wait for all three callbacks to fire
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		mu.Lock()
		allReceived := len(received) == 3
		mu.Unlock()

		if allReceived {
			break
		}

		select {
		case <-ctx.Done():
			mu.Lock()
			defer mu.Unlock()
			if _, ok := received["sub"]; !ok {
				t.Error("Did not receive regular SUBSCRIBE message")
			}
			if _, ok := received["psub"]; !ok {
				t.Error("Did not receive PSUBSCRIBE message")
			}
			if _, ok := received["ssub"]; !ok {
				t.Error("Did not receive SSUBSCRIBE message")
			}
			return
		case <-ticker.C:
		}
	}

	// Verify correct message types
	mu.Lock()
	defer mu.Unlock()

	if received["sub"] != submux.MessageTypeMessage {
		t.Errorf("Expected MessageTypeMessage for sub, got %v", received["sub"])
	}
	if received["psub"] != submux.MessageTypePMessage {
		t.Errorf("Expected MessageTypePMessage for psub, got %v", received["psub"])
	}
	if received["ssub"] != submux.MessageTypeSMessage {
		t.Errorf("Expected MessageTypeSMessage for ssub, got %v", received["ssub"])
	}
}

// TestMigrationTimeoutSignal verifies that EventMigrationTimeout signal is delivered
// when auto-resubscription exceeds timeout.
func TestMigrationTimeoutSignal(t *testing.T) {
	t.Parallel() // Dedicated cluster - safe to run in parallel
	// Use 0 replicas so pausing a node prevents failover, ensuring resubscription hangs.
	cluster := startTestClusterNoReplicas(t)
	pubClient := cluster.GetClusterClient()

	// Short ReadTimeout (500ms) so topology polls fail fast when routed to the
	// paused node. The paused node's kernel accepts TCP but the process is
	// frozen (SIGSTOP), so reads hang until ReadTimeout. go-redis PubSub reader
	// retries on timeout (doesn't close the channel), so this doesn't kill the
	// PubSub connection prematurely.
	addrs := make([]string, len(cluster.nodes))
	for i, node := range cluster.nodes {
		addrs[i] = node.Address
	}
	submuxClient := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs:        addrs,
		DialTimeout:  500 * time.Millisecond,
		ReadTimeout:  500 * time.Millisecond,
		WriteTimeout: 500 * time.Millisecond,
	})
	t.Cleanup(func() { submuxClient.Close() })

	// migrationTimeout is 1s (the config minimum). stallCheck is 1100ms so the
	// monitoring ticker fires every 1.1s. At the first tick (~1.1s), the timeout
	// check runs first (1.1s > 1s → true) and returns before reaching the stall
	// check. Poll interval is 300ms to detect migration quickly (the ~1μs window
	// between Migrate and Pause makes race probability negligible at this interval).
	subMux, err := submux.New(submuxClient,
		submux.WithAutoResubscribe(true),
		submux.WithTopologyPollInterval(300*time.Millisecond),
		submux.WithMigrationTimeout(1*time.Second),
		submux.WithMigrationStallCheck(1100*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("Failed to create SubMux: %v", err)
	}
	defer subMux.Close()

	channelName := uniqueChannel("migration-timeout")
	signals := make(chan *submux.Message, 20)
	messages := make(chan *submux.Message, 10)

	_, err = subMux.SSubscribeSync(context.Background(), []string{channelName}, func(ctx context.Context, msg *submux.Message) {
		switch msg.Type {
		case submux.MessageTypeSignal:
			signals <- msg
		default:
			messages <- msg
		}
	})
	if err != nil {
		t.Fatalf("Failed to subscribe: %v", err)
	}

	// Confirm initial receipt
	err = pubClient.SPublish(context.Background(), channelName, "initial").Err()
	if err != nil {
		t.Fatalf("Failed to SPUBLISH: %v", err)
	}

	select {
	case <-messages:
		// Good
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for initial message")
	}

	// Get hashslot and nodes
	hashslot := submux.Hashslot(channelName)
	sourceNode, err := cluster.GetNodeForHashslot(hashslot)
	if err != nil {
		t.Fatalf("Failed to get source node: %v", err)
	}

	// Pick a target node (different from source, must be a master)
	var targetNode string
	for _, node := range cluster.nodes {
		if node.Address != sourceNode {
			targetNode = node.Address
			break
		}
	}
	if targetNode == "" {
		t.Fatal("Could not find target node for migration")
	}

	t.Logf("Migrating hashslot %d from %s to %s", hashslot, sourceNode, targetNode)
	err = cluster.MigrateHashslot(context.Background(), hashslot, targetNode)
	if err != nil {
		t.Fatalf("Failed to migrate hashslot: %v", err)
	}

	// Pause the target node (SIGSTOP) so resubscription hangs instead of being refused.
	// This causes TCP connections to remain open but unresponsive, triggering timeout.
	t.Logf("Pausing target node %s", targetNode)
	err = cluster.PauseNode(targetNode)
	if err != nil {
		t.Fatalf("Failed to pause target node: %v", err)
	}
	t.Cleanup(func() {
		// Resume node to allow clean cluster shutdown
		_ = cluster.ResumeNode(targetNode)
	})

	// Collect signals: expect EventMigration then EventMigrationTimeout within 10s.
	var signalTypes []submux.EventType
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for {
		select {
		case sig := <-signals:
			t.Logf("Received signal: %v", sig.Signal.EventType)
			signalTypes = append(signalTypes, sig.Signal.EventType)
			// Check if we got the timeout signal
			for _, st := range signalTypes {
				if st == submux.EventMigrationTimeout {
					goto done
				}
			}
		case <-ctx.Done():
			t.Fatalf("Timeout waiting for migration signals; received: %v", signalTypes)
		}
	}
done:

	// Verify we got EventMigration then EventMigrationTimeout
	hasMigration := false
	hasTimeout := false
	for _, st := range signalTypes {
		if st == submux.EventMigration {
			hasMigration = true
		}
		if st == submux.EventMigrationTimeout {
			hasTimeout = true
		}
	}
	if !hasMigration {
		t.Error("Did not receive EventMigration signal")
	}
	if !hasTimeout {
		t.Error("Did not receive EventMigrationTimeout signal")
	}
}

// TestMigrationStalledSignal verifies that EventMigrationStalled signal is delivered
// when no resubscription progress is made.
func TestMigrationStalledSignal(t *testing.T) {
	t.Parallel() // Dedicated cluster - safe to run in parallel
	// Use 0 replicas so pausing a node prevents failover, ensuring resubscription hangs.
	cluster := startTestClusterNoReplicas(t)
	pubClient := cluster.GetClusterClient()

	// Create a separate client for SubMux with short timeouts (500ms ReadTimeout).
	// This keeps topology polling fast (fails within 500ms when hitting the paused node)
	// while the resubscription read hangs long enough (>100ms) for the stall check to fire.
	addrs := make([]string, len(cluster.nodes))
	for i, node := range cluster.nodes {
		addrs[i] = node.Address
	}
	submuxClient := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs:        addrs,
		DialTimeout:  500 * time.Millisecond,
		ReadTimeout:  500 * time.Millisecond,
		WriteTimeout: 500 * time.Millisecond,
	})
	t.Cleanup(func() { submuxClient.Close() })

	// stallCheck (100ms) fires well before migrationTimeout (30s).
	// Poll interval is 300ms to detect migration quickly.
	subMux, err := submux.New(submuxClient,
		submux.WithAutoResubscribe(true),
		submux.WithTopologyPollInterval(300*time.Millisecond),
		submux.WithMigrationStallCheck(100*time.Millisecond),
		submux.WithMigrationTimeout(30*time.Second),
	)
	if err != nil {
		t.Fatalf("Failed to create SubMux: %v", err)
	}
	defer subMux.Close()

	channelName := uniqueChannel("migration-stalled")
	signals := make(chan *submux.Message, 20)
	messages := make(chan *submux.Message, 10)

	_, err = subMux.SSubscribeSync(context.Background(), []string{channelName}, func(ctx context.Context, msg *submux.Message) {
		switch msg.Type {
		case submux.MessageTypeSignal:
			signals <- msg
		default:
			messages <- msg
		}
	})
	if err != nil {
		t.Fatalf("Failed to subscribe: %v", err)
	}

	// Confirm initial receipt
	err = pubClient.SPublish(context.Background(), channelName, "initial").Err()
	if err != nil {
		t.Fatalf("Failed to SPUBLISH: %v", err)
	}

	select {
	case <-messages:
		// Good
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for initial message")
	}

	// Get hashslot and nodes
	hashslot := submux.Hashslot(channelName)
	sourceNode, err := cluster.GetNodeForHashslot(hashslot)
	if err != nil {
		t.Fatalf("Failed to get source node: %v", err)
	}

	var targetNode string
	for _, node := range cluster.nodes {
		if node.Address != sourceNode {
			targetNode = node.Address
			break
		}
	}
	if targetNode == "" {
		t.Fatal("Could not find target node for migration")
	}

	t.Logf("Migrating hashslot %d from %s to %s", hashslot, sourceNode, targetNode)
	err = cluster.MigrateHashslot(context.Background(), hashslot, targetNode)
	if err != nil {
		t.Fatalf("Failed to migrate hashslot: %v", err)
	}

	// Pause the target node (SIGSTOP) so resubscription hangs instead of being refused.
	// This causes TCP connections to remain open but unresponsive, triggering stall detection.
	t.Logf("Pausing target node %s", targetNode)
	err = cluster.PauseNode(targetNode)
	if err != nil {
		t.Fatalf("Failed to pause target node: %v", err)
	}
	t.Cleanup(func() {
		// Resume node to allow clean cluster shutdown
		_ = cluster.ResumeNode(targetNode)
	})

	// Collect signals: expect EventMigration then EventMigrationStalled within 10s.
	var signalTypes []submux.EventType
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for {
		select {
		case sig := <-signals:
			t.Logf("Received signal: %v", sig.Signal.EventType)
			signalTypes = append(signalTypes, sig.Signal.EventType)
			for _, st := range signalTypes {
				if st == submux.EventMigrationStalled {
					goto done
				}
			}
		case <-ctx.Done():
			t.Fatalf("Timeout waiting for migration signals; received: %v", signalTypes)
		}
	}
done:

	hasMigration := false
	hasStalled := false
	for _, st := range signalTypes {
		if st == submux.EventMigration {
			hasMigration = true
		}
		if st == submux.EventMigrationStalled {
			hasStalled = true
		}
	}
	if !hasMigration {
		t.Error("Did not receive EventMigration signal")
	}
	if !hasStalled {
		t.Error("Did not receive EventMigrationStalled signal")
	}
}

// TestDoubleCloseIdempotency verifies that calling Close() twice doesn't panic,
// error, or deadlock.
func TestDoubleCloseIdempotency(t *testing.T) {
	cluster := getSharedCluster(t)
	client := cluster.GetClusterClient()

	subMux, err := submux.New(client)
	if err != nil {
		t.Fatalf("Failed to create SubMux: %v", err)
	}

	channel := uniqueChannel("double-close")
	_, err = subMux.SubscribeSync(context.Background(), []string{channel}, func(ctx context.Context, msg *submux.Message) {
		// no-op
	})
	if err != nil {
		t.Fatalf("Failed to subscribe: %v", err)
	}

	// Sequential double close
	err1 := subMux.Close()
	err2 := subMux.Close()
	if err1 != nil {
		t.Errorf("First Close() returned error: %v", err1)
	}
	if err2 != nil {
		t.Errorf("Second Close() returned error: %v", err2)
	}

	// Concurrent double close (on a fresh SubMux)
	subMux2, err := submux.New(client)
	if err != nil {
		t.Fatalf("Failed to create second SubMux: %v", err)
	}

	_, err = subMux2.SubscribeSync(context.Background(), []string{uniqueChannel("double-close-concurrent")}, func(ctx context.Context, msg *submux.Message) {
		// no-op
	})
	if err != nil {
		t.Fatalf("Failed to subscribe on second SubMux: %v", err)
	}

	done := make(chan error, 2)
	for range 2 {
		go func() {
			done <- subMux2.Close()
		}()
	}

	// Assert both complete within 5s and no panic
	for range 2 {
		select {
		case closeErr := <-done:
			if closeErr != nil {
				t.Errorf("Concurrent Close() returned error: %v", closeErr)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("Concurrent Close() deadlocked (timeout 5s)")
		}
	}
}

// TestWorkerPoolBackpressure verifies that with a tiny worker pool, all messages
// are eventually delivered despite backpressure.
func TestWorkerPoolBackpressure(t *testing.T) {
	cluster := getSharedCluster(t)
	client := cluster.GetClusterClient()

	subMux, err := submux.New(client,
		submux.WithCallbackWorkers(1),
		submux.WithCallbackQueueSize(1),
	)
	if err != nil {
		t.Fatalf("Failed to create SubMux: %v", err)
	}
	defer subMux.Close()

	channel := uniqueChannel("backpressure")
	var count atomic.Int32

	_, err = subMux.SubscribeSync(context.Background(), []string{channel}, func(ctx context.Context, msg *submux.Message) {
		if msg.Type == submux.MessageTypeMessage {
			time.Sleep(50 * time.Millisecond)
			count.Add(1)
		}
	})
	if err != nil {
		t.Fatalf("Failed to subscribe: %v", err)
	}

	// Publish 20 messages rapidly
	pubClient := cluster.GetClusterClient()
	const numMessages = 20
	for i := range numMessages {
		err = pubClient.Publish(context.Background(), channel, "msg").Err()
		if err != nil {
			t.Fatalf("Failed to publish message %d: %v", i, err)
		}
	}

	// Wait up to 5s for all 20 to be received
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		if count.Load() >= numMessages {
			break
		}

		select {
		case <-ctx.Done():
			t.Fatalf("Timeout: only received %d/%d messages", count.Load(), numMessages)
		case <-ticker.C:
		}
	}

	if final := count.Load(); final != numMessages {
		t.Errorf("Expected %d messages, got %d", numMessages, final)
	}
}

// startTestClusterNoReplicas starts a 3-shard Redis Cluster with 0 replicas.
// This prevents automatic failover when a node is paused with SIGSTOP,
func startTestClusterNoReplicas(t *testing.T) *TestCluster {
	t.Helper()

	if _, err := exec.LookPath("redis-server"); err != nil {
		t.Fatalf("redis-server not found in PATH: %v", err)
	}
	if _, err := exec.LookPath("redis-cli"); err != nil {
		t.Fatalf("redis-cli not found in PATH: %v", err)
	}

	clusterName := fmt.Sprintf("test-%s-%d", t.Name(), time.Now().UnixNano())

	cluster, err := StartCluster(context.Background(), 3, 0, clusterName)
	if err != nil {
		t.Fatalf("Failed to start no-replica cluster: %v", err)
	}

	// Increase cluster-node-timeout so the cluster doesn't mark a paused node
	// as failed before migration timeout/stall detection fires in the test.
	// The default is 1000ms which causes FAIL state in ~1.5s — too fast.
	for _, node := range cluster.nodes {
		cmd := exec.Command("redis-cli", "-h", "127.0.0.1", "-p", fmt.Sprintf("%d", node.Port), "CONFIG", "SET", "cluster-node-timeout", "60000")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("Failed to set cluster-node-timeout on %s: %v\n%s", node.Address, err, out)
		}
	}

	t.Cleanup(func() {
		if t.Failed() {
			cluster.DumpProcessOutput()
		}
		if err := cluster.StopCluster(context.Background()); err != nil {
			t.Logf("Error stopping test cluster: %v", err)
		}
	})

	return cluster
}
