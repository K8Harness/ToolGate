# Requirements Document

## Introduction

The bare-proxy forwards every MCP tool call unconditionally. Production AI agents need deterministic allow / deny / approval-required decisions enforced by the gateway — not by the agent — before any call reaches the upstream. Without policy interception, a refund call for any amount passes through, a delete call is never blocked, and there is no audit record. This slice adds an in-process YAML policy engine that evaluates every `tools/call` against a loaded `AgentPolicy` before forwarding, writes every decision durably to a Postgres audit log, and inserts a stub ticket row for approval-required cases.

## Boundary Context

- **In scope**: YAML `AgentPolicy` loading and validation at startup; synchronous policy evaluation on every `tools/call`; allow / deny / approval-required decision enforcement; per-turn tool-call budget tracking; durable audit logging of every decision; ticket stub row insertion for approval-required decisions; Postgres schema for `audit_log` and `ticket` tables; Postgres connection configuration.
- **Out of scope**: Approval notification, routing, or resume (slice 4); Redis session locking (slice 3); rate limits beyond per-turn call-count budget; PII redaction; Rego / CEL policy backends; policy hot-reload / file-watching.
- **Adjacent expectations**: This slice inserts `ticket` rows that slice 4 (approval-flow) will read and act on — the row schema must accommodate that future read without requiring a breaking migration. The bare-proxy pipeline (slice 1) must be running for this slice's handler to be registered.

---

## Requirements

### Requirement 1: AgentPolicy Configuration Loading

**Objective:** As a gateway operator, I want the gateway to load tool-call policies from a YAML file at startup, so that I can configure allow / deny / approval rules without code changes.

#### Acceptance Criteria

1. The Gateway shall load an `AgentPolicy` YAML configuration from a file path provided via environment variable at startup.
2. If the policy file path is not configured, the Gateway shall use a default file path and log a notice indicating the default is in use.
3. When the policy YAML file cannot be found at the configured path, the Gateway shall refuse to start and emit an error identifying the missing file path.
4. When the policy YAML is syntactically invalid or contains unrecognized fields, the Gateway shall refuse to start and emit an error identifying the parse failure.
5. The `AgentPolicy` YAML shall support: an ordered list of rules (each specifying a tool name, an action, and optional additional match conditions), a `budgets` section with a `maxToolCallsPerTurn` integer, and a `defaultAction` field (`allow` or `deny`) applied when no rule matches.

---

### Requirement 2: Non-Tool-Call Request Passthrough

**Objective:** As an MCP client, I want the gateway to forward all non-tool-call JSON-RPC requests without policy evaluation, so that protocol handshake and capability negotiation are unaffected.

#### Acceptance Criteria

1. When a JSON-RPC request with a method other than `tools/call` is received, the Gateway shall not apply policy evaluation and shall forward the request to the upstream MCP server without modification.

---

### Requirement 3: Policy Evaluation on Tool Calls

**Objective:** As a gateway operator, I want every `tools/call` to be evaluated against the loaded policy before it is forwarded, so that no tool invocation bypasses enforcement.

#### Acceptance Criteria

1. When a `tools/call` JSON-RPC request is received, the Gateway shall evaluate it against the loaded `AgentPolicy` before forwarding it upstream.
2. While evaluating a `tools/call` request, the Gateway shall have access to the session ID and turn ID associated with the request.
3. The Gateway shall evaluate rules in the order they are listed in the policy file; the first matching rule determines the decision.
4. When no rule matches a `tools/call` request, the Gateway shall apply the `defaultAction` defined in the `AgentPolicy`.

---

### Requirement 4: Allow Decision

**Objective:** As an AI agent, I want approved tool calls to be forwarded transparently, so that policy enforcement is invisible on the happy path.

#### Acceptance Criteria

1. When policy evaluation produces an `allow` decision for a `tools/call` request, the Gateway shall forward the request to the upstream MCP server and return its response to the caller without modification.

---

### Requirement 5: Deny Decision

**Objective:** As a gateway operator, I want denied tool calls to be rejected immediately with a structured error, so that agents receive a deterministic signal and the upstream is never reached.

#### Acceptance Criteria

1. When policy evaluation produces a `deny` decision for a `tools/call` request, the Gateway shall return a JSON-RPC error response with code `-32001` to the caller without forwarding the request upstream.
2. When a `tools/call` request is denied, the Gateway shall not contact the upstream MCP server.

---

### Requirement 6: Approval-Required Decision

**Objective:** As a gateway operator, I want high-risk tool calls to be held in a pending state rather than forwarded or rejected outright, so that a human approver can later resume or cancel them.

#### Acceptance Criteria

1. When policy evaluation produces an `approvalRequired` decision for a `tools/call` request, the Gateway shall return a synthetic response to the caller indicating the request is pending, without forwarding it upstream.
2. When a `tools/call` request enters approval-required state, the Gateway shall insert a ticket record that identifies the held request and its context.
3. The Gateway shall not forward an approval-required `tools/call` to the upstream MCP server.

> **Boundary note**: Actual approval notification, approval-state polling, and request resumption are owned by slice 4 (approval-flow). This slice only inserts the ticket row.

---

### Requirement 7: Per-Turn Tool-Call Budget

**Objective:** As a gateway operator, I want to cap how many tool calls an agent can make within a single turn, so that runaway agents cannot exceed defined usage limits.

#### Acceptance Criteria

1. The Gateway shall track the number of `tools/call` requests received for each active session and turn combination.
2. If the number of `tools/call` requests for a given session and turn has reached the configured `maxToolCallsPerTurn` limit, the Gateway shall deny any subsequent `tools/call` for that session and turn with a JSON-RPC error response with code `-32001`.
3. When a `tools/call` request is denied due to budget exhaustion, the Gateway shall record the denial in the audit log with a reason indicating the budget limit was reached.

---

### Requirement 8: Audit Logging

**Objective:** As a gateway operator, I want every tool-call policy decision recorded durably, so that I have a complete, attributable trace of all enforcement actions.

#### Acceptance Criteria

1. The Gateway shall write an audit record for every `tools/call` policy decision — allow, deny, approval-required, or budget-exceeded — before returning a response to the caller.
2. Each audit record shall contain: session ID, turn ID, tool name, tool arguments (as structured data), the decision taken, and the UTC timestamp of the decision.
3. If writing an audit record fails, the Gateway shall not fail or delay the in-flight request; it shall log the write failure locally.
4. The Gateway shall require a reachable Postgres instance to start; if a Postgres connection cannot be established at startup, the Gateway shall refuse to start.

---

### Requirement 9: Startup Validation

**Objective:** As a gateway operator, I want the gateway to fail fast at startup when required configuration or infrastructure is missing, so that misconfigurations are caught before any traffic is served.

#### Acceptance Criteria

1. When the Gateway starts with policy-gate active, it shall validate that a policy file path is configured (or default is accessible), the policy file parses without error, and a Postgres connection can be established — before accepting any incoming requests.
2. If any startup validation check fails, the Gateway shall exit with a non-zero status code and a human-readable error message identifying which check failed.
