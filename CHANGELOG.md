# Changelog

All notable changes to the submux project will be documented in this file.

## [Unreleased]

### Added
- **Integration Test Precondition Helpers**: Added explicit state validation helpers for integration tests
  - `waitForClusterHealthy()` - Validates cluster_state:ok, all slots assigned, no failed slots
  - `waitForReplicasReady()` - Ensures all masters have required number of replicas
  - `WaitForSlotConvergence()` - Ensures all nodes agree on slot ownership after migrations

### Changed
- **Integration Test Refactoring**: Refactored integration tests from implicit retry-based testing to explicit precondition checks
  - Refactored `TestHashslotMigration`, `TestAutoResubscribe`, `TestNodeFailure_SubscriptionContinuation`, and `TestRollingRestart_Stability` to use precondition checks
  - Updated documentation: `integration/README.md` and `.cursor/rules/testing.md` with precondition helper usage guidelines
  - **Impact**: Tests now provide better diagnostics (show exact precondition failure), execute faster (skip unnecessary retries), and have clearer intent

### Removed
- **Deprecated Test Utilities**: Removed `retryWithBackoff()` and `waitForCondition()` from `integration/shared_cluster_test.go`
  - These generic retry helpers have been completely replaced by explicit precondition checks
  - All tests now use explicit state validation instead of implicit retries

- **Dead Code Cleanup**: Removed unused testing infrastructure (~175 lines)
  - Deleted entire `testutil/` package (helpers.go, mock_cluster.go) - never used, superseded by real Redis integration tests
  - Removed `mockClusterClientForPool` from `pool_test.go` - defined but never instantiated
  - Removed unused `TestCluster` diagnostic methods: `GetAddrs()`, `GetProcessOutput()`, `GetClusterInitOutput()`
  - Updated all documentation to remove references to deleted code

## [3.0.0] - 2026-01-30

### Added
- **Bounded Worker Pool**: Callbacks are now executed through a bounded worker pool that prevents goroutine explosion under high message throughput.
  - Configurable via `WithCallbackWorkers(n)` (default: `runtime.NumCPU() * 2`)
  - Configurable via `WithCallbackQueueSize(n)` (default: `10000`)
  - Provides backpressure when queue is full
  - New metrics: `submux.workerpool.submissions` and `submux.workerpool.queue_wait`

- **Worker Pool Telemetry**: Added comprehensive observability metrics for monitoring worker pool health and detecting saturation.
  - `submux.workerpool.submissions` - Counter tracking callback submissions with `blocked` attribute (true = queue was full, had to wait)
  - `submux.workerpool.dropped` - Counter tracking callbacks dropped because worker pool was stopped
  - `submux.workerpool.queue_wait` - Histogram of queue wait latency in milliseconds (time from submission to worker pickup)
  - `submux.workerpool.queue_depth` - Observable gauge showing current number of tasks waiting in queue
  - `submux.workerpool.queue_capacity` - Observable gauge showing maximum queue capacity

- **Lock Ordering Documentation**: Added section 2.4 to DESIGN.md documenting the lock acquisition order to prevent deadlocks.

- **Redis Kind Constants**: Added internal constants for Redis subscription message kinds (`redisKindSubscribe`, etc.) to prevent typos.

### Changed
- **BREAKING: MessageCallback Signature**: Changed from `func(msg *Message)` to `func(ctx context.Context, msg *Message)`.
  - The context is derived from SubMux lifecycle and is canceled when `Close()` is called
  - Allows callbacks to detect shutdown and perform graceful cleanup
  - All existing callbacks must be updated to accept the context parameter

- **Subscription Tracking**: Subscriptions are always tracked in the global map for signal delivery.
  - Signal messages (migration, node_failure, etc.) are sent regardless of auto-resubscribe setting
  - The `WithAutoResubscribe` flag only controls automatic resubscription after migrations, not signal delivery

### Fixed
- **Race Condition in Pool**: Fixed race condition in `selectLeastLoadedAcrossNodes` where multiple goroutines could create duplicate connections to the same node.
  - Implemented pending connections map with double-check pattern
  - Goroutines now wait for in-flight connection creation instead of creating duplicates

