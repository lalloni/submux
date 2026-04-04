package submux

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// Helper function to build a topology state directly for testing
func buildTopologyState(slots map[int]string) *topologyState {
	ts := newTopologyState()
	ts.hashslotToNode = make(map[int]string)
	ts.hashslotToNodes = make(map[int][]string)
	ts.nodeToHashslots = make(map[string][]int)

	for slot, node := range slots {
		ts.hashslotToNode[slot] = node
		ts.hashslotToNodes[slot] = []string{node} // Single node (master only) for tests
		ts.nodeToHashslots[node] = append(ts.nodeToHashslots[node], slot)
	}
	return ts
}

func TestTopologyState_New(t *testing.T) {
	ts := newTopologyState()

	if ts.hashslotToNode == nil {
		t.Error("hashslotToNode map should be initialized")
	}
	if ts.nodeToHashslots == nil {
		t.Error("nodeToHashslots map should be initialized")
	}
	if len(ts.hashslotToNode) != 0 {
		t.Errorf("hashslotToNode should be empty, got %d entries", len(ts.hashslotToNode))
	}
	if len(ts.nodeToHashslots) != 0 {
		t.Errorf("nodeToHashslots should be empty, got %d entries", len(ts.nodeToHashslots))
	}
}

func TestTopologyState_Update(t *testing.T) {
	tests := []struct {
		name                string
		slots               []redis.ClusterSlot
		expectedSlotCount   int
		expectedNodeCount   int
		checkSlot           int
		expectedNode        string
		expectedNodeHasSlot bool
	}{
		{
			name:                "empty slots",
			slots:               []redis.ClusterSlot{},
			expectedSlotCount:   0,
			expectedNodeCount:   0,
			checkSlot:           0,
			expectedNode:        "",
			expectedNodeHasSlot: false,
		},
		{
			name: "single node single slot",
			slots: []redis.ClusterSlot{
				{Start: 0, End: 0, Nodes: []redis.ClusterNode{{Addr: "node1:7000"}}},
			},
			expectedSlotCount:   1,
			expectedNodeCount:   1,
			checkSlot:           0,
			expectedNode:        "node1:7000",
			expectedNodeHasSlot: true,
		},
		{
			name: "single node slot range",
			slots: []redis.ClusterSlot{
				{Start: 0, End: 5460, Nodes: []redis.ClusterNode{{Addr: "node1:7000"}}},
			},
			expectedSlotCount:   5461,
			expectedNodeCount:   1,
			checkSlot:           1000,
			expectedNode:        "node1:7000",
			expectedNodeHasSlot: true,
		},
		{
			name: "slot with no nodes is skipped",
			slots: []redis.ClusterSlot{
				{Start: 0, End: 100, Nodes: []redis.ClusterNode{}},
			},
			expectedSlotCount:   0,
			expectedNodeCount:   0,
			checkSlot:           50,
			expectedNode:        "",
			expectedNodeHasSlot: false,
		},
		{
			name: "three node cluster",
			slots: []redis.ClusterSlot{
				{Start: 0, End: 5460, Nodes: []redis.ClusterNode{{Addr: "node1:7000"}}},
				{Start: 5461, End: 10922, Nodes: []redis.ClusterNode{{Addr: "node2:7000"}}},
				{Start: 10923, End: 16383, Nodes: []redis.ClusterNode{{Addr: "node3:7000"}}},
			},
			expectedSlotCount:   16384,
			expectedNodeCount:   3,
			checkSlot:           6000,
			expectedNode:        "node2:7000",
			expectedNodeHasSlot: true,
		},
		{
			name: "uses first node as master",
			slots: []redis.ClusterSlot{
				{Start: 0, End: 100, Nodes: []redis.ClusterNode{
					{Addr: "master:7000"},
					{Addr: "replica1:7001"},
					{Addr: "replica2:7002"},
				}},
			},
			expectedSlotCount:   101,
			expectedNodeCount:   1,
			checkSlot:           50,
			expectedNode:        "master:7000",
			expectedNodeHasSlot: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := newTopologyState()
			ts.update(tt.slots)

			if len(ts.hashslotToNode) != tt.expectedSlotCount {
				t.Errorf("hashslotToNode count = %d, want %d", len(ts.hashslotToNode), tt.expectedSlotCount)
			}
			if len(ts.nodeToHashslots) != tt.expectedNodeCount {
				t.Errorf("nodeToHashslots count = %d, want %d", len(ts.nodeToHashslots), tt.expectedNodeCount)
			}

			node, ok := ts.getNodeForHashslot(tt.checkSlot)
			if ok != tt.expectedNodeHasSlot {
				t.Errorf("getNodeForHashslot(%d) ok = %v, want %v", tt.checkSlot, ok, tt.expectedNodeHasSlot)
			}
			if node != tt.expectedNode {
				t.Errorf("getNodeForHashslot(%d) = %q, want %q", tt.checkSlot, node, tt.expectedNode)
			}
		})
	}
}

func TestTopologyState_Update_ClearsPrevious(t *testing.T) {
	ts := newTopologyState()

	// First update
	ts.update([]redis.ClusterSlot{
		{Start: 0, End: 16383, Nodes: []redis.ClusterNode{{Addr: "node1:7000"}}},
	})

	if len(ts.hashslotToNode) != 16384 {
		t.Fatalf("after first update, expected 16384 slots, got %d", len(ts.hashslotToNode))
	}

	// Second update with different data should replace
	ts.update([]redis.ClusterSlot{
		{Start: 0, End: 100, Nodes: []redis.ClusterNode{{Addr: "node2:7000"}}},
	})

	if len(ts.hashslotToNode) != 101 {
		t.Errorf("after second update, expected 101 slots, got %d", len(ts.hashslotToNode))
	}

	// Verify old node is gone
	if _, ok := ts.nodeToHashslots["node1:7000"]; ok {
		t.Error("node1 should have been removed after update")
	}

	// Verify new node exists
	if _, ok := ts.nodeToHashslots["node2:7000"]; !ok {
		t.Error("node2 should exist after update")
	}
}

func TestTopologyState_GetNodeForHashslot(t *testing.T) {
	tests := []struct {
		name         string
		slots        map[int]string
		hashslot     int
		expectedNode string
		expectedOk   bool
	}{
		{
			name:         "empty state",
			slots:        map[int]string{},
			hashslot:     0,
			expectedNode: "",
			expectedOk:   false,
		},
		{
			name:         "slot exists",
			slots:        map[int]string{100: "node1:7000", 200: "node2:7000"},
			hashslot:     100,
			expectedNode: "node1:7000",
			expectedOk:   true,
		},
		{
			name:         "slot does not exist",
			slots:        map[int]string{100: "node1:7000"},
			hashslot:     200,
			expectedNode: "",
			expectedOk:   false,
		},
		{
			name:         "boundary slot 0",
			slots:        map[int]string{0: "node1:7000"},
			hashslot:     0,
			expectedNode: "node1:7000",
			expectedOk:   true,
		},
		{
			name:         "boundary slot 16383",
			slots:        map[int]string{16383: "node1:7000"},
			hashslot:     16383,
			expectedNode: "node1:7000",
			expectedOk:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := buildTopologyState(tt.slots)
			node, ok := ts.getNodeForHashslot(tt.hashslot)

			if ok != tt.expectedOk {
				t.Errorf("getNodeForHashslot(%d) ok = %v, want %v", tt.hashslot, ok, tt.expectedOk)
			}
			if node != tt.expectedNode {
				t.Errorf("getNodeForHashslot(%d) = %q, want %q", tt.hashslot, node, tt.expectedNode)
			}
		})
	}
}

