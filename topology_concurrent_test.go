package submux

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// Tests for issue #5: concurrent resubscription goroutines for the same hashslot.

// migrationTestFixture sets up a pool, subscription, and topology monitor
// suitable for testing migration behavior without a real Redis cluster.
type migrationTestFixture struct {
	cfg      *config
	pool     *pubSubPool
	oldMeta  *pubSubMetadata
	newMeta  *pubSubMetadata
	oldPS    *redis.PubSub
	newPS    *redis.PubSub
	sub      *subscription
	sm       *SubMux
	tm       *topologyMonitor
	hashslot int
}

func newMigrationTestFixture(t *testing.T) *migrationTestFixture {
	t.Helper()

	cfg := defaultConfig()
	cfg.autoResubscribe = true
	cfg.recorder = &noopMetrics{}
	cfg.migrationTimeout = 2 * time.Second
	cfg.migrationStallCheck = 500 * time.Millisecond

	pool := newPubSubPool(nil, cfg)
	hashslot := 42

	oldPS := &redis.PubSub{}
	oldMeta := &pubSubMetadata{
		pubsub:               oldPS,
		nodeAddr:             "nodeA:7000",
		logger:               cfg.logger,
		recorder:             &noopMetrics{},
		subscriptions:        make(map[string][]*subscription),
		pendingSubscriptions: make(map[string]*subscription),
		state:                connStateActive,
		cmdCh:                make(chan *command, 100),
		done:                 make(chan struct{}),
		loopDone:             make(chan struct{}),
	}

	newPS := &redis.PubSub{}
	newMeta := &pubSubMetadata{
		pubsub:               newPS,
		nodeAddr:             "nodeB:7000",
		logger:               cfg.logger,
		recorder:             &noopMetrics{},
		subscriptions:        make(map[string][]*subscription),
		pendingSubscriptions: make(map[string]*subscription),
		state:                connStateActive,
		cmdCh:                make(chan *command), // unbuffered
		done:                 make(chan struct{}),
		loopDone:             make(chan struct{}),
	}
	close(newMeta.done) // sendCommand returns "pubsub closed" immediately

	pool.mu.Lock()
	pool.pubSubMetadata[oldPS] = oldMeta
	pool.pubSubMetadata[newPS] = newMeta
	pool.hashslotPubSubs[hashslot] = []*redis.PubSub{newPS}
	pool.nodePubSubs["nodeB:7000"] = []*redis.PubSub{newPS}
	pool.mu.Unlock()

	topoState := buildTopologyState(map[int]string{hashslot: "nodeB:7000"})

	sub := &subscription{
		channel:   "test-channel",
		subType:   subTypeSubscribe,
		pubsub:    oldPS,
		state:     subStateActive,
		confirmCh: make(chan error, 1),
		doneCh:    make(chan struct{}),
		hashslot:  hashslot,
		callback:  func(ctx context.Context, msg *Message) {},
	}
	oldMeta.addSubscription(sub)

	sm := &SubMux{
		pool: pool,
		subscriptions: map[string][]*subscription{
			"test-channel": {sub},
		},
	}

	tm := &topologyMonitor{
		config:       cfg,
		subMux:       sm,
		currentState: topoState,
		done:         make(chan struct{}),
	}
	pool.setTopologyMonitor(tm)

	return &migrationTestFixture{
		cfg: cfg, pool: pool,
		oldMeta: oldMeta, newMeta: newMeta,
		oldPS: oldPS, newPS: newPS,
		sub: sub, sm: sm, tm: tm,
		hashslot: hashslot,
	}
}

// repopulateHashslot restores the hashslot→PubSub mapping that handleMigration
// invalidates, so the next migration goroutine can find a PubSub.
func (f *migrationTestFixture) repopulateHashslot() {
	f.pool.mu.Lock()
	f.pool.hashslotPubSubs[f.hashslot] = []*redis.PubSub{f.newPS}
	f.pool.mu.Unlock()
}

