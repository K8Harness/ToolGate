# Brief: policy-gate

## Problem

The bare proxy forwards every tool call unconditionally. In production, agents need deterministic allow/deny/approval decisions on every call — enforced by the gateway, not by the agent. Without policy interception, a refund call for any amount passes through, a delete call is never blocked, and there is no audit record of what happened.

## Current State

`bare-proxy` (slice 1) provides a working MCP proxy with a middleware-extensible forwarding pipeline. No policy evaluation exists. Postgres is not yet wired.

## Desired Outcome

The gateway evaluates every `tools/call` against a loaded `AgentPolicy` YAML before forwarding:

- **Allow**: request proceeds normally
- **Deny**: gateway returns JSON-RPC error `-32001` immediately without forwarding
- **ApprovalRequired**: request is held (returns a pending status); approval routing is implemented in slice 4

Every call decision (allow / deny / approval-pending) is written as a row in a Postgres `audit_log` table with: session ID, turn ID, tool name, operation, arguments (JSON), decision, timestamp.

## Approach

In-process YAML policy engine: load `AgentPolicy` at startup, parse rules into an in-memory structure, evaluate each incoming `tools/call` against the rules in O(rules) time. For `approvalRequired` cases, return a synthetic hold response and write a `ticket` row — the actual approval flow (Redis pub/sub, Slack) comes in slice 4.

Postgres schema introduced in this slice: `audit_log` table. Connection via `pgx` or `database/sql` + `lib/pq`.

## Scope

- **In**: YAML `AgentPolicy` loader (rules, budgets, defaultAction), in-process predicate evaluator (allow/deny/approvalRequired), budget tracking (maxToolCallsPerTurn), JSON-RPC `-32001` deny response, Postgres `audit_log` table + write on every decision, minimal `ticket` table stub (rows inserted, not acted on), Postgres connection config
- **Out**: Redis (any locking or pub/sub), Slack notification, actual approval resume flow, budget tracking beyond per-turn call count, PII redaction transform, Rego/CEL backends

## Boundary Candidates

- Policy loader (file-watching for hot-reload later)
- Predicate evaluator (pure function: `(rule, call) → decision`, easy to unit test)
- Audit writer (async vs sync — choose in design; async preferred to keep critical path clean)

## Out of Boundary

- Approval notification or resume — slice 4
- Redis-based session locking — slice 3
- Rate limits beyond tool-call budget — v1+

## Upstream / Downstream

- **Upstream**: bare-proxy middleware pipeline (slice 1), Postgres
- **Downstream**: session-mgmt (slice 3) will add Redis locking around the same call path; approval-flow (slice 4) will act on `ticket` rows written here

## Existing Spec Touchpoints

- **Extends**: bare-proxy — adds a middleware handler into the forwarding pipeline
- **Adjacent**: session-mgmt (slice 3) touches the same request lifecycle

## Constraints

- Policy evaluation must complete before the upstream forward happens (synchronous in critical path)
- Audit writes may be async (non-blocking) to meet the <10ms overhead target
- YAML policy schema must match the format specified in `ToolGate_revised (1).md` (rules, budgets, defaultAction: deny)
- No Rego in v0 — pure Go predicate evaluation against the YAML AST
