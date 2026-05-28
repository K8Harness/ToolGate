# Eval Runner Operator UI — Design

**Date:** 2026-05-28

## Overview

Make the eval runner demo feel like a single ToolGate resilience operator surface. The current custom runner UI exposes a YAML textarea and waits for one blocking response. The new flow should let a demo operator choose a scenario, see the intentionally degraded stack state, run the eval with an elapsed timer, watch case results appear as they finish, and inspect the audit decisions that explain each verdict.

This pass stays within `cmd/eval-runner`. It does not make the browser UI start or stop Docker services. Fault injection remains owned by the existing demo script/operator workflow.

## Goals

- Replace the YAML textarea as the main path with preset scenario cards.
- Keep custom YAML available for power users.
- Show elapsed time while a run is active, especially for the 15s approval timeout case.
- Render per-case audit decisions as colored trace badges.
- Stream case-level progress so earlier cases appear before later blocking cases finish.
- Add a small stack-health strip so expected outages are visible in the UI.

## Non-Goals

- No gateway policy or approval behavior changes.
- No Docker lifecycle control from the browser.
- No persistence of run history beyond the current page session.
- No migration to a frontend build system; `cmd/eval-runner/ui.html` remains a static embedded page.

## Current Context

The existing server in `cmd/eval-runner/serve.go` serves `ui.html`, exposes blocking eval endpoints, and accepts custom YAML through `POST /run-eval/custom`. `CaseRunner.Run` already returns audit `TraceRow` values from Postgres. `CaseResult` has a `Trace` field and `TraceRow` has JSON tags in the current workspace, but `Evaluate` does not populate the trace into the result yet.

The existing UI has:

- Agent URL input.
- Test Suite YAML textarea.
- Blocking `fetch('/run-eval/custom')`.
- Results table with case, pass/fail, and failures.

## Architecture

### Components

| Unit | Responsibility |
|---|---|
| Scenario preset display data in `ui.html` | Holds scenario labels, descriptions, default URL values, display-only YAML/plan copy, and expected stack-state copy. Server scenario definitions remain authoritative. |
| Streaming eval handler in `serve.go` | Parses custom suite input and emits one event per case plus a final summary. |
| Streaming scenario handler in `serve.go` | Runs named built-in scenarios that are not expressible as plain eval YAML. |
| Shared case execution helper in `serve.go` | Runs one `EvalCase`, evaluates it, attaches trace, and returns `CaseResult`. |
| Retry storm executor in `serve.go` | Replays the gateway retry loop currently implemented in `scripts/demo-resilience.sh` and returns a normal `CaseResult`. |
| Stack health handler in `serve.go` | Returns lightweight service status values for the UI strip. |
| Result renderer in `ui.html` | Appends streaming case results and renders decision badges from `CaseResult.Trace`. |

### Scenario Registry

Preset scenario definitions are server-authoritative. The UI may display YAML or a scenario plan for clarity, but preset cards run by `scenario_id`, not by trusting client-provided scenario definitions. Displayed preset YAML is explanatory copy and must not be posted back as the source of truth for preset runs.

| Scenario ID | Label | Executor | Target URL | Events/Total |
|---|---|---|---|---|
| `mcp-crash` | MCP Crash | YAML suite with `mcp-server-down` | eval agent URL | 1 case |
| `retry-storm` | Retry Storm | Built-in retry storm executor | gateway MCP URL | 1 case |
| `approval-timeout` | Approval Timeout | YAML suite with `approval-timeout-slack-down` | eval agent URL | 1 case |

Default URLs:

- Eval agent URL: request `agent_url`, else `AGENT_URL`. Serve mode already requires `AGENT_URL` at startup through `LoadConfig()`.
- Gateway MCP URL: request `gateway_mcp_url`, else `GATEWAY_MCP_URL`, else `http://localhost:18080/mcp`.

There is no runnable `all-scenarios` preset in this pass. The full three-part demo still requires external stack transitions: MCP down for MCP Crash and Retry Storm, then MCP restored and Slack down for Approval Timeout. The browser UI surfaces and runs each scenario once the operator has prepared the matching stack state.

Serve-mode startup keeps the existing `LoadConfig()` contract for this pass: `POSTGRES_DSN` and `AGENT_URL` are required environment variables even when the selected scenario later uses only the gateway MCP URL. The UI can override the eval agent URL per request after startup.

### Data Flow

```
operator selects preset
  -> UI displays preset plan/YAML and posts scenario_id plus relevant URLs
  -> UI starts timer and opens stream request
  -> serve.go runs either a YAML suite or a named built-in scenario
  -> after each case: runner.Run -> Evaluate -> attach trace -> emit case event
  -> UI appends/updates result row with trace badges
  -> final summary event updates verdict badge and stops timer
```

## Backend Design

### Case Result Trace

`Evaluate(c, trace)` should preserve the trace in the returned `CaseResult` for both passing and failing cases:

```go
result := CaseResult{Name: c.Name, Trace: trace}
```

For runner errors before a trace is available, the result should include the run failure and leave `Trace` empty.

