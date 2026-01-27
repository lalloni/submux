# submux: Agent Guidelines

This document provides a high-level overview of the **submux** project for AI agents and human developers.

---

## 📚 Documentation Structure - READ THIS FIRST

**🎯 CRITICAL: For ALL architectural, design, and implementation questions, consult [DESIGN.md](DESIGN.md) immediately.**

### Documentation Hierarchy

1. **[DESIGN.md](DESIGN.md)** - **PRIMARY TECHNICAL REFERENCE** (Architecture, Design Patterns, API Design, Resilience, Testing Strategy, Best Practices)
2. **[README.md](README.md)** - User-facing quick start and installation guide
3. **[TODO.md](TODO.md)** - Project roadmap and pending work items
4. **[CHANGELOG.md](CHANGELOG.md)** - Release history and version changes
5. **[integration/README.md](integration/README.md)** - Integration test suite guide

**This file (AGENTS.md)** provides only a quick-start overview and common workflows. **All design decisions are documented in DESIGN.md.**

---

## 🏢 Project Overview

**submux** is a Go library that acts as a multiplexer for Redis Cluster Pub/Sub connections. It allows thousands of logical subscriptions to share a small number of physical TCP connections, overcoming Redis client limitations and handling cluster topology changes transparently.

**Key Features:**
- Intelligent connection multiplexing based on hashslot routing
- Automatic topology monitoring and migration handling
- Load balancing across master and replica nodes
- Optional OpenTelemetry metrics for production observability

