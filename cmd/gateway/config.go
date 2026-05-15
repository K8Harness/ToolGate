package main

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"
)

const (
	defaultGatewayPort     = 8080
	defaultPolicyFilePath  = "policy.yaml"
	defaultTurnIDHeader    = "X-Mcp-Turn-Id"
	defaultUpstreamTimeout = 30 * time.Second
	defaultSessionTTL      = 60 * time.Minute
)

type Config struct {
	ListenPort      int
	PolicyFilePath  string
	PostgresDSN     string
	UpstreamMCPURL  string
	TurnIDHeader    string
	UpstreamTimeout time.Duration
	SessionTTL      time.Duration
}

func LoadConfig() (*Config, error) {
	upstreamURL := os.Getenv("UPSTREAM_MCP_URL")
	if upstreamURL == "" {
		return nil, fmt.Errorf("missing required environment variable UPSTREAM_MCP_URL")
	}
	postgresDSN := os.Getenv("POSTGRES_DSN")
	if postgresDSN == "" {
		return nil, fmt.Errorf("missing required environment variable POSTGRES_DSN")
	}

	listenPort, err := envInt("GATEWAY_PORT", defaultGatewayPort)
	if err != nil {
		return nil, err
	}

	upstreamTimeout, err := envDuration("UPSTREAM_TIMEOUT", defaultUpstreamTimeout)
	if err != nil {
		return nil, err
	}

	sessionTTL, err := envDuration("SESSION_TTL", defaultSessionTTL)
	if err != nil {
		return nil, err
	}

	return &Config{
		ListenPort:      listenPort,
		PolicyFilePath:  envStringWithInfoNotice("POLICY_FILE", defaultPolicyFilePath, "using default policy file path"),
		PostgresDSN:     postgresDSN,
		UpstreamMCPURL:  upstreamURL,
		TurnIDHeader:    envString("TURN_ID_HEADER", defaultTurnIDHeader),
		UpstreamTimeout: upstreamTimeout,
		SessionTTL:      sessionTTL,
	}, nil
}

func envString(name, fallback string) string {
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	return value
}

func envStringWithInfoNotice(name, fallback, notice string) string {
	value := os.Getenv(name)
	if value == "" {
		slog.Info(name+" not set; "+notice, "value", fallback)
		return fallback
	}
	return value
}

func envInt(name string, fallback int) (int, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}

	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %w", name, err)
	}
	return parsed, nil
}

func envDuration(name string, fallback time.Duration) (time.Duration, error) {
	value := os.Getenv(name)
	if value == "" {
		return fallback, nil
	}

	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %w", name, err)
	}
	return parsed, nil
}
