# Brief: session-mgmt

## Problem

When an agent issues multiple tool calls in parallel within the same session, the gateway can process them concurrently — producing context races (two writes to the same customer record), budget-count drift (two calls both pass a "19 calls so far" check), and audit log inconsistencies. The policy engine in slice 2 is stateless per-call; it has no awareness of other in-flight calls in the same session or turn.

## Current State

`policy-gate` (slice 2) evaluates each call independently. The Postgres `audit_log` and `ticket` tables exist. Redis is not yet wired.

## Desired Outcome

The gateway enforces the scheduling rules from the design:

- **Different sessions** may run fully in parallel — no cross-session blocking
- **Same-session turns** are serialized — only one turn active per session at a time (Redis session mutex, 60s TTL)
- **Within a turn**, read-class tool calls may run in parallel; write-class tool calls are serialized and wait for all reads to finish first (Redis RWLock registry per turn)

Budget tracking (maxToolCallsPerTurn) from slice 2 is now safe to enforce accurately because calls within a turn are properly sequenced.

## Approach

Redis as the coordination layer:
- **Session mutex**: `SET session:<id>:lock <turn_id> EX 60 NX` — acquired before a turn starts, released on turn completion or timeout
- **RWLock registry**: `HSET turn:<id>:rwlock <task_id> read|write` — tracks in-flight tasks per turn; write tasks block until all reads complete

Read vs. write classification: determined from the MCP operation name against a configurable classification map in the `AgentPolicy` (or a default heuristic: operations prefixed with `read_` / `get_` / `list_` are read-class; everything else is write-class).

## Scope

- **In**: Redis connection setup, session mutex (SET NX + TTL), turn queue (wait for mutex, with configurable timeout), RWLock registry (per-turn read/write tracking), read/write operation classification, mutex release on turn completion or error, budget counter accuracy fix (counters incremented under lock)
- **Out**: Slack, approval flow, eval runner, persistent turn history in Postgres (that's a v1+ concern — turns are ephemeral in v0)

## Boundary Candidates

- Lock acquisition/release lifecycle (pairs with policy evaluation middleware from slice 2)
- Read/write classifier (pure function `(operation string) → class`, independently testable)
- Timeout and retry policy for lock acquisition (configurable; default: fail after N seconds with a JSON-RPC error)

## Out of Boundary

- Durable turn storage in Postgres — v0 uses Redis as ephemeral state only; turns are not persisted across gateway restarts
- Per-session budget rollups across turns — v1+
- Queue-based worker pool scheduling — that is the v1 Kubernetes worker pool design

## Upstream / Downstream

- **Upstream**: Redis, policy-gate middleware pipeline (slice 2)
- **Downstream**: approval-flow (slice 4) needs the session mutex to be held during an approval wait — it will extend the TTL or use a separate approval-wait lock

## Existing Spec Touchpoints

- **Extends**: policy-gate — wraps the per-call evaluation with pre/post locking middleware
- **Adjacent**: approval-flow (slice 4) must coordinate with the session mutex during hold

## Constraints

- Redis is ephemeral in v0 — a gateway restart drops all locks. This is acceptable for the demo; document it explicitly
- Session mutex TTL (60s) must be longer than the maximum expected turn duration; configurable
- Must not introduce deadlock: the lock acquisition order must be consistent (session mutex first, then RWLock)
