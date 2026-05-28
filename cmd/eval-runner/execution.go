package main

import "context"

func runEvalCase(ctx context.Context, runner caseExecutor, testCase EvalCase) CaseResult {
	trace, err := runner.Run(ctx, testCase)
	if err != nil {
		return CaseResult{
			Name: testCase.Name,
			Failures: []CheckFailure{{
				Check:    "run",
				Expected: "case completes successfully",
				Observed: err.Error(),
			}},
		}
	}

	return Evaluate(testCase, trace)
}

func summarizeResults(results []CaseResult) evalResponse {
	passCount := 0
	for _, result := range results {
		if result.Passed {
			passCount++
		}
	}

	return evalResponse{
		Passed:     passCount == len(results),
		PassCount:  passCount,
		TotalCount: len(results),
		Cases:      results,
		Report:     GenerateReport(results),
	}
}
