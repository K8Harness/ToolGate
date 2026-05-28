package main

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestLoadConfigRequiresUpstreamMCPURL(t *testing.T) {
	t.Setenv("GATEWAY_PORT", "")
	t.Setenv("POLICY_FILE", "")
	t.Setenv("POSTGRES_DSN", "postgres://gateway:gateway@localhost:5432/gateway?sslmode=disable")
	t.Setenv("REDIS_DSN", "redis://localhost:6379/0")
	t.Setenv("UPSTREAM_MCP_URL", "")
	t.Setenv("TURN_ID_HEADER", "")
	t.Setenv("UPSTREAM_TIMEOUT", "")
	t.Setenv("SESSION_TTL", "")
	t.Setenv("SESSION_LOCK_TTL", "")
	t.Setenv("LOCK_ACQUIRE_TIMEOUT", "")

	cfg, err := LoadConfig()
	if err == nil {
		t.Fatalf("LoadConfig() error = nil, want missing upstream error")
	}
	if cfg != nil {
		t.Fatalf("LoadConfig() config = %#v, want nil config on error", cfg)
	}
	if !strings.Contains(err.Error(), "UPSTREAM_MCP_URL") {
		t.Fatalf("LoadConfig() error = %q, want message naming UPSTREAM_MCP_URL", err.Error())
	}
}

func TestLoadConfigDefaultsWithOnlyUpstreamMCPURL(t *testing.T) {
	t.Setenv("GATEWAY_PORT", "")
	t.Setenv("POLICY_FILE", "")
	t.Setenv("POSTGRES_DSN", "postgres://gateway:gateway@localhost:5432/gateway?sslmode=disable")
	t.Setenv("REDIS_DSN", "redis://localhost:6379/0")
	t.Setenv("UPSTREAM_MCP_URL", "http://upstream.example/mcp")
	t.Setenv("TURN_ID_HEADER", "")
	t.Setenv("UPSTREAM_TIMEOUT", "")
	t.Setenv("SESSION_TTL", "")
	t.Setenv("SESSION_LOCK_TTL", "")
	t.Setenv("LOCK_ACQUIRE_TIMEOUT", "")
	t.Setenv("SLACK_BOT_TOKEN", "xoxb-default-token")
	t.Setenv("SLACK_SIGNING_SECRET", "default-signing-secret")
	t.Setenv("SLACK_CHANNEL", "#approvals")

	var logs bytes.Buffer
	restoreDefaultLogger := setDefaultLoggerForTest(&logs)
	defer restoreDefaultLogger()

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v, want nil", err)
	}
	if cfg == nil {
		t.Fatalf("LoadConfig() config = nil, want config")
	}
	if cfg.ListenPort != 8080 {
		t.Fatalf("ListenPort = %d, want 8080", cfg.ListenPort)
	}
	if cfg.UpstreamMCPURL != "http://upstream.example/mcp" {
		t.Fatalf("UpstreamMCPURL = %q, want upstream URL", cfg.UpstreamMCPURL)
	}
	if cfg.PolicyFilePath != "policy.yaml" {
		t.Fatalf("PolicyFilePath = %q, want policy.yaml", cfg.PolicyFilePath)
	}
	if cfg.PostgresDSN != "postgres://gateway:gateway@localhost:5432/gateway?sslmode=disable" {
		t.Fatalf("PostgresDSN = %q, want configured DSN", cfg.PostgresDSN)
	}
	if cfg.TurnIDHeader != "X-Mcp-Turn-Id" {
		t.Fatalf("TurnIDHeader = %q, want X-Mcp-Turn-Id", cfg.TurnIDHeader)
	}
	if cfg.UpstreamTimeout != 30*time.Second {
		t.Fatalf("UpstreamTimeout = %s, want 30s", cfg.UpstreamTimeout)
	}
	if cfg.SessionTTL != 60*time.Minute {
		t.Fatalf("SessionTTL = %s, want 60m", cfg.SessionTTL)
	}
	if cfg.RedisDSN != "redis://localhost:6379/0" {
		t.Fatalf("RedisDSN = %q, want configured DSN", cfg.RedisDSN)
	}
	if cfg.SessionLockTTL != 60*time.Second {
		t.Fatalf("SessionLockTTL = %s, want 60s", cfg.SessionLockTTL)
	}
	if cfg.LockAcquireTimeout != 5*time.Second {
		t.Fatalf("LockAcquireTimeout = %s, want 5s", cfg.LockAcquireTimeout)
	}
	if !strings.Contains(logs.String(), "POLICY_FILE not set; using default policy file path") {
		t.Fatalf("startup log = %q, want default policy file notice", logs.String())
	}
	if !strings.Contains(logs.String(), "policy.yaml") {
		t.Fatalf("startup log = %q, want default policy path", logs.String())
	}
}

