package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/K8Harness/ToolGate/core/mcp"
	corepolicy "github.com/K8Harness/ToolGate/core/policy"
)

type budgetCounter interface {
	IncrementAndGet(sessionID, turnID string) int
}

type auditRecorder interface {
	Write(record AuditRecord)
}

type ticketInserter interface {
	Insert(ctx context.Context, record TicketRecord) (string, error)
}

type policyEvaluator interface {
	Evaluate(policy *corepolicy.AgentPolicy, toolName string) corepolicy.PolicyDecision
}

type defaultPolicyEvaluator struct{}

func (defaultPolicyEvaluator) Evaluate(policy *corepolicy.AgentPolicy, toolName string) corepolicy.PolicyDecision {
	return corepolicy.Evaluate(policy, toolName)
}

type BudgetTracker struct {
	mu     sync.Mutex
	counts map[string]int
}

func NewBudgetTracker() *BudgetTracker {
	return &BudgetTracker{
		counts: make(map[string]int),
	}
}

func (t *BudgetTracker) IncrementAndGet(sessionID, turnID string) int {
	t.mu.Lock()
	defer t.mu.Unlock()

	key := sessionID + ":" + turnID
	t.counts[key]++
	return t.counts[key]
}

type PolicyGateHandler struct {
	policy    *corepolicy.AgentPolicy
	budget    budgetCounter
	audit     auditRecorder
	tickets   ticketInserter
	evaluator policyEvaluator
	bridge    ApprovalBridge
	notifier  SlackNotifier
	log       *slog.Logger
	now       func() time.Time
}

func NewPolicyGateHandler(
	policy *corepolicy.AgentPolicy,
	budget *BudgetTracker,
	audit *AuditWriter,
	tickets *TicketStore,
	bridge ApprovalBridge,
	notifier SlackNotifier,
	log *slog.Logger,
) *PolicyGateHandler {
	return newPolicyGateHandler(policy, budget, audit, tickets, defaultPolicyEvaluator{}, bridge, notifier, log, time.Now)
}

func newPolicyGateHandler(
	policy *corepolicy.AgentPolicy,
	budget budgetCounter,
	audit auditRecorder,
	tickets ticketInserter,
	evaluator policyEvaluator,
	bridge ApprovalBridge,
	notifier SlackNotifier,
	log *slog.Logger,
	now func() time.Time,
) *PolicyGateHandler {
	if log == nil {
		log = slog.Default()
	}
	if now == nil {
		now = time.Now
	}

	return &PolicyGateHandler{
		policy:    policy,
		budget:    budget,
		audit:     audit,
		tickets:   tickets,
		evaluator: evaluator,
		bridge:    bridge,
		notifier:  notifier,
		log:       log,
		now:       now,
	}
}

type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

type policyGateParamsError struct {
	err error
}

func (e *policyGateParamsError) Error() string {
	if e.err == nil {
		return "internal error: policy gate params parse failed"
	}
	return "internal error: policy gate params parse failed: " + e.err.Error()
}

func (e *policyGateParamsError) Unwrap() error {
	return e.err
}

func (e *policyGateParamsError) JSONRPCCode() int {
	return mcp.CodeInternalError
}

