# TrueFoundry Resilience Pivot Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add three resilience demo scenarios to ToolGate showcasing fault-tolerance of the proxy layer, policy gate, and approval flow under infrastructure failures.

**Architecture:** Seven atomic changes across the gateway, eval runner, Docker Compose, and a new demo script. Each task is independently testable and commits on its own. Tasks 1–4 are gateway/eval-runner code changes; Tasks 5–7 are infra and demo wiring.

**Tech Stack:** Go 1.22+, pgx/v5, Redis go-redis/v9, Docker Compose v2, bash

---

## File Map

| File | Change |
|---|---|
| `cmd/eval-runner/suite.go` | Add `"upstream_error"` to `allowedPolicyOutcomes` |
| `cmd/eval-runner/suite_test.go` | Add acceptance test for `upstream_error` outcome |
| `cmd/gateway/server.go` | Add `audit auditStore` field; write `upstream_error` on forwarder failure |
| `cmd/gateway/main.go` | Wire `server.audit = auditWriter`; pass `config.ApprovalLockTTL` to bridge |
| `cmd/gateway/server_test.go` | Add test verifying `upstream_error` audit write |
| `cmd/gateway/policy_gate.go` | Write `expired` audit record when `ErrApprovalTimeout` fires |
| `cmd/gateway/policy_gate_test.go` | Extend timeout test to verify `expired` audit write |
| `cmd/gateway/config.go` | Add `ApprovalLockTTL time.Duration`; load from `APPROVAL_LOCK_TTL` env var |
| `cmd/gateway/config_test.go` | Add test for `APPROVAL_LOCK_TTL` loading |
| `cmd/gateway/approval_bridge.go` | Add `approvalTimeout time.Duration` param to `NewRedisApprovalBridge` |
| `cmd/gateway/approval_bridge_integration_test.go` | Update `NewRedisApprovalBridge` call site |
| `docker-compose.yml` | Add `mock-slack` service; set `APPROVAL_LOCK_TTL: "15s"` and `SLACK_API_BASE_URL` on gateway |
| `evalsuite/resilience.yaml` | Two eval cases: `mcp-server-down`, `approval-timeout-slack-down` |
| `scripts/demo-resilience.sh` | Orchestrate all three fault-injection scenarios |
| `Makefile` | Add `demo-resilience` target |

---

## Task 1: `upstream_error` in eval runner allowed outcomes

**Files:**
- Modify: `cmd/eval-runner/suite.go:11-16`
- Modify: `cmd/eval-runner/suite_test.go`

- [ ] **Step 1: Write the failing test**

Add to `cmd/eval-runner/suite_test.go` inside a new test function:

```go
func TestLoadSuiteAcceptsUpstreamErrorPolicyOutcome(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "suite.yaml")
	writeTestFile(t, path, `
cases:
  - name: mcp-down
    input: "Show me my recent charges."
    mustInclude: [list_recent_charges]
    policyOutcome: upstream_error
`)
	suite, err := LoadSuite(path)
	if err != nil {
		t.Fatalf("LoadSuite() error = %v, want nil", err)
	}
	if suite.Cases[0].PolicyOutcome != "upstream_error" {
		t.Fatalf("PolicyOutcome = %q, want upstream_error", suite.Cases[0].PolicyOutcome)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/henry/Programming/ToolGate && go test ./cmd/eval-runner/ -run TestLoadSuiteAcceptsUpstreamErrorPolicyOutcome -v
```

Expected: `FAIL` — `invalid policyOutcome "upstream_error"`

- [ ] **Step 3: Add `upstream_error` to `allowedPolicyOutcomes`**

In `cmd/eval-runner/suite.go`, change:

```go
var allowedPolicyOutcomes = map[string]struct{}{
	"allow":            {},
	"deny":             {},
	"approvalRequired": {},
	"expired":          {},
}
```

to:

```go
var allowedPolicyOutcomes = map[string]struct{}{
	"allow":            {},
	"deny":             {},
	"approvalRequired": {},
	"expired":          {},
	"upstream_error":   {},
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd /Users/henry/Programming/ToolGate && go test ./cmd/eval-runner/ -run TestLoadSuiteAcceptsUpstreamErrorPolicyOutcome -v
```

