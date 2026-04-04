# ADR-003: Cascade ClusterClient Options to Direct Node Connections

**Date:** 2026-04-03
**Status:** Accepted
**Deciders:** Pablo Lalloni

## Context

submux creates direct `redis.Client` connections to specific Redis Cluster nodes in two places:

1. **PubSub connections** (`pool.go`, `createPubSubToNode`): Every subscription creates a direct client to the target node, bypassing the ClusterClient's slot routing which may be stale after migrations.
2. **Topology fallback** (`topology.go`, `refreshTopology`): When the main ClusterClient fails to fetch cluster topology, temporary clients are created to seed nodes.

The PubSub path — the primary code path for all subscription operations — creates clients with **only the node address**:

```go
directClient := redis.NewClient(&redis.Options{Addr: nodeAddr})
```

This discards all user-provided configuration from the `ClusterClient`:

- **Authentication** (Username, Password, CredentialsProvider, StreamingCredentialsProvider) — connections fail with `NOAUTH` on authenticated clusters.
- **TLS** (TLSConfig) — connections fail or are rejected on TLS-enabled clusters.
- **Custom networking** (Dialer, OnConnect) — proxy, custom DNS, and connection hooks are bypassed.
- **Timeouts** (DialTimeout, ReadTimeout, WriteTimeout) — go-redis defaults are used instead of the user's configuration.
- **Protocol and identity** (Protocol, ClientName, DisableIdentity) — RESP version and client tracking are lost.

The topology fallback path partially copies options (auth, TLS, dialer) but duplicates the field mapping inline and misses several fields (CredentialsProvider variants, Protocol, buffer sizes, identity settings).

## Decision

### 1. Extract a shared helper function

Introduce an unexported `clusterOptionsToNodeOptions(clusterOpts *redis.ClusterOptions, addr string) *redis.Options` function that copies all connection-level fields from `ClusterOptions` to `Options`. Both call sites use this helper.

**Fields copied** (connection-level, relevant to any direct node client):
- Authentication: Username, Password, CredentialsProvider, CredentialsProviderContext, StreamingCredentialsProvider
- TLS/Connection: TLSConfig, Dialer, OnConnect, ClientName
- Protocol: Protocol, ContextTimeoutEnabled
- Timeouts: DialTimeout, ReadTimeout, WriteTimeout
- Identity: DisableIdentity, IdentitySuffix
- Retry: MaxRetries, MinRetryBackoff, MaxRetryBackoff
- Buffers: ReadBufferSize, WriteBufferSize

**Fields NOT copied** (pool-specific, left at go-redis defaults):
- PoolSize, MinIdleConns, MaxIdleConns, MaxActiveConns
- PoolTimeout, ConnMaxIdleTime, ConnMaxLifetime, PoolFIFO

Pool-specific fields are intentionally omitted because each use case has different requirements: PubSub connections are long-lived with a single connection, topology fallback clients are short-lived and ephemeral. Callers override pool fields as needed.

### 2. Callers may override fields after calling the helper

The topology fallback overrides timeout and pool fields for its short-lived query pattern:

```go
nodeOpts := clusterOptionsToNodeOptions(opts, addr)
nodeOpts.PoolSize = 1
nodeOpts.MaxRetries = 1
nodeOpts.DialTimeout = 1 * time.Second
// ...
```

PubSub connections use the helper's output directly, inheriting the user's timeout configuration.

### 3. Maintenance annotation

The helper includes a comment referencing the go-redis version (`v9.17.2`) and listing the field categories. When upgrading go-redis, reviewers should check for new fields on `ClusterOptions` that should cascade.

## Consequences

### Positive
- **Fixes authenticated and TLS clusters:** PubSub connections now inherit all connection-level configuration.
- **Single maintenance point:** One function to update when go-redis adds new fields, instead of two inline mappings.
- **Topology fallback gains missing fields:** CredentialsProvider variants, Protocol, buffer sizes, and identity settings are now cascaded to topology fallback clients too.

### Negative
- **Field drift risk:** New fields added to `ClusterOptions` in future go-redis versions must be manually added to the helper. The version annotation mitigates but doesn't eliminate this risk.
- **No compile-time enforcement:** Go has no mechanism to enforce that all struct fields are copied. This is a maintenance discipline issue, not a code issue.

### Alternatives Considered

1. **Copy the entire `ClusterOptions` and convert field-by-field:** Rejected because `ClusterOptions` contains cluster-specific fields (MaxRedirects, RouteByLatency, ClusterSlots) that have no meaning for a standalone `redis.Options`. Copying everything would require more exclusions than inclusions.

2. **Store a pre-built `redis.Options` template in the pool:** Rejected because `ClusterClient.Options()` returns a snapshot at call time. Building it once at pool creation would miss any dynamic credential providers that return different values over time.

3. **Use the ClusterClient's internal `NewClient` hook:** `ClusterOptions.NewClient` is a factory function the ClusterClient uses to create per-node clients internally. However, this is designed for the ClusterClient's own connection pool, not for standalone PubSub clients, and using it would couple submux to go-redis internals.
