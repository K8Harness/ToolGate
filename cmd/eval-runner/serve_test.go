package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type serveStubRunner struct {
	traces map[string][]TraceRow
	errs   map[string]error
}

func (s serveStubRunner) Run(_ context.Context, c EvalCase) ([]TraceRow, error) {
	if err := s.errs[c.Name]; err != nil {
		return nil, err
	}
	return s.traces[c.Name], nil
}

func TestRunEvalCaseReturnsRunFailure(t *testing.T) {
	runner := serveStubRunner{errs: map[string]error{"bad": errors.New("agent down")}}

	got := runEvalCase(context.Background(), runner, EvalCase{Name: "bad", Input: "x"})

	if got.Passed {
		t.Fatal("Passed = true, want false")
	}
	if len(got.Failures) != 1 || got.Failures[0].Check != "run" {
		t.Fatalf("Failures = %#v, want run failure", got.Failures)
	}
	if got.Trace != nil {
		t.Fatalf("Trace = %#v, want nil", got.Trace)
	}
}

func TestCustomEvalJSONIncludesTrace(t *testing.T) {
	runnerTrace := []TraceRow{{ToolName: "lookup_customer", Decision: "allow"}}
	runner := serveStubRunner{
		traces: map[string][]TraceRow{"lookup": runnerTrace},
		errs:   map[string]error{},
	}
	suite := &EvalSuite{Cases: []EvalCase{{
		Name:          "lookup",
		Input:         "lookup",
		MustInclude:   []string{"lookup_customer"},
		PolicyOutcome: "allow",
	}}}
	req := httptest.NewRequest(http.MethodPost, "/run-eval", nil)
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()

	makeEvalHandler(runner, suite, nil)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var body evalResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("Decode(response): %v", err)
	}
	if len(body.Cases) != 1 || len(body.Cases[0].Trace) != 1 {
		t.Fatalf("cases = %#v, want trace in response", body.Cases)
	}
	if body.Cases[0].Trace[0].ToolName != "lookup_customer" {
		t.Fatalf("trace = %#v, want lookup_customer", body.Cases[0].Trace)
	}
}

func TestCustomEvalRejectsMissingAgentURL(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/run-eval/custom", strings.NewReader(`{"suite":"cases: []"}`))
	rec := httptest.NewRecorder()

	makeCustomEvalHandler(nil)(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestCustomEvalStreamEmitsCaseEventsAndSummary(t *testing.T) {
	body := `{"agent_url":"http://agent.example","suite":"cases:\n  - name: lookup\n    input: lookup\n    mustInclude:\n      - lookup_customer\n    policyOutcome: allow\n"}`
	req := httptest.NewRequest(http.MethodPost, "/run-eval/custom/stream", strings.NewReader(body))
	rec := httptest.NewRecorder()

	makeCustomEvalStreamHandler(func(agentURL string) caseExecutor {
		if agentURL != "http://agent.example" {
			t.Fatalf("agentURL = %q, want http://agent.example", agentURL)
		}
		return serveStubRunner{
			traces: map[string][]TraceRow{"lookup": {{ToolName: "lookup_customer", Decision: "allow"}}},
			errs:   map[string]error{},
		}
	})(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	got := rec.Body.String()
	for _, want := range []string{"event: case_start", `"name":"lookup"`, "event: case_result", "event: summary"} {
		if !strings.Contains(got, want) {
			t.Fatalf("stream = %q, missing %q", got, want)
		}
	}
	if strings.Index(got, "event: case_start") > strings.Index(got, "event: case_result") {
		t.Fatalf("case_start should precede case_result: %q", got)
	}
}

func TestCustomEvalStreamContinuesAfterRunnerError(t *testing.T) {
	body := `{"agent_url":"http://agent.example","suite":"cases:\n  - name: bad\n    input: bad\n    policyOutcome: allow\n  - name: good\n    input: good\n    mustInclude:\n      - lookup_customer\n    policyOutcome: allow\n"}`
	req := httptest.NewRequest(http.MethodPost, "/run-eval/custom/stream", strings.NewReader(body))
	rec := httptest.NewRecorder()

	makeCustomEvalStreamHandler(func(string) caseExecutor {
		return serveStubRunner{
			traces: map[string][]TraceRow{"good": {{ToolName: "lookup_customer", Decision: "allow"}}},
			errs:   map[string]error{"bad": errors.New("agent down")},
		}
	})(rec, req)

	got := rec.Body.String()
	if strings.Count(got, "event: case_result") != 2 {
		t.Fatalf("case_result count = %d, want 2 in %q", strings.Count(got, "event: case_result"), got)
	}
	if !strings.Contains(got, `"pass_count":1`) {
		t.Fatalf("stream = %q, want one pass in summary", got)
	}
}