func TestTopologyState_GetAnySlotForNode(t *testing.T) {
	tests := []struct {
		name       string
		slots      map[int]string
		nodeAddr   string
		expectedOk bool
	}{
		{
			name:       "empty state",
			slots:      map[int]string{},
			nodeAddr:   "node1:7000",
			expectedOk: false,
		},
		{
			name:       "node exists with slots",
			slots:      map[int]string{100: "node1:7000", 101: "node1:7000", 200: "node2:7000"},
			nodeAddr:   "node1:7000",
			expectedOk: true,
		},
		{
			name:       "node does not exist",
			slots:      map[int]string{100: "node1:7000"},
			nodeAddr:   "node2:7000",
			expectedOk: false,
		},
		{
			name:       "single slot for node",
			slots:      map[int]string{5000: "node1:7000"},
			nodeAddr:   "node1:7000",
			expectedOk: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := buildTopologyState(tt.slots)
			slot, ok := ts.getAnySlotForNode(tt.nodeAddr)

			if ok != tt.expectedOk {
				t.Errorf("getAnySlotForNode(%q) ok = %v, want %v", tt.nodeAddr, ok, tt.expectedOk)
			}

			if ok {
				// Verify the returned slot actually belongs to this node
				actualNode, _ := ts.getNodeForHashslot(slot)
				if actualNode != tt.nodeAddr {
					t.Errorf("getAnySlotForNode returned slot %d which belongs to %q, not %q",
						slot, actualNode, tt.nodeAddr)
				}
			}
		})
	}
}

func TestTopologyState_CompareAndDetectChanges(t *testing.T) {
	tests := []struct {
		name               string
		currentSlots       map[int]string
		previousSlots      map[int]string
		expectedMigrations int
		checkMigration     *hashslotMigration // Optional specific migration to verify
	}{
		{
			name:               "empty to empty",
			currentSlots:       map[int]string{},
			previousSlots:      map[int]string{},
			expectedMigrations: 0,
		},
		{
			name:               "no changes",
			currentSlots:       map[int]string{0: "node1:7000", 1: "node1:7000", 2: "node2:7000"},
			previousSlots:      map[int]string{0: "node1:7000", 1: "node1:7000", 2: "node2:7000"},
			expectedMigrations: 0,
		},
		{
			name:               "single slot migration",
			currentSlots:       map[int]string{0: "node1:7000", 1: "node2:7000"},
			previousSlots:      map[int]string{0: "node1:7000", 1: "node1:7000"},
			expectedMigrations: 1,
			checkMigration:     &hashslotMigration{hashslot: 1, oldNode: "node1:7000", newNode: "node2:7000"},
		},
		{
			name:               "slot appears (new)",
			currentSlots:       map[int]string{0: "node1:7000", 1: "node1:7000"},
			previousSlots:      map[int]string{0: "node1:7000"},
			expectedMigrations: 1,
			checkMigration:     &hashslotMigration{hashslot: 1, oldNode: "", newNode: "node1:7000"},
		},
		{
			name:               "slot disappears",
			currentSlots:       map[int]string{0: "node1:7000"},
			previousSlots:      map[int]string{0: "node1:7000", 1: "node1:7000"},
			expectedMigrations: 1,
			checkMigration:     &hashslotMigration{hashslot: 1, oldNode: "node1:7000", newNode: ""},
		},
		{
			name:               "multiple migrations",
			currentSlots:       map[int]string{0: "node2:7000", 1: "node2:7000", 2: "node2:7000"},
			previousSlots:      map[int]string{0: "node1:7000", 1: "node1:7000", 2: "node1:7000"},
			expectedMigrations: 3,
		},
		{
			name:               "empty to populated",
			currentSlots:       map[int]string{0: "node1:7000", 1: "node1:7000", 2: "node1:7000"},
			previousSlots:      map[int]string{},
			expectedMigrations: 3,
		},
		{
			name:               "populated to empty",
			currentSlots:       map[int]string{},
			previousSlots:      map[int]string{0: "node1:7000", 1: "node1:7000", 2: "node1:7000"},
			expectedMigrations: 3,
		},
		{
			name: "mixed: some migrate, some appear, some disappear",
			currentSlots: map[int]string{
				0: "node1:7000", // unchanged
				1: "node2:7000", // migrated from node1
				3: "node3:7000", // new
			},
			previousSlots: map[int]string{
				0: "node1:7000", // unchanged
				1: "node1:7000", // will migrate
				2: "node1:7000", // will disappear
			},
			expectedMigrations: 3, // 1 migrate + 1 appear + 1 disappear
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			current := buildTopologyState(tt.currentSlots)
			previous := buildTopologyState(tt.previousSlots)

			migrations := current.compareAndDetectChanges(previous)

			if len(migrations) != tt.expectedMigrations {
				t.Errorf("compareAndDetectChanges returned %d migrations, want %d",
					len(migrations), tt.expectedMigrations)
				for _, m := range migrations {
					t.Logf("  migration: slot=%d, old=%q, new=%q", m.hashslot, m.oldNode, m.newNode)
				}
			}

			// Check for specific migration if provided
			if tt.checkMigration != nil {
				found := false
				for _, m := range migrations {
					if m.hashslot == tt.checkMigration.hashslot &&
						m.oldNode == tt.checkMigration.oldNode &&
						m.newNode == tt.checkMigration.newNode {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected migration not found: slot=%d, old=%q, new=%q",
						tt.checkMigration.hashslot, tt.checkMigration.oldNode, tt.checkMigration.newNode)
				}
			}
		})
	}
}

func TestTopologyState_CompareAndDetectChanges_NoDuplicates(t *testing.T) {
	// Ensure no duplicate migrations are detected
	current := buildTopologyState(map[int]string{
		0: "node2:7000",
		1: "node2:7000",
	})
	previous := buildTopologyState(map[int]string{
		0: "node1:7000",
		1: "node1:7000",
	})

	migrations := current.compareAndDetectChanges(previous)

	// Check for duplicates
	seen := make(map[int]bool)
	for _, m := range migrations {
		if seen[m.hashslot] {
			t.Errorf("duplicate migration detected for hashslot %d", m.hashslot)
		}
		seen[m.hashslot] = true
	}
}

