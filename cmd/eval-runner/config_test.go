package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestLoadConfigRequiresPostgresDSN(t *testing.T) {
	t.Setenv("POSTGRES_DSN", "")
	t.Setenv("EVAL_COMPOSE_FILE", "")
	t.Setenv("AGENT_URL", "http://agent.example")

	cfg, err := LoadConfig()
	if err == nil {
		t.Fatalf("LoadConfig() error = nil, want missing POSTGRES_DSN error")
	}
	if cfg != nil {
		t.Fatalf("LoadConfig() config = %#v, want nil config on error", cfg)
	}
	if !strings.Contains(err.Error(), "POSTGRES_DSN") {
		t.Fatalf("LoadConfig() error = %q, want message naming POSTGRES_DSN", err.Error())
	}
}

func TestLoadConfigRequiresAgentURL(t *testing.T) {
	t.Setenv("POSTGRES_DSN", "postgres://eval:eval@localhost:5432/eval?sslmode=disable")
	t.Setenv("EVAL_COMPOSE_FILE", "")
	t.Setenv("AGENT_URL", "")

	cfg, err := LoadConfig()
	if err == nil {
		t.Fatalf("LoadConfig() error = nil, want missing AGENT_URL error")
	}
	if cfg != nil {
		t.Fatalf("LoadConfig() config = %#v, want nil config on error", cfg)
	}
	if !strings.Contains(err.Error(), "AGENT_URL") {
		t.Fatalf("LoadConfig() error = %q, want message naming AGENT_URL", err.Error())
	}
}

func TestLoadConfigDefaultsComposeFile(t *testing.T) {
	t.Setenv("POSTGRES_DSN", "postgres://eval:eval@localhost:5432/eval?sslmode=disable")
	t.Setenv("EVAL_COMPOSE_FILE", "")
	t.Setenv("AGENT_URL", "http://agent.example")

	var logs bytes.Buffer
	restoreDefaultLogger := setDefaultLoggerForTest(&logs)
	defer restoreDefaultLogger()

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v, want nil", err)
	}
	if cfg == nil {
		t.Fatalf("LoadConfig() config = nil, want config")
	}
	if cfg.PostgresDSN != "postgres://eval:eval@localhost:5432/eval?sslmode=disable" {
		t.Fatalf("PostgresDSN = %q, want configured DSN", cfg.PostgresDSN)
	}
	if cfg.ComposeFile != "deploy/docker-compose.yml" {
		t.Fatalf("ComposeFile = %q, want deploy/docker-compose.yml", cfg.ComposeFile)
	}
	if cfg.AgentURL != "http://agent.example" {
		t.Fatalf("AgentURL = %q, want configured URL", cfg.AgentURL)
	}
	if !strings.Contains(logs.String(), "EVAL_COMPOSE_FILE not set; using default compose file path") {
		t.Fatalf("startup log = %q, want default compose file notice", logs.String())
	}
	if !strings.Contains(logs.String(), "deploy/docker-compose.yml") {
		t.Fatalf("startup log = %q, want default compose file path", logs.String())
	}
}

func TestLoadConfigReadsEnvironmentOverrides(t *testing.T) {
	t.Setenv("POSTGRES_DSN", "postgres://localhost:5432/toolgate")
	t.Setenv("EVAL_COMPOSE_FILE", "testdata/eval-compose.yml")
	t.Setenv("AGENT_URL", "http://localhost:8085")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v, want nil", err)
	}
	if cfg.PostgresDSN != "postgres://localhost:5432/toolgate" {
		t.Fatalf("PostgresDSN = %q, want override", cfg.PostgresDSN)
	}
	if cfg.ComposeFile != "testdata/eval-compose.yml" {
		t.Fatalf("ComposeFile = %q, want override", cfg.ComposeFile)
	}
	if cfg.AgentURL != "http://localhost:8085" {
		t.Fatalf("AgentURL = %q, want override", cfg.AgentURL)
	}
}

func setDefaultLoggerForTest(dst *bytes.Buffer) func() {
	previous := slog.Default()
	logger := slog.New(slog.NewJSONHandler(dst, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)
	return func() {
		slog.SetDefault(previous)
	}
}

func TestSharedTypesExposeExpectedFields(t *testing.T) {
	caseInput := EvalCase{
		Name:                 "small-refund-allow",
		Input:                "small-refund",
		MustInclude:          []string{"refund_small"},
		MustNotInclude:       []string{"delete_record"},
		PolicyOutcome:        "allow",
		MustNotContainInArgs: []string{"123-45-6789"},
	}
	suite := EvalSuite{Cases: []EvalCase{caseInput}}
	row := TraceRow{
		ToolName:  "refund_small",
		Decision:  "allow",
		Arguments: json.RawMessage(`{"amount":10}`),
	}
	failure := CheckFailure{
		Check:    "policyOutcome",
		Expected: "allow",
		Observed: "deny",
	}
	result := CaseResult{
		Name:     caseInput.Name,
		Passed:   false,
		Failures: []CheckFailure{failure},
	}

	if len(suite.Cases) != 1 {
		t.Fatalf("len(EvalSuite.Cases) = %d, want 1", len(suite.Cases))
	}
	if suite.Cases[0].MustNotContainInArgs[0] != "123-45-6789" {
		t.Fatalf("MustNotContainInArgs = %v, want %q", suite.Cases[0].MustNotContainInArgs, "123-45-6789")
	}
	if string(row.Arguments) != `{"amount":10}` {
		t.Fatalf("TraceRow.Arguments = %s, want JSON payload", string(row.Arguments))
	}
	if result.Failures[0].Observed != "deny" {
		t.Fatalf("CaseResult.Failures[0].Observed = %q, want deny", result.Failures[0].Observed)
	}
}
