# ADR-002: Ring Buffer for Sequencer Queue

**Date:** 2026-04-03
**Status:** Accepted
**Deciders:** Pablo Lalloni

## Context

ADR-001 introduced a bounded per-subscription queue in `callbackSequencer` using a Go slice (`[]pendingCallback`). The slice uses a front-pop / back-push pattern:

```go
// pop (drain side)
item := s.queue[0]
s.queue[0] = pendingCallback{} // zero for GC
s.queue = s.queue[1:]

// push (enqueue side)
s.queue = append(s.queue, pendingCallback{...})
```

This pattern causes the slice's backing array to **slide forward**: `[1:]` advances the start pointer without freeing the consumed prefix. The consumed slots are retained until `append` exhausts the remaining capacity and allocates a new array.

### Memory behavior

- **Bounded queues (maxQueueSize > 0):** Waste is proportional to the limit. With the default limit of 100 and Go's doubling growth, the backing array peaks at ~200 slots (~6KB). The consumed prefix is reclaimed when `append` triggers reallocation. This is acceptable but suboptimal — the bounded case should ideally use a single allocation with no GC pressure.

- **Unlimited queues (maxQueueSize == 0):** If the queue peaks at N messages during a burst and then settles to a small steady-state size M, the backing array retains ~2N slots until the sliding window reaches the end. For a peak of 1M messages, this is ~64MB of temporary retention. Recovery time depends on how fast the window slides (one slot per drain cycle), which can be very slow if steady-state throughput is low.

In both cases, the consumed prefix holds `pendingCallback` structs whose `msg *Message` pointer has been zeroed (line 137 handles this), so the actual message payloads are GC-eligible. The waste is the `pendingCallback` struct slots themselves (pointer + `time.Time` ≈ 32 bytes each).

## Decision

Replace the `[]pendingCallback` slice in `callbackSequencer` with a circular ring buffer (`pendingCallbackRing`). The ring buffer reuses slots cyclically, eliminating the sliding window problem entirely.

### Design

```go
type pendingCallbackRing struct {
    buf        []pendingCallback
    head, tail int
    count      int
    fixed      bool // true = bounded (no growth), false = growable
}
```

**Two modes:**
- **Fixed capacity** (bounded, `maxQueueSize > 0`): Pre-allocate `buf` to `maxQueueSize`. `push` returns false when full — the caller handles drops (unchanged from ADR-001). **Single allocation for the lifetime of the subscription. Zero GC pressure in steady state.**
- **Growable** (unlimited, `maxQueueSize == 0`): Start with a small initial capacity (16). Double when full, copying elements in FIFO order into the new array. Old array becomes immediately GC-eligible — no lingering consumed-prefix retention.

**Operations:**
- `push(item) bool` — write at `tail`, advance `tail = (tail + 1) % len(buf)`, increment `count`. If full: return false (fixed) or grow (growable).
- `pop() (item, bool)` — read at `head`, zero the slot, advance `head = (head + 1) % len(buf)`, decrement `count`. Return false if empty.
- `len() int` — return `count`.

**Lazy initialization:** The buffer is initialized on first `push` when `buf` is nil. This preserves the zero-value-ready property of `callbackSequencer` — `maxQueueSize` is set after struct creation in `submux.go`, so the ring buffer reads it at first use to decide fixed vs growable mode and initial capacity.

### Scope

The ring buffer is internal to `callbackSequencer` — unexported, in its own file (`ringbuf.go`). No public API changes. No changes to metrics, signals, configuration, or the sequencer's external behavior.

## Consequences

### Positive

- **Bounded mode: single allocation, zero GC pressure.** The `buf` array is allocated once at subscription creation and reused for the subscription's lifetime. No `append` reallocations, no consumed-prefix waste.
- **Unlimited mode: immediate reclamation on growth.** When the ring buffer doubles, the old array is GC-eligible immediately (not retained by a sliding window). Peak memory is still proportional to peak queue size, but recovery is instant on reallocation rather than gradual.
- **Zeroing on pop is preserved.** Popped slots are zeroed, ensuring `*Message` pointers don't leak.
- **No behavioral changes.** All existing sequencer tests pass without modification. The ring buffer is a drop-in replacement for the slice.

### Negative

- **Slightly more code.** A new ~50-line file (`ringbuf.go`) plus unit tests. The sequencer code itself gets simpler (no 3-line pop pattern).
- **No shrink policy for growable mode.** If an unlimited queue peaks at N and settles to M << N, the ring buffer retains N slots until it grows again (which it won't, since it's underutilized). This is the same as the slice behavior post-reallocation. A shrink-on-low-utilization policy was considered but rejected for simplicity — unlimited queues are opt-in and the user accepts the memory profile.

### Alternatives Considered

1. **Periodic slice compaction** (reallocate when `len < cap/4`): Would fix the sliding-window issue for slices but adds runtime cost and complexity. The ring buffer is cleaner and more predictable.
2. **`container/list` (doubly-linked list):** Per-element allocation, cache-unfriendly, higher overhead than both slices and ring buffers. Rejected.
3. **Channel-based queue:** Would require a fixed capacity (channels can't grow), making the unlimited mode impossible. Also harder to inspect length for metrics. Rejected.
