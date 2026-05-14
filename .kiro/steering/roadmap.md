# Roadmap

## Overview

AgentPlane is a two-layer control plane for AI agents in production: an MCP policy gateway that enforces per-call rules at runtime, and an eval-gated deployment gate that blocks promotion of a new agent version until a user-defined eval suite passes a numeric threshold.

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

## Specs (dependency order)

- [ ] bare-proxy — Plain MCP proxy over SSE: SessionID generation, JSON-RPC forwarding, no policy. Dependencies: none
- [ ] policy-gate — YAML policy engine in-process: allow/deny/approvalRequired predicates + Postgres audit log. Dependencies: bare-proxy
- [ ] session-mgmt — Redis session mutex + RWLock registry: same-session turn serialization, within-turn read/write concurrency. Dependencies: policy-gate
- [ ] approval-flow — Slack approval bridge: Postgres ticket table, Slack Block Kit notification, Redis pub/sub resume, 5-minute timeout. Dependencies: session-mgmt
- [ ] eval-gate — EvalSuite YAML runner CLI + Docker Compose + fake MCP servers + support-refund demo agent. Dependencies: approval-flow
