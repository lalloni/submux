# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

**submux** is a Go library that provides intelligent connection multiplexing for Redis Cluster Pub/Sub operations. It significantly reduces connection overhead by reusing connections based on hashslot routing, while maintaining topology awareness and resilience during cluster reconfigurations.

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

## Architecture

### Core Components

1. **SubMux** (`submux.go`): Main entry point wrapping `*redis.ClusterClient`. Routes subscriptions to appropriate connections and manages the subscription lifecycle. Provides three subscription methods:
   - `SubscribeSync`: Regular SUBSCRIBE for exact channel matches
   - `PSubscribeSync`: Pattern-based PSUBSCRIBE using glob-style patterns (`news:*`)
   - `SSubscribeSync`: Sharded SSUBSCRIBE (Redis 7.0+) for cluster-aware pub/sub

2. **PubSub Pool** (`pool.go`): Manages physical `*redis.PubSub` connections indexed by hashslot and node address. Implements connection reuse logic and load balancing.

3. **Event Loop** (`eventloop.go`): Each physical connection has a single goroutine (`runEventLoop`) that handles both:
   - Sending commands to Redis (SUBSCRIBE/UNSUBSCRIBE/PSUBSCRIBE/SSUBSCRIBE)
   - Receiving messages and subscription confirmations from Redis

   This single-goroutine-per-connection design eliminates synchronization complexity.

4. **Topology Monitor** (`topology.go`): Background goroutine that polls cluster state, detects hashslot migrations, and coordinates auto-resubscribe operations. Uses hardcoded timeouts: 2s stall check, 30s migration timeout.

5. **Subscription** (`subscription.go`): Internal state machine tracking individual subscription lifecycle (Pending → Active → Failed/Closed).

### Key Design Patterns

**Hashslot-Based Routing**: All channels are mapped to hashslots using `CRC16(channel) % 16384`. Subscriptions for the same hashslot reuse the same connection. Supports Redis hashtag syntax (`{tag}`).

**Connection Multiplexing**: Multiple callbacks can subscribe to the same channel on the same connection. The pool tracks active subscriptions per connection and only sends actual Redis commands when the first subscription to a channel occurs.

**Signal Messages**: When topology changes are detected (migration, node failure), the system sends `MessageTypeSignal` to callbacks, allowing applications to react to cluster events even when auto-resubscribe is enabled.

**Auto-Resubscribe**: When enabled (`WithAutoResubscribe(true)`), the system automatically handles hashslot migrations by:
1. Detecting migration via topology diff
2. Sending signal to callbacks
3. Unsubscribing from old node
4. Resubscribing to new node
5. Monitoring for stalls (2s) and timeouts (30s)

**Important:** Auto-resubscribe defaults to `false`. Must be explicitly enabled for automatic migration handling.

**Node Distribution Strategy**: Subscriptions can be distributed across cluster nodes using three strategies:
- **BalancedAll** (default): Distributes subscriptions equally across all nodes (masters + replicas) for optimal resource utilization
- **PreferMasters**: Routes all subscriptions to master nodes only
- **PreferReplicas**: Prefers replica nodes to protect write-saturated masters, falls back to masters if no replicas available

The system always selects the least-loaded node within the chosen strategy.

### Configuration Options

Available via `submux.New(clusterClient, options...)`:

- `WithAutoResubscribe(bool)`: Enable automatic migration handling (default: `false`)
- `WithNodePreference(NodePreference)`: Set node distribution strategy (default: `BalancedAll`)
  - `PreferMasters`: All subscriptions to master nodes
  - `BalancedAll`: Equal distribution across all nodes (recommended)
  - `PreferReplicas`: Prefer replicas to protect write-heavy masters
- `WithReplicaPreference(bool)`: **Deprecated** - Use `WithNodePreference()` instead
- `WithTopologyPollInterval(time.Duration)`: Cluster topology refresh rate (default: `1s`, min: `100ms`)
- `WithMinConnectionsPerNode(int)`: Minimum connection pool size per node (default: `1`)
- `WithLogger(*slog.Logger)`: Custom structured logger (default: `slog.Default()`)

### Concurrency Model