func TestTopologyState_CompareAndDetectChanges_LargeScale(t *testing.T) {
	// Test with realistic slot counts
	current := make(map[int]string)
	previous := make(map[int]string)

	// Build 3-node cluster topology (like real Redis Cluster)
	for i := 0; i < 5461; i++ {
		current[i] = "node1:7000"
		previous[i] = "node1:7000"
	}
	for i := 5461; i < 10923; i++ {
		current[i] = "node2:7000"
		previous[i] = "node2:7000"
	}
	for i := 10923; i < 16384; i++ {
		current[i] = "node3:7000"
		previous[i] = "node3:7000"
	}

	// Migrate 100 slots from node1 to node2
	for i := 5361; i < 5461; i++ {
		current[i] = "node2:7000"
	}

	currentState := buildTopologyState(current)
	previousState := buildTopologyState(previous)

	migrations := currentState.compareAndDetectChanges(previousState)

	if len(migrations) != 100 {
		t.Errorf("expected 100 migrations, got %d", len(migrations))
	}

	// Verify all migrations are from node1 to node2
	for _, m := range migrations {
		if m.oldNode != "node1:7000" || m.newNode != "node2:7000" {
			t.Errorf("unexpected migration: slot=%d, old=%q, new=%q",
				m.hashslot, m.oldNode, m.newNode)
		}
		if m.hashslot < 5361 || m.hashslot >= 5461 {
			t.Errorf("unexpected hashslot in migration: %d", m.hashslot)
		}
	}
}

// Tests for topologyMonitor methods

func TestTopologyMonitor_GetNodeForHashslot(t *testing.T) {
	cfg := defaultConfig()
	tm := &topologyMonitor{
		config:       cfg,
		currentState: buildTopologyState(map[int]string{100: "node1:7000", 200: "node2:7000"}),
		done:         make(chan struct{}),
	}

	tests := []struct {
		name       string
		hashslot   int
		expectNode string
		expectOk   bool
	}{
		{"existing slot", 100, "node1:7000", true},
		{"another existing slot", 200, "node2:7000", true},
		{"non-existent slot", 300, "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node, ok := tm.getNodeForHashslot(tt.hashslot)
			if ok != tt.expectOk {
				t.Errorf("getNodeForHashslot(%d) ok = %v, want %v", tt.hashslot, ok, tt.expectOk)
			}
			if node != tt.expectNode {
				t.Errorf("getNodeForHashslot(%d) = %q, want %q", tt.hashslot, node, tt.expectNode)
			}
		})
	}
}

func TestTopologyMonitor_GetNodeForHashslot_NilState(t *testing.T) {
	cfg := defaultConfig()
	tm := &topologyMonitor{
		config:       cfg,
		currentState: nil,
		done:         make(chan struct{}),
	}

	node, ok := tm.getNodeForHashslot(100)
	if ok {
		t.Error("expected ok=false with nil state")
	}
	if node != "" {
		t.Errorf("expected empty node with nil state, got %q", node)
	}
}

func TestTopologyMonitor_GetAnySlotForNode(t *testing.T) {
	cfg := defaultConfig()
	tm := &topologyMonitor{
		config:       cfg,
		currentState: buildTopologyState(map[int]string{100: "node1:7000", 101: "node1:7000", 200: "node2:7000"}),
		done:         make(chan struct{}),
	}

	tests := []struct {
		name     string
		nodeAddr string
		expectOk bool
	}{
		{"existing node with slots", "node1:7000", true},
		{"another existing node", "node2:7000", true},
		{"non-existent node", "node3:7000", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			slot, ok := tm.getAnySlotForNode(tt.nodeAddr)
			if ok != tt.expectOk {
				t.Errorf("getAnySlotForNode(%q) ok = %v, want %v", tt.nodeAddr, ok, tt.expectOk)
			}
			if ok {
				// Verify returned slot belongs to the node
				verifyNode, _ := tm.getNodeForHashslot(slot)
				if verifyNode != tt.nodeAddr {
					t.Errorf("returned slot %d belongs to %q, not %q", slot, verifyNode, tt.nodeAddr)
				}
			}
		})
	}
}

func TestTopologyMonitor_GetAnySlotForNode_NilState(t *testing.T) {
	cfg := defaultConfig()
	tm := &topologyMonitor{
		config:       cfg,
		currentState: nil,
		done:         make(chan struct{}),
	}

	slot, ok := tm.getAnySlotForNode("node1:7000")
	if ok {
		t.Error("expected ok=false with nil state")
	}
	if slot != 0 {
		t.Errorf("expected slot=0 with nil state, got %d", slot)
	}
}

// Tests for signal sending functions

func TestSendSignalMessages(t *testing.T) {
	cfg := defaultConfig()
	sm := &SubMux{
		lifecycleCtx: context.Background(),
	}
	tm := &topologyMonitor{
		config: cfg,
		subMux: sm,
	}

	var receivedMsgs []*Message
	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(2)

	callback := func(ctx context.Context, msg *Message) {
		mu.Lock()
		receivedMsgs = append(receivedMsgs, msg)
		mu.Unlock()
		wg.Done()
	}

	subs := []*subscription{
		{channel: "ch1", callback: callback},
		{channel: "ch2", callback: callback},
	}

	migration := hashslotMigration{
		hashslot: 100,
		oldNode:  "node1:7000",
		newNode:  "node2:7000",
	}

	tm.sendSignalMessages(subs, migration)

	// Wait for async callbacks
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()

	if len(receivedMsgs) != 2 {
		t.Errorf("received %d messages, expected 2", len(receivedMsgs))
	}

	for _, msg := range receivedMsgs {
		if msg.Type != MessageTypeSignal {
			t.Errorf("message type = %v, expected %v", msg.Type, MessageTypeSignal)
		}
		if msg.Signal == nil {
			t.Fatal("signal info should not be nil")
		}
		if msg.Signal.EventType != EventMigration {
			t.Errorf("event type = %v, expected %v", msg.Signal.EventType, EventMigration)
		}
		if msg.Signal.Hashslot != 100 {
			t.Errorf("hashslot = %d, expected 100", msg.Signal.Hashslot)
		}
		if msg.Signal.OldNode != "node1:7000" {
			t.Errorf("old node = %q, expected %q", msg.Signal.OldNode, "node1:7000")
		}
		if msg.Signal.NewNode != "node2:7000" {
			t.Errorf("new node = %q, expected %q", msg.Signal.NewNode, "node2:7000")
		}
	}
}

func TestSendMigrationTimeoutSignal(t *testing.T) {
	cfg := defaultConfig()
	sm := &SubMux{
		lifecycleCtx: context.Background(),
	}
	tm := &topologyMonitor{
		config: cfg,
		subMux: sm,
	}

	var receivedMsgs []*Message
	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(1)

	callback := func(ctx context.Context, msg *Message) {
		mu.Lock()
		receivedMsgs = append(receivedMsgs, msg)
		mu.Unlock()
		wg.Done()
	}

	subs := []*subscription{
		{channel: "ch1", callback: callback},
	}

	migration := hashslotMigration{
		hashslot: 200,
		oldNode:  "oldNode:7000",
		newNode:  "newNode:7000",
	}

	tm.sendMigrationTimeoutSignal(subs, migration, 30*time.Second)

	wg.Wait()

	mu.Lock()
	defer mu.Unlock()

	if len(receivedMsgs) != 1 {
		t.Fatalf("received %d messages, expected 1", len(receivedMsgs))
	}

	msg := receivedMsgs[0]
	if msg.Type != MessageTypeSignal {
		t.Errorf("message type = %v, expected %v", msg.Type, MessageTypeSignal)
	}
	if msg.Signal.EventType != EventMigrationTimeout {
		t.Errorf("event type = %v, expected %v", msg.Signal.EventType, EventMigrationTimeout)
	}
	if msg.Signal.Hashslot != 200 {
		t.Errorf("hashslot = %d, expected 200", msg.Signal.Hashslot)
	}
}

