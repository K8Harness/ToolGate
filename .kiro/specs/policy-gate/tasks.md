# Implementation Plan

- [ ] 1. Foundation — module dependencies, error code, config, infrastructure, schema
- [x] 1.1 Add Go module dependencies for YAML and Postgres
  - Run `go get gopkg.in/yaml.v3` and `go get github.com/jackc/pgx/v5` (which provides `github.com/jackc/pgx/v5/pgxpool`); run `go mod tidy`
  - Commit updated `go.mod` and `go.sum`
  - Observable: `go build ./...` succeeds with stub imports of `gopkg.in/yaml.v3` and `github.com/jackc/pgx/v5/pgxpool`; `go.sum` contains pinned versions of both modules
  - _Requirements: 1.5, 8.4_
  - _Boundary: Module dependencies_

- [x] 1.2 (P) Add policy-denied JSON-RPC error code constant
  - Add `CodePolicyDenied = -32001` alongside the existing `CodeParseError` and `CodeInternalError` constants in `core/mcp`
  - Observable: importing `core/mcp` from `cmd/gateway` exposes `CodePolicyDenied`; `go build ./...` succeeds
  - _Requirements: 5.1_
  - _Boundary: core/mcp.types_

- [x] 1.3 (P) Extend gateway configuration with policy and Postgres environment variables
  - Add `PolicyFilePath` (env `POLICY_FILE`, default `"policy.yaml"`) and `PostgresDSN` (env `POSTGRES_DSN`, required) to the `Config` struct
  - `LoadConfig` returns a non-nil error when `POSTGRES_DSN` is unset; the default policy file path is logged at startup-info level when the env var is absent
  - Observable: starting the gateway without `POSTGRES_DSN` exits non-zero with an error message naming the missing variable; starting with only `POSTGRES_DSN` set yields a `Config` whose `PolicyFilePath` is `"policy.yaml"`
  - _Requirements: 1.1, 1.2, 8.4_
  - _Boundary: Config_

- [x] 1.4 (P) Define Docker Compose service for Postgres and gateway environment wiring
  - Add a `postgres:16` service with a persistent volume, container health check, and credentials matching the documented `POSTGRES_DSN`
  - Wire the gateway service environment to point `POSTGRES_DSN` at the compose-internal Postgres host
  - Observable: `docker compose config` validates without errors; `docker compose up postgres` reports the service as `healthy`
  - _Requirements: 8.4_
  - _Boundary: Infrastructure_

- [x] 1.5 Initialize Postgres connection pool and apply idempotent audit_log + ticket schema migrations
  - Build a `pgxpool` with `MaxConns: 5`, `MinConns: 1`, `MaxConnIdleTime: 5min`, `HealthCheckPeriod: 1min`; verify connectivity with `Ping` at startup
  - Apply `CREATE TABLE IF NOT EXISTS` for `audit_log` (with the `(session_id, turn_id)` index and `decided_at TIMESTAMPTZ NOT NULL DEFAULT NOW()` column) and `ticket` (with the `(status, expires_at)` index) per the physical data model in design.md
  - Schema must include the `ticket.status` CHECK constraint covering `pending/approved/rejected/expired/cancelled` and the `audit_log.decision` CHECK constraint covering `allow/deny/approvalRequired/budgetExceeded`
  - Observable: invoking the migration on an empty database yields the two tables with the expected columns, indexes, and check constraints; re-invocation is a no-op; `audit_log.decided_at` defaults to the Postgres server clock
  - _Requirements: 8.2, 8.4, 9.1_
  - _Boundary: DB_

