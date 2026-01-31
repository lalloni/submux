# submux

**submux** is a smart Pub/Sub multiplexer for Redis Cluster in Go. It significantly reduces the number of connections required for high-volume Pub/Sub applications by multiplexing multiple subscriptions over a small pool of dedicated connections.

## Features

-   **Smart Multiplexing**: Automatically routes subscriptions to the correct connection based on hashslots.
-   **Topology Aware**: Monitors Redis Cluster topology changes and handles hashslot migrations automatically.
-   **Resilient**: Background event loop manages connection health and auto-reconnects.
-   **Scalable**: Distributes read load across replicas (optional).
-   **Production Ready**: Optional OpenTelemetry metrics for observability (zero overhead when disabled).
-   **Drop-in Ready**: Built on top of the standard `go-redis/v9` library.

## Installation

```bash
go get github.com/lalloni/submux
```

## Requirements

- **Go**: 1.25.6 or later
- **Redis**: 6.0+ (Redis 7.0+ required for sharded pub/sub via `SSubscribeSync`)
- **go-redis**: v9

## Quick Start

```go
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/redis/go-redis/v9"
	"github.com/lalloni/submux"
)

func main() {
	// 1. Initialize go-redis cluster client
	rdb := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs: []string{"localhost:7000", "localhost:7001"},
	})
	defer rdb.Close()

	// 2. Create SubMux instance with auto-resubscribe enabled
	sm, err := submux.New(rdb,
		submux.WithAutoResubscribe(true),         // Handle migrations automatically
		submux.WithNodePreference(submux.BalancedAll), // Distribute across all nodes
	)
	if err != nil {
		log.Fatalf("Failed to create SubMux: %v", err)
	}
	defer sm.Close()

	// 3. Subscribe to channels
	ctx := context.Background()
	sub, err := sm.SubscribeSync(ctx, []string{"my-channel"}, func(ctx context.Context, msg *submux.Message) {
		// Handle different message types
		switch msg.Type {
		case submux.MessageTypeMessage:
			fmt.Printf("Message on %s: %s\n", msg.Channel, msg.Payload)
		case submux.MessageTypeSignal:
			// Topology change notification (migration, node failure, etc.)
			log.Printf("Signal: %s - %s", msg.Signal.EventType, msg.Signal.Details)
		}
	})
	if err != nil {
		log.Fatalf("Failed to subscribe: %v", err)
	}

	// 4. Cleanup when done
	defer sub.Unsubscribe(ctx)

	// Keep alive...
	select {}
}
```

## Subscription Types

submux supports all three Redis pub/sub mechanisms:

### 1. Regular Subscribe (SUBSCRIBE)

Subscribe to specific channels by exact name:

```go
sub, err := sm.SubscribeSync(ctx, []string{"orders", "payments"}, func(ctx context.Context, msg *submux.Message) {
	fmt.Printf("Channel %s: %s\n", msg.Channel, msg.Payload)
})
```

### 2. Pattern Subscribe (PSUBSCRIBE)

Subscribe using glob-style patterns:

```go
sub, err := sm.PSubscribeSync(ctx, []string{"news:*", "logs:error:*"}, func(ctx context.Context, msg *submux.Message) {
	// msg.Pattern contains the matched pattern
	// msg.Channel contains the actual channel name
	fmt.Printf("Pattern %s matched channel %s: %s\n", msg.Pattern, msg.Channel, msg.Payload)
})
```

Supported wildcards: `?` (single char), `*` (multiple chars), `[abc]` (character set)

### 3. Sharded Subscribe (SSUBSCRIBE) - Redis 7.0+

Cluster-aware pub/sub with guaranteed node affinity and lower overhead:

```go
sub, err := sm.SSubscribeSync(ctx, []string{"events", "notifications"}, func(ctx context.Context, msg *submux.Message) {
	fmt.Printf("Sharded channel %s: %s\n", msg.Channel, msg.Payload)
})
```

**Note:** Requires Redis 7.0 or later. Falls back to regular `SubscribeSync` on older versions.

## Configuration Options

All options are configured via `submux.New()`:

