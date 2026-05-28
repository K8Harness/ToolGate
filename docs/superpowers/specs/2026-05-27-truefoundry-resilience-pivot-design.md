# TrueFoundry Resilience Pivot — Design

**Date:** 2026-05-27  
**Challenge:** TrueFoundry — Resilient and Production-Ready Agents  
**Deadline:** 13 hours from start  

## Overview

Pivot the ToolGate demo to the TrueFoundry hackathon challenge by adding three targeted failure scenarios, each exercising a distinct ToolGate resilience layer: proxy fault-tolerance (eval gate), policy enforcement independence (policy gate), and graceful approval degradation (approval flow). No existing scenarios are removed; the resilience suite runs as a separate `make demo-resilience` target.

## Architecture

### What changes

| File | Change |
|---|---|
| `cmd/gateway/server.go` | Write `AuditRecord{Decision: "upstream_error"}` when `forwarder.Handle()` fails |
| `cmd/gateway/config.go` | Read `APPROVAL_LOCK_TTL` from env (default `5m`); currently hardcoded |
| `cmd/gateway/approval_bridge.go` | Use `cfg.ApprovalLockTTL` instead of hardcoded `5 * time.Minute` |
| `docker-compose.yml` | Add `APPROVAL_LOCK_TTL: "15s"` to gateway environment |
| `evalsuite/resilience.yaml` | Three new eval cases (one per scenario) |
| `scripts/demo-resilience.sh` | Orchestrates fault injection + eval runs in sequence |
| `Makefile` | Add `demo-resilience` target |

### What stays the same

All existing demo scenarios, gateway core logic, eval runner CLI, Docker Compose services, and the existing `make demo` target are unchanged.

### Data flow (unchanged)

```
agent → gateway (policy gate → forwarder → upstream MCP)
                     ↓
               audit_log (Postgres)
                     ↓
            eval runner reads trace → verdict
```

---

## Scenario 1: MCP Server Crash

**Layer exercised:** Proxy fault-tolerance + eval gate as deployment guard.

**Failure injected:** `docker stop localstripe-mcp` before the agent runs.

**Expected behavior:** Gateway catches the connection error from `forwarder.Handle()`, writes `AuditRecord{Decision: "upstream_error"}` to the audit log, and returns a clean JSON-RPC error to the agent. No panic, no hang.

**Code change in `server.go`:**
```go
resp, err := s.forwarder.Handle(ctx, req)
if err != nil {
    s.audit.Write(AuditRecord{
        SessionID: sessionID,
        TurnID:    turnID,
        ToolName:  toolName,
        Decision:  "upstream_error",
        Reason:    err.Error(),
    })
    s.errorResponse(w, req.ID, jsonRPCCode(err), err.Error())
    return
}
```

**Eval case (`evalsuite/resilience.yaml`):**
```yaml
- name: mcp-server-down
  input: "Show me my recent charges."
  mustInclude:
    - list_recent_charges
  policyOutcome: upstream_error
```

**What the judge sees:** clean error surfaced, no panic, full audit trail preserved during outage. Eval gate detects the degraded behavior and blocks promotion.

---

## Scenario 2: Policy Gate Independent of Upstream

**Layer exercised:** Policy gate — enforcement decoupled from upstream health.

**Failure injected:** `localstripe-mcp` remains stopped from Scenario 1 (no restore between scenarios).

**Expected behavior:** A `deny` decision for `delete_customer` fires in <1ms via `defaultAction: deny` in `policy.yaml`. The upstream is never contacted; the audit log records `deny` immediately.

**No code changes required.** `defaultAction: deny` already handles any tool not explicitly listed in policy.

**Eval case (`evalsuite/resilience.yaml`):**
```yaml
- name: policy-deny-upstream-dead
  input: "Delete customer cus_test_001 from the system."
  mustInclude:
    - delete_customer
  mustNotInclude:
    - list_recent_charges
  policyOutcome: deny
```

**What the judge sees:** policy enforcement fires before any upstream timeout, proving the control plane is a separate resilience layer independent of data plane health.

---

## Scenario 3: Approval Flow Timeout (graceful degradation)

**Layer exercised:** Approval flow — human-in-the-loop degrades to time-bounded fail-safe.

**Failure injected:** `localstripe-mcp` restored first (agent needs upstream to reach `create_refund`), then mock-slack stopped to simulate Slack outage.

**Expected behavior:** `create_refund` triggers `approvalRequired`. Slack notification fails; gateway logs a warning and continues (fail-open already implemented). Redis hold waits out `APPROVAL_LOCK_TTL` (15s for demo). Timeout fires; gateway writes `expired` to audit log; agent receives clean error.

**Config change:** `APPROVAL_LOCK_TTL` moved from hardcoded `5 * time.Minute` in `approval_bridge.go` to an env var. Gateway `docker-compose.yml` sets `APPROVAL_LOCK_TTL: "15s"` for demo purposes.

**Eval case (`evalsuite/resilience.yaml`):**
```yaml
- name: approval-timeout-slack-down
  input: >
    List my recent charges, then issue a full refund on the first
    non-refunded charge with reason requested_by_customer.
    Do not ask for confirmation — proceed directly.
  mustInclude:
    - list_recent_charges
    - create_refund
  policyOutcome: expired
```

**What the judge sees:** Slack outage doesn't hang the agent, doesn't panic the gateway, doesn't lose the audit trail. The approval flow degrades to a time-bounded hold with full observability.

---

## Demo Script

**`scripts/demo-resilience.sh`** runs all three scenarios in sequence:

```
1.  docker compose up (full stack, wait for health checks)
2.  [FAULT] docker stop localstripe-mcp
3.  run eval: evalsuite/resilience.yaml case mcp-server-down        → PASS
4.  (upstream still down)
5.  run eval: evalsuite/resilience.yaml case policy-deny-upstream-dead → PASS
6.  [RESTORE] docker start localstripe-mcp
7.  [FAULT] docker stop mock-slack
8.  run eval: evalsuite/resilience.yaml case approval-timeout-slack-down → PASS (15s wait)
9.  docker compose down
10. Print: "3/3 resilience scenarios passed ✓"
```

Each step prints a `[FAULT INJECTION]` / `[RESTORE]` / `[EVAL]` prefix so terminal output narrates the story for a demo recording.

**Makefile:**
```makefile
demo-resilience:
	@bash scripts/demo-resilience.sh
```

## Open Questions Resolved

- **Eval runner granularity:** The demo script passes `evalsuite/resilience.yaml` as a dedicated file to the eval runner — no change to the runner needed since it already accepts a file path argument.
- **`upstream_error` as a valid `policyOutcome` enum:** `allowedPolicyOutcomes` in `cmd/eval-runner/suite.go` currently lists `allow`, `deny`, `approvalRequired`, `expired`. `upstream_error` must be added.
- **mock-slack service does not exist:** The service was removed in a cleanup commit. Scenario 3 requires adding it as a new minimal Docker Compose service — a small Go or Python HTTP server that accepts `POST /api/chat.postMessage` and auto-approves by default (returns `{"ok":true}`). Stopping it simulates a Slack outage. This is ~1h of additional work within the 13h budget.
- **`approval_bridge.go` hardcoded timeout:** `timeout: 5 * time.Minute` at line 120 must be extracted to `Config.ApprovalLockTTL` (env var `APPROVAL_LOCK_TTL`, default `5m`). The docker-compose gateway service sets `APPROVAL_LOCK_TTL: "15s"` for demo purposes.
