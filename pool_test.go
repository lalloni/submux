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

func TestPubSubPool_InvalidateHashslot(t *testing.T) {
	cfg := defaultConfig()
	pool := newPubSubPool(nil, cfg)

	// Setup: add some entries to hashslotPubSubs
	pool.mu.Lock()
	pool.hashslotPubSubs[100] = []*redis.PubSub{nil} // dummy entries
	pool.hashslotPubSubs[200] = []*redis.PubSub{nil}
	pool.mu.Unlock()

	// Test invalidating existing hashslot
	pool.invalidateHashslot(100)

	pool.mu.RLock()
	_, exists := pool.hashslotPubSubs[100]
	pool.mu.RUnlock()

	if exists {
		t.Error("hashslot 100 should be removed after invalidation")
	}

	// Verify other hashslot is not affected
	pool.mu.RLock()
	_, exists = pool.hashslotPubSubs[200]
	pool.mu.RUnlock()

	if !exists {
		t.Error("hashslot 200 should still exist")
	}

	// Test invalidating non-existent hashslot (should not panic)
	pool.invalidateHashslot(999)

	// Test double invalidation (idempotent)
	pool.invalidateHashslot(100)
}

func TestPubSubMetadata_PendingSubscriptions(t *testing.T) {
	meta := &pubSubMetadata{
		subscriptions:        make(map[string][]*subscription),
		pendingSubscriptions: make(map[string]*subscription),
		state:                connStateActive,
		cmdCh:                make(chan *command, 10),
		done:                 make(chan struct{}),
	}

	sub := &subscription{
		channel:   "test-channel",
		state:     subStatePending,
		confirmCh: make(chan error, 1),
	}

	// Test addPendingSubscription
	meta.addPendingSubscription(sub)

	// Test getPendingSubscription
	pending := meta.getPendingSubscription("test-channel")
	if pending != sub {
		t.Error("getPendingSubscription returned wrong subscription")
	}

	// Test getPendingSubscription for non-existent
	pending = meta.getPendingSubscription("nonexistent")
	if pending != nil {
		t.Error("getPendingSubscription should return nil for non-existent channel")
	}

	// Test removePendingSubscription
	meta.removePendingSubscription("test-channel")
	pending = meta.getPendingSubscription("test-channel")
	if pending != nil {
		t.Error("getPendingSubscription should return nil after removal")
	}

	// Test removePendingSubscription for non-existent (should not panic)
	meta.removePendingSubscription("nonexistent")
}

// Helper to create a mock pubSubMetadata for load balancing tests
func newMockMetadataWithSubs(nodeAddr string, state connectionState, subCount int) *pubSubMetadata {
	meta := &pubSubMetadata{
		nodeAddr:             nodeAddr,
		subscriptions:        make(map[string][]*subscription),
		pendingSubscriptions: make(map[string]*subscription),
		state:                state,
		cmdCh:                make(chan *command, 10),
		done:                 make(chan struct{}),
	}

	// Add dummy subscriptions
	for i := 0; i < subCount; i++ {
		ch := "channel-" + string(rune('a'+i))
		meta.subscriptions[ch] = []*subscription{{channel: ch}}
	}

	return meta
}