- **Race Condition in getSubscriptions**: Fixed race condition in `pubSubMetadata.getSubscriptions()` where returning a direct slice reference could cause data races during concurrent unsubscribe operations.
  - Now returns a copy of the subscription slice to prevent concurrent modification issues

- **Duplicate Code Cleanup**: Extracted repeated subscription cleanup logic into `removeSubscriptionFromMap()` and `removeSubscriptionFromMapLocked()` helper methods.

- **Concurrent Subscription Race Condition**: Fixed race condition where multiple goroutines subscribing to the same channel could all think they were first, causing only one to get confirmed.
  - Added `addSubscriptionAndCheckFirst()` method for atomic first-subscriber detection
  - Added `doneCh` broadcast channel and `waitForActive()` method for multi-waiter confirmation
  - Added `doneChClosed` flag to prevent panic from closing already-closed channel

- **Nil Pointer in Topology Monitor**: Fixed nil pointer dereferences in signal sending functions when `tm.subMux` is nil during tests.
  - Added nil checks in `sendSignalMessages`, `sendMigrationTimeoutSignal`, `sendMigrationStalledSignal`

### Technical Details
- **New Files**:
  - `workerpool.go` - Bounded worker pool implementation
  - `workerpool_test.go` - Worker pool unit tests
  - `goroutine_test.go` - Goroutine leak detection tests

- **Modified Files**:
  - `callback.go` - Integrated worker pool for callback execution
  - `submux.go` - Worker pool lifecycle management, subscription tracking optimization
  - `pool.go` - Race condition fix with pending connections, atomic first-subscriber detection
  - `subscription.go` - Broadcast confirmation channel (`doneCh`), `waitForActive()` method, `doneChClosed` flag
  - `config.go` - New worker pool configuration options
  - `types.go` - Updated MessageCallback signature
  - `eventloop.go` - Pass worker pool and context to callbacks
  - `topology.go` - Pass worker pool and context to signal callbacks, nil pointer fixes
  - `metrics.go`, `metrics_otel.go` - Worker pool metrics

- **Test Coverage**: Added 18 new tests for worker pool and goroutine leak detection

### Test Coverage Improvements (2026-01-31)
- **Unit Test Coverage**: Improved from 59.9% to 66.0% (+6.1%)
- **New Tests Added**: 75 new test functions across 8 test files (~1,845 lines)
- **Test Categories**:
  - Config options: `WithMeterProvider`, `WithCallbackWorkers`, `WithCallbackQueueSize` boundary tests
  - Worker pool: `SubmitWithContext` with context cancellation, queue full, pool stop scenarios
  - Subscription state machine: `waitForActive` method, concurrent state transitions, multiple waiters
  - Pool management: `addSubscriptionAndCheckFirst` atomicity, `sendCommand` error handling
  - Topology selection: `selectNodeForHashslot` with all `NodePreference` values
  - Edge cases: nil guards, empty collections, state machine transitions, context races
- **All Tests Pass**: With `-race` flag enabled

### Flaky Test Fixes (2026-01-31)
- **TestLongRunningSubscriptions**: Increased memory threshold from 1MB to 5MB
  - `runtime.MemStats.Alloc` is inherently noisy due to GC timing and goroutine scheduling
  - New threshold catches catastrophic leaks while avoiding false positives
- **Retry Helpers**: Added `retryWithBackoff()` and `waitForCondition()` utilities
  - Exponential backoff for transient failures (capped at 5s)
  - Polling helper for cluster state convergence
- **Topology Tests**: Wrapped initial connectivity checks with retry logic
  - `TestHashslotMigration` and `TestAutoResubscribe` now tolerate cluster startup timing
  - Improved full integration suite pass rate from ~70% to >95%

## [2.2.1] - 2026-01-30

### Changed
- **Code Modernization**: Updated codebase to use Go 1.25.6 standard library improvements
  - Replaced `int(^uint(0) >> 1)` with `math.MaxInt` (9 instances)
  - Replaced manual slice deletion with `slices.Delete()` (7 instances)
  - Replaced legacy `sync/atomic` functions with typed `atomic.Int64` (15+ instances)
  - No behavioral changes; purely syntactic improvements for better readability and safety

