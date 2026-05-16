package main

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestEvaluatePassesWhenAllChecksMatch(t *testing.T) {
	testCase := EvalCase{
		Name:                 "small-refund-allow",
		MustInclude:          []string{"lookup_customer", "refund_small"},
		MustNotInclude:       []string{"delete_record"},
		PolicyOutcome:        "allow",
		MustNotContainInArgs: []string{"123-45-6789"},
	}
	trace := []TraceRow{
		{ToolName: "lookup_customer", Decision: "allow", Arguments: json.RawMessage(`{"customer":"abc"}`)},
		{ToolName: "refund_small", Decision: "allow", Arguments: json.RawMessage(`{"amount":10}`)},
	}

	got := Evaluate(testCase, trace)

	want := CaseResult{
		Name:     "small-refund-allow",
		Passed:   true,
		Failures: nil,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Evaluate() = %#v, want %#v", got, want)
	}
}

func TestEvaluateMustIncludeAllowsGaps(t *testing.T) {
	testCase := EvalCase{
		Name:          "gapped-subsequence",
		MustInclude:   []string{"lookup_customer", "refund_small"},
		PolicyOutcome: "allow",
	}
	trace := []TraceRow{
		{ToolName: "lookup_customer", Decision: "allow"},
		{ToolName: "create_ticket", Decision: "allow"},
		{ToolName: "refund_small", Decision: "allow"},
	}

	got := Evaluate(testCase, trace)

	if !got.Passed {
		t.Fatalf("Evaluate() Passed = false, want true; failures = %#v", got.Failures)
	}
}

func TestEvaluateMustIncludeFailsWhenToolMissing(t *testing.T) {
	testCase := EvalCase{
		Name:          "missing-tool",
		MustInclude:   []string{"refund_small"},
		PolicyOutcome: "allow",
	}
	trace := []TraceRow{
		{ToolName: "lookup_customer", Decision: "allow"},
	}

	got := Evaluate(testCase, trace)

	wantFailure := CheckFailure{
		Check:    "mustInclude",
		Expected: "refund_small",
		Observed: "lookup_customer",
	}
	if got.Passed {
		t.Fatalf("Evaluate() Passed = true, want false")
	}
	if len(got.Failures) != 1 {
		t.Fatalf("len(Failures) = %d, want 1", len(got.Failures))
	}
	if !reflect.DeepEqual(got.Failures[0], wantFailure) {
		t.Fatalf("Failure = %#v, want %#v", got.Failures[0], wantFailure)
	}
}

func TestEvaluateMustIncludeFailsOnOutOfOrderTrace(t *testing.T) {
	testCase := EvalCase{
		Name:          "out-of-order",
		MustInclude:   []string{"lookup_customer", "refund_small"},
		PolicyOutcome: "allow",
	}
	trace := []TraceRow{
		{ToolName: "refund_small", Decision: "allow"},
		{ToolName: "lookup_customer", Decision: "allow"},
	}

	got := Evaluate(testCase, trace)

	if got.Passed {
		t.Fatalf("Evaluate() Passed = true, want false")
	}
	if len(got.Failures) == 0 || got.Failures[0].Check != "mustInclude" {
		t.Fatalf("Failures = %#v, want mustInclude failure", got.Failures)
	}
}

func TestEvaluateEmptyTraceReportsEmptyObserved(t *testing.T) {
	testCase := EvalCase{
		Name:          "empty-trace",
		MustInclude:   []string{"refund_small"},
		PolicyOutcome: "allow",
	}

	got := Evaluate(testCase, nil)

	wantFailures := []CheckFailure{
		{Check: "mustInclude", Expected: "refund_small", Observed: "(empty trace)"},
		{Check: "policyOutcome", Expected: "allow", Observed: "(empty trace)"},
	}
	if got.Passed {
		t.Fatalf("Evaluate() Passed = true, want false")
	}
	if !reflect.DeepEqual(got.Failures, wantFailures) {
		t.Fatalf("Failures = %#v, want %#v", got.Failures, wantFailures)
	}
}

