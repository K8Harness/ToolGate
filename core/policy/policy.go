package policy

type Action string

const (
	ActionAllow            Action = "allow"
	ActionDeny             Action = "deny"
	ActionApprovalRequired Action = "approvalRequired"
)

type PolicyRule struct {
	Tool   string `yaml:"tool"`
	Action Action `yaml:"action"`
}

type Budgets struct {
	MaxToolCallsPerTurn int `yaml:"maxToolCallsPerTurn"`
}

type AgentPolicy struct {
	Rules         []PolicyRule `yaml:"rules"`
	Budgets       Budgets      `yaml:"budgets"`
	DefaultAction Action       `yaml:"defaultAction"`
}

type PolicyDecision struct {
	Action Action
}
