package submux

import (
	"context"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

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
	minSubs := math.MaxInt // Max int

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
	minSubs := math.MaxInt

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
	minSubs := math.MaxInt

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
	minSubs := math.MaxInt

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
	minSubs := math.MaxInt

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

// Tests for getKeyForSlot - pure function that generates a key hashing to specific slot

func TestGetKeyForSlot_ReturnsCorrectSlot(t *testing.T) {
	testSlots := []int{0, 1, 100, 1000, 5000, 10000, 16383}

	for _, slot := range testSlots {
		t.Run("slot_"+string(rune('0'+slot%10)), func(t *testing.T) {
			key := getKeyForSlot(slot)
			actualSlot := Hashslot(key)
			if actualSlot != slot {
				t.Errorf("getKeyForSlot(%d) returned key %q which hashes to %d", slot, key, actualSlot)
			}
		})
	}
}

func TestGetKeyForSlot_ConsistentResults(t *testing.T) {
	// Same slot should return same key
	slot := 5000
	key1 := getKeyForSlot(slot)
	key2 := getKeyForSlot(slot)
	if key1 != key2 {
		t.Errorf("getKeyForSlot(%d) returned different keys: %q and %q", slot, key1, key2)
	}
}

func TestGetKeyForSlot_BoundarySlots(t *testing.T) {
	// Test boundary slots
	tests := []int{0, 16383}
	for _, slot := range tests {
		key := getKeyForSlot(slot)
		if Hashslot(key) != slot {
			t.Errorf("getKeyForSlot(%d) = %q, hashes to %d", slot, key, Hashslot(key))
		}
	}
}

// Tests for removePubSub with actual data

func TestPubSubPool_RemovePubSub_WithData(t *testing.T) {
	cfg := defaultConfig()
	pool := newPubSubPool(nil, cfg)

	// Create mock PubSub and metadata
	pubsub := &redis.PubSub{}
	meta := &pubSubMetadata{
		nodeAddr:             "node1:7000",
		subscriptions:        make(map[string][]*subscription),
		pendingSubscriptions: make(map[string]*subscription),
		state:                connStateActive,
		cmdCh:                make(chan *command, 10),
		done:                 make(chan struct{}),
	}

	// Setup pool state
	pool.mu.Lock()
	pool.nodePubSubs["node1:7000"] = []*redis.PubSub{pubsub}
	pool.hashslotPubSubs[100] = []*redis.PubSub{pubsub}
	pool.hashslotPubSubs[200] = []*redis.PubSub{pubsub}
	pool.pubSubMetadata[pubsub] = meta
	pool.mu.Unlock()

	// Remove the PubSub
	pool.removePubSub(pubsub)

	// Verify it's removed from all maps
	pool.mu.RLock()
	defer pool.mu.RUnlock()

	if _, ok := pool.pubSubMetadata[pubsub]; ok {
		t.Error("pubsub should be removed from pubSubMetadata")
	}
	if _, ok := pool.nodePubSubs["node1:7000"]; ok {
		t.Error("node should be removed from nodePubSubs when last pubsub is removed")
	}
	if _, ok := pool.hashslotPubSubs[100]; ok {
		t.Error("hashslot 100 should be removed from hashslotPubSubs")
	}
	if _, ok := pool.hashslotPubSubs[200]; ok {
		t.Error("hashslot 200 should be removed from hashslotPubSubs")
	}
}

func TestPubSubPool_RemovePubSub_KeepsOtherPubSubs(t *testing.T) {
	cfg := defaultConfig()
	pool := newPubSubPool(nil, cfg)

	pubsub1 := &redis.PubSub{}
	pubsub2 := &redis.PubSub{}

	meta1 := &pubSubMetadata{
		nodeAddr:             "node1:7000",
		subscriptions:        make(map[string][]*subscription),
		pendingSubscriptions: make(map[string]*subscription),
		state:                connStateActive,
		cmdCh:                make(chan *command, 10),
		done:                 make(chan struct{}),
	}
	meta2 := &pubSubMetadata{
		nodeAddr:             "node1:7000",
		subscriptions:        make(map[string][]*subscription),
		pendingSubscriptions: make(map[string]*subscription),
		state:                connStateActive,
		cmdCh:                make(chan *command, 10),
		done:                 make(chan struct{}),
	}

	pool.mu.Lock()
	pool.nodePubSubs["node1:7000"] = []*redis.PubSub{pubsub1, pubsub2}
	pool.hashslotPubSubs[100] = []*redis.PubSub{pubsub1, pubsub2}
	pool.pubSubMetadata[pubsub1] = meta1
	pool.pubSubMetadata[pubsub2] = meta2
	pool.mu.Unlock()

	// Remove only pubsub1
	pool.removePubSub(pubsub1)

	pool.mu.RLock()
	defer pool.mu.RUnlock()

	// pubsub2 should still exist
	if _, ok := pool.pubSubMetadata[pubsub2]; !ok {
		t.Error("pubsub2 should still be in pubSubMetadata")
	}
	if len(pool.nodePubSubs["node1:7000"]) != 1 {
		t.Errorf("nodePubSubs should have 1 pubsub, got %d", len(pool.nodePubSubs["node1:7000"]))
	}
	if len(pool.hashslotPubSubs[100]) != 1 {
		t.Errorf("hashslotPubSubs should have 1 pubsub, got %d", len(pool.hashslotPubSubs[100]))
	}
}

func TestPubSubPool_RemovePubSub_FromMiddle(t *testing.T) {
	cfg := defaultConfig()
	pool := newPubSubPool(nil, cfg)

	pubsubs := make([]*redis.PubSub, 3)
	for i := range pubsubs {
		pubsubs[i] = &redis.PubSub{}
	}

	metas := make([]*pubSubMetadata, 3)
	for i := range metas {
		metas[i] = &pubSubMetadata{
			nodeAddr:             "node1:7000",
			subscriptions:        make(map[string][]*subscription),
			pendingSubscriptions: make(map[string]*subscription),
			state:                connStateActive,
			cmdCh:                make(chan *command, 10),
			done:                 make(chan struct{}),
		}
	}

	// Store pointers before any modification (slice backing array gets modified)
	first := pubsubs[0]
	middle := pubsubs[1]
	last := pubsubs[2]

	// Use separate slices for each map to avoid shared backing array issues
	nodePubSubsCopy := make([]*redis.PubSub, len(pubsubs))
	copy(nodePubSubsCopy, pubsubs)
	hashslotPubSubsCopy := make([]*redis.PubSub, len(pubsubs))
	copy(hashslotPubSubsCopy, pubsubs)

	pool.mu.Lock()
	pool.nodePubSubs["node1:7000"] = nodePubSubsCopy
	pool.hashslotPubSubs[100] = hashslotPubSubsCopy
	for i, ps := range pubsubs {
		pool.pubSubMetadata[ps] = metas[i]
	}
	pool.mu.Unlock()

	// Remove the middle one
	pool.removePubSub(middle)

	pool.mu.RLock()
	defer pool.mu.RUnlock()

	if len(pool.nodePubSubs["node1:7000"]) != 2 {
		t.Errorf("nodePubSubs should have 2 pubsubs, got %d", len(pool.nodePubSubs["node1:7000"]))
	}

	// Verify first and last still exist
	if _, ok := pool.pubSubMetadata[first]; !ok {
		t.Error("first pubsub should still exist")
	}
	if _, ok := pool.pubSubMetadata[last]; !ok {
		t.Error("last pubsub should still exist")
	}
	if _, ok := pool.pubSubMetadata[middle]; ok {
		t.Error("middle pubsub should be removed")
	}
}

// Tests for createPubSubToNode

func TestCreatePubSubToNode_NilTopologyMonitor(t *testing.T) {
	cfg := defaultConfig()
	pool := newPubSubPool(nil, cfg)
	// topologyMonitor is nil by default

	ctx := context.Background()
	_, err := pool.createPubSubToNode(ctx, "node1:7000")

	if err == nil {
		t.Error("expected error when topology monitor is nil")
	}
	if err.Error() != "topology monitor not initialized" {
		t.Errorf("error = %q, want %q", err.Error(), "topology monitor not initialized")
	}
}

// Tests for addSubscriptionAndCheckFirst method

func TestPubSubMetadata_AddSubscriptionAndCheckFirst_FirstSubscription(t *testing.T) {
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

	needsSubscribe, existingPending := meta.addSubscriptionAndCheckFirst(sub, "test-channel")

	// First subscription should require SUBSCRIBE command
	if !needsSubscribe {
		t.Error("first subscription should return needsSubscribe=true")
	}
	if existingPending != nil {
		t.Error("first subscription should return existingPending=nil")
	}

	// Subscription should be added to both lists
	subs := meta.getSubscriptions("test-channel")
	if len(subs) != 1 {
		t.Errorf("expected 1 subscription, got %d", len(subs))
	}

	pending := meta.getPendingSubscription("test-channel")
	if pending != sub {
		t.Error("subscription should be in pending list")
	}
}

func TestPubSubMetadata_AddSubscriptionAndCheckFirst_SecondWhilePending(t *testing.T) {
	meta := &pubSubMetadata{
		subscriptions:        make(map[string][]*subscription),
		pendingSubscriptions: make(map[string]*subscription),
		state:                connStateActive,
		cmdCh:                make(chan *command, 10),
		done:                 make(chan struct{}),
	}

	// Add first subscription (this creates the pending entry)
	sub1 := &subscription{
		channel:   "test-channel",
		state:     subStatePending,
		confirmCh: make(chan error, 1),
		doneCh:    make(chan struct{}),
	}
	needsSubscribe1, _ := meta.addSubscriptionAndCheckFirst(sub1, "test-channel")
	if !needsSubscribe1 {
		t.Fatal("first subscription should need subscribe")
	}

	// Add second subscription while first is still pending
	sub2 := &subscription{
		channel:   "test-channel",
		state:     subStatePending,
		confirmCh: make(chan error, 1),
	}
	needsSubscribe2, existingPending := meta.addSubscriptionAndCheckFirst(sub2, "test-channel")

	// Second should NOT need subscribe, but should get existing pending
	if needsSubscribe2 {
		t.Error("second subscription should NOT need subscribe")
	}
	if existingPending != sub1 {
		t.Error("second subscription should get existingPending=sub1")
	}

	// Both subscriptions should be in the list
	subs := meta.getSubscriptions("test-channel")
	if len(subs) != 2 {
		t.Errorf("expected 2 subscriptions, got %d", len(subs))
	}
}

func TestPubSubMetadata_AddSubscriptionAndCheckFirst_AfterActive(t *testing.T) {
	meta := &pubSubMetadata{
		subscriptions:        make(map[string][]*subscription),
		pendingSubscriptions: make(map[string]*subscription),
		state:                connStateActive,
		cmdCh:                make(chan *command, 10),
		done:                 make(chan struct{}),
	}

	// Add first subscription and confirm it
	sub1 := &subscription{
		channel:   "test-channel",
		state:     subStateActive, // Already active
		confirmCh: make(chan error, 1),
	}
	meta.addSubscription(sub1)
	// Don't add to pending - simulating already confirmed

	// Add second subscription
	sub2 := &subscription{
		channel:   "test-channel",
		state:     subStatePending,
		confirmCh: make(chan error, 1),
	}
	needsSubscribe, existingPending := meta.addSubscriptionAndCheckFirst(sub2, "test-channel")

	// Should NOT need subscribe (channel already has active subs)
	// Should NOT have existingPending (no one is pending)
	if needsSubscribe {
		t.Error("should NOT need subscribe for already active channel")
	}
	if existingPending != nil {
		t.Error("should NOT have existingPending when channel is already active")
	}

	// Both subscriptions should be in the list
	subs := meta.getSubscriptions("test-channel")
	if len(subs) != 2 {
		t.Errorf("expected 2 subscriptions, got %d", len(subs))
	}
}

func TestPubSubMetadata_AddSubscriptionAndCheckFirst_ConcurrentAccess(t *testing.T) {
	meta := &pubSubMetadata{
		subscriptions:        make(map[string][]*subscription),
		pendingSubscriptions: make(map[string]*subscription),
		state:                connStateActive,
		cmdCh:                make(chan *command, 100),
		done:                 make(chan struct{}),
	}

	numGoroutines := 100
	var needsSubscribeCount int64
	var existingPendingCount int64

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	// Start many concurrent subscriptions to the same channel
	for i := range numGoroutines {
		go func(idx int) {
			defer wg.Done()

			sub := &subscription{
				channel:   "test-channel",
				state:     subStatePending,
				confirmCh: make(chan error, 1),
				doneCh:    make(chan struct{}),
			}

			needsSubscribe, existingPending := meta.addSubscriptionAndCheckFirst(sub, "test-channel")

			if needsSubscribe {
				atomic.AddInt64(&needsSubscribeCount, 1)
			}
			if existingPending != nil {
				atomic.AddInt64(&existingPendingCount, 1)
			}
		}(i)
	}

	wg.Wait()

	// Exactly ONE goroutine should have needsSubscribe=true
	if needsSubscribeCount != 1 {
		t.Errorf("expected exactly 1 goroutine with needsSubscribe=true, got %d", needsSubscribeCount)
	}

	// All others should either have existingPending or be after the first was confirmed
	// (In this test, they all get existingPending since we don't remove from pending)
	expectedPendingCount := int64(numGoroutines - 1)
	if existingPendingCount != expectedPendingCount {
		t.Errorf("expected %d goroutines with existingPending, got %d", expectedPendingCount, existingPendingCount)
	}

	// All subscriptions should be registered
	subs := meta.getSubscriptions("test-channel")
	if len(subs) != numGoroutines {
		t.Errorf("expected %d subscriptions, got %d", numGoroutines, len(subs))
	}
}

// Tests for edge cases in pool management

func TestPubSubMetadata_SendCommand_ClosedDoneChannel(t *testing.T) {
	meta := &pubSubMetadata{
		subscriptions:        make(map[string][]*subscription),
		pendingSubscriptions: make(map[string]*subscription),
		state:                connStateActive,
		cmdCh:                make(chan *command), // Unbuffered - will block on send
		done:                 make(chan struct{}),
		logger:               defaultConfig().logger,
	}

	// Close the done channel first
	close(meta.done)

	cmd := &command{
		cmd:      cmdSubscribe,
		args:     []any{"test-channel"},
		response: make(chan error, 1),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// With unbuffered cmdCh and closed done, should return "pubsub closed" error
	err := meta.sendCommand(ctx, cmd)
	if err == nil {
		t.Error("sendCommand should return error when done channel is closed")
	}
	// Error could be either context timeout or pubsub closed (race between select cases)
}

func TestPubSubMetadata_SendCommand_ContextTimeout(t *testing.T) {
	meta := &pubSubMetadata{
		subscriptions:        make(map[string][]*subscription),
		pendingSubscriptions: make(map[string]*subscription),
		state:                connStateActive,
		cmdCh:                make(chan *command), // Unbuffered - will block
		done:                 make(chan struct{}),
		logger:               defaultConfig().logger,
	}

	cmd := &command{
		cmd:      cmdSubscribe,
		args:     []any{"test-channel"},
		response: make(chan error, 1),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := meta.sendCommand(ctx, cmd)
	if err != context.DeadlineExceeded {
		t.Errorf("sendCommand should return context.DeadlineExceeded, got %v", err)
	}
}

func TestPubSubMetadata_StateTransitions(t *testing.T) {
	meta := &pubSubMetadata{
		subscriptions:        make(map[string][]*subscription),
		pendingSubscriptions: make(map[string]*subscription),
		state:                connStateActive,
		cmdCh:                make(chan *command, 10),
		done:                 make(chan struct{}),
	}

	// Test state transitions
	if meta.getState() != connStateActive {
		t.Error("initial state should be Active")
	}

	meta.setState(connStateFailed)
	if meta.getState() != connStateFailed {
		t.Error("state should be Failed")
	}

	meta.setState(connStateClosed)
	if meta.getState() != connStateClosed {
		t.Error("state should be Closed")
	}
}

func TestPubSubMetadata_GetAllSubscriptions_Empty(t *testing.T) {
	meta := &pubSubMetadata{
		subscriptions:        make(map[string][]*subscription),
		pendingSubscriptions: make(map[string]*subscription),
		state:                connStateActive,
	}

	subs := meta.getAllSubscriptions()
	if len(subs) != 0 {
		t.Errorf("expected 0 subscriptions, got %d", len(subs))
	}
}

func TestPubSubMetadata_GetAllSubscriptions_Multiple(t *testing.T) {
	sub1 := &subscription{channel: "ch1"}
	sub2 := &subscription{channel: "ch2"}
	sub3 := &subscription{channel: "ch1"} // Same channel as sub1

	meta := &pubSubMetadata{
		subscriptions: map[string][]*subscription{
			"ch1": {sub1, sub3},
			"ch2": {sub2},
		},
		pendingSubscriptions: make(map[string]*subscription),
		state:                connStateActive,
	}

	subs := meta.getAllSubscriptions()
	if len(subs) != 3 {
		t.Errorf("expected 3 subscriptions, got %d", len(subs))
	}
}

func TestPubSubMetadata_RemoveSubscription_NotFound(t *testing.T) {
	meta := &pubSubMetadata{
		subscriptions:        make(map[string][]*subscription),
		pendingSubscriptions: make(map[string]*subscription),
		state:                connStateActive,
	}

	sub := &subscription{channel: "ch1"}

	// Should not panic when subscription is not found
	meta.removeSubscription(sub)
}

func TestPubSubMetadata_RemoveSubscription_EmptyAfterRemoval(t *testing.T) {
	sub := &subscription{channel: "ch1"}

	meta := &pubSubMetadata{
		subscriptions: map[string][]*subscription{
			"ch1": {sub},
		},
		pendingSubscriptions: make(map[string]*subscription),
		state:                connStateActive,
	}

	meta.removeSubscription(sub)

	// Channel entry should be removed entirely
	if _, ok := meta.subscriptions["ch1"]; ok {
		t.Error("empty channel subscription list should be removed")
	}
}

func TestPool_HashslotInvalidation(t *testing.T) {
	cfg := defaultConfig()
	pool := newPubSubPool(nil, cfg)

	// Add a mock hashslot mapping
	pubsub := &redis.PubSub{}
	pool.mu.Lock()
	pool.hashslotPubSubs[100] = []*redis.PubSub{pubsub}
	pool.mu.Unlock()

	// Invalidate
	pool.invalidateHashslot(100)

	// Verify removed
	pool.mu.RLock()
	defer pool.mu.RUnlock()
	if _, ok := pool.hashslotPubSubs[100]; ok {
		t.Error("hashslot should be invalidated")
	}
}

func TestPool_HashslotInvalidation_NotExists(t *testing.T) {
	cfg := defaultConfig()
	pool := newPubSubPool(nil, cfg)

	// Should not panic when hashslot doesn't exist
	pool.invalidateHashslot(999)
}

func TestPubSubMetadata_AddSubscriptionAndCheckFirst_DifferentChannels(t *testing.T) {
	meta := &pubSubMetadata{
		subscriptions:        make(map[string][]*subscription),
		pendingSubscriptions: make(map[string]*subscription),
		state:                connStateActive,
		cmdCh:                make(chan *command, 10),
		done:                 make(chan struct{}),
	}

	// Subscribe to two different channels
	sub1 := &subscription{
		channel:   "channel-1",
		state:     subStatePending,
		confirmCh: make(chan error, 1),
	}
	sub2 := &subscription{
		channel:   "channel-2",
		state:     subStatePending,
		confirmCh: make(chan error, 1),
	}

	needsSubscribe1, _ := meta.addSubscriptionAndCheckFirst(sub1, "channel-1")
	needsSubscribe2, _ := meta.addSubscriptionAndCheckFirst(sub2, "channel-2")

	// Both should need subscribe (different channels)
	if !needsSubscribe1 {
		t.Error("channel-1 should need subscribe")
	}
	if !needsSubscribe2 {
		t.Error("channel-2 should need subscribe")
	}
}

func TestCreatePubSubToNode_NodeNotInTopology(t *testing.T) {
	cfg := defaultConfig()
	pool := newPubSubPool(nil, cfg)

	// Create a topology monitor with a state that doesn't include the target node
	tm := &topologyMonitor{
		config:       cfg,
		currentState: buildTopologyState(map[int]string{100: "other-node:7000"}),
		done:         make(chan struct{}),
	}

	pool.mu.Lock()
	pool.topologyMonitor = tm
	pool.mu.Unlock()

	ctx := context.Background()
	_, err := pool.createPubSubToNode(ctx, "nonexistent-node:7000")

	if err == nil {
		t.Error("expected error when node not found in topology")
	}
	expectedErr := "node nonexistent-node:7000 not found in topology or owns no slots"
	if err.Error() != expectedErr {
		t.Errorf("error = %q, want %q", err.Error(), expectedErr)
	}
}
