package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	corepolicy "github.com/K8Harness/ToolGate/core/policy"
	"github.com/K8Harness/ToolGate/core/mcp"
)

func TestPolicyGateHandlerPassthroughSkipsAuditAndBudget(t *testing.T) {
	audit := &policyGateAuditStub{}
	tickets := &policyGateTicketStub{}
	evaluator := &policyGateEvaluatorStub{decision: corepolicy.PolicyDecision{Action: corepolicy.ActionAllow}}
	handler := newPolicyGateHandler(
		&corepolicy.AgentPolicy{Budgets: corepolicy.Budgets{MaxToolCallsPerTurn: 2}},
		&policyGateBudgetStub{},
		audit,
		tickets,
		evaluator,
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		nowStub(time.Unix(0, 0)),
	)

	resp, err := handler.Handle(contextWithSessionAndTurn("session-pass", "turn-pass"), &mcp.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/list",
		Params:  json.RawMessage(`{"cursor":"page-1"}`),
	})
	if err != nil {
		t.Fatalf("Handle() error = %v, want nil", err)
	}
	if resp != nil {
		t.Fatalf("Handle() response = %#v, want nil", resp)
	}
	if got := len(audit.records); got != 0 {
		t.Fatalf("audit writes = %d, want 0", got)
	}
	if tickets.calls != 0 {
		t.Fatalf("ticket inserts = %d, want 0", tickets.calls)
	}
	if evaluator.calls != 0 {
		t.Fatalf("Evaluate calls = %d, want 0", evaluator.calls)
	}
}

func TestPolicyGateHandlerAllowWritesAuditAndPassesThrough(t *testing.T) {
	audit := &policyGateAuditStub{}
	evaluator := &policyGateEvaluatorStub{decision: corepolicy.PolicyDecision{Action: corepolicy.ActionAllow}}
	handler := newPolicyGateHandler(
		&corepolicy.AgentPolicy{Budgets: corepolicy.Budgets{MaxToolCallsPerTurn: 3}},
		NewBudgetTracker(),
		audit,
		&policyGateTicketStub{},
		evaluator,
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		nowStub(time.Unix(0, 0)),
	)

	req := testPolicyGateToolsCallRequest()
	resp, err := handler.Handle(contextWithSessionAndTurn("session-allow", "turn-allow"), req)
	if err != nil {
		t.Fatalf("Handle() error = %v, want nil", err)
	}
	if resp != nil {
		t.Fatalf("Handle() response = %#v, want nil", resp)
	}
	if evaluator.calls != 1 {
		t.Fatalf("Evaluate calls = %d, want 1", evaluator.calls)
	}
	if got := len(audit.records); got != 1 {
		t.Fatalf("audit writes = %d, want 1", got)
	}
	record := audit.records[0]
	if record.SessionID != "session-allow" || record.TurnID != "turn-allow" {
		t.Fatalf("audit session/turn = %q/%q, want session-allow/turn-allow", record.SessionID, record.TurnID)
	}
	if record.ToolName != "refund" {
		t.Fatalf("audit toolName = %q, want refund", record.ToolName)
	}
	if got := string(record.Arguments); got != `{"amount":42}` {
		t.Fatalf("audit arguments = %s, want %s", got, `{"amount":42}`)
	}
	if record.Decision != string(corepolicy.ActionAllow) {
		t.Fatalf("audit decision = %q, want %q", record.Decision, corepolicy.ActionAllow)
	}
}

