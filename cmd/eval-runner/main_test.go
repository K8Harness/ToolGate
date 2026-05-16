package main

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"
)

type stubOrchestrator struct {
	upCalls   int
	downCalls int
	upErr     error
	downErr   error
	callOrder *[]string
}

func (s *stubOrchestrator) Up(context.Context) error {
	s.upCalls++
	if s.callOrder != nil {
		*s.callOrder = append(*s.callOrder, "up")
	}
	return s.upErr
}

func (s *stubOrchestrator) Down(context.Context) error {
	s.downCalls++
	if s.callOrder != nil {
		*s.callOrder = append(*s.callOrder, "down")
	}
	return s.downErr
}

type stubCaseRunner struct {
	tracesByCase map[string][]TraceRow
	errsByCase   map[string]error
	runCalls     []string
}

func (s *stubCaseRunner) Run(_ context.Context, c EvalCase) ([]TraceRow, error) {
	s.runCalls = append(s.runCalls, c.Name)
	if err := s.errsByCase[c.Name]; err != nil {
		return nil, err
	}
	return s.tracesByCase[c.Name], nil
}

func TestRunUsesDefaultSuitePathWhenArgMissing(t *testing.T) {
	var loadedPath string
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	orch := &stubOrchestrator{}

	exitCode := run(evalRunnerDeps{
		args:   []string{},
		stdout: stdout,
		stderr: stderr,
		lookPath: func(file string) (string, error) {
			if file != "docker" {
				t.Fatalf("lookPath file = %q, want docker", file)
			}
			return "/usr/bin/docker", nil
		},
		loadConfig: func() (*Config, error) {
			return &Config{
				PostgresDSN: "postgres://eval:eval@localhost:5432/eval?sslmode=disable",
				ComposeFile: "deploy/docker-compose.yml",
				AgentURL:    "http://agent.example",
			}, nil
		},
		loadSuite: func(path string) (*EvalSuite, error) {
			loadedPath = path
			return nil, fs.ErrNotExist
		},
		newOrch: func(cfg *Config) stackOrchestrator {
			return orch
		},
	})

	if exitCode == 0 {
		t.Fatalf("run() exitCode = %d, want non-zero", exitCode)
	}
	if loadedPath != defaultSuitePath {
		t.Fatalf("loadSuite path = %q, want %q", loadedPath, defaultSuitePath)
	}
	if !strings.Contains(stderr.String(), defaultSuitePath) {
		t.Fatalf("stderr = %q, want missing default suite path", stderr.String())
	}
	if !strings.Contains(stderr.String(), "no such file or directory") {
		t.Fatalf("stderr = %q, want file-not-found diagnostic", stderr.String())
	}
	if orch.upCalls != 0 || orch.downCalls != 0 {
		t.Fatalf("orchestrator calls = up:%d down:%d, want none", orch.upCalls, orch.downCalls)
	}
}

func TestRunUsesProvidedSuitePath(t *testing.T) {
	var loadedPath string
	stderr := &bytes.Buffer{}
	orch := &stubOrchestrator{}

	exitCode := run(evalRunnerDeps{
		args:   []string{"evalsuite/custom.yaml"},
		stdout: &bytes.Buffer{},
		stderr: stderr,
		lookPath: func(file string) (string, error) {
			return "/usr/bin/docker", nil
		},
		loadConfig: func() (*Config, error) {
			return &Config{
				PostgresDSN: "postgres://eval:eval@localhost:5432/eval?sslmode=disable",
				ComposeFile: "deploy/docker-compose.yml",
				AgentURL:    "http://agent.example",
			}, nil
		},
		loadSuite: func(path string) (*EvalSuite, error) {
			loadedPath = path
			return nil, fs.ErrNotExist
		},
		newOrch: func(cfg *Config) stackOrchestrator {
			return orch
		},
	})

	if exitCode == 0 {
		t.Fatalf("run() exitCode = %d, want non-zero", exitCode)
	}
	if loadedPath != "evalsuite/custom.yaml" {
		t.Fatalf("loadSuite path = %q, want provided path", loadedPath)
	}
	if !strings.Contains(stderr.String(), "evalsuite/custom.yaml") {
		t.Fatalf("stderr = %q, want provided suite path", stderr.String())
	}
	if orch.upCalls != 0 || orch.downCalls != 0 {
		t.Fatalf("orchestrator calls = up:%d down:%d, want none", orch.upCalls, orch.downCalls)
	}
}

