package main

import (
	"context"
	"encoding/json"
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
	log       *slog.Logger
	now       func() time.Time
}

func NewPolicyGateHandler(
	policy *corepolicy.AgentPolicy,
	budget *BudgetTracker,
	audit *AuditWriter,
	tickets *TicketStore,
	log *slog.Logger,
) *PolicyGateHandler {
	return newPolicyGateHandler(policy, budget, audit, tickets, defaultPolicyEvaluator{}, log, time.Now)
}

func newPolicyGateHandler(
	policy *corepolicy.AgentPolicy,
	budget budgetCounter,
	audit auditRecorder,
	tickets ticketInserter,
	evaluator policyEvaluator,
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
	h.audit.Write(AuditRecord{
		SessionID: sessionID,
		TurnID:    turnID,
		ToolName:  toolName,
		Arguments: arguments,
		Decision:  string(decision.Action),
	})
	h.logDecision(ctx, string(decision.Action), sessionID, turnID, toolName)

	switch decision.Action {
	case corepolicy.ActionAllow:
		return nil, nil
	case corepolicy.ActionDeny:
		return mcp.NewErrorResponse(req.ID, mcp.CodePolicyDenied, "denied by policy"), nil
	case corepolicy.ActionApprovalRequired:
		_, err := h.tickets.Insert(ctx, TicketRecord{
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
		}

		result, err := json.Marshal(map[string]string{
			"status":  "pending",
			"message": "tool call requires human approval",
		})
		if err != nil {
			return nil, &policyGateParamsError{err: fmt.Errorf("encode pending result: %w", err)}
		}

		return &mcp.JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  result,
		}, nil
	default:
		return nil, &policyGateParamsError{err: fmt.Errorf("unsupported policy decision %q", decision.Action)}
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