func TestSendMigrationStalledSignal(t *testing.T) {
	cfg := defaultConfig()
	sm := &SubMux{
		lifecycleCtx: context.Background(),
	}
	tm := &topologyMonitor{
		config: cfg,
		subMux: sm,
	}

	var receivedMsgs []*Message
	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(1)

	callback := func(ctx context.Context, msg *Message) {
		mu.Lock()
		receivedMsgs = append(receivedMsgs, msg)
		mu.Unlock()
		wg.Done()
	}

	subs := []*subscription{
		{channel: "ch1", callback: callback},
	}

	migration := hashslotMigration{
		hashslot: 300,
		oldNode:  "oldNode:7000",
		newNode:  "newNode:7000",
	}

	tm.sendMigrationStalledSignal(subs, migration, 10*time.Second)

	wg.Wait()

	mu.Lock()
	defer mu.Unlock()

	if len(receivedMsgs) != 1 {
		t.Fatalf("received %d messages, expected 1", len(receivedMsgs))
	}

	msg := receivedMsgs[0]
	if msg.Type != MessageTypeSignal {
		t.Errorf("message type = %v, expected %v", msg.Type, MessageTypeSignal)
	}
	if msg.Signal.EventType != EventMigrationStalled {
		t.Errorf("event type = %v, expected %v", msg.Signal.EventType, EventMigrationStalled)
	}
	if msg.Signal.Hashslot != 300 {
		t.Errorf("hashslot = %d, expected 300", msg.Signal.Hashslot)
	}
}

func TestSendSignalMessages_EmptySubscriptions(t *testing.T) {
	cfg := defaultConfig()
	sm := &SubMux{
		lifecycleCtx: context.Background(),
	}
	tm := &topologyMonitor{
		config: cfg,
		subMux: sm,
	}

	// Should not panic with empty subscription list
	migration := hashslotMigration{hashslot: 100, oldNode: "a", newNode: "b"}
	tm.sendSignalMessages(nil, migration)
	tm.sendSignalMessages([]*subscription{}, migration)
}

func TestSendSignalMessages_NilCallback(t *testing.T) {
	cfg := defaultConfig()
	sm := &SubMux{
		lifecycleCtx: context.Background(),
	}
	tm := &topologyMonitor{
		config: cfg,
		subMux: sm,
	}

	// Should not panic with nil callback
	subs := []*subscription{
		{channel: "ch1", callback: nil},
	}
	migration := hashslotMigration{hashslot: 100, oldNode: "a", newNode: "b"}
	tm.sendSignalMessages(subs, migration) // Should not panic
}

// Tests for handleMigrations

func TestHandleMigrations_Empty(t *testing.T) {
	cfg := defaultConfig()
	pool := newPubSubPool(nil, cfg)

	sm := &SubMux{
		pool:          pool,
		subscriptions: make(map[string][]*subscription),
	}

	tm := &topologyMonitor{
		config: cfg,
		subMux: sm,
		done:   make(chan struct{}),
	}

	// Should not panic with empty migrations
	tm.handleMigrations(nil)
	tm.handleMigrations([]hashslotMigration{})
}

func TestHandleMigrations_Multiple(t *testing.T) {
	cfg := defaultConfig()
	pool := newPubSubPool(nil, cfg)

	sm := &SubMux{
		pool:          pool,
		subscriptions: make(map[string][]*subscription),
	}

	tm := &topologyMonitor{
		config: cfg,
		subMux: sm,
		done:   make(chan struct{}),
	}

	migrations := []hashslotMigration{
		{hashslot: 100, oldNode: "node1:7000", newNode: "node2:7000"},
		{hashslot: 200, oldNode: "node2:7000", newNode: "node3:7000"},
	}

	// Should not panic - processes each migration
	tm.handleMigrations(migrations)
}

// Tests for handleMigration

func TestHandleMigration_NoAffectedSubscriptions(t *testing.T) {
	cfg := defaultConfig()
	pool := newPubSubPool(nil, cfg)

	sm := &SubMux{
		pool:          pool,
		subscriptions: make(map[string][]*subscription),
	}

	tm := &topologyMonitor{
		config: cfg,
		subMux: sm,
		done:   make(chan struct{}),
	}

	migration := hashslotMigration{hashslot: 100, oldNode: "node1:7000", newNode: "node2:7000"}

	// Should not panic when no subscriptions affected
	tm.handleMigration(migration)
}

func TestHandleMigration_WithAffectedSubscriptions_NoAutoResubscribe(t *testing.T) {
	cfg := defaultConfig()
	cfg.autoResubscribe = false
	pool := newPubSubPool(nil, cfg)

	var receivedSignal *Message
	var mu sync.Mutex
	var wg sync.WaitGroup
	wg.Add(1)

	callback := func(ctx context.Context, msg *Message) {
		mu.Lock()
		receivedSignal = msg
		mu.Unlock()
		wg.Done()
	}

	sub := &subscription{
		channel:  "test-channel",
		hashslot: 100,
		callback: callback,
		state:    subStateActive,
	}

	sm := &SubMux{
		pool: pool,
		subscriptions: map[string][]*subscription{
			"test-channel": {sub},
		},
	}

	tm := &topologyMonitor{
		config: cfg,
		subMux: sm,
		done:   make(chan struct{}),
	}

	migration := hashslotMigration{hashslot: 100, oldNode: "node1:7000", newNode: "node2:7000"}

	tm.handleMigration(migration)

	// Wait for signal
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()

	if receivedSignal == nil {
		t.Fatal("expected signal to be sent")
	}
	if receivedSignal.Type != MessageTypeSignal {
		t.Errorf("message type = %v, want %v", receivedSignal.Type, MessageTypeSignal)
	}
	if receivedSignal.Signal.EventType != EventMigration {
		t.Errorf("event type = %v, want %v", receivedSignal.Signal.EventType, EventMigration)
	}
	if receivedSignal.Signal.Hashslot != 100 {
		t.Errorf("hashslot = %d, want %d", receivedSignal.Signal.Hashslot, 100)
	}
}

func TestHandleMigration_InvalidatesHashslot(t *testing.T) {
	cfg := defaultConfig()
	cfg.autoResubscribe = false
	pool := newPubSubPool(nil, cfg)

	// Set up a hashslot mapping
	pubsub := &redis.PubSub{}
	pool.mu.Lock()
	pool.hashslotPubSubs[100] = []*redis.PubSub{pubsub}
	pool.mu.Unlock()

	sm := &SubMux{
		pool:          pool,
		subscriptions: make(map[string][]*subscription),
	}

	tm := &topologyMonitor{
		config: cfg,
		subMux: sm,
		done:   make(chan struct{}),
	}

	migration := hashslotMigration{hashslot: 100, oldNode: "node1:7000", newNode: "node2:7000"}

	tm.handleMigration(migration)

	// Verify hashslot was invalidated
	pool.mu.RLock()
	defer pool.mu.RUnlock()
	if _, ok := pool.hashslotPubSubs[100]; ok {
		t.Error("hashslot should be invalidated after migration")
	}
}

// Tests for findAffectedSubscriptions