func TestLoadConfigReadsEnvironmentOverrides(t *testing.T) {
	t.Setenv("GATEWAY_PORT", "9090")
	t.Setenv("POLICY_FILE", "/tmp/policy.yaml")
	t.Setenv("POSTGRES_DSN", "postgres://localhost:5432/toolgate")
	t.Setenv("REDIS_DSN", "redis://localhost:6380/1")
	t.Setenv("UPSTREAM_MCP_URL", "http://localhost:9999/mcp")
	t.Setenv("TURN_ID_HEADER", "X-Turn")
	t.Setenv("UPSTREAM_TIMEOUT", "5s")
	t.Setenv("SESSION_TTL", "2h")
	t.Setenv("SESSION_LOCK_TTL", "90s")
	t.Setenv("LOCK_ACQUIRE_TIMEOUT", "7s")
	t.Setenv("SLACK_BOT_TOKEN", "xoxb-override-token")
	t.Setenv("SLACK_SIGNING_SECRET", "override-signing-secret")
	t.Setenv("SLACK_CHANNEL", "#override-approvals")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v, want nil", err)
	}
	if cfg.ListenPort != 9090 {
		t.Fatalf("ListenPort = %d, want 9090", cfg.ListenPort)
	}
	if cfg.UpstreamMCPURL != "http://localhost:9999/mcp" {
		t.Fatalf("UpstreamMCPURL = %q, want override", cfg.UpstreamMCPURL)
	}
	if cfg.PolicyFilePath != "/tmp/policy.yaml" {
		t.Fatalf("PolicyFilePath = %q, want override", cfg.PolicyFilePath)
	}
	if cfg.PostgresDSN != "postgres://localhost:5432/toolgate" {
		t.Fatalf("PostgresDSN = %q, want override", cfg.PostgresDSN)
	}
	if cfg.TurnIDHeader != "X-Turn" {
		t.Fatalf("TurnIDHeader = %q, want X-Turn", cfg.TurnIDHeader)
	}
	if cfg.UpstreamTimeout != 5*time.Second {
		t.Fatalf("UpstreamTimeout = %s, want 5s", cfg.UpstreamTimeout)
	}
	if cfg.SessionTTL != 2*time.Hour {
		t.Fatalf("SessionTTL = %s, want 2h", cfg.SessionTTL)
	}
	if cfg.RedisDSN != "redis://localhost:6380/1" {
		t.Fatalf("RedisDSN = %q, want override", cfg.RedisDSN)
	}
	if cfg.SessionLockTTL != 90*time.Second {
		t.Fatalf("SessionLockTTL = %s, want 90s", cfg.SessionLockTTL)
	}
	if cfg.LockAcquireTimeout != 7*time.Second {
		t.Fatalf("LockAcquireTimeout = %s, want 7s", cfg.LockAcquireTimeout)
	}
}

