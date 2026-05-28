package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed ui.html
var uiHTML []byte

type evalResponse struct {
	Passed     bool         `json:"passed"`
	PassCount  int          `json:"pass_count"`
	TotalCount int          `json:"total_count"`
	Cases      []CaseResult `json:"cases"`
	Report     string       `json:"report"`
}

func serve(suitePath string) error {
	cfg, err := LoadConfig()
	if err != nil {
		return err
	}

	suite, err := LoadSuite(suitePath)
	if err != nil {
		return fmt.Errorf("load suite: %w", err)
	}

	ctx := context.Background()
	db, err := openPostgresPool(ctx, cfg.PostgresDSN)
	if err != nil {
		return fmt.Errorf("connect to postgres: %w", err)
	}
	defer db.Close()

	pool, _ := db.(*pgxpool.Pool)
	runner := NewCaseRunner(cfg.AgentURL, pool)

	// AI agent runner — optional, only active when AI_AGENT_URL is set
	aiAgentURL := os.Getenv("AI_AGENT_URL")
	var aiRunner caseExecutor
	var aiSuite *EvalSuite
	if aiAgentURL != "" {
		aiRunner = NewCaseRunner(aiAgentURL, pool)
		aiSuitePath := os.Getenv("AI_SUITE_PATH")
		if aiSuitePath == "" {
			aiSuitePath = "evalsuite/ai-agent.yaml"
		}
		aiSuite, err = LoadSuite(aiSuitePath)
		if err != nil {
			return fmt.Errorf("load AI suite: %w", err)
		}
	}

	port := os.Getenv("EVAL_SERVE_PORT")
	if port == "" {
		port = "8099"
	}

	http.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(uiHTML)
	})

	http.HandleFunc("POST /run-eval", makeEvalHandler(runner, suite, pool))

	http.HandleFunc("POST /run-eval/ai", func(w http.ResponseWriter, r *http.Request) {
		if aiRunner == nil {
			http.Error(w, `{"error":"AI_AGENT_URL not configured"}`, http.StatusServiceUnavailable)
			return
		}
		makeEvalHandler(aiRunner, aiSuite, pool)(w, r)
	})

	http.HandleFunc("POST /run-eval/custom", makeCustomEvalHandler(pool))

	http.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	slog.Info("eval server listening", "port", port)
	return http.ListenAndServe(":"+port, nil)
}

func makeCustomEvalHandler(pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Suite    string `json:"suite"`
			AgentURL string `json:"agent_url"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, fmt.Sprintf("invalid request: %v", err), http.StatusBadRequest)
			return
		}
		if body.AgentURL == "" {
			http.Error(w, "missing agent_url", http.StatusBadRequest)
			return
		}
		if body.Suite == "" {
			http.Error(w, "missing suite", http.StatusBadRequest)
			return
		}

		suite, err := LoadSuiteFromReader(strings.NewReader(body.Suite))
		if err != nil {
			http.Error(w, fmt.Sprintf("invalid suite: %v", err), http.StatusBadRequest)
			return
		}

		runner := NewCaseRunner(body.AgentURL, pool)
		makeEvalHandler(runner, suite, pool)(w, r)
	}
}

func makeEvalHandler(runner caseExecutor, suite *EvalSuite, _ *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		results := make([]CaseResult, 0, len(suite.Cases))
		for _, testCase := range suite.Cases {
			trace, err := runner.Run(r.Context(), testCase)
			result := CaseResult{Name: testCase.Name}
			if err != nil {
				result.Failures = []CheckFailure{{
					Check:    "run",
					Expected: "case completes successfully",
					Observed: err.Error(),
				}}
			} else {
				result = Evaluate(testCase, trace)
			}
			results = append(results, result)
		}

		passCount := 0
		for _, r := range results {
			if r.Passed {
				passCount++
			}
		}

		report := GenerateReport(results)

		if r.Header.Get("Accept") == "application/json" {
			resp := evalResponse{
				Passed:     passCount == len(results),
				PassCount:  passCount,
				TotalCount: len(results),
				Cases:      results,
				Report:     report,
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
			return
		}

		w.Header().Set("Content-Type", "text/plain")
		_, _ = fmt.Fprint(w, report)
	}
}