```go
sm, err := submux.New(rdb,
	submux.WithAutoResubscribe(true),                    // Enable automatic migration handling
	submux.WithNodePreference(submux.BalancedAll),       // Node distribution strategy
	submux.WithTopologyPollInterval(2 * time.Second),    // Topology refresh rate
	submux.WithMigrationTimeout(30 * time.Second),       // Max time for migration resubscription
	submux.WithMigrationStallCheck(2 * time.Second),     // Stall detection interval
	submux.WithMinConnectionsPerNode(2),                 // Connection pool size per node
	submux.WithLogger(slog.New(slog.NewJSONHandler(os.Stdout, nil))), // Custom logger
	submux.WithMeterProvider(meterProvider),             // OpenTelemetry metrics (optional)
)
```

### Available Options

| Option | Default | Description |
|--------|---------|-------------|
| `WithAutoResubscribe(bool)` | `false` | Enable automatic resubscription during hashslot migrations |
| `WithNodePreference(NodePreference)` | `BalancedAll` | Node distribution strategy (see below) |
| `WithTopologyPollInterval(duration)` | `1s` | How often to poll cluster topology (min: 100ms) |
| `WithMigrationTimeout(duration)` | `30s` | Max time to wait for migration resubscription (min: 1s) |
| `WithMigrationStallCheck(duration)` | `2s` | How often to check for stalled migrations (min: 100ms) |
| `WithMinConnectionsPerNode(int)` | `1` | Minimum connection pool size per node |
| `WithCallbackWorkers(int)` | `runtime.NumCPU() * 2` | Number of worker goroutines for callback execution |
| `WithCallbackQueueSize(int)` | `10000` | Maximum pending callbacks in worker pool queue |
| `WithLogger(*slog.Logger)` | `slog.Default()` | Custom structured logger |
| `WithMeterProvider(metric.MeterProvider)` | `nil` | OpenTelemetry metrics provider (opt-in) |

### Node Distribution Strategies

Control how subscriptions are distributed across cluster nodes:

```go
// BalancedAll (default, recommended): Distribute equally across ALL nodes (masters + replicas)
// - Best resource utilization
// - Optimal for clusters with many nodes
sm, _ := submux.New(rdb, submux.WithNodePreference(submux.BalancedAll))

// PreferMasters: Route all subscriptions to master nodes only
// - Legacy behavior
// - Use if replicas are unreliable
sm, _ := submux.New(rdb, submux.WithNodePreference(submux.PreferMasters))

// PreferReplicas: Prefer replica nodes to protect write-saturated masters
// - Offloads read operations from masters
// - Falls back to masters if no replicas available
sm, _ := submux.New(rdb, submux.WithNodePreference(submux.PreferReplicas))
```

## Handling Signal Messages

**Critical for production:** Always handle `MessageTypeSignal` in callbacks to monitor topology events:

```go
sub, _ := sm.SubscribeSync(ctx, []string{"orders"}, func(ctx context.Context, msg *submux.Message) {
	switch msg.Type {
	case submux.MessageTypeMessage:
		// Normal pub/sub message
		processOrder(msg.Payload)

	case submux.MessageTypeSignal:
		// Topology change notification
		switch msg.Signal.EventType {
		case submux.EventMigration:
			// Hashslot migration detected (auto-resubscribe initiated if enabled)
			log.Printf("Migration: hashslot %d from %s to %s",
				msg.Signal.Hashslot, msg.Signal.OldNode, msg.Signal.NewNode)
			metrics.RecordMigration()

		case submux.EventNodeFailure:
			// Connection to Redis node lost
			log.Printf("Node failure: %s - %s", msg.Signal.OldNode, msg.Signal.Details)
			alerts.SendAlert("Redis node failure detected")

		case submux.EventMigrationStalled:
			// Migration resubscription made no progress for 2+ seconds
			log.Printf("Migration stalled: %s", msg.Signal.Details)
			alerts.SendWarning("Migration stalled")

		case submux.EventMigrationTimeout:
			// Migration resubscription exceeded 30 seconds
			log.Printf("Migration timeout: %s", msg.Signal.Details)
			alerts.SendCritical("Migration timeout - manual intervention may be required")
		}
	}
})
```

