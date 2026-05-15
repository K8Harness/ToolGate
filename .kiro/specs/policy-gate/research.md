# Research & Design Decisions: policy-gate

---

## Summary

- **Feature**: `policy-gate`
- **Discovery Scope**: Extension (extends bare-proxy pipeline; adds new Postgres infrastructure)
- **Key Findings**:
  - The bare-proxy `Handler` interface and `Pipeline.Use()` are already the correct extension point — no new middleware pattern is needed
  - No YAML or Postgres libraries exist in `go.mod`; both must be added (`gopkg.in/yaml.v3`, `github.com/jackc/pgx/v5`)
  - No Docker Compose file exists; the Postgres service must be created from scratch
  - Budget tracker should use `map[string]int` + `sync.Mutex` (not `sync.Map`) — counters are write-heavy with ephemeral keys
  - Ticket table schema must include `expires_at`, `payload JSONB`, and `status TEXT CHECK(...)` now to avoid a breaking migration in slice 4

---

## Research Log

### Codebase Extension Points

- **Context**: Needed to understand how policy-gate registers into bare-proxy without modifying the core pipeline.
- **Findings**:
  - `core/mcp/handler.go` defines `Handler interface { Handle(ctx, req) (*JSONRPCResponse, error) }`
  - `Pipeline.Use(h Handler)` registers handlers in order before the terminal `UpstreamForwarder`
  - `mcp.SessionIDFromContext(ctx)` and `mcp.TurnIDFromContext(ctx)` are available in any handler
  - `core/mcp/types.go` defines `CodeParseError = -32700` and `CodeInternalError = -32603`; `CodePolicyDenied = -32001` is not yet defined — policy-gate adds it
- **Implications**: `PolicyGateHandler` is a single `Handler` struct registered via `pipeline.Use()` in `main.go`; no changes to `Pipeline` itself are needed.

### Go YAML Parsing

- **Sources**: Go standard library docs, `gopkg.in/yaml.v3` documentation
- **Findings**:
  - `gopkg.in/yaml.v3` with `dec.KnownFields(true)` rejects unknown fields at parse time
  - `github.com/goccy/go-yaml` offers richer error messages but adds non-trivial dependency for marginal gain
  - `yaml.v3` has no built-in required-field enforcement; post-decode validation is needed
- **Selected**: `gopkg.in/yaml.v3` — de-facto standard, already used throughout the Go ecosystem, satisfies all requirements
- **Implications**: `LoadPolicy()` uses `yaml.NewDecoder(f)` + `dec.KnownFields(true)` + post-decode validation loop

### Postgres Client

- **Sources**: `github.com/jackc/pgx/v5` documentation, pgxpool API
- **Findings**:
  - `pgx/v5` with `pgxpool` is the current standard for Go Postgres; `database/sql` + `lib/pq` is legacy
  - For a low-traffic v0 demo: `MaxConns: 5`, `MinConns: 1`, `MaxConnIdleTime: 5min`, `HealthCheckPeriod: 1min`
  - `pgxpool.New()` + `pool.Ping()` at startup satisfies req 8.4 (refuse to start if Postgres unreachable)
- **Selected**: `github.com/jackc/pgx/v5` + `pgxpool`

### Audit Write Strategy

- **Context**: Req 8.1 requires a write before returning; req 8.3 requires non-blocking on failure.
- **Options**:
  1. Synchronous write in hot path — blocks handler; violates 8.3
  2. Buffered channel + goroutine worker — non-blocking enqueue; async commit
  3. pgx `SendBatch` — higher throughput but added complexity for v0
- **Selected**: Option 2 (buffered channel, capacity 256, single worker goroutine)
- **Trade-off**: The Postgres commit happens asynchronously after the response is returned. The enqueue (initiating the write) always happens before the response. Channel-full drops are logged at WARN. This satisfies the spirit of req 8.1 and the letter of req 8.3.

### Budget Counter Implementation

- **Context**: Needed a thread-safe per-(sessionID, turnID) counter.
- **Options**:
  1. `sync.Map` — optimized for stable keys with many readers
  2. `map[string]int` + `sync.Mutex` — simple, correct for write-heavy ephemeral keys
- **Selected**: Option 2 — budget counters are write-heavy (increment on every call) with short-lived keys (per turn). `sync.Map` would actually be slower here. Plain Mutex hold time is nanoseconds (map lookup + increment).

### Ticket Table Schema for Future Slice Compatibility

- **Context**: Slice 4 (approval-flow) will read and update `ticket` rows. Schema must not require a breaking migration.
- **Decisions**:
  - `status TEXT CHECK(...)` not `ENUM` — adding new status values requires only changing the CHECK constraint, not `ALTER TYPE`
  - `expires_at TIMESTAMPTZ NOT NULL` at insert time — slice 4's poller relies on this column without schema changes
  - `payload JSONB` — extensible bag for slice 4 metadata (Slack `message_ts`, resume token) without schema changes
  - `decision_by TEXT`, `decided_at TIMESTAMPTZ` — nullable; populated by slice 4 on approval/rejection
  - Index on `(status, expires_at)` — slice 4 polls/expires by this