func TestLoadConfigRequiresPostgresDSN(t *testing.T) {
	t.Setenv("GATEWAY_PORT", "")
	t.Setenv("POLICY_FILE", "")
	t.Setenv("POSTGRES_DSN", "")
	t.Setenv("REDIS_DSN", "redis://localhost:6379/0")
	t.Setenv("UPSTREAM_MCP_URL", "http://upstream.example/mcp")
	t.Setenv("TURN_ID_HEADER", "")
	t.Setenv("UPSTREAM_TIMEOUT", "")
	t.Setenv("SESSION_TTL", "")
	t.Setenv("SESSION_LOCK_TTL", "")
	t.Setenv("LOCK_ACQUIRE_TIMEOUT", "")

	cfg, err := LoadConfig()
	if err == nil {
		t.Fatalf("LoadConfig() error = nil, want missing POSTGRES_DSN error")
	}
	if cfg != nil {
		t.Fatalf("LoadConfig() config = %#v, want nil config on error", cfg)
	}
	if !strings.Contains(err.Error(), "POSTGRES_DSN") {
		t.Fatalf("LoadConfig() error = %q, want message naming POSTGRES_DSN", err.Error())
	}
}

func TestLoadConfigRequiresRedisDSN(t *testing.T) {
	t.Setenv("GATEWAY_PORT", "")
	t.Setenv("POLICY_FILE", "")
	t.Setenv("POSTGRES_DSN", "postgres://gateway:gateway@localhost:5432/gateway?sslmode=disable")
	t.Setenv("REDIS_DSN", "")
	t.Setenv("UPSTREAM_MCP_URL", "http://upstream.example/mcp")
	t.Setenv("TURN_ID_HEADER", "")
	t.Setenv("UPSTREAM_TIMEOUT", "")
	t.Setenv("SESSION_TTL", "")
	t.Setenv("SESSION_LOCK_TTL", "")
	t.Setenv("LOCK_ACQUIRE_TIMEOUT", "")

	cfg, err := LoadConfig()
	if err == nil {
		t.Fatalf("LoadConfig() error = nil, want missing REDIS_DSN error")
	}
	if cfg != nil {
		t.Fatalf("LoadConfig() config = %#v, want nil config on error", cfg)
	}
	if !strings.Contains(err.Error(), "REDIS_DSN") {
		t.Fatalf("LoadConfig() error = %q, want message naming REDIS_DSN", err.Error())
	}
}

func TestLoadConfigRequiresSlackBotToken(t *testing.T) {
	t.Setenv("GATEWAY_PORT", "")
	t.Setenv("POLICY_FILE", "")
	t.Setenv("POSTGRES_DSN", "postgres://gateway:gateway@localhost:5432/gateway?sslmode=disable")
	t.Setenv("REDIS_DSN", "redis://localhost:6379/0")
	t.Setenv("UPSTREAM_MCP_URL", "http://upstream.example/mcp")
	t.Setenv("TURN_ID_HEADER", "")
	t.Setenv("UPSTREAM_TIMEOUT", "")
	t.Setenv("SESSION_TTL", "")
	t.Setenv("SESSION_LOCK_TTL", "")
	t.Setenv("LOCK_ACQUIRE_TIMEOUT", "")
	t.Setenv("SLACK_BOT_TOKEN", "")
	t.Setenv("SLACK_SIGNING_SECRET", "xsecret")
	t.Setenv("SLACK_CHANNEL", "#approvals")

	cfg, err := LoadConfig()
	if err == nil {
		t.Fatalf("LoadConfig() error = nil, want missing SLACK_BOT_TOKEN error")
	}
	if cfg != nil {
		t.Fatalf("LoadConfig() config = %#v, want nil config on error", cfg)
	}
	if !strings.Contains(err.Error(), "SLACK_BOT_TOKEN") {
		t.Fatalf("LoadConfig() error = %q, want message naming SLACK_BOT_TOKEN", err.Error())
	}
}

