# Brief: eval-gate

## Problem

Even with a working gateway, there is no automated way to verify that a new agent version behaves correctly before it would be promoted to production. Regressions (wrong tool call order, new policy violations, unexpected denials) are caught only by running the agent in production. The eval runner is the deployment gate that prevents this.

## Current State

`approval-flow` (slice 4) completes the full runtime gateway: all four gateway behaviors (allow, deny, approval, PII redact) work end-to-end. Nothing orchestrates a containerized agent test run, loads EvalSuite YAML cases, or produces a pass/fail verdict.

## Desired Outcome

Running `make demo` (or `go run ./cmd/eval-runner`) against the Docker Compose stack:

1. Spins up a fresh containerized Python demo agent + the Go gateway + Postgres + Redis + fake MCP servers
2. Loads `EvalSuite` YAML test cases (input → expected tool sequence + policy outcome)
3. For each case: feeds the input to the agent, captures the gateway's trace (which tools were called, in what order, with what policy outcomes)
4. Compares the observed trace against the expectations in the YAML
5. Outputs a Markdown pass/fail report with per-case results

The demo must exercise all four gateway scenarios:
- Small duplicate refund → auto-approved (verification token path)
- Large refund ($12,000) → Slack approval required
- Delete customer → denied by policy
- Slack message with PII → redacted before forwarding

The pass rate is printed; a non-zero failure count exits with a non-zero code (blocking the "deployment").

## Approach

Standalone Go CLI binary (`cmd/eval-runner`). Uses Docker Compose (or Docker SDK) to orchestrate the stack for each eval run. The eval runner intercepts gateway OTel traces (or reads the Postgres `audit_log`) to reconstruct the tool call trace for comparison.

Fake MCP servers (Stripe, Zendesk, Slack) are minimal Go HTTP servers that return canned responses — sufficient for trace verification without real API calls.

Python demo agent: minimal LangGraph or CrewAI agent wired to the gateway's SSE endpoint, with tools registered as MCP. Ships as a Docker image in `examples/support-agent/`.

## Scope

- **In**: `EvalSuite` YAML format + loader, eval runner CLI binary (`cmd/eval-runner`), Docker Compose file (`deploy/docker-compose.yml`) with all v0 services, fake MCP servers for Stripe/Zendesk/Slack (`examples/fake-mcp-servers/`), Python support-refund demo agent (`examples/support-agent/`), trace capture from Postgres `audit_log`, per-case pass/fail evaluation, Markdown report output, non-zero exit on any failure, `make demo` target in Makefile
- **Out**: Baseline-relative gating (cost vs. previous version), replay of stored traces, OTel trace diffing, auto-suggested eval cases, multi-agent eval suites, Kubernetes deployment of the eval runner

## Boundary Candidates

- Trace capture mechanism (Postgres `audit_log` vs OTel span — choose Postgres for v0 simplicity; OTel in v2+)
- EvalSuite schema (`mustInclude`, `mustNotInclude`, `policyOutcome`) — must be forward-compatible with v2 baseline-relative additions
- Fake MCP server protocol (must speak real MCP JSON-RPC, not custom stubs)

## Out of Boundary

- The CI/CD runner integration (GitHub Actions, etc.) — out of v0 scope; the exit code convention is sufficient
- Kubernetes eval runner deployment — v1+
- Statistical pass criteria for nondeterministic agents — v2+ open question (noted in design doc)

## Upstream / Downstream

- **Upstream**: all previous slices (bare-proxy, policy-gate, session-mgmt, approval-flow), Docker, Python MCP SDK
- **Downstream**: v1 will replace the Docker Compose orchestration with a Kubernetes Job; the EvalSuite YAML format and exit-code convention must remain stable

## Existing Spec Touchpoints

- **Extends**: approval-flow — the Slack approval scenario in the demo requires a test Slack webhook (or a mock webhook endpoint) wired into the eval runner
- **Adjacent**: all gateway slices — the eval runner is a consumer of the fully assembled gateway

## Constraints

- `make demo` must work from a fresh `git clone` with only Docker and Go installed (no pre-existing Slack token required for the deny/allow/redact cases; Slack approval case requires a real or mocked webhook)
- Fake MCP servers must speak the MCP JSON-RPC-over-SSE protocol — no custom stubs
- EvalSuite YAML schema must match the format specified in `ToolGate_revised (1).md`
- Pass/fail report must be Markdown (viewable in CI artifact or terminal)
- Non-zero exit code on any case failure (enables CI gate integration)
