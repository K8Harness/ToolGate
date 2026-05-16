# Research & Design Decisions: approval-flow

## Summary

- **Feature**: `approval-flow`
- **Discovery Scope**: Complex Integration
- **Key Findings**:
  - The pipeline's `(nil, nil)` continue-contract means `PolicyGateHandler` can block inline for the approval wait and return `(nil, nil)` on approval — the pipeline naturally continues to `UpstreamForwarder` with no forwarder injection or pipeline restructuring
  - Slack block_actions payloads arrive URL-encoded with a `payload` form field containing JSON; raw body must be read before any parsing for HMAC verification to work correctly
  - `go-redis/v9` is already present in the codebase; `client.Subscribe().Channel()` returns a Go channel that closes on connection loss — the nil-message case directly maps to the timeout fallback path (req 5.3)

---

## Research Log

### Slack Interactive Components Payload Format

- **Context**: Need to parse Slack manager's Approve/Deny click to extract ticket ID and decision
- **Sources**: Slack API block_actions interaction payload documentation
- **Findings**:
  - POST arrives as `application/x-www-form-urlencoded`; single field `payload` contains URL-encoded JSON
  - Top-level `type = "block_actions"` (not `interactive_message` — that is the legacy format)
  - Decision routing: `actions[0].action_id` (`"approval_approve"` / `"approval_deny"`)
  - Ticket ID carrier: `actions[0].value` (arbitrary string set when composing the Block Kit message)
  - No `callback_id` at root for block_actions — that belongs to the legacy payload type
  - `user.id` available for audit logging of `decision_by`
- **Implications**: Parse `payload` field after URL-decoding from raw body; route on `action_id`; extract ticketID from `value`

### Slack Request Signature Verification

