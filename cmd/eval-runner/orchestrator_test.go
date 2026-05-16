package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestOrchestratorUpFailsWhenDockerMissing(t *testing.T) {
	t.Cleanup(withTestExecLookPath(func(file string) (string, error) {
		if file != "docker" {
			t.Fatalf("lookPath file = %q, want docker", file)
		}
		return "", errors.New("not found")
	}))

	o := NewOrchestrator("deploy/docker-compose.yml", "eval-gate")
	err := o.Up(context.Background())
	if err == nil {
		t.Fatal("Up() error = nil, want error")
	}
	if !strings.Contains(err.Error(), "docker not found in PATH") {
		t.Fatalf("Up() error = %q, want docker diagnostic", err.Error())
	}
}

func TestOrchestratorUpRunsDockerComposeUp(t *testing.T) {
	t.Cleanup(withTestExecLookPath(func(file string) (string, error) {
		return "/usr/bin/docker", nil
	}))
	t.Cleanup(withTestExecCommandContext(fakeExecCommandContext(t)))
	t.Setenv("GO_WANT_HELPER_PROCESS_MODE", "up-ok")

	o := NewOrchestrator("testdata/compose.yml", "eval-gate")
	if err := o.Up(context.Background()); err != nil {
		t.Fatalf("Up() error = %v", err)
	}
}

func TestOrchestratorDownRunsDockerComposeDown(t *testing.T) {
	t.Cleanup(withTestExecCommandContext(fakeExecCommandContext(t)))
	t.Setenv("GO_WANT_HELPER_PROCESS_MODE", "down-ok")

	o := NewOrchestrator("testdata/compose.yml", "eval-gate")
	if err := o.Down(context.Background()); err != nil {
		t.Fatalf("Down() error = %v", err)
	}
}

func TestOrchestratorUpReturnsTailOfComposeFailure(t *testing.T) {
	t.Cleanup(withTestExecLookPath(func(file string) (string, error) {
		return "/usr/bin/docker", nil
	}))
	t.Cleanup(withTestExecCommandContext(fakeExecCommandContext(t)))

	var lines []string
	for i := 1; i <= 25; i++ {
		lines = append(lines, fmt.Sprintf("compose stderr line %02d", i))
	}
	o := NewOrchestrator("testdata/compose.yml", "eval-gate")
	t.Setenv("GO_WANT_HELPER_PROCESS_MODE", "up-fail")
	t.Setenv("GO_WANT_HELPER_PROCESS_STDERR", strings.Join(lines, "\n"))

	err := o.Up(context.Background())
	if err == nil {
		t.Fatal("Up() error = nil, want error")
	}
	msg := err.Error()
	if strings.Contains(msg, "compose stderr line 01") {
		t.Fatalf("Up() error = %q, should not include early stderr lines", msg)
	}
	if !strings.Contains(msg, "compose stderr line 06") {
		t.Fatalf("Up() error = %q, want tail starting at line 06", msg)
	}
	if !strings.Contains(msg, "compose stderr line 25") {
		t.Fatalf("Up() error = %q, want final stderr line", msg)
	}
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}

	mode := os.Getenv("GO_WANT_HELPER_PROCESS_MODE")
	args := os.Args
	idx := 0
	for i, arg := range args {
		if arg == "--" {
			idx = i + 1
			break
		}
	}

	if idx == 0 || idx >= len(args) {
		fmt.Fprintln(os.Stderr, "missing helper args")
		os.Exit(2)
	}

	got := strings.Join(args[idx:], " ")
	switch mode {
	case "up-ok":
		want := "docker compose -f testdata/compose.yml -p eval-gate up -d --build --wait"
		if got != want {
			fmt.Fprintf(os.Stderr, "got %q want %q\n", got, want)
			os.Exit(2)
		}
		os.Exit(0)
	case "down-ok":
		want := "docker compose -f testdata/compose.yml -p eval-gate down -v"
		if got != want {
			fmt.Fprintf(os.Stderr, "got %q want %q\n", got, want)
			os.Exit(2)
		}
		os.Exit(0)
	case "up-fail":
		want := "docker compose -f testdata/compose.yml -p eval-gate up -d --build --wait"
		if got != want {
			fmt.Fprintf(os.Stderr, "got %q want %q\n", got, want)
			os.Exit(2)
		}
		if stderr := os.Getenv("GO_WANT_HELPER_PROCESS_STDERR"); stderr != "" {
			fmt.Fprintln(os.Stderr, stderr)
		}
		os.Exit(1)
	default:
		fmt.Fprintf(os.Stderr, "unexpected helper mode %q", mode)
		os.Exit(2)
	}
}

func fakeExecCommandContext(t *testing.T) func(context.Context, string, ...string) *exec.Cmd {
	t.Helper()

	return func(ctx context.Context, name string, args ...string) *exec.Cmd {
		helperArgs := []string{"-test.run=TestHelperProcess", "--", name}
		helperArgs = append(helperArgs, args...)
		cmd := exec.CommandContext(ctx, os.Args[0], helperArgs...)
		cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1")
		return cmd
	}
}

func withTestExecLookPath(fn func(string) (string, error)) func() {
	prev := execLookPath
	execLookPath = fn
	return func() {
		execLookPath = prev
	}
}

func withTestExecCommandContext(fn func(context.Context, string, ...string) *exec.Cmd) func() {
	prev := execCommandContext
	execCommandContext = fn
	return func() {
		execCommandContext = prev
	}
}
