# ToolGate

ToolGate is an MCP gateway that enforces policy on every tool call an AI agent makes — logging decisions, requiring human approval for sensitive operations, and surfacing clean errors when upstream services fail.

## Prerequisites

- Docker + Docker Compose
- Go 1.22+
- `ANTHROPIC_API_KEY` set in your environment (or in `.env`)

## Quick start — resilience demo UI

The demo UI lets you run three fault-injection scenarios against a live stack and watch the audit trail update in real time.

### 1. Build the gateway binary

The compose stack mounts a pre-built binary instead of compiling inside Docker:

```bash
make build-compose-bins
```

### 2. Start the full stack

```bash
source .env          # loads ANTHROPIC_API_KEY and optional overrides
docker compose up -d --wait
```

Services started:

| Service | Host port | Purpose |
|---|---|---|
| `gateway` | 18080 | ToolGate MCP gateway |
| `localstripe` | 18420 | Fake Stripe API |
| `localstripe-mcp` | 18421 | MCP server wrapping localstripe |
| `eval-trigger` | 18086 | Python agent that the eval runner drives |
| `mock-slack` | 18090 | Fake Slack (receives approval requests) |
| `postgres` | 15432 | Audit log store |

### 3. Start the eval runner UI

```bash
POSTGRES_DSN="postgres://gateway:gateway@127.0.0.1:15432/gateway?sslmode=disable" \
AGENT_URL="http://127.0.0.1:18086" \
go run ./cmd/eval-runner --serve evalsuite/resilience.yaml
```

Open **http://localhost:8099** in your browser.

---

## Running the three scenarios

Each scenario requires a specific stack state. The **Stack Health** panel in the UI shows the current state of each service — use **Refresh Health** before running.

### Scenario 1 — MCP Crash

**What it tests:** Gateway surfaces a clean `upstream_error` when the upstream MCP server is unavailable.

**Required state:** Gateway up, MCP down, Slack any, Postgres up.

```bash
# Warm the gateway capability cache while MCP is healthy
SESSION=$(curl -s -D - -X POST http://localhost:18080/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"warmup","version":"1.0"}}}' \
  | grep -i "^Mcp-Session-Id:" | awk '{print $2}' | tr -d '\r\n')
curl -s -X POST http://localhost:18080/mcp \
  -H "Content-Type: application/json" \
  -H "Mcp-Session-Id: $SESSION" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}' > /dev/null

# Inject the fault
docker compose stop localstripe-mcp
```

Click **MCP Crash → Run Scenario**.

**Expected result:** `list_recent_charges → allow → upstream_error` — the gateway served the tool list from its capability cache and recorded the upstream failure.

### Scenario 2 — Retry Storm

**What it tests:** Budget limiter stops an agent from hammering a downed service.

**Required state:** Gateway up, MCP down (carry over from Scenario 1).

No additional setup needed. Click **Retry Storm → Run Scenario**.

**Expected result:** Five `allow` decisions followed by `budgetExceeded`.

### Scenario 3 — Approval Timeout

**What it tests:** An `approvalRequired` decision expires gracefully when Slack is unreachable.

**Required state:** Gateway up, MCP up, Slack down, Postgres up.

```bash
# Restore MCP
docker compose start localstripe-mcp

# Wait for it to become healthy, then seed demo charges for alice@example.com
until docker inspect toolgate-localstripe-mcp-1 \
  --format '{{.State.Health.Status}}' 2>/dev/null | grep -q healthy; do sleep 2; done

docker exec toolgate-eval-trigger-1 python3 -c "
import asyncio, sys
sys.path.insert(0, '/app')
from demo_webapp.stripe_client import StripeClient
from demo_webapp.seed import seed_demo_customer

async def main():
    client = StripeClient('http://localstripe:8420', 'sk_test_12345')
    cust = await client.find_customer_by_email('alice@example.com')
    if cust is None:
        cust = await client.create_customer('alice@example.com', 'Alice')
    await seed_demo_customer(client, cust['id'])
    await client.aclose()

asyncio.run(main())
"

# Re-warm gateway after MCP restart
SESSION=$(curl -s -D - -X POST http://localhost:18080/mcp \
  -H "Content-Type: application/json" \
  -d '{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"warmup","version":"1.0"}}}' \
  | grep -i "^Mcp-Session-Id:" | awk '{print $2}' | tr -d '\r\n')
curl -s -X POST http://localhost:18080/mcp \
  -H "Content-Type: application/json" \
  -H "Mcp-Session-Id: $SESSION" \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}' > /dev/null

# Stop Slack
docker compose stop mock-slack
```

Click **Approval Timeout → Run Scenario**. The case waits ~15 s for the approval TTL to expire.

**Expected result:** `list_recent_charges → allow`, `create_refund → approvalRequired → expired`.

---

## Scripted end-to-end run

To run all three scenarios headlessly in one shot:

```bash
make demo-resilience
```

This script manages the full Docker lifecycle, runs each scenario in sequence, and tears down the stack on exit.

---

## Gateway capability cache

The gateway caches the last successful `initialize` and `tools/list` responses from the upstream MCP server. When the upstream is unavailable, it serves tool metadata from this cache so agents can still discover tools — requests then fail with `upstream_error` at the call site rather than at tool-list time.

**Important:** the cache is populated the first time a successful `tools/list` reaches the gateway. Always warm it (see Scenario 1 setup above) before stopping the MCP server.

---

## Teardown

```bash
docker compose down -v   # stops all services and removes volumes
```