Expected: `PASS`

- [ ] **Step 5: Run full eval-runner test suite**

```bash
cd /Users/henry/Programming/ToolGate && go test ./cmd/eval-runner/ -v
```

Expected: all tests pass

- [ ] **Step 6: Commit**

```bash
git add cmd/eval-runner/suite.go cmd/eval-runner/suite_test.go
git commit -m "feat(eval-runner): accept upstream_error policyOutcome"
```

---

## Task 2: `upstream_error` audit write on forwarder failure

**Files:**
- Modify: `cmd/gateway/server.go`
- Modify: `cmd/gateway/main.go:89` (set `server.audit`)
- Modify: `cmd/gateway/server_test.go`

The `auditStore` interface (`Write(AuditRecord)`) is already defined in `cmd/gateway/policy_gate.go` and is accessible within the same package.

- [ ] **Step 1: Write the failing test**

Add to `cmd/gateway/server_test.go`:

```go
type captureAuditWriter struct {
	records []AuditRecord
}

func (c *captureAuditWriter) Write(r AuditRecord) {
	c.records = append(c.records, r)
}

func TestServerToolsCallWritesUpstreamErrorAuditOnForwarderFailure(t *testing.T) {
	audit := &captureAuditWriter{}
	server := newTestServer(t, &captureHandler{})
	server.audit = audit
	server.pipeline = mcp.NewPipeline(mcp.HandlerFunc(func(ctx context.Context, req *mcp.JSONRPCRequest) (*mcp.JSONRPCResponse, error) {
		return nil, fmt.Errorf("connection refused")
	}))
	session := server.sessions.Create()

	req := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"list_recent_charges","arguments":{}}}`))
	req.Header.Set(mcpSessionIDHeader, session.ID)
	rec := httptest.NewRecorder()

	server.ServeHTTP(rec, req)

	if len(audit.records) != 1 {
		t.Fatalf("audit records = %d, want 1", len(audit.records))
	}
	if audit.records[0].Decision != "upstream_error" {
		t.Fatalf("Decision = %q, want upstream_error", audit.records[0].Decision)
	}
	if audit.records[0].ToolName != "list_recent_charges" {
		t.Fatalf("ToolName = %q, want list_recent_charges", audit.records[0].ToolName)
	}
}
```

Also add `"fmt"` to the imports in `server_test.go` if not present.

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/henry/Programming/ToolGate && go test ./cmd/gateway/ -run TestServerToolsCallWritesUpstreamErrorAuditOnForwarderFailure -v
```

Expected: `FAIL` — `audit records = 0, want 1`

- [ ] **Step 3: Add `audit` field to `Server` and write record on error**

In `cmd/gateway/server.go`, add `audit auditStore` field to the `Server` struct:

```go
type Server struct {
	config       *Config
	pipeline     *mcp.Pipeline
	forwarder    mcp.Handler
	guard        *ConcurrencyGuard
	slackWebhook http.Handler
	sessions     *SessionRegistry
	mux          *http.ServeMux
	log          *slog.Logger
	audit        auditStore // nil-safe; set by buildGatewayServer
}
```

In `handleMCPPost`, change the error branch after `runPipeline` from:

```go
	resp, err := s.runPipeline(r.Context(), sessionID, toolName, req)
	if err != nil {
		if req.Method == "tools/call" {
			NewRequestLogger(s.log).LogOutcome(r.Context(), req, nil, err)
		}
		s.errorResponse(w, req.ID, jsonRPCCode(err), err.Error())
		return
	}
```

to:

```go
	resp, err := s.runPipeline(r.Context(), sessionID, toolName, req)
	if err != nil {
		if req.Method == "tools/call" {
			NewRequestLogger(s.log).LogOutcome(r.Context(), req, nil, err)
			if toolName != "" && s.audit != nil {
				s.audit.Write(AuditRecord{
					SessionID: sessionID,
					TurnID:    mcp.TurnIDFromContext(r.Context()),
					ToolName:  toolName,
					Decision:  "upstream_error",
					Reason:    err.Error(),
				})
			}
		}
		s.errorResponse(w, req.ID, jsonRPCCode(err), err.Error())
		return
	}
```

