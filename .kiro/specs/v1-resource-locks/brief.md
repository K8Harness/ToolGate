# Brief: v1-resource-locks

## Problem

The v0 concurrency model assumes intra-turn read/write ordering is the gateway's job. Three files implement this: `cmd/gateway/concurrency_guard.go` (per-turn read/write enforcement), `cmd/gateway/classifier.go` (read/write classifier), `cmd/gateway/turn_rwlock.go` (per-turn RWLock). The v1 threat model commits to **delegating intra-turn ordering to the agent framework (LangGraph)** and **owning cross-session resource-conflict prevention** instead. The current code mixes both responsibilities and does the wrong one well.

Separately, the v0 code has no place for idempotency keys. The threat model commits to **"validate, not generate"**: when policy declares `idempotency.required: true`, the gateway must check that the client provided a key (returning `-32602` on miss), pass it through byte-for-byte, and record it in audit — but never construct one. This is a load-bearing decision because the proxy has no source of logical-operation identity, and a self-generated key would either collapse on retry or collide on outwardly-identical-but-logically-distinct calls.

## Current State

- `cmd/gateway/concurrency_guard.go`, `classifier.go`, `turn_rwlock.go` + their `_test.go` files exist and are wired into the request pipeline
- The policy YAML schema includes `read-class` / `write-class` distinctions consumed by the classifier
- After `v1-lock-substrate` ships, `core/locks/session_locker.go` exists alongside the Postgres-advisory-lock primitive this spec reuses
- No resource-keyed locking; no YAML template extractor; no idempotency handling

## Desired Outcome

- `core/locks/manager.go` defines `LockManager` interface + Postgres advisory-lock implementation. Write locks use `pg_try_advisory_xact_lock`; read locks compose via a shared/exclusive lock count table; write locks block when any read or write lock is held on the same key.
- `core/locks/extractor.go` parses YAML `resourceKey` templates with a restricted `text/template` function set (`lower`, `default`, `coalesce`; no iteration, no conditionals)
- `cmd/gateway/resource_lock.go` pipeline stage between `PolicyGate` and `Forwarder`
- When policy declares `idempotency.required: true`, the stage validates client-provided key presence (returns `-32602` naming the missing field on miss) and **skips the resource lock** — the upstream API enforces idempotency semantics. The key flows byte-for-byte to the upstream call. No generation, no caching, no canonicalization.
- When no idempotency contract, the stage extracts the resource key and acquires the lock; release fires in a `defer` at the response-write site so it runs on all paths including panics
- All three deleted files are gone; policy YAML no longer references `read-class` / `write-class`; the module compiles with no references to `ConcurrencyGuard`, `Classifier`, `TurnRWLock`
- Property-based tests (`pgregory.net/rapid`, 10k iterations) verify the lock-manager invariants in v1.md §Testing strategy

## Approach

Build `LockManager` + extractor in `core/locks/` reusing the Postgres-advisory primitives landed by `v1-lock-substrate`. Add `cmd/gateway/resource_lock.go` with the pseudocode from v1.md §Track 1. Wire it into the existing handler chain between `PolicyGate` and `Forwarder`. Delete the three legacy files + their tests + the `read-class`/`write-class` schema fields. Run the property-based test suite at 10k iterations in CI.

## Scope

- **In**: `core/locks/manager.go` + `manager_test.go` + `core/locks/extractor.go` + `extractor_test.go` + `doc.go`; `cmd/gateway/resource_lock.go`; idempotency presence-validation in the new pipeline stage; deletion of `concurrency_guard.go`/`classifier.go`/`turn_rwlock.go` + their tests; policy YAML schema migration (drop `read-class`/`write-class`, add `concurrency.{mode, resourceKey}` and `idempotency.{required, field}`); property-based tests at 10k iterations.
- **Out**: Idempotency-key generation, caching, or canonicalization (DoD #6 forbids any such code path in `core/locks/` or `cmd/gateway/`); the session locker (`v1-lock-substrate`); audit schema changes (`v1-identity-model`); the `requirePrincipalScope` policy field (`v1-identity-model`); the approval ticket schema (`v1-approval-checkpoint`).

## Boundary Candidates

- `LockManager` interface vs. Postgres implementation (interface in `core/locks/manager.go`; impl alongside)
- YAML extractor template language scope (whitelisted functions only, no Turing-completeness)
- Pipeline stage placement (between `PolicyGate` and `Forwarder`; `defer` at response-write for release)
- Schema validation timing (parse error at policy-load → gateway refuses to start; no silent fallback)
- Idempotency check site (early in the pipeline stage, before resource-key extraction, since presence/absence determines whether to lock at all)

## Out of Boundary

- Idempotency-key generation, caching, equivalence semantics (gateway does none of this; DoD #6)
- The `audit_log.idempotency_key` column itself (data shape owned by `v1-identity-model`; this spec only writes to the column when the column exists)
- Verification-token issuance (deferred to v2; schema accepts `verificationToken:` clauses but the gateway logs and ignores them)
- Multi-tenant isolation (the `LockHolder.Tenant` field exists but is unused in v1)
- Approval flow (`v1-approval-checkpoint`)

## Upstream / Downstream

- **Upstream**: `v1-lock-substrate` (Postgres-advisory-lock primitive + shared `LockHolder` struct); v0 `policy-gate` (policy load/parse path this spec extends with the new `concurrency` + `idempotency` blocks)
- **Downstream**: `v1-identity-model` (audit row records the extracted resource key + idempotency key alongside principal); `v1-benchmark` (scenarios 04/05 exercise contended/uncontended resource locks)

## Existing Spec Touchpoints

- **Extends**: none — v0 `policy-gate` and `session-mgmt` are frozen; this spec replaces the v0 read/write classifier and reshapes the policy YAML schema
- **Adjacent**: `v1-lock-substrate` (shared `core/locks/` directory); `v1-identity-model` (extends the policy parser independently)

## Constraints

- Go 1.22+; `text/template` standard library only (restricted function set; no plugins)
- Postgres `pg_try_advisory_xact_lock` for write locks; shared/exclusive count table for read locks
- Resource-key hashing: `fnv64a` of `tenant + "|" + resource_key + "|" + mode`; must be deterministic and consistent with `v1-lock-substrate`'s hashing convention
- **Idempotency: the gateway never generates a key.** Validation is presence-only. Passthrough is byte-for-byte. DoD #6 is a release blocker — any code path constructing an idempotency key in `core/locks/` or `cmd/gateway/` fails the release.
- Template parse failure at policy-load time → gateway refuses to start (no silent fallback)
- Missing required field at request time → `-32602` invalid params; call denied
- Property-based tests run at 10k iterations in CI (DoD #2)