- All public APIs are thread-safe
- Each PubSub connection has its own event loop goroutine
- The topology monitor runs in a separate background goroutine
- Callbacks are invoked asynchronously in separate goroutines (one per message)
- Callbacks are wrapped with panic recovery (`callback.go`) - panics are logged but don't crash the system
- Internal state is protected by mutexes (`sync.RWMutex` for reads, `sync.Mutex` for writes)

### Testing Architecture

**Unit Tests**: Use `testutil/mock_cluster.go` to mock `ClusterClient` for isolated testing of hashslot calculation, pool management, and subscription state machines.

**Integration Tests**: Located in `integration/`. The test harness (`cluster_setup.go`) manages real Redis clusters:
- Finds available ports dynamically
- Spawns redis-server processes with process group isolation
- Configures cluster topology via redis-cli
- Tears down cleanly after tests via signal handlers and `t.Cleanup()`
- Test data written to `integration/testdata/` (auto-deleted)

**Robust Process Cleanup**: The test infrastructure includes PID file tracking (`testdata/.redis-test-pids`) to ensure no orphaned redis-server processes survive interrupted test runs:
- PIDs are recorded when processes start
- On test startup, orphaned processes from previous runs are detected and killed
- Signal handlers (SIGINT, SIGTERM, SIGQUIT) trigger cleanup before exit
- Works even when tests are forcefully interrupted (Ctrl+C, timeout)

**Key Integration Test Files**:
- `cluster_test.go`: Basic Pub/Sub functionality
- `subscribe_test.go`: PSUBSCRIBE, SSUBSCRIBE, multiple callbacks
- `topology_test.go`: Migration detection, signal messages, auto-resubscribe
- `resiliency_test.go`: Replica failures, rolling restarts, chaos testing
- `concurrency_test.go`: Race conditions, concurrent subscriptions
- `load_test.go`: High throughput, memory usage

## Important Implementation Details

### Exported Errors

The package exports the following sentinel errors (`errors.go`):

- `ErrInvalidClusterClient`: Returned when nil or invalid cluster client is provided to `New()`
- `ErrInvalidChannel`: Returned when channel name is empty or invalid
- `ErrSubscriptionFailed`: Returned when subscription operation fails
- `ErrConnectionFailed`: Returned when connection to Redis node fails
- `ErrClosed`: Returned when operation is attempted on a closed SubMux

### Subscription Types

submux supports three Redis pub/sub mechanisms:

1. **SUBSCRIBE** (`SubscribeSync`): Exact channel name matching
   - Use for: Known, specific channel names
   - Example: `["orders", "payments"]`
   - Message type: `MessageTypeMessage`

2. **PSUBSCRIBE** (`PSubscribeSync`): Glob-style pattern matching
   - Use for: Subscribing to multiple channels with a pattern
   - Example: `["news:*", "logs:error:*"]`
   - Supports `?`, `*`, and `[abc]` wildcards
   - Message type: `MessageTypePMessage`
   - Message includes both `Channel` (actual) and `Pattern` (matched) fields

3. **SSUBSCRIBE** (`SSubscribeSync`): Sharded pub/sub (Redis 7.0+)
   - Use for: Cluster-aware pub/sub with guaranteed node affinity
   - Channels are explicitly sharded to specific cluster nodes
   - Lower overhead than regular pub/sub in cluster mode
   - Message type: `MessageTypeSMessage`
   - **Note:** Requires Redis 7.0+ server

### Message Types

The `Message` struct has a `Type` field that distinguishes:
- `MessageTypeMessage`: Regular SUBSCRIBE message
- `MessageTypePMessage`: Pattern PSUBSCRIBE message
- `MessageTypeSMessage`: Sharded SSUBSCRIBE message (Redis 7.0+)
- `MessageTypeSignal`: Topology change notification

**Callbacks MUST handle all message types**, especially `MessageTypeSignal` for production monitoring.

### Event Types (Signals)

When `msg.Type == MessageTypeSignal`, the `msg.Signal.EventType` field contains:

- `EventNodeFailure`: Connection to Redis node was lost
- `EventMigration`: Hashslot migration detected, resubscription initiated (if auto-resubscribe enabled)
- `EventMigrationStalled`: Migration resubscription made no progress for 2+ seconds
- `EventMigrationTimeout`: Migration resubscription did not complete within 30 seconds