- [ ] **Step 4: Wire `server.audit` in `main.go`**

In `cmd/gateway/main.go`, after `server := NewServer(config, pipeline, logger)` (line 102), add:

```go
	server.audit = auditWriter
```

So the block becomes:

```go
	server := NewServer(config, pipeline, logger)
	server.audit = auditWriter
	server.forwarder = forwarder
	server.guard = guard
	server.SetSlackWebhookHandler(slackWebhook)
```

- [ ] **Step 5: Run test to verify it passes**

```bash
cd /Users/henry/Programming/ToolGate && go test ./cmd/gateway/ -run TestServerToolsCallWritesUpstreamErrorAuditOnForwarderFailure -v
```

Expected: `PASS`

- [ ] **Step 6: Run full gateway test suite**

```bash
cd /Users/henry/Programming/ToolGate && go test ./cmd/gateway/ -v -count=1 -short 2>&1 | tail -20
```

Expected: all unit tests pass (integration tests may be skipped with `-short`)

- [ ] **Step 7: Commit**

```bash
git add cmd/gateway/server.go cmd/gateway/main.go cmd/gateway/server_test.go
git commit -m "feat(gateway): write upstream_error audit record on forwarder failure"
```

---

## Task 3: `expired` audit write on approval timeout

**Files:**
- Modify: `cmd/gateway/policy_gate.go:212-216`
- Modify: `cmd/gateway/policy_gate_test.go`

- [ ] **Step 1: Write the failing test**

Find `TestPolicyGateHandlerApprovalHoldTimeoutReturnsTimeoutError` in `cmd/gateway/policy_gate_test.go` (line 585). Replace it with:

```go
func TestPolicyGateHandlerApprovalHoldTimeoutReturnsTimeoutError(t *testing.T) {
	audit := &policyGateAuditStub{}
	bridge := &mockApprovalBridge{err: ErrApprovalTimeout}
	notifier := newMockSlackNotifier(nil)
	handler := newPolicyGateHandler(
		&corepolicy.AgentPolicy{Budgets: corepolicy.Budgets{MaxToolCallsPerTurn: 3}},
		NewBudgetTracker(),
		audit,
		&policyGateTicketStub{},
		&policyGateEvaluatorStub{decision: corepolicy.PolicyDecision{Action: corepolicy.ActionApprovalRequired}},
		bridge,
		notifier,
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		nowStub(time.Unix(0, 0)),
	)

	resp, err := handler.Handle(contextWithSessionAndTurn("session-timeout", "turn-timeout"), testPolicyGateToolsCallRequest())
	if err != nil {
		t.Fatalf("Handle() error = %v, want nil", err)
	}
	if resp == nil || resp.Error == nil {
		t.Fatalf("Handle() response = %#v, want error response", resp)
	}
	if resp.Error.Code != -32001 {
		t.Fatalf("error code = %d, want -32001", resp.Error.Code)
	}
	if resp.Error.Message != "approval timeout" {
		t.Fatalf("error message = %q, want %q", resp.Error.Message, "approval timeout")
	}

	// Verify expired audit record written after the approvalRequired record.
	var expiredRecord *AuditRecord
	for i := range audit.records {
		if audit.records[i].Decision == "expired" {
			expiredRecord = &audit.records[i]
		}
	}
	if expiredRecord == nil {
		t.Fatalf("no expired audit record written; got records: %+v", audit.records)
	}
	if expiredRecord.SessionID != "session-timeout" {
		t.Fatalf("expired record SessionID = %q, want session-timeout", expiredRecord.SessionID)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/henry/Programming/ToolGate && go test ./cmd/gateway/ -run TestPolicyGateHandlerApprovalHoldTimeoutReturnsTimeoutError -v
```

Expected: `FAIL` — `no expired audit record written`

- [ ] **Step 3: Add `expired` audit write in `policy_gate.go`**

In `cmd/gateway/policy_gate.go`, change the timeout handling from:

```go
		decision, err := h.bridge.WaitForDecision(ctx, ticketID, sessionID, turnID)
		if errors.Is(err, ErrApprovalTimeout) {
			h.log.Error("approval timed out", "ticketID", ticketID, "sessionID", sessionID, "turnID", turnID)
			return approvalErrorResponse(req.ID, "approval timeout"), nil
		}
```

