package integration

import (
	"context"
	"fmt"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
)

var (
	sharedCluster     *TestCluster
	sharedClusterOnce sync.Once
	sharedClusterErr  error
	channelCounter    atomic.Uint64
)

// getSharedCluster returns a shared cluster for all tests.
// This avoids the overhead of creating a new cluster for each test.
func getSharedCluster(t testing.TB) *TestCluster {
	t.Helper()

	sharedClusterOnce.Do(func() {
		// Check if redis-server and redis-cli are available
		if _, err := exec.LookPath("redis-server"); err != nil {
			sharedClusterErr = err
			return
		}
		if _, err := exec.LookPath("redis-cli"); err != nil {
			sharedClusterErr = err
			return
		}

		// Create a 9-node cluster (3 shards × 3 nodes: 1 master + 2 replicas per shard)
		ctx := context.Background()

		cluster, err := StartCluster(ctx, 3, 2, "shared-cluster")
		if err != nil {
			sharedClusterErr = err
			return
		}

		sharedCluster = cluster
	})

	if sharedClusterErr != nil {
		t.Fatalf("redis tools not available: %v", sharedClusterErr)
	}

	if sharedCluster == nil {
		t.Fatal("shared cluster is nil")
	}

	return sharedCluster
}

// uniqueChannel returns a unique channel name for use in tests.
// This prevents parallel tests from interfering with each other.
func uniqueChannel(base string) string {
	id := channelCounter.Add(1)
	return fmt.Sprintf("%s-%d", base, id)
}

// TestMain removed - moved to main_test.go for global package cleanup