func (h *PolicyGateHandler) Handle(ctx context.Context, req *mcp.JSONRPCRequest) (*mcp.JSONRPCResponse, error) {
	if req.Method != "tools/call" {
		return nil, nil
	}

	sessionID := mcp.SessionIDFromContext(ctx)
	turnID := mcp.TurnIDFromContext(ctx)

	toolName, arguments, err := parseToolCallParams(req.Params)
	if err != nil {
		return nil, &policyGateParamsError{err: err}
	}

	if h.budget.IncrementAndGet(sessionID, turnID) > h.policy.Budgets.MaxToolCallsPerTurn {
		h.audit.Write(AuditRecord{
			SessionID: sessionID,
			TurnID:    turnID,
			ToolName:  toolName,
			Arguments: arguments,
			Decision:  "budgetExceeded",
			Reason:    "maxToolCallsPerTurn exceeded",
		})
		h.logDecision(ctx, "budgetExceeded", sessionID, turnID, toolName)
		return mcp.NewErrorResponse(req.ID, mcp.CodePolicyDenied, "tool-call budget exceeded"), nil
	}

	decision := h.evaluator.Evaluate(h.policy, toolName)
	if decision.Action != corepolicy.ActionRedact {
		h.audit.Write(AuditRecord{
			SessionID: sessionID,
			TurnID:    turnID,
			ToolName:  toolName,
			Arguments: arguments,
			Decision:  string(decision.Action),
		})
		h.logDecision(ctx, string(decision.Action), sessionID, turnID, toolName)
	}

	switch decision.Action {
	case corepolicy.ActionAllow:
		return nil, nil
	case corepolicy.ActionDeny:
		return mcp.NewErrorResponse(req.ID, mcp.CodePolicyDenied, "denied by policy"), nil
	case corepolicy.ActionApprovalRequired:
		ticketID, err := h.tickets.Insert(ctx, TicketRecord{
			SessionID: sessionID,
			TurnID:    turnID,
			ToolName:  toolName,
			Arguments: arguments,
			ExpiresAt: h.now().Add(5 * time.Minute),
		})
		if err != nil {
			h.log.WarnContext(
				ctx,
				"ticket insert failed",
				"sessionId", sessionID,
				"turnId", turnID,
				"toolName", toolName,
				"error", err,
			)
			// Fail open: continue with empty ticketID so the approval hold still proceeds.
		}

		notifier := h.notifier
		go func() {
			if err := notifier.SendApprovalRequest(context.Background(), ticketID, TicketRecord{
				SessionID: sessionID,
				TurnID:    turnID,
				ToolName:  toolName,
				Arguments: arguments,
			}); err != nil {
				h.log.Error("slack notification failed", "ticketID", ticketID, "error", err)
			}
		}()

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
		if err != nil || !decision.Approved {
			h.log.Info("approval denied", "ticketID", ticketID, "sessionID", sessionID)
			return approvalErrorResponse(req.ID, "approval denied"), nil
		}
		// Approved: return (nil, nil) — pipeline continues to UpstreamForwarder
		return nil, nil
	case corepolicy.ActionRedact:
		redacted := redactArguments(arguments, decision.RedactFields)
		h.audit.Write(AuditRecord{
			SessionID: sessionID,
			TurnID:    turnID,
			ToolName:  toolName,
			Arguments: redacted,
			Decision:  "allow",
		})
		h.logDecision(ctx, "allow", sessionID, turnID, toolName)
		newParams, err := json.Marshal(toolCallParams{Name: toolName, Arguments: redacted})
		if err != nil {
			return nil, fmt.Errorf("redact: re-marshal params: %w", err)
		}
		req.Params = newParams
		return nil, nil
	default:
		return nil, &policyGateParamsError{err: fmt.Errorf("unsupported policy decision %q", decision.Action)}
	}
}

// approvalErrorResponse constructs a JSON-RPC error response for approval failures.
// It uses code -32001 (CodePolicyDenied) for both "approval denied" and "approval timeout".
func approvalErrorResponse(id json.RawMessage, msg string) *mcp.JSONRPCResponse {
	return &mcp.JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error: &mcp.JSONRPCError{
			Code:    mcp.CodePolicyDenied,
			Message: msg,
		},
	}
}

func (h *PolicyGateHandler) logDecision(ctx context.Context, decision, sessionID, turnID, toolName string) {
	h.log.InfoContext(
		ctx,
		"policy gate decision",
		"decision", decision,
		"toolName", toolName,
		"sessionId", sessionID,
		"turnId", turnID,
	)
}

// redactArguments returns a copy of raw with each field in fields replaced by "***REDACTED***".
// Fields not present in raw are silently skipped. If raw is not a JSON object, it is returned unchanged.
func redactArguments(raw json.RawMessage, fields []string) json.RawMessage {
	if len(fields) == 0 || raw == nil {
		return raw
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return raw
	}
	for _, f := range fields {
		if _, ok := m[f]; ok {
			m[f] = "***REDACTED***"
		}
	}
	redacted, err := json.Marshal(m)
	if err != nil {
		return raw
	}
	return redacted
}

func parseToolCallParams(raw json.RawMessage) (string, json.RawMessage, error) {
	var params toolCallParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return "", nil, fmt.Errorf("decode params: %w", err)
	}
	if params.Name == "" {
		return "", nil, fmt.Errorf("decode params: missing name")
	}
	if params.Arguments == nil {
		params.Arguments = json.RawMessage(`null`)
	}

	return params.Name, params.Arguments, nil
}
