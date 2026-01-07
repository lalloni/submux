package submux

import (
	"context"
	"testing"

	"github.com/redis/go-redis/v9"
)

// mockClusterClientForPool creates a minimal mock for pool testing
type mockClusterClientForPool struct {
	subscribeFunc    func(ctx context.Context) *redis.PubSub
	clusterSlotsFunc func(ctx context.Context) *redis.ClusterSlotsCmd
}

func (m *mockClusterClientForPool) Subscribe(ctx context.Context, channels ...string) *redis.PubSub {
	if m.subscribeFunc != nil {
		return m.subscribeFunc(ctx)
	}
	// Return a real PubSub that will be closed immediately
	// This is a workaround - in real tests we'd use a proper mock
	return nil
}

func (m *mockClusterClientForPool) ClusterSlots(ctx context.Context) *redis.ClusterSlotsCmd {
	if m.clusterSlotsFunc != nil {
		return m.clusterSlotsFunc(ctx)
	}
	cmd := redis.NewClusterSlotsCmd(ctx)
	cmd.SetErr(redis.Nil)
	return cmd
}

func TestPubSubPool_Creation(t *testing.T) {
	cfg := defaultConfig()
	// We can't easily test with real ClusterClient without Redis, so we test the structure
	// In integration tests, we'll test with real clients
	var clusterClient *redis.ClusterClient = nil // Will be nil, but tests structure

	pool := newPubSubPool(clusterClient, cfg)

	if pool == nil {
		t.Fatal("newPubSubPool returned nil")
	}
	if pool.clusterClient != clusterClient {
		t.Errorf("pool.clusterClient = %v, want %v", pool.clusterClient, clusterClient)
	}
	if pool.config != cfg {
		t.Errorf("pool.config = %v, want %v", pool.config, cfg)
	}
	if pool.nodePubSubs == nil {
		t.Error("pool.nodePubSubs is nil")
	}
	if pool.hashslotPubSubs == nil {
		t.Error("pool.hashslotPubSubs is nil")
	}
	if pool.pubSubMetadata == nil {
		t.Error("pool.pubSubMetadata is nil")
	}
}

func TestPubSubMetadata_SubscriptionManagement(t *testing.T) {
	meta := &pubSubMetadata{
		pubsub:        nil, // Not needed for this test
		nodeAddr:      "localhost:7000",
		subscriptions: make(map[string][]*subscription),
		state:         connStateActive,
		cmdCh:         make(chan *command, 100),
		done:          make(chan struct{}),
	}

	// Test addSubscription
	sub1 := &subscription{
		channel:   "channel1",
		subType:   subTypeSubscribe,
		confirmCh: make(chan error, 1),
	}
	meta.addSubscription(sub1)

	subs := meta.getSubscriptions("channel1")
	if len(subs) != 1 {
		t.Errorf("getSubscriptions returned %d subscriptions, want 1", len(subs))
	}
	if subs[0] != sub1 {
		t.Error("getSubscriptions returned wrong subscription")
	}

	// Test adding multiple subscriptions to same channel
	sub2 := &subscription{
		channel:   "channel1",
		subType:   subTypeSubscribe,
		confirmCh: make(chan error, 1),
	}
	meta.addSubscription(sub2)

	subs = meta.getSubscriptions("channel1")
	if len(subs) != 2 {
		t.Errorf("getSubscriptions returned %d subscriptions, want 2", len(subs))
	}

	// Test subscriptionCount
	count := meta.subscriptionCount()
	if count != 2 {
		t.Errorf("subscriptionCount() = %d, want 2", count)
	}

	// Test getAllSubscriptions
	allSubs := meta.getAllSubscriptions()
	if len(allSubs) != 2 {
		t.Errorf("getAllSubscriptions returned %d subscriptions, want 2", len(allSubs))
	}

	// Test removeSubscription
	meta.removeSubscription(sub1)
	subs = meta.getSubscriptions("channel1")
	if len(subs) != 1 {
		t.Errorf("After removeSubscription, getSubscriptions returned %d subscriptions, want 1", len(subs))
	}
	if subs[0] != sub2 {
		t.Error("After removeSubscription, remaining subscription is wrong")
	}

	// Test removeSubscription removes channel when empty
	meta.removeSubscription(sub2)
	subs = meta.getSubscriptions("channel1")
	if len(subs) != 0 {
		t.Errorf("After removing all subscriptions, getSubscriptions returned %d subscriptions, want 0", len(subs))
	}
}

func TestPubSubMetadata_StateManagement(t *testing.T) {
	meta := &pubSubMetadata{
		pubsub:        nil,
		nodeAddr:      "localhost:7000",
		subscriptions: make(map[string][]*subscription),
		state:         connStateActive,
		cmdCh:         make(chan *command, 100),
		done:          make(chan struct{}),
	}

	// Test getState
	if meta.getState() != connStateActive {
		t.Errorf("getState() = %v, want %v", meta.getState(), connStateActive)
	}

	// Test setState
	meta.setState(connStateFailed)
	if meta.getState() != connStateFailed {
		t.Errorf("After setState(Failed), getState() = %v, want %v", meta.getState(), connStateFailed)
	}

	meta.setState(connStateClosed)
	if meta.getState() != connStateClosed {
		t.Errorf("After setState(Closed), getState() = %v, want %v", meta.getState(), connStateClosed)
	}
}