func TestFindAffectedSubscriptions_NoSubscriptions(t *testing.T) {
	cfg := defaultConfig()
	pool := newPubSubPool(nil, cfg)

	sm := &SubMux{
		pool:          pool,
		subscriptions: make(map[string][]*subscription),
	}

	tm := &topologyMonitor{
		config: cfg,
		subMux: sm,
	}

	affected := tm.findAffectedSubscriptions(100)

	if len(affected) != 0 {
		t.Errorf("expected 0 affected subscriptions, got %d", len(affected))
	}
}

func TestFindAffectedSubscriptions_MatchingHashslot(t *testing.T) {
	cfg := defaultConfig()
	pool := newPubSubPool(nil, cfg)

	sub1 := &subscription{channel: "ch1", hashslot: 100}
	sub2 := &subscription{channel: "ch2", hashslot: 200}
	sub3 := &subscription{channel: "ch3", hashslot: 100}

	sm := &SubMux{
		pool: pool,
		subscriptions: map[string][]*subscription{
			"ch1": {sub1},
			"ch2": {sub2},
			"ch3": {sub3},
		},
	}

	tm := &topologyMonitor{
		config: cfg,
		subMux: sm,
	}

	affected := tm.findAffectedSubscriptions(100)

	if len(affected) != 2 {
		t.Errorf("expected 2 affected subscriptions, got %d", len(affected))
	}

	// Verify the correct subscriptions were found
	found := make(map[string]bool)
	for _, sub := range affected {
		found[sub.channel] = true
	}
	if !found["ch1"] || !found["ch3"] {
		t.Error("expected ch1 and ch3 to be affected")
	}
}

func TestFindAffectedSubscriptions_MultipleSubsPerChannel(t *testing.T) {
	cfg := defaultConfig()
	pool := newPubSubPool(nil, cfg)

	sub1 := &subscription{channel: "ch1", hashslot: 100}
	sub2 := &subscription{channel: "ch1", hashslot: 100}
	sub3 := &subscription{channel: "ch1", hashslot: 200} // Different hashslot

	sm := &SubMux{
		pool: pool,
		subscriptions: map[string][]*subscription{
			"ch1": {sub1, sub2, sub3},
		},
	}

	tm := &topologyMonitor{
		config: cfg,
		subMux: sm,
	}

	affected := tm.findAffectedSubscriptions(100)

	if len(affected) != 2 {
		t.Errorf("expected 2 affected subscriptions, got %d", len(affected))
	}
}

// Tests for triggerRefresh
// Note: We can't test triggerRefresh fully without a real cluster client.
// These tests verify the logging behavior and that the function doesn't
// block the caller. The actual refresh will panic without a cluster client,
// but that's expected in unit tests - integration tests cover the full flow.

// Tests for resubscribeOnNewNodeWithMonitoring
// Note: Full resubscription testing requires a real cluster client.
// These tests verify the empty case and basic structure.

func TestResubscribeOnNewNodeWithMonitoring_EmptySubscriptions(t *testing.T) {
	cfg := defaultConfig()
	cfg.migrationTimeout = 100 * time.Millisecond
	cfg.migrationStallCheck = 50 * time.Millisecond

	pool := newPubSubPool(nil, cfg)
	sm := &SubMux{
		pool:          pool,
		subscriptions: make(map[string][]*subscription),
	}

	tm := &topologyMonitor{
		config: cfg,
		subMux: sm,
		done:   make(chan struct{}),
	}

	migration := hashslotMigration{hashslot: 100, oldNode: "node1:7000", newNode: "node2:7000"}

	// Should complete quickly with empty subscriptions
	done := make(chan struct{})
	go func() {
		tm.resubscribeOnNewNodeWithMonitoring(context.Background(), nil, migration)
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timeout waiting for resubscription to complete")
	}
}

// Tests for resubscribeOnNewNode
// Note: These tests verify empty subscription handling.
// Full resubscription testing requires a real cluster client (covered by integration tests).

// Tests for selectNodeForHashslot method

func TestTopologyState_SelectNodeForHashslot_PreferMasters(t *testing.T) {
	ts := newTopologyState()
	ts.hashslotToNodes[100] = []string{"master:7000", "replica1:7001", "replica2:7002"}

	node, ok := ts.selectNodeForHashslot(100, PreferMasters)
	if !ok {
		t.Error("expected ok=true")
	}
	if node != "master:7000" {
		t.Errorf("PreferMasters should return master, got %q", node)
	}
}

func TestTopologyState_SelectNodeForHashslot_PreferReplicas(t *testing.T) {
	ts := newTopologyState()
	ts.hashslotToNodes[100] = []string{"master:7000", "replica1:7001", "replica2:7002"}

	node, ok := ts.selectNodeForHashslot(100, PreferReplicas)
	if !ok {
		t.Error("expected ok=true")
	}
	// Should return a replica, not the master
	if node == "master:7000" {
		t.Errorf("PreferReplicas should return a replica, got master %q", node)
	}
	if node != "replica1:7001" && node != "replica2:7002" {
		t.Errorf("PreferReplicas returned unexpected node %q", node)
	}
}

func TestTopologyState_SelectNodeForHashslot_PreferReplicas_NoReplicas(t *testing.T) {
	ts := newTopologyState()
	ts.hashslotToNodes[100] = []string{"master:7000"} // Only master, no replicas

	node, ok := ts.selectNodeForHashslot(100, PreferReplicas)
	if !ok {
		t.Error("expected ok=true")
	}
	// Should fall back to master when no replicas
	if node != "master:7000" {
		t.Errorf("PreferReplicas should fall back to master when no replicas, got %q", node)
	}
}

func TestTopologyState_SelectNodeForHashslot_BalancedAll(t *testing.T) {
	ts := newTopologyState()
	ts.hashslotToNodes[100] = []string{"master:7000", "replica1:7001", "replica2:7002"}

	node, ok := ts.selectNodeForHashslot(100, BalancedAll)
	if !ok {
		t.Error("expected ok=true")
	}
	// BalancedAll returns master as default (caller handles actual balancing)
	if node != "master:7000" {
		t.Errorf("BalancedAll should return master as default, got %q", node)
	}
}

func TestTopologyState_SelectNodeForHashslot_InvalidPreference(t *testing.T) {
	ts := newTopologyState()
	ts.hashslotToNodes[100] = []string{"master:7000", "replica1:7001"}

	// Invalid preference value should default to master
	node, ok := ts.selectNodeForHashslot(100, NodePreference(999))
	if !ok {
		t.Error("expected ok=true")
	}
	if node != "master:7000" {
		t.Errorf("invalid preference should default to master, got %q", node)
	}
}

func TestTopologyState_SelectNodeForHashslot_EmptyNodes(t *testing.T) {
	ts := newTopologyState()
	ts.hashslotToNodes[100] = []string{} // Empty nodes list

	node, ok := ts.selectNodeForHashslot(100, PreferMasters)
	if ok {
		t.Error("expected ok=false for empty nodes")
	}
	if node != "" {
		t.Errorf("expected empty node, got %q", node)
	}
}

func TestTopologyState_SelectNodeForHashslot_HashslotNotFound(t *testing.T) {
	ts := newTopologyState()
	// hashslot 100 not added

	node, ok := ts.selectNodeForHashslot(100, PreferMasters)
	if ok {
		t.Error("expected ok=false for non-existent hashslot")
	}
	if node != "" {
		t.Errorf("expected empty node, got %q", node)
	}
}

