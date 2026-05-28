#!/usr/bin/env bash
set -euo pipefail

COMPOSE="docker compose"
GATEWAY_URL="http://localhost:18080"
POSTGRES_DSN="postgres://gateway:gateway@127.0.0.1:15432/gateway?sslmode=disable"
AGENT_URL="http://127.0.0.1:18086"

# eval runner is invoked with EVAL_SKIP_COMPOSE=true so it only runs evals
# against the already-running stack — this script owns the Docker lifecycle.
eval_run() {
  POSTGRES_DSN="$POSTGRES_DSN" \
  AGENT_URL="$AGENT_URL" \
  EVAL_SKIP_COMPOSE=true \
  go run ./cmd/eval-runner "$@" 2>&1 || true
}

pass() { echo "  ✓ $1"; }
fail() { echo "  ✗ $1"; exit 1; }
section() { echo ""; echo "━━━ $1 ━━━"; }

# ─── Teardown on exit ─────────────────────────────────────────────────────────
trap '$COMPOSE down -v 2>/dev/null || true' EXIT

section "Starting full stack"
$COMPOSE up -d --wait
echo "  Stack healthy"

# Warm the gateway's capability cache (initialize + tools/list) while all services
# are healthy so it can serve cached responses when localstripe-mcp is stopped.
WARMUP_SESSION=$(curl -s -D - -X POST "$GATEWAY_URL/mcp" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"warmup","version":"1.0"}}}' \
  | grep -i "^Mcp-Session-Id:" | awk '{print $2}' | tr -d '\r\n')
if [ -n "$WARMUP_SESSION" ]; then
  curl -s -X POST "$GATEWAY_URL/mcp" \
    -H "Content-Type: application/json" \
    -H "Mcp-Session-Id: $WARMUP_SESSION" \
    -d '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}' > /dev/null
  echo "  Gateway capability cache warmed (session $WARMUP_SESSION)"
fi

# ─── Scenario 1: MCP server crash ─────────────────────────────────────────────
section "SCENARIO 1 — MCP Server Crash (proxy resilience + eval gate)"
echo "  [FAULT] Stopping localstripe-mcp..."
$COMPOSE stop localstripe-mcp

echo "  Running eval case: mcp-server-down"
EVAL_RESULT=$(eval_run evalsuite/resilience-s1.yaml)

if echo "$EVAL_RESULT" | grep -q "\[PASS\] mcp-server-down"; then
  pass "Gateway surfaced clean upstream_error — audit trail preserved"
else
  echo "$EVAL_RESULT"
  fail "Expected mcp-server-down PASS"
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
$COMPOSE up -d --wait localstripe-mcp

# Ensure localstripe has demo charges so the eval agent can find something to refund.
docker exec -i toolgate-eval-trigger-1 python3 - <<'PYEOF'
import asyncio, sys
sys.path.insert(0, "/app")
from demo_webapp.stripe_client import StripeClient
from demo_webapp.seed import seed_demo_customer

async def main():
    client = StripeClient("http://localstripe:8420", "sk_test_12345")
    try:
        cust = await client.find_customer_by_email("alice@example.com")
        if cust is None:
            cust = await client.create_customer("alice@example.com", "Alice")
            await seed_demo_customer(client, cust["id"])
            print("  Seeded alice@example.com with demo charges")
        else:
            print("  alice@example.com already seeded")
    finally:
        await client.aclose()

asyncio.run(main())
PYEOF

# Re-warm gateway's upstream session after mcp restart so the eval-trigger
# connection hits a valid upstream session rather than triggering stale-session
# revalidation mid-flight.
S3_WARMUP=$(curl -s -D - -X POST "$GATEWAY_URL/mcp" \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"warmup-s3","version":"1.0"}}}' \
  | grep -i "^Mcp-Session-Id:" | awk '{print $2}' | tr -d '\r\n')
if [ -n "$S3_WARMUP" ]; then
  curl -s -X POST "$GATEWAY_URL/mcp" \
    -H "Content-Type: application/json" \
    -H "Mcp-Session-Id: $S3_WARMUP" \
    -d '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}' > /dev/null
  echo "  Gateway upstream session refreshed (session $S3_WARMUP)"
fi

echo "  [FAULT] Stopping mock-slack..."
$COMPOSE stop mock-slack

echo "  Running eval case: approval-timeout-slack-down (waiting up to 90s for timeout...)"
EVAL_RESULT=$(eval_run evalsuite/resilience-s3.yaml)

if echo "$EVAL_RESULT" | grep -q "\[PASS\] approval-timeout-slack-down"; then
  pass "Slack outage did not hang or panic — approval expired gracefully after 15s"
else
  echo "$EVAL_RESULT"
  fail "Expected approval-timeout-slack-down PASS"
fi

# ─── Summary (teardown handled by trap) ───────────────────────────────────────
echo ""
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
echo "  3/3 resilience scenarios passed"
echo "  ToolGate held under: MCP crash . retry storm . Slack outage"
echo "━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━"