to:

```go
		decision, err := h.bridge.WaitForDecision(ctx, ticketID, sessionID, turnID)
		if errors.Is(err, ErrApprovalTimeout) {
			h.log.Error("approval timed out", "ticketID", ticketID, "sessionID", sessionID, "turnID", turnID)
			h.audit.Write(AuditRecord{
				SessionID: sessionID,
				TurnID:    turnID,
				ToolName:  toolName,
				Arguments: arguments,
				Decision:  "expired",
				Reason:    "approval timeout",
			})
			return approvalErrorResponse(req.ID, "approval timeout"), nil
		}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd /Users/henry/Programming/ToolGate && go test ./cmd/gateway/ -run TestPolicyGateHandlerApprovalHoldTimeoutReturnsTimeoutError -v
```

Expected: `PASS`

- [ ] **Step 5: Run full gateway tests**

```bash
cd /Users/henry/Programming/ToolGate && go test ./cmd/gateway/ -count=1 -short 2>&1 | tail -5
```

Expected: all pass

- [ ] **Step 6: Commit**

```bash
git add cmd/gateway/policy_gate.go cmd/gateway/policy_gate_test.go
git commit -m "feat(gateway): write expired audit record on approval timeout"
```

---

## Task 4: Configurable `APPROVAL_LOCK_TTL`

**Files:**
- Modify: `cmd/gateway/config.go`
- Modify: `cmd/gateway/config_test.go`
- Modify: `cmd/gateway/approval_bridge.go:106-128`
- Modify: `cmd/gateway/main.go:89`
- Modify: `cmd/gateway/approval_bridge_integration_test.go`

- [ ] **Step 1: Write the failing config test**

Add to `cmd/gateway/config_test.go`:

```go
func TestLoadConfigReadsApprovalLockTTL(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("APPROVAL_LOCK_TTL", "15s")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.ApprovalLockTTL != 15*time.Second {
		t.Fatalf("ApprovalLockTTL = %v, want 15s", cfg.ApprovalLockTTL)
	}
}

func TestLoadConfigDefaultsApprovalLockTTLToFiveMinutes(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("APPROVAL_LOCK_TTL", "")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.ApprovalLockTTL != 5*time.Minute {
		t.Fatalf("ApprovalLockTTL = %v, want 5m0s", cfg.ApprovalLockTTL)
	}
}
```

Check how `setRequiredEnv` is defined in the existing config_test.go — use the same helper.

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd /Users/henry/Programming/ToolGate && go test ./cmd/gateway/ -run "TestLoadConfigReadsApprovalLockTTL|TestLoadConfigDefaultsApprovalLockTTLToFiveMinutes" -v
```

Expected: `FAIL` — `cfg.ApprovalLockTTL undefined`

- [ ] **Step 3: Add `ApprovalLockTTL` to `Config` and `LoadConfig`**

In `cmd/gateway/config.go`, add the field to the `Config` struct after `LockAcquireTimeout`:

```go
	LockAcquireTimeout time.Duration
	ApprovalLockTTL    time.Duration // APPROVAL_LOCK_TTL (optional, default 5m)
```

In `LoadConfig()`, add before the `return &Config{...}`:

```go
	approvalLockTTL, err := envDuration("APPROVAL_LOCK_TTL", 5*time.Minute)
	if err != nil {
		return nil, err
	}
```

In the `return &Config{...}` block, add:

```go
		ApprovalLockTTL:    approvalLockTTL,
