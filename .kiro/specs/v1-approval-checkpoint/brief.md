# Brief: v1-approval-checkpoint

## Problem

The v0 approval flow in `cmd/gateway/approval_bridge.go` is a **synchronous hold**: a goroutine writes a Postgres ticket, posts to Slack, and blocks via `select` on a Redis Pub/Sub channel with a 5-minute timer, returning the resolved decision to the agent. This pattern has three failure modes:

1. **Process death loses approvals in flight** — the blocked goroutine disappears; the approval is silently dropped.
2. **Network reconnect breaks the resume** — the Pub/Sub channel is per-process; reconnecting to a different gateway replica loses the listener.
3. **Long approvals tie up gateway resources** — each pending approval costs a long-lived goroutine + open HTTP request, capping concurrency.

The v1 threat model commits to a **checkpointed** model: the gateway returns immediately with `pending(ticket_id)`, the agent polls (or receives an SSE callback) and retries with an approval-ticket header. Correctness lives in the Postgres ticket state, not in a process-bound goroutine.

A second concern is replay and payload-swap attacks: an agent that retries with a *different* MCP payload than the one originally approved (e.g. swapping a $50 refund ticket for a $50,000 refund retry) must be rejected explicitly.

## Current State

- `cmd/gateway/approval_bridge.go` implements the synchronous-hold pattern (writes ticket, posts to Slack, blocks on Pub/Sub)
- `cmd/gateway/slack_webhook.go` publishes to Redis Pub/Sub on resolution
- `approval_tickets` table has v0 schema (no `status`, `resolved_at`, `approver`, `resolution_ttl`, `original_call` columns)
- The approval path is the only remaining Redis dependency after `v1-lock-substrate` ships
- `v1-identity-model` has landed: principal is in request context; `audit_log` records principal

## Desired Outcome

- The policy gate (or a new dedicated approval handler) returns `pending(ticket_id)` **immediately** as a structured MCP response: `{ status: "pending_approval", ticketId, pollUrl, estimatedSeconds }` — no goroutine block.
- `approval_tickets` gains `status TEXT NOT NULL DEFAULT 'pending'`, `resolved_at TIMESTAMPTZ`, `approver TEXT`, `resolution_ttl TIMESTAMPTZ NOT NULL`, `original_call JSONB NOT NULL` + partial index on `(status, resolution_ttl) WHERE status = 'pending'`.
- New HTTP endpoint `GET /tickets/{id}` returns the ticket state (status / resolved_at / approver) for polling.
- Agent retries the original MCP call with `X-ToolGate-Approval-Ticket: tkt_...`. The policy engine validates the ticket is `approved`, fresh (TTL default 1 hour), unconsumed (single-use), and that the retry payload matches the original (SHA-256 of canonicalized request). On match → forward without re-routing to approval. On mismatch → `-32603` "approval ticket does not match request". On replay → denial.
- Optional SSE callback path: an open `GET /mcp` channel receives a server-initiated `notifications/approval/resolved` event. This is a latency optimization; correctness lives in ticket state.
- Slack webhook handler updates the ticket row directly (no Pub/Sub): `UPDATE approval_tickets SET status = $1, resolved_at = NOW(), approver = $2 WHERE ticket_id = $3 AND status = 'pending' RETURNING *` — the `WHERE` + `RETURNING` clauses provide webhook-retry idempotency.
- Background sweeper goroutine scans pending tickets past `resolution_ttl` every 10s and marks them `status = 'timeout'`. Uses the partial index.
- `cmd/gateway/approval_bridge.go` no longer contains a `select` on a Redis Pub/Sub channel (DoD #7).
- `make demo` gains a fifth scenario: a refund approved asynchronously (pending → approved → retry succeeds) — DoD #11.

## Approach

Refactor `approval_bridge.go` (or replace it) so the policy gate writes the ticket, posts to Slack, and returns `pending(ticket_id)` synchronously — no blocking goroutine. Migrate the `approval_tickets` schema at gateway startup. Add the `GET /tickets/{id}` endpoint. Add `X-ToolGate-Approval-Ticket` header recognition in the existing pipeline; on match, validate freshness + single-use + payload-hash + retrieve the recorded policy decision; forward. Replace the Slack webhook's Pub/Sub publish with a direct row update. Spin a sweeper goroutine in `main.go` on a 10s tick. Add the new demo scenario script. Implement the 5 integration tests from v1.md §Testing strategy (pending→approved, pending→denied, pending→timeout, integrity, replay).

## Scope

- **In**: Synchronous → checkpointed approval refactor; `approval_tickets` schema migration + partial index; `GET /tickets/{id}` endpoint; `X-ToolGate-Approval-Ticket` retry path; payload-hash validation (SHA-256 canonicalized); single-use ticket consumption; sweeper goroutine; Slack webhook direct update (no Pub/Sub); SSE callback path (optional optimization); new demo scenario; 5 integration tests.
- **Out**: Removal of the `cmd/gateway/redis.go` file (Redis remains optional infrastructure for rate-limit buckets and metrics counters; only the *approval* dependency on Redis is removed); changes to the Slack notification *content* (still uses the v0 Block Kit format); approval channels beyond Slack (email, Teams — v2+); verification-token issuance (out per v1 guardrails); multi-approver / quorum workflows.

## Boundary Candidates

- Ticket state machine vs. retry pipeline (separate: state machine in `cmd/gateway/ticket.go`; retry recognition as a pre-check in the policy gate)
- Sweeper goroutine ownership (lives in `main.go`, started post-migration, uses the partial index)
- Payload-canonicalization algorithm (must be deterministic across replays; SHA-256 of JSON with sorted keys + UTF-8 normalization)
- SSE callback as optimization vs. correctness (correctness lives in ticket state; SSE is "fast path"; can ship behind a flag if priorities demand)

## Out of Boundary

- Removal of Redis from the codebase entirely (Redis remains optional)
- Migration of v0 `approval_tickets` rows (v0 ticket schema is a subset; new columns added with defaults or NULL; no row rewrite needed beyond column-add)
- Approval channels beyond Slack
- Multi-approver / quorum approval
- Verification-token issuance

## Upstream / Downstream

- **Upstream**: `v1-identity-model` (principal must be in request context; `audit_log` records principal alongside ticket creation); v0 `approval-flow` (provides the existing ticket + Slack + webhook infrastructure this spec reshapes)
- **Downstream**: `v1-benchmark` (scenario 06 exercises the pending-approval path)

## Existing Spec Touchpoints

- **Extends**: none — v0 `approval-flow` is frozen; this spec replaces its synchronous-hold implementation with the checkpointed pattern
- **Adjacent**: `v1-identity-model` (provides the principal context the ticket row records)

## Constraints

- DoD #7: `cmd/gateway/approval_bridge.go` no longer contains a `select` on a Redis Pub/Sub channel
- DoD #8: sweeper runs and marks expired tickets `timeout`
- DoD #11: `make demo` fifth scenario (pending → approved → retry succeeds) passes end-to-end
- Single-use ticket: first retry consumes; second retry → denial
- Payload-match validation: SHA-256 of canonicalized request; mismatch → `-32603` "approval ticket does not match request"
- TTL default: 1 hour; configurable via policy `approvalRequired.timeoutSeconds`
- Sweeper period: 10 seconds (cheap; uses partial index)
- Migration: one-shot, idempotent, gateway startup before port-listen; failure → gateway refuses to start
- Webhook handler idempotency: `WHERE status = 'pending'` + `RETURNING` (handles retried webhook deliveries safely)