**Signal messages are sent even when auto-resubscribe is enabled** - use them for monitoring, alerting, and metrics collection.

## Error Handling

### Subscription Errors

```go
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()

sub, err := sm.SubscribeSync(ctx, []string{"my-channel"}, callback)
if err != nil {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		// Redis didn't confirm subscription within timeout
		log.Printf("Subscription timeout - cluster may be slow or unhealthy")

	case errors.Is(err, submux.ErrInvalidChannel):
		// Channel name is empty or invalid
		log.Printf("Invalid channel name")

	case errors.Is(err, submux.ErrSubscriptionFailed):
		// Redis returned an error during subscription
		log.Printf("Subscription failed: %v", err)

	case errors.Is(err, submux.ErrConnectionFailed):
		// Failed to connect to Redis node
		log.Printf("Connection failed: %v", err)

	default:
		log.Printf("Unexpected error: %v", err)
	}
	return
}
defer sub.Unsubscribe(context.Background())
```

### Unsubscribe Errors

```go
// Always use a fresh context for unsubscribe (don't reuse expired contexts)
unsubCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()

if err := sub.Unsubscribe(unsubCtx); err != nil {
	// Unsubscribe errors are usually safe to ignore
	// The connection will be cleaned up automatically
	log.Printf("Unsubscribe warning: %v", err)
}
```

### Callback Panics

Callbacks are automatically wrapped with panic recovery. Panics are logged but don't crash the application:

```go
sub, _ := sm.SubscribeSync(ctx, []string{"orders"}, func(ctx context.Context, msg *submux.Message) {
	// If this panics, it's caught and logged, other subscriptions continue working
	riskyOperation(msg.Payload)
})
```

## Migration Handling

### Without Auto-Resubscribe (default)

By default, auto-resubscribe is **disabled**. Your application must handle migrations manually:

```go
sm, _ := submux.New(rdb) // auto-resubscribe is false by default

sub, _ := sm.SubscribeSync(ctx, []string{"orders"}, func(ctx context.Context, msg *submux.Message) {
	if msg.Type == submux.MessageTypeSignal && msg.Signal.EventType == submux.EventMigration {
		log.Printf("Migration detected - application must handle resubscription")

		// Option 1: Manually resubscribe
		sub.Unsubscribe(ctx)
		newSub, _ := sm.SubscribeSync(ctx, []string{"orders"}, callback)

		// Option 2: Restart application
		// Option 3: Ignore if messages are not critical
	}
})
```

### With Auto-Resubscribe (recommended for production)

Enable automatic migration handling:

```go
sm, _ := submux.New(rdb,
	submux.WithAutoResubscribe(true), // Enable automatic resubscription
)

sub, _ := sm.SubscribeSync(ctx, []string{"orders"}, func(ctx context.Context, msg *submux.Message) {
	switch msg.Type {
	case submux.MessageTypeMessage:
		// Process normal messages
		processOrder(msg.Payload)

	case submux.MessageTypeSignal:
		// Still receive signals for monitoring, but resubscription is automatic
		if msg.Signal.EventType == submux.EventMigration {
			log.Printf("Migration detected - automatic resubscription in progress")
			metrics.RecordMigration()
		}
	}
})
```

**How auto-resubscribe works:**

1. Topology monitor detects hashslot migration (via polling or MOVED errors)
2. Signal message sent to callback with `EventMigration`
3. Old subscription is automatically unsubscribed
4. New subscription is automatically created on the new node
5. If resubscription stalls (>2s) or times out (>30s), additional signals are sent

## Examples

### Multiple Subscriptions to Same Channel

Different callbacks can subscribe to the same channel independently:

```go
// Subscriber 1: Process orders
sub1, _ := sm.SubscribeSync(ctx, []string{"orders"}, func(ctx context.Context, msg *submux.Message) {
	processOrder(msg.Payload)
})

// Subscriber 2: Log orders for audit
sub2, _ := sm.SubscribeSync(ctx, []string{"orders"}, func(ctx context.Context, msg *submux.Message) {
	auditLog(msg.Payload)
})

// Subscriber 3: Update metrics
sub3, _ := sm.SubscribeSync(ctx, []string{"orders"}, func(ctx context.Context, msg *submux.Message) {
	metrics.Increment("orders.received")
})

// Unsubscribing one doesn't affect the others
sub1.Unsubscribe(ctx) // sub2 and sub3 continue receiving messages
```