- [ ] 2. Core — policy engine and Postgres-backed stores
- [x] 2.1 (P) Build the in-process policy package: types, YAML loader, ordered evaluator
  - Define `Action`, `PolicyRule`, `Budgets`, `AgentPolicy`, and `PolicyDecision` types with YAML struct tags matching the design contract
  - Implement `LoadPolicy` with strict YAML decoding (`KnownFields(true)`) and a post-decode validation pass that rejects empty `Tool` names, invalid `Action` values, and a `DefaultAction` of `approvalRequired`
  - Implement `Evaluate` to return the first matching rule's decision by exact tool-name equality, or `DefaultAction` when no rule matches; never returns an error
  - Observable: a sample policy YAML with three rules (allow / deny / approvalRequired) loads cleanly via `LoadPolicy`; `Evaluate` returns the documented decision for matching and non-matching tool names
  - _Requirements: 1.3, 1.4, 1.5, 3.1, 3.3, 3.4_
  - _Boundary: core/policy_

- [x] 2.2 (P) Build the asynchronous audit log writer
  - Define `AuditRecord` (sessionID, turnID, toolName, arguments, decision, reason) and an `AuditWriter` that owns a buffered channel of capacity 256 and a `Start(ctx)` goroutine that drains the channel into `audit_log` via the configured `pgxpool`
  - `Write` is non-blocking: it enqueues on the channel via `select { case ch <- r: default: warn }`; if the channel is full, log a WARN and drop the record
  - The drain goroutine inserts each record using the documented SQL (`INSERT INTO audit_log (session_id, turn_id, tool_name, arguments, decision, reason) VALUES ($1..$6)`) and exits cleanly when the supplied context is cancelled
  - Observable: with a stub pool, calling `Write` on a full channel does not block and emits a WARN log; calling `Write` under capacity results in an `INSERT` executed by the worker goroutine and a row whose `decided_at` is populated by Postgres
  - _Requirements: 8.1, 8.2, 8.3_
  - _Depends: 1.5_
  - _Boundary: AuditWriter_

- [x] 2.3 (P) Build the synchronous ticket stub store
  - Define `TicketRecord` (sessionID, turnID, toolName, arguments, expiresAt) and `TicketStore.Insert` that performs a synchronous `INSERT INTO ticket (...) VALUES (...) RETURNING id` against the configured `pgxpool`
  - Caller is responsible for setting `ExpiresAt` — the store does not invent timing or compute defaults
  - Insert errors are returned to the caller (for WARN logging upstream); the store does not retry
  - Observable: against a real Postgres, `Insert` returns a non-empty UUID and the inserted row has `status='pending'`, `expires_at` populated, and `created_at` defaulted by Postgres
  - _Requirements: 6.2_
  - _Depends: 1.5_
  - _Boundary: TicketStore_

- [x] 2.4 Build the policy gate pipeline handler with embedded budget tracker
  - Implement `BudgetTracker` with a `sync.Mutex`-guarded `map[string]int` (key `sessionID + ":" + turnID`) and an `IncrementAndGet(sessionID, turnID) int` method that atomically increments and returns the new count
  - Implement `PolicyGateHandler.Handle` following the design's execution sequence: (a) passthrough non-`tools/call` requests with `(nil, nil)`; (b) extract `sessionID`/`turnID` from context using `mcp.SessionIDFromContext` / `mcp.TurnIDFromContext`; (c) parse `toolName` and raw `arguments` from `req.Params` (return `-32603` on malformed); (d) increment budget and, if over `policy.Budgets.MaxToolCallsPerTurn`, write a `budgetExceeded` audit record with `Reason = "maxToolCallsPerTurn exceeded"` and return `-32001`; (e) otherwise call `Evaluate`, write the audit record with the evaluated decision before returning, and dispatch on the decision
  - Decision dispatch: `allow` → return `(nil, nil)`; `deny` → return `NewErrorResponse(req.ID, CodePolicyDenied, "denied by policy")`; `approvalRequired` → call `TicketStore.Insert(ctx, TicketRecord{..., ExpiresAt: time.Now().Add(5 * time.Minute)})`, log WARN on insert error, return a synthetic pending response of shape `{"jsonrpc":"2.0","id":<original>,"result":{"status":"pending","message":"tool call requires human approval"}}`
  - Audit `Write` is called in every decision branch (allow, deny, approvalRequired, budgetExceeded) before the return
  - Observable: feeding a `tools/call` into `Handle` with stub dependencies returns the documented response for each of the four decision branches and triggers exactly one `AuditWriter.Write` per branch; a non-`tools/call` request returns `(nil, nil)` with no audit write and no budget increment
  - _Requirements: 2.1, 3.1, 3.2, 4.1, 5.1, 5.2, 6.1, 6.2, 6.3, 7.1, 7.2, 7.3, 8.1_
  - _Depends: 1.2_
  - _Boundary: PolicyGateHandler, BudgetTracker_

