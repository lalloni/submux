package submux

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// TestRefreshTopology_DoesNotBlockLookups verifies that getNodeForHashslot
// does not block while refreshTopology is performing network I/O.
// Before the fix, refreshTopology held tm.mu for the entire duration
// (including network calls), causing getNodeForHashslot to block.
func TestRefreshTopology_DoesNotBlockLookups(t *testing.T) {
	cfg := defaultConfig()
	cfg.recorder = &noopMetrics{}
	cfg.topologyPollInterval = 10 * time.Second // Don't auto-poll

	// Create a ClusterClient that will be slow to respond to ClusterSlots.
	// We use a real ClusterClient pointed at an unreachable address with a long timeout.
	// refreshTopology will block on the network call.
	clusterClient := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs:       []string{"192.0.2.1:6379"}, // RFC 5737 TEST-NET, unreachable
		DialTimeout: 3 * time.Second,
		ReadTimeout: 3 * time.Second,
	})
	defer clusterClient.Close()

	sm := &SubMux{
		subscriptions: make(map[string][]*subscription),
	}
	pool := newPubSubPool(clusterClient, cfg)
	sm.pool = pool

	tm := newTopologyMonitor(clusterClient, cfg, sm)
	pool.setTopologyMonitor(tm)

	// Pre-populate topology so getNodeForHashslot has something to return
	initialState := buildTopologyState(map[int]string{42: "node1:7000"})
	tm.mu.Lock()
	tm.currentState = initialState
	tm.mu.Unlock()

	// Start refreshTopology in background — it will block on network I/O
	var refreshDone atomic.Bool
	go func() {
		_ = tm.refreshTopology() // Will fail eventually, that's fine
		refreshDone.Store(true)
	}()

	// Give refreshTopology time to acquire the lock and start the network call
	time.Sleep(200 * time.Millisecond)

	// Now try to call getNodeForHashslot — it should NOT block
	var lookupDone atomic.Bool
	var lookupNode string
	var lookupOK bool

	go func() {
		lookupNode, lookupOK = tm.getNodeForHashslot(42)
		lookupDone.Store(true)
	}()

	// Wait up to 500ms for the lookup to complete
	deadline := time.After(500 * time.Millisecond)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-deadline:
			if !lookupDone.Load() {
				t.Fatal("getNodeForHashslot blocked for >500ms while refreshTopology is running — lock held during network I/O")
			}
			goto done
		case <-ticker.C:
			if lookupDone.Load() {
				goto done
			}
		}
	}
done:

	if !lookupOK {
		t.Error("getNodeForHashslot returned false after pre-populating state")
	}
	if lookupNode != "node1:7000" {
		t.Errorf("getNodeForHashslot returned %q, want %q", lookupNode, "node1:7000")
	}

	// refreshTopology should still be running (blocked on network)
	if refreshDone.Load() {
		t.Log("Note: refreshTopology completed before expected — test may be non-deterministic on fast networks")
	}
}