func TestPubSubPool_SelectLeastLoaded(t *testing.T) {
	// Test the load balancing selection logic by examining getPubSubForHashslot behavior
	// We can't easily call getPubSubForHashslot without a real Redis connection,
	// but we can test the underlying selection pattern

	cfg := defaultConfig()
	pool := newPubSubPool(nil, cfg)

	// Create mock PubSubs and metadata with different subscription counts
	pubsub1 := &redis.PubSub{}
	pubsub2 := &redis.PubSub{}
	pubsub3 := &redis.PubSub{}

	meta1 := newMockMetadataWithSubs("node1:7000", connStateActive, 10)
	meta2 := newMockMetadataWithSubs("node1:7000", connStateActive, 5) // Least loaded
	meta3 := newMockMetadataWithSubs("node1:7000", connStateActive, 15)

	// Setup pool state
	pool.mu.Lock()
	pool.hashslotPubSubs[100] = []*redis.PubSub{pubsub1, pubsub2, pubsub3}
	pool.pubSubMetadata[pubsub1] = meta1
	pool.pubSubMetadata[pubsub2] = meta2
	pool.pubSubMetadata[pubsub3] = meta3
	pool.mu.Unlock()

	// Manually test the selection logic (simulating getPubSubForHashslot internal logic)
	pool.mu.RLock()
	pubsubs := pool.hashslotPubSubs[100]

	var selectedPubSub *redis.PubSub
	minSubs := int(^uint(0) >> 1) // Max int

	for _, ps := range pubsubs {
		meta := pool.pubSubMetadata[ps]
		if meta == nil || meta.getState() != connStateActive {
			continue
		}
		count := meta.subscriptionCount()
		if count < minSubs {
			minSubs = count
			selectedPubSub = ps
		}
	}
	pool.mu.RUnlock()

	// Should select pubsub2 (least loaded with 5 subscriptions)
	if selectedPubSub != pubsub2 {
		t.Errorf("Expected least loaded pubsub2 to be selected, got different pubsub")
	}
	if minSubs != 5 {
		t.Errorf("minSubs = %d, expected 5", minSubs)
	}
}

func TestPubSubPool_SelectLeastLoaded_SkipsInactive(t *testing.T) {
	cfg := defaultConfig()
	pool := newPubSubPool(nil, cfg)

	pubsub1 := &redis.PubSub{}
	pubsub2 := &redis.PubSub{}

	meta1 := newMockMetadataWithSubs("node1:7000", connStateFailed, 2) // Failed, should be skipped
	meta2 := newMockMetadataWithSubs("node1:7000", connStateActive, 10)

	pool.mu.Lock()
	pool.hashslotPubSubs[100] = []*redis.PubSub{pubsub1, pubsub2}
	pool.pubSubMetadata[pubsub1] = meta1
	pool.pubSubMetadata[pubsub2] = meta2
	pool.mu.Unlock()

	// Simulate selection logic
	pool.mu.RLock()
	pubsubs := pool.hashslotPubSubs[100]

	var selectedPubSub *redis.PubSub
	minSubs := int(^uint(0) >> 1)

	for _, ps := range pubsubs {
		meta := pool.pubSubMetadata[ps]
		if meta == nil || meta.getState() != connStateActive {
			continue
		}
		count := meta.subscriptionCount()
		if count < minSubs {
			minSubs = count
			selectedPubSub = ps
		}
	}
	pool.mu.RUnlock()

	// Should select pubsub2 (only active one)
	if selectedPubSub != pubsub2 {
		t.Errorf("Expected active pubsub2 to be selected")
	}
}

func TestPubSubPool_SelectLeastLoaded_AllInactive(t *testing.T) {
	cfg := defaultConfig()
	pool := newPubSubPool(nil, cfg)

	pubsub1 := &redis.PubSub{}
	pubsub2 := &redis.PubSub{}

	meta1 := newMockMetadataWithSubs("node1:7000", connStateFailed, 2)
	meta2 := newMockMetadataWithSubs("node1:7000", connStateClosed, 5)

	pool.mu.Lock()
	pool.hashslotPubSubs[100] = []*redis.PubSub{pubsub1, pubsub2}
	pool.pubSubMetadata[pubsub1] = meta1
	pool.pubSubMetadata[pubsub2] = meta2
	pool.mu.Unlock()

	// Simulate selection logic
	pool.mu.RLock()
	pubsubs := pool.hashslotPubSubs[100]

	var selectedPubSub *redis.PubSub
	minSubs := int(^uint(0) >> 1)

	for _, ps := range pubsubs {
		meta := pool.pubSubMetadata[ps]
		if meta == nil || meta.getState() != connStateActive {
			continue
		}
		count := meta.subscriptionCount()
		if count < minSubs {
			minSubs = count
			selectedPubSub = ps
		}
	}
	pool.mu.RUnlock()

	// Should be nil (no active connections)
	if selectedPubSub != nil {
		t.Error("Expected no pubsub to be selected when all are inactive")
	}
}

