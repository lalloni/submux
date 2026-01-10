package submux

import (
	"testing"

	"github.com/redis/go-redis/v9"
)

// Helper function to build a topology state directly for testing
func buildTopologyState(slots map[int]string) *topologyState {
	ts := newTopologyState()
	ts.hashslotToNode = make(map[int]string)
	ts.nodeToHashslots = make(map[string][]int)

	for slot, node := range slots {
		ts.hashslotToNode[slot] = node
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
