package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/K8Harness/ToolGate/core/mcp"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPolicyGateIntegrationAllowFlowWritesAuditAndForwards(t *testing.T) {
	pool, serverURL, toolCalls, cleanup := newPolicyGateIntegrationHarness(t, `
defaultAction: deny
budgets:
  maxToolCallsPerTurn: 5
rules:
  - tool: refund_small
    action: allow
`)
	defer cleanup()

	sessionID := initializeSession(t, serverURL)
	rec := postJSON(t, serverURL+"/mcp", sessionID, "turn-allow", `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"refund_small","arguments":{"amount":10}}}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("tools/call status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp mcp.JSONRPCResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response body: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("response error = %+v, want nil", resp.Error)
	}
	if got := toolCalls.Load(); got != 1 {
		t.Fatalf("tool calls = %d, want 1", got)
	}

	record := fetchAuditRecord(t, pool, sessionID, "turn-allow", "refund_small")
	if record.Decision != "allow" {
		t.Fatalf("audit decision = %q, want %q", record.Decision, "allow")
	}
	if record.DecidedAt.IsZero() {
		t.Fatal("audit decided_at is zero, want Postgres timestamp")
	}
}

func TestPolicyGateIntegrationDenyFlowSkipsUpstreamAndWritesAudit(t *testing.T) {
	pool, serverURL, toolCalls, cleanup := newPolicyGateIntegrationHarness(t, `
defaultAction: allow
budgets:
  maxToolCallsPerTurn: 5
rules:
  - tool: delete_record
    action: deny
`)
	defer cleanup()

	sessionID := initializeSession(t, serverURL)
	rec := postJSON(t, serverURL+"/mcp", sessionID, "turn-deny", `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"delete_record","arguments":{"id":"abc"}}}`)

	assertIntegrationErrorCode(t, rec, mcp.CodePolicyDenied)
	if got := toolCalls.Load(); got != 0 {
		t.Fatalf("tool calls = %d, want 0", got)
	}

	record := fetchAuditRecord(t, pool, sessionID, "turn-deny", "delete_record")
	if record.Decision != "deny" {
		t.Fatalf("audit decision = %q, want %q", record.Decision, "deny")
	}
}

func TestPolicyGateIntegrationApprovalRequiredHoldsConnectionAndCreatesTicket(t *testing.T) {
	pool, serverURL, toolCalls, cleanup := newPolicyGateIntegrationHarness(t, `
defaultAction: deny
budgets:
  maxToolCallsPerTurn: 5
rules:
  - tool: refund_large
    action: approvalRequired
`)
	defer cleanup()

	sessionID := initializeSession(t, serverURL)
	before := time.Now().UTC()

	// The handler now blocks waiting for a human decision — send the request asynchronously.
	// We verify the DB side-effects (ticket + audit) while the connection is held open.
	go func() {
		req, err := http.NewRequest(http.MethodPost, serverURL+"/mcp",
			strings.NewReader(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"refund_large","arguments":{"amount":9001}}}`))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set(mcpSessionIDHeader, sessionID)
		req.Header.Set(defaultTurnIDHeader, "turn-approval")
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
		}
	}()

	// Give the handler time to insert the ticket and begin the approval hold.
	time.Sleep(100 * time.Millisecond)

	// Upstream must NOT be called while the approval hold is active.
	if got := toolCalls.Load(); got != 0 {
		t.Fatalf("tool calls = %d, want 0 (upstream must not be called during approval hold)", got)
	}

	// Ticket must be created with pending status while the connection is held open.
	ticket := fetchTicketRecord(t, pool, sessionID, "turn-approval", "refund_large")
	if ticket.Status != "pending" {
		t.Fatalf("ticket status = %q, want %q", ticket.Status, "pending")
	}
	want := before.Add(5 * time.Minute)
	if diff := ticket.ExpiresAt.Sub(want); diff < -5*time.Second || diff > 5*time.Second {
		t.Fatalf("ticket expires_at = %s, want within 5s of %s", ticket.ExpiresAt, want)
	}

	// Audit record must be written before the decision is made.
	record := fetchAuditRecord(t, pool, sessionID, "turn-approval", "refund_large")
	if record.Decision != "approvalRequired" {
		t.Fatalf("audit decision = %q, want %q", record.Decision, "approvalRequired")
	}

	// cleanup() (deferred) closes the test server, cancelling the pending request goroutine.
}

