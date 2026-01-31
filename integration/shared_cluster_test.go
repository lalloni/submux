package integration

import (
	"context"
	"fmt"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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

// retryWithBackoff executes the given function with exponential backoff.
// It returns nil on success, or the last error after all retries are exhausted.
// This is useful for topology-sensitive tests where cluster state may need time to converge.
func retryWithBackoff(t testing.TB, maxAttempts int, initialDelay time.Duration, fn func() error) error {
	t.Helper()

	var lastErr error
	delay := initialDelay

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		lastErr = fn()
		if lastErr == nil {
			return nil
		}

		if attempt < maxAttempts {
			t.Logf("Attempt %d/%d failed: %v, retrying in %v", attempt, maxAttempts, lastErr, delay)
			time.Sleep(delay)
			delay = min(delay*2, 5*time.Second) // Exponential backoff, capped at 5s
		}
	}

	return fmt.Errorf("all %d attempts failed, last error: %w", maxAttempts, lastErr)
}

// waitForCondition polls a condition function until it returns true or timeout is reached.
// This is useful for waiting on cluster state changes in tests.
func waitForCondition(t testing.TB, timeout time.Duration, pollInterval time.Duration, condition func() bool, description string) bool {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return true
		}
		time.Sleep(pollInterval)
	}
	t.Logf("Condition not met within %v: %s", timeout, description)
	return false
}
