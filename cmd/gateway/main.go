package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/K8Harness/ToolGate/core/mcp"
)

func main() {
	os.Exit(runGateway(os.Stderr))
}

func runGateway(stderr io.Writer) int {
	config, err := LoadConfig()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}

	server := newGatewayServer(config)
	server.log.Info("gateway listening", "addr", fmt.Sprintf(":%d", config.ListenPort))
	if err := server.ListenAndServe(); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}

	return 0
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
