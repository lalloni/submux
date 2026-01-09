# submux: Agent Guidelines

This document provides a high-level overview of the **submux** project, designed to help AI agents (and human developers) understand the codebase, conventions, and workflows quickly.

## 🏢 Project Overview

**submux** is a Go library that acts as a multiplexer for Redis Cluster Pub/Sub connections. It allows thousands of logical subscriptions to share a small number of physical TCP connections (`redis.PubSub` objects), overcoming Redis client limitations and handling cluster topology changes transparently.

## 📂 Directory Structure

| Path | Description | Key Files |
|------|-------------|-----------|
| `/` | Root package. Public API. | `submux.go`, `config.go`, `pool.go` |
| `integration/` | Integration test suite. | `cluster_setup.go`, `topology_test.go` |
| `testutil/` | Test helpers and mocks. | `mock_cluster.go` |

## 🔑 Key Files (Source of Truth)

1.  **`DESIGN.md`**: **Primary technical reference.** Contains the consolidated architecture, API logic, and best practices. **Read this first.**
2.  **`PLAN.md`**: Project status tracker. Lists completed phases and the roadmap.
3.  **`CHANGELOG.md`**: Release history.
4.  **`integration/README.md`**: Guide to running the integration suite.

## 🏗️ Architecture Shortcuts

*   **Single Event Loop**: Each `connection` struct has exactly one `runEventLoop` goroutine. It handles all writes (commands) and reads (messages) via a `select` block.
*   **Synchronous API**: `SubscribeSync` blocks until Redis confirms the subscription. This eliminates race conditions.
*   **Topology Awareness**: `topology.go` monitors hashslots. If a slot moves, `submux` detects it (via `ClusterSlots` polling or generic errors) and triggers signal messages.
*   **Auto-Resubscribe**: **Not fully automated yet.** The library detects migration/failure and sends a `SignalMessage` to the user's channel. The *user* is responsible for re-subscribing, though helper logic exists to facilitate this.
    *   *Correction (v1.0)*: Recent updates added internal auto-resubscribe logic for seamless recovery in many cases, but Signals are still the primary notification mechanism.

## 🧪 Testing Conventions

### 1. Integration Tests (`integration/`)
*   **Requirement**: `redis-server` and `redis-cli` must be in `$PATH`.
*   **Execution**: `go test ./integration/... -v -race -timeout=30s`
*   **Mechanism**:
    *   Spawns real Redis processes.
    *   Uses **random ports** to avoid conflicts.
    *   **NO `time.Sleep`**: Tests use event polling (`Eventually`) or channel synchronization.
    *   **Debug**: Logs are captured. Use `go test -v` to see them.

### 2. Unit Tests
*   **Requirement**: No external dependencies.
*   **Execution**: `go test ./...` (excludes `integration/` by default due to build tags or folder structure).
*   **Mocking**: Use `testutil` for mocking Redis clients if needed, but core logic often relies on real structs with mocked network interfaces if refactored (currently mostly integration-heavy).

## 🛠️ Common Tasks

### How to...
*   **Run all tests**: `go test ./... -v -race -timeout=30s`
*   **Add a new feature**:
    1.  Update `DESIGN.md` with the proposal.
    2.  Implement in a new file or existing component.
    3.  Add unit tests.
    4.  Add an integration test in `integration/` if it involves Redis interaction.
*   **Verify Topology Logic**: Run `go test ./integration/topology_test.go -v`.

## ⚠️ Critical Rules
1.  **Do not use `time.Sleep`** in tests. Use robust synchronization.
2.  **Do not modify `API_DESIGN.md`** (it is deleted). Use `DESIGN.md`.
3.  **Respect the Single Event Loop**: Do not spawn new goroutines for reading/writing inside `connection.go`. All I/O must go through the loop.
4.  **Always Keep Documentation Updated**:
    *   **Markdown Files**: If you modify logic, you **MUST** update `DESIGN.md` and `README.md` immediately. Do not leave documentation drift.
    *   **Code Comments**: If you change code behavior, update the GoDoc comments above it. Inaccurate comments are worse than no comments.
    *   **CHANGELOG**: Add an entry to `CHANGELOG.md` for any notable change.
5. **Always verify all tests pass after each change** including unit tests and integration tests.
6. **Always apply Go style guidelines** and verify compliance using tools like `gofmt`, `golint`, `gosec` after every change.