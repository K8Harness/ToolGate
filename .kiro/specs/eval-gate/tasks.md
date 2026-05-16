# Implementation Plan

- [x] 1. Foundation — shared types, config, and CLI entry point
- [x] 1.1 Define shared types and load configuration
  - Create `cmd/eval-runner/types.go` with all cross-component types: `EvalCase`, `EvalSuite`, `TraceRow`, `CheckFailure`, `CaseResult` (struct definitions only — no logic)
  - Create `cmd/eval-runner/config.go` with a `Config` struct; load `POSTGRES_DSN` (required), `EVAL_COMPOSE_FILE` (optional, default `deploy/docker-compose.yml` relative to working directory), `AGENT_URL` (required); return descriptive error if required var is absent
  - Follow the same env-var validation pattern as `cmd/gateway/config.go`
  - Observable: `go build ./cmd/eval-runner` compiles with all required env vars set; omitting `POSTGRES_DSN` returns an error naming the missing variable before the program proceeds
  - _Requirements: 1.2, 2.5, 3.1_

- [x] 1.2 Create `cmd/eval-runner/main.go` entry point with CLI argument parsing
  - Accept one positional argument: path to the EvalSuite YAML file; default to `evalsuite/default.yaml` when no argument is supplied
  - Call `exec.LookPath("docker")` at startup; print diagnostic message and exit non-zero if Docker binary is not found
  - Wire `LoadConfig()` at startup; exit non-zero with the config error message if any required variable is absent
  - Stub the main run loop (load suite → orchestrate → run cases → report) as a sequence of placeholder function calls — full wiring is done in task 8.1
  - Observable: `go run ./cmd/eval-runner` with no args and required env vars set starts, passes config and Docker checks, and exits with a clear "no suite found" message; passing a non-existent file path exits with a file-not-found error
  - _Requirements: 3.1, 3.2, 3.3, 3.5_

- [x] 2. Core — loader, evaluator, and reporter
- [x] 2.1 (P) Build the EvalSuite YAML loader
  - Implement `LoadSuite(path string) (*EvalSuite, error)` in `cmd/eval-runner/suite.go`; populate `EvalCase` values using types from `types.go`
  - Use the default `yaml.v3` decoder (do NOT call `KnownFields(true)`); silently accept unknown fields for forward-compatibility
  - Post-decode: validate `policyOutcome` for each case against the allowed enum (`allow`, `deny`, `approvalRequired`, `expired`); return an error naming the case and the invalid value
  - Observable: `LoadSuite("evalsuite/default.yaml")` returns a non-nil `*EvalSuite` with 4 populated cases; a YAML file with an extra unknown field is accepted; a file with `policyOutcome: "bad"` returns a descriptive error; a non-existent path returns a file-not-found error
  - _Requirements: 1.1, 1.2, 1.3, 1.4_
  - _Boundary: EvalSuiteLoader_

- [x] 2.2 (P) Build the evaluator
  - Implement `Evaluate(c EvalCase, trace []TraceRow) CaseResult` in `cmd/eval-runner/evaluator.go` using types from `types.go`
  - `mustInclude`: subsequence match — walk the `mustInclude` list; for each item, advance a pointer through the trace until found or exhausted; out-of-order fails
  - `mustNotInclude`: verify no trace row's `ToolName` equals any listed item (set membership)
  - `policyOutcome`: exact string match against the final trace row's `Decision`; when trace is empty, `Observed = "(empty trace)"`
  - `mustNotContainInArgs`: for each string, `strings.Contains(string(row.Arguments), s)` must be false for every trace row
  - Collect all `CheckFailure` entries before returning (no short-circuit on first failure)
  - Observable: `Evaluate` with a fully matching trace returns `CaseResult{Passed: true, Failures: nil}`; a missing `mustInclude` tool returns a `CheckFailure{Check: "mustInclude", Expected: toolName}`; an empty trace fails `mustInclude` with `Observed: "(empty trace)"`; a PII string present in any row's Arguments fails with `Check: "mustNotContainInArgs"`
  - _Requirements: 5.1, 5.2, 5.3, 5.4, 5.5_
  - _Boundary: Evaluator_