- [ ] 3. Integration — startup sequence and pipeline registration
- [ ] 3.1 Wire policy loading, DB pool, schema migration, and PolicyGateHandler into the gateway binary
  - In `main.go`, sequence: `LoadConfig()` → `LoadPolicy(cfg.PolicyFilePath)` → `NewDBPool(ctx, cfg.PostgresDSN)` → `MigrateSchema(ctx, pool)` → construct `BudgetTracker`, `AuditWriter` (call `Start(ctx)`), `TicketStore`, `PolicyGateHandler` → register pipeline handlers via `pipeline.Use` in the order `RequestLogger`, `ContextInjector`, `PolicyGateHandler`, with `UpstreamForwarder` set as the terminal handler
  - Any failure in policy loading, pool initialization, ping, or schema migration must call `log.Fatalf` with a human-readable message identifying the failed check, before any port is bound
  - Observable: running the binary with valid `POLICY_FILE` + `POSTGRES_DSN` against a reachable Postgres binds port 8080 and serves; running with an unreachable Postgres exits non-zero before binding the port; running with a missing policy file exits non-zero before binding the port
  - _Requirements: 1.3, 9.1, 9.2_
  - _Depends: 1.1, 1.3, 1.5, 2.1, 2.2, 2.3, 2.4_
  - _Boundary: main.go (integration)_

- [ ] 4. Validation — unit, integration, and end-to-end tests
- [ ] 4.1 (P) Unit tests for the policy package
  - `LoadPolicy`: missing file → error containing the path; YAML syntax error → error; unknown YAML field → error (KnownFields); invalid `defaultAction` (e.g., `approvalRequired`) → validation error; valid policy → `AgentPolicy` fields match the input
  - `Evaluate`: first rule matches → returns that action; first rule does not match but second does → returns the second; no rule matches → returns `defaultAction`; empty rules list → returns `defaultAction`
  - Observable: all cases pass in `core/policy/*_test.go` under `go test -race` with no warnings
  - _Requirements: 1.3, 1.4, 1.5, 3.3, 3.4_
  - _Boundary: core/policy_

- [ ] 4.2 (P) Unit tests for BudgetTracker
  - First call returns `1`; N sequential calls return `N`; independent `(sessionID, turnID)` pairs do not interfere with each other's counters
  - Concurrent goroutine increments produce the expected total count with no data-race warnings
  - Observable: `cmd/gateway/budget_test.go` passes under `go test -race` with no warnings
  - _Requirements: 7.1_
  - _Boundary: BudgetTracker_

