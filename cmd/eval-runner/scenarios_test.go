package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestScenarioStreamRequiresAgentURLForYAMLScenario(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/run-scenario/stream", strings.NewReader(`{"scenario_id":"mcp-crash"}`))
	rec := httptest.NewRecorder()

	makeScenarioStreamHandler(scenarioDeps{})(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestScenarioStreamRetryStormRequiresGatewayURLOnly(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/run-scenario/stream", strings.NewReader(`{"scenario_id":"retry-storm","gateway_mcp_url":"://bad"}`))
	rec := httptest.NewRecorder()

	makeScenarioStreamHandler(scenarioDeps{})(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestScenarioStreamRetryStormUsesGatewayDefaultWithoutAgentURL(t *testing.T) {
	ran := false
	req := httptest.NewRequest(http.MethodPost, "/run-scenario/stream", strings.NewReader(`{"scenario_id":"retry-storm"}`))
	rec := httptest.NewRecorder()

	makeScenarioStreamHandler(scenarioDeps{
		defaultGatewayMCPURL: "http://gateway.example/mcp",
		newRetryStorm: func(gatewayURL string) scenarioCaseExecutor {
			if gatewayURL != "http://gateway.example/mcp" {
				t.Fatalf("gatewayURL = %q, want default", gatewayURL)
			}
			return scenarioCaseExecutorFunc(func(context.Context) CaseResult {
				ran = true
				return CaseResult{Name: "retry-storm-budget", Passed: true}
			})
		},
	})(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if !ran {
		t.Fatal("retry storm executor did not run")
	}
}

func TestScenarioStreamRetryStormRequestGatewayURLWins(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/run-scenario/stream", strings.NewReader(`{"scenario_id":"retry-storm","gateway_mcp_url":"http://request.example/mcp"}`))
	rec := httptest.NewRecorder()

	makeScenarioStreamHandler(scenarioDeps{
		defaultGatewayMCPURL: "http://default.example/mcp",
		newRetryStorm: func(gatewayURL string) scenarioCaseExecutor {
			if gatewayURL != "http://request.example/mcp" {
				t.Fatalf("gatewayURL = %q, want request override", gatewayURL)
			}
			return scenarioCaseExecutorFunc(func(context.Context) CaseResult {
				return CaseResult{Name: "retry-storm-budget", Passed: true}
			})
		},
	})(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

func TestScenarioStreamRetryStormUsesEnvGatewayURL(t *testing.T) {
	t.Setenv("GATEWAY_MCP_URL", "http://env.example/mcp")
	req := httptest.NewRequest(http.MethodPost, "/run-scenario/stream", strings.NewReader(`{"scenario_id":"retry-storm"}`))
	rec := httptest.NewRecorder()

	makeScenarioStreamHandler(scenarioDeps{
		newRetryStorm: func(gatewayURL string) scenarioCaseExecutor {
			if gatewayURL != "http://env.example/mcp" {
				t.Fatalf("gatewayURL = %q, want env fallback", gatewayURL)
			}
			return scenarioCaseExecutorFunc(func(context.Context) CaseResult {
				return CaseResult{Name: "retry-storm-budget", Passed: true}
			})
		},
	})(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

func TestScenarioStreamRetryStormUsesHardcodedGatewayFallback(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/run-scenario/stream", strings.NewReader(`{"scenario_id":"retry-storm"}`))
	rec := httptest.NewRecorder()

	makeScenarioStreamHandler(scenarioDeps{
		newRetryStorm: func(gatewayURL string) scenarioCaseExecutor {
			if gatewayURL != "http://localhost:18080/mcp" {
				t.Fatalf("gatewayURL = %q, want hardcoded fallback", gatewayURL)
			}
			return scenarioCaseExecutorFunc(func(context.Context) CaseResult {
				return CaseResult{Name: "retry-storm-budget", Passed: true}
			})
		},
	})(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

func TestScenarioStreamYAMLScenarioUsesDefaultAgentURL(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/run-scenario/stream", strings.NewReader(`{"scenario_id":"mcp-crash"}`))
	rec := httptest.NewRecorder()

	makeScenarioStreamHandler(scenarioDeps{
		defaultAgentURL: "http://agent.example",
		newRunner: func(agentURL string) caseExecutor {
			if agentURL != "http://agent.example" {
				t.Fatalf("agentURL = %q, want default", agentURL)
			}
			return serveStubRunner{
				traces: map[string][]TraceRow{"mcp-server-down": {{ToolName: "list_recent_charges", Decision: "upstream_error"}}},
				errs:   map[string]error{},
			}
		},
	})(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

func TestScenarioStreamRejectsUnknownScenario(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/run-scenario/stream", strings.NewReader(`{"scenario_id":"unknown","agent_url":"http://agent.example"}`))
	rec := httptest.NewRecorder()

	makeScenarioStreamHandler(scenarioDeps{})(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestRetryStormExecutorPollsUntilBudgetExceeded(t *testing.T) {
	attempts := 0
	exec := retryStormExecutor{
		gatewayMCPURL: "http://gateway.example/mcp",
		callGateway: func(context.Context, string, string) (string, error) {
			return `{"error":{"message":"budget exceeded"}}`, nil
		},
		queryTrace: func(context.Context, string) ([]TraceRow, error) {
			attempts++
			if attempts < 3 {
				return []TraceRow{{ToolName: "list_recent_charges", Decision: "upstream_error"}}, nil
			}
			return []TraceRow{
				{ToolName: "list_recent_charges", Decision: "upstream_error"},
				{ToolName: "list_recent_charges", Decision: "budgetExceeded"},
			}, nil
		},
		initialize:   func(context.Context) (string, error) { return "session-1", nil },
		pollInterval: time.Millisecond,
		pollTimeout:  50 * time.Millisecond,
	}

	got := exec.Run(context.Background())

	if !got.Passed {
		t.Fatalf("Passed = false, want true; failures = %#v", got.Failures)
	}
	if attempts < 3 {
		t.Fatalf("query attempts = %d, want polling", attempts)
	}
}

func TestRetryStormExecutorFailsWhenInitializeFails(t *testing.T) {
	exec := retryStormExecutor{
		gatewayMCPURL: "http://gateway.example/mcp",
		initialize: func(context.Context) (string, error) {
			return "", errors.New("initialize failed")
		},
	}

	got := exec.Run(context.Background())

	if got.Passed {
		t.Fatal("Passed = true, want false")
	}
	if len(got.Failures) == 0 || got.Failures[0].Check != "run" {
		t.Fatalf("Failures = %#v, want run failure", got.Failures)
	}
}

func TestRetryStormExecutorFailsWhenInitializeReturnsNoSession(t *testing.T) {
	exec := retryStormExecutor{
		gatewayMCPURL: "http://gateway.example/mcp",
		initialize: func(context.Context) (string, error) {
			return "", nil
		},
	}

	got := exec.Run(context.Background())

	if got.Passed {
		t.Fatal("Passed = true, want false")
	}
	if len(got.Failures) == 0 || got.Failures[0].Check != "run" {
		t.Fatalf("Failures = %#v, want run failure", got.Failures)
	}
}

func TestRetryStormExecutorFailsWhenAuditQueryFails(t *testing.T) {
	exec := retryStormExecutor{
		gatewayMCPURL: "http://gateway.example/mcp",
		initialize:    func(context.Context) (string, error) { return "session-1", nil },
		callGateway: func(context.Context, string, string) (string, error) {
			return `{"error":{"message":"budget exceeded"}}`, nil
		},
		queryTrace: func(context.Context, string) ([]TraceRow, error) {
			return nil, errors.New("select failed")
		},
		pollInterval: time.Millisecond,
		pollTimeout:  5 * time.Millisecond,
	}

	got := exec.Run(context.Background())

	if got.Passed {
		t.Fatal("Passed = true, want false")
	}
	if len(got.Failures) == 0 || got.Failures[0].Check != "run" {
		t.Fatalf("Failures = %#v, want run failure", got.Failures)
	}
}

func TestRetryStormExecutorFailsAfterSixNonBudgetResponses(t *testing.T) {
	calls := 0
	exec := retryStormExecutor{
		gatewayMCPURL: "http://gateway.example/mcp",
		initialize:    func(context.Context) (string, error) { return "session-1", nil },
		callGateway: func(context.Context, string, string) (string, error) {
			calls++
			return `{"error":{"message":"upstream unavailable"}}`, nil
		},
		queryTrace: func(context.Context, string) ([]TraceRow, error) {
			return []TraceRow{{ToolName: "list_recent_charges", Decision: "upstream_error"}}, nil
		},
		pollInterval: time.Millisecond,
		pollTimeout:  5 * time.Millisecond,
	}

	got := exec.Run(context.Background())

	if got.Passed {
		t.Fatal("Passed = true, want false")
	}
	if calls != 6 {
		t.Fatalf("calls = %d, want 6", calls)
	}
}

func TestRetryStormExecutorFailsWhenBudgetNeverAppears(t *testing.T) {
	exec := retryStormExecutor{
		gatewayMCPURL: "http://gateway.example/mcp",
		initialize:    func(context.Context) (string, error) { return "session-1", nil },
		callGateway: func(context.Context, string, string) (string, error) {
			return `{"error":{"message":"budget exceeded"}}`, nil
		},
		queryTrace: func(context.Context, string) ([]TraceRow, error) {
			return []TraceRow{{ToolName: "list_recent_charges", Decision: "upstream_error"}}, nil
		},
		pollInterval: time.Millisecond,
		pollTimeout:  5 * time.Millisecond,
	}

	got := exec.Run(context.Background())

	if got.Passed {
		t.Fatal("Passed = true, want false")
	}
	if len(got.Failures) == 0 || got.Failures[0].Check != "policyOutcome" {
		t.Fatalf("Failures = %#v, want policyOutcome failure", got.Failures)
	}
}
