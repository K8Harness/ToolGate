# Architectural Decisions & Trade-offs

This document tracks significant design decisions and trade-offs made during the development of ToolGate, particularly regarding the `policy-gate` (Slice 2) and subsequent slices.

## Policy-Gate (Slice 2)

### 1. Audit Logging: Async vs. Sync
- **Decision:** Asynchronous logging via an in-memory Go channel.
- **Trade-off:** Performance over absolute non-repudiation.
- **Rationale:** To meet the <10ms overhead target, we avoid waiting for a synchronous Postgres write in the critical path.
- **Risk:** "Ghost tool calls." If the gateway process crashes after forwarding a tool call but before flushing the log buffer to Postgres, no audit record will exist for that action. This was accepted for v0; future versions may introduce a local write-ahead log (WAL) for higher durability.

### 2. Budget Tracking: Local vs. Distributed
- **Decision:** In-memory (RAM) counters per server.
- **Trade-off:** Simplicity over strict budget enforcement in distributed environments.
- **Rationale:** Avoids the complexity of a shared state store (like Redis) in Slice 2.
- **Risk:** "Budget leakage." If the gateway is load-balanced across N servers, an agent can effectively make `N * budget` calls before being blocked. This is a known limitation to be addressed in Slice 3 (Session Management) using Redis.

### 3. Approval Flow: Blocking vs. Non-blocking
- **Decision:** Recommendation for Blocking turns on pending approvals.
- **Trade-off:** Consistency over agent autonomy.
- **Rationale:** Non-blocking agents that "move on" while a high-stakes tool call is pending create a "revert nightmare" if the human eventually rejects the action. By blocking, we ensure the agent's internal state remains consistent with the human's decisions.

### 4. Fail-Open vs. Fail-Closed on Logging Failure
- **Decision:** Fail-Open (per Requirement 8.3).
- **Trade-off:** Availability over Audit Integrity.
- **Rationale:** If the audit database is down, the system continues to serve tool calls to avoid a total system outage, while logging the failure locally.
- **Risk:** Tool calls can be executed without an audit trail during database outages.
