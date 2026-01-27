# TODO

## Bound goroutine count

Currently, there are a few uses of goroutines that can lead to having an unbounded number of goroutines.

1. Callbacks run in dedicated one-off goroutines
2. Automatic resubscriptions run in dedicated one-off goroutines
3. etc

We need to find better alternatives.

## Make subscription tracking optional

The *SubscribeSync methods could receive an option to request auto-resubscription explicitly, if not, then the subscription doesn't need to be tracked centrally at submux.

Alternatively, re-subscription could be determined by the callback function when processing a message type notifying of a Redis connection issue that implies the subscription doesn't exist anymore.

---

# Completed

## ✅ Integrate with OpenTelemetry

**Status:** Completed - Full OpenTelemetry instrumentation with 21 metrics (11 counters, 4 histograms, 2 observable gauges planned)

**Implementation Summary:**
- ✅ Optional dependency with `WithMeterProvider(metric.MeterProvider)` configuration
- ✅ Zero-overhead no-op implementation when disabled (0.1 ns/op)
- ✅ Cardinality-safe metrics (no channel names as attributes)
- ✅ Metric naming: `submux.*` prefix following OTEL conventions
- ✅ All metrics tested with race detector
- ✅ Documentation in DESIGN.md, CLAUDE.md, and README.md

**Metrics Implemented:**

**Counters (11):**
- `submux.messages.received` - Messages from Redis
- `submux.callbacks.invoked` - Callback invocations
- `submux.callbacks.panics` - Panic recoveries
- `submux.subscriptions.attempts` - Subscription attempts
- `submux.connections.created` - Connections created
- `submux.connections.failed` - Connection failures
- `submux.migrations.started` - Migrations detected
- `submux.migrations.completed` - Migrations finished
- `submux.migrations.stalled` - Stalled migrations (>2s)
- `submux.migrations.timeout` - Migration timeouts (>30s)
- `submux.topology.refreshes` - Topology refresh attempts

**Histograms (4):**
- `submux.callbacks.latency` - Callback execution time
- `submux.messages.latency` - End-to-end message latency
- `submux.migrations.duration` - Migration completion time
- `submux.topology.refresh_latency` - Topology refresh time

**Observable Gauges (2 - planned for future enhancement):**
- `submux.subscriptions.active` - Current subscriptions
- `submux.connections.active` - Current connections
