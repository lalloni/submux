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

| Helper | Purpose | Checks |
|--------|---------|--------|
| `waitForClusterHealthy(client, timeout)` | Ensures cluster is operational | `cluster_state:ok`, all 16384 slots assigned, no failed slots |
| `waitForReplicasReady(client, requiredPerMaster, timeout)` | Ensures replication is available | All masters have N connected replicas |
| `WaitForSlotConvergence(cluster, slot, expectedOwner, timeout)` | Ensures consistent slot routing | All nodes agree on slot owner |

### Usage Pattern

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
