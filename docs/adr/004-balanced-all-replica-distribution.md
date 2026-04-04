# ADR-004: Distribute BalancedAll Connections Across All Shard Replicas

**Date:** 2026-04-04
**Status:** Accepted
**Deciders:** Pablo Lalloni

## Context

`BalancedAll` is the default and recommended `NodePreference` mode. It is documented as distributing PubSub connections equally across **all** nodes (masters + replicas). In practice, connections were concentrated on master nodes only due to two independent bugs:

### Problem 1: Topology missing replica information

`refreshTopology` uses `CLUSTER SLOTS` to build the `hashslotToNodes` map. On Redis 8+, `CLUSTER SLOTS` only returns master nodes in its response. The topology state therefore contained a single node per shard (the master), making it impossible for any node selection strategy to choose replicas.

### Problem 2: Connection reuse preventing spread

`selectLeastLoadedAcrossNodes` selected the least-loaded **existing** connection across all nodes in a shard. When at least one connection existed on the master, the function always reused it rather than creating new connections to other nodes. Even if replica information were available, the function would never create a connection to a replica when the master already had one.

The combined effect: all PubSub connections landed on the 3 master nodes in a 3-shard cluster, ignoring 6 replica nodes entirely. This defeated the purpose of `BalancedAll` and concentrated load on masters.

## Decision

### 1. Enrich topology with CLUSTER NODES after CLUSTER SLOTS

After each `CLUSTER SLOTS` refresh, issue a `CLUSTER NODES` command and parse its output to discover replica nodes. Add an `enrichWithReplicaInfo` method on `topologyState` that appends replica addresses to `hashslotToNodes` entries where only the master is present.

**Why CLUSTER NODES:** It is the only command that reliably returns replica membership across all Redis versions (6, 7, 8+). The alternative — `CLUSTER REPLICAS <node-id>` per master — would require N additional round trips (one per shard) and more complex error handling.

**Guarding against duplicates:** If the Redis version already includes replicas in `CLUSTER SLOTS`, the enrichment is a no-op: it only appends replicas when `len(nodes) == 1` for a given hashslot. This prevents double-counting on older Redis versions.

**Failed replica filtering:** Replicas with `fail` in their flags are excluded from enrichment, preventing connections to nodes that are known to be down.

### 2. "Spread first, then balance" in selectLeastLoadedAcrossNodes

Change the connection selection strategy from "always reuse the least-loaded existing connection" to:

1. If any node in the shard has **zero** connections, create a new connection there.
2. Only when **all** nodes in the shard have at least one connection, reuse the least-loaded existing one.

This is implemented by tracking a `hasUnconnectedNode` flag during the scan. When true, the function falls through to the "create new connection" path, picking the node with the fewest connections (which will be 0).

### 3. Memory test threshold adjustment

The integration test `TestHighSubscriptionCount_MemoryUsage` had a 10 MB budget. With BalancedAll now creating connections to all 9 nodes (3 masters + 6 replicas) instead of 3 masters, the per-connection overhead (event loop goroutine, buffers, Redis client state) increases the baseline by ~2.5 MB. Threshold raised to 15 MB (~1.5 KB/channel at 1000 channels) with a comment explaining the budget.

## Consequences

### Positive
- **BalancedAll works as documented:** Connections are now distributed across all nodes in each shard (masters + replicas), matching the documented behavior and user expectations.
- **Reduced master load:** Read-heavy PubSub traffic is spread across replicas, reducing CPU and network pressure on masters.
- **Works across Redis versions:** The CLUSTER NODES enrichment handles Redis 6/7 (where CLUSTER SLOTS includes replicas) and Redis 8+ (where it does not) transparently.

### Negative
- **Additional round trip per topology refresh:** Each `refreshTopology` now issues `CLUSTER NODES` in addition to `CLUSTER SLOTS`. This adds ~1ms of latency to each topology poll cycle. Since polling runs on a background timer (default: 30s), the overhead is negligible.
- **More connections per SubMux instance:** A 3-shard, 2-replica cluster now uses up to 9 connections instead of 3. This is the intended behavior and the reason users choose `BalancedAll`, but it increases resource usage. The memory test threshold documents this trade-off.
- **CLUSTER NODES parsing:** The parser handles the standard format but does not cover edge cases like nodes with no address (`noaddr` flag). Failed replicas are filtered, but other unusual states (e.g., `handshake`) are not explicitly handled — they will be included if they don't contain `fail`.

### Alternatives Considered

1. **Use CLUSTER REPLICAS per master node:** Rejected. Requires one additional round trip per shard (3 for a typical cluster), more complex error handling, and doesn't provide the full picture in a single call.

2. **Use INFO REPLICATION on each node:** Rejected. Would require connecting to every node to discover topology, defeating the purpose of centralized topology polling.

3. **Always create new connections (never reuse):** Rejected. This would create O(subscriptions) connections instead of O(nodes), which is the problem `BalancedAll` is meant to solve. The "spread first, then balance" approach provides even distribution with bounded connection count.
