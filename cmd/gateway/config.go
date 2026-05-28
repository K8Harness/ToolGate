package main

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultGatewayPort        = 8080
	defaultPolicyFilePath     = "policy.yaml"
	defaultTurnIDHeader       = "X-Mcp-Turn-Id"
	defaultUpstreamTimeout    = 30 * time.Second
	defaultSessionTTL         = 60 * time.Minute
	defaultSessionLockTTL     = 60 * time.Second
	defaultLockAcquireTimeout = 5 * time.Second
)

type Config struct {
	ListenPort         int
	PolicyFilePath     string
	PostgresDSN        string
	RedisDSN           string
	UpstreamMCPURL     string
	TurnIDHeader       string
	UpstreamTimeout    time.Duration
	SessionTTL         time.Duration
	SessionLockTTL     time.Duration
	LockAcquireTimeout time.Duration
	ApprovalLockTTL       time.Duration // APPROVAL_LOCK_TTL       (optional, default 5m)
	LarkAppID             string        // LARK_APP_ID             (required)
	LarkAppSecret         string        // LARK_APP_SECRET         (required)
	LarkChatID            string        // LARK_CHAT_ID            (required)
	LarkVerificationToken string        // LARK_VERIFICATION_TOKEN (required)
	LarkAPIBaseURL        string        // LARK_API_BASE_URL       (optional, default "https://open.feishu.cn/open-apis")
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
	redisDSN := os.Getenv("REDIS_DSN")
	if redisDSN == "" {
		return nil, fmt.Errorf("missing required environment variable REDIS_DSN")
	}

	larkAppID := os.Getenv("LARK_APP_ID")
	larkAppSecret := os.Getenv("LARK_APP_SECRET")
	larkChatID := os.Getenv("LARK_CHAT_ID")
	larkVerificationToken := os.Getenv("LARK_VERIFICATION_TOKEN")
	var missingLark []string
	if larkAppID == "" {
		missingLark = append(missingLark, "LARK_APP_ID")
	}
	if larkAppSecret == "" {
		missingLark = append(missingLark, "LARK_APP_SECRET")
	}
	if larkChatID == "" {
		missingLark = append(missingLark, "LARK_CHAT_ID")
	}
	if larkVerificationToken == "" {
		missingLark = append(missingLark, "LARK_VERIFICATION_TOKEN")
	}
	if len(missingLark) > 0 {
		return nil, fmt.Errorf("missing required environment variables: %s", strings.Join(missingLark, ", "))
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

	sessionLockTTL, err := envDuration("SESSION_LOCK_TTL", defaultSessionLockTTL)
	if err != nil {
		return nil, err
	}

	lockAcquireTimeout, err := envDuration("LOCK_ACQUIRE_TIMEOUT", defaultLockAcquireTimeout)
	if err != nil {
		return nil, err
	}

	approvalLockTTL, err := envDuration("APPROVAL_LOCK_TTL", 5*time.Minute)
	if err != nil {
		return nil, err
	}

	return &Config{
		ListenPort:         listenPort,
		PolicyFilePath:     envStringWithInfoNotice("POLICY_FILE", defaultPolicyFilePath, "using default policy file path"),
		PostgresDSN:        postgresDSN,
		RedisDSN:           redisDSN,
		UpstreamMCPURL:     upstreamURL,
		TurnIDHeader:       envString("TURN_ID_HEADER", defaultTurnIDHeader),
		UpstreamTimeout:    upstreamTimeout,
		SessionTTL:         sessionTTL,
		SessionLockTTL:     sessionLockTTL,
		LockAcquireTimeout: lockAcquireTimeout,
		ApprovalLockTTL:       approvalLockTTL,
		LarkAppID:             larkAppID,
		LarkAppSecret:         larkAppSecret,
		LarkChatID:            larkChatID,
		LarkVerificationToken: larkVerificationToken,
		LarkAPIBaseURL:        envString("LARK_API_BASE_URL", larkAPIBaseURL),
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
