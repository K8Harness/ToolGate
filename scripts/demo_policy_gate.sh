#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

fail() {
  echo "FAIL: $1" >&2
  exit 1
}

request() {
  local session_id="$1"
  local turn_id="$2"
  local body="$3"
  local headers_file="$4"
  local body_file="$5"

  local curl_args=(
    -sS
    -D "$headers_file"
    -o "$body_file"
    -H "Content-Type: application/json"
  )

  if [[ -n "$session_id" ]]; then
    curl_args+=(-H "Mcp-Session-Id: $session_id")
  fi
  if [[ -n "$turn_id" ]]; then
    curl_args+=(-H "X-Mcp-Turn-Id: $turn_id")
  fi

  curl_args+=(-d "$body" "http://127.0.0.1:18080/mcp")
  curl "${curl_args[@]}" >/dev/null
}

extract_session_id() {
  python - "$1" <<'PY'
import sys

for line in open(sys.argv[1], encoding="utf-8"):
    if line.lower().startswith("mcp-session-id:"):
        print(line.split(":", 1)[1].strip())
        raise SystemExit(0)
raise SystemExit(1)
PY
}

assert_json() {
  python - "$1" "$2" <<'PY'
import json
import sys

body = json.load(open(sys.argv[1], encoding="utf-8"))
mode = sys.argv[2]

if mode == "allow":
    assert body["result"]["ok"] is True
    assert body["result"]["tool"] == "refund_small"
elif mode == "deny":
    assert body["error"]["code"] == -32001
    assert body["error"]["message"] == "denied by policy"
elif mode == "approval":
    assert body["result"]["status"] == "pending"
    assert body["result"]["message"] == "tool call requires human approval"
else:
    raise SystemExit(f"unknown mode: {mode}")
PY
}

query_psql() {
  docker compose exec -T postgres psql -U gateway -d gateway -Atc "$1"
}

echo "Resetting compose stack..."
docker compose down -v --remove-orphans >/dev/null 2>&1 || true

echo "Starting fake upstream, postgres, and gateway..."
docker compose up -d fake-upstream postgres gateway >/dev/null

headers_file="$(mktemp)"
body_file="$(mktemp)"
trap 'rm -f "$headers_file" "$body_file"' EXIT

echo "Waiting for gateway initialize..."
session_id=""
for _ in $(seq 1 60); do
  : >"$headers_file"
  : >"$body_file"
  if request "" "" '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"client":"demo"}}' "$headers_file" "$body_file"; then
    if session_id="$(extract_session_id "$headers_file" 2>/dev/null)"; then
      break
    fi
  fi
  sleep 1
done

[[ -n "$session_id" ]] || fail "gateway did not return a session id"

echo "Running refund_small allow scenario..."
request "$session_id" "turn-allow" '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"refund_small","arguments":{"amount":10}}}' "$headers_file" "$body_file"
assert_json "$body_file" allow || fail "refund_small response was not allowed"
[[ "$(query_psql "SELECT count(*) FROM audit_log WHERE tool_name = 'refund_small' AND decision = 'allow';")" == "1" ]] || fail "refund_small audit row missing"
echo "PASS refund_small -> allowed and audited"

echo "Running delete_record deny scenario..."
request "$session_id" "turn-deny" '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"delete_record","arguments":{"id":"abc"}}}' "$headers_file" "$body_file"
assert_json "$body_file" deny || fail "delete_record response was not denied"
[[ "$(query_psql "SELECT count(*) FROM ticket WHERE tool_name = 'delete_record';")" == "0" ]] || fail "delete_record unexpectedly created a ticket"
[[ "$(query_psql "SELECT count(*) FROM audit_log WHERE tool_name = 'delete_record' AND decision = 'deny';")" == "1" ]] || fail "delete_record deny audit row missing"
echo "PASS delete_record -> denied without ticket"

echo "Running refund_large approval scenario..."
request "$session_id" "turn-approval" '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"refund_large","arguments":{"amount":9001}}}' "$headers_file" "$body_file"
assert_json "$body_file" approval || fail "refund_large response was not pending"
[[ "$(query_psql "SELECT count(*) FROM ticket WHERE tool_name = 'refund_large' AND status = 'pending';")" == "1" ]] || fail "refund_large pending ticket missing"
[[ "$(query_psql "SELECT count(*) FROM audit_log WHERE tool_name = 'refund_large' AND decision = 'approvalRequired';")" == "1" ]] || fail "refund_large approval audit row missing"
echo "PASS refund_large -> pending with ticket"

echo
echo "Demo complete. Inspect rows with:"
echo "  docker compose exec -T postgres psql -U gateway -d gateway -c 'TABLE audit_log;'"
echo "  docker compose exec -T postgres psql -U gateway -d gateway -c 'TABLE ticket;'"