**For complete architecture and problem statement:** See [DESIGN.md Section 1-2](DESIGN.md#1-overview)

---

## 📂 Directory Structure and Key Files

### Root Package (Public API)
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
  - `cluster_setup.go` - Test infrastructure and cluster management
  - `cluster_test.go` - Basic Pub/Sub functionality
  - `subscribe_test.go` - PSUBSCRIBE, SSUBSCRIBE, multiple callbacks
  - `topology_test.go` - Migration detection, signals, auto-resubscribe
  - `resiliency_test.go` - Replica failures, rolling restarts, chaos testing
  - `concurrency_test.go` - Race conditions, concurrent subscriptions
  - `load_test.go` - High throughput, memory usage
- `testutil/` - Test helpers and mocks
  - `mock_cluster.go` - Mock ClusterClient for unit tests

**For detailed component descriptions:** See [DESIGN.md Section 2.1: High-Level Design](DESIGN.md#21-high-level-design)

---

## 🏗️ Architecture Quick Reference

**⚠️ IMPORTANT: These are summaries only. For complete architectural details, consult [DESIGN.md Section 2: Architecture](DESIGN.md#2-architecture)**

### Core Components

1. **SubMux** (`submux.go`) - Main API entry point, manages subscriptions and routing
2. **PubSub Pool** (`pool.go`) - Manages physical `*redis.PubSub` connections, implements connection reuse
3. **Event Loop** (`eventloop.go`) - Single goroutine per connection handles all I/O (commands + messages)
4. **Topology Monitor** (`topology.go`) - Background goroutine monitors cluster state, handles migrations
5. **Subscription** (`subscription.go`) - Internal state machine (Pending → Active → Failed/Closed)

**For detailed component responsibilities:** See [DESIGN.md Section 2](DESIGN.md#2-architecture)

### Key Design Patterns

**⚠️ These are summaries. For implementation details, see [DESIGN.md Section 2](DESIGN.md#2-architecture)**

- **Hashslot-Based Routing**: `CRC16(channel) % 16384` - See [DESIGN.md 2.2](DESIGN.md#22-connection-multiplexing)
- **Single Event Loop**: One goroutine per connection, eliminates synchronization complexity - See [DESIGN.md 2.3](DESIGN.md#23-single-event-loop-architecture)
- **Auto-Resubscribe**: Automatic migration handling when enabled via `WithAutoResubscribe(true)` - See [DESIGN.md 3.2](DESIGN.md#32-migration-recovery-auto-resubscribe)
- **Signal Messages**: Topology events sent to callbacks for monitoring - See [DESIGN.md 3.2](DESIGN.md#32-migration-recovery-auto-resubscribe)
- **Topology Awareness**: Dual detection (polling + MOVED/ASK errors) - See [DESIGN.md 3.1](DESIGN.md#31-topology-change-detection)

### Concurrency Model

**⚠️ For complete concurrency patterns, see [DESIGN.md Section 2.3](DESIGN.md#23-single-event-loop-architecture)**

Key concurrency characteristics:
- **All public APIs are thread-safe** - Safe to call from multiple goroutines
- **Single event loop per connection** - Each PubSub connection has one goroutine
- **Topology monitor** - Separate background goroutine polls cluster state
- **Callback invocation** - Each message spawns a new goroutine for the callback
- **Panic recovery** - Callbacks wrapped with recovery; panics logged but don't crash
- **State protection** - `sync.RWMutex` for topology (many readers), `sync.Mutex` for pool/subscriptions

---

## 🧪 Testing Quick Reference

**For complete testing strategy:** See [DESIGN.md Section 5: Testing Strategy](DESIGN.md#5-testing-strategy)

### Running Tests

```bash
# All tests (unit + integration) with race detector
go test ./... -v -race -timeout=30s

# Unit tests only
go test -v -race

# Integration tests only (requires redis-server and redis-cli in PATH)
go test ./integration/... -v -race

# Specific test
go test ./integration/... -v -run TestAutoResubscribeMigration

# Benchmarks
go test -bench=. -benchmem
```

### Integration Test Infrastructure

- **Requirement**: `redis-server` and `redis-cli` must be in `$PATH`
- **Cluster Topology**: 9 nodes (3 shards × 3 nodes: 1 master + 2 replicas per shard)
- **Port Allocation**: Random ports to avoid conflicts
- **Execution Time**: ~8 seconds (parallel execution)
- **Cleanup**: Automatic via PID tracking and signal handlers

**For detailed test infrastructure:** See [DESIGN.md Section 5: Testing Strategy](DESIGN.md#5-testing-strategy)

---

## 🔨 Building and Linting

### Build Commands

```bash
# Build the project
go build

# Build without OpenTelemetry dependencies
go build -tags nometrics -o submux-nometrics
```

### Code Quality Tools

```bash
# Format code (run before every commit)
go fmt ./...

# Static analysis
go vet ./...

# Advanced static analysis (if available)
staticcheck ./...
```

**Always run these before committing** - See [Critical Rules](#️-critical-rules---must-follow) below.

---

## 🛠️ Common Development Tasks

### How to Add a New Feature

1. **Consult [DESIGN.md](DESIGN.md)** - Understand existing architecture and patterns
2. **Update [DESIGN.md](DESIGN.md) first** - Document the design before implementation
3. **Implement** in appropriate component file
4. **Add unit tests** for isolated logic
5. **Add integration test** if feature involves Redis interaction
6. **Update [README.md](README.md)** if user-facing API changes
7. **Run all tests**: `go test ./... -v -race`

### How to Debug Topology Issues

```bash
# Run topology tests with verbose logging
go test ./integration/topology_test.go -v -race

# Check specific migration scenario
go test ./integration/... -v -run TestAutoResubscribeMigration
```

### How to Verify Performance

```bash
# Run benchmarks
go test -bench=. -benchmem

# Check metrics overhead
go test -bench=BenchmarkNoopMetrics -benchmem
go test -bench=BenchmarkOtelMetrics -benchmem
```

---

## ⚠️ Critical Rules - MUST FOLLOW

### 1. Documentation Updates
- **Always update [DESIGN.md](DESIGN.md) FIRST** when changing architecture, design patterns, or API behavior
- **Never leave documentation drift** - update docs immediately with code changes
- If you modify logic, update GoDoc comments above it
- Add entries to [CHANGELOG.md](CHANGELOG.md) for notable changes

### 2. Testing Requirements
- **NO `time.Sleep` in tests** - Use event polling (Eventually) or channel synchronization
- **Always run with `-race` flag** - Race conditions must be caught
- **Verify all tests pass** after every change: `go test ./... -v -race`
- Integration tests must clean up processes (handled automatically via signal handlers)

### 3. Code Conventions
- **Respect Single Event Loop** - Do not spawn new goroutines for I/O inside `eventloop.go`
- **Thread safety** - Use `sync.RWMutex` for topology, `sync.Mutex` for pool/subscriptions
- **Panic recovery** - Callbacks are wrapped; don't add additional recovery unless necessary
- **Fast callbacks** - Offload heavy work to queues/worker pools

### 4. Style Guidelines
- **Run `gofmt`** before committing: `go fmt ./...`
- **Run `go vet`**: `go vet ./...`
- **Run `staticcheck`**: `staticcheck ./...` (if available)

**For complete best practices:** See [DESIGN.md Section 6: Best Practices](DESIGN.md#6-best-practices)

---

## 📋 Configuration Options Quick Reference

**For detailed configuration documentation:** See [DESIGN.md Section 4.3: Configuration Options](DESIGN.md#43-configuration-options)

Available via `submux.New(clusterClient, options...)`:

- `WithAutoResubscribe(bool)` - Enable automatic migration handling (default: `false`)
- `WithNodePreference(NodePreference)` - Node distribution strategy (default: `BalancedAll`)
  - `PreferMasters`, `BalancedAll`, `PreferReplicas`
- `WithTopologyPollInterval(time.Duration)` - Topology refresh rate (default: `1s`, min: `100ms`)
- `WithMinConnectionsPerNode(int)` - Minimum pool size per node (default: `1`)
- `WithMigrationTimeout(time.Duration)` - Max migration duration (default: `30s`)
- `WithMigrationStallCheck(time.Duration)` - Stall check interval (default: `2s`)
- `WithLogger(*slog.Logger)` - Custom logger (default: `slog.Default()`)
- `WithMeterProvider(metric.MeterProvider)` - OpenTelemetry metrics (default: `nil`)

---

## 📊 Subscription Types

**For detailed API documentation:** See [DESIGN.md Section 4: API Design](DESIGN.md#4-api-design)

1. **SUBSCRIBE** (`SubscribeSync`) - Exact channel matching
2. **PSUBSCRIBE** (`PSubscribeSync`) - Pattern matching with wildcards (`news:*`)
3. **SSUBSCRIBE** (`SSubscribeSync`) - Sharded pub/sub (Redis 7.0+ required)

All methods are **synchronous** - they block until Redis confirms the subscription.

---

## 🔔 Message and Event Types

**For complete type documentation:** See [DESIGN.md Section 4.1: Core Types](DESIGN.md#41-core-types)

### Message Types
- `MessageTypeMessage` - Regular SUBSCRIBE message
- `MessageTypePMessage` - Pattern PSUBSCRIBE message
- `MessageTypeSMessage` - Sharded SSUBSCRIBE message
- `MessageTypeSignal` - Topology change notification

### Event Types (Signals)
- `EventNodeFailure` - Connection lost
- `EventMigration` - Hashslot migration detected
- `EventMigrationStalled` - Migration stalled (>2s no progress)
- `EventMigrationTimeout` - Migration timeout (>30s)

**⚠️ Callbacks MUST handle `MessageTypeSignal` for production monitoring.**

---

## 🚫 Exported Errors

**For error handling patterns:** See [DESIGN.md Section 6.2: Error Handling](DESIGN.md#62-error-handling)

Sentinel errors defined in `errors.go`:
- `ErrInvalidClusterClient` - Nil or invalid cluster client provided to `New()`
- `ErrInvalidChannel` - Empty or invalid channel name
- `ErrSubscriptionFailed` - Subscription operation failed
- `ErrConnectionFailed` - Connection to Redis node failed
- `ErrClosed` - Operation attempted on closed SubMux

Use these with `errors.Is()` for error checking.

---

## 📈 OpenTelemetry Metrics

**For complete metrics documentation:** See [DESIGN.md Section 6.5: Observability](DESIGN.md#65-observability)

submux provides 21 production metrics (11 counters, 4 histograms, 2 gauges planned):
- Message throughput and latency
- Connection creation and failures
- Migration events and duration
- Callback invocations and panics
- Topology refresh operations

**Performance**: 0.1 ns/op (disabled), 150-210 ns/op (enabled)

Enable with `WithMeterProvider(provider)` configuration option.

---

## 🚨 Common Gotchas

**For detailed explanations:** See [DESIGN.md Section 6: Best Practices](DESIGN.md#6-best-practices)

1. **Auto-resubscribe disabled by default** - Must enable with `WithAutoResubscribe(true)`
2. **Context timeout = confirmation timeout** - Not message delivery timeout
3. **Multiple subscriptions to same channel** - Each callback is independent; unsubscribe only removes specific callback
4. **Signals sent even with auto-resubscribe** - Always monitor for observability
5. **SSUBSCRIBE requires Redis 7.0+** - Use `SubscribeSync`/`PSubscribeSync` for older versions
6. **Callbacks run in goroutines** - Must be thread-safe and non-blocking

---

## 🎯 Quick Decision Tree

**"Where do I find information about..."**

| Topic | Reference |
|-------|-----------|
| Architecture/Design patterns | [DESIGN.md Section 2](DESIGN.md#2-architecture) |
| Configuration options | [DESIGN.md Section 4.3](DESIGN.md#43-configuration-options) |
| API usage and types | [DESIGN.md Section 4](DESIGN.md#4-api-design) |
| Testing strategy | [DESIGN.md Section 5](DESIGN.md#5-testing-strategy) |
| Best practices | [DESIGN.md Section 6](DESIGN.md#6-best-practices) |
| Metrics/Observability | [DESIGN.md Section 6.5](DESIGN.md#65-observability) |
| Topology/Resilience | [DESIGN.md Section 3](DESIGN.md#3-resilience-and-topology-handling) |
| Development commands | This file (sections above) |
| User quick start | [README.md](README.md) |
| Pending work | [TODO.md](TODO.md) |

---

## 🎓 Getting Started as an Agent

### 1. First Steps
1. **Read [DESIGN.md](DESIGN.md) Sections 1-2** - Understand problem statement and architecture
2. **Review key files** - `submux.go`, `pool.go`, `eventloop.go`, `topology.go`
3. **Run tests** - `go test ./... -v -race` to verify environment setup

### 2. Before Making Changes
1. **Consult [DESIGN.md](DESIGN.md)** - Understand existing design patterns
2. **Check [TODO.md](TODO.md)** - See if related work is planned
3. **Review test coverage** - Check if similar tests exist in `integration/`

### 3. When Implementing
1. **Update [DESIGN.md](DESIGN.md) first** - Document design before coding
2. **Follow existing patterns** - Maintain consistency with current architecture
3. **Add tests** - Unit tests + integration tests if needed
4. **Verify with race detector** - `go test ./... -v -race`

### 4. When Stuck
1. **Check [DESIGN.md](DESIGN.md)** - Most questions answered there
2. **Read related test files** - Tests often demonstrate usage patterns
3. **Grep for examples** - `grep -r "SubscribeSync" .` to find usage examples

---

## 💡 Philosophy

**submux prioritizes:**
- **Correctness** over performance (but we optimize where it matters)
- **Simplicity** over cleverness (single event loop, clear state machines)
- **Observability** over opacity (signals, metrics, structured logging)
- **Resilience** over fragility (auto-resubscribe, panic recovery, graceful degradation)

**When in doubt:**
1. Consult [DESIGN.md](DESIGN.md)
2. Look at existing code for patterns
3. Write tests first
4. Keep it simple

---

## 📝 Final Reminder

**🎯 ALL ARCHITECTURAL AND DESIGN DECISIONS ARE DOCUMENTED IN [DESIGN.md](DESIGN.md)**

This file is a quick reference only. For any non-trivial question about architecture, design patterns, API behavior, resilience strategies, or best practices:

**→ Start with [DESIGN.md](DESIGN.md) ←**

When making changes that affect architecture or design:

**→ Update [DESIGN.md](DESIGN.md) FIRST ←**