func TestPubSubPool_SelectLeastLoaded_NilMetadata(t *testing.T) {
	cfg := defaultConfig()
	pool := newPubSubPool(nil, cfg)

	pubsub1 := &redis.PubSub{}
	pubsub2 := &redis.PubSub{}

	meta2 := newMockMetadataWithSubs("node1:7000", connStateActive, 5)

	pool.mu.Lock()
	pool.hashslotPubSubs[100] = []*redis.PubSub{pubsub1, pubsub2}
	// pubsub1 has no metadata (nil)
	pool.pubSubMetadata[pubsub2] = meta2
	pool.mu.Unlock()

	// Simulate selection logic
	pool.mu.RLock()
	pubsubs := pool.hashslotPubSubs[100]

	var selectedPubSub *redis.PubSub
	minSubs := int(^uint(0) >> 1)

	for _, ps := range pubsubs {
		meta := pool.pubSubMetadata[ps]
		if meta == nil || meta.getState() != connStateActive {
			continue // Should skip pubsub1 which has nil metadata
		}
		count := meta.subscriptionCount()
		if count < minSubs {
			minSubs = count
			selectedPubSub = ps
		}
	}
	pool.mu.RUnlock()

	// Should select pubsub2 (pubsub1 has nil metadata)
	if selectedPubSub != pubsub2 {
		t.Error("Expected pubsub2 to be selected when pubsub1 has nil metadata")
	}
}

func TestPubSubPool_SelectLeastLoaded_EqualLoad(t *testing.T) {
	cfg := defaultConfig()
	pool := newPubSubPool(nil, cfg)

	pubsub1 := &redis.PubSub{}
	pubsub2 := &redis.PubSub{}
	pubsub3 := &redis.PubSub{}

	// All have equal subscription counts
	meta1 := newMockMetadataWithSubs("node1:7000", connStateActive, 5)
	meta2 := newMockMetadataWithSubs("node1:7000", connStateActive, 5)
	meta3 := newMockMetadataWithSubs("node1:7000", connStateActive, 5)

	pool.mu.Lock()
	pool.hashslotPubSubs[100] = []*redis.PubSub{pubsub1, pubsub2, pubsub3}
	pool.pubSubMetadata[pubsub1] = meta1
	pool.pubSubMetadata[pubsub2] = meta2
	pool.pubSubMetadata[pubsub3] = meta3
	pool.mu.Unlock()

	// Simulate selection logic
	pool.mu.RLock()
	pubsubs := pool.hashslotPubSubs[100]

	var selectedPubSub *redis.PubSub
	minSubs := int(^uint(0) >> 1)

	for _, ps := range pubsubs {
		meta := pool.pubSubMetadata[ps]
		if meta == nil || meta.getState() != connStateActive {
			continue
		}
		count := meta.subscriptionCount()
		if count < minSubs {
			minSubs = count
			selectedPubSub = ps
		}
	}
	pool.mu.RUnlock()

	// Should select one of them (first one found with equal min)
	if selectedPubSub == nil {
		t.Error("Expected a pubsub to be selected")
	}
	if minSubs != 5 {
		t.Errorf("minSubs = %d, expected 5", minSubs)
	}
}

func TestPubSubPool_EmptyHashslot(t *testing.T) {
	cfg := defaultConfig()
	pool := newPubSubPool(nil, cfg)

	// Simulate selection with empty hashslot
	pool.mu.RLock()
	pubsubs := pool.hashslotPubSubs[100] // Non-existent hashslot
	pool.mu.RUnlock()

	if len(pubsubs) != 0 {
		t.Errorf("Expected empty list for non-existent hashslot, got %d", len(pubsubs))
	}
}

