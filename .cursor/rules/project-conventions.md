---
trigger: always_on
---

# submux Project Conventions

These rules apply to all work in the submux project. Always consult the primary documentation files for detailed guidance.

## 📚 Documentation Hierarchy - CRITICAL

**Read these files in order when starting any task:**

1. **[AGENTS.md](../../AGENTS.md)** - START HERE for all workflows, commands, and conventions
2. **[DESIGN.md](../../DESIGN.md)** - PRIMARY TECHNICAL REFERENCE for architecture and design decisions
3. **[README.md](../../README.md)** - User-facing documentation
4. **[TODO.md](../../TODO.md)** - Planned work and completed features
5. **[CHANGELOG.md](../../CHANGELOG.md)** - Version history and changes

**Golden Rule:** Always reference these docs, never duplicate their content.

## 🔐 Thread Safety - Lock Ordering Rules

**CRITICAL:** When acquiring multiple locks, ALWAYS follow this order to prevent deadlocks:

1. `SubMux.mu` (subscription management)
2. `topologyMonitor.mu` (topology coordination)
3. `topologyState.mu` (topology data)
4. `pubSubPool.mu` (connection pool)
5. `pubSubMetadata.mu` (per-connection state)

**Never acquire locks in reverse order.** This is documented in DESIGN.md.

## 🎯 Code Quality Standards

### Error Handling
- Return errors, don't panic (except for truly unrecoverable conditions)
- Wrap errors with context: `fmt.Errorf("operation failed: %w", err)`
- Use structured logging with `slog` for error context
- Implement panic recovery in callback execution (see `callback.go`)

### Logging
- Use structured logging with `slog.Logger`
- Log levels: Debug (verbose), Info (normal), Warn (issues), Error (failures)
- Include context: `logger.Info("event occurred", "key", value)`
- Never log sensitive data (passwords, tokens, PII)

### Concurrency
- Prefer `sync.RWMutex` for read-heavy operations
- Use channels for goroutine communication
- Always defer `Unlock()` immediately after `Lock()`
- Document goroutine lifecycle in comments

### Testing
- Maintain >65% unit test coverage
- Write integration tests for all critical paths
- Use table-driven tests for multiple scenarios
- Mock external dependencies when needed

## 📝 Documentation Maintenance

**When making changes, update documentation in this order:**

### 1. Design/Architecture Change
- ✅ Update DESIGN.md FIRST (document the design)
- ✅ Implement the change
- ✅ Update AGENTS.md if workflows change
- ✅ Update README.md if user-facing API changes
- ✅ Update CHANGELOG.md with the change (include version)

### 2. New Feature
- ✅ Document design in DESIGN.md
- ✅ Update README.md if user-visible
- ✅ Update CHANGELOG.md in "Added" section
- ✅ Move from TODO.md to "Completed" if applicable

### 3. Bug Fix
- ✅ Update CHANGELOG.md in "Fixed" section
- ✅ Update DESIGN.md if fix changes design/behavior

### 4. Documentation Update
- ✅ Update CHANGELOG.md in "Documentation" section (if substantial)

## 🏗️ Architecture Patterns

### Single Event Loop Pattern
- Each Redis connection managed by ONE goroutine (not separate sender/receiver)
- All I/O operations handled sequentially
- Use channel-based command queue (`cmdCh`)
- See `eventloop.go` for reference implementation

### State Machines
- Subscriptions follow: Pending → Active → Closed/Failed
- Document state transitions in comments
- Use atomic operations for state checks where appropriate

### Worker Pool Pattern
- Use bounded worker pools to prevent goroutine explosion
- Implement backpressure when queue is full
- See `workerpool.go` for reference implementation

## 🔧 Development Commands

### Building
```bash
# Standard build
go build ./...

# Build without metrics (zero OpenTelemetry overhead)
go build -tags nometrics ./...
```

### Testing
```bash
# Unit tests only (fast)
go test ./...

# Integration tests (requires Docker)
cd integration && go test -v

# With coverage
go test -cover ./...

# Race detection
go test -race ./...
```

