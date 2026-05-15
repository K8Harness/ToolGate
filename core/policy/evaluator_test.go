package policy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadPolicyScenarios(t *testing.T) {
	t.Run("missing file returns path in error", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "missing-policy.yaml")

		got, err := LoadPolicy(path)
		if err == nil {
			t.Fatal("LoadPolicy error = nil, want error")
		}
		if got != nil {
			t.Fatalf("LoadPolicy policy = %#v, want nil", got)
		}
		if !strings.Contains(err.Error(), path) {
			t.Fatalf("LoadPolicy error = %q, want path %q in error", err.Error(), path)
		}
	})

	t.Run("yaml syntax error returns error", func(t *testing.T) {
		path := writePolicyFile(t, "rules: [\n")

		got, err := LoadPolicy(path)
		if err == nil {
			t.Fatal("LoadPolicy error = nil, want error")
		}
		if got != nil {
			t.Fatalf("LoadPolicy policy = %#v, want nil", got)
		}
	})

	t.Run("unknown yaml field returns error", func(t *testing.T) {
		path := writePolicyFile(t, `
rules:
  - tool: refund_small
    action: allow
unexpected: true
budgets:
  maxToolCallsPerTurn: 5
defaultAction: deny
`)

		got, err := LoadPolicy(path)
		if err == nil {
			t.Fatal("LoadPolicy error = nil, want error")
		}
		if got != nil {
			t.Fatalf("LoadPolicy policy = %#v, want nil", got)
		}
	})

	t.Run("approvalRequired default action is rejected", func(t *testing.T) {
		path := writePolicyFile(t, `
rules:
  - tool: refund_small
    action: allow
budgets:
  maxToolCallsPerTurn: 5
defaultAction: approvalRequired
`)

		got, err := LoadPolicy(path)
		if err == nil {
			t.Fatal("LoadPolicy error = nil, want error")
		}
		if got != nil {
			t.Fatalf("LoadPolicy policy = %#v, want nil", got)
		}
		if !strings.Contains(err.Error(), "defaultAction") {
			t.Fatalf("LoadPolicy error = %q, want mention of defaultAction", err.Error())
		}
	})

	t.Run("empty tool names are rejected", func(t *testing.T) {
		path := writePolicyFile(t, `
rules:
  - tool: ""
    action: allow
budgets:
  maxToolCallsPerTurn: 5
defaultAction: deny
`)

		got, err := LoadPolicy(path)
		if err == nil {
			t.Fatal("LoadPolicy error = nil, want error")
		}
		if got != nil {
			t.Fatalf("LoadPolicy policy = %#v, want nil", got)
		}
		if !strings.Contains(err.Error(), "tool") {
			t.Fatalf("LoadPolicy error = %q, want mention of tool", err.Error())
		}
	})

	t.Run("invalid rule action is rejected", func(t *testing.T) {
		path := writePolicyFile(t, `
rules:
  - tool: refund_small
    action: maybe
budgets:
  maxToolCallsPerTurn: 5
defaultAction: deny
`)

		got, err := LoadPolicy(path)
		if err == nil {
			t.Fatal("LoadPolicy error = nil, want error")
		}
		if got != nil {
			t.Fatalf("LoadPolicy policy = %#v, want nil", got)
		}
		if !strings.Contains(err.Error(), "action") {
			t.Fatalf("LoadPolicy error = %q, want mention of action", err.Error())
		}
	})

	t.Run("invalid operation class is rejected", func(t *testing.T) {
		path := writePolicyFile(t, `
rules:
  - tool: refund_small
    action: allow
budgets:
  maxToolCallsPerTurn: 5
defaultAction: deny
operationClasses:
  get_customer: maybe
`)

		got, err := LoadPolicy(path)
		if err == nil {
			t.Fatal("LoadPolicy error = nil, want error")
		}
		if got != nil {
			t.Fatalf("LoadPolicy policy = %#v, want nil", got)
		}
		if !strings.Contains(err.Error(), "operationClasses") {
			t.Fatalf("LoadPolicy error = %q, want mention of operationClasses", err.Error())
		}
	})

	t.Run("valid policy loads expected fields", func(t *testing.T) {
		path := writePolicyFile(t, `
rules:
  - tool: refund_small
    action: allow
  - tool: delete_record
    action: deny
  - tool: refund_large
    action: approvalRequired
budgets:
  maxToolCallsPerTurn: 5
defaultAction: deny
`)

		got, err := LoadPolicy(path)
		if err != nil {
			t.Fatalf("LoadPolicy error = %v, want nil", err)
		}

		want := &AgentPolicy{
			Rules: []PolicyRule{
				{Tool: "refund_small", Action: ActionAllow},
				{Tool: "delete_record", Action: ActionDeny},
				{Tool: "refund_large", Action: ActionApprovalRequired},
			},
			Budgets: Budgets{
				MaxToolCallsPerTurn: 5,
			},
			DefaultAction: ActionDeny,
		}

		if len(got.Rules) != len(want.Rules) {
			t.Fatalf("len(Rules) = %d, want %d", len(got.Rules), len(want.Rules))
		}
		for i := range want.Rules {
			if got.Rules[i] != want.Rules[i] {
				t.Fatalf("Rules[%d] = %#v, want %#v", i, got.Rules[i], want.Rules[i])
			}
		}
		if got.Budgets != want.Budgets {
			t.Fatalf("Budgets = %#v, want %#v", got.Budgets, want.Budgets)
		}
		if got.DefaultAction != want.DefaultAction {
			t.Fatalf("DefaultAction = %q, want %q", got.DefaultAction, want.DefaultAction)
		}
	})

	t.Run("operation classes load into agent policy", func(t *testing.T) {
		path := writePolicyFile(t, `
rules:
  - tool: refund_small
    action: allow
budgets:
  maxToolCallsPerTurn: 5
defaultAction: deny
operationClasses:
  get_customer: read
  create_refund: write
`)

		got, err := LoadPolicy(path)
		if err != nil {
			t.Fatalf("LoadPolicy error = %v, want nil", err)
		}

		want := map[string]string{
			"get_customer":  "read",
			"create_refund": "write",
		}

		if len(got.OperationClasses) != len(want) {
			t.Fatalf("len(OperationClasses) = %d, want %d", len(got.OperationClasses), len(want))
		}
		for operation, wantClass := range want {
			if got.OperationClasses[operation] != wantClass {
				t.Fatalf("OperationClasses[%q] = %q, want %q", operation, got.OperationClasses[operation], wantClass)
			}
		}
	})
}