func TestRunFailsWhenDockerMissing(t *testing.T) {
	stderr := &bytes.Buffer{}
	orch := &stubOrchestrator{}
	var calls []string

	exitCode := run(evalRunnerDeps{
		args:   []string{},
		stdout: &bytes.Buffer{},
		stderr: stderr,
		lookPath: func(file string) (string, error) {
			calls = append(calls, "docker")
			return "", errors.New("executable file not found")
		},
		loadConfig: func() (*Config, error) {
			calls = append(calls, "config")
			return &Config{
				PostgresDSN: "postgres://eval:eval@localhost:5432/eval?sslmode=disable",
				ComposeFile: "deploy/docker-compose.yml",
				AgentURL:    "http://agent.example",
			}, nil
		},
		loadSuite: func(path string) (*EvalSuite, error) {
			t.Fatal("loadSuite should not be called when docker is missing")
			return nil, nil
		},
		newOrch: func(cfg *Config) stackOrchestrator {
			return orch
		},
	})

	if exitCode == 0 {
		t.Fatalf("run() exitCode = %d, want non-zero", exitCode)
	}
	if !strings.Contains(stderr.String(), "docker not found in PATH") {
		t.Fatalf("stderr = %q, want docker diagnostic", stderr.String())
	}
	if strings.Join(calls, ",") != "config,docker" {
		t.Fatalf("calls = %v, want config,docker", calls)
	}
	if orch.upCalls != 0 || orch.downCalls != 0 {
		t.Fatalf("orchestrator calls = up:%d down:%d, want none", orch.upCalls, orch.downCalls)
	}
}

func TestRunFailsWhenConfigMissing(t *testing.T) {
	stderr := &bytes.Buffer{}
	orch := &stubOrchestrator{}
	lookPathCalled := false

	exitCode := run(evalRunnerDeps{
		args:   []string{},
		stdout: &bytes.Buffer{},
		stderr: stderr,
		lookPath: func(file string) (string, error) {
			lookPathCalled = true
			return "/usr/bin/docker", nil
		},
		loadConfig: func() (*Config, error) {
			return nil, errors.New("missing required environment variable POSTGRES_DSN")
		},
		loadSuite: func(path string) (*EvalSuite, error) {
			t.Fatal("loadSuite should not be called when config fails")
			return nil, nil
		},
		newOrch: func(cfg *Config) stackOrchestrator {
			return orch
		},
	})

	if exitCode == 0 {
		t.Fatalf("run() exitCode = %d, want non-zero", exitCode)
	}
	if !strings.Contains(stderr.String(), "missing required environment variable POSTGRES_DSN") {
		t.Fatalf("stderr = %q, want config diagnostic", stderr.String())
	}
	if lookPathCalled {
		t.Fatal("lookPath should not be called when config loading fails first")
	}
	if orch.upCalls != 0 || orch.downCalls != 0 {
		t.Fatalf("orchestrator calls = up:%d down:%d, want none", orch.upCalls, orch.downCalls)
	}
}