- [x] 2.3 (P) Build the Markdown reporter
  - Implement `GenerateReport(results []CaseResult) string` and `ExitCode(results []CaseResult) int` in `cmd/eval-runner/reporter.go` using types from `types.go`
  - Report structure (in order): summary table with columns `Case | Status` for every case; pass-rate line (`N/M cases passed`); per-failed-case detail blocks (case name header + each `CheckFailure`: check | expected | observed); final verdict line
  - Final verdict must be the exact last line of the string: `"PASS"` when all pass, `"FAIL: N case(s) failed"` when any fail
  - `ExitCode`: returns 0 when all cases passed, 1 otherwise
  - Observable: `GenerateReport` with 2 passing and 1 failing case produces a string whose last line is `"FAIL: 1 case(s) failed"` and whose summary table contains 3 rows; `ExitCode` returns 1 for that input and 0 for an all-pass input
  - _Requirements: 6.1, 6.2, 6.3, 6.4_
  - _Boundary: Reporter_

- [x] 3. Orchestrator and case runner
- [x] 3.1 Build the Docker Compose orchestrator
  - Implement `Orchestrator` with `Up(ctx context.Context) error` and `Down(ctx context.Context) error` in `cmd/eval-runner/orchestrator.go`
  - At startup check: call `exec.LookPath("docker")` and return a descriptive error if not found (this check is also done in main.go; Orchestrator is the authoritative location)
  - `Up`: run `docker compose -f <ComposeFile> -p <ProjectName> up -d --wait` via `exec.CommandContext`; on non-zero exit, capture last 20 lines of combined stderr and include in the returned error
  - `Down`: run `docker compose -f <ComposeFile> -p <ProjectName> down -v`; called via `defer` in main after `Up` succeeds
  - Default `ComposeFile` from `Config.ComposeFile` (`deploy/docker-compose.yml` relative to working directory)
  - Observable: `Up` against a compose file with a healthy single-service stack returns nil; `docker compose ps` shows the service as running; `Down` removes all containers; `Up` with a compose file referencing an image that fails its healthcheck returns an error containing the last lines of compose stderr
  - _Requirements: 2.1, 2.2, 2.3, 2.4, 2.5, 3.5_
  - _Boundary: Orchestrator_

- [x] 3.2 Build the case runner (agent trigger and audit log trace capture)
  - Implement `CaseRunner` with `Run(ctx context.Context, c EvalCase) ([]TraceRow, error)` in `cmd/eval-runner/runner.go` using types from `types.go`
  - `Run`: POST `{"input": c.Input}` to `Config.AgentURL + "/trigger"` with a 60s HTTP timeout; parse `{"session_id": "<uuid>"}` from the response body
  - On non-200 HTTP status: return `(nil, error)` with the status code and first 256 bytes of response body
  - Query `SELECT tool_name, decision, arguments FROM audit_log WHERE session_id = $1 ORDER BY decided_at ASC` using pgxpool; scan `arguments` as `json.RawMessage`
  - On DB connection or query error: return `(nil, error)` with the error text; caller marks the case as failed and continues
  - Observable: `Run` with a stub HTTP server returning `{"session_id": "s1"}` and 3 pre-inserted audit_log rows for `"s1"` returns a 3-element `[]TraceRow` in `decided_at` order with correct `ToolName`, `Decision`, and raw `Arguments`; a non-200 trigger response returns an error with the HTTP status code; a closed DB returns an error without panicking
  - _Requirements: 4.1, 4.2, 4.3_
  - _Boundary: CaseRunner_

- [ ] 4. Fake MCP servers
- [x] 4.1 (P) Build the fake Stripe MCP server
  - Create `examples/fake-mcp-servers/stripe/main.go` using the `go-sdk` server registration pattern
  - Register `create_charge(amount int, currency string, customer_id string)` returning `{"id":"ch_fake_001","status":"succeeded"}`
  - Register `get_customer(customer_id string)` returning `{"id":<customer_id>,"name":"Test Customer"}`
  - Serve on Streamable HTTP (POST `/mcp`) on port 8082; unrecognized tools return JSON-RPC error code -32601 (go-sdk default)
  - Create `examples/fake-mcp-servers/stripe/Dockerfile` using `golang:1.25-alpine` builder and `alpine` runtime
  - Observable: `docker build -t fake-stripe examples/fake-mcp-servers/stripe` exits 0; a `tools/call create_charge` MCP POST to the running container returns a JSON-RPC result containing `"status":"succeeded"`; a `tools/call unknown_tool` returns a JSON-RPC error with code -32601
  - _Requirements: 8.1, 8.2, 8.3, 8.4_
  - _Boundary: Fake Stripe Server_

