# Research & Design Decisions

---
**Feature**: `eval-gate`  
**Discovery Scope**: New Feature (greenfield — no prior eval-runner code exists)  
**Key Findings**:
- `github.com/modelcontextprotocol/go-sdk v1.6.0` is already in go.mod; fake MCP servers can use it directly
- The gateway's `SlackNotifier` hardcodes `https://slack.com/api`; one config field (`SLACK_API_BASE_URL`) enables the mock Slack service without touching notification logic
- `audit_log` stores `arguments` as JSONB with `decided_at` ordering — sufficient for trace capture without OTel

---

## Research Log

### MCP Protocol Version

- **Context**: Fake MCP servers must speak the "real" MCP protocol per Req 8.1. Need to determine current spec.
- **Sources**: modelcontextprotocol.io/specification/2025-06-18
- **Findings**:
  - Current spec (2025-06-18) uses **Streamable HTTP**: single `/mcp` endpoint supporting POST (client→server) and optional GET (server push)
  - The old 2024-11-05 spec used a separate `GET /sse` + `POST /messages` pair; newer clients use Streamable HTTP
  - Required init sequence: `initialize` JSON-RPC call → `initialized` notification; server may return `Mcp-Session-Id` header
  - The existing gateway already uses `POST /mcp` (Streamable HTTP); fake servers must match
- **Implications**: Use `go-sdk v1.6.0` server helper which handles Streamable HTTP transport; no custom SSE plumbing needed

### Existing Codebase Patterns

- **Context**: What patterns in `cmd/gateway/` should `cmd/eval-runner/` follow?
- **Sources**: Codebase exploration of all 18 production .go files
- **Findings**:
  - Config pattern: struct + env var loading with startup validation (return error if required var absent)
  - Handler pattern: `Handle(ctx, *JSONRPCRequest) (*JSONRPCResponse, error)` — return `(nil, nil)` to continue pipeline
  - `audit_log` schema: `session_id TEXT`, `turn_id TEXT`, `tool_name TEXT`, `arguments JSONB`, `decision TEXT`, `decided_at TIMESTAMPTZ`
  - pgx/v5 already in use; `pgxpool.Pool` is the connection management pattern
  - Slack notifier sends to `https://slack.com/api/chat.postMessage` — URL is currently hardcoded
  - No Makefile exists; `docker-compose.yml` at root covers gateway + fake-upstream + postgres + redis
- **Implications**: Eval-runner config follows same env-var pattern; CaseRunner uses pgxpool; mock Slack requires `SLACK_API_BASE_URL` extension to config.go

### Docker Compose Integration Strategy

- **Context**: Eval-runner must orchestrate the full stack (Req 2.1-2.5) without requiring non-Docker/Go tools.
- **Sources**: Docker Compose CLI docs; testcontainers-go modules
- **Findings**:
  - `docker compose up --wait` exits non-zero if any service fails its healthcheck (requires Compose v2.1+)
  - `testcontainers-go/modules/compose` wraps `docker/compose/v2` but adds ~20 transitive dependencies
  - `os/exec` wrapping `docker compose` CLI adds zero new dependencies and works identically
- **Implications**: Use `os/exec` approach for the eval-runner binary; simpler, no dep bloat

### YAML Library

- **Context**: EvalSuite loader needs YAML parsing.
- **Sources**: gopkg.in/yaml.v3 docs; goccy/go-yaml comparison
- **Findings**:
  - `gopkg.in/yaml.v3 v3.0.1` is already in go.mod (used by policy loading)
  - Default decoder silently ignores unknown fields — satisfies Req 1.4 (forward-compatibility) without any extra flag
  - `goccy/go-yaml` has better error messages but no advantage for simple struct unmarshalling
- **Implications**: Use existing `yaml.v3`; no new dependency needed

### Python Demo Agent Approach

- **Context**: Demo agent must call gateway MCP tools and expose an HTTP trigger endpoint for the eval-runner.
- **Sources**: Python `mcp` SDK GitHub; FastMCP docs
- **Findings**:
  - `mcp` Python SDK v1.x: `FastMCP` for servers, `ClientSession` with `streamablehttp_client` for clients
  - Streamable HTTP client requires setting `Mcp-Session-Id` header per session; SDK supports custom headers
  - LangGraph/CrewAI would add significant complexity for no benefit in v0 — the demo agent needs deterministic tool calls, not reasoning
- **Implications**: Use plain Python `mcp` client + Flask HTTP server; no LLM or agent framework needed for v0

---

## Architecture Pattern Evaluation

| Option | Description | Strengths | Risks / Limitations | Decision |
|--------|-------------|-----------|---------------------|----------|
| Sequential CLI | Load → compose up → for each case (trigger→trace→evaluate) → compose down → report | Simple, debuggable, no concurrency bugs | Slower than parallel case execution | **Selected** — acceptable for v0 case count |
| Parallel cases | Run all cases concurrently | Faster | Race on session_id allocation, harder to debug | Deferred to v2 |
| testcontainers-go | Programmatic compose control | Type-safe API | 20+ new transitive deps | Rejected — adds dep bloat for no functional gain |
| Docker SDK direct | `docker/compose/v2` library | No subprocess | Not independently importable without significant setup | Rejected |

---

## Design Decisions

### Decision: `mustNotContainInArgs` EvalCase field for PII verification