- [ ] 4.3 (P) Unit tests for PolicyGateHandler decision branches
  - Passthrough: `req.Method = "tools/list"` → returns `(nil, nil)`; `AuditWriter.Write` not called; `BudgetTracker.IncrementAndGet` not called
  - Allow: evaluator returns `allow` → returns `(nil, nil)`; audit written with `decision="allow"`; sessionID/turnID from context appear on the audit record
  - Deny: evaluator returns `deny` → returns `-32001` response with message `"denied by policy"`; audit written with `decision="deny"`; `TicketStore.Insert` not called
  - ApprovalRequired: evaluator returns `approvalRequired` → `TicketStore.Insert` called once with `ExpiresAt ≈ now+5min`; pending response shape matches the design contract; audit written with `decision="approvalRequired"`
  - Budget exceeded: with `MaxToolCallsPerTurn=2`, the third call → returns `-32001` before the evaluator is invoked; audit written with `decision="budgetExceeded"` and `reason="maxToolCallsPerTurn exceeded"`
  - Observable: all five cases pass in `cmd/gateway/policy_gate_test.go` using stub `AuditWriter`, `TicketStore`, `PolicyEvaluator`, and `BudgetTracker`
  - _Requirements: 2.1, 3.2, 4.1, 5.1, 5.2, 6.1, 6.3, 7.2, 7.3, 8.1_
  - _Boundary: PolicyGateHandler_

- [ ] 4.4 (P) Unit tests for AuditWriter
  - Full channel → `Write` returns immediately and emits a WARN log entry (verified by capturing `slog` JSON output); the request path is not blocked
  - Worker goroutine drains queued records to a stub pool and invokes the documented `INSERT` SQL; goroutine exits cleanly within a short timeout when the supplied context is cancelled
  - Observable: `cmd/gateway/audit_test.go` passes under `go test -race`; no goroutine leaks reported
  - _Requirements: 8.1, 8.2, 8.3_
  - _Boundary: AuditWriter_

- [ ] 4.5 Integration tests with real Postgres and fake upstream
  - Stand up the full gateway via `httptest.NewServer` against a real Postgres (testcontainer or compose-managed) and a second `httptest.NewServer` as the fake upstream MCP server
  - Allow flow: `tools/call` matching an allow rule → upstream is reached; `audit_log` row exists with `decision="allow"`, the correct `session_id`, `turn_id`, `tool_name`, and a non-null `decided_at`
  - Deny flow: `tools/call` matching a deny rule → upstream not reached; `-32001` returned; `audit_log` row with `decision="deny"`
  - ApprovalRequired flow: matching rule → `ticket` row inserted with `status="pending"` and `expires_at` within ±5 seconds of `now+5min`; pending response returned to the client; upstream not reached
  - Budget exhaustion: `maxToolCallsPerTurn=2`; third `tools/call` returns `-32001` and produces a `audit_log` row with `decision="budgetExceeded"`
  - Startup failure cases: missing policy file, syntactically invalid YAML, unreachable Postgres → process exits non-zero before binding port (verified by `go build` + subprocess exit code)
  - Non-`tools/call` passthrough: `tools/list` request → upstream reached; no `audit_log` row inserted
  - Observable: all scenarios pass in a single integration test file; assertions query Postgres directly to confirm row presence, column values, and timestamp correctness
  - _Requirements: 1.3, 1.4, 2.1, 4.1, 5.1, 5.2, 6.1, 6.2, 6.3, 7.2, 7.3, 8.1, 8.2, 8.4, 9.1, 9.2_
  - _Boundary: Integration_

- [ ] 4.6 End-to-end demo via Docker Compose with a curated policy.yaml
  - Author a `policy.yaml` with rules: `refund_small → allow`, `refund_large → approvalRequired`, `delete_record → deny`; `defaultAction: deny`; `maxToolCallsPerTurn: 5`
  - Use `docker compose up` to bring up gateway + postgres + a fake upstream MCP server; drive the three scenarios with `curl` or a small Go client
  - `refund_small` → upstream result returned; `audit_log` row present with `decision="allow"`
  - `delete_record` → `-32001` error returned; no `ticket` row written; `audit_log` row with `decision="deny"`
  - `refund_large` → pending response returned; `ticket` row with `status="pending"` and `expires_at ≈ now+5min`
  - Observable: a documented `make demo`-style script runs all three scenarios and prints PASS for each; resulting `audit_log` and `ticket` rows are queryable via `psql` against the compose-managed Postgres
  - _Requirements: 4.1, 5.1, 6.1, 6.2_
  - _Boundary: E2E_