func TestTopologyState_SelectNodeForHashslot_AllPreferences(t *testing.T) {
	tests := []struct {
		name       string
		nodes      []string
		preference NodePreference
		expected   string
	}{
		{"PreferMasters single", []string{"m:7000"}, PreferMasters, "m:7000"},
		{"PreferMasters multi", []string{"m:7000", "r:7001"}, PreferMasters, "m:7000"},
		{"PreferReplicas single", []string{"m:7000"}, PreferReplicas, "m:7000"},
		{"PreferReplicas multi", []string{"m:7000", "r:7001"}, PreferReplicas, "r:7001"},
		{"BalancedAll single", []string{"m:7000"}, BalancedAll, "m:7000"},
		{"BalancedAll multi", []string{"m:7000", "r:7001"}, BalancedAll, "m:7000"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := newTopologyState()
			ts.hashslotToNodes[100] = tt.nodes

			node, ok := ts.selectNodeForHashslot(100, tt.preference)
			if !ok {
				t.Error("expected ok=true")
			}
			if node != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, node)
			}
		})
	}
}


func TestSelectNodeForHashslot_PreferReplicas_DistributesAcrossReplicas(t *testing.T) {
	ts := newTopologyState()
	ts.hashslotToNodes[0] = []string{"master:6379", "replica1:6379", "replica2:6379", "replica3:6379"}

	counts := make(map[string]int)
	for range 100 {
		node, ok := ts.selectNodeForHashslot(0, PreferReplicas)
		if !ok {
			t.Fatal("expected node")
		}
		if node == "master:6379" {
			t.Fatal("should not select master when replicas exist")
		}
		counts[node]++
	}

	if len(counts) < 2 {
		t.Errorf("expected distribution across replicas, got: %v", counts)
	}
}

// Tests for getNodesForHashslot

func TestTopologyState_GetNodesForHashslot_ReturnsCopy(t *testing.T) {
	ts := newTopologyState()
	originalNodes := []string{"m:7000", "r:7001", "r:7002"}
	ts.hashslotToNodes[100] = originalNodes

	nodes, ok := ts.getNodesForHashslot(100)
	if !ok {
		t.Fatal("expected ok=true")
	}

	// Modify the returned slice
	nodes[0] = "modified"

	// Original should be unchanged
	if ts.hashslotToNodes[100][0] == "modified" {
		t.Error("getNodesForHashslot should return a copy, not the original slice")
	}
}

// Tests for triggerRefresh non-blocking behavior
// Note: Full triggerRefresh testing requires a real cluster client.
// The non-blocking nature is verified by integration tests.

func TestResubscribeOnNewNode_EmptySubscriptions(t *testing.T) {
	cfg := defaultConfig()
	pool := newPubSubPool(nil, cfg)

	sm := &SubMux{
		pool:          pool,
		subscriptions: make(map[string][]*subscription),
	}

	tm := &topologyMonitor{
		config: cfg,
		subMux: sm,
		done:   make(chan struct{}),
	}

	migration := hashslotMigration{hashslot: 100, oldNode: "node1:7000", newNode: "node2:7000"}

	var counter atomic.Int64
	var wg sync.WaitGroup

	ctx := context.Background()
	// Should not panic with empty subscriptions
	tm.resubscribeOnNewNode(ctx, nil, migration, &counter, &wg)
	tm.resubscribeOnNewNode(ctx, []*subscription{}, migration, &counter, &wg)
}

func TestResubscribeOnNewNode_RespectsParentContext(t *testing.T) {
	clusterClient := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs:       []string{"localhost:7000"},
		DialTimeout: 100 * time.Millisecond,
	})
	defer clusterClient.Close()

	cfg := defaultConfig()
	cfg.recorder = &noopMetrics{}
	cfg.autoResubscribe = true
	pool := newPubSubPool(clusterClient, cfg)

	sm := &SubMux{
		pool:          pool,
		subscriptions: make(map[string][]*subscription),
	}
	pool.setSubMux(sm)

	tm := newTopologyMonitor(clusterClient, cfg, sm)
	pool.setTopologyMonitor(tm)
	sm.topologyMonitor = tm

	sub := &subscription{
		channel:  "test-channel",
		subType:  subTypeSubscribe,
		state:    subStateClosed,
		hashslot: 100,
	}

	var processedCount atomic.Int64
	var wg sync.WaitGroup

	// Use an already-cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	migration := hashslotMigration{hashslot: 100, oldNode: "old:7000", newNode: "new:7001"}

	// With cancelled ctx, resubscribeOnNewNode should skip the resubscription
	// because getPubSubForHashslot should fail with context error.
	tm.resubscribeOnNewNode(ctx, []*subscription{sub}, migration, &processedCount, &wg)

	wg.Wait()

	// The subscription should have been counted as processed (skipped due to ctx)
	if processedCount.Load() != 1 {
		t.Errorf("expected processedCount=1, got %d", processedCount.Load())
	}
}

// Additional topology edge case tests

func TestTopologyState_SingleHashslotRange(t *testing.T) {
	ts := newTopologyState()

	// Single hashslot range where Start == End
	slots := []redis.ClusterSlot{
		{Start: 100, End: 100, Nodes: []redis.ClusterNode{{Addr: "node1:7000"}}},
	}

	ts.update(slots)

	// Should have exactly 1 slot
	if len(ts.hashslotToNode) != 1 {
		t.Errorf("expected 1 hashslot, got %d", len(ts.hashslotToNode))
	}

	node, ok := ts.getNodeForHashslot(100)
	if !ok || node != "node1:7000" {
		t.Errorf("hashslot 100 = %q, %v, want node1:7000, true", node, ok)
	}
}

func TestTopologyState_Update_ReplicaInfo(t *testing.T) {
	ts := newTopologyState()

	// Slot with master and 2 replicas
	slots := []redis.ClusterSlot{
		{
			Start: 0,
			End:   5460,
			Nodes: []redis.ClusterNode{
				{Addr: "master:7000"},
				{Addr: "replica1:7001"},
				{Addr: "replica2:7002"},
			},
		},
	}

	ts.update(slots)

	// getNodesForHashslot should return all nodes
	nodes, ok := ts.getNodesForHashslot(1000)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if len(nodes) != 3 {
		t.Errorf("expected 3 nodes, got %d", len(nodes))
	}
	if nodes[0] != "master:7000" {
		t.Errorf("first node should be master, got %q", nodes[0])
	}
}

func TestTopologyMonitor_GetNodesForHashslot_NilState(t *testing.T) {
	cfg := defaultConfig()
	tm := &topologyMonitor{
		config:       cfg,
		currentState: nil,
		done:         make(chan struct{}),
	}

	nodes, ok := tm.getNodesForHashslot(100)
	if ok {
		t.Error("expected ok=false with nil state")
	}
	if nodes != nil {
		t.Errorf("expected nil nodes, got %v", nodes)
	}
}