Applications should monitor these events for observability and alerting.

### Subscription Lifecycle

All subscription methods (`SubscribeSync`, `PSubscribeSync`, `SSubscribeSync`) are synchronous - they wait for Redis confirmation before returning:

1. Method call creates internal `subscription` objects (one per channel/pattern)
2. Each subscription starts in `subStatePending`
3. Command is sent via `cmdCh` to the event loop
4. Event loop sends SUBSCRIBE/PSUBSCRIBE/SSUBSCRIBE to Redis
5. Redis confirms with a subscription confirmation message
6. Subscription transitions to `subStateActive`
7. Method returns `(*Sub, error)` after confirmation or context timeout

**Context Usage**: The `context.Context` parameter controls the subscription confirmation timeout. If the context expires before Redis confirms, the method returns `context.DeadlineExceeded` or `context.Canceled`.

### Sub and Unsubscribe

The `Sub` type represents a subscription handle returned by all subscription methods. It tracks multiple internal subscriptions (one per channel/pattern in the request).

**`Sub.Unsubscribe(ctx context.Context) error`**: Unsubscribes ALL channels/patterns associated with this specific callback. This method:
- Sends UNSUBSCRIBE commands for all channels
- Removes the callback from the connection's subscription list
- Does NOT close other callbacks subscribed to the same channels
- Is idempotent - safe to call multiple times

**Important:** Always defer cleanup: `defer sub.Unsubscribe(context.Background())`

### Error Handling

- Connection failures mark the connection state as `connStateFailed`
- All subscriptions on a failed connection receive `node_failure` signals
- The pool invalidates cached connections for the affected node
- Subsequent operations create new connections
- If auto-resubscribe is disabled, the application must handle resubscription manually

### Hashslot Migration Detection

submux uses two complementary mechanisms to detect topology changes:

**1. Periodic Polling** (default 1s, configurable via `WithTopologyPollInterval`):
1. Calls `ClusterClient.ReloadState()` to refresh cluster state
2. Calls `ClusterSlots()` to get current topology
3. Diffs against previous topology to detect:
   - Hashslot ownership changes (migrations)
   - Node additions/removals
4. For each migrated hashslot, invalidates pool cache and triggers auto-resubscribe if enabled

**2. Real-time MOVED/ASK Detection** (`eventloop.go`):
When a subscription command fails with a MOVED or ASK error:
1. `checkAndHandleRedirect()` detects the error using `redis.IsMovedError()` / `redis.IsAskError()`
2. Immediately triggers `topologyMonitor.triggerRefresh()` for faster topology update
3. This ensures migrations are detected without waiting for the next poll interval

The combination provides both reliable periodic updates and fast reaction to actual redirects

## Code Conventions

- Use `sync.RWMutex` for topology state (many readers, few writers)
- Use `sync.Mutex` for connection pool and subscription state
- Always defer cleanup: `defer sub.Unsubscribe(ctx)` and `defer subMux.Close()`
- Callbacks should be fast; offload heavy work to queues
- Integration tests with dedicated clusters use `t.Parallel()` for faster execution; tests using the shared cluster should be careful with concurrent modifications
- When modifying event loop, ensure commands and messages are handled atomically to avoid race conditions

## Common Gotchas

1. **Auto-resubscribe is disabled by default**: Must explicitly enable with `WithAutoResubscribe(true)` for automatic migration handling.

2. **Context timeouts apply to subscription confirmation**: A 1-second context timeout on `SubscribeSync` means Redis must confirm within 1 second, not that messages must arrive within 1 second.

3. **Multiple subscriptions to same channel**: Each callback gets its own subscription. Use `Sub.Unsubscribe()` to remove only your callback, not all callbacks for that channel.

4. **Signal messages for all topology events**: Even with auto-resubscribe enabled, callbacks still receive signal messages. Applications should handle these for monitoring/alerting.

5. **SSUBSCRIBE requires Redis 7.0+**: Attempting to use `SSubscribeSync` on older Redis versions will fail. Use `SubscribeSync` or `PSubscribeSync` instead.

6. **Callbacks run in separate goroutines**: Each message invokes the callback in a new goroutine. Callbacks must be thread-safe and should not block.
