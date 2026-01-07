# submux Integration Tests

This directory contains comprehensive integration tests running against **real Redis Cluster instances**.

The tests automatically spawn independent Redis clusters (using `redis-server`) for each test run, ensuring isolation and realistic environment simulation.

## 🧪 Test Suite Overview

| File | Purpose | Key Scenarios |
|------|---------|---------------|
| `cluster_test.go` | **Basic Functionality** | Simple Subscribe/Publish, Connection reuse. |
| `subscribe_test.go` | **Subscription Logic** | `PSUBSCRIBE`, `SSUBSCRIBE` (Sharded), multiple callbacks per channel. |
| `topology_test.go` | **Resilience** | Hashslot migration, Node failure, Auto-resubscribe, Signal messages. |
| `concurrency_test.go` | **Threa Safety** | Concurrent subscriptions, High-concurrency message processing. |
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

> **Note**: Tests typically take **20-50 seconds** to complete as they spin up real clusters.

## 🔧 Infrastructure

The test harness (`cluster_setup.go`) handles the complexity of managing Redis clusters:

1.  **Random Port Allocation**: Automatically finds available ports to avoid conflicts.
2.  **Topology Generation**: Creates 3-shard clusters (1 Master + 2 Replicas each = 9 nodes total).
3.  **Event-Driven**: Uses polling for cluster readiness rather than fixed sleeps.
4.  **Auto Cleanup**: Tears down processes and deletes temporary data directories (`testdata/`) after tests.

### Directory Structure
```
integration/
├── cluster_setup.go     # Core test harness & Cluster management logic
├── ..._test.go          # Integration test implementation files
└── README.md            # This file
```

## ⚠️ Common Issues

- **Port Conflicts**: If tests fail immediately, check if many ports are occupied. The harness tries to find free ones but limits retries.
- **Slow Startup**: If `redis-server` is slow to start (e.g., under heavy load), tests might time out. Increase `clusterStartTimeout` in `cluster_setup.go` if needed.