- **Context**: Req 3.2–3.4 require HMAC-SHA256 verification with replay protection
- **Sources**: Slack verifying-requests-from-slack documentation
- **Findings**:
  - Base string format: `"v0:" + X-Slack-Request-Timestamp + ":" + rawBody`
  - HMAC key: Slack Signing Secret (from app's Basic Info panel)
  - Expected signature format: `"v0=" + hex(HMAC-SHA256(key, basestring))`
  - Compare with `X-Slack-Signature` header using constant-time comparison (`hmac.Equal`)
  - Raw body must be read before any form parsing (body reader is consumed)
- **Implications**: Buffer raw body first; timestamp check first (cheap) then HMAC check; use `hmac.Equal` not `==`

### Redis Pub/Sub API (go-redis/v9)

- **Context**: Need event-driven resume signal delivery from SlackWebhookHandler to waiting PolicyGateHandler
- **Sources**: go-redis/v9 pkg.go.dev documentation
- **Findings**:
  - `client.Subscribe(ctx, channel)` returns `*redis.PubSub`
  - `pubsub.Channel()` returns `<-chan *redis.Message`; closes when `pubsub.Close()` is called or connection drops
  - `client.Publish(ctx, channel, payload)` publishes to all current subscribers
  - Nil message received from a closed channel — select `case msg, ok := <-ch` where `!ok` indicates closure
  - No persistence: if no subscriber is active when signal is published, signal is lost
- **Implications**: Nil/closed channel case must be handled as a timeout fallback; always `defer pubsub.Close()`

### Existing Pipeline Handler Contract

- **Context**: Need to understand how to wire the approval hold without modifying the pipeline
- **Sources**: `/Users/henry/Programming/ToolGate/core/mcp/pipeline.go`, `handler.go`
- **Findings**:
  - `(nil, nil)` → continue to next registered handler or terminal forwarder
  - `(*JSONRPCResponse, nil)` → halt pipeline, return response to client
  - `PolicyGateHandler` is the last `pipeline.Use()` middleware before the terminal `UpstreamForwarder`
  - Returning `(nil, nil)` from `PolicyGateHandler` on approval naturally calls `UpstreamForwarder.Handle(ctx, req)` with the original in-scope `req`
- **Implications**: No forwarder injection needed; eliminates a component from the design

### Existing TicketStore and TicketRecord

- **Context**: Need to understand the current ticket API before extending it
- **Sources**: `/Users/henry/Programming/ToolGate/cmd/gateway/ticket.go`
- **Findings**:
  - `TicketRecord{SessionID, TurnID, ToolName, Arguments, ExpiresAt}` — no ID field
  - `Insert(ctx, TicketRecord) (string, error)` — returns UUID as string
  - `approved`, `denied`, `expired` status values exist in the DB schema (policy-gate defined them)
  - No `UpdateStatus` or `GetByID` currently exists
- **Implications**: Add `UpdateStatus(ctx, id, status, decidedBy string) error`; idempotent (WHERE status='pending'); no GetByID needed since decision is in pub/sub payload

---

## Architecture Pattern Evaluation

| Option | Description | Strengths | Risks / Limitations | Decision |
|--------|-------------|-----------|---------------------|----------|
| Block inline in PolicyGateHandler | Block via `WaitForDecision`; return `(nil,nil)` on approve | No pipeline changes; original req stays in scope for free forwarding | Holds goroutine for 5 min; HTTP WriteTimeout must accommodate | **Selected** |
| Separate ApprovalInterceptor middleware | Wrap PolicyGateHandler; detect pending response; run approval hold | No changes to PolicyGateHandler | Requires parsing response body to detect "pending" — fragile | Rejected |
| Async resume (fire-and-return) | Return immediately; resume via callback when decision arrives | No long-held connections | Requires async request resumption infrastructure; explicitly out-of-scope for v0 | Rejected (v1+) |

---

## Design Decisions

### Decision: `(nil, nil)` continue-contract eliminates forwarder injection

- **Context**: On approval, the gateway must forward the original MCP request to the upstream server
- **Alternatives Considered**:
  1. Inject `UpstreamForwarder` into `PolicyGateHandler` and call it directly
  2. Return `(nil, nil)` from the handler on approval; let the pipeline continue naturally
- **Selected Approach**: Option 2 — return `(nil, nil)`; the pipeline calls the terminal forwarder
- **Rationale**: The original `*JSONRPCRequest` stays in scope throughout the blocking wait; returning `(nil, nil)` invokes the forwarder with no additional wiring
- **Trade-offs**: Subtle: the pipeline continues in the same goroutine that was blocked; context is still valid since the client is still connected
- **Follow-up**: Verify that the upstream forwarder's HTTP client timeout starts fresh from the point it's called (not from the start of the approval wait)

### Decision: Decision carried in pub/sub payload, not re-fetched from DB

- **Context**: After the resume signal arrives, `WaitForDecision` needs to know the decision
- **Alternatives Considered**:
  1. Publish a signal; waiter re-fetches ticket status from Postgres (requires `GetByID`)
  2. Publish the decision string in the signal payload (`"approved"` / `"denied"`)
- **Selected Approach**: Option 2 — decision in payload
- **Rationale**: Avoids a Postgres round-trip; simpler; the authoritative decision is already in DB (persisted before publish); the signal is just a notification
- **Trade-offs**: Signal payload is trusted internal data (UUID-keyed channel, not user-controllable); no external trust issue
- **Follow-up**: None

### Decision: SlackNotifier and ApprovalBridge as interfaces

- **Context**: The brief explicitly identifies both as boundary candidates for v1+ swap-in
- **Alternatives Considered**:
  1. Concrete types only (simpler, fewer files)
  2. Interfaces (brief-specified extensibility)
- **Selected Approach**: Interfaces — one implementation each for v0
- **Rationale**: Brief explicitly states "Approval notifier interface (initially Slack; designed as interface so v1+ can swap in other channels)" and "the interface hides the [resume] mechanism". Synthesis principle allows interfaces when they are explicitly specified as boundary candidates, even with one implementation.
- **Trade-offs**: Slightly more files; enables v1+ to add Teams/email without touching PolicyGateHandler
- **Follow-up**: None

### Decision: `UpdateStatus` must be idempotent (WHERE status='pending')

- **Context**: Slack retries webhook delivery on 5xx responses; a duplicate action payload could double-update a ticket
- **Selected Approach**: `UPDATE ticket SET ... WHERE id=$1 AND status='pending'` — no-op if already terminal
- **Rationale**: Prevents double-approval; returns success even if no rows updated (Slack gets 200 on retry)
- **Follow-up**: Verify Postgres `UPDATE` returning 0 rows is not treated as error in `UpdateStatus`

---

## Synthesis Outcomes

- **Generalization**: `ApprovalBridge` and `SlackNotifier` as interfaces generalize the "decision wait" and "notification delivery" concepts without over-building (one implementation each)
- **Build vs. Adopt**: `go-redis/v9` Pub/Sub (already in codebase), `crypto/hmac`+`crypto/sha256` (stdlib), `net/http` (stdlib) — no new dependencies
- **Simplification**: Removed `GetByID` (not needed; decision in payload); removed forwarder injection (pipeline contract handles it); removed `TicketRecord.ID` field addition (Insert returns ID separately)

---

## Risks & Mitigations

- **HTTP server WriteTimeout too short**: If `WriteTimeout < 5 minutes`, approval waits will be forcibly closed by the server before the timeout fires. Mitigation: document required server timeout configuration; add to deployment checklist.
- **Gateway restart during approval wait**: In-flight approval waits are lost on restart; `WaitForDecision` context is cancelled and the client sees a connection close. The ticket remains `pending` until it expires naturally. Mitigation: document limitation; v0 spec explicitly scopes this out.
- **Signal lost between UpdateStatus and Publish**: Ticket is persisted but signal is never received; waiter times out after 5 minutes. Mitigation: waiter timeout marks ticket `expired` (already terminal, no update); client sees timeout error. Acceptable for v0.
- **Slack argument payload too long**: Approval messages with large arguments may exceed Slack block character limits. Mitigation: truncate arguments display in `SlackClient.SendApprovalRequest`.

---

## References

- Slack Block Actions payload: https://api.slack.com/reference/interaction-payloads/block-actions
- Slack request signature verification: https://api.slack.com/authentication/verifying-requests-from-slack
- Slack Block Kit message format: https://api.slack.com/block-kit
- go-redis/v9 Pub/Sub: https://pkg.go.dev/github.com/redis/go-redis/v9