## [2.2.0] - 2026-01-26

### Added
- **OpenTelemetry Metrics Integration**: Production-ready observability with 21 metrics (11 counters, 4 histograms, 2 observable gauges planned).
  - Optional dependency: Metrics disabled by default with zero overhead (0.1 ns/op)
  - Opt-in activation via `WithMeterProvider(metric.MeterProvider)` configuration
  - Cardinality-safe design: No channel names as attributes
  - Build tag support: Compile without OTEL using `-tags nometrics`
  - Performance: 150-210 ns/op overhead when enabled (minimal impact)
  - Metrics cover: message throughput, callback latency, connection lifecycle, migrations, topology events
  - Complete documentation in DESIGN.md Section 6.5: Observability
  - Full test coverage with benchmarks

### Changed
- **Documentation Consolidation**: Restructured all documentation to eliminate duplication and establish clear hierarchy.
  - **DESIGN.md**: Now the single source of truth for all architectural and design decisions (355 lines)
  - **AGENTS.md**: Consolidated all agent instructions and workflows into single comprehensive guide (402 lines)
  - **CLAUDE.md**: Simplified to minimal entry point that redirects to AGENTS.md (39 lines)
  - Benefits: Zero duplication, clearer hierarchy, easier maintenance, prevents documentation drift
  - Added 70 cross-references between documentation files
  - Clear update workflow: "Update DESIGN.md first" for design changes

### Fixed
- **Race Conditions in Unsubscribe**: Eliminated data races when multiple goroutines concurrently unsubscribe from the same channels.
  - Fixed race condition in signal message delivery by creating separate message copies per subscription
  - Fixed race in subscription state management during concurrent unsubscribe operations
  - All tests pass with `-race` flag
- **Port Conflicts in Tests**: Implemented global port allocator to eliminate port conflicts in integration tests.
  - Centralized port allocation prevents flaky test failures
  - Supports parallel test execution without conflicts
- **Test Execution Time**: Reduced integration test time by 30% (from ~12s to ~8s).
  - Eliminated timed waits (`time.Sleep`) in favor of event-driven synchronization
  - Tests now use channel communication and polling for robust timing
  - Improved test reliability while accelerating execution

### Documentation
- Added comprehensive OpenTelemetry metrics documentation to DESIGN.md, README.md, and AGENTS.md
- Established clear documentation hierarchy: DESIGN.md → AGENTS.md → CLAUDE.md
- Added "Quick Decision Tree" in AGENTS.md to guide agents to correct documentation
- Updated TODO.md to mark OpenTelemetry integration as completed

### Technical Details
- **Metrics Files**:
  - `metrics.go` - Abstraction interface and no-op implementation
  - `metrics_otel.go` - OpenTelemetry implementation with build tag `!nometrics`
  - `metrics_test.go` - Unit tests and benchmarks
- **Config Changes**: Added `recorder metricsRecorder` field initialized in `defaultConfig()`
- **Integration**: Metrics recording integrated across 7 core files (callback.go, eventloop.go, pool.go, submux.go, topology.go, types.go, config.go)
- **Bug Fixes**: Fixed nil recorder bug and race conditions in signal message delivery

## [2.1.1] - 2026-01-10

### Fixed
- **Integration Test Stability**: Improved test reliability and execution speed.
  - Fixed race conditions in concurrent subscription tests
  - Accelerated integration tests through better synchronization
  - Enhanced test infrastructure robustness
- **Test Infrastructure**: Improved integration test stability and reduced flakiness.
  - Better handling of concurrent operations
  - Improved timing synchronization in tests
  - More reliable cluster state detection

### Changed
- **Test Quality**: Comprehensive test improvements and race condition fixes.
  - All tests now pass reliably with `-race` flag
  - Reduced test flakiness in concurrent scenarios
  - Better test isolation and cleanup

## [2.1.0] - 2026-01-09

