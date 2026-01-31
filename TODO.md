# TODO

## Make subscription tracking optional

The *SubscribeSync methods could receive an option to request auto-resubscription explicitly, if not, then the subscription doesn't need to be tracked centrally at submux.

Alternatively, re-subscription could be determined by the callback function when processing a message type notifying of a Redis connection issue that implies the subscription doesn't exist anymore.

---

# Completed

## ✅ Bound goroutine count

**Status:** Completed - Worker pool with bounded goroutines and backpressure

**Implementation Summary:**
- ✅ Bounded worker pool prevents goroutine explosion under high throughput
- ✅ Configuration: `WithCallbackWorkers(n)` (default: `runtime.NumCPU() * 2`)
- ✅ Configuration: `WithCallbackQueueSize(n)` (default: `10000`)
- ✅ Backpressure when queue is full (blocks until space available)
- ✅ Worker pool telemetry: submissions, dropped, queue_wait, queue_depth, queue_capacity
- ✅ Context passed to callbacks, canceled on `Close()`

---

## ✅ Integrate with OpenTelemetry

**Status:** Completed - Full OpenTelemetry instrumentation with 20 metrics (13 counters, 5 histograms, 2 observable gauges)

**Implementation Summary:**
- ✅ Optional dependency with `WithMeterProvider(metric.MeterProvider)` configuration
- ✅ Zero-overhead no-op implementation when disabled (0.1 ns/op)
- ✅ Cardinality-safe metrics (no channel names as attributes)
- ✅ Metric naming: `submux.*` prefix following OTEL conventions
- ✅ All metrics tested with race detector
- ✅ Documentation in DESIGN.md, CLAUDE.md, and README.md

**Metrics Implemented:**

**Counters (13):**
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
- `submux.workerpool.submissions` - Callback submissions to pool
- `submux.workerpool.dropped` - Callbacks dropped (pool stopped)

**Histograms (5):**
- `submux.callbacks.latency` - Callback execution time
- `submux.messages.latency` - End-to-end message latency
- `submux.migrations.duration` - Migration completion time
- `submux.topology.refresh_latency` - Topology refresh time
- `submux.workerpool.queue_wait` - Queue wait time before execution

**Observable Gauges (2 - worker pool implemented, others planned):**
- ✅ `submux.workerpool.queue_depth` - Current tasks in queue
- ✅ `submux.workerpool.queue_capacity` - Maximum queue capacity
- ⏳ `submux.subscriptions.active` - Current subscriptions (planned)
- ⏳ `submux.connections.active` - Current connections (planned)
