# ADR-001: Bounded Per-Subscription Queue with Drop Policy

**Date:** 2026-04-02
**Status:** Accepted
**Deciders:** Pablo Lalloni

## Context

The `callbackSequencer` (an internal component) maintains a per-subscription message queue implemented as a `[]pendingCallback` slice. This queue decouples the event loop (which reads messages from Redis) from callback execution (which may be slow). The event loop appends messages via `enqueue()`, which never blocks, while a single drain task processes them sequentially.

Under sustained imbalance — high publish rate combined with slow callbacks on a specific subscription — the queue grows without bound at a rate of `R_in - R_out` per unit time. No existing backpressure mechanism limits this growth:

- The **WorkerPool** bounded channel only governs drain task submission (one per subscription at a time), not per-message queuing.
- The **go-redis internal channel** buffer only fills if the event loop blocks, which it doesn't since `enqueue()` is non-blocking.
- **Redis TCP backpressure** never engages for the same reason.

This can lead to out-of-memory conditions in production when a single subscription's callback cannot keep up with message volume.

## Decision

### 1. Configurable queue limit with tail-drop

Add a configurable maximum queue size per subscription. When the queue reaches its limit, the newest incoming message is dropped (tail-drop). This preserves the non-blocking property of `enqueue()` — the event loop is never stalled by a slow subscription.

**Default limit: 100.** This is deliberately conservative to protect against OOM by default. Users with high-throughput subscriptions and fast callbacks can increase or disable it.

**Why tail-drop (drop newest) instead of head-drop (drop oldest):**
- Tail-drop is simpler to implement (skip the append).
- The subscription is already behind; discarding the newest message while the callback catches up on the backlog avoids reordering the already-queued messages.
- Head-drop would require dequeuing and discarding from a position potentially being read by the drain task, adding synchronization complexity.

### 2. Two-level configuration

- **SubMux-level default:** `WithSubscriptionQueueLimit(limit int)` sets the default for all subscriptions created on that SubMux. Zero means unlimited. Default is 100.
- **Per-subscription override:** `WithQueueLimit(limit int)` on `SubscribeOption` overrides for a specific subscription. Zero means unlimited. Not calling it inherits the SubMux default.

This follows the existing pattern where SubMux-level options (`WithCallbackWorkers`, `WithCallbackQueueSize`) set global defaults. The per-subscription option is new — it introduces a `SubscribeOption` type and variadic `...SubscribeOption` parameter on `SubscribeSync`, `PSubscribeSync`, and `SSubscribeSync`.

**Resolution logic:**
```go
effective := sm.config.subscriptionQueueLimit  // SubMux default (100)
if subCfg.queueLimitSet {
    effective = subCfg.queueLimit  // per-subscription override (0 = unlimited)
}
```

A `queueLimitSet` boolean distinguishes "not specified" (inherit default) from "explicitly set to 0" (unlimited).

### 3. Signal notification on overflow

When the queue first reaches capacity and begins dropping messages, a `EventQueueOverflow` signal is delivered to the subscription's callback. This signal:

- **Bypasses the sequencer queue** (which is full) via a direct goroutine tracked by `callbackWg`. This is the same fallback pattern already used when the WorkerPool is stopped.
- **Is coalesced:** only one signal per overflow episode. When the queue drains below capacity and a new message enqueues successfully, the overflow state resets, allowing a fresh signal on the next overflow.
- **Carries context** via a new `DroppedCount` field on `SignalInfo`, indicating how many messages have been dropped since the overflow episode began.

**The signal may arrive out-of-order** relative to queued messages because it bypasses the queue. This is intentional and unavoidable — the alternative (no notification) is worse.

### 4. Metrics

Two new metrics on the `metricsRecorder` interface:

| Metric | Type | Name | Purpose |
|--------|------|------|---------|
| Queue depth at enqueue time | Histogram | `submux.subscriptions.queue_depth` | Distribution of queue depths; reveals how close queues get to their limits |
| Dropped messages | Counter | `submux.subscriptions.queue_dropped` | Aggregate count of data loss across all subscriptions |

These follow the existing naming convention (`submux.{domain}.{metric}`) and include both OTel and no-op implementations.

### 5. The term "sequencer" remains internal

The internal `callbackSequencer` type and its `maxQueueSize` field are implementation details. The public API uses "subscription queue" terminology:
- `WithSubscriptionQueueLimit` (SubMux option)
- `WithQueueLimit` (SubscribeOption)
- `EventQueueOverflow` (signal event type)
- `submux.subscriptions.queue_depth` / `submux.subscriptions.queue_dropped` (metrics)

## Consequences

### Positive
- **Prevents OOM** from slow subscriptions under high message volume.
- **Non-breaking for fast consumers:** With a default of 100, subscriptions that keep up never hit the limit.
- **Observable:** Metrics and signals give operators visibility into queue pressure and data loss.
- **Backward-compatible API:** Adding `...SubscribeOption` as a variadic parameter doesn't break existing callers.

### Negative
- **Behavior change for upgraders:** Existing users who relied on unlimited queuing (e.g., intentionally slow callbacks buffering thousands of messages) will see drops at 100. They must explicitly opt out via `WithSubscriptionQueueLimit(0)`.
- **Out-of-order signal delivery:** The overflow signal bypasses the queue and may interleave with normal message processing. Callbacks must handle this.
- **No backpressure to Redis:** The design deliberately avoids blocking the event loop. This means Redis will keep sending messages even when they're being dropped. True backpressure would require blocking `enqueue()`, which would stall all subscriptions sharing the same connection.

### Alternatives Considered

1. **Blocking enqueue (backpressure to event loop):** Rejected because it couples slow subscriptions to the event loop, stalling all subscriptions on the same connection. The event loop's non-blocking property is a core design invariant.

2. **Head-drop (drop oldest):** Rejected for implementation complexity (concurrent access with drain task) and because it reorders the remaining messages relative to what the callback has already seen.

3. **No default limit (opt-in only):** Rejected because the OOM risk is the common case that needs protection. Users who need unlimited queuing can explicitly configure it.

4. **Per-subscription observable gauge for queue depth:** Rejected in favor of a histogram at enqueue time, which captures the distribution without requiring periodic polling and doesn't miss transient spikes.