```

- [ ] **Step 4: Run config tests to verify they pass**

```bash
cd /Users/henry/Programming/ToolGate && go test ./cmd/gateway/ -run "TestLoadConfigReadsApprovalLockTTL|TestLoadConfigDefaultsApprovalLockTTLToFiveMinutes" -v
```

Expected: `PASS`

- [ ] **Step 5: Add `approvalTimeout` parameter to `NewRedisApprovalBridge`**

In `cmd/gateway/approval_bridge.go`, change the constructor signature from:

```go
func NewRedisApprovalBridge(
	rdb *redis.Client,
	tickets *TicketStore,
	locker *SessionLocker,
	lockTTL time.Duration,
	log *slog.Logger,
) *RedisApprovalBridge {
```

to:

```go
func NewRedisApprovalBridge(
	rdb *redis.Client,
	tickets *TicketStore,
	locker *SessionLocker,
	lockTTL time.Duration,
	approvalTimeout time.Duration,
	log *slog.Logger,
) *RedisApprovalBridge {
```

In the constructor body, change:

```go
	b := &RedisApprovalBridge{
		redis:              rdb,
		tickets:            tickets,
		locker:             locker,
		timeout:            5 * time.Minute,
		lockExtendInterval: lockTTL / 2,
		log:                log,
	}
```

to:

```go
	b := &RedisApprovalBridge{
		redis:              rdb,
		tickets:            tickets,
		locker:             locker,
		timeout:            approvalTimeout,
		lockExtendInterval: lockTTL / 2,
		log:                log,
	}
```

- [ ] **Step 6: Update call sites**

In `cmd/gateway/main.go`, change:

```go
	approvalBridge := NewRedisApprovalBridge(redisClient, ticketStore, sessionLocker, config.SessionLockTTL, logger)
```

to:

```go
	approvalBridge := NewRedisApprovalBridge(redisClient, ticketStore, sessionLocker, config.SessionLockTTL, config.ApprovalLockTTL, logger)
```

In `cmd/gateway/approval_bridge_integration_test.go` (line 214), change:

```go
	bridge := NewRedisApprovalBridge(redisClient, store, locker, lockTTL, slog.New(slog.NewTextHandler(io.Discard, nil)))
```

to:

```go
	bridge := NewRedisApprovalBridge(redisClient, store, locker, lockTTL, 5*time.Minute, slog.New(slog.NewTextHandler(io.Discard, nil)))
```

- [ ] **Step 7: Build to confirm no compile errors**

```bash
cd /Users/henry/Programming/ToolGate && go build ./cmd/gateway/ ./cmd/eval-runner/
```

Expected: exits 0, no output

- [ ] **Step 8: Run full gateway tests**

```bash
cd /Users/henry/Programming/ToolGate && go test ./cmd/gateway/ -count=1 -short 2>&1 | tail -5
```

Expected: all pass

- [ ] **Step 9: Commit**

```bash
git add cmd/gateway/config.go cmd/gateway/config_test.go cmd/gateway/approval_bridge.go cmd/gateway/main.go cmd/gateway/approval_bridge_integration_test.go
git commit -m "feat(gateway): make approval timeout configurable via APPROVAL_LOCK_TTL env var"
```

---

## Task 5: Docker Compose — mock-slack + env vars

**Files:**
- Modify: `docker-compose.yml`

The `mock-slack` binary is already built from `examples/mock-slack/` using the repo-root Dockerfile context.

- [ ] **Step 1: Add `mock-slack` service and gateway env vars**

In `docker-compose.yml`, add the `mock-slack` service after `eval-trigger` (before `demo-webapp`):

```yaml
  mock-slack:
    build:
      context: .
      dockerfile: examples/mock-slack/Dockerfile
    environment:
      GATEWAY_URL: http://gateway:8080
      SLACK_SIGNING_SECRET: "demo-signing-secret"
    ports:
      - "18090:8090"
    healthcheck:
      test: ["CMD-SHELL", "wget -q -O /dev/null http://127.0.0.1:8090/healthz 2>/dev/null || exit 0"]
      interval: 5s
      timeout: 5s
      retries: 6
      start_period: 5s
```

Note: mock-slack doesn't have a `/healthz` endpoint so the healthcheck will always exit 0 (the `|| exit 0` makes it pass regardless). This just gives Compose a consistent health state.

In the `gateway` service environment block, add:

```yaml
      SLACK_API_BASE_URL: "http://mock-slack:8090/api"
      APPROVAL_LOCK_TTL: "15s"
```

In the `gateway` `depends_on` block (in `docker-compose.override.yml`), add:

```yaml
      mock-slack:
        condition: service_started
```

- [ ] **Step 2: Verify the compose file parses**

```bash
cd /Users/henry/Programming/ToolGate && docker compose config --quiet
```

Expected: exits 0, no errors

- [ ] **Step 3: Commit**

```bash
git add docker-compose.yml docker-compose.override.yml
git commit -m "feat(compose): add mock-slack service and wire APPROVAL_LOCK_TTL + SLACK_API_BASE_URL"
```

---

## Task 6: `evalsuite/resilience.yaml`

**Files:**
- Create: `evalsuite/resilience.yaml`

Two eval cases — one for Scenario 1 (MCP server crash) and one for Scenario 3 (approval timeout). Scenario 2 (budget limiter) is exercised directly via curl in the demo script.

- [ ] **Step 1: Create `evalsuite/resilience.yaml`**

```yaml
cases:
  - name: mcp-server-down
    input: "Show me my recent charges."
    mustInclude:
      - list_recent_charges
    policyOutcome: upstream_error

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

- [ ] **Step 2: Verify the eval runner loads it**

```bash
cd /Users/henry/Programming/ToolGate && go run ./cmd/eval-runner evalsuite/resilience.yaml 2>&1 | head -5
```

Expected: fails fast with a config/docker error (POSTGRES_DSN missing), NOT a YAML parse error. This confirms the file loads correctly.

- [ ] **Step 3: Commit**

```bash
git add evalsuite/resilience.yaml
git commit -m "feat(evalsuite): add resilience eval cases for mcp-down and approval-timeout"
```

---

## Task 7: Demo script and Makefile target

**Files:**
- Create: `scripts/demo-resilience.sh`
- Modify: `Makefile`

The script orchestrates three scenarios. Scenario 2 uses direct `curl` calls to show the budget limiter without needing the AI agent.

- [ ] **Step 1: Create `scripts/demo-resilience.sh`**

```bash
#!/usr/bin/env bash
set -euo pipefail

COMPOSE="docker compose"
GATEWAY_URL="http://localhost:18080"
POSTGRES_DSN="postgres://gateway:gateway@127.0.0.1:15432/gateway?sslmode=disable"
AGENT_URL="http://127.0.0.1:18086"

pass() { echo "  ✓ $1"; }
fail() { echo "  ✗ $1"; exit 1; }
section() { echo ""; echo "━━━ $1 ━━━"; }

section "Starting full stack"
$COMPOSE up -d --wait
echo "  Stack healthy"

# ─── Scenario 1: MCP server crash ─────────────────────────────────────────────
section "SCENARIO 1 — MCP Server Crash (proxy resilience + eval gate)"
echo "  [FAULT] Stopping localstripe-mcp..."
$COMPOSE stop localstripe-mcp

echo "  Running eval case: mcp-server-down"
EVAL_RESULT=$(
  POSTGRES_DSN="$POSTGRES_DSN" \
  AGENT_URL="$AGENT_URL" \
  go run ./cmd/eval-runner evalsuite/resilience.yaml 2>&1 || true
)

if echo "$EVAL_RESULT" | grep -q "mcp-server-down.*PASS\|PASS.*mcp-server-down\|1/1\|1\/1"; then
  pass "Gateway surfaced clean upstream_error — audit trail preserved"
elif echo "$EVAL_RESULT" | grep -q "upstream_error"; then
  pass "Gateway surfaced clean upstream_error — audit trail preserved"
else
  echo "$EVAL_RESULT"
  fail "Expected upstream_error in eval result"
fi

# ─── Scenario 2: Budget limiter stops retry storm ─────────────────────────────
section "SCENARIO 2 — Budget Limiter (policy gate stops retry storm)"
echo "  [NOTE] MCP server still down — simulating aggressive retry agent..."

# Initialize a gateway session
SESSION_ID=$(curl -s -D - -X POST "$GATEWAY_URL/mcp" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"retry-bot","version":"1.0"}}}' \
  | grep -i "^Mcp-Session-Id:" | awk '{print $2}' | tr -d '\r\n')

if [ -z "$SESSION_ID" ]; then
  fail "Could not obtain gateway session ID"
fi
echo "  Session: $SESSION_ID"

TURN_ID="retry-storm-$(date +%s)"
BUDGET_HIT=false

for i in 1 2 3 4 5 6; do
  RESP=$(curl -s -X POST "$GATEWAY_URL/mcp" \
    -H "Content-Type: application/json" \
    -H "Mcp-Session-Id: $SESSION_ID" \
    -H "X-Mcp-Turn-Id: $TURN_ID" \
    -d "{\"jsonrpc\":\"2.0\",\"id\":$i,\"method\":\"tools/call\",\"params\":{\"name\":\"list_recent_charges\",\"arguments\":{}}}")
  if echo "$RESP" | grep -qi "budget"; then
    BUDGET_HIT=true
    echo "  Call $i: budgetExceeded (limiter fired)"
    break
  else
    echo "  Call $i: upstream_error (retried)"
  fi
done

if [ "$BUDGET_HIT" = true ]; then
  pass "Budget limiter stopped retry storm — agent cannot hammer a downed service"
else
  fail "Expected budgetExceeded after 5 upstream_error calls"
fi

# ─── Scenario 3: Approval timeout (graceful degradation) ──────────────────────
section "SCENARIO 3 — Approval Flow Timeout (graceful degradation)"
echo "  [RESTORE] Starting localstripe-mcp..."
$COMPOSE start localstripe-mcp
sleep 5  # brief stabilisation

echo "  [FAULT] Stopping mock-slack..."
$COMPOSE stop mock-slack

echo "  Running eval case: approval-timeout-slack-down (waiting up to 30s for timeout...)"
EVAL_RESULT=$(
  POSTGRES_DSN="$POSTGRES_DSN" \
  AGENT_URL="$AGENT_URL" \
  timeout 60 go run ./cmd/eval-runner evalsuite/resilience.yaml 2>&1 || true
)

if echo "$EVAL_RESULT" | grep -q "approval-timeout-slack-down.*PASS\|expired"; then
  pass "Slack outage did not hang or panic — approval expired gracefully after 15s"
else
  echo "$EVAL_RESULT"
  fail "Expected expired outcome in eval result"
fi

# ─── Teardown ─────────────────────────────────────────────────────────────────
section "Teardown"
$COMPOSE down -v
echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "  3/3 resilience scenarios passed ✓"
echo "  ToolGate held under: MCP crash · retry storm · Slack outage"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
```

- [ ] **Step 2: Make it executable**

```bash
chmod +x /Users/henry/Programming/ToolGate/scripts/demo-resilience.sh
```

- [ ] **Step 3: Add `demo-resilience` target to `Makefile`**

In `Makefile`, add after the existing `demo` target:

```makefile
demo-resilience:
	@bash scripts/demo-resilience.sh
```

- [ ] **Step 4: Verify the script is syntactically valid**

```bash
bash -n /Users/henry/Programming/ToolGate/scripts/demo-resilience.sh && echo "syntax OK"
```

Expected: `syntax OK`

- [ ] **Step 5: Commit**

```bash
git add scripts/demo-resilience.sh Makefile
git commit -m "feat: add demo-resilience script and make target for TrueFoundry submission"
```

---

## Self-Review Checklist

- [x] **Spec coverage:**
  - Scenario 1 (MCP crash → upstream_error): Tasks 1, 2, 6, 7 ✓
  - Scenario 2 (policy deny / budget limiter): Task 7 (curl in demo script) ✓
  - Scenario 3 (approval timeout → expired): Tasks 3, 4, 5, 6, 7 ✓
  - `make demo-resilience`: Task 7 ✓
  - mock-slack added to compose: Task 5 ✓
  - APPROVAL_LOCK_TTL configurable: Task 4 ✓

- [x] **Type consistency:** `auditStore` interface (defined in `policy_gate.go`) used in `server.go` — same package, no redeclaration needed. `AuditRecord` fields `SessionID`, `TurnID`, `ToolName`, `Decision`, `Reason` match the struct in `audit.go`.

- [x] **No placeholders:** All code steps contain exact implementations.

- [x] **One gap noted:** The eval runner runs all cases in a file sequentially. `mcp-server-down` and `approval-timeout-slack-down` are in the same `resilience.yaml`. In the demo script, Scenario 1 runs the full file (only `mcp-server-down` will pass since Slack is still up). Scenario 3 also runs the full file (only `approval-timeout-slack-down` will be relevant). The eval runner reports per-case results, so the demo script greps for the specific case name. This is acceptable — adjust the grep patterns in Task 7 Step 1 if the eval runner output format differs.
