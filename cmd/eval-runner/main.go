package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"

	"github.com/jackc/pgx/v5/pgxpool"
)

const defaultSuitePath = "evalsuite/default.yaml"
const defaultComposeProjectName = "eval-gate"

type stackOrchestrator interface {
	Up(ctx context.Context) error
	Down(ctx context.Context) error
}

type dbCloser interface {
	Close()
}

type caseExecutor interface {
	Run(ctx context.Context, c EvalCase) ([]TraceRow, error)
}

type evalRunnerDeps struct {
	args       []string
	stdout     io.Writer
	stderr     io.Writer
	lookPath   func(file string) (string, error)
	loadConfig func() (*Config, error)
	loadSuite  func(path string) (*EvalSuite, error)
	newOrch    func(cfg *Config) stackOrchestrator
	openDB     func(ctx context.Context, dsn string) (dbCloser, error)
	newRunner  func(cfg *Config, db dbCloser) caseExecutor
	evaluate   func(c EvalCase, trace []TraceRow) CaseResult
	report     func(results []CaseResult) string
	exitCode   func(results []CaseResult) int
}

func main() {
	args := os.Args[1:]

	if len(args) > 0 && args[0] == "--serve" {
		suitePath := defaultSuitePath
		if len(args) > 1 {
			suitePath = args[1]
		}
		if err := serve(suitePath); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, err.Error())
			os.Exit(1)
		}
		return
	}

	os.Exit(run(evalRunnerDeps{
		args:       args,
		stdout:     os.Stdout,
		stderr:     os.Stderr,
		lookPath:   exec.LookPath,
		loadConfig: LoadConfig,
		loadSuite:  LoadSuite,
		newOrch: func(cfg *Config) stackOrchestrator {
			return NewOrchestrator(cfg.ComposeFile, defaultComposeProjectName)
		},
		openDB: openPostgresPool,
		newRunner: func(cfg *Config, db dbCloser) caseExecutor {
			pool, _ := db.(*pgxpool.Pool)
			return NewCaseRunner(cfg.AgentURL, pool)
		},
		evaluate: Evaluate,
		report:   GenerateReport,
		exitCode: ExitCode,
	}))
}

func run(deps evalRunnerDeps) (exitCode int) {
	exitCode = 0
	ctx := context.Background()

	if deps.openDB == nil {
		deps.openDB = openPostgresPool
	}
	if deps.newRunner == nil {
		deps.newRunner = func(cfg *Config, db dbCloser) caseExecutor {
			pool, _ := db.(*pgxpool.Pool)
			return NewCaseRunner(cfg.AgentURL, pool)
		}
	}
	if deps.evaluate == nil {
		deps.evaluate = Evaluate
	}
	if deps.report == nil {
		deps.report = GenerateReport
	}
	if deps.exitCode == nil {
		deps.exitCode = ExitCode
	}

	cfg, err := deps.loadConfig()
	if err != nil {
		_, _ = fmt.Fprintln(deps.stderr, err.Error())
		return 1
	}

	if _, err := deps.lookPath("docker"); err != nil {
		_, _ = fmt.Fprintln(deps.stderr, "docker not found in PATH")
		return 1
	}

	suitePath, err := resolveSuitePath(deps.args)
	if err != nil {
		_, _ = fmt.Fprintln(deps.stderr, err.Error())
		return 2
	}

	suite, err := deps.loadSuite(suitePath)
	if err != nil {
		_, _ = fmt.Fprintln(deps.stderr, formatSuiteLoadError(suitePath, err))
		return 1
	}

	orchestrator := deps.newOrch(cfg)
	if err := orchestrator.Up(ctx); err != nil {
		_, _ = fmt.Fprintln(deps.stderr, err.Error())
		return 1
	}

	defer func() {
		if err := orchestrator.Down(ctx); err != nil {
			_, _ = fmt.Fprintln(deps.stderr, err.Error())
			if exitCode == 0 {
				exitCode = 1
			}
		}
	}()

	db, err := deps.openDB(ctx, cfg.PostgresDSN)
	if err != nil {
		_, _ = fmt.Fprintln(deps.stderr, err.Error())
		return 1
	}
	if db != nil {
		defer db.Close()
	}

	runner := deps.newRunner(cfg, db)
	results := make([]CaseResult, 0, len(suite.Cases))
	for _, testCase := range suite.Cases {
		_, _ = fmt.Fprintf(deps.stdout, "[RUN] %s\n", testCase.Name)

		trace, err := runner.Run(ctx, testCase)
		result := CaseResult{Name: testCase.Name}
		if err != nil {
			result.Failures = []CheckFailure{{
				Check:    "run",
				Expected: "case completes successfully",
				Observed: err.Error(),
			}}
		} else {
			result = deps.evaluate(testCase, trace)
		}

		if result.Passed {
			_, _ = fmt.Fprintf(deps.stdout, "[PASS] %s\n", result.Name)
		} else {
			_, _ = fmt.Fprintf(deps.stdout, "[FAIL] %s\n", result.Name)
		}
		results = append(results, result)
	}

	_, _ = fmt.Fprintln(deps.stdout, deps.report(results))
	return deps.exitCode(results)
}

func openPostgresPool(ctx context.Context, dsn string) (dbCloser, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
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