func TestPolicyGateHandlerDenyReturnsPolicyErrorAndNoTicket(t *testing.T) {
	audit := &policyGateAuditStub{}
	tickets := &policyGateTicketStub{}
	handler := newPolicyGateHandler(
		&corepolicy.AgentPolicy{Budgets: corepolicy.Budgets{MaxToolCallsPerTurn: 3}},
		NewBudgetTracker(),
		audit,
		tickets,
		&policyGateEvaluatorStub{decision: corepolicy.PolicyDecision{Action: corepolicy.ActionDeny}},
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		nowStub(time.Unix(0, 0)),
	)

	resp, err := handler.Handle(contextWithSessionAndTurn("session-deny", "turn-deny"), testPolicyGateToolsCallRequest())
	if err != nil {
		t.Fatalf("Handle() error = %v, want nil", err)
	}
	if resp == nil || resp.Error == nil {
		t.Fatalf("Handle() response = %#v, want JSON-RPC error response", resp)
	}
	if resp.Error.Code != mcp.CodePolicyDenied {
		t.Fatalf("error code = %d, want %d", resp.Error.Code, mcp.CodePolicyDenied)
	}
	if resp.Error.Message != "denied by policy" {
		t.Fatalf("error message = %q, want %q", resp.Error.Message, "denied by policy")
	}
	if tickets.calls != 0 {
		t.Fatalf("ticket inserts = %d, want 0", tickets.calls)
	}
	if got := len(audit.records); got != 1 {
		t.Fatalf("audit writes = %d, want 1", got)
	}
	if audit.records[0].Decision != string(corepolicy.ActionDeny) {
		t.Fatalf("audit decision = %q, want %q", audit.records[0].Decision, corepolicy.ActionDeny)
	}
}

func TestPolicyGateHandlerApprovalRequiredReturnsPendingAndInsertsTicket(t *testing.T) {
	frozenNow := time.Date(2026, time.May, 14, 12, 0, 0, 0, time.UTC)
	audit := &policyGateAuditStub{}
	tickets := &policyGateTicketStub{}
	handler := newPolicyGateHandler(
		&corepolicy.AgentPolicy{Budgets: corepolicy.Budgets{MaxToolCallsPerTurn: 3}},
		NewBudgetTracker(),
		audit,
		tickets,
		&policyGateEvaluatorStub{decision: corepolicy.PolicyDecision{Action: corepolicy.ActionApprovalRequired}},
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		nowStub(frozenNow),
	)

	resp, err := handler.Handle(contextWithSessionAndTurn("session-approval", "turn-approval"), testPolicyGateToolsCallRequest())
	if err != nil {
		t.Fatalf("Handle() error = %v, want nil", err)
	}
	if resp == nil {
		t.Fatal("Handle() response = nil, want pending response")
	}

	gotResult := decodePolicyGateResult(t, resp.Result)
	if gotResult["status"] != "pending" {
		t.Fatalf("result.status = %v, want pending", gotResult["status"])
	}
	if gotResult["message"] != "tool call requires human approval" {
		t.Fatalf("result.message = %v, want tool call requires human approval", gotResult["message"])
	}
	if tickets.calls != 1 {
		t.Fatalf("ticket inserts = %d, want 1", tickets.calls)
	}
	if tickets.lastRecord.SessionID != "session-approval" || tickets.lastRecord.TurnID != "turn-approval" {
		t.Fatalf("ticket session/turn = %q/%q, want session-approval/turn-approval", tickets.lastRecord.SessionID, tickets.lastRecord.TurnID)
	}
	if got := string(tickets.lastRecord.Arguments); got != `{"amount":42}` {
		t.Fatalf("ticket arguments = %s, want %s", got, `{"amount":42}`)
	}
	wantExpires := frozenNow.Add(5 * time.Minute)
	if !tickets.lastRecord.ExpiresAt.Equal(wantExpires) {
		t.Fatalf("ticket expiresAt = %s, want %s", tickets.lastRecord.ExpiresAt, wantExpires)
	}
	if got := len(audit.records); got != 1 {
		t.Fatalf("audit writes = %d, want 1", got)
	}
	if audit.records[0].Decision != string(corepolicy.ActionApprovalRequired) {
		t.Fatalf("audit decision = %q, want %q", audit.records[0].Decision, corepolicy.ActionApprovalRequired)
	}
}

