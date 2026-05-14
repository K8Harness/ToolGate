package main

import (
	"bytes"
	"testing"
	"time"
)

func TestRunGatewayReturnsErrorWhenConfigMissing(t *testing.T) {
	t.Setenv("UPSTREAM_MCP_URL", "")

	var stderr bytes.Buffer
	code := runGateway(&stderr)

	if code != 1 {
		t.Fatalf("runGateway() code = %d, want 1", code)
	}
	if got := stderr.String(); got == "" {
		t.Fatal("stderr = empty, want config error output")
	}
}

func TestNewGatewayServerBuildsPipelineAndForwarder(t *testing.T) {
	config := &Config{
		ListenPort:      8080,
		UpstreamMCPURL:  "http://example.invalid",
		TurnIDHeader:    defaultTurnIDHeader,
		UpstreamTimeout: time.Second,
		SessionTTL:      time.Minute,
	}

	server := newGatewayServer(config)

	if server == nil {
		t.Fatal("newGatewayServer() = nil, want server")
	}
	if server.pipeline == nil {
		t.Fatal("server.pipeline = nil, want pipeline")
	}
	if server.forwarder == nil {
		t.Fatal("server.forwarder = nil, want upstream forwarder")
	}
	if server.log == nil {
		t.Fatal("server.log = nil, want logger")
	}
}
