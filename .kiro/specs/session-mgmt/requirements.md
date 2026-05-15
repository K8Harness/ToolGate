# Requirements Document

## Introduction

The session-mgmt feature adds a concurrency control layer to the MCP gateway. Without it, an agent that issues multiple tool calls in parallel within the same session can produce context races (two writes to the same customer record), budget drift (two calls both pass the same budget check), and audit log inconsistencies. This feature enforces three scheduling rules: different sessions run fully in parallel; within a session, only one turn is active at a time; within a turn, read-class calls run concurrently while write-class calls are serialized and wait for all reads to finish.

## Boundary Context

- **In scope**: Per-session turn serialization, per-turn read/write concurrency control, read/write operation classification (configurable map + default heuristic), configurable lock-acquisition timeout and session-lock TTL, lock release on turn completion or error, TTL-based crash protection, budget counter accuracy under concurrency control
- **Out of scope**: Durable turn history in Postgres (turns are ephemeral in v0), per-session budget rollups across turns, queue-based worker pool scheduling, cross-session rate limiting
- **Adjacent expectations**: The approval-flow feature (slice 4) will need the session lock to remain held (or coordinately extended) during an approval wait. This spec does not implement that coordination but must not prevent it — the session-lock TTL and release mechanism must be usable by downstream features.

## Requirements

### Requirement 1: Session Isolation

**Objective:** As a gateway operator, I want tool calls from different sessions to be processed without cross-session blocking, so that session-level concurrency control does not reduce overall gateway throughput.

#### Acceptance Criteria

1. The Gateway shall process tool calls from different sessions concurrently without any cross-session synchronization or waiting.

---

### Requirement 2: Same-Session Turn Serialization

**Objective:** As a gateway operator, I want only one turn to be active per session at a time, so that within-session ordering prevents context races and budget drift.

#### Acceptance Criteria

1. When a turn request arrives for a session that already has an active turn, the Gateway shall hold the new turn's request until the active turn completes or the lock-acquisition timeout elapses.
2. When the lock-acquisition timeout elapses before a session lock is acquired, the Gateway shall return a JSON-RPC error to the caller without forwarding the request upstream.
3. When a turn completes — whether successfully, with a policy denial, or with an upstream error — the Gateway shall release the session lock for that turn.
4. If the gateway process restarts, the Gateway shall not preserve in-flight session locks from the previous process; subsequent turn requests for any session may acquire the lock immediately.
5. The Gateway shall enforce a configurable session-lock time-to-live so that a crashed or stalled turn does not permanently block the session.
6. The Gateway shall accept a configurable lock-acquisition timeout with a documented default value.

---

### Requirement 3: Within-Turn Read/Write Concurrency

**Objective:** As a gateway operator, I want read-class tool calls within a turn to proceed in parallel while write-class calls are serialized and wait for all reads to complete, so that throughput is maximized without sacrificing write safety.

#### Acceptance Criteria

1. While a turn is active, when multiple read-class tool calls arrive concurrently, the Gateway shall allow them to proceed in parallel.
2. While a turn is active, when a write-class tool call arrives, the Gateway shall wait for all in-flight read-class calls in that turn to complete before forwarding the write-class call.
3. While a turn is active, when multiple write-class tool calls arrive concurrently, the Gateway shall serialize them so that no two write-class calls proceed at the same time.
4. When a tool call within a turn completes — regardless of outcome — the Gateway shall remove it from the per-turn concurrency registry.

---

### Requirement 4: Read/Write Operation Classification

**Objective:** As a gateway operator, I want control over how tool calls are classified as read or write, so that the concurrency policy accurately reflects the real semantics of each operation.

#### Acceptance Criteria

1. The Gateway shall classify a tool call as read-class or write-class according to a configurable operation classification map provided in the agent policy configuration.
2. Where no explicit classification entry exists for an operation, the Gateway shall apply a default heuristic: operations whose name begins with `read_`, `get_`, or `list_` are classified as read-class; all other operations are classified as write-class.

---

### Requirement 5: Budget Counter Accuracy

**Objective:** As a gateway operator, I want the per-turn tool call budget counter to be accurate even when multiple calls arrive in parallel, so that agents cannot exceed their configured call limit through concurrent calls.

#### Acceptance Criteria

1. While a turn is active, the Gateway shall increment the tool-call budget counter for that turn only after the call has acquired its concurrency slot (read or write), ensuring that concurrent calls observe an accurate and consistent budget count.