func TestPubSubMetadata_SendCommand(t *testing.T) {
	meta := &pubSubMetadata{
		pubsub:        nil,
		nodeAddr:      "localhost:7000",
		subscriptions: make(map[string][]*subscription),
		state:         connStateActive,
		cmdCh:         make(chan *command, 100),
		done:          make(chan struct{}),
	}

	cmd := &command{
		cmd:      cmdSubscribe,
		args:     []any{"channel1"},
		response: make(chan error, 1),
	}

	// Test sendCommand
	ctx := context.Background()
	err := meta.sendCommand(ctx, cmd)
	if err != nil {
		t.Errorf("sendCommand returned error: %v", err)
	}

	// Verify command was sent
	select {
	case receivedCmd := <-meta.cmdCh:
		if receivedCmd != cmd {
			t.Error("Received command is not the same as sent command")
		}
	default:
		t.Error("Command was not received on cmdCh")
	}
}

func TestPubSubMetadata_SendCommand_Closed(t *testing.T) {
	meta := &pubSubMetadata{
		pubsub:        nil,
		nodeAddr:      "localhost:7000",
		subscriptions: make(map[string][]*subscription),
		state:         connStateActive,
		cmdCh:         make(chan *command, 1), // Small buffer
		done:          make(chan struct{}),
	}

	// Fill the channel to force blocking on cmdCh
	blockingCmd := &command{
		cmd:      "BLOCK",
		args:     []any{},
		response: make(chan error, 1),
	}
	meta.cmdCh <- blockingCmd

	// Close the done channel to simulate closed PubSub
	close(meta.done)

	cmd := &command{
		cmd:      cmdSubscribe,
		args:     []any{"channel1"},
		response: make(chan error, 1),
	}

	ctx := context.Background()
	err := meta.sendCommand(ctx, cmd)
	if err == nil {
		t.Error("sendCommand should return error when PubSub is closed")
	}
	if err != nil && err.Error() != "pubsub closed" {
		t.Errorf("sendCommand returned error %v, want 'pubsub closed'", err)
	}

	// Cleanup: drain the channel
	select {
	case <-meta.cmdCh:
	default:
	}
}

func TestPubSubMetadata_SendCommand_ContextCancellation(t *testing.T) {
	meta := &pubSubMetadata{
		pubsub:        nil,
		nodeAddr:      "localhost:7000",
		subscriptions: make(map[string][]*subscription),
		state:         connStateActive,
		cmdCh:         make(chan *command, 1), // Small buffer to test blocking
		done:          make(chan struct{}),
	}

	// Fill the channel to force blocking
	blockingCmd := &command{
		cmd:      "BLOCK",
		args:     []any{},
		response: make(chan error, 1),
	}
	meta.cmdCh <- blockingCmd

	cmd := &command{
		cmd:      cmdSubscribe,
		args:     []any{"channel1"},
		response: make(chan error, 1),
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	err := meta.sendCommand(ctx, cmd)
	if err != context.Canceled {
		t.Errorf("sendCommand returned error %v, want %v", err, context.Canceled)
	}

	// Cleanup: drain the channel
	select {
	case <-meta.cmdCh:
	default:
	}
}

func TestPubSubMetadata_Close(t *testing.T) {
	meta := &pubSubMetadata{
		pubsub:        nil,
		nodeAddr:      "localhost:7000",
		subscriptions: make(map[string][]*subscription),
		state:         connStateActive,
		cmdCh:         make(chan *command, 100),
		done:          make(chan struct{}),
	}

	// Close should be idempotent
	err1 := meta.close()
	if err1 != nil {
		t.Errorf("close() returned error: %v", err1)
	}
	if meta.getState() != connStateClosed {
		t.Errorf("After close(), getState() = %v, want %v", meta.getState(), connStateClosed)
	}

	// Second close should also succeed
	err2 := meta.close()
	if err2 != nil {
		t.Errorf("Second close() returned error: %v", err2)
	}
}

func TestPubSubPool_GetMetadata(t *testing.T) {
	cfg := defaultConfig()
	var clusterClient *redis.ClusterClient = nil
	pool := newPubSubPool(clusterClient, cfg)

	// Test getMetadata with nil PubSub
	meta := pool.getMetadata(nil)
	if meta != nil {
		t.Error("getMetadata(nil) should return nil")
	}

	// Test getMetadata with non-existent PubSub
	var pubsub *redis.PubSub = nil
	meta = pool.getMetadata(pubsub)
	if meta != nil {
		t.Error("getMetadata(non-existent) should return nil")
	}
}

func TestPubSubPool_RemovePubSub(t *testing.T) {
	cfg := defaultConfig()
	var clusterClient *redis.ClusterClient = nil
	pool := newPubSubPool(clusterClient, cfg)

	// Test removePubSub with nil (should not panic)
	pool.removePubSub(nil)

	// Test removePubSub with non-existent PubSub (should not panic)
	var pubsub *redis.PubSub = nil
	pool.removePubSub(pubsub)
}

func TestPubSubPool_CloseAll(t *testing.T) {
	cfg := defaultConfig()
	var clusterClient *redis.ClusterClient = nil
	pool := newPubSubPool(clusterClient, cfg)

	// Test closeAll with empty pool
	err := pool.closeAll()
	if err != nil {
		t.Errorf("closeAll() on empty pool returned error: %v", err)
	}
}