// TestAddSubscription_NoDuplicatePointer verifies that calling addSubscription
// twice with the same *subscription pointer does not create a duplicate entry.
func TestAddSubscription_NoDuplicatePointer(t *testing.T) {
	meta := &pubSubMetadata{
		subscriptions: make(map[string][]*subscription),
	}
	sub := &subscription{channel: "ch"}

	meta.addSubscription(sub)
	meta.addSubscription(sub)

	if count := meta.subscriptionCount(); count != 1 {
		t.Errorf("subscriptionCount = %d, want 1 (same pointer added twice should dedup)", count)
	}
}

// TestConcurrentMigrationsSameHashslot_NoDuplicateSubscription verifies that
// two concurrent resubscribeOnNewNode calls for the same hashslot do not cause
// the subscription to appear twice in the new metadata.
func TestConcurrentMigrationsSameHashslot_NoDuplicateSubscription(t *testing.T) {
	f := newMigrationTestFixture(t)

	migration := hashslotMigration{hashslot: f.hashslot, oldNode: "nodeA:7000", newNode: "nodeB:7000"}

	// Simulate two concurrent resubscription goroutines for the same hashslot
	// (as would happen when handleMigration is called twice rapidly).
	var wg sync.WaitGroup
	var processedCount1, processedCount2 atomic.Int64
	var innerWg1, innerWg2 sync.WaitGroup

	var barrier sync.WaitGroup
	barrier.Add(1)

	wg.Add(2)
	go func() {
		defer wg.Done()
		barrier.Wait()
		f.tm.resubscribeOnNewNode(context.Background(), []*subscription{f.sub}, migration, &processedCount1, &innerWg1)
		innerWg1.Wait()
	}()
	go func() {
		defer wg.Done()
		barrier.Wait()
		f.tm.resubscribeOnNewNode(context.Background(), []*subscription{f.sub}, migration, &processedCount2, &innerWg2)
		innerWg2.Wait()
	}()

	barrier.Done()
	wg.Wait()

	subs := f.newMeta.getSubscriptions("test-channel")
	if len(subs) != 1 {
		t.Errorf("subscription count in new metadata = %d, want 1 (no duplicates)", len(subs))
	}
}

// TestSecondMigrationCancelsFirst verifies that when a second migration is
// detected for the same hashslot, the first migration goroutine's context
// is cancelled. We test this by checking that the activeMigrations entry
// is replaced (cancel-and-replace semantics).
func TestSecondMigrationCancelsFirst(t *testing.T) {
	f := newMigrationTestFixture(t)

	// Create a context we control, to verify cancellation propagates.
	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()

	handle1 := &migrationHandle{cancel: cancel1}
	f.tm.activeMigrations.Store(f.hashslot, handle1)

	migration := hashslotMigration{hashslot: f.hashslot, oldNode: "nodeA:7000", newNode: "nodeB:7000"}

	// handleMigration should cancel the previous handle and store a new one.
	f.tm.handleMigration(migration)

	// Verify the old context was cancelled.
	select {
	case <-ctx1.Done():
		// Success: previous migration's context was cancelled.
	default:
		t.Error("previous migration context was not cancelled when new migration arrived")
	}

	// Verify a new handle was stored (not the old one).
	val, loaded := f.tm.activeMigrations.Load(f.hashslot)
	if !loaded {
		t.Fatal("activeMigrations entry missing after handleMigration")
	}
	if val == handle1 {
		t.Error("activeMigrations should have a new handle, not the old one")
	}

	f.tm.wg.Wait()
}

// TestActiveMigrationsCleanup verifies that after a migration goroutine
// completes, a subsequent migration for the same hashslot can start and
// complete cleanly (no stale state).
func TestActiveMigrationsCleanup(t *testing.T) {
	f := newMigrationTestFixture(t)

	migration1 := hashslotMigration{hashslot: f.hashslot, oldNode: "nodeA:7000", newNode: "nodeB:7000"}
	f.tm.handleMigration(migration1)
	f.tm.wg.Wait()

	// Re-populate and trigger a second migration for the same hashslot.
	f.repopulateHashslot()
	migration2 := hashslotMigration{hashslot: f.hashslot, oldNode: "nodeB:7000", newNode: "nodeC:7000"}
	f.tm.handleMigration(migration2)

	done := make(chan struct{})
	go func() {
		f.tm.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(5 * time.Second):
		t.Fatal("second migration goroutine did not complete; stale state may be blocking")
	}
}