- [x] 4.2 (P) Build the fake Zendesk MCP server
  - Create `examples/fake-mcp-servers/zendesk/main.go` with the same go-sdk pattern
  - Register `create_ticket(subject string, description string, customer_id string)` returning `{"id":"tkt_fake_001","status":"open"}`
  - Register `close_ticket(ticket_id string)` returning `{"id":<ticket_id>,"status":"closed"}`
  - Serve on port 8083; create `examples/fake-mcp-servers/zendesk/Dockerfile`
  - Observable: `docker build -t fake-zendesk examples/fake-mcp-servers/zendesk` exits 0; a `tools/call create_ticket` request returns a result containing `"status":"open"`
  - _Requirements: 8.1, 8.2, 8.3, 8.4_
  - _Boundary: Fake Zendesk Server_

- [x] 4.3 (P) Build the fake Slack MCP server
  - Create `examples/fake-mcp-servers/slack/main.go` with go-sdk server pattern
  - Register `send_slack_message(channel string, message string)` returning `{"ok":true}`; append received arguments to an in-memory `[]map[string]any` protected by `sync.Mutex`
  - Serve MCP on port 8084; additionally expose `GET /inspect` on the same port via a path-muxed handler, returning `{"calls":[...]}` of stored arguments
  - Create `examples/fake-mcp-servers/slack/Dockerfile`
  - Observable: `docker build -t fake-slack examples/fake-mcp-servers/slack` exits 0; after a `tools/call send_slack_message` request, `GET /inspect` returns a JSON array containing the arguments that were passed; a second `send_slack_message` call appends a second entry to the inspect array
  - _Requirements: 8.1, 8.2, 8.3, 8.4_
  - _Boundary: Fake Slack Server_

- [ ] 5. Mock Slack service and Python demo agent
- [x] 5.1 (P) Build the mock Slack auto-approver service
  - Create `examples/mock-slack/main.go` exposing `POST /api/chat.postMessage`
  - Parse the incoming Block Kit notification JSON: traverse `blocks` array to find the `actions` block; extract the first button element's `value` field as `ticket_id`
  - Wait 50ms, then construct a Slack interactive payload: `type: block_actions`, `actions[0].action_id: "approval_approve"`, `actions[0].value: <ticket_id>`
  - Sign the payload using HMAC-SHA256 with `SLACK_SIGNING_SECRET` using a timestamp and the `v0=<hmac>` format matching the gateway's `SlackWebhookHandler` verification
  - POST the signed payload to `GATEWAY_URL + "/slack/actions"` with `X-Slack-Signature` and `X-Slack-Request-Timestamp` headers; return `{"ok":true}` to the gateway notifier regardless of the gateway's response
  - Read `GATEWAY_URL` and `SLACK_SIGNING_SECRET` from environment; create `examples/mock-slack/Dockerfile`
  - Observable: `docker build -t mock-slack examples/mock-slack` exits 0; when the gateway sends a `chat.postMessage` with a ticket_id embedded in a button value, the mock Slack service posts a correctly-signed approve action to `/slack/actions` within 1 second; the gateway logs HTTP 200 for the action (confirmed by gateway stdout)
  - _Requirements: 10.2, 10.3_
  - _Boundary: Mock Slack Service_

