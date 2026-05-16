# Implementation Plan

- [ ] 1. Foundation — configuration and persistence extension
- [x] 1.1 Extend gateway configuration with Slack environment variables
  - Add three required Slack fields to the Config struct: bot token, signing secret, and channel identifier
  - `LoadConfig()` reads each from its corresponding env var; if any is absent after loading, return an error that names the missing variable(s)
  - Gateway binary exits at startup with a clear error message when any Slack variable is missing
  - The gateway refuses to start and logs which specific variable is missing — observable via startup output
  - _Requirements: 2.4, 3.5, 6.1, 6.2, 6.3, 6.4_

- [x] 1.2 Add ticket status transition capability to TicketStore
  - Extend the ticket persistence layer with a method that transitions a ticket from `pending` to a terminal status (`approved`, `denied`, or `expired`)
  - The transition carries a decision timestamp and the ID of the actor who made the decision (empty for system-triggered transitions)
  - The operation is idempotent: if the ticket is already in a terminal status, the call succeeds silently with no update
  - A ticket in `pending` status is correctly transitioned to `approved` by a verified approve action — observable via DB row state after the call
  - _Requirements: 4.1, 4.2, 4.3, 5.2_

- [ ] 2. Core — approval-flow components
- [x] 2.1 (P) Build the approval hold bridge
  - Implement the `ApprovalBridge` interface with a method that blocks the calling goroutine until a resume signal is received, the context is cancelled, or a 5-minute timeout fires
  - The concrete implementation subscribes to a per-ticket notification channel on Redis when the wait begins; the subscription is cleaned up on any exit path
  - While waiting, a ticker periodically calls the session mutex TTL extension; extension failures are logged but do not abort the wait
  - On timeout: the ticket status is updated to `expired` and a sentinel timeout error is returned
  - On channel closure or connection loss: the same timeout path is followed
  - On approved signal: returns a decision indicating approval; on denied signal: returns a decision indicating denial
  - A wait that receives a `"denied"` signal correctly returns `Approved: false` and does not update the ticket status — observable by reading the in-memory decision result
  - _Requirements: 1.1, 1.2, 1.5, 5.1, 5.2, 5.3, 5.4_
  - _Boundary: RedisApprovalBridge_

- [x] 2.2 (P) Build the Slack approval notifier
  - Implement the `SlackNotifier` interface with a method that sends a Block Kit message to the configured Slack channel
  - The message body includes tool name, operation, call arguments, and session ID in a section block
  - The message includes an Approve button and a Deny button in an actions block; the ticket ID is embedded as the button value for each
  - Credentials (bot token, channel) are read from the injected config fields — no hardcoded values
  - A sent message contains `action_id` values `"approval_approve"` / `"approval_deny"` and `value` set to the ticket ID — observable by inspecting the outbound HTTP request body in tests
  - _Requirements: 2.1, 2.2, 2.4_
  - _Boundary: SlackClient_

- [x] 2.3 (P) Build the Slack webhook handler
  - Implement the HTTP handler for `POST /slack/actions` that processes Slack interactive component action payloads
  - Read the raw request body into a buffer before any parsing (required by the signature verification algorithm)
  - Verify the Slack request signature: check that `X-Slack-Request-Timestamp` is within 5 minutes of current time, then compute HMAC-SHA256 over the signing base string and compare using constant-time comparison
  - Parse the URL-encoded `payload` form field from the buffered body and unmarshal it into the block_actions structure
  - Route on `action_id` (`"approval_approve"` or `"approval_deny"`): extract the ticket ID from the button `value`, update the ticket status via UpdateStatus, then publish a resume signal on the per-ticket notification channel
  - Return HTTP 400 for invalid signatures and replay attacks; return HTTP 500 if ticket status update fails (do not publish if update fails); return HTTP 200 on success
  - A request with an incorrect HMAC signature returns HTTP 400 and no ticket is updated — observable by the response status code and unchanged DB state in tests
  - _Requirements: 3.1, 3.2, 3.3, 3.4, 3.5, 4.1, 4.2, 4.3, 4.4, 5.4_
  - _Boundary: SlackWebhookHandler_

- [ ] 3. Integration — pipeline modification and binary wiring
- [x] 3.1 Modify the policy gate approval path to hold the connection
  - Change the `approvalRequired` branch in `PolicyGateHandler` to invoke the approval hold and Slack notification instead of returning a pending response immediately
  - After inserting the ticket, launch a goroutine to send the Slack notification; notification failures are logged and do not block the approval hold
  - Call `WaitForDecision` with the ticket ID, session ID, and turn ID; block until a decision or timeout
  - On approval: return `(nil, nil)` so the pipeline continues naturally to the upstream forwarder with the original request
  - On denial: return a JSON-RPC error response with code `-32001` and message `"approval denied"`
  - On timeout (`ErrApprovalTimeout`): return a JSON-RPC error response with code `-32001` and message `"approval timeout"`
  - A previously immediate-return path now holds the HTTP connection open: confirmed by the handler not returning until `WaitForDecision` resolves
  - _Requirements: 1.1, 1.3, 1.4, 1.5, 2.3, 5.1, 5.4_

