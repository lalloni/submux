---
trigger: always_on
---

---
applyTo: "**/*_test.go"
---

# Testing Guidelines for submux

These rules apply when writing or modifying tests in the submux project.

## 🎯 Testing Philosophy

**Key Principles:**
1. **Test behavior, not implementation** - Focus on what the code does, not how
2. **Integration tests validate real-world scenarios** - Unit tests verify individual components
3. **Flaky tests are bugs** - Tests must be deterministic and reliable
4. **Test coverage is a guide, not a goal** - 100% coverage doesn't guarantee quality

## 📊 Current Test Coverage

- **Unit tests:** 66% coverage (target: >65%)
- **Integration tests:** 11 test suites covering critical paths
- **Test infrastructure:** Mock cluster client, retry utilities, shared test utilities

## 🧪 Test Types

### Unit Tests (`*_test.go` in root directory)

**Purpose:** Verify individual components in isolation

**Guidelines:**
- Use table-driven tests for multiple scenarios
- Mock external dependencies (Redis, network, time) when needed
- Keep tests fast (<10ms each)
- Test error paths, not just happy paths

**Example Structure:**
```go
func TestFunctionName(t *testing.T) {
    tests := []struct {
        name    string
        input   inputType
        want    outputType
        wantErr bool
    }{
        {
            name:    "success case",
            input:   validInput,
            want:    expectedOutput,
            wantErr: false,
        },
        {
            name:    "error case",
            input:   invalidInput,
            wantErr: true,
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            got, err := FunctionName(tt.input)
            if (err != nil) != tt.wantErr {
                t.Errorf("error = %v, wantErr %v", err, tt.wantErr)
                return
            }
            if !reflect.DeepEqual(got, tt.want) {
                t.Errorf("got %v, want %v", got, tt.want)
            }
        })
    }
}
```

### Integration Tests (`integration/*_test.go`)

**Purpose:** Verify system behavior with real Redis cluster

**Guidelines:**
- Run in CI with Docker-based Redis cluster
- **Use explicit precondition checks** (preferred) to validate cluster state
- Use retry utilities only for operations with inherent timing variance
- Clean up resources in `t.Cleanup()`
- Use realistic timeouts (not too short, not too long)
- Test topology changes, failures, migrations

**Test Suites:**
1. `cluster_test.go` - Basic functionality
2. `subscribe_test.go` - Subscription types (SUBSCRIBE, PSUBSCRIBE, SSUBSCRIBE)
3. `topology_test.go` - Migration detection, auto-resubscribe, signals
4. `resiliency_test.go` - Node failures, rolling restarts, chaos scenarios
5. `concurrency_test.go` - Race conditions, concurrent operations
6. `load_test.go` - High throughput, memory usage, connection limits

**Precondition Checks (Preferred):**
Use explicit state validation before operations:
```go
// ✅ Good: Explicit precondition
err := waitForClusterHealthy(t, client, 5*time.Second)
require.NoError(t, err, "Cluster not healthy")
client.Publish(...) // Safe - cluster is healthy

// ✅ Good: Wait for slot convergence after migration
err = WaitForSlotConvergence(t, cluster, slot, targetNode, 3*time.Second)
require.NoError(t, err, "Slot convergence timeout")

// ✅ Good: Ensure replicas before chaos testing
err = waitForReplicasReady(t, client, 1, 5*time.Second)
require.NoError(t, err, "Replicas not ready")
```

**Note on Generic Retries:**
The old `retryWithBackoff()` helper has been removed. Always use explicit precondition checks instead of generic retry patterns.

## 🛠️ Test Utilities

### Precondition Helpers (`integration/shared_cluster_test.go`)

**Use when:** Writing integration tests that need to validate Redis cluster state

**Available Functions:**
- `waitForClusterHealthy(client, timeout)` - Ensures cluster_state:ok, all slots assigned, no failed slots
- `waitForReplicasReady(client, requiredPerMaster, timeout)` - Ensures all masters have N replicas
- `WaitForSlotConvergence(cluster, slot, expectedOwner, timeout)` - Ensures all nodes agree on slot owner

**Benefits:**
1. **Better diagnostics** - Know what precondition failed (e.g., "cluster_state:fail" vs "publish failed")
2. **Faster execution** - No unnecessary retries if cluster is already healthy
3. **Clear intent** - Test explicitly states required infrastructure state
4. **Easier debugging** - Logs show what's being waited for and when it's achieved

**Example:**
```go
// ✅ Explicit precondition validation
err := waitForClusterHealthy(t, client, 5*time.Second)
require.NoError(t, err, "Cluster not healthy")

// Now safe to perform operations
client.Publish(...)
```

### Cluster Setup (`integration/cluster_setup.go`)

