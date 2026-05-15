# Research & Design Decisions: session-mgmt

## Summary
- **Feature**: `session-mgmt`
- **Discovery Scope**: Extension (builds on policy-gate pipeline and pipeline infrastructure from bare-proxy)
- **Key Findings**:
  - The existing `Handler` halt contract (`nil, nil` = continue) makes lock release impossible inside a pipeline handler; ConcurrencyGuard must wrap `pipeline.Run()` at the server level
  - `github.com/redis/go-redis/v9` v9.19.0 is the canonical Go Redis client; `Eval()` with inline Lua scripts provides atomic lock operations without extra libraries
  - Reference-counting is required for the session mutex because multiple tool calls within the same turn share a turnID and must re-enter the lock

## Research Log

### Pipeline Handler Halt Semantics

- **Context**: Needed to determine whether session/turn locks could be implemented as pipeline Handlers (simpler) or required a server-level wrapper
- **Findings**: `Handler.Handle(ctx, req) (*JSONRPCResponse, error)` returns either `(nil, nil)` to continue or a non-nil value to halt. The `Pipeline.Run()` loop stops at the first non-nil return. There is no "after" hook — a handler that returns `(nil, nil)` to pass through has no way to execute code after the downstream chain completes.
- **Implications**: Lock release must happen at the call site of `pipeline.Run()`. The server's `handleMCPPost` function is the only viable wrap point. This decision eliminates any need to change the `Handler` or `Pipeline` interfaces.

### Redis Client Library Selection

- **Context**: Selecting a Redis client library compatible with Go 1.25
- **Sources**: pkg.go.dev, GitHub releases
- **Findings**: `github.com/redis/go-redis/v9` is the canonical client (moved from `go-redis/redis` to `redis/go-redis` in the v9 cycle). Current latest: v9.19.0 (April 2026). Requires Go 1.24+, compatible with the project's Go 1.25.
- **Implications**: Single new `go.mod` dependency. `Eval()` supports inline Lua scripts, which is the mechanism for all atomic lock operations.

### Distributed Mutex Library Evaluation

- **Context**: Whether to adopt `redsync` (Redlock) or build a thin custom mutex
- **Sources**: `github.com/go-redsync/redsync/v4` (v4.16.0), Redis distributed locks documentation
- **Findings**: `redsync` implements the Redlock algorithm across N Redis instances for quorum-based locking. For a single Redis instance (v0 demo), Redlock adds complexity without safety benefit — Redlock's quorum property only matters with multiple independent Redis nodes.
- **Decision**: Build a thin `SessionLocker` using `SetNX` semantics via a Lua acquire script + compare-and-delete Lua release script. ~40 lines of Go + 3 Lua scripts. No Redlock needed for v0.

### Per-Turn RWLock Library Evaluation

- **Context**: Whether to adopt `github.com/e-chip/redis-rwlock` or build custom
- **Findings**: `redis-rwlock` implements a counter-bias Lua pattern (single int key; readers add 1, writers subtract 1<<30). Functional but: (a) unclear maintenance status; (b) adds an obscure dependency; (c) the counter-bias approach is equivalent in complexity to the two-key (readers counter + wlock key) approach, which is more readable.
- **Decision**: Build `TurnRWLock` with two Redis keys (`turn:<id>:readers` int + `turn:<id>:wlock` string). Lua scripts for all four operations (ReadLock, ReadUnlock, WriteLock, WriteUnlock). ~50 lines of Go + 4 Lua scripts.

### Reference-Counted Session Mutex Design

- **Context**: A simple SET NX session lock (one key, one holder) would work for single-caller-per-turn scenarios, but would be unsafe when the same turn issues multiple concurrent tool calls — the first call to complete would delete the key, allowing a different turn to acquire it while other calls from the same turn are still in flight.
- **Findings**: The server generates a new TurnID per request if none is provided via `X-Mcp-Turn-Id`. When an agent reuses the same turnID for multiple concurrent calls (same agentic loop iteration), the session lock must be re-entrant for that turn.
- **Decision**: Reference-counted mutex using two Redis keys (`session:<id>:lock` = turnID, `session:<id>:refcount` = int). Acquire increments refcount (or sets it to 1 on first acquire). Release decrements; deletes both keys only when refcount reaches 0. All state transitions are single Lua scripts.

### BudgetTracker Accuracy

- **Context**: Requirement 5.1 states the budget counter must be accurate under concurrent load. The current `BudgetTracker.IncrementAndGet()` is called at the start of `PolicyGateHandler.Handle()`. Would this need to change?
- **Findings**: `PolicyGateHandler.Handle()` is called inside `pipeline.Run()`, which is called inside `ConcurrencyGuard.Execute()` after both the session mutex and the turn RWLock slot are acquired. Under the ConcurrencyGuard wrapper, concurrent calls within a turn are serialized at the turn RWLock level (for write-class) or proceed in parallel (for read-class). In both cases, the budget increment happens only after the concurrency slot is held, not before — satisfying the requirement without any changes to BudgetTracker.
- **Implication**: No changes to `BudgetTracker` or `PolicyGateHandler`. This is automatic from the execution order.

### Lock Key Pitfalls

