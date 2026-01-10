# Changelog

All notable changes to the submux project will be documented in this file.

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

