# Claude Code Instructions for submux

This file provides **actionable guidance** for Claude Code when working with this Go project.

---

## 🎯 Quick Start (Read This First)

**submux** is a Go library that multiplexes Redis Cluster Pub/Sub connections. Key characteristics:
- **Language:** Go 1.25.6+
- **Architecture:** Single event loop per connection, lock-based concurrency
- **Testing:** 66% coverage (unit + integration tests with real Redis clusters)
- **Dependencies:** go-redis/v9, optional OpenTelemetry

**Primary documentation files:**
1. **AGENTS.md** (504 lines) - Complete development guide, workflows, conventions
2. **DESIGN.md** (430 lines) - Architecture, design patterns, thread safety rules
3. **TODO.md** - Current roadmap and completed features
4. **CHANGELOG.md** - Version history

---

## 🔧 Available Tools

### gopls MCP Server (Enabled)
You have gopls LSP tools available via MCP. **USE THESE for all Go code analysis:**

**Finding code:**
- `SearchSymbol({"query": "SubMux"})` - Find symbols by name (fuzzy search)
- `GoToDefinition({"file": "...", "line": 42, "column": 10})` - Jump to definitions
- `ListDocumentSymbols({"file": "..."})` - Get file outline

**Understanding code:**
- `Hover({"file": "...", "line": 42, "column": 10})` - Get type info and docs
- `FindReferences({"file": "...", "line": 42, "column": 10})` - Find all usages
- `FindImplementers({"file": "...", "line": 10, "column": 6})` - Find interface implementations

**Editing code:**
- `GetDiagnostics({"file": "..."})` - Check for compile errors (MUST use after editing)
- `FormatCode({"file": "..."})` - Format with gofmt
- `OrganizeImports({"file": "..."})` - Fix imports
- `RenameSymbol({"file": "...", "line": 42, "column": 10, "newName": "..."})` - Safe refactoring

**Workflow:**
1. Before editing: Use `FindReferences` to understand impact
2. After editing: ALWAYS use `GetDiagnostics` to check for errors
3. Before committing: Use `FormatCode` and `OrganizeImports`

---

## 🚨 CRITICAL RULES - Must Follow

### 1. Lock Ordering (Prevents Deadlocks)
When acquiring multiple locks, **ALWAYS use this order:**
1. `SubMux.mu`
2. `topologyMonitor.mu`
3. `topologyState.mu`
4. `pubSubPool.mu`
5. `pubSubMetadata.mu`

**Never acquire locks in reverse order.** See DESIGN.md for details.

### 2. Thread Safety Patterns
- Prefer `sync.RWMutex` for read-heavy operations
- Always `defer unlock()` immediately after `lock()`
- Use atomic operations for simple state checks
- Document goroutine lifecycle in comments

### 3. Error Handling
- Return errors, don't panic (except truly unrecoverable conditions)
- Wrap errors: `fmt.Errorf("operation failed: %w", err)`
- Use structured logging with `slog` for context
- Implement panic recovery in callbacks (see `callback.go`)

### 4. Testing Requirements
- Maintain >65% unit test coverage (currently 66%)
- Write table-driven tests for multiple scenarios
- Use retry utilities for integration tests (see `integration/shared_cluster_test.go`)
- Run with `-race` flag to detect race conditions

### 5. Documentation Maintenance
**When making changes, update in this order:**
1. Update **DESIGN.md** FIRST (if architecture changes)
2. Implement the change
3. Update **AGENTS.md** (if workflows change)
4. Update **README.md** (if public API changes)
5. Update **CHANGELOG.md** (ALWAYS - include version number)

---

## 🏗️ Architecture Quick Reference

### Core Components
- **SubMux** (`submux.go`) - Main API, subscription routing
- **PubSub Pool** (`pool.go`) - Connection reuse via hashslot routing
- **Event Loop** (`eventloop.go`) - Single goroutine per connection (commands + messages)
- **Topology Monitor** (`topology.go`) - Background polling, migration detection
- **Worker Pool** (`workerpool.go`) - Bounded callback execution

