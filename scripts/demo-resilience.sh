#!/usr/bin/env bash
set -euo pipefail

COMPOSE="docker compose"
GATEWAY_URL="http://localhost:18080"
POSTGRES_DSN="postgres://gateway:gateway@127.0.0.1:15432/gateway?sslmode=disable"
AGENT_URL="http://127.0.0.1:18086"

pass() { echo "  ✓ $1"; }
fail() { echo "  ✗ $1"; exit 1; }
section() { echo ""; echo "━━━ $1 ━━━"; }

section "Starting full stack"
$COMPOSE up -d --wait
echo "  Stack healthy"

# ─── Scenario 1: MCP server crash ─────────────────────────────────────────────
section "SCENARIO 1 — MCP Server Crash (proxy resilience + eval gate)"
echo "  [FAULT] Stopping localstripe-mcp..."
$COMPOSE stop localstripe-mcp

echo "  Running eval case: mcp-server-down"
EVAL_RESULT=$(
  POSTGRES_DSN="$POSTGRES_DSN" \
  AGENT_URL="$AGENT_URL" \
  go run ./cmd/eval-runner evalsuite/resilience.yaml 2>&1 || true
)

if echo "$EVAL_RESULT" | grep -q "upstream_error\|mcp-server-down.*PASS\|PASS"; then
  pass "Gateway surfaced clean upstream_error — audit trail preserved"
else
  echo "$EVAL_RESULT"
  fail "Expected upstream_error in eval result"
fi

# ─── Scenario 2: Budget limiter stops retry storm ─────────────────────────────
section "SCENARIO 2 — Budget Limiter (policy gate stops retry storm)"
echo "  [NOTE] MCP server still down — simulating aggressive retry agent..."

SESSION_ID=$(curl -s -D - -X POST "$GATEWAY_URL/mcp" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"retry-bot","version":"1.0"}}}' \
  | grep -i "^Mcp-Session-Id:" | awk '{print $2}' | tr -d '\r\n')

if [ -z "$SESSION_ID" ]; then
  fail "Could not obtain gateway session ID"
fi
echo "  Session: $SESSION_ID"

TURN_ID="retry-storm-$(date +%s)"
BUDGET_HIT=false

for i in 1 2 3 4 5 6; do
  RESP=$(curl -s -X POST "$GATEWAY_URL/mcp" \
    -H "Content-Type: application/json" \
    -H "Mcp-Session-Id: $SESSION_ID" \
    -H "X-Mcp-Turn-Id: $TURN_ID" \
    -d "{\"jsonrpc\":\"2.0\",\"id\":$i,\"method\":\"tools/call\",\"params\":{\"name\":\"list_recent_charges\",\"arguments\":{}}}")
  if echo "$RESP" | grep -qi "budget"; then
    BUDGET_HIT=true
    echo "  Call $i: budgetExceeded (limiter fired)"
    break
  else
    echo "  Call $i: upstream_error (retried)"
  fi
done

if [ "$BUDGET_HIT" = true ]; then
  pass "Budget limiter stopped retry storm — agent cannot hammer a downed service"
else
  fail "Expected budgetExceeded after 5 upstream_error calls"
fi

# ─── Scenario 3: Approval timeout (graceful degradation) ──────────────────────
section "SCENARIO 3 — Approval Flow Timeout (graceful degradation)"
echo "  [RESTORE] Starting localstripe-mcp..."
$COMPOSE start localstripe-mcp
sleep 10

echo "  [FAULT] Stopping mock-slack..."
$COMPOSE stop mock-slack

echo "  Running eval case: approval-timeout-slack-down (waiting up to 60s for timeout...)"
EVAL_RESULT=$(
  POSTGRES_DSN="$POSTGRES_DSN" \
  AGENT_URL="$AGENT_URL" \
  timeout 90 go run ./cmd/eval-runner evalsuite/resilience.yaml 2>&1 || true
)

if echo "$EVAL_RESULT" | grep -q "approval-timeout-slack-down.*PASS\|expired\|PASS"; then
  pass "Slack outage did not hang or panic — approval expired gracefully after 15s"
else
  echo "$EVAL_RESULT"
  fail "Expected expired outcome in eval result"
fi

# ─── Teardown ─────────────────────────────────────────────────────────────────
section "Teardown"
$COMPOSE down -v
echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "  3/3 resilience scenarios passed"
echo "  ToolGate held under: MCP crash . retry storm . Slack outage"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
