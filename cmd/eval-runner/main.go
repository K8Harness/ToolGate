package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
)

const defaultSuitePath = "evalsuite/default.yaml"
const defaultComposeProjectName = "eval-gate"

type stackOrchestrator interface {
	Up(ctx context.Context) error
	Down(ctx context.Context) error
}

type evalRunnerDeps struct {
	args       []string
	stdout     io.Writer
	stderr     io.Writer
	lookPath   func(file string) (string, error)
	loadConfig func() (*Config, error)
	loadSuite  func(path string) (*EvalSuite, error)
	newOrch    func(cfg *Config) stackOrchestrator
}

func main() {
	os.Exit(run(evalRunnerDeps{
		args:       os.Args[1:],
		stdout:     os.Stdout,
		stderr:     os.Stderr,
		lookPath:   exec.LookPath,
		loadConfig: LoadConfig,
		loadSuite:  LoadSuite,
		newOrch: func(cfg *Config) stackOrchestrator {
			return NewOrchestrator(cfg.ComposeFile, defaultComposeProjectName)
		},
	}))
}

func run(deps evalRunnerDeps) (exitCode int) {
	exitCode = 0

	if _, err := deps.lookPath("docker"); err != nil {
		fmt.Fprintln(deps.stderr, "docker not found in PATH")
		return 1
	}

	cfg, err := deps.loadConfig()
	if err != nil {
		fmt.Fprintln(deps.stderr, err.Error())
		return 1
	}

	suitePath, err := resolveSuitePath(deps.args)
	if err != nil {
		fmt.Fprintln(deps.stderr, err.Error())
		return 2
	}

	if _, err := deps.loadSuite(suitePath); err != nil {
		fmt.Fprintln(deps.stderr, formatSuiteLoadError(suitePath, err))
		return 1
	}

	orchestrator := deps.newOrch(cfg)
	if err := orchestrator.Up(context.Background()); err != nil {
		fmt.Fprintln(deps.stderr, err.Error())
		return 1
	}

	defer func() {
		if err := orchestrator.Down(context.Background()); err != nil {
			fmt.Fprintln(deps.stderr, err.Error())
			if exitCode == 0 {
				exitCode = 1
			}
		}
	}()

	fmt.Fprintf(deps.stdout, "Eval suite %q loaded; stack lifecycle complete, case execution/report wiring is deferred to task 8.1.\n", suitePath)
	return exitCode
}

func resolveSuitePath(args []string) (string, error) {
	switch len(args) {
	case 0:
		return defaultSuitePath, nil
	case 1:
		return args[0], nil
	default:
		return "", fmt.Errorf("expected at most one eval suite path argument")
	}
}

func formatSuiteLoadError(path string, err error) string {
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Sprintf("failed to load eval suite %q: no such file or directory", path)
	}
	return fmt.Sprintf("failed to load eval suite %q: %v", path, err)
}