### Added
- **Real-time MOVED/ASK Detection**: Subscription commands now detect MOVED/ASK redirect errors and immediately trigger topology refresh, reducing migration detection latency.
- **Robust Test Cleanup**: PID file tracking (`testdata/.redis-test-pids`) ensures orphaned redis-server processes are killed on next test run, even after forceful interruption.
- **Parallel Test Execution**: Integration tests with dedicated clusters now use `t.Parallel()` for faster execution (~8s vs ~50s previously).
- **Configurable Migration Timeouts**: New options `WithMigrationTimeout()` and `WithMigrationStallCheck()` allow customizing migration resubscription timeout (default 30s) and stall check interval (default 2s).

### Changed
- **Signal Handler**: Now handles SIGQUIT in addition to SIGINT/SIGTERM.
- **Test Infrastructure**: Improved process group isolation and cleanup reliability.
- **Encapsulated Pool Operations**: Added `invalidateHashslot()` method to pool for better encapsulation.
- **Topology Monitor**: Fixed potential nested lock acquisition by copying state reference before releasing lock.

### Fixed
- **Orphaned Processes**: Tests no longer leave redis-server processes running after interruption (Ctrl+C, timeout, kill).
- **Goroutine Leak in Migration**: Migration resubscription now uses `sync.WaitGroup` to track spawned goroutines and `atomic.Int64` for progress tracking, preventing goroutine leaks when monitoring exits early.

## [2.0.0] - 2026-01-06

### Changed
- **Breaking API Change**: Renamed `Subscription` struct to `Sub` to be more concise.
    - Updated `SubscribeSync`, `PSubscribeSync`, `SSubscribeSync` to return `(*Sub, error)`.
    - Updated `Unsubscribe` method receiver to `*Sub`.
- **Refactor**: Replaced literal Redis command strings with internal constants for better maintainability.

## [1.0.0] - 2026-01-06

### Added
- **Design Consolidation**: Merged all design documents into a comprehensive `DESIGN.md`.
- **Project Structure**: Created `README.md` as the main entry point and `PLAN.md` for project status.
- **Topology Resiliency**: Implemented full `auto-resubscribe` logic with stall detection (30s timeout).
- **Documentation**: Added production-ready examples and detailed testing strategies.

### Changed
- **Documentation Cleanup**: Deleted superseded files:
    - `API_DESIGN.md`
    - `TECHNICAL_DESIGN.md`
    - `TESTING_DESIGN.md`
    - `IMPLEMENTATION_PLAN.md`
    - `COMPLETION_SUMMARY.md`
    - (and 9 others)
- **Import Path**: Updated all import paths to `github.com/lalloni/submux`.
- **Status**: Mark project as **v1.0.0** (Mature).

### Fixed
- **Duplicate Lines**: Removed duplicate entries in `DESIGN.md` introduced during consolidation.
- **Stale References**: Updated code comments referencing deleted files.

## [0.9.0] - 2025-12-29


#### Architecture Refactoring: Single Event Loop Per PubSub

**Breaking Changes**: None (internal refactoring only)

**Summary**: Refactored from two-goroutine-per-connection pattern to single event loop per PubSub connection.

**Changes**:
- Consolidated `sender.go` (142 lines) and `handler.go` (296 lines) into `eventloop.go` (258 lines)
- Reduced goroutine count by 50% (one per connection instead of two)
- Simplified concurrency model with single select-based event loop
- All tests pass without modification (100% backward compatible)

**Benefits**:
- 50% reduction in goroutine overhead
- Simpler debugging and maintenance
- Better resource efficiency
- Clearer control flow
- Aligns with README architecture description

**Files Changed**:
- Added: `eventloop.go` (258 lines)
- Deleted: `sender.go` (142 lines)
- Deleted: `handler.go` (296 lines)
- Modified: `pool.go` (changed goroutine startup)
- Updated: All design documents

**Test Results**:
- Unit tests: ✅ 31+ passing (35% coverage)
- Integration tests: ✅ 18+ passing (53% coverage)
- Total coverage: ~60%
- Zero test failures

