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

## Quick Start

```go
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/lalloni/submux"
)

func main() {
	// 1. Initialize go-redis cluster client
	rdb := redis.NewClusterClient(&redis.ClusterOptions{
		Addrs: []string{"localhost:7000", "localhost:7001"},
	})
	defer rdb.Close()

	// 2. Create SubMux instance
	sm, _ := submux.New(rdb,
		submux.WithAutoResubscribe(true),
		submux.WithReplicaPreference(true),
	)
	defer sm.Close()

	// 3. Subscribe
	ctx := context.Background()
	sub, _ := sm.SubscribeSync(ctx, []string{"my-channel"}, func(msg *submux.Message) {
		fmt.Printf("Received: %s\n", msg.Payload)
	})
	
	// 4. Cleanup when done
	defer sub.Unsubscribe(ctx)

	// Keep alive...
	select {}
}
```

## Observability

submux provides optional OpenTelemetry metrics for production monitoring:

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

**Available Metrics:**
- 11 counters: messages, callbacks, subscriptions, connections, migrations
- 4 histograms: latency tracking for callbacks, messages, migrations, topology refreshes
- All metrics use low-cardinality attributes (no channel names)

**Performance:**
- Zero overhead when disabled (0.1 ns per operation)
- Minimal overhead when enabled (~200 ns per operation)

See [CLAUDE.md](CLAUDE.md#opentelemetry-metrics) for complete metrics documentation.

## Documentation

For detailed architecture, API design, testing strategies, and best practices, please refer to the **[Design Document](DESIGN.md)** and **[CLAUDE.md](CLAUDE.md)**.

## License

MIT
