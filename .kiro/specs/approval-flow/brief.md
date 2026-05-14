# Brief: approval-flow

## Problem

For tool calls that cannot be safely allowed or denied by policy alone (e.g., a $12,000 refund), a human must review and decide before the action executes. Without an approval flow, the gateway either blocks all large refunds (bad UX, no escape hatch) or lets them through unchecked (unsafe). The `ticket` table from slice 2 exists but nothing acts on it.

## Current State

`session-mgmt` (slice 3) provides Redis session/turn locking. `policy-gate` (slice 2) writes a `ticket` row and returns a hold response for `approvalRequired` cases but does not notify anyone or wait for a decision. The gateway Go routine handling the HTTP request does not yet "sleep" waiting for a resume signal.

## Desired Outcome

For a call routed to `approvalRequired`:

1. The gateway Go routine holds the HTTP connection open (pauses with a `select` on a channel)
2. A worker goroutine reads the `ticket` row and sends a **Slack Block Kit message** to the configured channel with: tool name, operation, arguments, session ID, approve/deny buttons
3. The manager clicks Approve or Deny → Slack sends a webhook POST to the gateway's `/slack/actions` endpoint
4. The webhook handler publishes a `resume:<ticket_id>` signal on a Redis Pub/Sub channel
5. The waiting Go routine wakes, reads the decision from the Postgres `ticket` row (updated to `approved` / `denied`)
6. **Approved**: forward the original call to the upstream MCP server, return result
7. **Denied**: return JSON-RPC error `-32001`
8. **Timeout** (5 minutes): return JSON-RPC error `-32001` with message `"approval timeout"`

## Approach

Synchronous hold strategy (as specified in v0.md): the Go routine handling the request uses `select` with a `time.After(5 * time.Minute)` and a channel wired to the Redis Pub/Sub subscriber. This keeps the HTTP connection alive and avoids polling.

Slack integration: Slack Block Kit message via Incoming Webhook (or Bot Token). The webhook listener is a new HTTP route on the gateway binary (`/slack/actions`).

## Scope

- **In**: Postgres `ticket` table update (status: pending → approved/denied, decision timestamp), Slack Block Kit notification (tool details, approve/deny buttons), `/slack/actions` webhook endpoint, Redis Pub/Sub publish on decision, `select`-based wait in request handler, 5-minute timeout with deny-on-timeout, session mutex TTL extension during approval wait
- **Out**: Email/PagerDuty/Teams approval channels, approval UI, multi-approver quorum, approval history dashboard, async approval (request returns immediately and resumes later) — that is the v1+ async architecture

## Boundary Candidates

- Approval notifier interface (initially Slack; designed as an interface so v1+ can swap in other channels)
- Decision poller vs. event-driven resume (this spec uses Redis Pub/Sub event-driven; the interface hides the mechanism)
- Timeout handling (configurable per-rule via `AgentPolicy.approvalRequired.timeoutSeconds`)

## Out of Boundary

- The async "fire and return" approval model — v0 uses synchronous hold only; document the limitation
- Approval state surviving a gateway restart — v0 does not handle this; a restart during an approval wait results in timeout on the next client retry

## Upstream / Downstream

- **Upstream**: session-mgmt Redis locking (slice 3), policy-gate `ticket` table (slice 2), Slack API
- **Downstream**: eval-gate (slice 5) demo requires this to be working for the "large refund → Slack approval" scenario

## Existing Spec Touchpoints

- **Extends**: policy-gate — activates the `ticket` rows that slice 2 creates but does not act on
- **Extends**: session-mgmt — must extend session mutex TTL during approval wait to prevent the lock from expiring mid-approval

## Constraints

- Slack webhook URL and signing secret must be configurable via environment variable (not hardcoded)
- The `/slack/actions` endpoint must verify the Slack request signature (HMAC-SHA256) before acting on the payload
- v0 uses synchronous hold — the HTTP connection stays open during approval. This limits approval timeout to practical HTTP timeout limits; document 5-minute max
- Approval decisions are written to Postgres before the Redis Pub/Sub signal is sent (durability before notification)
