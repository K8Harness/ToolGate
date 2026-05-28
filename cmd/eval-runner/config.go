package main

import (
	"fmt"
	"log/slog"
	"os"
)

const defaultComposeFilePath = "deploy/docker-compose.yml"

type Config struct {
	PostgresDSN string
	ComposeFile string
	AgentURL    string
	SkipCompose bool
}

func LoadConfig() (*Config, error) {
	postgresDSN := os.Getenv("POSTGRES_DSN")
	if postgresDSN == "" {
		return nil, fmt.Errorf("missing required environment variable POSTGRES_DSN")
	}

	agentURL := os.Getenv("AGENT_URL")
	if agentURL == "" {
		return nil, fmt.Errorf("missing required environment variable AGENT_URL")
	}

	return &Config{
		PostgresDSN: postgresDSN,
		ComposeFile: envStringWithInfoNotice("EVAL_COMPOSE_FILE", defaultComposeFilePath, "using default compose file path"),
		AgentURL:    agentURL,
		SkipCompose: os.Getenv("EVAL_SKIP_COMPOSE") == "true",
	}, nil
}

func envStringWithInfoNotice(name, fallback, notice string) string {
	value := os.Getenv(name)
	if value == "" {
		slog.Info(name+" not set; "+notice, "value", fallback)
		return fallback
	}
	return value
}