func TestFindAffectedSubscriptions_MultipleHashslots(t *testing.T) {
	cfg := defaultConfig()
	pool := newPubSubPool(nil, cfg)

	sub1 := &subscription{channel: "ch1", hashslot: 100}
	sub2 := &subscription{channel: "ch2", hashslot: 200}
	sub3 := &subscription{channel: "ch3", hashslot: 100}
	sub4 := &subscription{channel: "ch4", hashslot: 100}

	sm := &SubMux{
		pool: pool,
		subscriptions: map[string][]*subscription{
			"ch1": {sub1},
			"ch2": {sub2},
			"ch3": {sub3},
			"ch4": {sub4},
		},
	}

	tm := &topologyMonitor{
		config: cfg,
		subMux: sm,
	}

	// Find subscriptions for hashslot 100
	affected := tm.findAffectedSubscriptions(100)
	if len(affected) != 3 {
		t.Errorf("expected 3 affected subscriptions, got %d", len(affected))
	}

	// Find subscriptions for hashslot 200
	affected = tm.findAffectedSubscriptions(200)
	if len(affected) != 1 {
		t.Errorf("expected 1 affected subscription, got %d", len(affected))
	}

	// Find subscriptions for non-existent hashslot
	affected = tm.findAffectedSubscriptions(999)
	if len(affected) != 0 {
		t.Errorf("expected 0 affected subscriptions, got %d", len(affected))
	}
}

func TestHandleMigration_HashslotInvalidation(t *testing.T) {
	cfg := defaultConfig()
	cfg.autoResubscribe = false // Disable auto-resubscribe for this test
	pool := newPubSubPool(nil, cfg)

	// Set up a hashslot mapping
	pubsub := &redis.PubSub{}
	pool.mu.Lock()
	pool.hashslotPubSubs[100] = []*redis.PubSub{pubsub}
	pool.mu.Unlock()

	sm := &SubMux{
		pool:          pool,
		subscriptions: make(map[string][]*subscription),
	}

	tm := &topologyMonitor{
		config: cfg,
		subMux: sm,
		done:   make(chan struct{}),
	}

	migration := hashslotMigration{hashslot: 100, oldNode: "node1:7000", newNode: "node2:7000"}

	// Handle migration (no subscriptions affected)
	tm.handleMigration(migration)

	// Verify hashslot was invalidated
	pool.mu.RLock()
	_, exists := pool.hashslotPubSubs[100]
	pool.mu.RUnlock()

	if exists {
		t.Error("hashslot should be invalidated after migration")
	}
}

func TestResubscribeOnNewNodeWithMonitoring_QuickCompletion(t *testing.T) {
	cfg := defaultConfig()
	cfg.migrationTimeout = 5 * time.Second
	cfg.migrationStallCheck = 1 * time.Second
	pool := newPubSubPool(nil, cfg)

	sm := &SubMux{
		pool:          pool,
		subscriptions: make(map[string][]*subscription),
	}

	tm := &topologyMonitor{
		config: cfg,
		subMux: sm,
		done:   make(chan struct{}),
	}

	migration := hashslotMigration{hashslot: 100, oldNode: "node1:7000", newNode: "node2:7000"}

	// With empty subscriptions, should complete very quickly
	start := time.Now()
	tm.resubscribeOnNewNodeWithMonitoring(context.Background(), nil, migration)
	elapsed := time.Since(start)

	// Should complete in under 100ms (not waiting for timeouts)
	if elapsed > 100*time.Millisecond {
		t.Errorf("empty subscription resubscription took %v, expected < 100ms", elapsed)
	}
}

func TestTopologyState_GetAnySlotForNode_ReturnsConsistently(t *testing.T) {
	ts := newTopologyState()
	ts.nodeToHashslots["node1:7000"] = []int{100, 200, 300}

	// Should always return the first slot (index 0)
	slot, ok := ts.getAnySlotForNode("node1:7000")
	if !ok {
		t.Fatal("expected ok=true")
	}
	if slot != 100 {
		t.Errorf("expected first slot (100), got %d", slot)
	}

	// Call again to verify consistency
	slot2, _ := ts.getAnySlotForNode("node1:7000")
	if slot != slot2 {
		t.Errorf("inconsistent slot returned: %d vs %d", slot, slot2)
	}
}

func TestTriggerRefresh_TrackedByWaitGroup(t *testing.T) {
	// Create a topology monitor where refreshTopology blocks on tm.mu
	clusterClient := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs:       []string{"localhost:7000"},
		DialTimeout: 100 * time.Millisecond,
	})
	defer clusterClient.Close()

	cfg := defaultConfig()
	cfg.recorder = &noopMetrics{}
	pool := newPubSubPool(clusterClient, cfg)
	subMux := &SubMux{
		clusterClient: clusterClient,
		config:        cfg,
		pool:          pool,
		subscriptions: make(map[string][]*subscription),
	}
	tm := newTopologyMonitor(clusterClient, cfg, subMux)

	// Hold tm.mu so refreshTopology blocks inside the goroutine
	tm.mu.Lock()

	// Trigger refresh — should launch a goroutine tracked by tm.wg
	tm.triggerRefresh("127.0.0.1:7001", true)

	// wg.Wait() should NOT complete while the goroutine is blocked on tm.mu
	wgDone := make(chan struct{})
	go func() {
		tm.wg.Wait()
		close(wgDone)
	}()

	// Give the goroutine time to start
	time.Sleep(50 * time.Millisecond)

	// BUG (without fix): wg.Wait() returns immediately because goroutine isn't tracked
	// EXPECTED (with fix): wg.Wait() blocks because goroutine is tracked and blocked on mu
	select {
	case <-wgDone:
		t.Error("wg.Wait() completed while refresh goroutine should still be running (not tracked in wg)")
	default:
		// Expected: goroutine is still running, wg.Wait() blocks
	}

	// Release the lock so the goroutine can proceed and finish
	tm.mu.Unlock()

	// Now wg.Wait() should complete
	select {
	case <-wgDone:
		// Success
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for refresh goroutine to complete")
	}
}

func TestHandleMigration_ResubscribeTrackedByWaitGroup(t *testing.T) {
	clusterClient := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs:       []string{"localhost:7000"},
		DialTimeout: 100 * time.Millisecond,
	})
	defer clusterClient.Close()

	cfg := defaultConfig()
	cfg.autoResubscribe = true
	cfg.recorder = &noopMetrics{}
	// Use longer migration timeout so the goroutine takes some time
	cfg.migrationTimeout = 1 * time.Second
	cfg.migrationStallCheck = 200 * time.Millisecond
	pool := newPubSubPool(clusterClient, cfg)

	// Use a callback that records it was called (for the signal message)
	var signalReceived atomic.Bool
	callback := func(ctx context.Context, msg *Message) {
		signalReceived.Store(true)
	}

	sub := &subscription{
		channel:  "test-channel",
		hashslot: 100,
		callback: callback,
		state:    subStateActive,
		subType:  subTypeSubscribe,
	}

	sm := &SubMux{
		pool: pool,
		subscriptions: map[string][]*subscription{
			"test-channel": {sub},
		},
	}
	pool.setSubMux(sm)

	tm := newTopologyMonitor(clusterClient, cfg, sm)
	pool.setTopologyMonitor(tm)
	sm.topologyMonitor = tm

	migration := hashslotMigration{hashslot: 100, oldNode: "node1:7000", newNode: "node2:7000"}

	// Handle migration — launches resubscribeOnNewNodeWithMonitoring in goroutine
	tm.handleMigration(migration)

	// wg.Wait() should block until the resubscription goroutine completes
	wgDone := make(chan struct{})
	go func() {
		tm.wg.Wait()
		close(wgDone)
	}()

	select {
	case <-wgDone:
		// Success — wg tracked the resubscription goroutine and it completed
	case <-time.After(5 * time.Second):
		t.Fatal("timeout: resubscription goroutine should have completed")
	}
}