### Shared Execution Helper

Add a helper around the repeated case execution logic:

```go
func runEvalCase(ctx context.Context, runner caseExecutor, testCase EvalCase) CaseResult
```

Both the blocking handler and streaming handler should use this helper so JSON and streaming behavior stay consistent.

### Streaming Endpoint

Add:

```text
POST /run-eval/custom/stream
```

Request body matches `/run-eval/custom`:

```json
{
  "agent_url": "http://127.0.0.1:18086",
  "suite": "cases:\n  - ..."
}
```

Response should use server-sent events:

```text
Content-Type: text/event-stream
Cache-Control: no-cache
```

Event shapes:

```text
event: case_start
data: {"name":"mcp-server-down","index":0,"total":3}

event: case_result
data: {"index":0,"total":3,"result":{"name":"mcp-server-down","passed":true,"trace":[...]}}

event: summary
data: {"passed":true,"pass_count":3,"total_count":3,"cases":[...],"report":"..."}
```

If suite parsing or request validation fails before streaming starts, return the same HTTP error style as the blocking endpoint. If a case run fails, emit a `case_result` containing a failed `CaseResult`; do not terminate the whole stream unless the client context is canceled or writing fails.

Use `http.Flusher` and flush after every event. If the server cannot flush, return HTTP 500 before running cases.

### Scenario Streaming Endpoint

Add:

```text
POST /run-scenario/stream
```

Request:

```json
{
  "scenario_id": "retry-storm",
  "agent_url": "http://127.0.0.1:18086",
  "gateway_mcp_url": "http://localhost:18080/mcp"
}
```

Behavior:

- `mcp-crash` and `approval-timeout` use server-defined YAML suites and the same internal execution path as `/run-eval/custom/stream`.
- `retry-storm` runs a built-in gateway retry loop because the current `EvalCase` model cannot express repeated direct MCP calls within one turn.
- Unknown scenario IDs return HTTP 400.

The frontend calls `/run-scenario/stream` for preset cards and `/run-eval/custom/stream` only for the custom editable YAML mode. The request contract for `/run-scenario/stream` does not accept client-provided suite YAML.

Server-side URL validation:

- YAML-backed scenarios require an absolute `agent_url` after request/env resolution.
- Retry Storm requires an absolute `gateway_mcp_url` after request/env/default resolution and must not require or use `agent_url`.
- Invalid URLs return HTTP 400 before streaming starts.

### Retry Storm Executor

The retry storm executor should mirror the proven shell flow in `scripts/demo-resilience.sh`:

1. Initialize a gateway MCP session by posting `initialize` to the resolved gateway MCP URL.
2. Capture the `Mcp-Session-Id` response header from initialize.
3. Reuse a single `X-Mcp-Turn-Id`.
4. Send `Mcp-Session-Id` and `X-Mcp-Turn-Id` on every `tools/call`.
5. Call `tools/call` for `list_recent_charges` up to six times.
6. Stop once a response contains the budget limiter error.
7. Poll `audit_log` for the captured session trace until the terminal `budgetExceeded` row appears or the audit poll timeout expires, matching the existing async-audit handling in `CaseRunner.Run`.
8. Return `CaseResult{Name: "retry-storm-budget", Passed: true, Trace: trace}` if any trace row has decision `budgetExceeded`.

If initialization fails, calls never reach `budgetExceeded`, or the audit query fails, return a failed `CaseResult` with a `run` or `policyOutcome` failure. This executor should not stop or start Docker services; it assumes the operator or demo script has already placed the stack in the intended state.

Required external precondition for Retry Storm: the gateway capability cache must already be warmed while MCP is healthy, then `localstripe-mcp` must be stopped. This mirrors `scripts/demo-resilience.sh`; without warm cache, the gateway may fail earlier during capability/session setup instead of demonstrating the budget limiter.

### Blocking Endpoint Compatibility

`POST /run-eval/custom` should keep its current response shape. The only intentional response change is that each case may now include `trace` when audit rows were available. Existing consumers that ignore unknown fields remain compatible.

### Stack Health Endpoint

Add:

```text
GET /stack-health
```

Response:

```json
{
  "services": [
    {"name":"Gateway","status":"up","detail":"http://localhost:18080"},
    {"name":"MCP","status":"down","detail":"http://localhost:18421"},
    {"name":"Slack","status":"up","detail":"http://localhost:18090"},
    {"name":"Postgres","status":"up","detail":"configured DSN reachable"}
  ]
}
```

Statuses are `up`, `down`, or `unknown`. The handler should use short timeouts and avoid expensive operations:

- Gateway: HTTP probe to `http://localhost:18080/healthz` if configured or hardcoded for the demo compose port.
- MCP: HTTP probe to `http://localhost:18421/mcp` is not a safe generic health call because MCP initialize requires a POST; use TCP dial or mark `unknown` if a safe probe is not available.
- Slack: HTTP probe to `http://localhost:18090/healthz`.
- Postgres: use the existing pool `Ping` with a short context timeout.