// Edge case tests for pool operations

func TestPubSubMetadata_RemoveNonExistentSubscription(t *testing.T) {
	meta := &pubSubMetadata{
		subscriptions:        make(map[string][]*subscription),
		pendingSubscriptions: make(map[string]*subscription),
		state:                connStateActive,
		cmdCh:                make(chan *command, 10),
		done:                 make(chan struct{}),
	}

	// Add a subscription
	sub1 := &subscription{channel: "channel1", confirmCh: make(chan error, 1)}
	meta.addSubscription(sub1)

	// Try to remove a non-existent subscription (should not panic)
	nonExistent := &subscription{channel: "channel1", confirmCh: make(chan error, 1)}
	meta.removeSubscription(nonExistent)

	// Original should still be there
	subs := meta.getSubscriptions("channel1")
	if len(subs) != 1 {
		t.Errorf("After removing non-existent, got %d subscriptions, want 1", len(subs))
	}
}

func TestPubSubMetadata_RemoveFromEmptyChannel(t *testing.T) {
	meta := &pubSubMetadata{
		subscriptions:        make(map[string][]*subscription),
		pendingSubscriptions: make(map[string]*subscription),
		state:                connStateActive,
		cmdCh:                make(chan *command, 10),
		done:                 make(chan struct{}),
	}

	// Try to remove from a channel that doesn't exist (should not panic)
	sub := &subscription{channel: "nonexistent", confirmCh: make(chan error, 1)}
	meta.removeSubscription(sub)
}

func TestPubSubMetadata_MultipleSubscriptionsSameChannel(t *testing.T) {
	meta := &pubSubMetadata{
		subscriptions:        make(map[string][]*subscription),
		pendingSubscriptions: make(map[string]*subscription),
		state:                connStateActive,
		cmdCh:                make(chan *command, 10),
		done:                 make(chan struct{}),
	}

	// Add multiple subscriptions to the same channel
	subs := make([]*subscription, 10)
	for i := 0; i < 10; i++ {
		subs[i] = &subscription{channel: "channel1", confirmCh: make(chan error, 1)}
		meta.addSubscription(subs[i])
	}

	if meta.subscriptionCount() != 10 {
		t.Errorf("subscriptionCount = %d, want 10", meta.subscriptionCount())
	}

	// Remove from middle
	meta.removeSubscription(subs[5])
	if meta.subscriptionCount() != 9 {
		t.Errorf("After removal, subscriptionCount = %d, want 9", meta.subscriptionCount())
	}

	// Remove first
	meta.removeSubscription(subs[0])
	if meta.subscriptionCount() != 8 {
		t.Errorf("After removal, subscriptionCount = %d, want 8", meta.subscriptionCount())
	}

	// Remove last
	meta.removeSubscription(subs[9])
	if meta.subscriptionCount() != 7 {
		t.Errorf("After removal, subscriptionCount = %d, want 7", meta.subscriptionCount())
	}
}

func TestPubSubMetadata_SubscriptionCountZero(t *testing.T) {
	meta := &pubSubMetadata{
		subscriptions:        make(map[string][]*subscription),
		pendingSubscriptions: make(map[string]*subscription),
		state:                connStateActive,
		cmdCh:                make(chan *command, 10),
		done:                 make(chan struct{}),
	}

	if meta.subscriptionCount() != 0 {
		t.Errorf("Empty metadata subscriptionCount = %d, want 0", meta.subscriptionCount())
	}
}