// TestRapidSequentialMigrationsSameHashslot fires multiple migrations for the
// same hashslot in rapid succession. Each new migration should cancel the
// previous one. The subscription should end up registered exactly once.
func TestRapidSequentialMigrationsSameHashslot(t *testing.T) {
	f := newMigrationTestFixture(t)

	// Fire 5 rapid sequential migrations for the same hashslot.
	for i := range 5 {
		f.repopulateHashslot()
		migration := hashslotMigration{
			hashslot: f.hashslot,
			oldNode:  fmt.Sprintf("node%d:7000", i),
			newNode:  fmt.Sprintf("node%d:7000", i+1),
		}
		f.tm.handleMigration(migration)
	}

	done := make(chan struct{})
	go func() {
		f.tm.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for migration goroutines to complete")
	}

	// The subscription should appear exactly once (no duplicates from overlapping goroutines).
	subs := f.newMeta.getSubscriptions("test-channel")
	if len(subs) != 1 {
		t.Errorf("subscription count in metadata = %d, want 1 (no duplicates after rapid migrations)", len(subs))
	}
}

// TestResubscribeOnNewNode_StalePubSubReference verifies that when a
// subscription's PubSub reference is changed by another goroutine between
// the state-marking loop and the migration loop, the subscription is still
// correctly removed from the *original* metadata (issue #6 TOCTOU race).
func TestResubscribeOnNewNode_StalePubSubReference(t *testing.T) {
	f := newMigrationTestFixture(t)

	// Create an intermediate PubSub+metadata that a concurrent goroutine
	// will swap into the subscription between the two loops.
	intermediatePS := &redis.PubSub{}
	intermediateMeta := &pubSubMetadata{
		pubsub:               intermediatePS,
		nodeAddr:             "nodeC:7000",
		logger:               f.cfg.logger,
		recorder:             &noopMetrics{},
		subscriptions:        make(map[string][]*subscription),
		pendingSubscriptions: make(map[string]*subscription),
		state:                connStateActive,
		cmdCh:                make(chan *command, 100),
		done:                 make(chan struct{}),
		loopDone:             make(chan struct{}),
	}

	f.pool.mu.Lock()
	f.pool.pubSubMetadata[intermediatePS] = intermediateMeta
	f.pool.mu.Unlock()

	// Simulate a concurrent goroutine that changes the sub's PubSub reference
	// between the first getPubSub() read and its use for removeSubscription.
	// With the merged loop + snapshot (issue #6 fix), getPubSub() is read
	// once and used consistently, so this mutation cannot cause the removal
	// to target the wrong metadata.
	//
	// We start a goroutine that flips the PubSub as soon as the state
	// transitions to Closed (set at the top of the migration loop).
	flipDone := make(chan struct{})
	go func() {
		defer close(flipDone)
		for {
			s := f.sub.getState()
			if s == subStateClosed || s == subStatePending || s == subStateFailed {
				f.sub.setPubSub(intermediatePS)
				return
			}
			runtime.Gosched()
		}
	}()

	migration := hashslotMigration{hashslot: f.hashslot, oldNode: "nodeA:7000", newNode: "nodeB:7000"}

	var processedCount atomic.Int64
	var wg sync.WaitGroup
	f.tm.resubscribeOnNewNode(context.Background(), []*subscription{f.sub}, migration, &processedCount, &wg)
	wg.Wait()
	<-flipDone

	// The subscription must have been removed from oldMeta.
	// With the TOCTOU bug, the second loop re-reads getPubSub() which now
	// returns intermediatePS, so removeSubscription targets intermediateMeta
	// instead of oldMeta — leaving the subscription orphaned in oldMeta.
	oldSubs := f.oldMeta.getSubscriptions("test-channel")
	if len(oldSubs) != 0 {
		t.Errorf("subscription still in oldMeta after migration: count=%d, want 0 (TOCTOU race — issue #6)", len(oldSubs))
	}
}