func TestPolicyGateHandlerApprovalRequiredLogsTicketInsertFailureAndReturnsPending(t *testing.T) {
	var buf bytes.Buffer
	handler := newPolicyGateHandler(
		&corepolicy.AgentPolicy{Budgets: corepolicy.Budgets{MaxToolCallsPerTurn: 3}},
		NewBudgetTracker(),
		&policyGateAuditStub{},
		&policyGateTicketStub{err: errors.New("insert failed")},
		&policyGateEvaluatorStub{decision: corepolicy.PolicyDecision{Action: corepolicy.ActionApprovalRequired}},
		slog.New(slog.NewJSONHandler(&buf, nil)),
		nowStub(time.Unix(0, 0)),
	)

	resp, err := handler.Handle(contextWithSessionAndTurn("session-warn", "turn-warn"), testPolicyGateToolsCallRequest())
	if err != nil {
		t.Fatalf("Handle() error = %v, want nil", err)
	}
	if resp == nil {
		t.Fatal("Handle() response = nil, want pending response")
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("captured %d log lines, want 2: %q", len(lines), buf.String())
	}
	entry := decodeLogEntry(t, lines[1])
	assertLogString(t, entry, "level", "WARN")
	assertLogString(t, entry, "msg", "ticket insert failed")
	assertLogString(t, entry, "sessionId", "session-warn")
	assertLogString(t, entry, "turnId", "turn-warn")
	assertLogString(t, entry, "toolName", "refund")
}

func TestPolicyGateHandlerBudgetExceededWritesAuditAndSkipsEvaluator(t *testing.T) {
	audit := &policyGateAuditStub{}
	evaluator := &policyGateEvaluatorStub{decision: corepolicy.PolicyDecision{Action: corepolicy.ActionAllow}}
	handler := newPolicyGateHandler(
		&corepolicy.AgentPolicy{Budgets: corepolicy.Budgets{MaxToolCallsPerTurn: 2}},
		NewBudgetTracker(),
		audit,
		&policyGateTicketStub{},
		evaluator,
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		nowStub(time.Unix(0, 0)),
	)
	ctx := contextWithSessionAndTurn("session-budget", "turn-budget")

	for i := 0; i < 2; i++ {
		resp, err := handler.Handle(ctx, testPolicyGateToolsCallRequest())
		if err != nil {
			t.Fatalf("warmup call %d error = %v, want nil", i+1, err)
		}
		if resp != nil {
			t.Fatalf("warmup call %d response = %#v, want nil", i+1, resp)
		}
	}

	resp, err := handler.Handle(ctx, testPolicyGateToolsCallRequest())
	if err != nil {
		t.Fatalf("budget exceeded call error = %v, want nil", err)
	}
	if resp == nil || resp.Error == nil {
		t.Fatalf("budget exceeded response = %#v, want JSON-RPC error", resp)
	}
	if resp.Error.Code != mcp.CodePolicyDenied {
		t.Fatalf("error code = %d, want %d", resp.Error.Code, mcp.CodePolicyDenied)
	}
	if resp.Error.Message != "tool-call budget exceeded" {
		t.Fatalf("error message = %q, want %q", resp.Error.Message, "tool-call budget exceeded")
	}
	if evaluator.calls != 2 {
		t.Fatalf("Evaluate calls = %d, want 2 before budget denial", evaluator.calls)
	}
	if got := len(audit.records); got != 3 {
		t.Fatalf("audit writes = %d, want 3", got)
	}
	last := audit.records[2]
	if last.Decision != "budgetExceeded" {
		t.Fatalf("audit decision = %q, want budgetExceeded", last.Decision)
	}
	if last.Reason != "maxToolCallsPerTurn exceeded" {
		t.Fatalf("audit reason = %q, want %q", last.Reason, "maxToolCallsPerTurn exceeded")
	}
}

func TestPolicyGateHandlerMalformedParamsReturnsInternalError(t *testing.T) {
	handler := newPolicyGateHandler(
		&corepolicy.AgentPolicy{Budgets: corepolicy.Budgets{MaxToolCallsPerTurn: 3}},
		NewBudgetTracker(),
		&policyGateAuditStub{},
		&policyGateTicketStub{},
		&policyGateEvaluatorStub{decision: corepolicy.PolicyDecision{Action: corepolicy.ActionAllow}},
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		nowStub(time.Unix(0, 0)),
	)

	resp, err := handler.Handle(contextWithSessionAndTurn("session-bad", "turn-bad"), &mcp.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":`),
	})
	if err == nil {
		t.Fatal("Handle() error = nil, want coded internal error")
	}
	if resp != nil {
		t.Fatalf("Handle() response = %#v, want nil", resp)
	}
	assertJSONRPCCode(t, err, mcp.CodeInternalError)
}

