package integration

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
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

// waitForClusterHealthy polls until cluster_state:ok and all expected conditions are met.
// This validates that the Redis cluster is in a healthy operational state before proceeding.
//
// Checks performed:
// - cluster_state:ok (cluster is operational)
// - cluster_slots_assigned:16384 (all slots are assigned)
// - cluster_slots_fail:0 (no failed slots)
//
// Returns nil on success, or an error with diagnostic information on timeout.
func waitForClusterHealthy(t testing.TB, client *redis.ClusterClient, timeout time.Duration) error {
	t.Helper()

	start := time.Now()
	t.Logf("Waiting for cluster healthy (timeout=%v)...", timeout)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	var lastInfo string
	for {
		select {
		case <-ctx.Done():
			// Timeout - provide diagnostic information
			return fmt.Errorf("cluster not healthy within %v:\n%s", timeout, lastInfo)
		case <-ticker.C:
			info, err := client.ClusterInfo(ctx).Result()
			if err != nil {
				t.Logf("ClusterInfo error: %v", err)
				lastInfo = fmt.Sprintf("Error: %v", err)
				continue
			}
			lastInfo = info

			// Check cluster_state:ok
			if !strings.Contains(info, "cluster_state:ok") {
				t.Logf("Cluster state not ok")
				continue
			}

			// Extract and check slots_assigned count
			if match := regexp.MustCompile(`cluster_slots_assigned:(\d+)`).FindStringSubmatch(info); len(match) > 1 {
				assigned, _ := strconv.Atoi(match[1])
				if assigned != 16384 {
					t.Logf("Only %d/16384 slots assigned", assigned)
					continue
				}
			} else {
				t.Logf("Could not parse cluster_slots_assigned")
				continue
			}

			// Extract and check slots_fail count
			if match := regexp.MustCompile(`cluster_slots_fail:(\d+)`).FindStringSubmatch(info); len(match) > 1 {
				failed, _ := strconv.Atoi(match[1])
				if failed > 0 {
					t.Logf("%d slots in failed state", failed)
					continue
				}
			} else {
				t.Logf("Could not parse cluster_slots_fail")
				continue
			}

			// All checks passed
			elapsed := time.Since(start)
			t.Logf("✓ Cluster healthy after %v", elapsed)
			return nil
		}
	}
}

// waitForReplicasReady polls until all masters have the required number of connected replicas.
// This ensures replication is available before tests that depend on replica failover or redundancy.
//
// Returns nil on success, or an error with diagnostic information on timeout.
func waitForReplicasReady(t testing.TB, client *redis.ClusterClient, requiredPerMaster int, timeout time.Duration) error {
	t.Helper()

	start := time.Now()
	t.Logf("Waiting for replicas ready (required=%d per master, timeout=%v)...", requiredPerMaster, timeout)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	var lastDiagnostic string
	for {
		select {
		case <-ctx.Done():
			// Timeout - provide diagnostic information
			return fmt.Errorf("replicas not ready within %v:\n%s", timeout, lastDiagnostic)
		case <-ticker.C:
			nodes, err := client.ClusterNodes(ctx).Result()
			if err != nil {
				t.Logf("ClusterNodes error: %v", err)
				lastDiagnostic = fmt.Sprintf("Error: %v", err)
				continue
			}

			// Parse nodes and count replicas per master
			masterReplicaCounts := make(map[string]int)
			masterIDs := make(map[string]string) // nodeID -> address for diagnostics
			lines := strings.Split(nodes, "\n")

			for _, line := range lines {
				if line == "" {
					continue
				}

				fields := strings.Fields(line)
				if len(fields) < 8 {
					continue
				}

				nodeID := fields[0]
				address := fields[1]
				flags := fields[2]

				if strings.Contains(flags, "slave") || strings.Contains(flags, "replica") {
					masterID := fields[3]
					// Don't count failed replicas
					if !strings.Contains(flags, "fail") {
						masterReplicaCounts[masterID]++
					}
				}

				if strings.Contains(flags, "master") {
					masterIDs[nodeID] = address
				}
			}

			// Check if all masters have required replicas
			allReady := true
			var diagnosticParts []string
			for nodeID, address := range masterIDs {
				replicaCount := masterReplicaCounts[nodeID]
				if replicaCount < requiredPerMaster {
					allReady = false
					diagnosticParts = append(diagnosticParts,
						fmt.Sprintf("Master %s (%s) has %d/%d replicas",
							nodeID[:8], address, replicaCount, requiredPerMaster))
				}
			}

			if !allReady {
				lastDiagnostic = strings.Join(diagnosticParts, "\n")
				continue
			}

			// All masters have required replicas
			elapsed := time.Since(start)
			t.Logf("✓ Replicas ready after %v", elapsed)
			return nil
		}
	}
}

// WaitForSlotConvergence polls until all master nodes agree on the owner of a hashslot.
// This is critical after slot migrations to ensure all nodes have consistent routing information.
//
// The function queries all nodes in the cluster and verifies they all report the same owner
// for the specified slot. This prevents MOVED errors and ensures stable message routing.
//
// Returns nil on success, or an error with diagnostic information on timeout.
func WaitForSlotConvergence(t testing.TB, cluster *TestCluster, slot int, expectedOwner string, timeout time.Duration) error {
	t.Helper()

	start := time.Now()
	t.Logf("Waiting for slot %d convergence to %s (timeout=%v)...", slot, expectedOwner, timeout)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Timeout - provide diagnostic information
			return fmt.Errorf("slot %d did not converge to owner %s within %v", slot, expectedOwner, timeout)
		case <-ticker.C:
			allAgree := true
			var disagreements []string

			for _, node := range cluster.GetNodes() {
				owner, err := getSlotOwnerFromNode(ctx, node.Address, slot)
				if err != nil {
					t.Logf("Error querying node %s: %v", node.Address, err)
					allAgree = false
					disagreements = append(disagreements,
						fmt.Sprintf("%s: error - %v", node.Address, err))
					break
				}
				if owner != expectedOwner {
					allAgree = false
					disagreements = append(disagreements,
						fmt.Sprintf("%s: reports owner as %s", node.Address, owner))
				}
			}

			if allAgree {
				elapsed := time.Since(start)
				t.Logf("✓ Slot %d converged to %s after %v", slot, expectedOwner, elapsed)
				return nil
			}

			// Log disagreements for debugging
			if len(disagreements) > 0 {
				t.Logf("Slot convergence disagreements: %s", strings.Join(disagreements, "; "))
			}
		}
	}
}

// getSlotOwnerFromNode queries a specific node for who owns a slot.
// This is a helper function for WaitForSlotConvergence.
func getSlotOwnerFromNode(ctx context.Context, nodeAddr string, slot int) (string, error) {
	client := redis.NewClient(&redis.Options{Addr: nodeAddr})
	defer client.Close()

	slots, err := client.ClusterSlots(ctx).Result()
	if err != nil {
		return "", err
	}

	for _, slotRange := range slots {
		if slot >= int(slotRange.Start) && slot <= int(slotRange.End) {
			if len(slotRange.Nodes) > 0 {
				return slotRange.Nodes[0].Addr, nil
			}
		}
	}

	return "", fmt.Errorf("slot %d not found in cluster slots", slot)
}