func TestPolicyGateIntegrationBudgetExhaustionWritesBudgetExceededAudit(t *testing.T) {
	pool, serverURL, toolCalls, cleanup := newPolicyGateIntegrationHarness(t, `
defaultAction: allow
budgets:
  maxToolCallsPerTurn: 2
rules:
  - tool: refund_small
    action: allow
`)
	defer cleanup()

	sessionID := initializeSession(t, serverURL)
	for i := range 2 {
		rec := postJSON(t, serverURL+"/mcp", sessionID, "turn-budget", `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"refund_small","arguments":{"amount":10}}}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("warmup call %d status = %d, want %d", i+1, rec.Code, http.StatusOK)
		}
	}

	rec := postJSON(t, serverURL+"/mcp", sessionID, "turn-budget", `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"refund_small","arguments":{"amount":10}}}`)
	assertIntegrationErrorCode(t, rec, mcp.CodePolicyDenied)
	if got := toolCalls.Load(); got != 2 {
		t.Fatalf("tool calls = %d, want 2", got)
	}

	record := fetchAuditRecordByDecision(t, pool, sessionID, "turn-budget", "budgetExceeded")
	if record.Decision != "budgetExceeded" {
		t.Fatalf("audit decision = %q, want %q", record.Decision, "budgetExceeded")
	}
}

func TestPolicyGateIntegrationNonToolCallPassthroughSkipsAudit(t *testing.T) {
	pool, serverURL, toolCalls, cleanup := newPolicyGateIntegrationHarness(t, `
defaultAction: deny
budgets:
  maxToolCallsPerTurn: 5
rules:
  - tool: delete_record
    action: deny
`)
	defer cleanup()

	sessionID := initializeSession(t, serverURL)
	rec := postJSON(t, serverURL+"/mcp", sessionID, "", `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{"cursor":"page-1"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("tools/list status = %d, want %d", rec.Code, http.StatusOK)
	}

	if got := toolCalls.Load(); got != 1 {
		t.Fatalf("tool calls = %d, want 1 tools/list forward", got)
	}
	if got := countAuditRows(t, pool); got != 0 {
		t.Fatalf("audit_log row count = %d, want 0", got)
	}
}

type integrationAuditRecord struct {
	Decision  string
	DecidedAt time.Time
}

type integrationTicketRecord struct {
	Status    string
	ExpiresAt time.Time
}

func newPolicyGateIntegrationHarness(t *testing.T, policyContents string) (*pgxpool.Pool, string, *atomic.Int32, func()) {
	t.Helper()

	ctx := context.Background()
	dsn := testSchemaDSN(t, testPostgresDSN(t))
	pool, err := NewDBPool(ctx, dsn)
	if err != nil {
		t.Fatalf("NewDBPool() error = %v, want nil", err)
	}

	var toolCalls atomic.Int32

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req mcp.JSONRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("Decode upstream request: %v", err)
		}

		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "initialize":
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"server":"ok"}}`))
		default:
			toolCalls.Add(1)
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"ok":true}}`))
		}
	}))

	config := &Config{
		ListenPort:         8080,
		PolicyFilePath:     writePolicyFile(t, policyContents),
		PostgresDSN:        dsn,
		RedisDSN:           testRedisDSN(t),
		UpstreamMCPURL:     upstream.URL,
		TurnIDHeader:       defaultTurnIDHeader,
		UpstreamTimeout:    time.Second,
		SessionTTL:         time.Minute,
		SessionLockTTL:     defaultSessionLockTTL,
		LockAcquireTimeout: defaultLockAcquireTimeout,
		SlackBotToken:      "test-token",
		SlackSigningSecret: "test-secret",
		SlackChannel:       "#test",
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server, cleanupServer, err := buildGatewayServer(ctx, config, logger)
	if err != nil {
		t.Fatalf("buildGatewayServer() error = %v, want nil", err)
	}
	ts := httptest.NewServer(server)

	cleanup := func() {
		ts.Close()
		cleanupServer()
		upstream.Close()
		pool.Close()
	}

	return pool, ts.URL, &toolCalls, cleanup
}

func fetchAuditRecord(t *testing.T, pool *pgxpool.Pool, sessionID, turnID, toolName string) integrationAuditRecord {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for {
		var record integrationAuditRecord
		err := pool.QueryRow(
			context.Background(),
			`SELECT decision, decided_at
			 FROM audit_log
			 WHERE session_id = $1 AND turn_id = $2 AND tool_name = $3`,
			sessionID,
			turnID,
			toolName,
		).Scan(&record.Decision, &record.DecidedAt)
		if err == nil {
			return record
		}
		if time.Now().After(deadline) {
			t.Fatalf("SELECT audit_log row: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func fetchAuditRecordByDecision(t *testing.T, pool *pgxpool.Pool, sessionID, turnID, decision string) integrationAuditRecord {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for {
		var record integrationAuditRecord
		err := pool.QueryRow(
			context.Background(),
			`SELECT decision, decided_at
			 FROM audit_log
			 WHERE session_id = $1 AND turn_id = $2 AND decision = $3
			 LIMIT 1`,
			sessionID,
			turnID,
			decision,
		).Scan(&record.Decision, &record.DecidedAt)
		if err == nil {
			return record
		}
		if time.Now().After(deadline) {
			t.Fatalf("SELECT audit_log row by decision: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func fetchTicketRecord(t *testing.T, pool *pgxpool.Pool, sessionID, turnID, toolName string) integrationTicketRecord {
	t.Helper()

	var record integrationTicketRecord
	err := pool.QueryRow(
		context.Background(),
		`SELECT status, expires_at
		 FROM ticket
		 WHERE session_id = $1 AND turn_id = $2 AND tool_name = $3`,
		sessionID,
		turnID,
		toolName,
	).Scan(&record.Status, &record.ExpiresAt)
	if err != nil {
		t.Fatalf("SELECT ticket row: %v", err)
	}
	return record
}

func countAuditRows(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()

	var count int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_log`).Scan(&count); err != nil {
		t.Fatalf("SELECT count(*) FROM audit_log: %v", err)
	}
	return count
}