- [x] 5.2 (P) Build the Python demo agent with HTTP trigger endpoint
  - Create `examples/support-agent/agent.py` with a Flask `POST /trigger` endpoint
  - Implement keyword dispatch table: `small-refund` → `refund_small({amount:50, customer_id:"cust_001"})`, `large-refund` → `refund_large({amount:12000, customer_id:"cust_002"})`, `delete-customer` → `delete_record({customer_id:"cust_003"})`, `slack-pii-message` → `send_slack_message({channel:"#support", message:"Customer SSN: 123-45-6789"})`
  - Generate a UUID `session_id` per trigger call; include it as `Mcp-Session-Id` header in all MCP requests to the gateway
  - Open an MCP `ClientSession` via `streamablehttp_client` to `GATEWAY_URL`; call `session.initialize()` then `session.call_tool(tool, args)`; wait for the response before returning
  - Return `{"session_id": "<uuid>"}` as the trigger response body; return HTTP 400 for unknown input keywords
  - Create `examples/support-agent/requirements.txt` (`mcp[cli]`, `flask`) and `examples/support-agent/Dockerfile`
  - Observable: `docker build -t support-agent examples/support-agent` exits 0; `POST /trigger {"input":"small-refund"}` returns `{"session_id":"<uuid>"}` after the gateway processes the `refund_small` call; `POST /trigger {"input":"unknown"}` returns HTTP 400; the `Mcp-Session-Id` header sent to the gateway matches the returned `session_id`
  - _Requirements: 9.1, 9.2, 9.3, 9.4_
  - _Boundary: Demo Agent_

- [ ] 6. Gateway extensions — redact action and configurable Slack API base URL
- [x] 6.1 Add `SLACK_API_BASE_URL` config field to the gateway
  - Add optional `SlackAPIBaseURL string` to `Config` in `cmd/gateway/config.go`; load from `SLACK_API_BASE_URL` env var with default `"https://slack.com/api"`
  - Update `SlackNotifier` in `cmd/gateway/slack_notifier.go` to use `cfg.SlackAPIBaseURL` as the API base when constructing the `chat.postMessage` request URL, replacing the hardcoded string
  - Observable: starting the gateway with `SLACK_API_BASE_URL=http://mock-slack:8090/api` causes `chat.postMessage` HTTP requests to be sent to `http://mock-slack:8090/api/chat.postMessage`; starting without `SLACK_API_BASE_URL` preserves existing behavior with `https://slack.com/api`; `go build ./cmd/gateway` succeeds
  - _Requirements: 10.2, 10.3_
  - _Boundary: Gateway config, SlackNotifier_

- [ ] 6.2 Implement the `redact` policy action in the gateway
  - Add optional `RedactFields []string \`yaml:"redactFields"\`` to the `Rule` struct in `core/policy/policy.go`; existing policy YAML files without this field continue to load without error
  - Add `case "redact":` to the action switch in `cmd/gateway/policy_gate.go`: deep-copy `req.Params.Arguments` map, replace the value of each field listed in `RedactFields` with the string `"***REDACTED***"` (skip fields not present), call `auditWriter.Write` with `decision="allow"` and the post-redaction arguments, set `req.Params.Arguments` to the masked copy, return `(nil, nil)` to continue to the upstream forwarder
  - Add `redact` rule for `send_slack_message` with `redactFields: ["message"]` to `policy.yaml`
  - Observable: a `tools/call send_slack_message` request with `arguments.message = "SSN: 123-45-6789"` produces an `audit_log` row with `decision="allow"` and `arguments` containing `"***REDACTED***"` instead of `"123-45-6789"`; the upstream fake Slack server receives the redacted message; existing allow, deny, and approvalRequired rules are unaffected; `go build ./...` exits 0 with no regressions
  - _Requirements: 7.4_
  - _Boundary: PolicyGate extension, Rule struct_

- [ ] 7. Docker Compose stack and default EvalSuite
- [ ] 7.1 Create `deploy/docker-compose.yml` with all v0 services
  - Include all services from the root `docker-compose.yml` (gateway, postgres, redis) with their existing healthchecks and env vars
  - Add new services: `fake-stripe` (build: `examples/fake-mcp-servers/stripe`, port 8082, healthcheck on POST /mcp), `fake-zendesk` (build: zendesk, port 8083), `fake-slack` (build: slack, port 8084), `mock-slack` (build: `examples/mock-slack`, port 8090, env: `GATEWAY_URL=http://gateway:8080`, `SLACK_SIGNING_SECRET`), `support-agent` (build: `examples/support-agent`, port 8085, env: `GATEWAY_URL=http://gateway:8080`)
  - Gateway service: add `SLACK_API_BASE_URL=http://mock-slack:8090/api` and appropriate `depends_on` with `condition: service_healthy` for all services it calls (postgres, redis, fake-stripe as the primary upstream)
  - `support-agent` and `mock-slack` depend on gateway being healthy
  - All volumes: ephemeral only (no named volumes for postgres data in this file); `down -v` must fully reset state
  - Observable: `docker compose -f deploy/docker-compose.yml up -d --wait` completes successfully and `docker compose -f deploy/docker-compose.yml ps` shows all services as healthy; `docker compose -f deploy/docker-compose.yml down -v` removes all containers and volumes without errors
  - _Requirements: 2.1, 2.2, 2.5, 7.5, 10.1, 10.4_
  - _Boundary: Docker Compose stack_