func TestPubSubMetadata_GetSubscriptionsEmptyChannel(t *testing.T) {
	meta := &pubSubMetadata{
		subscriptions:        make(map[string][]*subscription),
		pendingSubscriptions: make(map[string]*subscription),
		state:                connStateActive,
		cmdCh:                make(chan *command, 10),
		done:                 make(chan struct{}),
	}

	subs := meta.getSubscriptions("nonexistent")
	if len(subs) != 0 {
		t.Errorf("getSubscriptions for non-existent channel = %d, want 0", len(subs))
	}
}

func TestPubSubMetadata_GetAllSubscriptionsEmpty(t *testing.T) {
	meta := &pubSubMetadata{
		subscriptions:        make(map[string][]*subscription),
		pendingSubscriptions: make(map[string]*subscription),
		state:                connStateActive,
		cmdCh:                make(chan *command, 10),
		done:                 make(chan struct{}),
	}

	all := meta.getAllSubscriptions()
	if len(all) != 0 {
		t.Errorf("getAllSubscriptions on empty = %d, want 0", len(all))
	}
}

func TestPubSubMetadata_ConnectionStateTransitions(t *testing.T) {
	meta := &pubSubMetadata{
		subscriptions:        make(map[string][]*subscription),
		pendingSubscriptions: make(map[string]*subscription),
		state:                connStateActive,
		cmdCh:                make(chan *command, 10),
		done:                 make(chan struct{}),
	}

	// Active -> Failed
	meta.setState(connStateFailed)
	if meta.getState() != connStateFailed {
		t.Errorf("state = %v, want connStateFailed", meta.getState())
	}

	// Failed -> Closed
	meta.setState(connStateClosed)
	if meta.getState() != connStateClosed {
		t.Errorf("state = %v, want connStateClosed", meta.getState())
	}
}

func TestPubSubMetadata_PendingSubscriptionOverwrite(t *testing.T) {
	meta := &pubSubMetadata{
		subscriptions:        make(map[string][]*subscription),
		pendingSubscriptions: make(map[string]*subscription),
		state:                connStateActive,
		cmdCh:                make(chan *command, 10),
		done:                 make(chan struct{}),
	}

	sub1 := &subscription{channel: "test", state: subStatePending, confirmCh: make(chan error, 1)}
	sub2 := &subscription{channel: "test", state: subStatePending, confirmCh: make(chan error, 1)}

	meta.addPendingSubscription(sub1)
	meta.addPendingSubscription(sub2)

	// Second should overwrite first
	pending := meta.getPendingSubscription("test")
	if pending != sub2 {
		t.Error("Second pending subscription should overwrite first")
	}
}

func TestPubSubMetadata_RemovePendingOnRemoveSubscription(t *testing.T) {
	meta := &pubSubMetadata{
		subscriptions:        make(map[string][]*subscription),
		pendingSubscriptions: make(map[string]*subscription),
		state:                connStateActive,
		cmdCh:                make(chan *command, 10),
		done:                 make(chan struct{}),
	}

	sub := &subscription{channel: "test", state: subStatePending, confirmCh: make(chan error, 1)}
	meta.addSubscription(sub)
	meta.addPendingSubscription(sub)

	// Verify both are present
	if meta.getPendingSubscription("test") != sub {
		t.Error("Pending subscription should be present")
	}

	// Remove subscription should also remove from pending
	meta.removeSubscription(sub)

	// Pending should be removed
	if meta.getPendingSubscription("test") != nil {
		t.Error("Pending subscription should be removed when subscription is removed")
	}
}

func TestPubSubPool_ConcurrentAccess(t *testing.T) {
	cfg := defaultConfig()
	pool := newPubSubPool(nil, cfg)

	done := make(chan bool)
	iterations := 100

	// Writer goroutine for hashslotPubSubs
	go func() {
		for i := 0; i < iterations; i++ {
			pool.mu.Lock()
			pool.hashslotPubSubs[i%16384] = []*redis.PubSub{}
			pool.mu.Unlock()
		}
		done <- true
	}()

	// Reader goroutine
	go func() {
		for i := 0; i < iterations; i++ {
			pool.mu.RLock()
			_ = pool.hashslotPubSubs[i%16384]
			pool.mu.RUnlock()
		}
		done <- true
	}()

	// Invalidation goroutine
	go func() {
		for i := 0; i < iterations; i++ {
			pool.invalidateHashslot(i % 16384)
		}
		done <- true
	}()

	// Wait for all goroutines
	<-done
	<-done
	<-done
}

