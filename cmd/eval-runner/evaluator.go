package main

import "strings"

func Evaluate(c EvalCase, trace []TraceRow) CaseResult {
	result := CaseResult{Name: c.Name, Trace: trace}
	failures := make([]CheckFailure, 0)

	failures = append(failures, evaluateMustInclude(c.MustInclude, trace)...)
	failures = append(failures, evaluateMustNotInclude(c.MustNotInclude, trace)...)
	failures = append(failures, evaluatePolicyOutcome(c.PolicyOutcome, trace)...)
	failures = append(failures, evaluateMustNotContainInArgs(c.MustNotContainInArgs, trace)...)

	if len(failures) == 0 {
		result.Passed = true
		return result
	}

	result.Failures = failures
	return result
}

func evaluateMustInclude(expectedTools []string, trace []TraceRow) []CheckFailure {
	if len(expectedTools) == 0 {
		return nil
	}
	if len(trace) == 0 {
		return []CheckFailure{{
			Check:    "mustInclude",
			Expected: expectedTools[0],
			Observed: "(empty trace)",
		}}
	}

	traceIndex := 0
	for _, toolName := range expectedTools {
		found := false
		for traceIndex < len(trace) {
			if trace[traceIndex].ToolName == toolName {
				traceIndex++
				found = true
				break
			}
			traceIndex++
		}
		if !found {
			return []CheckFailure{{
				Check:    "mustInclude",
				Expected: toolName,
				Observed: trace[len(trace)-1].ToolName,
			}}
		}
	}

	return nil
}

func evaluateMustNotInclude(forbiddenTools []string, trace []TraceRow) []CheckFailure {
	if len(forbiddenTools) == 0 || len(trace) == 0 {
		return nil
	}

	forbidden := make(map[string]struct{}, len(forbiddenTools))
	for _, toolName := range forbiddenTools {
		forbidden[toolName] = struct{}{}
	}

	failures := make([]CheckFailure, 0)
	for _, row := range trace {
		if _, ok := forbidden[row.ToolName]; ok {
			failures = append(failures, CheckFailure{
				Check:    "mustNotInclude",
				Expected: row.ToolName,
				Observed: row.ToolName,
			})
		}
	}

	return failures
}

func evaluatePolicyOutcome(expected string, trace []TraceRow) []CheckFailure {
	if expected == "" {
		return nil
	}
	if len(trace) == 0 {
		return []CheckFailure{{
			Check:    "policyOutcome",
			Expected: expected,
			Observed: "(empty trace)",
		}}
	}

	observed := trace[len(trace)-1].Decision
	if observed == expected {
		return nil
	}

	return []CheckFailure{{
		Check:    "policyOutcome",
		Expected: expected,
		Observed: observed,
	}}
}

func evaluateMustNotContainInArgs(forbiddenSubstrings []string, trace []TraceRow) []CheckFailure {
	if len(forbiddenSubstrings) == 0 || len(trace) == 0 {
		return nil
	}

	failures := make([]CheckFailure, 0)
	for _, forbidden := range forbiddenSubstrings {
		for _, row := range trace {
			args := string(row.Arguments)
			if strings.Contains(args, forbidden) {
				failures = append(failures, CheckFailure{
					Check:    "mustNotContainInArgs",
					Expected: forbidden,
					Observed: args,
				})
				break
			}
		}
	}

	return failures
}
