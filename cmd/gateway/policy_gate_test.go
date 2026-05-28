package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/K8Harness/ToolGate/core/mcp"
	corepolicy "github.com/K8Harness/ToolGate/core/policy"
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
		nil,
		nil,
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
		nil,
		nil,
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
		nil,
		nil,
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

func TestPolicyGateHandlerApprovalRequiredInsertsTicketAndCallsBridge(t *testing.T) {
	frozenNow := time.Date(2026, time.May, 14, 12, 0, 0, 0, time.UTC)
	audit := &policyGateAuditStub{}
	tickets := &policyGateTicketStub{}
	bridge := &mockApprovalBridge{decision: ApprovalDecision{Approved: true, TicketID: "ticket-1"}}
	notifier := newMockApprovalNotifier(nil)
	handler := newPolicyGateHandler(
		&corepolicy.AgentPolicy{Budgets: corepolicy.Budgets{MaxToolCallsPerTurn: 3}},
		NewBudgetTracker(),
		audit,
		tickets,
		&policyGateEvaluatorStub{decision: corepolicy.PolicyDecision{Action: corepolicy.ActionApprovalRequired}},
		bridge,
		notifier,
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		nowStub(frozenNow),
	)

	resp, err := handler.Handle(contextWithSessionAndTurn("session-approval", "turn-approval"), testPolicyGateToolsCallRequest())
	if err != nil {
		t.Fatalf("Handle() error = %v, want nil", err)
	}
	// Approved => pipeline continues => (nil, nil)
	if resp != nil {
		t.Fatalf("Handle() response = %#v, want nil (approved => continue)", resp)
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
	if !bridge.called {
		t.Fatal("bridge.WaitForDecision was not called")
	}
}

func TestPolicyGateHandlerApprovalRequiredLogsTicketInsertFailureAndContinuesHold(t *testing.T) {
	var buf bytes.Buffer
	bridge := &mockApprovalBridge{decision: ApprovalDecision{Approved: true, TicketID: ""}}
	notifier := newMockApprovalNotifier(nil)
	handler := newPolicyGateHandler(
		&corepolicy.AgentPolicy{Budgets: corepolicy.Budgets{MaxToolCallsPerTurn: 3}},
		NewBudgetTracker(),
		&policyGateAuditStub{},
		&policyGateTicketStub{err: errors.New("insert failed")},
		&policyGateEvaluatorStub{decision: corepolicy.PolicyDecision{Action: corepolicy.ActionApprovalRequired}},
		bridge,
		notifier,
		slog.New(slog.NewJSONHandler(&buf, nil)),
		nowStub(time.Unix(0, 0)),
	)

	resp, err := handler.Handle(contextWithSessionAndTurn("session-warn", "turn-warn"), testPolicyGateToolsCallRequest())
	if err != nil {
		t.Fatalf("Handle() error = %v, want nil", err)
	}
	// Fail open: ticket insert failure does not abort; bridge is still called and approved => nil resp
	if resp != nil {
		t.Fatalf("Handle() response = %#v, want nil (approved after insert failure)", resp)
	}
	if !bridge.called {
		t.Fatal("bridge.WaitForDecision was not called after ticket insert failure")
	}

	// Log output: 2 lines — decision log + warn log
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("captured %d log lines, want >= 2: %q", len(lines), buf.String())
	}
	// Find the warn line
	var warnEntry map[string]any
	for _, line := range lines {
		e := decodeLogEntry(t, line)
		if e["level"] == "WARN" {
			warnEntry = e
			break
		}
	}
	if warnEntry == nil {
		t.Fatalf("no WARN log line found in: %q", buf.String())
	}
	assertLogString(t, warnEntry, "msg", "ticket insert failed")
	assertLogString(t, warnEntry, "sessionId", "session-warn")
	assertLogString(t, warnEntry, "turnId", "turn-warn")
	assertLogString(t, warnEntry, "toolName", "refund")
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
		nil,
		nil,
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
		nil,
		nil,
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

func (s *policyGateEvaluatorStub) Evaluate(policy *corepolicy.AgentPolicy, toolName string, args json.RawMessage) corepolicy.PolicyDecision {
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

func TestPolicyGateHandlerApprovalRequiredErrorResponseShape(t *testing.T) {
	// Verify the error response shape matches the spec: code -32001, message "approval denied"
	bridge := &mockApprovalBridge{decision: ApprovalDecision{Approved: false}}
	notifier := newMockApprovalNotifier(nil)
	handler := newPolicyGateHandler(
		&corepolicy.AgentPolicy{Budgets: corepolicy.Budgets{MaxToolCallsPerTurn: 1}},
		NewBudgetTracker(),
		&policyGateAuditStub{},
		&policyGateTicketStub{},
		&policyGateEvaluatorStub{decision: corepolicy.PolicyDecision{Action: corepolicy.ActionApprovalRequired}},
		bridge,
		notifier,
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		nowStub(time.Unix(0, 0)),
	)

	resp, err := handler.Handle(contextWithSessionAndTurn("session-shape", "turn-shape"), testPolicyGateToolsCallRequest())
	if err != nil {
		t.Fatalf("Handle() error = %v, want nil", err)
	}
	if resp == nil || resp.Error == nil {
		t.Fatalf("Handle() response = %#v, want error response", resp)
	}

	payload, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("json.Marshal(response) error = %v, want nil", err)
	}
	if string(payload) != `{"jsonrpc":"2.0","id":1,"error":{"code":-32001,"message":"approval denied"}}` {
		t.Fatalf("response JSON = %s, want documented approval denied shape", payload)
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
		nil,
		nil,
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

// --- Approval hold path tests (bridge + notifier injected) ---

// mockApprovalBridge is a test double for ApprovalBridge.
type mockApprovalBridge struct {
	decision ApprovalDecision
	err      error
	called   bool
}

func (m *mockApprovalBridge) WaitForDecision(_ context.Context, _, _, _ string) (ApprovalDecision, error) {
	m.called = true
	return m.decision, m.err
}

// mockApprovalNotifier is a test double for ApprovalNotifier.
type mockApprovalNotifier struct {
	err        error
	sendCalled chan struct{}
}

func newMockApprovalNotifier(err error) *mockApprovalNotifier {
	return &mockApprovalNotifier{
		err:        err,
		sendCalled: make(chan struct{}, 1),
	}
}

func (m *mockApprovalNotifier) SendApprovalRequest(_ context.Context, _ string, _ TicketRecord) error {
	m.sendCalled <- struct{}{}
	return m.err
}

type policyGateLockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *policyGateLockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *policyGateLockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestPolicyGateHandlerApprovalHoldApprovedReturnsContinue(t *testing.T) {
	bridge := &mockApprovalBridge{decision: ApprovalDecision{Approved: true, TicketID: "ticket-1"}}
	notifier := newMockApprovalNotifier(nil)
	handler := newPolicyGateHandler(
		&corepolicy.AgentPolicy{Budgets: corepolicy.Budgets{MaxToolCallsPerTurn: 3}},
		NewBudgetTracker(),
		&policyGateAuditStub{},
		&policyGateTicketStub{},
		&policyGateEvaluatorStub{decision: corepolicy.PolicyDecision{Action: corepolicy.ActionApprovalRequired}},
		bridge,
		notifier,
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		nowStub(time.Unix(0, 0)),
	)

	resp, err := handler.Handle(contextWithSessionAndTurn("session-approved", "turn-approved"), testPolicyGateToolsCallRequest())
	if err != nil {
		t.Fatalf("Handle() error = %v, want nil", err)
	}
	if resp != nil {
		t.Fatalf("Handle() response = %#v, want nil (approved => pipeline continues)", resp)
	}
	if !bridge.called {
		t.Fatal("bridge.WaitForDecision was not called")
	}
}

func TestPolicyGateHandlerApprovalHoldDeniedReturnsError(t *testing.T) {
	bridge := &mockApprovalBridge{decision: ApprovalDecision{Approved: false, TicketID: "ticket-1"}}
	notifier := newMockApprovalNotifier(nil)
	handler := newPolicyGateHandler(
		&corepolicy.AgentPolicy{Budgets: corepolicy.Budgets{MaxToolCallsPerTurn: 3}},
		NewBudgetTracker(),
		&policyGateAuditStub{},
		&policyGateTicketStub{},
		&policyGateEvaluatorStub{decision: corepolicy.PolicyDecision{Action: corepolicy.ActionApprovalRequired}},
		bridge,
		notifier,
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		nowStub(time.Unix(0, 0)),
	)

	resp, err := handler.Handle(contextWithSessionAndTurn("session-denied", "turn-denied"), testPolicyGateToolsCallRequest())
	if err != nil {
		t.Fatalf("Handle() error = %v, want nil", err)
	}
	if resp == nil || resp.Error == nil {
		t.Fatalf("Handle() response = %#v, want error response", resp)
	}
	if resp.Error.Code != -32001 {
		t.Fatalf("error code = %d, want -32001", resp.Error.Code)
	}
	if resp.Error.Message != "approval denied" {
		t.Fatalf("error message = %q, want %q", resp.Error.Message, "approval denied")
	}
}

func TestPolicyGateHandlerApprovalHoldBridgeErrorReturnsDenied(t *testing.T) {
	bridge := &mockApprovalBridge{err: errors.New("bridge internal error")}
	notifier := newMockApprovalNotifier(nil)
	handler := newPolicyGateHandler(
		&corepolicy.AgentPolicy{Budgets: corepolicy.Budgets{MaxToolCallsPerTurn: 3}},
		NewBudgetTracker(),
		&policyGateAuditStub{},
		&policyGateTicketStub{},
		&policyGateEvaluatorStub{decision: corepolicy.PolicyDecision{Action: corepolicy.ActionApprovalRequired}},
		bridge,
		notifier,
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		nowStub(time.Unix(0, 0)),
	)

	resp, err := handler.Handle(contextWithSessionAndTurn("session-bridgeerr", "turn-bridgeerr"), testPolicyGateToolsCallRequest())
	if err != nil {
		t.Fatalf("Handle() error = %v, want nil", err)
	}
	if resp == nil || resp.Error == nil {
		t.Fatalf("Handle() response = %#v, want error response", resp)
	}
	if resp.Error.Code != -32001 {
		t.Fatalf("error code = %d, want -32001", resp.Error.Code)
	}
	if resp.Error.Message != "approval denied" {
		t.Fatalf("error message = %q, want %q", resp.Error.Message, "approval denied")
	}
}

func TestPolicyGateHandlerApprovalHoldTimeoutReturnsTimeoutError(t *testing.T) {
	audit := &policyGateAuditStub{}
	bridge := &mockApprovalBridge{err: ErrApprovalTimeout}
	notifier := newMockApprovalNotifier(nil)
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

func TestPolicyGateHandlerRedactMasksFieldAndAuditsAllow(t *testing.T) {
	audit := &policyGateAuditStub{}
	evaluator := &policyGateEvaluatorStub{
		decision: corepolicy.PolicyDecision{
			Action:       corepolicy.ActionRedact,
			RedactFields: []string{"message"},
		},
	}
	handler := newPolicyGateHandler(
		&corepolicy.AgentPolicy{Budgets: corepolicy.Budgets{MaxToolCallsPerTurn: 3}},
		NewBudgetTracker(),
		audit,
		&policyGateTicketStub{},
		evaluator,
		nil,
		nil,
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		nowStub(time.Unix(0, 0)),
	)

	req := &mcp.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"send_lark_message","arguments":{"message":"secret content","channel":"#general"}}`),
	}

	resp, err := handler.Handle(contextWithSessionAndTurn("session-redact", "turn-redact"), req)
	if err != nil {
		t.Fatalf("Handle() error = %v, want nil", err)
	}
	// redact returns (nil, nil) — pipeline continues
	if resp != nil {
		t.Fatalf("Handle() response = %#v, want nil (pipeline continues)", resp)
	}

	// Exactly one audit record
	if got := len(audit.records); got != 1 {
		t.Fatalf("audit writes = %d, want 1", got)
	}
	record := audit.records[0]

	// Audit decision must be "allow"
	if record.Decision != "allow" {
		t.Fatalf("audit decision = %q, want %q", record.Decision, "allow")
	}

	// Audit arguments must contain REDACTED value for "message"
	var args map[string]any
	if err := json.Unmarshal(record.Arguments, &args); err != nil {
		t.Fatalf("json.Unmarshal(audit.Arguments): %v", err)
	}
	if args["message"] != "***REDACTED***" {
		t.Fatalf("audit args[message] = %v, want ***REDACTED***", args["message"])
	}
	// Fields not in redactFields are preserved
	if args["channel"] != "#general" {
		t.Fatalf("audit args[channel] = %v, want #general", args["channel"])
	}

	// req.Params must be updated with redacted args
	var updatedParams toolCallParams
	if err := json.Unmarshal(req.Params, &updatedParams); err != nil {
		t.Fatalf("json.Unmarshal(req.Params): %v", err)
	}
	var updatedArgs map[string]any
	if err := json.Unmarshal(updatedParams.Arguments, &updatedArgs); err != nil {
		t.Fatalf("json.Unmarshal(req.Params.Arguments): %v", err)
	}
	if updatedArgs["message"] != "***REDACTED***" {
		t.Fatalf("req.Params message = %v, want ***REDACTED***", updatedArgs["message"])
	}
}

func TestPolicyGateHandlerRedactSkipsMissingField(t *testing.T) {
	audit := &policyGateAuditStub{}
	evaluator := &policyGateEvaluatorStub{
		decision: corepolicy.PolicyDecision{
			Action:       corepolicy.ActionRedact,
			RedactFields: []string{"nonexistent_field"},
		},
	}
	handler := newPolicyGateHandler(
		&corepolicy.AgentPolicy{Budgets: corepolicy.Budgets{MaxToolCallsPerTurn: 3}},
		NewBudgetTracker(),
		audit,
		&policyGateTicketStub{},
		evaluator,
		nil,
		nil,
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		nowStub(time.Unix(0, 0)),
	)

	req := &mcp.JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"send_lark_message","arguments":{"channel":"#general"}}`),
	}

	resp, err := handler.Handle(contextWithSessionAndTurn("session-redact-skip", "turn-redact-skip"), req)
	if err != nil {
		t.Fatalf("Handle() error = %v, want nil", err)
	}
	if resp != nil {
		t.Fatalf("Handle() response = %#v, want nil", resp)
	}
	// Should still write audit with decision "allow"
	if got := len(audit.records); got != 1 {
		t.Fatalf("audit writes = %d, want 1", got)
	}
	if audit.records[0].Decision != "allow" {
		t.Fatalf("audit decision = %q, want allow", audit.records[0].Decision)
	}
}

func TestPolicyGateHandlerApprovalHoldNotifierErrorDoesNotBlockBridge(t *testing.T) {
	// Even if notifier returns an error, WaitForDecision must still be called.
	var buf policyGateLockedBuffer
	bridge := &mockApprovalBridge{decision: ApprovalDecision{Approved: true, TicketID: "ticket-notifier-err"}}
	notifier := newMockApprovalNotifier(errors.New("lark down"))
	handler := newPolicyGateHandler(
		&corepolicy.AgentPolicy{Budgets: corepolicy.Budgets{MaxToolCallsPerTurn: 3}},
		NewBudgetTracker(),
		&policyGateAuditStub{},
		&policyGateTicketStub{},
		&policyGateEvaluatorStub{decision: corepolicy.PolicyDecision{Action: corepolicy.ActionApprovalRequired}},
		bridge,
		notifier,
		slog.New(slog.NewTextHandler(&buf, nil)),
		nowStub(time.Unix(0, 0)),
	)

	resp, err := handler.Handle(contextWithSessionAndTurn("session-notifiererr", "turn-notifiererr"), testPolicyGateToolsCallRequest())
	if err != nil {
		t.Fatalf("Handle() error = %v, want nil", err)
	}
	if resp != nil {
		t.Fatalf("Handle() response = %#v, want nil (approved => pipeline continues)", resp)
	}
	if !bridge.called {
		t.Fatal("bridge.WaitForDecision was not called even though notifier failed")
	}
	// Wait for the notifier goroutine to complete before checking
	select {
	case <-notifier.sendCalled:
	case <-time.After(time.Second):
		t.Fatal("notifier.SendApprovalRequest was not called within 1 second")
	}
	deadline := time.Now().Add(time.Second)
	for !strings.Contains(buf.String(), "lark notification failed") {
		if time.Now().After(deadline) {
			t.Fatalf("logs = %q, want lark notification failure entry", buf.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}