func testPolicyGateToolsCallRequest() *mcp.JSONRPCRequest {
	return &mcp.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"refund","arguments":{"amount":42}}`),
	}
}

func decodePolicyGateResult(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("json.Unmarshal(result) error = %v, want nil", err)
	}
	return result
}

type policyGateAuditStub struct {
	records []AuditRecord
}

func (s *policyGateAuditStub) Write(record AuditRecord) {
	s.records = append(s.records, record)
}

type policyGateTicketStub struct {
	calls      int
	lastRecord TicketRecord
	err        error
}

func (s *policyGateTicketStub) Insert(ctx context.Context, record TicketRecord) (string, error) {
	s.calls++
	s.lastRecord = record
	if s.err != nil {
		return "", s.err
	}
	return "ticket-1", nil
}

type policyGateEvaluatorStub struct {
	calls    int
	toolName string
	decision corepolicy.PolicyDecision
}

func (s *policyGateEvaluatorStub) Evaluate(policy *corepolicy.AgentPolicy, toolName string) corepolicy.PolicyDecision {
	s.calls++
	s.toolName = toolName
	return s.decision
}

type policyGateBudgetStub struct {
	calls int
}

func (s *policyGateBudgetStub) IncrementAndGet(sessionID, turnID string) int {
	s.calls++
	return 1
}

func nowStub(now time.Time) func() time.Time {
	return func() time.Time {
		return now
	}
}

func TestPolicyGateHandlerApprovalRequiredPendingResponseShape(t *testing.T) {
	handler := newPolicyGateHandler(
		&corepolicy.AgentPolicy{Budgets: corepolicy.Budgets{MaxToolCallsPerTurn: 1}},
		NewBudgetTracker(),
		&policyGateAuditStub{},
		&policyGateTicketStub{},
		&policyGateEvaluatorStub{decision: corepolicy.PolicyDecision{Action: corepolicy.ActionApprovalRequired}},
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		nowStub(time.Unix(0, 0)),
	)

	resp, err := handler.Handle(contextWithSessionAndTurn("session-shape", "turn-shape"), testPolicyGateToolsCallRequest())
	if err != nil {
		t.Fatalf("Handle() error = %v, want nil", err)
	}

	payload, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("json.Marshal(response) error = %v, want nil", err)
	}
	if string(payload) != `{"jsonrpc":"2.0","id":1,"result":{"message":"tool call requires human approval","status":"pending"}}` &&
		string(payload) != `{"jsonrpc":"2.0","id":1,"result":{"status":"pending","message":"tool call requires human approval"}}` {
		t.Fatalf("response JSON = %s, want documented pending response shape", payload)
	}
}

func TestPolicyGateHandlerLogsEachDecisionAtInfo(t *testing.T) {
	var buf bytes.Buffer
	handler := newPolicyGateHandler(
		&corepolicy.AgentPolicy{Budgets: corepolicy.Budgets{MaxToolCallsPerTurn: 1}},
		NewBudgetTracker(),
		&policyGateAuditStub{},
		&policyGateTicketStub{},
		&policyGateEvaluatorStub{decision: corepolicy.PolicyDecision{Action: corepolicy.ActionDeny}},
		slog.New(slog.NewJSONHandler(&buf, nil)),
		nowStub(time.Unix(0, 0)),
	)

	_, err := handler.Handle(contextWithSessionAndTurn("session-log", "turn-log"), testPolicyGateToolsCallRequest())
	if err != nil {
		t.Fatalf("Handle() error = %v, want nil", err)
	}

	line := singleLogLine(t, &buf)
	if !strings.Contains(line, `"decision":"deny"`) {
		t.Fatalf("log line = %s, want decision field", line)
	}
	if !strings.Contains(line, `"toolName":"refund"`) {
		t.Fatalf("log line = %s, want toolName field", line)
	}
	if !strings.Contains(line, `"sessionId":"session-log"`) {
		t.Fatalf("log line = %s, want sessionId field", line)
	}
	if !strings.Contains(line, `"turnId":"turn-log"`) {
		t.Fatalf("log line = %s, want turnId field", line)
	}
}
