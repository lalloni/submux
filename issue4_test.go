package submux

import (
	"testing"
)

// Tests for GitHub issue #4:
// "Subscriptions permanently lost after migrationTimeout expires with no recovery path"
//
// These tests verify the fixes for two problems:
// 1. Failed subscriptions are recovered via collectFailedSubscriptions even when
//    compareAndDetectChanges detects no topology change (node recovered at same address).
// 2. subscribeToChannel cleans up failed/closed entries before appending new ones,
//    preventing duplicate subscriptions and double delivery.

// TestIssue4_RecoveryDetectsFailedSubscriptionsWhenTopologyUnchanged verifies that
// even when compareAndDetectChanges returns no migrations (node recovered at same
// address), the recovery mechanism via collectFailedSubscriptions finds stranded
// subscriptions that need resubscription.
func TestIssue4_RecoveryDetectsFailedSubscriptionsWhenTopologyUnchanged(t *testing.T) {
	// Simulate initial topology: hashslot 100 → node A
	initial := buildTopologyState(map[int]string{
		100: "node-a:7000",
		200: "node-b:7001",
	})

	// Node A recovers at same address — no topology change
	afterRecovery := buildTopologyState(map[int]string{
		100: "node-a:7000",
		200: "node-b:7001",
	})

	migrations := afterRecovery.compareAndDetectChanges(initial)

	// No migrations expected — this is correct behavior for compareAndDetectChanges
	if len(migrations) != 0 {
		t.Fatalf("expected 0 migrations for unchanged topology, got %d", len(migrations))
	}

	// The fix: collectFailedSubscriptions finds the stranded subscription
	cfg := defaultConfig()
	cfg.autoResubscribe = true
	pool := newPubSubPool(nil, cfg)

	failedSub := &subscription{
		channel:  "events",
		hashslot: 100,
		state:    subStateFailed,
	}

	sm := &SubMux{
		pool: pool,
		subscriptions: map[string][]*subscription{
			"events": {failedSub},
		},
	}

	tm := &topologyMonitor{
		config:       cfg,
		subMux:       sm,
		currentState: afterRecovery,
	}

	failed := tm.collectFailedSubscriptions()

	if len(failed) == 0 {
		t.Fatal("collectFailedSubscriptions should find the stranded failed subscription")
	}
	if subs, ok := failed[100]; !ok || len(subs) != 1 || subs[0] != failedSub {
		t.Errorf("expected failed subscription at hashslot 100, got %v", failed)
	}
}

// TestIssue4_CollectFailedSubscriptionsIgnoresActiveAndPending verifies that
// collectFailedSubscriptions only returns subscriptions in subStateFailed,
// not active or pending ones.
func TestIssue4_CollectFailedSubscriptionsIgnoresActiveAndPending(t *testing.T) {
	cfg := defaultConfig()
	pool := newPubSubPool(nil, cfg)

	activeSub := &subscription{channel: "ch1", hashslot: 100, state: subStateActive}
	pendingSub := &subscription{channel: "ch2", hashslot: 200, state: subStatePending}
	failedSub := &subscription{channel: "ch3", hashslot: 300, state: subStateFailed}
	closedSub := &subscription{channel: "ch4", hashslot: 400, state: subStateClosed}

	sm := &SubMux{
		pool: pool,
		subscriptions: map[string][]*subscription{
			"ch1": {activeSub},
			"ch2": {pendingSub},
			"ch3": {failedSub},
			"ch4": {closedSub},
		},
	}

	tm := &topologyMonitor{
		config: cfg,
		subMux: sm,
	}

	failed := tm.collectFailedSubscriptions()

	if len(failed) != 1 {
		t.Fatalf("expected 1 hashslot with failed subscriptions, got %d", len(failed))
	}
	if subs, ok := failed[300]; !ok || len(subs) != 1 || subs[0] != failedSub {
		t.Error("expected only the failed subscription at hashslot 300")
	}
}

// TestIssue4_AddSubscriptionToMapCleansFailedEntries verifies that
// addSubscriptionToMapLocked removes failed/closed entries before appending
// the new subscription, preventing duplicate accumulation.
func TestIssue4_AddSubscriptionToMapCleansFailedEntries(t *testing.T) {
	sm := &SubMux{
		subscriptions: make(map[string][]*subscription),
	}

	// Simulate a channel with a failed subscription (from a prior timeout)
	failedSub := &subscription{
		channel:  "events",
		hashslot: 100,
		state:    subStateFailed,
	}
	sm.subscriptions["events"] = []*subscription{failedSub}

	// Add a new subscription using the fixed method
	newSub := &subscription{
		channel:   "events",
		hashslot:  100,
		state:     subStatePending,
		confirmCh: make(chan error, 1),
		doneCh:    make(chan struct{}),
	}

	sm.mu.Lock()
	sm.addSubscriptionToMapLocked("events", newSub)
	sm.mu.Unlock()

	sm.mu.RLock()
	subs := sm.subscriptions["events"]
	sm.mu.RUnlock()

	if len(subs) != 1 {
		t.Errorf("expected 1 subscription after cleanup+append, got %d", len(subs))
	}
	if subs[0] != newSub {
		t.Error("expected the new subscription to be the only entry")
	}
}