### Key Design Patterns

**1. Single Event Loop Pattern**
- Each Redis connection managed by ONE goroutine (not separate sender/receiver)
- Eliminates complex synchronization
- See `eventloop.go` for reference implementation

**2. Hashslot-Based Connection Reuse**
- Uses `CRC16(channel) % 16384` to map channels to hashslots
- Reuses connections for channels in same hashslot
- Reduces connections from O(channels) to O(hashslots)

**3. State Machines**
- Subscriptions: `Pending → Active → Closed/Failed`
- Document all state transitions
- Use atomic operations where appropriate

**For complete architecture:** Read DESIGN.md Section 2-3.

---

## 🔄 Development Workflows

### Understanding Code (Read Workflow)
1. **Start with gopls:** `SearchSymbol` to find what you're looking for
2. **Navigate:** Use `GoToDefinition` and `Hover` to understand implementation
3. **Check references:** Use `FindReferences` to see how it's used
4. **Read DESIGN.md:** For architecture context (don't guess!)

### Making Changes (Edit Workflow)
1. **Read AGENTS.md** for complete conventions (if first time)
2. **Find references:** Use `FindReferences` before changing any function signature
3. **Make edits:** Update definition and all call sites
4. **Check diagnostics:** MUST run `GetDiagnostics` after every edit
5. **Fix errors:** Iterate until clean
6. **Format code:** Run `FormatCode` and `OrganizeImports`
7. **Run tests:** `go test ./...` (unit) and `cd integration && go test` (integration)
8. **Update docs:** Follow the 5-step documentation maintenance workflow above

### Before Committing
```bash
# Required checks
go test ./...              # All tests pass
go test -race ./...        # No race conditions
go fmt ./...               # Code formatted

# Update documentation
# 1. DESIGN.md (if architecture changed)
# 2. AGENTS.md (if workflows changed)
# 3. README.md (if public API changed)
# 4. CHANGELOG.md (ALWAYS - record the change)
```

---

## 🧪 Testing Guidelines

### Unit Tests
- **Location:** `*_test.go` files in root directory
- **Style:** Table-driven tests with multiple scenarios
- **Mocking:** Mock external dependencies when needed
- **Coverage target:** >65% (currently 66%)

### Integration Tests
- **Location:** `integration/` directory
- **Infrastructure:** Real 6-node Redis cluster (Docker)
- **Retry utilities:** Use `retryWithBackoff()` and `eventually()` for timing-dependent operations
- **Test suites:** 11 suites covering topology, resiliency, concurrency, load

**Common pitfall:** Integration tests can be flaky without retry utilities. ALWAYS use:
```go
err := retryWithBackoff(t, 5*time.Second, 100*time.Millisecond, func() error {
    return operationThatMightFail()
})
```

**For complete testing guide:** Read `integration/README.md` or `.cursor/rules/testing.md`.

---

## 📊 Metrics (OpenTelemetry)

- Metrics are **optional** - build with `-tags nometrics` for zero overhead
- Uses interface abstraction (`metricsRecorder`)
- No-op implementation when disabled
- See `metrics.go` and `metrics_otel.go` for examples

---

## 🎨 Code Style

### Naming
- Exported: `PascalCase`
- Unexported: `camelCase`
- Interfaces: `-er` suffix (`Subscriber`, `Notifier`)

### Comments
- GoDoc on all exported symbols
- Comment non-obvious logic (not obvious code)
- Format: `// FunctionName does X by doing Y.`

### File Organization
- One primary type per file (exceptions for small helpers)
- Order: imports, constants, types, constructors, methods, helpers

---

## 🚨 Common Gotchas

### 1. Hashslot Calculation
```go
// Correct: Use CRC16 modulo 16384 (matches Redis Cluster)
slot := crc16.ChecksumCCITT([]byte(channel)) % 16384

// Support hashtag syntax: {tag} controls slot
// "news:{sports}" and "updates:{sports}" map to same slot
```

### 2. Topology Changes
- Always handle MOVED and ASK redirects from Redis
- Implement timeout for migration detection (default: 30s)
- Use stall detection to catch stuck migrations (default: 2s)
- Consider enabling auto-resubscribe in production

### 3. Callback Execution
- Always recover from panics (see `callback.go`)
- Use worker pool to limit concurrency (prevents goroutine explosion)
- Implement backpressure when queue is full

---

## 🔍 When You're Stuck

### "How does X work?"
1. Use `SearchSymbol` to find X
2. Use `GoToDefinition` to read implementation
3. Use `FindReferences` to see usage examples
4. Read relevant DESIGN.md section

### "Where is Y implemented?"
1. `SearchSymbol({"query": "Y"})` - Fuzzy search
2. Check `ListDocumentSymbols` in likely files
3. Use `Grep` tool as last resort (gopls is smarter)

### "What are the design principles?"
Read DESIGN.md Section 6 (Best Practices)

### "How do I test this?"
Read `.cursor/rules/testing.md` or `integration/README.md`

### "I'm getting deadlocks"
Check lock ordering (see CRITICAL RULES above) and DESIGN.md Section 3.5

---

## 🎯 Common Tasks

### Adding a New Feature
1. **Plan:** Read DESIGN.md to understand architecture
2. **Design:** Update DESIGN.md FIRST with your design
3. **Implement:** Follow code conventions above
4. **Test:** Write unit tests (table-driven) and integration tests
5. **Document:** Update README.md (if user-visible) and CHANGELOG.md
6. **Review:** Check all items in "Before Committing" section

### Fixing a Bug
1. **Understand:** Use gopls tools to find the bug location
2. **Find impact:** Use `FindReferences` to see all affected code
3. **Fix:** Make the fix and add regression test
4. **Verify:** Run `go test -race ./...`
5. **Document:** Update CHANGELOG.md in "Fixed" section

### Refactoring Code
1. **Before changing:** Use `FindReferences` to see all usages
2. **Safe rename:** Use `RenameSymbol` instead of manual find-replace
3. **Check errors:** Run `GetDiagnostics` after each step
4. **Run tests:** Verify no behavior changed
5. **Update docs:** If architecture changed, update DESIGN.md

---

## 📚 Complete Documentation References

For deep dives, read these files in order:

1. **AGENTS.md** (504 lines) - Complete development guide
   - All commands and workflows
   - Detailed conventions
   - Common gotchas and troubleshooting

2. **DESIGN.md** (430 lines) - Architecture documentation
   - Complete architecture overview
   - Lock ordering and thread safety
   - Resilience strategies
   - Testing philosophy
   - Best practices

3. **integration/README.md** - Integration testing guide
   - Cluster setup
   - Running tests
   - Debugging flaky tests

4. **.cursor/rules/** - Context-specific guides (for Cursor users)
   - `gopls-mcp-server.md` - Go tooling workflows
   - `project-conventions.md` - Detailed conventions
   - `testing.md` - Comprehensive testing guide

---

## ⚡ TL;DR - Start Here Every Session

1. **Use gopls MCP tools** for all Go code analysis (not grep/search)
2. **Follow lock ordering** (SubMux → topologyMonitor → topologyState → pubSubPool → pubSubMetadata)
3. **Check diagnostics** after every code edit with `GetDiagnostics`
4. **Maintain >65% coverage** with table-driven tests and retry utilities
5. **Update CHANGELOG.md** for every notable change (with version number)
6. **Read DESIGN.md** before making architectural changes
7. **Use AGENTS.md** as your complete reference guide

**When in doubt:** Read AGENTS.md → It will guide you to DESIGN.md for technical details.

---

**Configuration:**
- Output style: Explanatory (set in `.claude/settings.local.json`)
- Plugins enabled: gopls-lsp, code-simplifier (see `.claude/settings.json`)
- Permissions: Pre-approved for common Go commands (see `.claude/settings.local.json`)
