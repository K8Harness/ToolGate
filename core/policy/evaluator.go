package policy

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

func LoadPolicy(path string) (*AgentPolicy, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open policy %q: %w", path, err)
	}
	defer f.Close()

	var policy AgentPolicy

	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	if err := dec.Decode(&policy); err != nil {
		return nil, fmt.Errorf("decode policy %q: %w", path, err)
	}

	if err := validatePolicy(&policy); err != nil {
		return nil, fmt.Errorf("validate policy %q: %w", path, err)
	}

	return &policy, nil
}

func Evaluate(policy *AgentPolicy, toolName string) PolicyDecision {
	for _, rule := range policy.Rules {
		if rule.Tool == toolName {
			return PolicyDecision{Action: rule.Action}
		}
	}

	return PolicyDecision{Action: policy.DefaultAction}
}

func validatePolicy(policy *AgentPolicy) error {
	if !isRuleAction(policy.DefaultAction) || policy.DefaultAction == ActionApprovalRequired {
		return fmt.Errorf("defaultAction %q must be allow or deny", policy.DefaultAction)
	}

	for i, rule := range policy.Rules {
		if rule.Tool == "" {
			return fmt.Errorf("rules[%d].tool must not be empty", i)
		}
		if !isRuleAction(rule.Action) {
			return fmt.Errorf("rules[%d].action %q is invalid", i, rule.Action)
		}
	}

	return nil
}

func isRuleAction(action Action) bool {
	switch action {
	case ActionAllow, ActionDeny, ActionApprovalRequired:
		return true
	default:
		return false
	}
}