// TestIssue4_AddSubscriptionToMapPreservesActiveEntries verifies that
// addSubscriptionToMapLocked does not remove active or pending entries.
func TestIssue4_AddSubscriptionToMapPreservesActiveEntries(t *testing.T) {
	sm := &SubMux{
		subscriptions: make(map[string][]*subscription),
	}

	activeSub := &subscription{channel: "events", hashslot: 100, state: subStateActive}
	failedSub := &subscription{channel: "events", hashslot: 100, state: subStateFailed}
	closedSub := &subscription{channel: "events", hashslot: 100, state: subStateClosed}
	sm.subscriptions["events"] = []*subscription{activeSub, failedSub, closedSub}

	newSub := &subscription{
		channel:   "events",
		hashslot:  100,
		state:     subStatePending,
		confirmCh: make(chan error, 1),
		doneCh:    make(chan struct{}),
	}

	sm.mu.Lock()
	sm.addSubscriptionToMapLocked("events", newSub)
	sm.mu.Unlock()

	sm.mu.RLock()
	subs := sm.subscriptions["events"]
	sm.mu.RUnlock()

	if len(subs) != 2 {
		t.Fatalf("expected 2 subscriptions (active + new), got %d", len(subs))
	}
	if subs[0] != activeSub {
		t.Error("expected active subscription to be preserved")
	}
	if subs[1] != newSub {
		t.Error("expected new subscription to be appended")
	}
}

// TestIssue4_FindAffectedSubscriptionsFiltersDeadSubs verifies that
// findAffectedSubscriptions only returns active/pending subscriptions,
// filtering out failed and closed ones to prevent duplicate signal delivery.
func TestIssue4_FindAffectedSubscriptionsFiltersDeadSubs(t *testing.T) {
	cfg := defaultConfig()
	pool := newPubSubPool(nil, cfg)

	failedSub := &subscription{channel: "events", hashslot: 100, state: subStateFailed}
	closedSub := &subscription{channel: "events", hashslot: 100, state: subStateClosed}
	activeSub := &subscription{channel: "events", hashslot: 100, state: subStateActive}
	pendingSub := &subscription{channel: "events", hashslot: 100, state: subStatePending}

	sm := &SubMux{
		pool: pool,
		subscriptions: map[string][]*subscription{
			"events": {failedSub, closedSub, activeSub, pendingSub},
		},
	}

	tm := &topologyMonitor{
		config: cfg,
		subMux: sm,
	}

	affected := tm.findAffectedSubscriptions(100)

	if len(affected) != 2 {
		t.Fatalf("expected 2 affected subscriptions (active + pending), got %d", len(affected))
	}

	found := make(map[subscriptionState]bool)
	for _, sub := range affected {
		found[sub.getState()] = true
	}
	if !found[subStateActive] || !found[subStatePending] {
		t.Error("expected only active and pending subscriptions in result")
	}
}

// TestAddSubscriptionToMapLocked_COW verifies that addSubscriptionToMapLocked uses
// copy-on-write to avoid mutating the backing array of old slice references.
// This is critical for correctness when concurrent readers have captured slice
// references before acquiring locks.
func TestAddSubscriptionToMapLocked_COW(t *testing.T) {
	sm := &SubMux{
		subscriptions: make(map[string][]*subscription),
	}

	sub1 := &subscription{channel: "ch", state: subStateActive}
	sub2 := &subscription{channel: "ch", state: subStateActive}
	// Create with capacity > 2 so that existing[:0] would allow array reuse
	sl := make([]*subscription, 2, 5)
	sl[0] = sub1
	sl[1] = sub2
	sm.subscriptions["ch"] = sl

	// Capture reference to old slice (simulating a reader that held the reference
	// before the lock was acquired)
	oldSlice := sm.subscriptions["ch"]
	oldPtr := &oldSlice[0]

	// Add a new subscription
	sub3 := &subscription{channel: "ch", state: subStateActive}
	sm.mu.Lock()
	sm.addSubscriptionToMapLocked("ch", sub3)
	sm.mu.Unlock()

	newSlice := sm.subscriptions["ch"]
	newPtr := &newSlice[0]

	// With buggy implementation using existing[:0], the backing arrays would be
	// identical. The fix must allocate a new array to avoid corruption.
	if oldPtr == newPtr {
		t.Fatal("old and new slices share the same backing array - COW not implemented!")
	}

	// Verify the new slice has the expected contents
	if len(newSlice) != 3 {
		t.Errorf("new slice wrong len: %d, want 3", len(newSlice))
	}
	if newSlice[2] != sub3 {
		t.Error("new subscription not appended correctly")
	}
}
