package main

import (
	"fmt"
	"io"
	"os"

	"gopkg.in/yaml.v3"
)

var allowedPolicyOutcomes = map[string]struct{}{
	"allow":            {},
	"deny":             {},
	"approvalRequired": {},
	"expired":          {},
	"upstream_error":   {},
}

func LoadSuite(path string) (*EvalSuite, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	suite, err := LoadSuiteFromReader(file)
	if err != nil {
		return nil, fmt.Errorf("parse eval suite %q: %w", path, err)
	}
	return suite, nil
}

func LoadSuiteFromReader(r io.Reader) (*EvalSuite, error) {
	var suite EvalSuite
	if err := yaml.NewDecoder(r).Decode(&suite); err != nil {
		return nil, fmt.Errorf("parse eval suite: %w", err)
	}

	for i, evalCase := range suite.Cases {
		if evalCase.Name == "" {
			return nil, fmt.Errorf("case[%d]: missing required field %q", i, "name")
		}
		if evalCase.Input == "" {
			return nil, fmt.Errorf("case %q: missing required field %q", evalCase.Name, "input")
		}
		if evalCase.PolicyOutcome == "" {
			return nil, fmt.Errorf("case %q: missing required field %q", evalCase.Name, "policyOutcome")
		}
		if _, ok := allowedPolicyOutcomes[evalCase.PolicyOutcome]; !ok {
			return nil, fmt.Errorf("case %q: invalid policyOutcome %q", evalCase.Name, evalCase.PolicyOutcome)
		}
	}

	return &suite, nil
}