func TestPubSubPool_InvalidateAllHashslots(t *testing.T) {
	cfg := defaultConfig()
	pool := newPubSubPool(nil, cfg)

	// Add entries for multiple hashslots
	pool.mu.Lock()
	for i := 0; i < 100; i++ {
		pool.hashslotPubSubs[i] = []*redis.PubSub{}
	}
	pool.mu.Unlock()

	// Invalidate all
	for i := 0; i < 100; i++ {
		pool.invalidateHashslot(i)
	}

	pool.mu.RLock()
	count := len(pool.hashslotPubSubs)
	pool.mu.RUnlock()

	if count != 0 {
		t.Errorf("After invalidating all, hashslotPubSubs count = %d, want 0", count)
	}
}

func TestPubSubPool_NodePubSubsManagement(t *testing.T) {
	cfg := defaultConfig()
	pool := newPubSubPool(nil, cfg)

	// Add pubsubs for multiple nodes
	pubsub1 := &redis.PubSub{}
	pubsub2 := &redis.PubSub{}

	pool.mu.Lock()
	pool.nodePubSubs["node1:7000"] = []*redis.PubSub{pubsub1}
	pool.nodePubSubs["node2:7000"] = []*redis.PubSub{pubsub2}
	pool.mu.Unlock()

	pool.mu.RLock()
	node1Subs := pool.nodePubSubs["node1:7000"]
	node2Subs := pool.nodePubSubs["node2:7000"]
	pool.mu.RUnlock()

	if len(node1Subs) != 1 {
		t.Errorf("node1 pubsubs = %d, want 1", len(node1Subs))
	}
	if len(node2Subs) != 1 {
		t.Errorf("node2 pubsubs = %d, want 1", len(node2Subs))
	}
}

func TestPubSubMetadata_SendCommandWithZeroBufferChannel(t *testing.T) {
	// Use unbuffered channel - will block unless there's a receiver
	meta := &pubSubMetadata{
		subscriptions:        make(map[string][]*subscription),
		pendingSubscriptions: make(map[string]*subscription),
		state:                connStateActive,
		cmdCh:                make(chan *command), // Unbuffered
		done:                 make(chan struct{}),
	}

	cmd := &command{
		cmd:      cmdSubscribe,
		args:     []any{"channel1"},
		response: make(chan error, 1),
	}

	// Create a receiver before sending
	received := make(chan bool)
	go func() {
		<-meta.cmdCh
		received <- true
	}()

	ctx := context.Background()
	err := meta.sendCommand(ctx, cmd)
	if err != nil {
		t.Errorf("sendCommand returned error: %v", err)
	}

	<-received
}

func TestPubSubMetadata_CloseWithPendingSubscriptions(t *testing.T) {
	meta := &pubSubMetadata{
		subscriptions:        make(map[string][]*subscription),
		pendingSubscriptions: make(map[string]*subscription),
		state:                connStateActive,
		cmdCh:                make(chan *command, 10),
		done:                 make(chan struct{}),
	}

	// Add pending subscriptions
	sub := &subscription{channel: "test", state: subStatePending, confirmCh: make(chan error, 1)}
	meta.addPendingSubscription(sub)

	// Close should work even with pending subscriptions
	err := meta.close()
	if err != nil {
		t.Errorf("close() returned error: %v", err)
	}
	if meta.getState() != connStateClosed {
		t.Errorf("state = %v, want connStateClosed", meta.getState())
	}
}
