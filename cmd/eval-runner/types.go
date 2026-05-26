package main

import "encoding/json"

type EvalCase struct {
	Name                 string   `yaml:"name"`
	Input                string   `yaml:"input"`
	MustInclude          []string `yaml:"mustInclude"`
	MustNotInclude       []string `yaml:"mustNotInclude"`
	PolicyOutcome        string   `yaml:"policyOutcome"`
	MustNotContainInArgs []string `yaml:"mustNotContainInArgs"`
}

type EvalSuite struct {
	Cases []EvalCase `yaml:"cases"`
}

type TraceRow struct {
	ToolName  string
	Decision  string
	Arguments json.RawMessage
}

type CheckFailure struct {
	Check    string `json:"check"`
	Expected string `json:"expected"`
	Observed string `json:"observed"`
}

type CaseResult struct {
	Name     string         `json:"name"`
	Passed   bool           `json:"passed"`
	Failures []CheckFailure `json:"failures"`
}
