package policy

type Action string

const (
	ActionAllow            Action = "allow"
	ActionDeny             Action = "deny"
	ActionApprovalRequired Action = "approvalRequired"
	ActionRedact           Action = "redact"
)

type PolicyRule struct {
	Tool         string   `yaml:"tool"`
	Action       Action   `yaml:"action"`
	RedactFields []string `yaml:"redactFields,omitempty"`
}

type Budgets struct {
	MaxToolCallsPerTurn int `yaml:"maxToolCallsPerTurn"`
}

type AgentPolicy struct {
	Rules            []PolicyRule      `yaml:"rules"`
	Budgets          Budgets           `yaml:"budgets"`
	DefaultAction    Action            `yaml:"defaultAction"`
	OperationClasses map[string]string `yaml:"operationClasses"`
}

type PolicyDecision struct {
	Action       Action
	RedactFields []string
}