func TestEvaluateScenarios(t *testing.T) {
	policy := &AgentPolicy{
		Rules: []PolicyRule{
			{Tool: "refund_small", Action: ActionAllow},
			{Tool: "delete_record", Action: ActionDeny},
			{Tool: "refund_large", Action: ActionApprovalRequired},
		},
		Budgets: Budgets{
			MaxToolCallsPerTurn: 5,
		},
		DefaultAction: ActionDeny,
	}

	tests := []struct {
		name     string
		toolName string
		want     PolicyDecision
	}{
		{
			name:     "first rule match returns its action",
			toolName: "refund_small",
			want:     PolicyDecision{Action: ActionAllow},
		},
		{
			name:     "second rule match returns its action",
			toolName: "delete_record",
			want:     PolicyDecision{Action: ActionDeny},
		},
		{
			name:     "third rule match returns approval required",
			toolName: "refund_large",
			want:     PolicyDecision{Action: ActionApprovalRequired},
		},
		{
			name:     "non matching tool returns default action",
			toolName: "tool_not_listed",
			want:     PolicyDecision{Action: ActionDeny},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Evaluate(policy, tt.toolName)
			if got != tt.want {
				t.Fatalf("Evaluate(%q) = %#v, want %#v", tt.toolName, got, tt.want)
			}
		})
	}

	t.Run("empty rules list returns default action", func(t *testing.T) {
		got := Evaluate(&AgentPolicy{DefaultAction: ActionAllow}, "tool_not_listed")
		want := PolicyDecision{Action: ActionAllow}
		if got != want {
			t.Fatalf("Evaluate(empty rules) = %#v, want %#v", got, want)
		}
	})
}

func writePolicyFile(t *testing.T, contents string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "policy.yaml")
	if err := os.WriteFile(path, []byte(strings.TrimLeft(contents, "\n")), 0o644); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
	return path
}
