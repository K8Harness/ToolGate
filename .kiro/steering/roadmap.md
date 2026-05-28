# Roadmap

## Overview

ToolGate is a two-layer control plane for AI agents in production: an MCP policy gateway that enforces per-call rules at runtime, and an eval-gated deployment gate that blocks promotion of a new agent version until a user-defined eval suite passes a numeric threshold.

v0 targets a single runnable demo on Docker Compose — no Kubernetes, no Rego, no dashboard. The goal is a `make demo` that shows four scenarios end-to-end: a refund approved automatically with a verification token, a large refund routed to Slack for human approval, a delete call denied by policy, and a Slack message with PII redacted. All traces visible in OTel (Jaeger/Tempo).

The decomposition uses **vertical slices**: each spec produces a runnable, testable system. Later slices extend earlier code rather than replacing it, so integration risk surfaces early.

## Approach Decision

- **Chosen**: Vertical slices — each slice delivers end-to-end behavior on top of the previous one
- **Why**: Greenfield codebase; catching integration problems early (protocol mis-parse, Postgres schema mismatch, Redis locking edge cases) is more valuable than clean horizontal separation
- **Rejected alternatives**: Component-per-spec (horizontal) — clean ownership but nothing runnable until the final spec; harder to validate incrementally

## Scope

- **In**: MCP gateway (Go), YAML policy engine (in-process), Postgres session/turn/audit store, Redis mutex + pub/sub, Slack approval bridge, EvalSuite YAML runner, Python demo agent (LangGraph/CrewAI), fake MCP servers (Stripe, Zendesk, Slack), Docker Compose
- **Out**: Kubernetes CRDs and operator, Rego/CEL policy backend, web UI, multi-tenant isolation, JS/TS SDK, cloud deployment, baseline-relative eval gating, verification token issuance

## Constraints

- Go 1.22+ for the gateway and eval runner
- Python SDK target: LangGraph or CrewAI (whichever is simplest to wire for the demo)
- MCP protocol: Model Context Protocol (JSON-RPC over SSE, current spec)
- Docker Compose: single-machine dev/demo only — documentation must not suggest production use
- Policy in v0: static YAML string matching (no Rego yet)
- Approval in v0: synchronous hold via Go select + Redis pub/sub (5-minute timeout)

## Boundary Strategy

- **Why this split**: Each slice is independently testable; each boundary maps to a concrete demo scenario that can be verified before moving to the next
- **Shared seams to watch**: The gateway's internal plugin/handler interface is extended by slices 2, 3, and 4 — design it for extension from slice 1 to avoid rewrites

## v1 Phase (architectural refactor)

v1 ships **no new user-visible features**. It refactors v0 internals to match the architectural commitments in `project.md` revision 2: resource-keyed locking, principal-bearing identity, checkpointed approval, and a Postgres-advisory-lock substrate. The deployment substrate remains Docker Compose. The shipping artifact is a smaller, cleaner codebase — the value is in what is *removed and reshaped*, not in what is added. Full design: `v1.md`.

### v1 approach decision

- **Chosen**: Substrate-first dependency order — lock substrate → resource locks → identity → approval → benchmark; the threat-model doc seeds every spec
- **Why**: The new resource-lock manager reuses the same Postgres-advisory primitives as the session locker; building them once is cheaper than twice. Identity threads through audit + approval; defining its data shape before approval avoids a second schema migration. Benchmark lands last so it exercises the shipping system, not an intermediate state.
- **Rejected alternatives**: One combined `v1-locks` spec (would invite scope creep across a ~5-week chunk); identity-first ordering (the lock substrate has zero principal dependencies, so the holder field can use a placeholder string for one iteration at no cost).

### v1 scope additions

- **In**: `core/locks/` package (Postgres advisory locks; session locker + resource-keyed LockManager + YAML extractor); identity headers (`X-ToolGate-Principal`/`Agent-Id`/`Agent-Version`) threaded through SDK + gateway + audit + policy; checkpointed approval (`pending(ticket_id)` + poll/SSE retry + sweeper); idempotency validate-only (gateway never generates a key); threat-model doc; reproducible benchmark harness. **Deletes**: `turn_rwlock`, `classifier`, `concurrency_guard`, the synchronous approval hold, and the policy YAML `read-class`/`write-class` distinction.
- **Out** (v2+): OAuth-passthrough credentials, OPA/Rego, baseline-relative eval gating, tamper-evident audit log (Merkle chain), web UI, multi-tenant enforcement, TypeScript SDK, new MCP server integrations, new policy operations, verification-token issuance, Kubernetes.