- [ ] 7.2 Create `evalsuite/default.yaml` with 4 test cases
  - Write the default EvalSuite YAML with exactly these 4 cases using the schema defined in `types.go`:
    - `small-refund-allow`: input=`small-refund`, mustInclude=[refund_small], policyOutcome=allow
    - `large-refund-approval`: input=`large-refund`, mustInclude=[refund_large], policyOutcome=approvalRequired
    - `delete-customer-deny`: input=`delete-customer`, mustInclude=[delete_record], policyOutcome=deny
    - `slack-pii-redact`: input=`slack-pii-message`, mustInclude=[send_slack_message], policyOutcome=allow, mustNotContainInArgs=["123-45-6789"]
  - Observable: `LoadSuite("evalsuite/default.yaml")` returns an `*EvalSuite` with 4 cases having the exact field values above; the file is valid YAML parseable with no errors
  - _Requirements: 7.1, 7.2, 7.3, 7.4_
  - _Boundary: EvalSuite YAML_

- [ ] 7.3 Create `Makefile` with `make demo` target
  - Add `demo` Makefile target that sets `EVAL_COMPOSE_FILE=deploy/docker-compose.yml`, `POSTGRES_DSN=postgres://gateway:gateway@localhost:5432/gateway`, `AGENT_URL=http://localhost:8085` and runs `go run ./cmd/eval-runner evalsuite/default.yaml`
  - Build the `cmd/eval-runner` binary before running if it does not exist (or use `go run` directly)
  - Observable: running `make demo` from the repository root with Docker available starts the compose stack, prints per-case status, prints the Markdown report, and exits 0 when all 4 cases pass; running without Docker prints a diagnostic and exits non-zero
  - _Requirements: 3.3, 7.5, 10.1_
  - _Boundary: Makefile_

- [ ] 8. Integration — wire eval-runner main loop end-to-end
- [ ] 8.1 Wire all eval-runner components into `main.go`
  - In `main.go`: call `LoadConfig()` — exit non-zero on error; detect Docker via `exec.LookPath` — exit non-zero with diagnostic if missing
  - Load EvalSuite via `LoadSuite(suitePath)` — exit non-zero on parse error (before compose up)
  - Open pgxpool connection to `Config.PostgresDSN` — exit non-zero on connection failure (before compose up)
  - Create `Orchestrator`, call `Up(ctx)` — exit non-zero on startup failure; defer `Down(ctx)` unconditionally after `Up` returns nil
  - Create `CaseRunner`; for each case: call `Run`, handle error (mark case failed, continue), call `Evaluate(case, trace)`, print case status line (`[PASS]` or `[FAIL]` + case name)
  - Call `GenerateReport(results)` and print to stdout; call `os.Exit(ExitCode(results))`
  - Observable: `go run ./cmd/eval-runner evalsuite/default.yaml` with all env vars set and a running demo agent runs the full loop end-to-end, prints per-case status, and exits with the correct code; a missing `POSTGRES_DSN` exits before compose up; a compose startup failure exits after printing the compose stderr excerpt
  - _Depends: 2.1, 2.2, 2.3, 3.1, 3.2_
  - _Requirements: 1.3, 2.1, 2.3, 2.4, 3.1, 3.2, 3.3, 3.4, 3.5, 4.3, 6.1, 6.2, 6.3, 6.4_

- [ ] 9. Validation — unit, integration, and E2E tests
- [ ] 9.1 (P) Unit tests for EvalSuiteLoader
  - Test valid YAML with all fields (including `mustNotContainInArgs`) loads correctly into `EvalSuite.Cases`
  - Test YAML with an unknown top-level field (e.g., `futureField: true`) is accepted without error
  - Test missing `policyOutcome` returns a descriptive error
  - Test invalid `policyOutcome` value (e.g., `"bad"`) returns an error naming the case
  - Test empty `cases` list is accepted and returns an `EvalSuite` with zero cases
  - Observable: `go test ./cmd/eval-runner/... -run TestLoadSuite` exits 0 with all 5 cases passing
  - _Requirements: 1.1, 1.2, 1.3, 1.4_
  - _Boundary: EvalSuiteLoader_

