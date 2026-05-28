# Brief: v1-lock-substrate

## Problem

The v0 session mutex in `cmd/gateway/session_locker.go` uses Redis `SETNX` + 60-second TTL. This is the Kleppmann distributed-locking failure mode: if a turn outlasts the TTL, the lock expires while the holder still believes it's held, allowing two workers to interleave writes on the same session. The v1 threat model (v1.md §Track 5a) names this as a safety/availability gap that must close before any further architectural work — every downstream spec (`v1-resource-locks` especially) reuses the same lock primitive, so building it on a sound substrate matters.

## Current State

- `cmd/gateway/session_locker.go` — Redis SETNX + TTL implementation, used by every request that touches a session
- `cmd/gateway/redis.go` — Redis client wiring; used by the session locker and (today) the approval bridge
- No Postgres-advisory-lock primitives in the codebase
- `core/locks/` directory does not exist

## Desired Outcome

A new `core/locks/` package contains a `PostgresSessionLocker` that holds the session lock for the lifetime of a single Postgres transaction (`pg_try_advisory_xact_lock`). Network drop or process death rolls back the transaction, releasing the lock. There is no stale-lock window. A `--session-lock-backend=postgres|redis` flag selects the implementation (default `postgres` in v1). Both backends pass the same integration tests. Two weeks after v1 ships, the Redis backend is deleted.

## Approach

Implement `core/locks/session_locker.go` per the pseudocode in v1.md §Track 4: `tx, _ := pool.Begin(ctx)` → `pg_try_advisory_xact_lock($1)` with `key = fnv64a("session:" + sessionID)` → return a `release` closure that commits the transaction. On context cancel: rollback. Wire the feature flag in `cmd/gateway/config.go`. Run both backends in CI integration tests until the Redis backend is removed.

## Scope

- **In**: `core/locks/session_locker.go` (Postgres impl); a shared `LockHolder` struct definition that v1-resource-locks will also consume; `--session-lock-backend` flag; integration tests with both backends; documented migration path for operators.
- **Out**: Resource-keyed locking (`v1-resource-locks`); the `LockManager` interface (also `v1-resource-locks`); deletion of the Redis backend (deferred until two weeks of pilot use after v1 ships); etcd or other alternative substrates (v3 risk-mitigation only).

## Boundary Candidates

- Locker contract surface (the locker exposes `Acquire(ctx, sessionID) (release, err)` only; transaction ownership is internal)
- Configuration plumbing (flag → config struct → constructor selection)
- Hashing convention (`fnv64a("session:" + sessionID)` — must match what `v1-resource-locks` uses for the same primitive so collisions don't pile up)
- Tx-rollback path (rollback releases the lock; commit also releases it; both are correct under the advisory-lock semantic)

## Out of Boundary

- The resource-keyed `LockManager` interface (different shape — takes a `key`, `mode`, and holder)
- Audit-log schema changes (`v1-identity-model`)
- Removal of Redis from the approval path (`v1-approval-checkpoint`)
- Removal of the Redis backend itself (post-v1 cleanup)

## Upstream / Downstream

- **Upstream**: v0 `session-mgmt` (defines the per-session mutex contract this spec is replacing)
- **Downstream**: `v1-resource-locks` (reuses the Postgres-advisory-lock primitive); `v1-benchmark` (scenarios 04/05 measure lock contention on this substrate)

## Existing Spec Touchpoints

- **Extends**: none — v0 `session-mgmt` is frozen; this spec replaces its Redis-backed implementation under the same external contract
- **Adjacent**: `v1-resource-locks` shares the `core/locks/` directory and the Postgres-advisory-lock primitive

## Constraints

- Go 1.22+; `pgx/v5` (existing Postgres driver in the codebase)
- `pg_try_advisory_xact_lock` only — no session-level advisory locks (those don't release on process death)
- Feature flag must default to `postgres` in v1 so `make demo` runs the new path
- Both backends must pass identical integration tests until the Redis backend is removed
- The release function must be safe to call exactly once; double-call is a programming error, not a runtime panic
