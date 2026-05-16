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
elif mode == "approval_approved":
    assert body["result"]["ok"] is True
    assert body["result"]["tool"] == "refund_large"
elif mode == "approval_denied":
    assert body["error"]["code"] == -32001
    assert body["error"]["message"] == "approval denied"
else:
    raise SystemExit(f"unknown mode: {mode}")
PY
}

query_psql() {
  docker compose exec -T postgres psql -U gateway -d gateway -Atc "$1"
}

sign_slack_request() {
  python - "$1" "$2" <<'PY'
import hashlib
import hmac
import sys
import time

signing_secret_bytes = sys.argv[1].encode("utf-8")
body = sys.argv[2].encode("utf-8")
timestamp = str(int(time.time()))
base = b"v0:" + timestamp.encode("utf-8") + b":" + body
sig = "v0=" + hmac.new(signing_secret_bytes, base, hashlib.sha256).hexdigest()
print(timestamp)
print(sig)
PY
}

build_slack_body() {
  python - "$1" "$2" "$3" <<'PY'
import json
import sys
import urllib.parse

action_id, ticket_id, user_id = sys.argv[1:4]
payload = {
    "type": "block_actions",
    "user": {"id": user_id},
    "actions": [{"action_id": action_id, "value": ticket_id}],
}
print("payload=" + urllib.parse.quote(json.dumps(payload, separators=(",", ":"))))
PY
}

wait_for_ticket_id() {
  local turn_id="$1"
  for _ in $(seq 1 50); do
    local ticket_id
    ticket_id="$(query_psql "SELECT id FROM ticket WHERE turn_id = '${turn_id}' ORDER BY created_at DESC LIMIT 1;")"
    if [[ -n "$ticket_id" ]]; then
      printf '%s\n' "$ticket_id"
      return 0
    fi
    sleep 0.1
  done
  return 1
}

post_slack_action() {
  local body="$1"
  local with_signature="$2"
  local status_file="$3"

  local curl_args=(
    -sS
    -o /dev/null
    -w "%{http_code}"
    -H "Content-Type: application/x-www-form-urlencoded"
    --data "$body"
    "http://127.0.0.1:18080/slack/actions"
  )

  if [[ "$with_signature" == "signed" ]]; then
    local sig_parts
    local timestamp
    local signature
    sig_parts="$(sign_slack_request "demo-signing-secret" "$body")"
    timestamp="$(printf '%s\n' "$sig_parts" | sed -n '1p')"
    signature="$(printf '%s\n' "$sig_parts" | sed -n '2p')"
    curl_args+=(
      -H "X-Slack-Request-Timestamp: ${timestamp}"
      -H "X-Slack-Signature: ${signature}"
    )
  fi

  curl "${curl_args[@]}" >"$status_file"
}

echo "Resetting compose stack..."
docker compose down -v --remove-orphans >/dev/null 2>&1 || true

echo "Building Linux gateway binary for compose..."
mkdir -p .compose-bin
GOOS=linux GOARCH="$(go env GOARCH)" CGO_ENABLED=0 go build -o .compose-bin/gateway ./cmd/gateway
chmod +x .compose-bin/gateway

echo "Starting fake upstream, postgres, redis, and gateway..."
docker compose up -d fake-upstream postgres redis gateway >/dev/null

headers_file="$(mktemp)"
body_file="$(mktemp)"
approval_headers_file="$(mktemp)"
approval_body_file="$(mktemp)"
deny_headers_file="$(mktemp)"
deny_body_file="$(mktemp)"
slack_status_file="$(mktemp)"
trap 'rm -f "$headers_file" "$body_file" "$approval_headers_file" "$approval_body_file" "$deny_headers_file" "$deny_body_file" "$slack_status_file"' EXIT

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

echo "Running refund_large approval-approve scenario..."
(
  request "$session_id" "turn-approval-approve" '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"refund_large","arguments":{"amount":9001}}}' "$approval_headers_file" "$approval_body_file"
) &
approval_pid=$!

approval_ticket_id="$(wait_for_ticket_id "turn-approval-approve")" || fail "approval ticket not created"
kill -0 "$approval_pid" >/dev/null 2>&1 || fail "approval request finished before human decision"
[[ "$(query_psql "SELECT status FROM ticket WHERE id = '${approval_ticket_id}';")" == "pending" ]] || fail "approval ticket was not pending while request was held"

approval_slack_body="$(build_slack_body "approval_approve" "$approval_ticket_id" "UAPPROVER")"
post_slack_action "$approval_slack_body" signed "$slack_status_file"
[[ "$(cat "$slack_status_file")" == "200" ]] || fail "signed approve webhook did not return HTTP 200"

wait "$approval_pid"
assert_json "$approval_body_file" approval_approved || fail "refund_large approve response was not forwarded upstream"
[[ "$(query_psql "SELECT status FROM ticket WHERE id = '${approval_ticket_id}';")" == "approved" ]] || fail "approval ticket did not transition to approved"
echo "PASS refund_large approve -> request held, webhook approved, upstream result returned"

echo "Running refund_large approval-deny scenario..."
(
  request "$session_id" "turn-approval-deny" '{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"refund_large","arguments":{"amount":1337}}}' "$deny_headers_file" "$deny_body_file"
) &
deny_pid=$!

deny_ticket_id="$(wait_for_ticket_id "turn-approval-deny")" || fail "deny ticket not created"
kill -0 "$deny_pid" >/dev/null 2>&1 || fail "deny request finished before human decision"
[[ "$(query_psql "SELECT status FROM ticket WHERE id = '${deny_ticket_id}';")" == "pending" ]] || fail "deny ticket was not pending while request was held"

deny_slack_body="$(build_slack_body "approval_deny" "$deny_ticket_id" "UDENIER")"
post_slack_action "$deny_slack_body" signed "$slack_status_file"
[[ "$(cat "$slack_status_file")" == "200" ]] || fail "signed deny webhook did not return HTTP 200"

wait "$deny_pid"
assert_json "$deny_body_file" approval_denied || fail "refund_large deny response was not approval denied"
[[ "$(query_psql "SELECT status FROM ticket WHERE id = '${deny_ticket_id}';")" == "denied" ]] || fail "deny ticket did not transition to denied"
echo "PASS refund_large deny -> request held, webhook denied, approval error returned"

echo "Running unsigned Slack webhook scenario..."
unsigned_body="$(build_slack_body "approval_approve" "$deny_ticket_id" "UNSIGNED")"
post_slack_action "$unsigned_body" unsigned "$slack_status_file"
[[ "$(cat "$slack_status_file")" == "400" ]] || fail "unsigned webhook did not return HTTP 400"
echo "PASS unsigned webhook rejected with HTTP 400"

echo
echo "Demo complete. Inspect rows with:"
echo "  docker compose exec -T postgres psql -U gateway -d gateway -c 'TABLE audit_log;'"
echo "  docker compose exec -T postgres psql -U gateway -d gateway -c 'TABLE ticket;'"
