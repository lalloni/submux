# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

---

## 📚 Documentation Structure

**IMPORTANT: For all architectural, design, and API questions, consult [DESIGN.md](DESIGN.md) first.**

- **[DESIGN.md](DESIGN.md)** - **PRIMARY TECHNICAL REFERENCE** for architecture, design patterns, API design, resilience, testing strategy, and best practices
- **[README.md](README.md)** - User-facing quick start and installation guide
- **[TODO.md](TODO.md)** - Project roadmap and pending work items
- **[CHANGELOG.md](CHANGELOG.md)** - Release history and version changes

**This file (CLAUDE.md)** contains only Claude Code-specific workflow guidance and development commands. All design decisions are documented in DESIGN.md.

---

## Project Overview

**submux** is a Go library that provides intelligent connection multiplexing for Redis Cluster Pub/Sub operations. It significantly reduces connection overhead by reusing connections based on hashslot routing, while maintaining topology awareness and resilience during cluster reconfigurations.

**For detailed architecture, design patterns, and component descriptions:** See [DESIGN.md Section 2: Architecture](DESIGN.md#2-architecture)

---

## Development Commands

### Testing

```bash
# Run all unit tests with race detector
go test -v -race

# Run all tests including integration tests
go test ./... -v -race

# Run only integration tests (requires redis-server and redis-cli in PATH)
go test ./integration/... -v -race

# Run specific integration test
go test ./integration/... -v -run TestAutoResubscribeMigration

# Run benchmarks
go test -bench=. -benchmem
```

**Important:** Integration tests automatically spawn real Redis Cluster instances (9 nodes: 3 shards with 1 master + 2 replicas each) on random ports. Tests typically complete in ~8 seconds due to parallel execution. Ensure `redis-server` and `redis-cli` are installed and in `$PATH`.

**For complete testing strategy and test scenarios:** See [DESIGN.md Section 5: Testing Strategy](DESIGN.md#5-testing-strategy)

### Building and Linting

```bash
# Build (standard Go module)
go build

# Run go vet
go vet ./...

# Format code
go fmt ./...

# Run static analysis
staticcheck ./...
```

---

## Code Structure Quick Reference

### Core Files
- `submux.go` - Main API entry point (`SubMux` type, subscription methods)
- `config.go` - Configuration options and defaults
- `pool.go` - PubSub connection pool management
- `eventloop.go` - Single event loop per connection (command sending + message receiving)
- `topology.go` - Cluster topology monitoring and auto-resubscribe logic
- `subscription.go` - Internal subscription state machine
- `types.go` - Public types (`Message`, `SignalInfo`, event types)
- `callback.go` - Callback invocation with panic recovery
- `errors.go` - Exported error types

### Metrics (OpenTelemetry)
- `metrics.go` - Metrics abstraction interface and no-op implementation
- `metrics_otel.go` - OpenTelemetry implementation (build tag: `!nometrics`)
- `metrics_test.go` - Metrics unit tests and benchmarks

### Testing
- `integration/` - Integration test suite with real Redis clusters
- `testutil/` - Test helpers and mocks

**For detailed component descriptions and responsibilities:** See [DESIGN.md Section 2.1: High-Level Design](DESIGN.md#21-high-level-design)

---

## Configuration Options

Available via `submux.New(clusterClient, options...)`:

- `WithAutoResubscribe(bool)` - Enable automatic migration handling (default: `false`)
- `WithNodePreference(NodePreference)` - Set node distribution strategy (default: `BalancedAll`)
- `WithTopologyPollInterval(time.Duration)` - Cluster topology refresh rate (default: `1s`, min: `100ms`)
- `WithMinConnectionsPerNode(int)` - Minimum connection pool size per node (default: `1`)
- `WithMigrationTimeout(time.Duration)` - Maximum duration for migration resubscription (default: `30s`)
- `WithMigrationStallCheck(time.Duration)` - How often to check for stalled migrations (default: `2s`)
- `WithLogger(*slog.Logger)` - Custom structured logger (default: `slog.Default()`)
- `WithMeterProvider(metric.MeterProvider)` - OpenTelemetry metrics provider (default: `nil`, metrics disabled)

**For detailed configuration documentation:** See [DESIGN.md Section 4.3: Configuration Options](DESIGN.md#43-configuration-options)

---

## Key Design Patterns (Summary)

**For complete design pattern documentation, consult [DESIGN.md Section 2](DESIGN.md#2-architecture)**

Quick reference:
- **Hashslot-Based Routing**: `CRC16(channel) % 16384` - See [DESIGN.md 2.2](DESIGN.md#22-connection-multiplexing)
- **Single Event Loop**: One goroutine per connection handles all I/O - See [DESIGN.md 2.3](DESIGN.md#23-single-event-loop-architecture)
- **Auto-Resubscribe**: Automatic migration handling when enabled - See [DESIGN.md 3.2](DESIGN.md#32-migration-recovery-auto-resubscribe)
- **Signal Messages**: Topology events sent to callbacks - See [DESIGN.md 3.2](DESIGN.md#32-migration-recovery-auto-resubscribe)
- **Node Distribution**: Three strategies (BalancedAll, PreferMasters, PreferReplicas) - See [DESIGN.md 4.3](DESIGN.md#43-configuration-options)

---

## Subscription Types

submux supports three Redis pub/sub mechanisms:

1. **SUBSCRIBE** (`SubscribeSync`) - Exact channel name matching
2. **PSUBSCRIBE** (`PSubscribeSync`) - Glob-style pattern matching (`news:*`)
3. **SSUBSCRIBE** (`SSubscribeSync`) - Sharded pub/sub (Redis 7.0+)

**For detailed API documentation:** See [DESIGN.md Section 4: API Design](DESIGN.md#4-api-design)

---

## Message and Event Types

Messages have a `Type` field that distinguishes:
- `MessageTypeMessage` - Regular SUBSCRIBE message
- `MessageTypePMessage` - Pattern PSUBSCRIBE message
- `MessageTypeSMessage` - Sharded SSUBSCRIBE message (Redis 7.0+)
- `MessageTypeSignal` - Topology change notification

Signal events include:
- `EventNodeFailure` - Connection lost
- `EventMigration` - Hashslot migration detected
- `EventMigrationStalled` - Migration stalled (>2s)
- `EventMigrationTimeout` - Migration timeout (>30s)

**Callbacks MUST handle `MessageTypeSignal` for production monitoring.**

**For complete message type documentation:** See [DESIGN.md Section 4.1: Core Types](DESIGN.md#41-core-types)

---

## Code Conventions

- Use `sync.RWMutex` for topology state (many readers, few writers)
- Use `sync.Mutex` for connection pool and subscription state
- Always defer cleanup: `defer sub.Unsubscribe(ctx)` and `defer subMux.Close()`
- Callbacks should be fast; offload heavy work to queues
- Integration tests with dedicated clusters use `t.Parallel()` for faster execution
- When modifying event loop, ensure commands and messages are handled atomically to avoid race conditions
- **Never use `time.Sleep` in tests** - use event polling or channel synchronization

**For complete best practices:** See [DESIGN.md Section 6: Best Practices](DESIGN.md#6-best-practices)

---

## Common Gotchas

1. **Auto-resubscribe is disabled by default** - Must explicitly enable with `WithAutoResubscribe(true)`
2. **Context timeouts apply to subscription confirmation** - Not to message delivery
3. **Multiple subscriptions to same channel** - Each callback gets its own `Sub`; unsubscribe only removes that callback
4. **Signal messages sent even with auto-resubscribe enabled** - Monitor these for observability
5. **SSUBSCRIBE requires Redis 7.0+** - Use `SubscribeSync`/`PSubscribeSync` for older versions
6. **Callbacks run in separate goroutines** - Must be thread-safe and non-blocking
7. **Panic recovery** - Callbacks are wrapped with panic recovery; panics are logged but don't crash the system

**For detailed gotcha explanations and solutions:** See [DESIGN.md Section 6: Best Practices](DESIGN.md#6-best-practices)

---

## OpenTelemetry Metrics

submux provides optional OpenTelemetry instrumentation with **zero overhead when disabled**.

### Quick Start

```go
import (
    "github.com/lalloni/submux"
    sdkmetric "go.opentelemetry.io/otel/sdk/metric"
    "go.opentelemetry.io/otel/exporters/prometheus"
)

exporter, _ := prometheus.New()
provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter))

subMux, _ := submux.New(clusterClient,
    submux.WithMeterProvider(provider),
    submux.WithAutoResubscribe(true),
)
```

### Metrics Summary

- **21 total metrics**: 11 counters, 4 histograms, 2 observable gauges (planned)
- **Metric prefix**: `submux.*`
- **Cardinality-safe**: No channel names as attributes
- **Performance**: 0.1 ns/op (no-op), 150-210 ns/op (with OTEL)

**Key metrics:**
- Message throughput and latency
- Connection creation and failures
- Migration events and duration
- Callback invocations and panics
- Topology refresh operations

**For complete metrics documentation:** See [DESIGN.md Section 6.5: Observability](DESIGN.md#65-observability)

---

## Testing Architecture

### Unit Tests
Use `testutil/mock_cluster.go` to mock `ClusterClient` for isolated testing.

### Integration Tests
Located in `integration/`. The test harness (`cluster_setup.go`) manages real Redis clusters with:
- Dynamic port allocation
- Process group isolation
- PID file tracking for cleanup
- Signal handlers for graceful shutdown

**Key test files:**
- `cluster_test.go` - Basic Pub/Sub functionality
- `subscribe_test.go` - PSUBSCRIBE, SSUBSCRIBE, multiple callbacks
- `topology_test.go` - Migration detection, signals, auto-resubscribe
- `resiliency_test.go` - Replica failures, rolling restarts, chaos testing
- `concurrency_test.go` - Race conditions, concurrent subscriptions
- `load_test.go` - High throughput, memory usage

**For complete testing strategy:** See [DESIGN.md Section 5: Testing Strategy](DESIGN.md#5-testing-strategy)

---

## Concurrency Model

- All public APIs are thread-safe
- Each PubSub connection has its own event loop goroutine
- Topology monitor runs in separate background goroutine
- Callbacks invoked asynchronously (one goroutine per message)
- Internal state protected by mutexes (`sync.RWMutex` for reads, `sync.Mutex` for writes)

**For detailed concurrency patterns:** See [DESIGN.md Section 2.3: Single Event Loop Architecture](DESIGN.md#23-single-event-loop-architecture)

---

## Exported Errors

Sentinel errors defined in `errors.go`:
- `ErrInvalidClusterClient` - Nil or invalid cluster client
- `ErrInvalidChannel` - Empty or invalid channel name
- `ErrSubscriptionFailed` - Subscription operation failed
- `ErrConnectionFailed` - Connection to Redis node failed
- `ErrClosed` - Operation on closed SubMux

**For error handling patterns:** See [DESIGN.md Section 6.2: Error Handling](DESIGN.md#62-error-handling)

---

## When Making Changes

### Architecture/Design Changes
1. **Update [DESIGN.md](DESIGN.md) first** - It's the source of truth
2. Implement the change
3. Update this file (CLAUDE.md) only if workflow/tooling changes
4. Update README.md if user-facing API changes

### Testing Changes
1. Run all tests: `go test ./... -v -race`
2. Verify integration tests pass
3. Check for race conditions (always use `-race` flag)
4. Update test documentation in DESIGN.md if test strategy changes

### Metrics Changes
1. Update metrics implementation in `metrics_otel.go`
2. Update metrics tests in `metrics_test.go`
3. Update metrics documentation in DESIGN.md Section 6.5
4. Run benchmarks to verify performance: `go test -bench=BenchmarkNoopMetrics -benchmem`

---

## Quick Decision Tree

**"Where should I look for information about..."**

- **Architecture/Design patterns?** → [DESIGN.md Section 2](DESIGN.md#2-architecture)
- **Configuration options?** → [DESIGN.md Section 4.3](DESIGN.md#43-configuration-options)
- **API usage?** → [DESIGN.md Section 4](DESIGN.md#4-api-design)
- **Testing strategy?** → [DESIGN.md Section 5](DESIGN.md#5-testing-strategy)
- **Best practices?** → [DESIGN.md Section 6](DESIGN.md#6-best-practices)
- **Metrics/Observability?** → [DESIGN.md Section 6.5](DESIGN.md#65-observability)
- **Development commands?** → This file (CLAUDE.md)
- **User quick start?** → [README.md](README.md)
- **Pending work?** → [TODO.md](TODO.md)

**When in doubt: Start with [DESIGN.md](DESIGN.md)**
