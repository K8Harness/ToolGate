# Requirements Document

## Introduction

The approval-flow feature enables human-in-the-loop oversight for tool calls that the policy gate classifies as `approvalRequired`. When such a call is received, the gateway holds the HTTP connection open, sends a Slack Block Kit notification with the tool details and decision buttons, and waits for a manager to approve or deny within a 5-minute window. An approved call is forwarded to the upstream MCP server and the result returned to the client; a denied or timed-out call receives a JSON-RPC error response.

## Boundary Context

- **In scope**: synchronous hold (HTTP connection open during approval wait), Slack Block Kit notification, `/slack/actions` webhook endpoint, Slack request signature verification, decision resume signaling, durable ticket status updates before signaling, 5-minute fixed timeout with deny-on-timeout, session mutex TTL extension during approval wait.
- **Out of scope**: email, PagerDuty, Teams, or other notification channels; multi-approver quorum; approval history dashboard or query API; async approval (fire-and-return) — v0 uses synchronous hold only; approval state surviving a gateway restart — a restart during an approval wait results in a client-side timeout on retry.
- **Adjacent expectations**: policy-gate creates ticket records with `pending` status for `approvalRequired` cases — this feature reads and updates those records, changing the prior behavior of returning a `pending` response immediately; session-mgmt provides a session mutex TTL extension mechanism — this feature calls it to keep the session lock alive during the approval wait; the Slack API delivers interactive action payloads via HTTP POST to the gateway's webhook endpoint.

## Requirements

### Requirement 1: Synchronous Approval Hold

**Objective:** As a gateway operator, I want the gateway to hold the HTTP connection open while awaiting a human decision, so that the agent client receives the tool call result only after the decision is made.

#### Acceptance Criteria
1. When the policy gate classifies a tool call as `approvalRequired`, the Gateway shall suspend the response and hold the HTTP connection open until either a decision signal is received or the timeout fires.
2. While an approval hold is active, the Gateway shall periodically extend the session mutex lock TTL to prevent it from expiring before the decision arrives.
3. When a decision signal is received and the decision is `approved`, the Gateway shall forward the original tool call to the upstream MCP server and return its result to the client.
4. When a decision signal is received and the decision is `denied`, the Gateway shall return a JSON-RPC error response with code `-32001` and message `"approval denied"`.
5. If no decision is received within 5 minutes, the Gateway shall return a JSON-RPC error response with code `-32001` and message `"approval timeout"`.

### Requirement 2: Slack Approval Notification

**Objective:** As a manager, I want to receive a Slack message when a tool call requires my approval, so that I can make an informed, timely decision.

#### Acceptance Criteria
1. When a tool call is held for approval, the Gateway shall send a Slack Block Kit message to the configured Slack channel containing: tool name, method/operation, call arguments, and session ID.
2. The Slack Block Kit message shall include an Approve button and a Deny button, each carrying the associated ticket ID as an action value.
3. If sending the Slack notification fails, the Gateway shall log the failure with the ticket ID and continue the approval hold — the timeout path still applies.
4. The Gateway shall send the Slack notification using credentials read exclusively from environment variables; credentials must not be hardcoded.

### Requirement 3: Slack Webhook Endpoint and Signature Verification

**Objective:** As a gateway operator, I want the gateway to receive and verify Slack interactive action payloads, so that only legitimate Slack-originated decisions are processed.

#### Acceptance Criteria
1. The Gateway shall expose a `POST /slack/actions` HTTP endpoint to receive Slack interactive component action payloads.
2. When a request arrives at `/slack/actions`, the Gateway shall verify the Slack request signature using HMAC-SHA256 with the `X-Slack-Signature` and `X-Slack-Request-Timestamp` headers against the configured signing secret before processing the payload.
3. If signature verification fails, the Gateway shall return HTTP 400 and take no further action on the payload.
4. If the `X-Slack-Request-Timestamp` value is more than 5 minutes in the past, the Gateway shall reject the request with HTTP 400 to prevent replay attacks.
5. The Gateway shall read the Slack signing secret from an environment variable; the secret must not be hardcoded.

### Requirement 4: Decision Persistence and Resume Signaling

**Objective:** As a gateway operator, I want approval decisions to be durably recorded before the waiting handler is notified, so that no decision is lost if the gateway restarts between recording and signaling.

#### Acceptance Criteria
1. When a valid `approve` action is received at `/slack/actions`, the Gateway shall update the ticket record status to `approved` with a decision timestamp, and then publish a resume signal for that ticket, before returning a response to Slack.
2. When a valid `deny` action is received at `/slack/actions`, the Gateway shall update the ticket record status to `denied` with a decision timestamp, and then publish a resume signal for that ticket, before returning a response to Slack.
3. The Gateway shall always update the ticket record before publishing the resume signal.
4. When the Gateway acknowledges a Slack action, it shall return HTTP 200 to dismiss the Slack button interaction.

### Requirement 5: Timeout and Failure Handling

**Objective:** As an agent client, I want the gateway to close the held connection with a clear error when no decision arrives in time, so that the connection does not hang indefinitely.

#### Acceptance Criteria
1. If no decision signal is received within 5 minutes of the approval hold beginning, the Gateway shall return a JSON-RPC error response with code `-32001` and message `"approval timeout"`.
2. When a timeout occurs, the Gateway shall update the ticket record status to `expired`.
3. If the decision notification channel fails during the approval wait, the Gateway shall treat the failure as a timeout and return the same error response as criterion 1.
4. The Gateway shall log all timeout and error events with the ticket ID, session ID, and failure cause.

### Requirement 6: Configuration

**Objective:** As a gateway operator, I want all Slack integration settings to be provided via environment variables, so that credentials and channel configuration are not embedded in the binary.

#### Acceptance Criteria
1. The Gateway shall read the Slack notification channel identifier from an environment variable.
2. The Gateway shall read the Slack signing secret from an environment variable.
3. The Gateway shall read the Slack notification credential (webhook URL or bot token) from an environment variable.
4. If any required Slack environment variable is absent at startup, the Gateway shall log an error describing which variable is missing and refuse to start.
