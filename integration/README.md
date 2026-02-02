# submux Integration Tests

This directory contains comprehensive integration tests running against **real Redis Cluster instances**.

The tests automatically spawn independent Redis clusters (using `redis-server`) for each test run, ensuring isolation and realistic environment simulation.

## 🧪 Test Suite Overview

| File | Purpose | Key Scenarios |
|------|---------|---------------|
| `cluster_test.go` | **Basic Functionality** | Simple Subscribe/Publish, Connection reuse. |
| `subscribe_test.go` | **Subscription Logic** | `PSUBSCRIBE`, `SSUBSCRIBE` (Sharded), multiple callbacks per channel. |
| `topology_test.go` | **Resilience** | Hashslot migration, Node failure, Auto-resubscribe, Signal messages. |
| `resiliency_test.go` | **Advanced Resilience** | Replica failure, Rolling restarts, Chaos testing. |
| `concurrency_test.go` | **Thread Safety** | Concurrent subscriptions, High-concurrency message processing. |
| `load_test.go` | **Performance** | High throughput ingestion, Memory usage under load. |
| `shared_cluster_test.go` | **Optimization** | Shared cluster infrastructure for faster parallel tests. |

## 🚀 Running Tests

### Prerequisites
- `redis-server` installed and in `$PATH`.
- `redis-cli` installed and in `$PATH`.
- Sufficient free ports (tests pick random available ports).

### Commands

```bash
# Run all integration tests (RECOMMENDED)
go test ./integration/... -v -race

# Run integration tests skipping "Short" mode
go test ./integration/... -v

# Run specific test file
go test ./integration/topology_test.go -v

# Run specific test case
go test ./integration/... -v -run TestAutoResubscribeMigration
```

> **Note**: Tests typically complete in **~8 seconds** due to parallel execution of dedicated cluster tests.

## 🔧 Infrastructure

The test harness (`cluster_setup.go`) handles the complexity of managing Redis clusters:

1.  **Random Port Allocation**: Automatically finds available ports to avoid conflicts.
2.  **Topology Generation**: Creates 3-shard clusters (1 Master + 2 Replicas each = 9 nodes total).
3.  **Event-Driven**: Uses polling for cluster readiness rather than fixed sleeps.
4.  **Auto Cleanup**: Tears down processes and deletes temporary data directories (`testdata/`) after tests.
5.  **Robust Orphan Cleanup**: PID file tracking (`testdata/.redis-test-pids`) ensures orphaned processes from interrupted runs are killed on next test startup.
6.  **Signal Handling**: Handles SIGINT, SIGTERM, and SIGQUIT for graceful cleanup on interruption.

### Directory Structure
```
integration/
├── cluster_setup.go     # Core test harness & Cluster management logic
├── ..._test.go          # Integration test implementation files
└── README.md            # This file
```

## 🎯 Test Precondition Helpers

Integration tests should use **explicit precondition checks** rather than retrying operations. This makes tests more reliable by validating underlying Redis cluster state before proceeding with test operations.

### Available Helpers

#### waitForClusterHealthy

Ensures cluster is fully operational before starting tests.

```go
func TestMyFeature(t *testing.T) {
    tc := setupTestCluster(t)
    defer tc.Cleanup()

    // Wait for cluster to be healthy before proceeding
    waitForClusterHealthy(t, tc.Client(), 10*time.Second)

    // Now safe to create subscriptions
    sm, err := submux.New(tc.Client())
    require.NoError(t, err)
    defer sm.Close()

    // Test logic here...
}
```

**Checks performed:**
- `cluster_state:ok` (cluster is operational)
- All 16384 slots are assigned
- No slots in `fail` state
- All nodes are reachable

**When to use:** At the start of every integration test, before creating SubMux instance.

---

#### waitForReplicasReady

Ensures all master nodes have required number of replicas synchronized.

```go
func TestFailover(t *testing.T) {
    tc := setupTestCluster(t)
    defer tc.Cleanup()

    waitForClusterHealthy(t, tc.Client(), 10*time.Second)

    // For failover tests, ensure replicas are ready
    waitForReplicasReady(t, tc.Client(), 1, 10*time.Second)

    // Now safe to test failover scenarios
}
```

**Checks performed:**
- All master nodes have at least N replicas
- All replicas are in `connected` state
- Replication lag is acceptable

**When to use:** Before tests that rely on replica promotion (failover, node shutdown tests).

---