func TestLoadConfigRequiresSlackSigningSecret(t *testing.T) {
	t.Setenv("GATEWAY_PORT", "")
	t.Setenv("POLICY_FILE", "")
	t.Setenv("POSTGRES_DSN", "postgres://gateway:gateway@localhost:5432/gateway?sslmode=disable")
	t.Setenv("REDIS_DSN", "redis://localhost:6379/0")
	t.Setenv("UPSTREAM_MCP_URL", "http://upstream.example/mcp")
	t.Setenv("TURN_ID_HEADER", "")
	t.Setenv("UPSTREAM_TIMEOUT", "")
	t.Setenv("SESSION_TTL", "")
	t.Setenv("SESSION_LOCK_TTL", "")
	t.Setenv("LOCK_ACQUIRE_TIMEOUT", "")
	t.Setenv("SLACK_BOT_TOKEN", "xoxb-token")
	t.Setenv("SLACK_SIGNING_SECRET", "")
	t.Setenv("SLACK_CHANNEL", "#approvals")

	cfg, err := LoadConfig()
	if err == nil {
		t.Fatalf("LoadConfig() error = nil, want missing SLACK_SIGNING_SECRET error")
	}
	if cfg != nil {
		t.Fatalf("LoadConfig() config = %#v, want nil config on error", cfg)
	}
	if !strings.Contains(err.Error(), "SLACK_SIGNING_SECRET") {
		t.Fatalf("LoadConfig() error = %q, want message naming SLACK_SIGNING_SECRET", err.Error())
	}
}

func TestLoadConfigRequiresSlackChannel(t *testing.T) {
	t.Setenv("GATEWAY_PORT", "")
	t.Setenv("POLICY_FILE", "")
	t.Setenv("POSTGRES_DSN", "postgres://gateway:gateway@localhost:5432/gateway?sslmode=disable")
	t.Setenv("REDIS_DSN", "redis://localhost:6379/0")
	t.Setenv("UPSTREAM_MCP_URL", "http://upstream.example/mcp")
	t.Setenv("TURN_ID_HEADER", "")
	t.Setenv("UPSTREAM_TIMEOUT", "")
	t.Setenv("SESSION_TTL", "")
	t.Setenv("SESSION_LOCK_TTL", "")
	t.Setenv("LOCK_ACQUIRE_TIMEOUT", "")
	t.Setenv("SLACK_BOT_TOKEN", "xoxb-token")
	t.Setenv("SLACK_SIGNING_SECRET", "xsecret")
	t.Setenv("SLACK_CHANNEL", "")

	cfg, err := LoadConfig()
	if err == nil {
		t.Fatalf("LoadConfig() error = nil, want missing SLACK_CHANNEL error")
	}
	if cfg != nil {
		t.Fatalf("LoadConfig() config = %#v, want nil config on error", cfg)
	}
	if !strings.Contains(err.Error(), "SLACK_CHANNEL") {
		t.Fatalf("LoadConfig() error = %q, want message naming SLACK_CHANNEL", err.Error())
	}
}

func TestLoadConfigReportsAllMissingSlackVars(t *testing.T) {
	t.Setenv("GATEWAY_PORT", "")
	t.Setenv("POLICY_FILE", "")
	t.Setenv("POSTGRES_DSN", "postgres://gateway:gateway@localhost:5432/gateway?sslmode=disable")
	t.Setenv("REDIS_DSN", "redis://localhost:6379/0")
	t.Setenv("UPSTREAM_MCP_URL", "http://upstream.example/mcp")
	t.Setenv("TURN_ID_HEADER", "")
	t.Setenv("UPSTREAM_TIMEOUT", "")
	t.Setenv("SESSION_TTL", "")
	t.Setenv("SESSION_LOCK_TTL", "")
	t.Setenv("LOCK_ACQUIRE_TIMEOUT", "")
	t.Setenv("SLACK_BOT_TOKEN", "")
	t.Setenv("SLACK_SIGNING_SECRET", "")
	t.Setenv("SLACK_CHANNEL", "")

	cfg, err := LoadConfig()
	if err == nil {
		t.Fatalf("LoadConfig() error = nil, want missing Slack vars error")
	}
	if cfg != nil {
		t.Fatalf("LoadConfig() config = %#v, want nil config on error", cfg)
	}
	if !strings.Contains(err.Error(), "SLACK_BOT_TOKEN") {
		t.Fatalf("LoadConfig() error = %q, want message naming SLACK_BOT_TOKEN", err.Error())
	}
	if !strings.Contains(err.Error(), "SLACK_SIGNING_SECRET") {
		t.Fatalf("LoadConfig() error = %q, want message naming SLACK_SIGNING_SECRET", err.Error())
	}
	if !strings.Contains(err.Error(), "SLACK_CHANNEL") {
		t.Fatalf("LoadConfig() error = %q, want message naming SLACK_CHANNEL", err.Error())
	}
}

