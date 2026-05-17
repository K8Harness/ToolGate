# Requirements Document

## Introduction

The eval-gate feature provides an automated deployment gate for the ToolGate gateway. An eval runner CLI binary orchestrates the Docker Compose stack, runs an EvalSuite of YAML-defined test cases against the fully assembled gateway and a Python demo agent, captures the gateway's policy decision trace from the Postgres audit log, and compares each observed trace against expected results. The runner outputs a Markdown pass/fail report and exits non-zero if any case fails, blocking promotion of a broken agent version. The feature concludes the v0 demo: `make demo` exercises all four gateway scenarios end-to-end.

## Boundary Context

- **In scope**: EvalSuite YAML format and loader; eval runner CLI binary (`cmd/eval-runner`); Docker Compose file covering all v0 services (gateway, Postgres, Redis, fake MCP servers, Python demo agent); fake MCP servers for Stripe, Zendesk, and Slack that speak the real MCP JSON-RPC-over-SSE protocol; Python support-refund demo agent wired to the gateway; trace capture from the Postgres `audit_log` table; per-case pass/fail evaluation against `mustInclude`, `mustNotInclude`, and `policyOutcome` criteria; Markdown report with per-case results and overall pass rate; non-zero exit code on any case failure; `make demo` Makefile target.
- **Out of scope**: baseline-relative gating (comparison against a previous version's results); replay of stored traces; OTel trace diffing; auto-suggested eval cases; multi-agent eval suites; Kubernetes eval runner deployment; CI/CD runner integration beyond the exit code convention.
- **Adjacent expectations**: the eval runner reads the `audit_log` table written by the existing gateway audit writer — it does not own that schema or its write path; all four gateway behaviors (allow, deny, approvalRequired, PII redaction) must be operational in the assembled stack before cases can pass; the Slack approval scenario requires either a real Slack webhook or a mock webhook endpoint included in the stack; the EvalSuite YAML schema must remain forward-compatible with future v2 baseline-relative fields.

## Requirements

### Requirement 1: EvalSuite YAML Format

**Objective:** As a gateway operator, I want to define test cases in a structured YAML file, so that eval suites are human-readable, version-controllable, and forward-compatible with future evaluation criteria.

#### Acceptance Criteria

1. The Eval Runner shall load an EvalSuite from a YAML file at a path provided as a CLI argument.
2. Each test case in the EvalSuite YAML shall contain: a unique case name, a natural-language or structured input that triggers the demo agent, a `mustInclude` list of expected tool names in declared sequence order, an optional `mustNotInclude` list of tool names that must not appear in the trace, and a `policyOutcome` field specifying the expected gateway decision (`allow`, `deny`, `approvalRequired`, or `expired`) for the final observed audit log entry of that case.
3. If the Eval Runner cannot parse the EvalSuite YAML file, it shall print a descriptive error identifying the file path and the first parse error, and exit with a non-zero code without running any cases.
4. The EvalSuite YAML format shall not break existing loaders when future fields (such as `baseline`) are added to a test case.

### Requirement 2: Demo Stack Orchestration

**Objective:** As a gateway operator, I want the eval runner to bring up the complete Docker Compose stack before running cases, so that each eval run starts from a consistent, fully initialized state.

#### Acceptance Criteria

1. When the eval runner starts, the Eval Runner shall bring up all required services (gateway, Postgres, Redis, fake MCP servers, Python demo agent) using Docker Compose before submitting any test case.
2. The Eval Runner shall wait for all services to pass their configured health checks before submitting the first test case.
3. When the eval run completes (whether all cases pass or any case fails), the Eval Runner shall tear down the Docker Compose stack.
4. If any required service fails to reach a healthy state within a startup timeout, the Eval Runner shall print a diagnostic error naming the unhealthy service and exit non-zero without running any cases.
5. The Eval Runner shall locate the Docker Compose file within the repository without requiring an externally configured environment variable.

### Requirement 3: Eval Runner CLI

**Objective:** As a gateway operator, I want a CLI binary that orchestrates the full eval run, so that evaluation can be triggered from a terminal or CI pipeline with a single command.

#### Acceptance Criteria

1. The Eval Runner shall be buildable with `go build ./cmd/eval-runner` and executable as `go run ./cmd/eval-runner <evalsuiteFile>`.
2. The Eval Runner shall accept at minimum one CLI argument: the path to the EvalSuite YAML file.
3. When invoked via `make demo`, the Eval Runner shall use a default EvalSuite YAML file included in the repository without requiring the operator to supply a file path.
4. The Eval Runner shall print per-case progress (case name and running/pass/fail status) to stdout as each case executes.
5. If Docker is not available or Docker Compose cannot be found, the Eval Runner shall print a clear diagnostic message and exit non-zero before attempting to start any service.

### Requirement 4: Trace Capture

**Objective:** As a gateway operator, I want the eval runner to read the gateway's audit log after each test case, so that it can reconstruct the sequence of tool calls and policy decisions the gateway made.

#### Acceptance Criteria

1. After each test case completes, the Eval Runner shall query the Postgres `audit_log` table for all rows associated with the case's session, ordered by `decided_at` ascending.
2. The Eval Runner shall extract the `tool_name` and `decision` fields from each matching row to form the observed trace for that case.
3. If the Eval Runner cannot connect to Postgres to retrieve the trace, it shall record that case as failed with a diagnostic error describing the connection failure, and continue executing subsequent cases.

### Requirement 5: Per-Case Evaluation

**Objective:** As a gateway operator, I want each test case evaluated against its expected trace, so that regressions in tool call order or policy outcomes are automatically detected.

#### Acceptance Criteria

1. When the observed trace is captured, the Eval Runner shall verify that every tool name in `mustInclude` appears in the trace in the declared order as a subsequence (intervening tool calls between expected ones are allowed).
2. When the observed trace is captured, the Eval Runner shall verify that no tool name in `mustNotInclude` appears anywhere in the trace.
3. When the observed trace is captured, the Eval Runner shall verify that the `decision` field of the final audit log row for the case matches the case's `policyOutcome` value exactly.
4. If any check (mustInclude order, mustNotInclude absence, or policyOutcome match) fails, the Eval Runner shall mark the case as failed and record which specific check failed, along with the expected and observed values.
5. If all checks pass, the Eval Runner shall mark the case as passed.

### Requirement 6: Pass/Fail Report and Exit Code

**Objective:** As a gateway operator, I want a Markdown report summarizing per-case results and an exit code that signals overall pass/fail, so that the eval run can serve as a deployment gate in CI.

#### Acceptance Criteria

1. The Eval Runner shall output a Markdown-formatted report to stdout containing: a summary table listing all cases with their pass/fail status; the overall pass rate (e.g., "3/4 cases passed"); and for each failed case, the specific check(s) that failed with the expected and observed values.
2. When all cases pass, the Eval Runner shall exit with code 0.
3. When one or more cases fail, the Eval Runner shall exit with a non-zero code.
4. The Eval Runner shall print the overall pass/fail verdict as the final line of the report before exiting.

### Requirement 7: Demo Scenarios Coverage

**Objective:** As a gateway operator, I want the default EvalSuite to exercise all four gateway behaviors, so that `make demo` validates the complete v0 gateway end-to-end.

#### Acceptance Criteria

1. The default EvalSuite shall include a test case for the auto-approve scenario: a small duplicate refund tool call that the policy gate allows automatically, with `policyOutcome: allow`.
2. The default EvalSuite shall include a test case for the human-approval scenario: a large refund ($12,000) tool call that the policy gate routes to the Slack approval flow, with `policyOutcome: approvalRequired`.
3. The default EvalSuite shall include a test case for the policy-deny scenario: a delete-customer tool call that the policy gate denies, with `policyOutcome: deny`.
4. The default EvalSuite shall include a test case for the PII-redaction scenario: a Slack-message tool call whose input arguments contain PII; the case shall verify via `mustNotInclude` or an arguments assertion that the PII value does not appear in the arguments recorded against the fake Slack MCP server's audit log entry.
5. When all services are correctly assembled and healthy, running `make demo` shall produce an overall passing eval result.

### Requirement 8: Fake MCP Server Compatibility

**Objective:** As a gateway operator, I want fake MCP servers for Stripe, Zendesk, and Slack that speak the real MCP protocol, so that the demo agent and gateway operate as they would in production.

#### Acceptance Criteria

1. The fake MCP servers for Stripe, Zendesk, and Slack shall accept and respond to MCP JSON-RPC requests over SSE using the current MCP protocol specification.
2. Each fake MCP server shall return canned, deterministic responses for all tool calls defined in the default EvalSuite.
3. The fake MCP servers shall be packaged as Docker images buildable from the repository without external API credentials.
4. If a fake MCP server receives an unrecognized tool call, it shall return a well-formed JSON-RPC error response rather than silently dropping the request.

### Requirement 9: Python Demo Agent

**Objective:** As a gateway operator, I want a Python support-refund demo agent wired to the gateway, so that test cases exercise a realistic agentic tool-call workflow through the full gateway stack.

#### Acceptance Criteria

1. The demo agent shall connect to the gateway's MCP SSE endpoint and register its tools using the MCP client protocol.
2. When given a test case input, the demo agent shall invoke its registered MCP tools to complete the support-refund scenario, generating tool calls that traverse the gateway.
3. The demo agent shall be packaged as a Docker image buildable from the repository.
4. The demo agent shall expose a mechanism by which the eval runner can submit a test case input and wait for the agent's tool call sequence to complete before the eval runner queries the audit log.

### Requirement 10: Fresh-Clone Usability

**Objective:** As a new contributor, I want `make demo` to work from a fresh git clone with only Docker and Go installed, so that the demo is fully self-contained and does not require external account setup for core scenarios.

#### Acceptance Criteria

1. Running `make demo` on a fresh git clone shall succeed without requiring any pre-installed tools other than Docker and Go.
2. The allow, deny, and PII-redaction test cases shall not require a pre-existing Slack bot token or signing secret to run.
3. The Slack approval test case in the default EvalSuite shall be satisfiable using a mock Slack webhook endpoint included in the Docker Compose stack, so that `make demo` passes without a real Slack token.
4. All Docker images required by `make demo` shall be buildable locally from the repository; no pre-pulled or externally hosted images shall be required for the demo scenarios.
