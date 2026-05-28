# Requirements Document

## Project Description (Input)

### Who has the problem

The ToolGate gateway operators and any agent issuing same-session turns. Today, the per-session mutex in `cmd/gateway/session_locker.go` uses Redis `SETNX` with a 60-second TTL — the Kleppmann distributed-locking failure mode. If a turn outlasts the TTL, the lock expires while the holder still believes it's held, allowing two workers to interleave writes on the same session. Every downstream v1 spec (`v1-resource-locks` especially) reuses the same lock primitive, so building it on a sound substrate matters before any other architectural reshape lands.

### Current situation

- `cmd/gateway/session_locker.go` implements the per-session mutex via Redis `SETNX` + TTL; it is used by every request that touches a session.
- `cmd/gateway/redis.go` provides the Redis client wiring, used by the session locker and (today) the approval bridge.
- No Postgres-advisory-lock primitives exist in the codebase.
- The `core/locks/` directory does not exist.
- The v0 `session-mgmt` spec defines the external mutex contract; that contract is frozen and remains the target this spec must continue to satisfy.

### What should change

A new `core/locks/` package introduces a `PostgresSessionLocker` that holds the session lock for the lifetime of a single Postgres transaction, using `pg_try_advisory_xact_lock`. The transaction itself owns the lock, so network drop or process death rolls back the transaction and releases the lock — there is no stale-lock window and no TTL-expiry failure mode.

Specifically:

- New file `core/locks/session_locker.go` implements `PostgresSessionLocker.Acquire(ctx, sessionID) (release func(), err error)`. The function begins a transaction, calls `SELECT pg_try_advisory_xact_lock($1)` with `key = fnv64a("session:" + sessionID)`, returns `ErrLockHeld` on a held lock, and otherwise returns a `release` closure that commits the transaction.
- A shared `LockHolder` struct is defined here (and consumed by `v1-resource-locks`).
- A new feature flag `--session-lock-backend=postgres|redis` selects the implementation at startup, default `postgres` in v1.
- Both backends pass an identical integration test suite while both are present.
- The Redis backend remains in tree under the flag until two weeks of pilot use have elapsed on the Postgres backend; only then is it deleted (post-v1 cleanup).
- The release function must be safe to call exactly once; double-call is a programming error.

### Constraints

- Go 1.22+; `pgx/v5` (the existing Postgres driver in the codebase).
- `pg_try_advisory_xact_lock` only — no session-level advisory locks (those do not release on process death, defeating the point of the migration).
- Resource-key hashing convention (`fnv64a("session:" + sessionID)`) must be consistent with what `v1-resource-locks` uses for its own Postgres-advisory-lock keys so namespaces do not collide.
- Default `--session-lock-backend=postgres` in v1 so `make demo` exercises the new path by default.
- Out of scope: the resource-keyed `LockManager` interface (`v1-resource-locks`), audit-log schema changes (`v1-identity-model`), removal of Redis from the approval path (`v1-approval-checkpoint`), removal of the Redis backend itself (post-v1 cleanup), etcd or other alternative substrates (v3 risk-mitigation only).

### Upstream / Downstream

- **Upstream**: v0 `session-mgmt` (defines the per-session mutex contract).
- **Downstream**: `v1-resource-locks` (reuses the Postgres-advisory-lock primitive and the `LockHolder` struct); `v1-benchmark` (scenarios 04/05 measure lock contention on this substrate).

### Reference

Full design rationale: `v1.md` §Track 4 — Lock substrate migration. Threat-model context: `docs/threat-model.md` (Track 5a, to be written ahead of implementation).

## Requirements
<!-- Will be generated in /kiro-spec-requirements phase -->