### Pattern Matching with Multiple Patterns

```go
sub, _ := sm.PSubscribeSync(ctx,
	[]string{"logs:*", "events:user:*", "metrics:cpu:*"},
	func(ctx context.Context, msg *submux.Message) {
		// msg.Pattern tells you which pattern matched
		// msg.Channel tells you the actual channel name
		log.Printf("[%s] %s: %s", msg.Pattern, msg.Channel, msg.Payload)
	},
)
```

### Graceful Shutdown

```go
func main() {
	rdb := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs: []string{"localhost:7000"},
	})
	defer rdb.Close()

	sm, _ := submux.New(rdb, submux.WithAutoResubscribe(true))
	defer sm.Close()

	// Subscribe to channels
	ctx := context.Background()
	sub, _ := sm.SubscribeSync(ctx, []string{"orders"}, handleOrder)

	// Handle shutdown signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Wait for shutdown signal
	<-sigChan
	log.Println("Shutting down gracefully...")

	// Create timeout context for cleanup
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Unsubscribe and cleanup
	if err := sub.Unsubscribe(shutdownCtx); err != nil {
		log.Printf("Unsubscribe error during shutdown: %v", err)
	}

	sm.Close() // Closes all connections and stops topology monitor
	rdb.Close()

	log.Println("Shutdown complete")
}
```

### Production-Ready Setup with Observability

```go
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/redis/go-redis/v9"
	"github.com/lalloni/submux"
	"go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

func main() {
	// 1. Setup structured logging
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	// 2. Setup OpenTelemetry metrics
	exporter, _ := prometheus.New()
	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(exporter),
	)

	// 3. Setup Redis cluster client
	rdb := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs: []string{
			"redis-cluster-0:6379",
			"redis-cluster-1:6379",
			"redis-cluster-2:6379",
		},
		MaxRetries: 3,
		PoolSize:   50,
	})
	defer rdb.Close()

	// 4. Create production-ready SubMux
	sm, err := submux.New(rdb,
		submux.WithAutoResubscribe(true),                 // Handle migrations automatically
		submux.WithNodePreference(submux.BalancedAll),    // Distribute across all nodes
		submux.WithTopologyPollInterval(2*time.Second),   // Poll topology every 2s
		submux.WithMigrationTimeout(30*time.Second),      // 30s timeout for migrations
		submux.WithMigrationStallCheck(2*time.Second),    // Check for stalls every 2s
		submux.WithMinConnectionsPerNode(2),              // 2 connections per node minimum
		submux.WithLogger(logger),                        // Structured logging
		submux.WithMeterProvider(meterProvider),          // OpenTelemetry metrics
	)
	if err != nil {
		logger.Error("Failed to create SubMux", "error", err)
		os.Exit(1)
	}
	defer sm.Close()

	// 5. Subscribe with comprehensive error handling
	ctx := context.Background()
	sub, err := sm.SubscribeSync(ctx, []string{"orders", "payments"}, handleMessage)
	if err != nil {
		logger.Error("Failed to subscribe", "error", err)
		os.Exit(1)
	}
	defer sub.Unsubscribe(context.Background())

	// 6. Expose Prometheus metrics
	http.Handle("/metrics", promhttp.Handler())
	go http.ListenAndServe(":9090", nil)

	// 7. Wait for shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan

	logger.Info("Shutting down gracefully...")
}

func handleMessage(ctx context.Context, msg *submux.Message) {
	switch msg.Type {
	case submux.MessageTypeMessage:
		// Process message
		processMessage(msg.Channel, msg.Payload)

	case submux.MessageTypeSignal:
		// Monitor topology events
		switch msg.Signal.EventType {
		case submux.EventMigration:
			slog.Info("Migration detected",
				"hashslot", msg.Signal.Hashslot,
				"old_node", msg.Signal.OldNode,
				"new_node", msg.Signal.NewNode)

		case submux.EventNodeFailure:
			slog.Error("Node failure",
				"node", msg.Signal.OldNode,
				"details", msg.Signal.Details)

		case submux.EventMigrationStalled:
			slog.Warn("Migration stalled", "details", msg.Signal.Details)

		case submux.EventMigrationTimeout:
			slog.Error("Migration timeout", "details", msg.Signal.Details)
		}
	}
}
```

