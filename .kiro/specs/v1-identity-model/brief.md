# Brief: v1-identity-model

## Problem

The v0 gateway treats the agent as the identity. Every audit row says "this call came from this agent" — but the v1 threat model (v1.md §Track 5a) draws a distinction between **actor** (the agent process) and **principal** (the human or system on whose behalf the agent acts). Without the principal, the audit log can't answer basic compliance questions like "did Sarah-the-customer-success-rep ever issue a refund over $1000?" or "did this agent-version ever act on behalf of a principal who later turned out to be compromised?" These are baseline audit questions and the v0 schema has no way to answer them.

Separately, the threat model commits to "validate, not generate" for idempotency keys. The audit schema needs a column to record the client-provided key when present. The gateway never reads this column at request time — it exists for offline duplicate-detection at audit-review time. Shipping the data shape now (with v1) avoids a second schema migration when v2 turns on duplicate-detection.

## Current State

- The Python SDK `ToolGateClient` constructor takes `gateway_url` only; no identity parameters
- `cmd/gateway/server.go` `handleMCPPost` extracts `Mcp-Session-Id` and turn ID; no identity headers parsed
- `core/mcp/types.go` defines context keys for session and turn but not principal/agent
- `audit_log` table has v0 schema; no `principal`, `agent_id`, `agent_version`, or `idempotency_key` columns
- `core/policy/` parser does not recognize a `requirePrincipalScope` clause

## Desired Outcome

- Python SDK `ToolGateClient` constructor **requires** `principal`, `agent_id`, `agent_version`; missing → constructor raises before any request is sent
- SDK attaches `X-ToolGate-Principal`, `X-ToolGate-Agent-Id`, `X-ToolGate-Agent-Version` on every request (in addition to existing `Mcp-Session-Id` + optional `X-Mcp-Turn-Id`)
- Gateway `handleMCPPost` extracts the three headers and stores them via `core/mcp/types.go` context keys (`ContextKeyPrincipal`, `ContextKeyAgentID`, `ContextKeyAgentVersion`)
- Missing `X-ToolGate-Principal` → `-32600 invalid request` naming the missing header (no silent default, no `anonymous` fallback)
- `audit_log` gains 4 columns: `principal TEXT NOT NULL`, `agent_id TEXT NOT NULL`, `agent_version TEXT NOT NULL`, `idempotency_key TEXT` (nullable), plus 3 indexes: `idx_audit_log_principal(principal, created_at DESC)`, `idx_audit_log_agent_version(agent_id, agent_version, created_at DESC)`, and a partial `idx_audit_log_idempotency(principal, idempotency_key) WHERE idempotency_key IS NOT NULL`
- Existing v0 rows backfilled with literal `"v0-unknown"` for the three NOT NULL identity columns; `idempotency_key` left NULL for v0 rows
- Migration runs at gateway startup, one-shot + idempotent + logged; failure → gateway refuses to start
- Policy YAML accepts optional `requirePrincipalScope: "<scope>"` clause; v1 validates schema + records the declared scope in the audit row but **does not enforce** (placeholder shipping the data shape for v2)
- v0 demo agent updated in lockstep with the identity-header requirement

## Approach

Update the Python SDK constructor and header attachment. Add header parsing + context-key plumbing in `handleMCPPost`. Write the Go migration (in `cmd/gateway/db.go` or a new `migrations.go`) that `ALTER TABLE`s `audit_log`, backfills v0 rows, and creates the three indexes. Add policy parser support for `requirePrincipalScope` (validate + record; no enforcement). Update the demo agent in the same change set — there is no v0-SDK production cohort to support.

## Scope

- **In**: Python SDK constructor + header attachment; gateway header parsing + new context keys in `core/mcp/types.go`; `audit_log` migration + backfill + 3 indexes; demo-agent update; `requirePrincipalScope` policy field (schema validation + audit recording, no enforcement); migration tests.
- **Out**: Principal-scope enforcement at request time (v2 multi-tenant work); idempotency-key generation, caching, or equivalence (gateway never generates); the idempotency-key validation logic at request time (that's `v1-resource-locks`; this spec only owns the audit column); a `tenant` column (out per v1 guardrails — single-tenant only); TypeScript SDK (v2+); OAuth-passthrough credentials (v2).

## Boundary Candidates

- SDK construction-time validation vs. server-side header validation (both — defense in depth; SDK fails fast, server is the authoritative gate)
- Audit migration timing (gateway startup, before listening on the port; one-shot + idempotent)
- Backfill literal (`"v0-unknown"` per v1.md; the exact string is mandated)
- Policy parser surface (`requirePrincipalScope` accepted + validated + recorded; enforcement deferred to v2)

## Out of Boundary

- Principal-scope enforcement at request time (v2)
- Multi-tenant `tenant` column / isolation primitives (v1 single-tenant)
- TypeScript SDK (v2+)
- Idempotency-key generation (explicit non-goal; v1.md DoD #6)
- Idempotency-key validation at request time (`v1-resource-locks` owns this; this spec just owns the column)
- Approval-ticket schema (`v1-approval-checkpoint`; this spec provides the audit/identity infrastructure that spec depends on)

## Upstream / Downstream

- **Upstream**: v0 `policy-gate` (audit_log schema + policy parser); v0 `bare-proxy` (the Python SDK and `handleMCPPost` header parsing)
- **Downstream**: `v1-resource-locks` (writes `idempotency_key` to the audit row when present); `v1-approval-checkpoint` (the `approval_tickets` row will record principal via context); `v1-benchmark` (scenario tooling needs valid identity headers to exercise the gateway)

## Existing Spec Touchpoints

- **Extends**: none — v0 `policy-gate` and `bare-proxy` are frozen; this spec adds a new identity layer atop them
- **Adjacent**: `v1-resource-locks` (independently extends the policy parser); `v1-approval-checkpoint` (depends on principal being in context)

## Constraints

- Python SDK target unchanged (LangGraph compatible)
- Header names: `X-ToolGate-Principal`, `X-ToolGate-Agent-Id`, `X-ToolGate-Agent-Version` — exact casing per v1.md
- Migration: one-shot, idempotent, runs at gateway startup before port-listen; failure → gateway refuses to start
- Backfill literal: `"v0-unknown"` (exact string)
- Indexes: `idx_audit_log_principal`, `idx_audit_log_agent_version`, `idx_audit_log_idempotency` (partial, `WHERE idempotency_key IS NOT NULL`)
- Missing principal at request time → `-32600` invalid request; no silent default
- `requirePrincipalScope` clause: schema-validated + audit-recorded but not enforced; v2 will turn enforcement on without a second migration
