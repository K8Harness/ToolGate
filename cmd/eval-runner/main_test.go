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

	exitCode := run(evalRunnerDeps{
		args:   []string{},
		stdout: &bytes.Buffer{},
		stderr: stderr,
		lookPath: func(file string) (string, error) {
			return "", errors.New("executable file not found")
		},
		loadConfig: func() (*Config, error) {
			t.Fatal("loadConfig should not be called when docker is missing")
			return nil, nil
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
	if orch.upCalls != 0 || orch.downCalls != 0 {
		t.Fatalf("orchestrator calls = up:%d down:%d, want none", orch.upCalls, orch.downCalls)
	}
}

func TestRunFailsWhenConfigMissing(t *testing.T) {
	stderr := &bytes.Buffer{}
	orch := &stubOrchestrator{}

	exitCode := run(evalRunnerDeps{
		args:   []string{},
		stdout: &bytes.Buffer{},
		stderr: stderr,
		lookPath: func(file string) (string, error) {
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
	})

	if exitCode != 0 {
		t.Fatalf("run() exitCode = %d, want 0", exitCode)
	}
	if strings.Join(callOrder, ",") != "config,suite,new,up,down" {
		t.Fatalf("callOrder = %v, want config,suite,new,up,down", callOrder)
	}
	if !strings.Contains(stdout.String(), "stack lifecycle complete") {
		t.Fatalf("stdout = %q, want lifecycle completion message", stdout.String())
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