#### WaitForSlotConvergence

Ensures all nodes agree on slot ownership after migrations or topology changes.

```go
func TestMigration(t *testing.T) {
    tc := setupTestCluster(t)
    defer tc.Cleanup()

    waitForClusterHealthy(t, tc.Client(), 10*time.Second)

    // Trigger hashslot migration
    sourceNode := "127.0.0.1:7000"
    targetNode := "127.0.0.1:7001"
    slot := 5000

    migrateSlot(t, tc.Client(), slot, sourceNode, targetNode)

    // CRITICAL: Wait for convergence before testing subscription behavior
    WaitForSlotConvergence(t, tc.Client(), slot, 10*time.Second)

    // Now test that subscriptions work correctly on migrated slot
}
```

**Checks performed:**
- All nodes report same owner for the slot
- Slot is not in `migrating` or `importing` state on any node
- No `MOVED` or `ASK` errors when accessing the slot

**When to use:** After any operation that changes slot ownership (migrations, manual cluster reconfiguration).

---

### Common Testing Patterns

#### Test with Topology Changes

```go
func TestAutoResubscribe(t *testing.T) {
    tc := setupTestCluster(t)
    defer tc.Cleanup()

    // 1. Ensure clean starting state
    waitForClusterHealthy(t, tc.Client(), 10*time.Second)

    // 2. Create SubMux with auto-resubscribe
    sm, err := submux.New(tc.Client(), submux.WithAutoResubscribe(true))
    require.NoError(t, err)
    defer sm.Close()

    // 3. Subscribe to channel
    received := make(chan *submux.Message, 10)
    sub, err := sm.SubscribeSync(context.Background(), []string{"test-channel"},
        func(ctx context.Context, msg *submux.Message) {
            received <- msg
        })
    require.NoError(t, err)
    defer sub.Close()

    // 4. Verify subscription works
    publishAndExpect(t, tc.Client(), "test-channel", "before-migration", received, 5*time.Second)

    // 5. Trigger migration affecting this channel
    slot := hashslot("test-channel")
    sourceNode, targetNode := findMigrationNodes(t, tc.Client(), slot)
    migrateSlot(t, tc.Client(), slot, sourceNode, targetNode)

    // 6. WAIT for convergence (critical!)
    WaitForSlotConvergence(t, tc.Client(), slot, 10*time.Second)

    // 7. Verify subscription still works after migration
    publishAndExpect(t, tc.Client(), "test-channel", "after-migration", received, 5*time.Second)
}
```

#### Test with Node Failure

```go
func TestNodeFailureRecovery(t *testing.T) {
    tc := setupTestCluster(t)
    defer tc.Cleanup()

    // 1. Ensure cluster is healthy with replicas ready
    waitForClusterHealthy(t, tc.Client(), 10*time.Second)
    waitForReplicasReady(t, tc.Client(), 1, 10*time.Second)

    // 2. Set up subscriptions
    // ... create SubMux and subscribe ...

    // 3. Kill a master node
    tc.KillNode(masterNodeID)

    // 4. Wait for failover to complete
    waitForClusterHealthy(t, tc.Client(), 20*time.Second)
    waitForReplicasReady(t, tc.Client(), 1, 20*time.Second)

    // 5. Verify subscriptions recovered
    // ... test subscription still works ...
}
```

---

### Usage Pattern Summary

```go
// ✅ Good: Explicit precondition check
err := waitForClusterHealthy(t, client, 5*time.Second)
require.NoError(t, err, "Cluster not healthy")
client.Publish(...) // Safe - cluster is healthy
```

### Why Use Precondition Checks?

1. **Better Diagnostics**: When a test fails, you know exactly which precondition wasn't met (e.g., "cluster_state:fail, slots_fail:42" vs "publish failed after 3 retries")
2. **Faster Execution**: Skip unnecessary retries when cluster is already healthy
3. **Clear Intent**: Test code explicitly states what infrastructure state is required
4. **Easier Debugging**: Logs show exactly what's being waited for and when it's achieved
5. **No Generic Retries**: The old `retryWithBackoff()` helper has been removed in favor of explicit state validation

## ⚠️ Common Issues

- **Port Conflicts**: If tests fail immediately, check if many ports are occupied. The harness tries to find free ones but limits retries.
- **Slow Startup**: If `redis-server` is slow to start (e.g., under heavy load), tests might time out. Increase `clusterStartTimeout` in `cluster_setup.go` if needed.