Use a 750ms timeout per probe. If a probe target is not configured and no demo default is available, report `unknown` instead of failing the endpoint.

## Frontend Design

### Layout

The first screen is an operator dashboard:

- Header with title and final verdict badge area.
- Scenario cards as the primary control row.
- Stack health strip near the run controls.
- URL controls for both eval agent URL and gateway MCP URL. The eval agent URL is required for YAML-backed presets and custom YAML; the gateway MCP URL is required for Retry Storm.
- Run button and elapsed timer.
- Results panel that can show `queued`, `running`, `PASS`, and `FAIL`.
- Collapsible or secondary scenario plan/editor below the primary controls.

The scenario plan/editor is read-only for preset modes. A `Customize YAML` action copies the displayed YAML-backed preset into editable `Custom YAML` mode. Non-YAML presets such as Retry Storm show a read-only plan and do not offer direct plan editing.

### Presets

Initial presets:

- `MCP Crash`: runs the `mcp-server-down` suite with agent URL `http://127.0.0.1:18086`.
- `Retry Storm`: runs the built-in `retry-storm` executor against `http://localhost:18080/mcp`.
- `Approval Timeout`: runs the `approval-timeout-slack-down` suite with agent URL `http://127.0.0.1:18086`.

For YAML-backed presets, the scenario plan/editor shows display-only YAML matching the server-defined scenario. For the retry storm preset, it shows a read-only scenario plan because the run is server-authoritative and uses a non-YAML executor. The operator can enter `Custom YAML` mode only through `Customize YAML` or by choosing the custom mode explicitly; custom mode runs `/run-eval/custom/stream` instead of `/run-scenario/stream`.

Client-side validation:

- YAML-backed presets and custom YAML require an absolute eval agent URL.
- Retry Storm requires an absolute gateway MCP URL and does not require an eval agent URL.
- The UI should not show an `All Scenarios` run button or imply stack transitions happen automatically.

### Elapsed Timer

When a run starts:

- Clear previous errors.
- Set elapsed to `00:00`.
- Tick once per second until summary, stream error, or request cancellation.
- Keep the run button disabled while active.

### Streaming Results

The browser should use `fetch()` and read the response body stream. Native `EventSource` is not suitable for a JSON `POST` body. The UI can parse SSE frames from the fetch stream.

Behavior:

- On `case_start`, mark the case row as running.
- On `case_result`, render pass/fail, failures, and trace badges.
- On `summary`, update final verdict and stop the timer.
- On stream parse or network failure, show the error box and stop the timer.

### Decision Trace Badges

Badge color mapping:

| Decision | Color |
|---|---|
| `allow` | green |
| `approvalRequired` | yellow |
| `expired` | gray |
| `deny` | red |
| `upstream_error` | red |
| `budgetExceeded` | orange |
| other/empty | slate |

Badge text should be compact:

```text
list_recent_charges -> allow
create_refund -> approvalRequired
create_refund -> expired
```

Tool names and decisions must be escaped before insertion into the DOM.

## Error Handling

- Invalid custom suite: show HTTP 400 text in the existing error box.
- Missing agent URL or YAML: validate client-side before request.
- Streaming unsupported by server: show a clear HTTP error.
- Mid-stream case failure: render that case as failed and continue to later cases if the server can continue.
- Mid-stream transport failure: show the error box and leave already-rendered case results visible.
- Empty trace: render a muted `no audit trace` label instead of an empty panel.

## Testing

Backend unit tests:

- `Evaluate` includes trace on pass and fail.
- Blocking JSON response includes `trace` fields when present.
- Streaming handler emits `case_start`, `case_result`, and `summary` in order.
- Streaming handler emits a failed `case_result` for runner errors and continues to the next case.
- Scenario streaming handler runs the retry storm executor for `scenario_id=retry-storm`.
- Retry storm executor passes only when the trace contains `budgetExceeded`.
- Retry storm executor polls for delayed async audit visibility before failing.
- Stack health handler returns a JSON response even when probes fail.

Manual UI verification:

- Load `POSTGRES_DSN=postgres://gateway:gateway@127.0.0.1:15432/gateway?sslmode=disable AGENT_URL=http://127.0.0.1:18086 go run ./cmd/eval-runner --serve evalsuite/resilience.yaml`.
- Select a preset and confirm YAML/agent URL populate.
- Run a suite and confirm timer ticks.
- Confirm first case result appears before an approval-timeout case completes.
- Prepare Retry Storm by warming gateway capability cache while MCP is healthy, stopping MCP, then running the preset; confirm delayed audit rows are polled until `budgetExceeded`.
- Confirm trace badges render with the expected colors and no overlapping text at desktop width.
- Interrupt a streaming run by stopping the target service or closing the request and confirm already-rendered rows remain visible.

## Implementation Notes

- Keep all JavaScript in `ui.html` for this pass to match the current embedded static UI.
- Prefer DOM creation over large `innerHTML` strings where user-controlled values are rendered.
- Keep `/run-eval/custom` stable for existing scripts and tests.
- Do not commit `.superpowers/brainstorm/` visual companion artifacts.