- **Sources**: Redis distributed locks documentation; go-redis pitfalls literature
- **Key findings**:
  - **Non-unique lock value**: Must use a UUID4 owner token for the write lock; compare-and-delete Lua prevents a wrong holder from deleting the key
  - **SETNX + EXPIRE (non-atomic)**: Never use two-command form; `SET key val EX ttl NX` (via go-redis `SetNX`) is a single atomic command
  - **TTL expiry during work**: Set TTL > maximum expected turn duration; `Extend()` is provided for approval-flow's long-hold case
  - **Restart behavior**: Redis restart clears all keys; v0 accepts this as the locks are ephemeral demo state

## Architecture Pattern Evaluation

| Option | Description | Strengths | Risks / Limitations | Decision |
|--------|-------------|-----------|---------------------|----------|
| Pipeline Handler (pre-policy) | SessionMutexHandler as first Handler in pipeline | Consistent with pipeline pattern | Cannot run code after downstream chain; halt semantics prevent lock release | ❌ Rejected |
| Server-level wrapper | ConcurrencyGuard wraps pipeline.Run() in handleMCPPost | Clean lifecycle; explicit defer; no interface changes | Slightly more coupling between guard and server struct | ✅ Selected |
| External library (redsync) | Adopt redsync for Redlock | Battle-tested for multi-node | Overkill for single-node v0; quorum irrelevant | ❌ Rejected |
| External library (redis-rwlock) | Adopt redis-rwlock for per-turn RWLock | Implements counter-bias Lua | Unclear maintenance; adds obscure dependency | ❌ Rejected |
| Build custom (two-key RWLock) | readers counter + wlock string; 4 Lua scripts | Readable, self-contained, v0 appropriate | Reader-preference only (writer-preference is v1) | ✅ Selected |

## Design Decisions

### Decision: Reader-Preference vs. Writer-Preference RWLock

- **Context**: The TurnRWLock must balance correctness and implementation complexity for v0
- **Alternatives Considered**:
  1. Reader-preference: writers wait for current readers to drain; new read requests may arrive while a writer is waiting
  2. Writer-preference: once a writer is queued, new reads are blocked; requires an additional "writer-pending" flag in Redis
- **Selected Approach**: Reader-preference for v0 using a simple readers counter + wlock key
- **Rationale**: The v0 demo does not have a scenario where a writer starves due to constant new readers. Writer-preference adds a third Redis key and a more complex Lua acquire script. Defer to v1 when real workloads drive the need.
- **Trade-offs**: Theoretical writer starvation under reader-heavy load; acceptable for demo
- **Follow-up**: Add `turn:<id>:wpending` flag and writer-preference Lua logic in v1

### Decision: ConcurrencyGuard as Server Middleware (not Pipeline Handler)

- **Context**: Where to integrate lock acquire/release in the request lifecycle
- **Selected Approach**: `guard.Execute()` wraps `pipeline.Run()` in `handleMCPPost`
- **Rationale**: The Handler interface has no "after" hook; any lock acquired by a Handler that returns `(nil, nil)` can never be released by that same Handler. Server-level wrapping is the only clean option without changing the Pipeline interface.
- **Trade-offs**: The guard is coupled to the server struct; not reusable as a pipeline plugin
- **Follow-up**: If the Pipeline grows an "around" middleware API, ConcurrencyGuard could be refactored into a pipeline middleware in v1

### Decision: Build vs. Adopt for Session Mutex

- **Selected Approach**: Build `SessionLocker` (~40 lines of Go + 3 Lua scripts) using go-redis `Eval()`
- **Rationale**: Redsync's Redlock is designed for multi-node quorum; single Redis node in v0 does not benefit. A thin custom implementation is more readable, has zero extra transitive dependencies, and is easy to test with miniredis or a real Redis container.

## Risks & Mitigations

- **Redis restart clears all in-flight locks** — Acceptable for v0 demo; document explicitly in README. Mitigation: short TTL (60s default) limits window of stuck-state after crash.
- **TTL expiry during a long turn** — If a turn takes longer than `SessionLockTTL`, another turn could acquire the mutex while the first is still in flight. Mitigation: default 60s TTL is much longer than any expected tool call; approval-flow extends TTL for approval-wait holds via `SessionLocker.Extend()`.
- **Busy-wait overhead under contention** — 50ms poll interval means up to 100 Redis round trips per blocked request at the default 5s timeout. Mitigation: for v0 demo traffic this is negligible; v1 can use Redis pub/sub or BLPOP for backpressure.
- **Writer starvation (reader-preference)** — Under constant new read traffic, write-class calls may starve. Mitigation: not a concern for v0 demo scenarios; writer-preference is a v1 upgrade path.

## References

- [go-redis v9 pkg.go.dev](https://pkg.go.dev/github.com/redis/go-redis/v9) — client API reference
- [Redis Distributed Locks (Redis docs)](https://redis.io/docs/latest/develop/clients/patterns/distributed-locks/) — SET NX EX pattern and pitfalls
- [Redlock algorithm](https://redis.io/docs/latest/develop/use/patterns/distributed-locks/) — why Redlock is not needed for single-node
- [go-redsync/redsync v4](https://pkg.go.dev/github.com/go-redsync/redsync/v4) — evaluated and rejected for v0