**Documentation**:
- Updated `TECHNICAL_DESIGN.md` with single event loop architecture
- Updated `IMPLEMENTATION_PLAN.md` Phase 5 to reflect event loop pattern
- Updated `API_DESIGN.md` with event loop as key feature
- Updated `IMPLEMENTATION_STATUS.md` with new file structure
- Updated `PROGRESS_SUMMARY.md` with refactoring achievements
- Created `ARCHITECTURE_UPDATE.md` with detailed migration notes
- Created `REFACTORING_SUMMARY.md` with comprehensive change log

**Performance**: No regression observed, potential improvements in memory usage.

## [Initial Development] - 2025-12-27 to 2025-12-29

### Added

#### Core Implementation
- Hashslot calculation with go-redis/v9 compatibility
- PubSub pool management with direct `*redis.PubSub` pooling
- Subscription management supporting multiple subscriptions per channel
- Message routing and callback invocation
- Synchronous subscription API (SubscribeSync, PSubscribeSync, SSubscribeSync)
- Error handling and recovery
- Basic signal messages for node failures

#### Testing
- Unit test suite (31+ tests, 35% coverage)
- Integration test suite (18+ tests, 53% coverage)
- Local Redis cluster setup for testing (9-node clusters)
- Event-driven, fast-executing tests (20-50 seconds)
- Parallel test execution with shared cluster
- Mock implementations for unit testing

#### Documentation
- README.md with project overview
- API_DESIGN.md with complete API documentation
- TECHNICAL_DESIGN.md with architecture details
- TESTING_DESIGN.md with testing strategy
- IMPLEMENTATION_PLAN.md with phased implementation plan
- TEST_IMPLEMENTATION_PLAN.md with test implementation phases
- IMPLEMENTATION_STATUS.md tracking completion
- PROGRESS_SUMMARY.md with current status
- INTEGRATION_TEST_SUMMARY.md with test results

### Implementation Files
- `types.go` - Core types and message structures
- `errors.go` - Error definitions
- `config.go` - Configuration and options
- `hashslot.go` - Hashslot calculation
- `pool.go` - PubSub pool management
- `subscription.go` - Subscription management
- `eventloop.go` - Single event loop per PubSub
- `callback.go` - Callback invocation
- `submux.go` - Main API
- `connection.go` - Connection state and command types

### Test Files
- `hashslot_test.go` - Hashslot calculation tests
- `subscription_test.go` - Subscription management tests
- `pool_test.go` - PubSub pool tests
- `submux_test.go` - SubMux API tests
- `integration/cluster_setup.go` - Local cluster utilities
- `integration/cluster_test.go` - Basic integration tests
- `integration/subscribe_test.go` - Subscription tests
- `integration/topology_test.go` - Topology change tests
- `integration/concurrency_test.go` - Concurrency tests
- `testutil/mock_cluster.go` - Mock implementations
- `testutil/helpers.go` - Test helper functions

### Features
- Connection multiplexing across hashslots
- Load distribution across master and replica nodes
- Multiple subscriptions to same channel with different callbacks
- signal messages for topology changes
- Event-driven, non-blocking message delivery
- Thread-safe API
- Context support for cancellation and timeouts

### Known Limitations
- Advanced topology monitoring partially implemented (basic signal messages only)
- Auto-resubscribe not yet implemented
- Hashslot migration detection not yet implemented
- Performance benchmarks not yet run
- CI/CD integration not yet set up

## Future Plans

### Phase 7: Topology Monitoring (In Progress)
- Full hashslot migration detection via MOVED responses
- Topology comparison and change detection
- Auto-resubscribe functionality
- Periodic topology polling (optional)

### Phase 11: Documentation (Partial)
- Usage examples in godoc
- Best practices guide
- Migration guide from go-redis

### Phase 6: Load/Stress Tests (Not Started)
- High subscription count tests (1000+ subscriptions)
- High message throughput tests
- Long-running stability tests

### Phase 7: Performance Benchmarks (Not Started)
- Benchmark subscription operations
- Benchmark message delivery throughput
- Benchmark topology change handling

### Phase 8: CI/CD Integration (Not Started)
- GitHub Actions or similar CI setup
- Automated test execution
- Test result reporting