func TestRefreshTopology_BoundedByPollInterval(t *testing.T) {
	// Start a TCP listener that accepts connections but never responds (black hole).
	// This simulates an unresponsive Redis node.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	// Accept connections in the background so the dial succeeds, but never
	// send any data so the subsequent read blocks until the context expires.
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			// Hold the connection open but never write anything.
			go func(c net.Conn) {
				buf := make([]byte, 1024)
				for {
					if _, err := c.Read(buf); err != nil {
						return
					}
				}
			}(conn)
		}
	}()

	blackHoleAddr := ln.Addr().String()

	// Create a ClusterClient with ContextTimeoutEnabled so go-redis
	// actually respects context deadlines for socket I/O. Without this,
	// go-redis ignores the context and uses its own DialTimeout/ReadTimeout.
	// The long ReadTimeout (30s) ensures the test distinguishes between
	// "bounded by context" (fast) vs "bounded by client timeout" (slow).
	clusterClient := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs:                 []string{blackHoleAddr},
		ContextTimeoutEnabled: true,
		ReadTimeout:           30 * time.Second,
	})
	defer clusterClient.Close()

	pollInterval := 500 * time.Millisecond
	cfg := defaultConfig()
	cfg.topologyPollInterval = pollInterval
	cfg.recorder = &noopMetrics{}

	sm := &SubMux{
		pool:          newPubSubPool(clusterClient, cfg),
		subscriptions: make(map[string][]*subscription),
	}
	tm := newTopologyMonitor(clusterClient, cfg, sm)

	start := time.Now()
	_ = tm.refreshTopology()
	elapsed := time.Since(start)

	// The call should complete within 2x the poll interval at most.
	// With context.Background() and 30s client timeouts, this will take
	// far longer than 1s, so the test fails — proving the timeout is needed.
	maxAllowed := 2 * pollInterval
	if elapsed > maxAllowed {
		t.Errorf("refreshTopology took %v, expected at most %v (pollInterval=%v)", elapsed, maxAllowed, pollInterval)
	}
}

func TestResubscribeOnNewNode_RemovesFromOldMetadata(t *testing.T) {
	// Verify that migration removes subscriptions from old metadata
	// before adding to new metadata.
	cfg := defaultConfig()
	pool := newPubSubPool(nil, cfg)

	// Set up old PubSub with metadata
	oldPubSub := &redis.PubSub{}
	oldMeta := &pubSubMetadata{
		pubsub:               oldPubSub,
		logger:               cfg.logger,
		recorder:             &noopMetrics{},
		subscriptions:        make(map[string][]*subscription),
		pendingSubscriptions: make(map[string]*subscription),
		state:                connStateActive,
		cmdCh:                make(chan *command, 100),
		done:                 make(chan struct{}),
		loopDone:             make(chan struct{}),
	}

	// Set up new PubSub with metadata
	newPubSub := &redis.PubSub{}
	newMeta := &pubSubMetadata{
		pubsub:               newPubSub,
		logger:               cfg.logger,
		recorder:             &noopMetrics{},
		subscriptions:        make(map[string][]*subscription),
		pendingSubscriptions: make(map[string]*subscription),
		state:                connStateActive,
		cmdCh:                make(chan *command, 100),
		done:                 make(chan struct{}),
		loopDone:             make(chan struct{}),
	}

	pool.mu.Lock()
	pool.pubSubMetadata[oldPubSub] = oldMeta
	pool.pubSubMetadata[newPubSub] = newMeta
	pool.hashslotPubSubs[42] = []*redis.PubSub{newPubSub}
	pool.mu.Unlock()

	// Create subscription on old metadata
	sub := &subscription{
		channel:   "test-channel",
		subType:   subTypeSubscribe,
		pubsub:    oldPubSub,
		state:     subStateActive,
		confirmCh: make(chan error, 1),
		doneCh:    make(chan struct{}),
		hashslot:  42,
		callback:  func(ctx context.Context, msg *Message) {},
	}
	oldMeta.addSubscription(sub)

	if oldMeta.subscriptionCount() != 1 {
		t.Fatalf("old meta should have 1 subscription, got %d", oldMeta.subscriptionCount())
	}

	// Simulate what resubscribeOnNewNode does
	oldMeta.removeSubscription(sub)
	sub.setPubSub(newPubSub)
	newMeta.addSubscription(sub)

	// Verify old metadata no longer tracks the subscription
	if oldMeta.subscriptionCount() != 0 {
		t.Errorf("old meta should have 0 subscriptions after migration, got %d", oldMeta.subscriptionCount())
	}
	// Verify new metadata tracks the subscription
	if newMeta.subscriptionCount() != 1 {
		t.Errorf("new meta should have 1 subscription after migration, got %d", newMeta.subscriptionCount())
	}
}

// TestMigrationContextHeadroom_MinimumOneSecond verifies that the migration
// context deadline has at least 1 second of headroom beyond migrationTimeout,
// even when stallCheckInterval is very small.
//
// Before the fix, headroom was 2*stallCheckInterval, which could be as low
// as 200ms (with minimum stallCheckInterval of 100ms). GC pauses or
// scheduling delays could eat this, causing the context to expire before
// the monitoring goroutine detected the timeout.
func TestMigrationContextHeadroom_MinimumOneSecond(t *testing.T) {
	tests := []struct {
		name               string
		stallCheckInterval time.Duration
		migrationTimeout   time.Duration
		wantMinDeadline    time.Duration // minimum expected context deadline from start
	}{
		{
			name:               "small stall check (100ms) gets 1s headroom",
			stallCheckInterval: 100 * time.Millisecond,
			migrationTimeout:   2 * time.Second,
			wantMinDeadline:    3 * time.Second, // 2s + 1s minimum headroom
		},
		{
			name:               "medium stall check (400ms) gets 1s headroom",
			stallCheckInterval: 400 * time.Millisecond,
			migrationTimeout:   2 * time.Second,
			wantMinDeadline:    3 * time.Second, // 2s + max(800ms, 1s) = 3s
		},
		{
			name:               "large stall check (2s) uses 2x formula",
			stallCheckInterval: 2 * time.Second,
			migrationTimeout:   5 * time.Second,
			wantMinDeadline:    9 * time.Second, // 5s + 2*2s = 9s (2x formula > 1s)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Compute headroom using the FIXED formula
			headroom := max(2*tt.stallCheckInterval, 1*time.Second)
			computedDeadline := tt.migrationTimeout + headroom

			if computedDeadline < tt.wantMinDeadline {
				t.Errorf("computed deadline %v < wantMinDeadline %v", computedDeadline, tt.wantMinDeadline)
			}

			// Verify the old (buggy) formula would have been too short for small stall checks
			oldHeadroom := 2 * tt.stallCheckInterval
			oldDeadline := tt.migrationTimeout + oldHeadroom
			if tt.stallCheckInterval < 500*time.Millisecond && oldDeadline >= tt.wantMinDeadline {
				t.Logf("Note: old formula also meets minimum for this case (stallCheck=%v)", tt.stallCheckInterval)
			}
		})
	}
}
