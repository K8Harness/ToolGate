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

assert_refund_small_response() {
  python - "$1" <<'PY'
import json
import sys

body = json.load(open(sys.argv[1], encoding="utf-8"))
assert body["result"]["ok"] is True
assert body["result"]["tool"] == "refund_small"
PY
}

redis_session_keys() {
  docker compose exec -T redis redis-cli --raw keys "session:*" | sed '/^$/d'
}

assert_only_session_keys_for_session() {
  local session_id="$1"
  local excluded_session_id="${2:-}"
  local keys

  for _ in $(seq 1 20); do
    keys="$(redis_session_keys)"
    if [[ -n "$excluded_session_id" ]] && printf '%s\n' "$keys" | rg -q "session:${excluded_session_id}:"; then
      sleep 0.05
      continue
    fi
    if printf '%s\n' "$keys" | rg -q "session:${session_id}:"; then
      if [[ -z "$excluded_session_id" ]] || ! printf '%s\n' "$keys" | rg -q "session:${excluded_session_id}:"; then
        return 0
      fi
    fi
    sleep 0.05
  done

  fail "expected only session keys for ${session_id} while request was in flight"
}

assert_lock_observed_for_session() {
  local session_id="$1"
  for _ in $(seq 1 40); do
    if redis_session_keys | rg -q "session:${session_id}:"; then
      return 0
    fi
    sleep 0.1
  done
  fail "did not observe Redis session lock for session ${session_id} during request"
}

assert_no_session_keys() {
  if [[ -n "$(redis_session_keys)" ]]; then
    fail "expected Redis session keys to be empty after request completed"
  fi
}

wait_for_gateway() {
  local headers_file="$1"
  local body_file="$2"
  for _ in $(seq 1 60); do
    : >"$headers_file"
    : >"$body_file"
    if request "" "" '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"client":"demo"}}' "$headers_file" "$body_file"; then
      if extract_session_id "$headers_file" >/dev/null 2>&1; then
        return 0
      fi
    fi
    sleep 1
  done
  fail "gateway did not become ready"
}

new_session_id() {
  local headers_file="$1"
  local body_file="$2"
  : >"$headers_file"
  : >"$body_file"
  request "" "" '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"client":"demo"}}' "$headers_file" "$body_file"
  extract_session_id "$headers_file"
}

echo "Resetting compose stack..."
docker compose down -v --remove-orphans >/dev/null 2>&1 || true

echo "Starting full compose stack..."
docker compose up -d postgres redis fake-upstream gateway >/dev/null

headers_file="$(mktemp)"
body_file="$(mktemp)"
request_headers_file="$(mktemp)"
request_body_file="$(mktemp)"
restart_headers_file="$(mktemp)"
restart_body_file="$(mktemp)"
trap 'rm -f "$headers_file" "$body_file" "$request_headers_file" "$request_body_file" "$restart_headers_file" "$restart_body_file"' EXIT

wait_for_gateway "$headers_file" "$body_file"
session_id="$(new_session_id "$headers_file" "$body_file")"

echo "Running held tools/call request and checking Redis lock visibility..."
(
  request \
    "$session_id" \
    "turn-lock-visible" \
    '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"refund_small","arguments":{"amount":10,"hold_ms":1500}}}' \
    "$request_headers_file" \
    "$request_body_file"
) &
request_pid=$!

assert_lock_observed_for_session "$session_id"
wait "$request_pid"
assert_refund_small_response "$request_body_file"
assert_no_session_keys

echo "Running restart recovery scenario..."
(
  request \
    "$session_id" \
    "turn-crash" \
    '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"refund_small","arguments":{"amount":20,"hold_ms":4000}}}' \
    "$restart_headers_file" \
    "$restart_body_file"
) &
crash_request_pid=$!

assert_lock_observed_for_session "$session_id"
docker compose kill -s SIGKILL gateway >/dev/null
wait "$crash_request_pid" || true

docker compose up -d gateway >/dev/null
wait_for_gateway "$headers_file" "$body_file"

restarted_session_id="$(new_session_id "$headers_file" "$body_file")"
(
  request \
  "$restarted_session_id" \
  "turn-after-restart" \
  '{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"refund_small","arguments":{"amount":30,"hold_ms":1500}}}' \
  "$request_headers_file" \
  "$request_body_file"
) &
restart_request_pid=$!

assert_lock_observed_for_session "$restarted_session_id"
assert_only_session_keys_for_session "$restarted_session_id" "$session_id"
wait "$restart_request_pid"

assert_refund_small_response "$request_body_file"
assert_no_session_keys

echo "PASS session lock observed during request, cleared after completion, and restart did not leave stuck Redis keys"