- [x] 3.2 Wire all approval-flow components into the gateway binary and register the webhook endpoint
  - Construct `SlackClient`, `RedisApprovalBridge`, and `SlackWebhookHandler` in the gateway startup sequence using the Slack config fields
  - Inject `ApprovalBridge` and `SlackNotifier` into `PolicyGateHandler` during pipeline construction
  - Register `POST /slack/actions` on the HTTP server's mux, routing to the `SlackWebhookHandler`
  - Verify that the HTTP server write timeout accommodates the 5-minute approval window (set `WriteTimeout` to at least 360 seconds or configure it per-handler)
  - The running gateway accepts `POST /slack/actions` and responds with HTTP 400 for unsigned requests — observable by sending an unsigned request to a running gateway instance
  - _Requirements: 3.1, 6.1, 6.2, 6.3_

- [ ] 4. Validation — unit, integration, and E2E tests
- [ ] 4.1 (P) Unit tests for the approval hold bridge
  - Test that a received `"approved"` signal causes `WaitForDecision` to return `ApprovalDecision{Approved: true}`
  - Test that a received `"denied"` signal causes `WaitForDecision` to return `ApprovalDecision{Approved: false}`
  - Test that timeout fires after the configured duration: ticket status is updated to `"expired"` and `ErrApprovalTimeout` is returned (use a short timeout config in tests)
  - Test that a closed pub/sub channel (connection loss) follows the timeout path
  - Test that lock extension errors are logged but the wait continues
  - All test cases verify the bridge cleans up its Redis subscription on exit
  - _Requirements: 1.1, 1.2, 1.5, 5.1, 5.2, 5.3, 5.4_
  - _Boundary: RedisApprovalBridge_

- [ ] 4.2 (P) Unit tests for the Slack webhook handler
  - Test that a correctly signed approve action updates the ticket to `"approved"` and publishes a resume signal, returning HTTP 200
  - Test that a correctly signed deny action updates the ticket to `"denied"` and publishes a resume signal, returning HTTP 200
  - Test that a request with a mismatched HMAC signature returns HTTP 400 and no DB or Redis calls are made
  - Test that a request with a timestamp older than 5 minutes returns HTTP 400 (replay protection)
  - Test that a `UpdateStatus` failure returns HTTP 500 and no Redis publish occurs
  - _Requirements: 3.2, 3.3, 3.4, 4.1, 4.2, 4.3, 4.4, 5.4_
  - _Boundary: SlackWebhookHandler_

- [ ] 4.3 (P) Unit tests for the Slack notifier and policy gate approval path
  - SlackClient: verify the outbound `chat.postMessage` payload contains the correct `action_id` values, button values (ticket ID), tool name, and session ID in the Block Kit structure
  - SlackClient: verify the `Authorization: Bearer` header is set from the injected bot token
  - PolicyGateHandler (mock bridge): verify that an approved decision causes the handler to return `(nil, nil)`
  - PolicyGateHandler (mock bridge): verify that a denied decision causes the handler to return a `-32001 "approval denied"` response
  - PolicyGateHandler (mock bridge): verify that `ErrApprovalTimeout` causes the handler to return a `-32001 "approval timeout"` response
  - PolicyGateHandler (mock notifier): verify that a Slack notification error is logged and does not prevent `WaitForDecision` from being called
  - _Requirements: 1.3, 1.4, 1.5, 2.1, 2.2, 2.3, 2.4, 5.1_
  - _Boundary: SlackClient, PolicyGateHandler_

- [ ] 4.4 Integration tests for the full approval flow with real Redis and Postgres
  - Approve flow: insert a pending ticket, start `WaitForDecision`, call `UpdateStatus("approved")` + `Publish("approved")` concurrently, verify `WaitForDecision` returns `ApprovalDecision{Approved: true}`
  - Deny flow: same structure with `"denied"`, verify `Approved: false` returned
  - Timeout flow: configure a short timeout (e.g. 100ms); verify `UpdateStatus("expired")` is called and `ErrApprovalTimeout` is returned
  - Idempotent `UpdateStatus`: call twice with `"approved"`; second call succeeds silently with no double-write
  - _Requirements: 1.1, 1.2, 1.3, 1.4, 1.5, 4.1, 4.2, 5.1, 5.2_

## Implementation Notes
- Schema bug fixed: ticket.status CHECK constraint had 'rejected' instead of 'denied' (the canonical value per design/requirements). Fixed in db.go with a DO-block repair migration for existing databases. All downstream tasks must use 'denied', not 'rejected'.

- [ ] 4.5 End-to-end validation via Docker Compose with real Redis and Slack webhook simulation
  - Bring up the full Docker Compose stack including Postgres and Redis
  - Send an `approvalRequired` MCP tool call and verify the HTTP connection is held open
  - Simulate a Slack approve action by sending a correctly HMAC-signed `POST /slack/actions` request; verify the tool result is returned to the MCP client
  - Simulate a Slack deny action; verify a `-32001 "approval denied"` error is returned
  - Send an unsigned `POST /slack/actions` request; verify HTTP 400 is returned
  - _Requirements: 1.1, 1.3, 1.4, 3.1, 3.2, 3.3, 4.1, 4.2, 4.4_