### Code Quality
```bash
# Format code
go fmt ./...

# Lint (if golangci-lint installed)
golangci-lint run

# Vulnerability check
go run golang.org/x/vuln/cmd/govulncheck@latest ./...
```

## 🎨 Code Style

### Naming Conventions
- Exported types/functions: `PascalCase`
- Unexported types/functions: `camelCase`
- Constants: `PascalCase` or `ALL_CAPS` for truly global constants
- Interfaces: `-er` suffix when possible (`Subscriber`, `Notifier`)

### Comments
- Public API: GoDoc comments on all exported symbols
- Internal: Comment non-obvious logic, not obvious code
- Use `//` for single-line, `/* */` for multi-line blocks
- TODO comments: `// TODO: description` (track in TODO.md for big items)

### File Organization
- One primary type per file (exceptions for small helpers)
- Group related functionality together
- Order: imports, constants, types, constructors, methods, helpers

## 🚨 Common Gotchas

### Hashslot Calculation
- Use `CRC16(channel) % 16384` - matches Redis Cluster algorithm
- Support hashtag syntax: `{tag}` extracts slot-controlling portion
- Test edge cases: empty channel, special characters

### Topology Changes
- Always handle MOVED and ASK redirects
- Implement timeout for migration detection (default: 30s)
- Use stall detection to catch stuck migrations (default: 2s)

### Callback Execution
- Always recover from panics in callbacks
- Use worker pool to limit concurrent callback execution
- Implement queue backpressure (drop or block based on config)

## 📊 Metrics (OpenTelemetry)

### When to Add Metrics
- Track throughput (counters)
- Measure latency (histograms)
- Monitor resource usage (gauges)
- Count errors/failures (counters)

### Naming Convention
- Prefix: `submux.`
- Format: `submux.category.metric_name`
- Units in suffix: `_seconds`, `_bytes`, `_total`
- Examples: `submux.messages.received_total`, `submux.callback.latency_seconds`

### Zero-Overhead Requirement
- Metrics MUST be optional (build tag: `nometrics`)
- Use interface abstraction (`metricsRecorder`)
- No-op implementation when disabled
- See `metrics.go` and `metrics_otel.go`

## 🔍 Code Review Checklist

Before submitting changes, verify:

- [ ] All tests pass (`go test ./...`)
- [ ] No race conditions (`go test -race ./...`)
- [ ] Code is formatted (`go fmt ./...`)
- [ ] Exported symbols have GoDoc comments
- [ ] Lock ordering rules followed (if using mutexes)
- [ ] Error handling is comprehensive
- [ ] Logging includes appropriate context
- [ ] DESIGN.md updated (if architecture changed)
- [ ] CHANGELOG.md updated (with version number)
- [ ] Integration tests added/updated (if applicable)

## 🎯 Git Commit Messages

Follow conventional commits format:

```
type(scope): brief description

Longer explanation if needed.

Fixes #issue-number
```

**Types:**
- `feat:` - New feature
- `fix:` - Bug fix
- `docs:` - Documentation only
- `test:` - Test improvements
- `refactor:` - Code restructuring (no behavior change)
- `perf:` - Performance improvement
- `chore:` - Maintenance tasks

**Scopes:** `pool`, `topology`, `metrics`, `eventloop`, `subscription`, `docs`, `test`

## 📦 Release Process

When creating a new release:

1. Update CHANGELOG.md with version number and date
2. Update version references in code (if applicable)
3. Create git tag: `git tag v3.x.x`
4. Push tag: `git push origin v3.x.x`
5. Document breaking changes prominently
6. Update README.md if API changed

## 🔗 External Resources

- **Redis Cluster Spec:** https://redis.io/docs/reference/cluster-spec/
- **go-redis Library:** https://github.com/redis/go-redis
- **OpenTelemetry Go:** https://opentelemetry.io/docs/languages/go/
- **Go Concurrency Patterns:** https://go.dev/blog/pipelines