func TestLoadConfigReadsSlackVars(t *testing.T) {
	t.Setenv("GATEWAY_PORT", "")
	t.Setenv("POLICY_FILE", "")
	t.Setenv("POSTGRES_DSN", "postgres://gateway:gateway@localhost:5432/gateway?sslmode=disable")
	t.Setenv("REDIS_DSN", "redis://localhost:6379/0")
	t.Setenv("UPSTREAM_MCP_URL", "http://upstream.example/mcp")
	t.Setenv("TURN_ID_HEADER", "")
	t.Setenv("UPSTREAM_TIMEOUT", "")
	t.Setenv("SESSION_TTL", "")
	t.Setenv("SESSION_LOCK_TTL", "")
	t.Setenv("LOCK_ACQUIRE_TIMEOUT", "")
	t.Setenv("SLACK_BOT_TOKEN", "xoxb-test-token")
	t.Setenv("SLACK_SIGNING_SECRET", "test-signing-secret")
	t.Setenv("SLACK_CHANNEL", "#test-approvals")

	var logs bytes.Buffer
	restoreDefaultLogger := setDefaultLoggerForTest(&logs)
	defer restoreDefaultLogger()

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v, want nil", err)
	}
	if cfg == nil {
		t.Fatalf("LoadConfig() config = nil, want config")
	}
	if cfg.SlackBotToken != "xoxb-test-token" {
		t.Fatalf("SlackBotToken = %q, want xoxb-test-token", cfg.SlackBotToken)
	}
	if cfg.SlackSigningSecret != "test-signing-secret" {
		t.Fatalf("SlackSigningSecret = %q, want test-signing-secret", cfg.SlackSigningSecret)
	}
	if cfg.SlackChannel != "#test-approvals" {
		t.Fatalf("SlackChannel = %q, want #test-approvals", cfg.SlackChannel)
	}
}

func TestLoadConfigReadsApprovalLockTTL(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("APPROVAL_LOCK_TTL", "15s")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.ApprovalLockTTL != 15*time.Second {
		t.Fatalf("ApprovalLockTTL = %v, want 15s", cfg.ApprovalLockTTL)
	}
}

func TestLoadConfigDefaultsApprovalLockTTLToFiveMinutes(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("APPROVAL_LOCK_TTL", "")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.ApprovalLockTTL != 5*time.Minute {
		t.Fatalf("ApprovalLockTTL = %v, want 5m0s", cfg.ApprovalLockTTL)
	}
}

func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GATEWAY_PORT", "")
	t.Setenv("POLICY_FILE", "")
	t.Setenv("POSTGRES_DSN", "postgres://gateway:gateway@localhost:5432/gateway?sslmode=disable")
	t.Setenv("REDIS_DSN", "redis://localhost:6379/0")
	t.Setenv("UPSTREAM_MCP_URL", "http://upstream.example/mcp")
	t.Setenv("TURN_ID_HEADER", "")
	t.Setenv("UPSTREAM_TIMEOUT", "")
	t.Setenv("SESSION_TTL", "")
	t.Setenv("SESSION_LOCK_TTL", "")
	t.Setenv("LOCK_ACQUIRE_TIMEOUT", "")
	t.Setenv("SLACK_BOT_TOKEN", "xoxb-default-token")
	t.Setenv("SLACK_SIGNING_SECRET", "default-signing-secret")
	t.Setenv("SLACK_CHANNEL", "#approvals")
}

func setDefaultLoggerForTest(dst *bytes.Buffer) func() {
	previous := slog.Default()
	logger := slog.New(slog.NewTextHandler(dst, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)
	return func() {
		slog.SetDefault(previous)
	}
}