**Use when:** Writing new integration tests

**Functions:**
- `setupRedisCluster(t)` - Spin up 6-node cluster (3 masters, 3 replicas)
- `cleanupRedisCluster(t, client)` - Tear down cluster
- `migrateSlot(t, client, slot, fromNode, toNode)` - Trigger hashslot migration

## ⚠️ Handling Flaky Tests

### Common Causes
1. **Race conditions** - Use proper synchronization, test with `-race`
2. **Timing assumptions** - Use retry patterns, not fixed sleeps
3. **Shared state** - Isolate tests, use unique channels/keys
4. **Resource leaks** - Always clean up in `t.Cleanup()`
5. **External dependencies** - Mock them in unit tests

### Fixing Flaky Tests
1. **Identify the flake:** Run test 100+ times with `go test -count=100`
2. **Add logging:** Use `t.Logf()` to understand failure conditions
3. **Use retries:** Wrap timing-dependent assertions in `eventually()`
4. **Increase timeouts:** Be generous with timeouts in integration tests
5. **Isolate state:** Use unique identifiers for channels, keys, connections

### Recent Fixes (v3.0.0)
- ✅ Added retry utilities with exponential backoff
- ✅ Increased timeouts for topology polling
- ✅ Used unique channel names per test
- ✅ Fixed resource cleanup ordering
- ✅ Improved test pass rate from ~70% to >95%

## 📝 Test Naming Conventions

### Test Function Names
- Format: `Test<ComponentName>_<Scenario>`
- Examples:
  - `TestSubMux_SubscribeBasic`
  - `TestPubSubPool_ReuseConnection`
  - `TestTopologyMonitor_DetectMigration`

### Table Test Cases
- Use descriptive names: `"success with valid input"`, `"error on nil client"`
- Group related scenarios: `"error: nil input"`, `"error: invalid format"`

### Integration Test Files
- Format: `<feature>_test.go`
- Examples: `topology_test.go`, `resiliency_test.go`, `load_test.go`

## 🔍 Testing Checklist

Before submitting test code:

- [ ] All tests pass locally: `go test ./...`
- [ ] No race conditions: `go test -race ./...`
- [ ] Tests are deterministic (run 10+ times to verify)
- [ ] Error cases are tested, not just happy paths
- [ ] Resources cleaned up in `t.Cleanup()`
- [ ] Test names are descriptive
- [ ] Integration tests use retry utilities for timing-dependent operations
- [ ] Mock dependencies in unit tests
- [ ] Tests document what they're verifying (comments or test names)

## 🎯 Coverage Targets

### By Component
- **Core logic** (submux.go, pool.go): >70%
- **Event loop** (eventloop.go): >65%
- **Topology monitor** (topology.go): >60%
- **Helper utilities** (hashslot.go, workerpool.go): >80%

### Priorities
1. **Critical paths:** Subscription lifecycle, message routing, error handling
2. **Resilience:** Reconnection, migration handling, panic recovery
3. **Edge cases:** Empty inputs, concurrent operations, resource exhaustion

### Low-Value Coverage
Don't obsess over testing:
- Simple getters/setters
- Trivial type conversions
- Generated code
- Unreachable error paths (e.g., "this can never happen")

## 🚀 Running Tests

### Quick Validation
```bash
# Unit tests only (fast, run frequently)
go test ./...

# With coverage
go test -cover ./...

# Verbose output
go test -v ./...
```

### Integration Tests
```bash
# Requires Docker for Redis cluster
cd integration
go test -v

# Specific test
go test -v -run TestTopologyChanges

# With race detection
go test -race -v
```

### CI/CD Pipeline
```bash
# Full test suite with coverage
go test -race -coverprofile=coverage.out ./...
go tool cover -html=coverage.out -o coverage.html
```

## 🐛 Debugging Tests

### Common Techniques
1. **Increase verbosity:** Add `t.Logf()` statements
2. **Run isolated:** `go test -v -run TestSpecificTest`
3. **Race detection:** `go test -race -run TestSpecificTest`
4. **Disable parallelism:** Remove `t.Parallel()` calls temporarily
5. **Inspect state:** Add debug prints before cleanup

### Integration Test Debugging
```bash
# Keep Redis cluster running after test
export KEEP_CLUSTER=1
go test -v -run TestSpecificTest

# Check Redis logs
docker logs redis-node-1

# Inspect cluster state
redis-cli -c -p 7000 cluster nodes
```

## 📚 Additional Resources

- **Integration Test Guide:** `integration/README.md`
- **Test Infrastructure:** `testutil/` directory
- **Go Testing Best Practices:** https://go.dev/doc/effective_go#testing
- **Table-Driven Tests:** https://go.dev/wiki/TableDrivenTests