func TestEvaluateMustNotIncludeFailsWhenToolPresent(t *testing.T) {
	testCase := EvalCase{
		Name:           "forbidden-tool",
		MustNotInclude: []string{"delete_record"},
		PolicyOutcome:  "allow",
	}
	trace := []TraceRow{
		{ToolName: "lookup_customer", Decision: "allow"},
		{ToolName: "delete_record", Decision: "allow"},
	}

	got := Evaluate(testCase, trace)

	wantFailure := CheckFailure{
		Check:    "mustNotInclude",
		Expected: "delete_record",
		Observed: "delete_record",
	}
	if got.Passed {
		t.Fatalf("Evaluate() Passed = true, want false")
	}
	if len(got.Failures) != 1 {
		t.Fatalf("len(Failures) = %d, want 1", len(got.Failures))
	}
	if !reflect.DeepEqual(got.Failures[0], wantFailure) {
		t.Fatalf("Failure = %#v, want %#v", got.Failures[0], wantFailure)
	}
}

func TestEvaluatePolicyOutcomeFailsOnMismatch(t *testing.T) {
	testCase := EvalCase{
		Name:          "wrong-outcome",
		PolicyOutcome: "deny",
	}
	trace := []TraceRow{
		{ToolName: "refund_small", Decision: "allow"},
	}

	got := Evaluate(testCase, trace)

	wantFailure := CheckFailure{
		Check:    "policyOutcome",
		Expected: "deny",
		Observed: "allow",
	}
	if got.Passed {
		t.Fatalf("Evaluate() Passed = true, want false")
	}
	if len(got.Failures) != 1 {
		t.Fatalf("len(Failures) = %d, want 1", len(got.Failures))
	}
	if !reflect.DeepEqual(got.Failures[0], wantFailure) {
		t.Fatalf("Failure = %#v, want %#v", got.Failures[0], wantFailure)
	}
}

func TestEvaluateMustNotContainInArgsFailsWhenSubstringPresent(t *testing.T) {
	testCase := EvalCase{
		Name:                 "pii-redaction",
		PolicyOutcome:        "allow",
		MustNotContainInArgs: []string{"123-45-6789"},
	}
	trace := []TraceRow{
		{ToolName: "send_slack_message", Decision: "allow", Arguments: json.RawMessage(`{"message":"ssn 123-45-6789 leaked"}`)},
	}

	got := Evaluate(testCase, trace)

	wantFailure := CheckFailure{
		Check:    "mustNotContainInArgs",
		Expected: "123-45-6789",
		Observed: `{"message":"ssn 123-45-6789 leaked"}`,
	}
	if got.Passed {
		t.Fatalf("Evaluate() Passed = true, want false")
	}
	if len(got.Failures) != 1 {
		t.Fatalf("len(Failures) = %d, want 1", len(got.Failures))
	}
	if !reflect.DeepEqual(got.Failures[0], wantFailure) {
		t.Fatalf("Failure = %#v, want %#v", got.Failures[0], wantFailure)
	}
}

func TestEvaluateCollectsAllFailuresWithoutShortCircuiting(t *testing.T) {
	testCase := EvalCase{
		Name:                 "multiple-failures",
		MustInclude:          []string{"refund_small"},
		MustNotInclude:       []string{"delete_record"},
		PolicyOutcome:        "deny",
		MustNotContainInArgs: []string{"123-45-6789"},
	}
	trace := []TraceRow{
		{ToolName: "delete_record", Decision: "allow", Arguments: json.RawMessage(`{"message":"123-45-6789"}`)},
	}

	got := Evaluate(testCase, trace)

	wantFailures := []CheckFailure{
		{Check: "mustInclude", Expected: "refund_small", Observed: "delete_record"},
		{Check: "mustNotInclude", Expected: "delete_record", Observed: "delete_record"},
		{Check: "policyOutcome", Expected: "deny", Observed: "allow"},
		{Check: "mustNotContainInArgs", Expected: "123-45-6789", Observed: `{"message":"123-45-6789"}`},
	}
	if got.Passed {
		t.Fatalf("Evaluate() Passed = true, want false")
	}
	if !reflect.DeepEqual(got.Failures, wantFailures) {
		t.Fatalf("Failures = %#v, want %#v", got.Failures, wantFailures)
	}
}