func TestRunBringsUpAndTearsDownStackAfterSuiteLoads(t *testing.T) {
	var callOrder []string
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	orch := &stubOrchestrator{callOrder: &callOrder}
	cfg := &Config{
		PostgresDSN: "postgres://eval:eval@localhost:5432/eval?sslmode=disable",
		ComposeFile: "deploy/docker-compose.yml",
		AgentURL:    "http://agent.example",
	}

	exitCode := run(evalRunnerDeps{
		args:   []string{"evalsuite/custom.yaml"},
		stdout: stdout,
		stderr: stderr,
		lookPath: func(file string) (string, error) {
			return "/usr/bin/docker", nil
		},
		loadConfig: func() (*Config, error) {
			callOrder = append(callOrder, "config")
			return cfg, nil
		},
		loadSuite: func(path string) (*EvalSuite, error) {
			callOrder = append(callOrder, "suite")
			return &EvalSuite{}, nil
		},
		newOrch: func(got *Config) stackOrchestrator {
			if got != cfg {
				t.Fatalf("newOrch config = %#v, want %#v", got, cfg)
			}
			callOrder = append(callOrder, "new")
			return orch
		},
		openDB: func(context.Context, string) (dbCloser, error) {
			callOrder = append(callOrder, "db")
			return nil, nil
		},
	})

	if exitCode != 0 {
		t.Fatalf("run() exitCode = %d, want 0", exitCode)
	}
	if strings.Join(callOrder, ",") != "config,suite,new,up,db,down" {
		t.Fatalf("callOrder = %v, want config,suite,new,up,db,down", callOrder)
	}
	if !strings.Contains(stdout.String(), "0/0 cases passed") {
		t.Fatalf("stdout = %q, want generated report", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestRunDoesNotCallDownWhenUpFails(t *testing.T) {
	stderr := &bytes.Buffer{}
	orch := &stubOrchestrator{upErr: errors.New("docker compose up failed: unhealthy service")}

	exitCode := run(evalRunnerDeps{
		args:   []string{"evalsuite/custom.yaml"},
		stdout: &bytes.Buffer{},
		stderr: stderr,
		lookPath: func(file string) (string, error) {
			return "/usr/bin/docker", nil
		},
		loadConfig: func() (*Config, error) {
			return &Config{
				PostgresDSN: "postgres://eval:eval@localhost:5432/eval?sslmode=disable",
				ComposeFile: "deploy/docker-compose.yml",
				AgentURL:    "http://agent.example",
			}, nil
		},
		loadSuite: func(path string) (*EvalSuite, error) {
			return &EvalSuite{}, nil
		},
		openDB: func(context.Context, string) (dbCloser, error) {
			return nil, nil
		},
		newOrch: func(cfg *Config) stackOrchestrator {
			return orch
		},
	})

	if exitCode == 0 {
		t.Fatalf("run() exitCode = %d, want non-zero", exitCode)
	}
	if orch.upCalls != 1 {
		t.Fatalf("Up() calls = %d, want 1", orch.upCalls)
	}
	if orch.downCalls != 0 {
		t.Fatalf("Down() calls = %d, want 0", orch.downCalls)
	}
	if !strings.Contains(stderr.String(), "docker compose up failed: unhealthy service") {
		t.Fatalf("stderr = %q, want startup failure", stderr.String())
	}
}

func TestRunReturnsNonZeroWhenDeferredDownFails(t *testing.T) {
	stderr := &bytes.Buffer{}
	orch := &stubOrchestrator{downErr: errors.New("docker compose down failed: cleanup error")}

	exitCode := run(evalRunnerDeps{
		args:   []string{"evalsuite/custom.yaml"},
		stdout: &bytes.Buffer{},
		stderr: stderr,
		lookPath: func(file string) (string, error) {
			return "/usr/bin/docker", nil
		},
		loadConfig: func() (*Config, error) {
			return &Config{
				PostgresDSN: "postgres://eval:eval@localhost:5432/eval?sslmode=disable",
				ComposeFile: "deploy/docker-compose.yml",
				AgentURL:    "http://agent.example",
			}, nil
		},
		loadSuite: func(path string) (*EvalSuite, error) {
			return &EvalSuite{}, nil
		},
		openDB: func(context.Context, string) (dbCloser, error) {
			return nil, nil
		},
		newOrch: func(cfg *Config) stackOrchestrator {
			return orch
		},
	})

	if exitCode == 0 {
		t.Fatalf("run() exitCode = %d, want non-zero when deferred Down fails", exitCode)
	}
	if orch.upCalls != 1 || orch.downCalls != 1 {
		t.Fatalf("orchestrator calls = up:%d down:%d, want one each", orch.upCalls, orch.downCalls)
	}
	if !strings.Contains(stderr.String(), "docker compose down failed: cleanup error") {
		t.Fatalf("stderr = %q, want cleanup failure", stderr.String())
	}
}

func TestRunExecutesCasesPrintsStatusesAndReturnsReportExitCode(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	orch := &stubOrchestrator{}
	runner := &stubCaseRunner{
		tracesByCase: map[string][]TraceRow{
			"allow-case": {
				{ToolName: "lookup", Decision: "allow"},
			},
			"deny-case": {
				{ToolName: "dangerous", Decision: "deny"},
			},
		},
		errsByCase: map[string]error{
			"db-error-case": errors.New("db query failed"),
		},
	}
	suite := &EvalSuite{
		Cases: []EvalCase{
			{Name: "allow-case", MustInclude: []string{"lookup"}, PolicyOutcome: "allow"},
			{Name: "deny-case", MustInclude: []string{"safe-tool"}, PolicyOutcome: "allow"},
			{Name: "db-error-case", Input: "trigger"},
		},
	}

	exitCode := run(evalRunnerDeps{
		args:   []string{"evalsuite/custom.yaml"},
		stdout: stdout,
		stderr: stderr,
		lookPath: func(file string) (string, error) {
			return "/usr/bin/docker", nil
		},
		loadConfig: func() (*Config, error) {
			return &Config{
				PostgresDSN: "postgres://eval:eval@localhost:5432/eval?sslmode=disable",
				ComposeFile: "deploy/docker-compose.yml",
				AgentURL:    "http://agent.example",
			}, nil
		},
		loadSuite: func(path string) (*EvalSuite, error) {
			return suite, nil
		},
		newOrch: func(cfg *Config) stackOrchestrator {
			return orch
		},
		openDB: func(context.Context, string) (dbCloser, error) {
			return nil, nil
		},
		newRunner: func(cfg *Config, db dbCloser) caseExecutor {
			return runner
		},
		evaluate: Evaluate,
		report:   GenerateReport,
		exitCode: ExitCode,
	})

	if exitCode != 1 {
		t.Fatalf("run() exitCode = %d, want 1", exitCode)
	}
	if strings.Join(runner.runCalls, ",") != "allow-case,deny-case,db-error-case" {
		t.Fatalf("runner calls = %v, want all cases in order", runner.runCalls)
	}
	output := stdout.String()
	for _, fragment := range []string{
		"[RUN] allow-case",
		"[PASS] allow-case",
		"[RUN] deny-case",
		"[FAIL] deny-case",
		"[RUN] db-error-case",
		"[FAIL] db-error-case",
		"| allow-case | PASS |",
		"| deny-case | FAIL |",
		"| db-error-case | FAIL |",
		"FAIL: 2 case(s) failed",
	} {
		if !strings.Contains(output, fragment) {
			t.Fatalf("stdout = %q, want fragment %q", output, fragment)
		}
	}
	assertOutputOrder(t, output, []string{
		"[RUN] allow-case",
		"[PASS] allow-case",
		"[RUN] deny-case",
		"[FAIL] deny-case",
		"[RUN] db-error-case",
		"[FAIL] db-error-case",
		"| Case | Status |",
		"FAIL: 2 case(s) failed",
	})
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	if orch.upCalls != 1 || orch.downCalls != 1 {
		t.Fatalf("orchestrator calls = up:%d down:%d, want one each", orch.upCalls, orch.downCalls)
	}
}

func assertOutputOrder(t *testing.T, output string, fragments []string) {
	t.Helper()

	lastIndex := -1
	for _, fragment := range fragments {
		idx := strings.Index(output, fragment)
		if idx == -1 {
			t.Fatalf("output = %q, missing fragment %q", output, fragment)
		}
		if idx < lastIndex {
			t.Fatalf("output = %q, fragment %q appeared out of order", output, fragment)
		}
		lastIndex = idx
	}
}

func TestRunFailsAfterComposeUpWhenDBConnectionFails(t *testing.T) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	orch := &stubOrchestrator{}
	var callOrder []string

	exitCode := run(evalRunnerDeps{
		args:   []string{"evalsuite/custom.yaml"},
		stdout: stdout,
		stderr: stderr,
		lookPath: func(file string) (string, error) {
			return "/usr/bin/docker", nil
		},
		loadConfig: func() (*Config, error) {
			return &Config{
				PostgresDSN: "postgres://eval:eval@localhost:5432/eval?sslmode=disable",
				ComposeFile: "deploy/docker-compose.yml",
				AgentURL:    "http://agent.example",
			}, nil
		},
		loadSuite: func(path string) (*EvalSuite, error) {
			callOrder = append(callOrder, "suite")
			return &EvalSuite{}, nil
		},
		newOrch: func(cfg *Config) stackOrchestrator {
			callOrder = append(callOrder, "new")
			return orch
		},
		openDB: func(context.Context, string) (dbCloser, error) {
			callOrder = append(callOrder, "db")
			return nil, errors.New("dial tcp: connection refused")
		},
		newRunner: func(cfg *Config, db dbCloser) caseExecutor {
			t.Fatal("newRunner should not be called when DB connection fails after startup")
			return nil
		},
		evaluate: Evaluate,
		report:   GenerateReport,
		exitCode: ExitCode,
	})

	if exitCode != 1 {
		t.Fatalf("run() exitCode = %d, want 1", exitCode)
	}
	if !strings.Contains(stderr.String(), "dial tcp: connection refused") {
		t.Fatalf("stderr = %q, want DB connection diagnostic", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if strings.Join(callOrder, ",") != "suite,new,db" {
		t.Fatalf("callOrder = %v, want suite,new,db", callOrder)
	}
	if orch.upCalls != 1 || orch.downCalls != 1 {
		t.Fatalf("orchestrator calls = up:%d down:%d, want startup and deferred cleanup", orch.upCalls, orch.downCalls)
	}
}
