package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type caseStartEvent struct {
	Name  string `json:"name"`
	Index int    `json:"index"`
	Total int    `json:"total"`
}

type caseResultEvent struct {
	Index  int        `json:"index"`
	Total  int        `json:"total"`
	Result CaseResult `json:"result"`
}

type runnerFactory func(agentURL string) caseExecutor

func writeSSE(w http.ResponseWriter, event string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
		return err
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("streaming unsupported")
	}
	flusher.Flush()
	return nil
}

func streamEvalSuite(ctx context.Context, w http.ResponseWriter, runner caseExecutor, cases []EvalCase) {
	results := make([]CaseResult, 0, len(cases))
	total := len(cases)
	for index, testCase := range cases {
		if err := writeSSE(w, "case_start", caseStartEvent{Name: testCase.Name, Index: index, Total: total}); err != nil {
			return
		}
		result := runEvalCase(ctx, runner, testCase)
		results = append(results, result)
		if err := writeSSE(w, "case_result", caseResultEvent{Index: index, Total: total, Result: result}); err != nil {
			return
		}
	}
	_ = writeSSE(w, "summary", summarizeResults(results))
}

func makeCustomEvalStreamHandler(newRunner runnerFactory) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Suite    string `json:"suite"`
			AgentURL string `json:"agent_url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, fmt.Sprintf("invalid request: %v", err), http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(body.AgentURL) == "" {
			http.Error(w, "missing agent_url", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(body.Suite) == "" {
			http.Error(w, "missing suite", http.StatusBadRequest)
			return
		}

		suite, err := LoadSuiteFromReader(strings.NewReader(body.Suite))
		if err != nil {
			http.Error(w, fmt.Sprintf("invalid suite: %v", err), http.StatusBadRequest)
			return
		}

		if _, ok := w.(http.Flusher); !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")

		streamEvalSuite(r.Context(), w, newRunner(body.AgentURL), suite.Cases)
	}
}
