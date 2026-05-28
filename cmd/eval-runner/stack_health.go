package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const stackHealthProbeTimeout = 750 * time.Millisecond

type stackHealthResponse struct {
	Services []stackHealthService `json:"services"`
}

type stackHealthService struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

type stackHealthDeps struct {
	pool       *pgxpool.Pool
	httpClient *http.Client
	gatewayURL string
	mcpAddr    string
	slackURL   string
}

func makeStackHealthHandler(deps stackHealthDeps) http.HandlerFunc {
	gatewayURL := deps.gatewayURL
	if gatewayURL == "" {
		gatewayURL = "http://localhost:18080/mcp"
	}
	mcpAddr := deps.mcpAddr
	if mcpAddr == "" {
		mcpAddr = "127.0.0.1:18421"
	}
	slackURL := deps.slackURL
	if slackURL == "" {
		slackURL = "http://localhost:18090/healthz"
	}
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(stackHealthResponse{
			Services: []stackHealthService{
				probeHTTPService(deps.httpClient, "Gateway", gatewayURL),
				probeTCPService("MCP", mcpAddr),
				probeHTTPService(deps.httpClient, "Lark", slackURL),
				probePostgresService(deps.pool),
			},
		})
	}
}

func probeHTTPService(client *http.Client, name, target string) stackHealthService {
	if client == nil {
		client = &http.Client{Timeout: stackHealthProbeTimeout}
	}
	req, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return stackHealthService{Name: name, Status: "unknown", Detail: err.Error()}
	}
	resp, err := client.Do(req)
	if err != nil {
		return stackHealthService{Name: name, Status: "down", Detail: target}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return stackHealthService{Name: name, Status: "up", Detail: target}
	}
	return stackHealthService{Name: name, Status: "down", Detail: target}
}

func probeTCPService(name, target string) stackHealthService {
	conn, err := net.DialTimeout("tcp", target, stackHealthProbeTimeout)
	if err != nil {
		return stackHealthService{Name: name, Status: "down", Detail: target}
	}
	_ = conn.Close()
	return stackHealthService{Name: name, Status: "up", Detail: target}
}

func probePostgresService(pool *pgxpool.Pool) stackHealthService {
	if pool == nil {
		return stackHealthService{Name: "Postgres", Status: "unknown", Detail: "pool unavailable"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), stackHealthProbeTimeout)
	defer cancel()
	if err := pool.Ping(ctx); err != nil {
		return stackHealthService{Name: "Postgres", Status: "down", Detail: "configured DSN unreachable"}
	}
	return stackHealthService{Name: "Postgres", Status: "up", Detail: "configured DSN reachable"}
}
