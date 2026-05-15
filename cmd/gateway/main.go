package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"

	"github.com/K8Harness/ToolGate/core/mcp"
	corepolicy "github.com/K8Harness/ToolGate/core/policy"
)

var logFatalf = log.Fatalf

func main() {
	os.Exit(runGateway(os.Stderr))
}

func runGateway(stderr io.Writer) int {
	config, err := LoadConfig()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	server, cleanup, err := buildGatewayServer(ctx, config, logger)
	if err != nil {
		logFatalf("%v", err)
		return 1
	}
	defer cleanup()

	server.log.Info("gateway listening", "addr", fmt.Sprintf(":%d", config.ListenPort))
	if err := server.ListenAndServe(); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}

	return 0
}

func buildGatewayServer(ctx context.Context, config *Config, logger *slog.Logger) (*Server, func(), error) {
	if logger == nil {
		logger = slog.New(slog.NewJSONHandler(os.Stdout, nil))
	}

	policy, err := corepolicy.LoadPolicy(config.PolicyFilePath)
	if err != nil {
		return nil, nil, fmt.Errorf("policy load failed: %w", err)
	}

	pool, err := NewDBPool(ctx, config.PostgresDSN)
	if err != nil {
		return nil, nil, fmt.Errorf("postgres initialization failed: %w", err)
	}

	cleanup := func() {
		pool.Close()
	}

	if err := MigrateSchema(ctx, pool); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("schema migration failed: %w", err)
	}

	budgetTracker := NewBudgetTracker()
	auditWriter := NewAuditWriter(pool, logger)
	auditWriter.Start(ctx)
	ticketStore := NewTicketStore(pool)
	policyGate := NewPolicyGateHandler(policy, budgetTracker, auditWriter, ticketStore, logger)

	forwarder := mcp.NewUpstreamForwarder(config.UpstreamMCPURL, config.UpstreamTimeout)
	pipeline := mcp.NewPipeline(forwarder)
	pipeline.Use(NewRequestLogger(logger))
	pipeline.Use(&ContextInjector{})
	pipeline.Use(policyGate)

	server := NewServer(config, pipeline, logger)
	server.forwarder = forwarder
	return server, cleanup, nil
}

func newGatewayServer(config *Config) *Server {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	forwarder := mcp.NewUpstreamForwarder(config.UpstreamMCPURL, config.UpstreamTimeout)
	pipeline := mcp.NewPipeline(forwarder)
	pipeline.Use(NewRequestLogger(logger))
	pipeline.Use(&ContextInjector{})

	server := NewServer(config, pipeline, logger)
	server.forwarder = forwarder
	return server
}