// TestResubscription_CleanupRaceWithConnectionFailure verifies that
// resubscribeOnNewNode correctly removes the subscription from old metadata
// even when the old connection transitions to connStateFailed concurrently.
//
// Before the fix, the code checked oldMeta.getState() == connStateActive
// before calling removeSubscription. If the connection failed between the
// check and the remove, both resubscribeOnNewNode and onEventLoopExit
// would race on the subscription map.
func TestResubscription_CleanupRaceWithConnectionFailure(t *testing.T) {
	f := newMigrationTestFixture(t)

	migration := hashslotMigration{
		hashslot: f.hashslot,
		oldNode:  "nodeA:7000",
		newNode:  "nodeB:7000",
	}

	// Verify subscription starts on oldMeta
	if count := f.oldMeta.subscriptionCount(); count != 1 {
		t.Fatalf("oldMeta subscription count = %d, want 1", count)
	}

	var wg sync.WaitGroup
	var processedCount atomic.Int64
	var innerWg sync.WaitGroup

	// Run resubscribeOnNewNode and concurrently fail the old connection
	var barrier sync.WaitGroup
	barrier.Add(1)

	// Goroutine A: transition oldMeta to connStateFailed and remove subscription
	// (simulating what onEventLoopExit does)
	wg.Add(1)
	go func() {
		defer wg.Done()
		barrier.Wait()
		f.oldMeta.setState(connStateFailed)
		f.oldMeta.removeSubscription(f.sub)
	}()

	// Goroutine B: run resubscribeOnNewNode
	wg.Add(1)
	go func() {
		defer wg.Done()
		barrier.Wait()
		ctx := context.Background()
		f.tm.resubscribeOnNewNode(ctx, []*subscription{f.sub}, migration, &processedCount, &innerWg)
		innerWg.Wait()
	}()

	// Release both goroutines simultaneously
	barrier.Done()
	wg.Wait()

	// Key assertion: subscription must be removed from oldMeta regardless of
	// the race outcome. Before the fix, if oldMeta.getState() returned
	// connStateFailed, the subscription might not be removed by resubscribeOnNewNode.
	oldCount := f.oldMeta.subscriptionCount()
	if oldCount != 0 {
		t.Errorf("oldMeta subscription count = %d, want 0 (subscription should be removed regardless of connection state)", oldCount)
	}
}

// TestResubscribeOnNewNode_NoGoroutineStormAfterCancel verifies that
// resubscribeOnNewNode does not spawn goroutines for subscriptions after
// the migration context is cancelled mid-iteration.
func TestResubscribeOnNewNode_NoGoroutineStormAfterCancel(t *testing.T) {
	f := newMigrationTestFixture(t)

	// Create many subscriptions for the same hashslot with unique channels
	const numSubs = 50
	subs := make([]*subscription, 0, numSubs)

	// Clear default fixture subscription
	f.oldMeta.mu.Lock()
	f.oldMeta.subscriptions = make(map[string][]*subscription)
	f.oldMeta.mu.Unlock()
	f.sm.subscriptions = make(map[string][]*subscription)

	for i := range numSubs {
		s := &subscription{
			channel:   fmt.Sprintf("channel-%d", i),
			subType:   subTypeSubscribe,
			pubsub:    f.oldPS,
			state:     subStateActive,
			confirmCh: make(chan error, 1),
			doneCh:    make(chan struct{}),
			hashslot:  f.hashslot,
			callback:  func(ctx context.Context, msg *Message) {},
		}
		f.oldMeta.addSubscription(s)
		f.sm.subscriptions[s.channel] = []*subscription{s}
		subs = append(subs, s)
	}

	migration := hashslotMigration{
		hashslot: f.hashslot,
		oldNode:  "nodeA:7000",
		newNode:  "nodeB:7000",
	}

	// Cancel context before calling. getPubSubForHashslot will fail because
	// context is done, and the function should skip ALL remaining subs.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var processedCount atomic.Int64
	var wg sync.WaitGroup

	f.tm.resubscribeOnNewNode(ctx, subs, migration, &processedCount, &wg)
	wg.Wait()

	// All subscriptions should be counted as processed (skipped due to cancelled ctx)
	if got := processedCount.Load(); got != numSubs {
		t.Errorf("processedCount = %d, want %d (all should be skipped on cancelled context)", got, numSubs)
	}
}