### v1 constraints (additional)

- Postgres advisory locks (`pg_try_advisory_xact_lock`) as the single lock primitive across `core/locks/`
- Idempotency keys: **validated only, never generated** by the gateway; pass-through byte-for-byte; recorded in audit (`audit_log.idempotency_key`)
- Schema migrations run at gateway startup, one-shot + idempotent; failed migration → gateway refuses to start
- v0 demo agent updated in lockstep with the identity-header requirement (no v0-SDK backwards-compat path)
- After v1 ships, the Redis session-locker backend is removed within two weeks of pilot use

### v1 boundary strategy

- **Why this split**: Each v1 spec owns one architectural reshape from v1.md's tracks. Lock substrate ships before resource locks so the Postgres-advisory primitive lands once. Identity lands before approval so the ticket schema records principal without a second migration. Benchmark lands last so it exercises the actual shipping system.
- **Shared seams to watch**: `core/locks/` is co-owned by `v1-lock-substrate` (session_locker) and `v1-resource-locks` (manager + extractor); the interface boundary between them must be settled in #1's design phase. The `audit_log` migration is owned end-to-end by `v1-identity-model`, but `v1-resource-locks` writes to the new `idempotency_key` column and `v1-approval-checkpoint` reads principal from request context — both depend on `v1-identity-model` having defined the data shape correctly.

## Specs (dependency order)

- [x] bare-proxy — Plain MCP proxy over SSE: SessionID generation, JSON-RPC forwarding, no policy. Dependencies: none
- [x] policy-gate — YAML policy engine in-process: allow/deny/approvalRequired predicates + Postgres audit log. Dependencies: bare-proxy
- [x] session-mgmt — Redis session mutex + RWLock registry: same-session turn serialization, within-turn read/write concurrency. Dependencies: policy-gate
- [x] approval-flow — Slack approval bridge: Postgres ticket table, Slack Block Kit notification, Redis pub/sub resume, 5-minute timeout. Dependencies: session-mgmt
- [x] eval-gate — EvalSuite YAML runner CLI + Docker Compose + fake MCP servers + support-refund demo agent. Dependencies: approval-flow
- [ ] v1-lock-substrate — Postgres advisory-lock session locker (replaces Redis SETNX+TTL); `--session-lock-backend` feature flag; idempotent startup migration. Dependencies: none
- [ ] v1-resource-locks — `core/locks/` `LockManager` + Postgres impl + YAML resource-key extractor + `ResourceLockHandler` pipeline stage; idempotency validate-only gate; deletes `turn_rwlock`/`classifier`/`concurrency_guard`. Dependencies: v1-lock-substrate
- [ ] v1-identity-model — Python SDK `principal`/`agent_id`/`agent_version` constructor params + headers; gateway header parsing in `handleMCPPost`; `audit_log` schema migration (principal, agent_id, agent_version, idempotency_key + 3 indexes); `requirePrincipalScope` policy field (placeholder). Dependencies: none
- [ ] v1-approval-checkpoint — `pending(ticket_id)` response; `approval_tickets` schema extensions (status, resolved_at, approver, resolution_ttl, original_call); timeout sweeper goroutine; `X-ToolGate-Approval-Ticket` retry with single-use + payload-match validation; new fifth `make demo` scenario. Dependencies: v1-identity-model
- [ ] v1-benchmark — `bench/` directory; `docker-compose.bench.yml`; 6 scenario YAMLs; vegeta/k6 runner; committed markdown report; CI nightly schedule integration. Dependencies: v1-lock-substrate, v1-resource-locks, v1-identity-model, v1-approval-checkpoint

## Direct Implementation Candidates

- [ ] docs/threat-model.md — Single-page threat model. Content structure already specified in v1.md §Track 5a. Run first; every v1 spec references it. Add a link from README.md.