- [ ] 9.2 (P) Unit tests for the evaluator
  - Test `mustInclude` subsequence match: exact match passes; match with intervening tool passes; out-of-order fails with `Check: "mustInclude"` and the tool name in `Expected`; empty trace fails with `Observed: "(empty trace)"`
  - Test `mustNotInclude`: absent tool passes; present tool fails with `Check: "mustNotInclude"` and the tool name in `Expected`
  - Test `policyOutcome`: correct match passes; wrong value fails with correct `Expected` and `Observed` values; empty trace fails with `Observed: "(empty trace)"`
  - Test `mustNotContainInArgs`: string absent from all rows' Arguments passes; string present in one row's Arguments fails with `Check: "mustNotContainInArgs"`
  - Test all failures collected: case with both `mustInclude` and `policyOutcome` failures returns `CaseResult.Failures` containing both `CheckFailure` entries
  - Observable: `go test -race ./cmd/eval-runner/... -run TestEvaluate` exits 0; no data races reported
  - _Requirements: 5.1, 5.2, 5.3, 5.4, 5.5_
  - _Boundary: Evaluator_

- [ ] 9.3 (P) Unit tests for the reporter
  - Test all-pass input: `GenerateReport` produces a string whose exact last line is `"PASS"`; `ExitCode` returns 0
  - Test one-failure input: last line is exactly `"FAIL: 1 case(s) failed"`; `ExitCode` returns 1
  - Test two-failure input: last line is `"FAIL: 2 case(s) failed"`
  - Test summary table contains a row for every case name in the input
  - Test pass rate line is present: `"N/M cases passed"` with correct counts
  - Observable: `go test ./cmd/eval-runner/... -run TestReporter` exits 0; string assertions on all final-line and table-content expectations pass
  - _Requirements: 6.1, 6.2, 6.3, 6.4_
  - _Boundary: Reporter_

- [ ] 9.4 Integration test for CaseRunner trace capture
  - Spin up a real Postgres instance in `TestMain` using testcontainers-go or a `docker run postgres:16` helper; apply the existing `audit_log` schema
  - Pre-insert 3 `audit_log` rows for session_id `"test-session-1"` with distinct `tool_name`, `decision`, and `arguments` values and increasing `decided_at` timestamps
  - Run a minimal HTTP stub server that returns `{"session_id":"test-session-1"}` on `POST /trigger`
  - Call `CaseRunner.Run` and assert the returned `[]TraceRow` has 3 entries in `decided_at` order with correct `ToolName`, `Decision`, and `Arguments` bytes
  - Test DB connection failure: pass an invalid DSN and verify `Run` returns a non-nil error without panicking
  - Observable: `go test ./cmd/eval-runner/... -run TestCaseRunner` exits 0; trace rows match the pre-inserted data exactly
  - _Requirements: 4.1, 4.2, 4.3_
  - _Boundary: CaseRunner_

- [ ] 9.5 End-to-end validation via `make demo`
  - Run `make demo` against the full `deploy/docker-compose.yml` stack
  - Verify all 4 EvalSuite cases pass: `audit_log` contains a row with the expected `tool_name` and `decision` for each case's `session_id`
  - Verify PII redaction: the `audit_log` `arguments` for `send_slack_message` contains `"***REDACTED***"` and does not contain `"123-45-6789"`
  - Verify approval case: mock Slack auto-approves within 5 seconds; eval-runner records the trace with `decision=approvalRequired`; the gateway's log shows HTTP 200 for the `/slack/actions` approve action
  - Verify the final report line is exactly `"PASS"` and `make demo` exits 0
  - Observable: `make demo` exits 0; printed report ends with `"PASS"`; all 4 case status lines show `[PASS]`
  - _Depends: 7.1, 7.3, 8.1_
  - _Requirements: 7.1, 7.2, 7.3, 7.4, 7.5, 10.1, 10.2, 10.3_
