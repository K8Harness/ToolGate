package main

import (
	"bytes"
	"errors"
	"io/fs"
	"strings"
	"testing"
)

func TestRunUsesDefaultSuitePathWhenArgMissing(t *testing.T) {
	var loadedPath string
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}

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
		loadSuite: func(path string) error {
			loadedPath = path
			return fs.ErrNotExist
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
}

func TestRunUsesProvidedSuitePath(t *testing.T) {
	var loadedPath string
	stderr := &bytes.Buffer{}

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
		loadSuite: func(path string) error {
			loadedPath = path
			return fs.ErrNotExist
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
}

func TestRunFailsWhenDockerMissing(t *testing.T) {
	stderr := &bytes.Buffer{}

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
		loadSuite: func(path string) error {
			t.Fatal("loadSuite should not be called when docker is missing")
			return nil
		},
	})

	if exitCode == 0 {
		t.Fatalf("run() exitCode = %d, want non-zero", exitCode)
	}
	if !strings.Contains(stderr.String(), "docker not found in PATH") {
		t.Fatalf("stderr = %q, want docker diagnostic", stderr.String())
	}
}

func TestRunFailsWhenConfigMissing(t *testing.T) {
	stderr := &bytes.Buffer{}

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
		loadSuite: func(path string) error {
			t.Fatal("loadSuite should not be called when config fails")
			return nil
		},
	})

	if exitCode == 0 {
		t.Fatalf("run() exitCode = %d, want non-zero", exitCode)
	}
	if !strings.Contains(stderr.String(), "missing required environment variable POSTGRES_DSN") {
		t.Fatalf("stderr = %q, want config diagnostic", stderr.String())
	}
}