- **Context**: Req 7.4 requires verifying that PII is not in the arguments forwarded to the upstream. Req 1.2 defines `mustInclude`, `mustNotInclude`, `policyOutcome` — no argument-level assertion field is specified. Req 1.4 requires forward-compatible YAML format.
- **Alternatives Considered**:
  1. Fake Slack `/inspect` endpoint — eval-runner calls `GET /inspect` after each case to check received arguments
  2. `mustNotContainInArgs` substring field in EvalCase — eval-runner checks against audit_log `arguments` JSONB
- **Selected Approach**: `mustNotContainInArgs []string` in `EvalCase`; Evaluator checks each string is absent from any row's `arguments` JSON in the trace
- **Rationale**: Keeps all assertions in the EvalSuite YAML; avoids eval-runner coupling to fake server inspection endpoints; audit_log already captures post-redaction arguments (gateway writes redacted args)
- **Trade-offs**: Requires gateway's `redact` action to write post-redaction arguments to audit_log (not originals); ensures the audit trail reflects what was actually forwarded
- **Follow-up**: Verify that `policy_gate.go` redact path calls `auditWriter.Write()` with the masked arguments, not the originals

### Decision: Gateway `redact` action owned by eval-gate spec

- **Context**: The roadmap demo scenario "Slack message with PII redacted" requires a gateway capability not present in the existing policy-gate implementation. The policy-gate spec is marked `implemented`.
- **Alternatives Considered**:
  1. Leave PII redaction out and simplify the demo scenario to 3 cases
  2. Extend policy-gate spec retroactively (risks re-opening an approved spec)
  3. Add `redact` action as part of eval-gate scope (additive extension to policy_gate.go)
- **Selected Approach**: eval-gate owns the `redact` action extension — a new `case "redact":` in the existing switch in `policy_gate.go`. The change is strictly additive.
- **Rationale**: eval-gate is the "make demo" spec; it is responsible for all 4 demo scenarios working end-to-end. Additive switch-case additions do not restructure or break existing behavior.
- **Trade-offs**: eval-gate modifies a file owned by policy-gate; must coordinate during code review. The change is small and isolated.
- **Follow-up**: Verify `redactFields` is optional so existing policy.yaml files without it continue to load without error

### Decision: Mock Slack service with SLACK_API_BASE_URL gateway config extension

- **Context**: Req 10.3 requires the approval scenario to work without a real Slack account. The gateway's `SlackNotifier` currently hardcodes `https://slack.com/api`.
- **Alternatives Considered**:
  1. Replace Slack notifier with webhook-based notifier (break existing behavior)
  2. Add `SLACK_API_BASE_URL` config field (default: `https://slack.com/api`); mock service overrides it
  3. Mock the Slack endpoint at the host-DNS level (fragile, platform-specific)
- **Selected Approach**: Option 2 — add optional `SLACK_API_BASE_URL` to `cmd/gateway/config.go`. `deploy/docker-compose.yml` sets it to `http://mock-slack:8090/api`.
- **Rationale**: Minimal gateway change (one config field, one string interpolation in SlackNotifier); makes the mock completely transparent
- **Trade-offs**: Adds a second gateway modification for eval-gate (alongside `redact`); both are additive and small
- **Follow-up**: Verify the mock Slack service correctly extracts ticket_id from Block Kit button values

### Decision: Demo agent uses keyword dispatch, not LLM

- **Context**: Req 9.2 says the agent "uses its registered MCP tools to complete the support-refund scenario." The brief mentions LangGraph/CrewAI as "whichever is simplest to wire."
- **Alternatives Considered**:
  1. LangGraph/CrewAI agent with LLM reasoning — general but requires API key, non-deterministic
  2. Simple keyword dispatch table — deterministic, no API key, always produces the same tool call
- **Selected Approach**: Keyword dispatch: `input` string → fixed tool + fixed arguments. The demo proves gateway behavior, not agent intelligence.
- **Rationale**: Deterministic behavior is essential for a deployment gate. Non-deterministic LLM dispatch would make eval cases flaky.
- **Trade-offs**: Agent is not a "real" AI agent; acceptable for v0 demo purposes
- **Follow-up**: Document that the demo agent is a v0 stub; v2 will wire a real LLM

---

## Risks & Mitigations

- `docker compose up --wait` requires Compose v2.1+ — document minimum version in README; orchestrator prints a clear error if `--wait` is unrecognized
- Python MCP SDK `streamablehttp_client` header support — verify `Mcp-Session-Id` can be set as a custom header in the SDK's client session; fallback: set it as a URL query parameter if the SDK doesn't support header injection
- Mock Slack HMAC signing must exactly match the gateway's signature verification — use shared test vector to validate both sides during implementation
- `redact` action in-place argument mutation — must deep-copy before masking to avoid data races if the request struct is shared; UpstreamForwarder reads from `req` after PolicyGateHandler returns

---

## References

- [MCP Specification 2025-06-18 — Transports](https://modelcontextprotocol.io/specification/2025-06-18/basic/transports)
- [MCP Specification 2025-06-18 — Tools](https://modelcontextprotocol.io/specification/2025-06-18/server/tools)
- [modelcontextprotocol/go-sdk](https://github.com/modelcontextprotocol/go-sdk)
- [modelcontextprotocol/python-sdk](https://github.com/modelcontextprotocol/python-sdk)
- [docker/compose v2 — up --wait](https://docs.docker.com/compose/reference/up/)
- [gopkg.in/yaml.v3](https://pkg.go.dev/gopkg.in/yaml.v3)