## OpenTelemetry Metrics (Optional)

submux provides optional OpenTelemetry instrumentation for production observability. Metrics are **opt-in** and have zero overhead when disabled.

### Basic Setup

```go
import (
    sdkmetric "go.opentelemetry.io/otel/sdk/metric"
    "go.opentelemetry.io/otel/exporters/prometheus"
)

// Enable metrics with Prometheus exporter
exporter, _ := prometheus.New()
provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(exporter))

sm, _ := submux.New(rdb,
    submux.WithMeterProvider(provider),  // Enable metrics
    submux.WithAutoResubscribe(true),
)
```

### Available Metrics

**13 Counters:**
- `submux.messages.received` - Messages received from Redis (by type, node)
- `submux.callbacks.invoked` - Callback invocations (by subscription type)
- `submux.callbacks.panics` - Panic recoveries (by subscription type)
- `submux.subscriptions.attempts` - Subscription attempts (by type, success)
- `submux.connections.created` - Connections created (by node)
- `submux.connections.failed` - Connection failures (by node)
- `submux.migrations.started` - Hashslot migrations detected
- `submux.migrations.completed` - Migrations completed successfully
- `submux.migrations.stalled` - Migrations stalled (>2s no progress)
- `submux.migrations.timeout` - Migrations timed out (>30s)
- `submux.topology.refreshes` - Topology refresh attempts (by success)
- `submux.workerpool.submissions` - Callback submissions to worker pool (blocked attribute)
- `submux.workerpool.dropped` - Callbacks dropped when pool stopped

**5 Histograms:**
- `submux.callbacks.latency` - Callback execution time (milliseconds)
- `submux.messages.latency` - End-to-end message latency (milliseconds)
- `submux.migrations.duration` - Migration completion time (milliseconds)
- `submux.topology.refresh_latency` - Topology refresh time (milliseconds)
- `submux.workerpool.queue_wait` - Queue wait time before callback execution (milliseconds)

**2 Observable Gauges:**
- `submux.workerpool.queue_depth` - Current tasks in worker pool queue
- `submux.workerpool.queue_capacity` - Maximum worker pool queue capacity

### Performance Characteristics

- **Disabled (default)**: 0.1 ns per operation (effectively zero overhead)
- **Enabled**: ~200 ns per operation (minimal impact)
- **Cardinality-safe**: No channel names in attributes (prevents metric explosion)
- **Build tag support**: Compile without OTEL dependencies using `-tags nometrics`

### Build Without Metrics

```bash
# Build without OpenTelemetry dependencies (smaller binary)
go build -tags nometrics -o myapp
```

## Documentation

### User Documentation
- **[README.md](README.md)** (this file) - User guide, examples, configuration
- **[CHANGELOG.md](CHANGELOG.md)** - Version history, release notes, migration guides

### Technical Documentation
- **[DESIGN.md](DESIGN.md)** - Architecture, design decisions, implementation details
- **[AGENTS.md](AGENTS.md)** - Development workflows, testing strategies, conventions

### Quick Links
- **Architecture**: See [DESIGN.md](DESIGN.md) Section 2-3
- **Configuration Options**: See [DESIGN.md](DESIGN.md) Section 4
- **Testing**: See [AGENTS.md](AGENTS.md) Testing section
- **Development**: See [AGENTS.md](AGENTS.md) Development Commands
- **Version History**: See [CHANGELOG.md](CHANGELOG.md)

## Contributing

For development setup, testing strategies, code conventions, and contribution guidelines, see [AGENTS.md](AGENTS.md).

## License

MIT
