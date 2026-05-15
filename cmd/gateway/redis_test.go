package main

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestNewRedisClientReturnsErrorForInvalidDSN(t *testing.T) {
	t.Parallel()

	cfg := Config{RedisDSN: "://bad redis dsn"}

	client, err := NewRedisClient(cfg)
	if err == nil {
		t.Fatal("NewRedisClient() error = nil, want parse failure")
	}
	if client != nil {
		t.Fatal("NewRedisClient() client != nil, want nil on parse failure")
	}
	if !strings.Contains(err.Error(), "parse redis dsn") {
		t.Fatalf("error = %q, want redis parse context", err)
	}
}

func TestNewRedisClientReturnsErrorWhenRedisUnreachable(t *testing.T) {
	t.Parallel()

	cfg := Config{RedisDSN: "redis://127.0.0.1:1/0"}

	client, err := NewRedisClient(cfg)
	if err == nil {
		t.Fatal("NewRedisClient() error = nil, want ping failure")
	}
	if client != nil {
		t.Fatal("NewRedisClient() client != nil, want nil on ping failure")
	}
	if !strings.Contains(err.Error(), "ping redis") {
		t.Fatalf("error = %q, want redis ping context", err)
	}
}

func TestNewRedisClientConnectsToLiveRedisWhenConfigured(t *testing.T) {
	t.Parallel()

	dsn := testRedisDSN(t)
	cfg := Config{RedisDSN: dsn}

	client, err := NewRedisClient(cfg)
	if err != nil {
		t.Fatalf("NewRedisClient() error = %v, want nil", err)
	}
	t.Cleanup(func() {
		_ = client.Close()
	})

	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("client.Ping() error = %v, want nil", err)
	}
}

func testRedisDSN(t *testing.T) string {
	t.Helper()

	dsn := os.Getenv("TOOLGATE_TEST_REDIS_DSN")
	if dsn == "" {
		t.Skip("TOOLGATE_TEST_REDIS_DSN is not set; skipping live Redis test")
	}
	return dsn
}