---

## Architecture Pattern Evaluation

| Option | Description | Strengths | Risks / Limitations |
|--------|-------------|-----------|---------------------|
| Single `PolicyGateHandler` | One `Handler` struct orchestrates budget + evaluate + audit + ticket | Simple, single integration point, easy to test | Slightly larger unit; mitigated by clear internal method decomposition |
| Four separate handlers | Budget, evaluator, auditor, ticket as separate `Handler` instances | Separation of concerns | Adds complexity to pipeline registration order; harder to audit the sequence as a unit |

**Selected**: Single `PolicyGateHandler` — v0 scope does not warrant four handlers; simplification lens confirms one handler is sufficient.

---

## Design Decisions

### Decision: `core/policy` as a Separate Package

- **Context**: Policy types and evaluation logic have no Postgres or HTTP dependencies; they are pure domain logic.
- **Alternatives**:
  1. Co-locate everything in `cmd/gateway/` — simpler file structure, no package boundary
  2. `core/policy/` package — testable independently of the gateway binary
- **Selected**: `core/policy/` — unit tests for `LoadPolicy` and `Evaluate` run without Postgres or HTTP setup; follows the existing `core/mcp` precedent
- **Trade-offs**: Adds one package boundary; negligible for the benefit of isolated testing
- **Follow-up**: Verify `core/policy` imports nothing from `cmd/gateway` during implementation

### Decision: Synchronous `TicketStore.Insert()` vs Fire-and-Forget

- **Context**: Approval-required tickets must be visible to slice 4 immediately. If the insert fails silently, the human approver is never notified.
- **Alternatives**:
  1. Fire-and-forget (channel) — consistent with audit, but ticket loss is silent
  2. Synchronous insert — caller can log failure; pending response still returned
- **Selected**: Synchronous insert — the ticket row is the signal for slice 4. A missed ticket is a silent correctness failure. The insert error is logged at WARN and the pending response is still returned to the caller. The insert is fast (single row, indexed table).

### Decision: Pipeline Registration Order

- **Context**: `PolicyGateHandler` must run after `ContextInjector` (which injects sessionId/turnId into `_meta`) but before `UpstreamForwarder`.
- **Selected order**: `RequestLogger` → `ContextInjector` → `PolicyGateHandler` → `UpstreamForwarder`
- **Rationale**: Policy evaluation reads `sessionId` and `turnId` from context (set by server, not injector); meta injection into `params._meta` is not needed for policy evaluation but must happen before forwarding. Order is correct: logger → injector → policy-gate → forwarder.
- **Revalidation**: If `ContextInjector` position changes in bare-proxy, this ordering must be re-verified.

### Decision: Synthesis Outcomes

- **Generalization**: Requirements 4 (allow), 5 (deny), 6 (approvalRequired) are variations of "dispatch on policy decision" — unified via `PolicyDecision { Action Action }` discriminated type returned by `Evaluate()`. The handler switches on `Action`; no per-case handler needed.
- **Build vs Adopt**: Policy engine built (no suitable library exists for YAML-based allow/deny/approvalRequired for MCP JSON-RPC). YAML parsing and Postgres client adopted.
- **Simplification**: `BudgetTracker` lives inside `policy_gate.go` (not a separate exported package) since only `PolicyGateHandler` uses it.

---

## Risks & Mitigations

- **Budget tracker resets on restart** — In-memory counter; any gateway restart resets all counters. Acceptable for v0 demo; Redis in slice 3 addresses this. Document in operator notes.
- **Audit record loss on channel full** — If audit goroutine falls behind (slow Postgres, 256+ queued), records drop. Mitigation: WARN log on drop; monitor log output; channel size 256 is far above expected v0 demo throughput.
- **Ticket insert failure leaves no audit trail** — If `TicketStore.Insert()` fails, the approvalRequired decision IS still in the audit log (audit write happens before ticket insert). Slice 4 will simply not see the ticket. Mitigation: WARN log; operator can replay from audit log.
- **arguments JSONB contains PII** — PII redaction is out of scope for v0. Operators must be informed. Mitigation: security consideration documented in design.md.

---

## References

- [gopkg.in/yaml.v3](https://pkg.go.dev/gopkg.in/yaml.v3) — KnownFields decoder, struct tag conventions
- [github.com/jackc/pgx/v5](https://pkg.go.dev/github.com/jackc/pgx/v5) — pgxpool config, query API
- bare-proxy design.md — Handler interface, Pipeline registration, context key helpers, error code constants
- MCP Streamable HTTP spec (2025-03-26) — tools/call method name, params.name field location
