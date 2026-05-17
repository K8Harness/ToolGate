# ToolGate / ToolGate: Project State, Industry Analysis, and Extension Roadmap

**Date:** May 16, 2026
**Scope:** Full project audit, 2026 industry landscape, segmented extension recommendations
**Author:** Analytical review

---

## 1. Project State (Audit)

### 1.1 Identity and Positioning

The repository carries two names that should be reconciled. The folder is `ToolGate` (the older identity, described in `.kiro/index.md` as an "AI tool-use governance layer for Claude Code"). The strategic doc `project.md` rebrands the product as **ToolGate**, an "eval-gated deployment platform and MCP policy gateway for AI agents in production." The Go module path is still `github.com/K8Harness/ToolGate`. This dual identity is a real liability — every brand asset, search query, GitHub star, and academic citation will leak across two names. Pick one before the first public release.

The rebrand from ToolGate → ToolGate is itself a strategic improvement, not a cosmetic one: it widens the buyer from "Claude Code users" to "any AI platform team," and it narrows the wedge from generic "tool governance" to the more defensible **eval-gated deployment**.

### 1.2 What is actually built

The v0 scope is implementation-complete per the `spec.json` files, although the `.kiro/steering/roadmap.md` file shows `eval-gate` as `[ ]` (stale — `eval-gate/spec.json` reports `implementation-complete`). Update the roadmap.

Five vertical-slice specs have shipped:

| Slice | Status | What it delivers |
|---|---|---|
| `bare-proxy` | implemented | MCP SSE proxy, session lifecycle, context injection, request logging |
| `policy-gate` | implemented | YAML policy engine, allow/deny/approval predicates, Postgres audit log |
| `session-mgmt` | implemented | Redis session mutex, per-turn RWLock for read/write concurrency |
| `approval-flow` | implemented | Slack approval bridge, Postgres tickets, Redis pub/sub resume, 5-min timeout |
| `eval-gate` | implementation-complete | EvalSuite YAML runner, Docker Compose orchestration, fake Stripe/Zendesk/Slack MCP servers, Markdown pass/fail report |

Codebase metrics: ~30 Go source files plus matching test files, dependency-injected `main.go` for the eval-runner, real integration tests (`testcontainers-go` for Postgres), OpenTelemetry instrumentation, MCP Go SDK at v1.6.0. The code follows defensible conventions — `crypto/rand` for IDs, panic recovery, `log/slog` structured logging, Go 1.22 `ServeMux` patterns, externalized config with startup validation. This is materially better engineering hygiene than the median open-source agent infrastructure project.

### 1.3 What is not built (vs. the `project.md` roadmap)

The v0 → v3 plan in `project.md` is ~15 months for a 3–5 engineer team. Currently shipped is approximately the v0 cut. Outstanding:

- **v1**: Kubernetes operator, 4 CRDs (`Agent`, `AgentPolicy`, `EvalSuite`, `AgentDeployment`), admission webhook for sidecar injection, Helm chart.
- **v2**: Baseline-relative eval gating, version comparison reports, OPA/Rego policy backend, tamper-evident audit log, multi-tenant isolation, JS/TS SDK.
- **v3**: Web UI, cost attribution, compliance export packs, Envoy/service-mesh integration, verification token issuance.

### 1.4 Spec-driven development framework (the meta-layer)

`.agents/skills/kiro-*/` contains a 19-skill Kiro-style spec-driven dev framework — discovery, requirements (EARS format), design (light/full discovery, principles, synthesis, review gate), tasks (parallel analysis), implementation (with reviewer/debugger sub-agents), validation (gap, design, impl), and a `kiro-verify-completion` "fresh-evidence" gate. This is heavier than the typical project's `CLAUDE.md` setup; the skill catalog is itself a product surface (see §4.1).

### 1.5 Audit findings — what to fix before going public

1. **Brand collision**: ToolGate ↔ ToolGate ↔ K8Harness ↔ `agents.io/v1` API group. Pick one and propagate.
2. **Roadmap drift**: `.kiro/steering/roadmap.md` says `eval-gate` is `[ ]`; its `spec.json` says `implementation-complete`. The whole point of the spec-driven methodology is fresh-evidence verification — this is a self-inflicted credibility hole.
3. **Missing README**: The repo has `project.md` and `v0.md` but no public-facing `README.md`. First-touch traffic will bounce.
4. **No LICENSE file** visible at the root (the doc claims Apache 2.0 — file must exist).
5. **No `Makefile` or `make demo` target visible** despite the brief calling it the demo gate.
6. **No CI configured** (`.github/workflows/` not present in the listing).
7. **No public benchmark** for the <10ms p95 overhead claim in `project.md`. This is a competitive talking point; without a published number, it is a marketing assertion.

---

## 2. Industry Trends (May 2026)

### 2.1 The agent governance market is real, fast, and getting crowded

The AI governance market sits at roughly $419M in 2026, projected to reach ~$5.9B by 2035 at 34% CAGR ([Precedence Research](https://www.precedenceresearch.com/ai-governance-market)). Compliance spending alone is forecast at $5B by 2027 as fragmented AI laws cover half the world's economies. Gartner projects guardian agents will capture 10–15% of the agentic AI market by 2030.

The structural driver: **agentic AI adoption outpaces governance roughly 8:1**, with agents entering production 7–8× faster than orgs build governance around them. This gap is ToolGate's reason to exist. The window to fill it is approximately the next 18 months before incumbents (Microsoft, Databricks, hyperscalers) close it from above and OSS competitors (Obot, IBM ContextForge, Bifrost) close it from below.

### 2.2 Regulatory forcing functions are now binding, not aspirational

- **OWASP Top 10 for Agentic Applications (2026)**, finalized in Dec 2025 by 100+ contributors ([OWASP](https://genai.owasp.org/resource/owasp-top-10-for-agentic-applications-for-2026/)). Ten risk categories (ASI01–ASI10) including goal hijacking, tool misuse, identity abuse, memory poisoning, cascading failures, rogue agents. Now a procurement checklist item.
- **EU AI Act high-risk obligations**: hard deadline **August 2, 2026** — Articles 9–17 (risk management, data governance, logging, human oversight, accuracy, robustness, cybersecurity) apply to any agent in a high-risk use case ([Modulos compliance guide](https://www.modulos.ai/ai-compliance-guide/)).
- **NIST AI RMF + GenAI Profile**, **ISO/IEC 42001**, **Colorado AI Act (June 2026)** — all in force or imminent.

ToolGate's `project.md` already maps coverage to OWASP/EU AI Act/NIST/ISO 42001/SOC 2. This positioning is correct and timely. The risk is that the mapping is documentary, not technical — actual evidence collection (audit trails, attestation, control mapping) is v2/v3 work.

### 2.3 The MCP gateway space went from open seam to crowded category in one year

By April 2026, MCP is deployed on 10,000+ enterprise servers with 97M+ SDK downloads ([Lunar.dev](https://www.lunar.dev/)). The MCP gateway category now contains at least:

- **Lunar.dev MCPX** — Gartner-recognized 2024/2025, ~4ms p99, DLP-in-gateway, identity-based governance, OAuth passthrough.
- **MintMCP** — SOC 2 Type II, regulated-industry positioning.
- **Lasso Security** — triple-gate (AI/MCP/API), prompt-injection focus.
- **IBM ContextForge** — OSS, K8s multi-cluster federation.
- **Microsoft MCP Gateway** — K8s-native, session-aware routing.
- **Obot** — OSS, K8s control plane, $35M seed (2025).
- **Bifrost** (Maxim AI) — Go-based, 11µs overhead at 5K req/s.
- **MCPJungle** — lightweight, OSS.

Implication: a generic MCP policy gateway is no longer a defensible product. **ToolGate's gateway alone won't win** — the deployment gate is what makes it different.

### 2.4 Microsoft Agent Governance Toolkit (April 2026) is the existential competitive event

Released April 3, 2026 ([Microsoft Open Source Blog](https://opensource.microsoft.com/blog/2026/04/02/introducing-the-agent-governance-toolkit-open-source-runtime-security-for-ai-agents/)). MIT-licensed. Available in Python, TypeScript, **Rust, Go**, and .NET. Seven packages:

1. **Agent OS** — sub-0.1ms p99 policy engine (vs. ToolGate's <10ms target).
2. **Agent Runtime** — execution rings (CPU-style privilege levels), saga orchestration, kill switch.
3. **Agent SRE** — SLOs, error budgets, circuit breakers, chaos engineering, progressive delivery.
4. **Agent Compliance** — automated mapping to EU AI Act, HIPAA, SOC 2; full OWASP coverage.
5. **Agent Marketplace** — Ed25519-signed plugin lifecycle.
6. **Agent Lightning** — RL training governance.
7. **Agent Mesh** — DIDs, Inter-Agent Trust Protocol, dynamic trust scoring.

Native adapters for LangChain, CrewAI, Google ADK, Microsoft Agent Framework, OpenAI Agents SDK, Haystack, LangGraph, PydanticAI.

This is much larger than ToolGate's surface area, has Microsoft's distribution, and covers all 10 OWASP categories — versus ToolGate's deliberately scoped subset. The `project.md` correctly identified the Toolkit as the most direct competitor; that prediction has now materialized.

**Counterargument to "Microsoft makes ToolGate irrelevant":** the Toolkit is library-shaped (in-process SDKs), while ToolGate is platform-shaped (operator + CRDs + out-of-process gateway). Library shape requires every framework integration to be re-instrumented; platform shape works for arbitrary in-pod processes. Microsoft will own the language-SDK governance story; ToolGate can own the **declarative, K8s-native deployment-gated** story. These are adjacent, not identical.

### 2.5 Eval-gated deployment is being claimed by Braintrust

Braintrust ([Braintrust eval-driven development](https://www.braintrust.dev/articles/eval-driven-development)) is now marketed as "the only platform that explicitly connects evaluation results to deployment decisions … blocks merges if eval scores fall below a threshold." Customers include Stripe and Notion. Their free tier is 1M spans/month, 10K evals.

This is the headline ToolGate planned to claim. Braintrust's wedge applies to LLM/agent quality at the application layer; ToolGate's wedge applies at the deployment/CI gate with policy + tool-trace verification. The differentiation is real but narrower than `project.md` implies.

Other movers in the eval space:
- **Langfuse** acquired by ClickHouse, Jan 2026; remains the dominant OSS observability platform.
- **Promptfoo** acquired by OpenAI for **$86M, March 9, 2026** ([DEV Community write-up](https://dev.to/nebulagg/top-5-ai-agent-eval-tools-after-promptfoos-exit-576i)). Creates an opening for vendor-neutral OSS eval tooling.
- **LangSmith Deployment** — quality-gated production releases via Control Plane API.
- **MLflow GenAI** — `mlflow.genai.evaluate()` callable from CI with programmatic pass/fail.

The pattern is clear: eval-gated CI is becoming a standard practice ([AppScale AI-Native CI/CD](https://appscale.blog/en/blog/ai-native-cicd-for-llm-features-eval-gates-prompt-diff-canary-rollouts-2026)). Five-gate pipelines (lint → offline eval → cost budget → shadow eval → canary with auto-rollback) are emerging as best practice. ToolGate needs to position itself as **the agent-layer deployment gate** (policy, tool-trace, MCP-aware), not generic eval gating — that battle is already being fought one layer up.

### 2.6 Spec-driven development is now its own category

- **GitHub Spec Kit**: 93,000+ stars, v0.8.7 (May 7, 2026), supports 30+ AI coding agents ([MarkTechPost roundup](https://www.marktechpost.com/2026/05/08/9-best-ai-tools-for-spec-driven-development-in-2026-kiro-bmad-gsd-and-more-compare/)).
- **AWS Kiro**: replaced Amazon Q Developer (effective May 15, 2026 — yesterday). AWS's primary IDE play.
- **BMAD-METHOD**: 46,700+ GitHub stars, 12+ specialist agents across the SDLC, v6.6.0 (April 29, 2026).

ToolGate's own development used a Kiro-style methodology. That methodology — the 19 skills in `.agents/skills/` — is a credible product surface in its own right (see §4.1).

Enterprise adoption headwind: ["Why Spec-Driven Development Tools Fail in the Enterprise"](https://martinelli.ch/why-spec-driven-development-tools-fail-in-the-enterprise/) — SDD tools assume greenfield; enterprises live in brownfield. ToolGate's `kiro-validate-gap` skill explicitly addresses this. Worth surfacing.

### 2.7 Hyperscaler agent platforms are converging on full-stack control planes

Google's Agentic Data Cloud (Cloud Next 2026) consolidates TPU + Gemini + Gemini Enterprise + Cross-Cloud Lakehouse. Databricks Unity AI Gateway governs MCP servers, LLM endpoints, and coding agents. AWS Bedrock AgentCore now hosts AWS's DevOps Agent and Security Agent as GA. Implication: enterprises buying agent platforms from a hyperscaler do not need a separate ToolGate; ToolGate's market is enterprises that **cannot or will not** lock into a single cloud — typically multi-cloud, regulated, or sovereignty-constrained.

---

## 3. Competitive Positioning Synthesis

| Axis | ToolGate | Closest competitor | Honest read |
|---|---|---|---|
| MCP gateway with policy | Yes | Lunar.dev MCPX, Obot, IBM ContextForge, Microsoft MCP Gateway | Table stakes. Not a wedge alone. |
| Eval-gated deployment | Yes (planned core wedge) | Braintrust, LangSmith Deployment, MLflow GenAI | Braintrust owns LLM-quality CI gating; ToolGate's angle is policy + tool-trace gating. Narrower than `project.md` implies. |
| OSS + K8s-native + framework-agnostic | Yes | Microsoft Agent Governance Toolkit, IBM ContextForge, Obot | Microsoft Toolkit is library-shaped; ToolGate is platform-shaped. Real but narrow differentiation. |
| OWASP Top 10 coverage | Partial (deliberately) | Microsoft Toolkit (all 10) | Honest scope discipline; vulnerable to RFP checklist comparisons. |
| Compliance vocabulary (EU AI Act / NIST / ISO 42001) | Mapped in docs | Credo AI, Holistic AI, IBM watsonx.governance | Mapping ≠ evidence collection. v2/v3 work. |
| Cloud-neutral | Yes | (vs. hyperscalers — they lose this axis) | Strong wedge for multi-cloud / sovereign / regulated buyers. |
| Spec-driven methodology baked in | Yes (19 skills) | Spec Kit, Kiro, BMAD as standalone tools; nothing combines methodology + governance product | Genuinely novel combination; can be a moat or a distraction. |

The defensible 18-month positioning: **the OSS, K8s-native, cloud-neutral deployment gate for MCP-governed agents in regulated environments**. Not the universal agent governance toolkit (Microsoft owns that race). Not the universal eval platform (Braintrust + Langfuse own that). The seam: **declarative, deployment-pipeline-native, framework-agnostic, MCP-aware governance for orgs that can't pick a hyperscaler.**

---

## 4. Extension Roadmap by Target Segment

### 4.1 AI Agent Infrastructure Layer (middleware for other agent tools)

**Premise:** Position ToolGate not as an end-user product but as governance infrastructure other agent platforms compose with.

| Move | Why | Effort |
|---|---|---|
| Split the spec-driven dev framework (`.agents/skills/kiro-*`) into a standalone, separately versioned, separately marketed OSS project | The 19-skill suite is materially more sophisticated than Spec Kit's CLI surface and pairs with any spec-driven IDE. Pitched alone, it can grow distribution that flows back to ToolGate. | M |
| Publish **EvalSuite YAML as a draft spec** with a JSON Schema, then court Braintrust/Langfuse/Promptfoo to adopt it as an interchange format | If the YAML format becomes a de facto standard, ToolGate is the reference implementation. If it doesn't, you've still de-risked the format. | M |
| Build first-class **eval-provider adapters** for Braintrust, Langfuse, Promptfoo, MLflow GenAI | Avoid the "Braintrust ate our lunch" framing — make ToolGate the gate, them the measurement provider. | M |
| Publish a **benchmark harness** that compares ToolGate / Microsoft Toolkit / Obot / Lunar MCPX on (latency, OWASP coverage, declarative surface, K8s integration, multi-tenant story) | Credible third-party-style comparisons earn citations and inbound. Without them, the <10ms claim is marketing. | M |
| Ship a **signed MCP-server catalog** (Ed25519 manifests, content-addressed) compatible with Microsoft Agent Marketplace's signing format | Interoperate with the largest signed-catalog effort rather than fragment it. | M-L |
| Publish a **standard for eval reproducibility under nondeterminism** (the project.md open question #1) — temperature-zero replay + statistical N-of-M criterion | If you propose the standard, you become the canonical implementation. | S-M |

### 4.2 Enterprise (Fortune 500, regulated)

**Premise:** Procurement-driven, security-team-vetoed, 12–18 month sales cycle, six-to-seven-figure ACV. ToolGate's current architecture choices (OSS, OPA/Rego on the roadmap, OTel-native, K8s-native, default-deny) are well-aligned. The gaps are operational and certification.

| Move | Why | Effort |
|---|---|---|
| **SOC 2 Type II** for managed offering; align OSS code paths so customers can self-attest | Procurement gate. Without it, no enterprise pilot converts. | XL |
| **EU AI Act compliance export pack** — Article 12 (logging) + Article 14 (human oversight) + Article 17 (quality management) evidence bundle | EU AI Act August 2, 2026 deadline is in 75 days. Late but a wedge product opportunity. | L |
| **Tamper-evident audit log** (Merkle hash chain, optional WORM target — S3 Object Lock / Azure immutable blobs) | Already on v2 roadmap. Make it concrete and certify-able. | M |
| **SSO/SAML + SCIM** for the v3 UI; per-namespace RBAC tied to K8s identity | Required for any org over ~500 employees. | M |
| **Air-gapped install**, Helm chart with offline image bundle, BYO-Postgres-and-Redis | Defense, finance, regulated healthcare. Differentiates from SaaS-only commercial competitors. | M |
| **Cost attribution per team / agent / tool** | v3 roadmap. Pull forward — it's the metric that justifies the line item internally. | M |
| **Partnerships with Credo AI, Holistic AI, IBM watsonx.governance** as the runtime-enforcement layer they currently lack | They have the buyer; you have the runtime. Mutual upgrade. | M |
| **Reference architectures**: financial services (PCI + SOX + SR 11-7), healthcare (HIPAA + 21 CFR Part 11), public sector (FedRAMP path) | Solves the "how do I deploy this in MY org" question that kills enterprise OSS adoption. | L |
| **BAA-eligible managed tier** | HIPAA-regulated buyers cannot use anything without one. | M |

### 4.3 Mid-Market SaaS / Scale-ups

**Premise:** Velocity-first engineering orgs. Want a default-on, low-config solution. Will pay for managed; will not run a dedicated platform team for governance.

| Move | Why | Effort |
|---|---|---|
| **One-command Helm install** with sane defaults that work without any policy authoring | Demo-to-prod in under an hour. Mid-market churns on day-1 friction. | M |
| **Managed/cloud offering** with usage-based pricing (per-million tool calls or per-eval-run) | Mid-market does not run K8s operators they didn't have to. | XL |
| **LiteLLM, Portkey, Helicone integration** (`project.md` already names them as adjacent, not competitive) | Compose with the LLM gateway buyers already have. | S-M |
| **GitHub App + GitHub Action for eval gates** — `toolgate-eval` action that fails the PR check on regression | Where mid-market lives. Lowers adoption cost from "deploy a platform" to "add a workflow." | M |
| **Pre-built policy packs** for common patterns (refunds-with-thresholds, PII-redaction, deletion-deny, rate-limit-per-tenant) | Removes the blank-canvas problem. | S |
| **Cost dashboard** built into v3 UI | "How much did agents spend this month per team" is the question executives ask. | M |
| **Templated demos for the popular frameworks**: LangGraph, CrewAI, OpenAI Agents SDK, PydanticAI | Reduces evaluation friction from "does this work with my stack" to "yes." | M |

### 4.4 Indie Developers / OSS Community

**Premise:** Bottom-up adoption fuels everything else. GitHub stars, Discord, conference talks, plugin ecosystem.

| Move | Why | Effort |
|---|---|---|
| Resolve the **brand and domain** (ToolGate vs ToolGate), publish the public README, the LICENSE file, the contributing guide | Without these, every other indie-targeted move underperforms. | S |
| **Demo video <90 seconds** showing the four-scenario `make demo` end-to-end | "Show, don't pitch." The bar for OSS launches in 2026 is high. | S |
| **VS Code / Cursor / Claude Code extension** for policy authoring (YAML schema, validation, preview decisions for sample inputs) | Authoring policy in YAML is the worst part of any policy product. Lower that friction and indie developers stay. | M |
| **Public eval-suite registry** — community-contributed suites for common agent patterns (RAG, refunds, ticket triage, code review) | Network effect on the eval side. Spec Kit's 93k stars is partly a registry effect. | M |
| **Plugin/skill marketplace** within the Cowork/Claude Code ecosystem since the spec-driven framework is already plugin-shaped | The methodology layer reaches an audience the platform layer cannot. | S-M |
| **Active Discord/Slack + weekly office hours** | Spec Kit and BMAD's growth is community-driven. So is Kiro's, despite AWS backing. | Ongoing |
| **Talks at KubeCon, AI Engineer Summit, MCP Dev Days** | Distribution channel for technical OSS. | M, ongoing |
| **Free Cowork plugin / Claude Code skill** that wraps `kiro-spec-quick` for non-ToolGate projects | The framework's quality is the easiest brand-builder you have. Give it away. | S |

---

## 5. Strategic Trade-offs and Counterarguments

A useful audit is one that surfaces its own weaknesses.

**Counterargument 1: "The window has already closed."**
Microsoft Toolkit shipped in April with sub-0.1ms latency and full OWASP coverage. Braintrust owns the eval-gated narrative. The MCP gateway space has at least eight credible players. **Response:** the seam (declarative, K8s-native, OSS, framework-agnostic, multi-cloud-by-default) is not occupied by any single competitor. Microsoft Toolkit is library-shaped. Braintrust is closed-source SaaS focused on LLM quality, not policy/tool gating. Obot has K8s control plane but not deployment gating. The combinatorial position is real, but it is also narrow — perhaps 100–500 target accounts globally, not 10,000.

**Counterargument 2: "The dual identity and absence of a public face suggests this is more academic exercise than product."**
60 Go files, real integration tests, OTel instrumentation, testcontainers harnesses. The engineering is real. The marketing is not. Diagnosis: builder-shaped project that needs a product-marketing function before its first GA. Without that, it stays a personal portfolio piece.

**Counterargument 3: "Spec-driven development methodology + governance product is a focus problem, not a synergy."**
There is a real risk that maintaining 19 skills, a Go gateway, a Python SDK, a Kubernetes operator, a Helm chart, a web UI, and a methodology framework with one engineer is impossible. **Response:** split the methodology into its own repo (§4.1) so they can be developed and adopted independently. Maintain the integration as documentation, not code.

**Counterargument 4: "Why would a regulated enterprise buy from a pre-alpha OSS project when Microsoft just shipped a complete toolkit?"**
They wouldn't. The path is: indie/OSS adoption → mid-market managed → enterprise commercial. Skipping straight to enterprise sales pre-1.0 fails.

**What to verify before committing:**
- Talk to 10 AI platform teams. Ask: "If you deployed an agent today, what blocks you from running it against production tools?" If the answer is "eval gating" — wedge confirmed. If it's "prompt injection" — wedge is wrong (and Microsoft / Lasso / Lakera already own that).
- Benchmark the gateway's actual p95 overhead. Publish it. If it is materially worse than Microsoft Toolkit's 0.1ms or Bifrost's 11µs, the latency claim must be removed from positioning.
- Verify the EU AI Act compliance mapping with a real compliance lawyer, not just `project.md`. Mapping is a legal artifact, not an engineering one.

---

## 6. Recommended Next 30 / 90 / 180 Days

### Next 30 days (positioning hygiene)
1. Pick one name (ToolGate recommended). Update module path, repo, docs.
2. Public `README.md`, `LICENSE`, `CONTRIBUTING.md`, `CODE_OF_CONDUCT.md`.
3. CI on every PR (`go test ./...` + `go vet` + `staticcheck` + `golangci-lint`).
4. Resolve the roadmap-vs-spec drift; make `kiro-verify-completion` the gate on every merge.
5. Publish a benchmark notebook (latency, throughput) against a baseline (raw HTTP proxy, MintMCP open-source tier, IBM ContextForge).
6. Public demo video.

### Next 90 days (v1 cut + first wedge)
1. Ship v1 per `project.md`: K8s operator, 4 CRDs, admission webhook, Helm chart.
2. Publish EvalSuite YAML as a versioned spec with a JSON Schema.
3. First eval-provider adapter (Braintrust or Promptfoo).
4. First reference architecture (financial services or healthcare — whichever the first 3 pilot customers are in).
5. Open-source the methodology framework as a separate repo.

### Next 180 days (v2 + commercial wedge)
1. Tamper-evident audit log, OPA/Rego backend, multi-tenant isolation.
2. EU AI Act compliance export pack (post-Aug 2 deadline, but still a wedge — most orgs will be late).
3. Managed offering with usage-based pricing.
4. Partnership announcement with one compliance vendor (Credo AI is the most natural fit).
5. First case study with a named pilot customer.

---

## 7. Open Questions for Henry

1. Is the strategic intent commercial (managed SaaS + enterprise) or community (OSS only, no monetization)? The roadmap differs materially.
2. Who is the second engineer? The v0→v3 plan is 15 months at 3–5 engineers; current team appears to be one.
3. Is the EU AI Act August 2 deadline a forcing function for ToolGate or a market opportunity ToolGate is too early for?
4. What's your tolerance for being a feature inside Microsoft Toolkit / Obot / Lunar MCPX vs. being a category-defining product?

---

## Sources

- [OWASP Top 10 for Agentic Applications 2026](https://genai.owasp.org/resource/owasp-top-10-for-agentic-applications-for-2026/)
- [Microsoft Open Source Blog — Agent Governance Toolkit](https://opensource.microsoft.com/blog/2026/04/02/introducing-the-agent-governance-toolkit-open-source-runtime-security-for-ai-agents/)
- [Microsoft Agent Governance Toolkit — Runtime Security](https://www.digitalapplied.com/blog/microsoft-agent-governance-toolkit-runtime-security)
- [Help Net Security — Microsoft AI Agent Governance Toolkit](https://www.helpnetsecurity.com/2026/04/03/microsoft-ai-agent-governance-toolkit/)
- [Lunar.dev — Enterprise MCP Gateway](https://www.lunar.dev/)
- [Best Open Source MCP Gateways 2026 (Lunar.dev)](https://www.lunar.dev/post/the-best-open-source-mcp-gateways-in-2026)
- [Composio — 10 Best MCP Gateways for Developers in 2026](https://composio.dev/content/best-mcp-gateway-for-developers)
- [MintMCP — Best MCP Gateways for Enterprise Engineering Teams 2026](https://www.mintmcp.com/blog/gateways-enterprise-engineering-with-mcp)
- [TrueFoundry — Definitive Guide to AI Gateways in 2026](https://www.truefoundry.com/blog/a-definitive-guide-to-ai-gateways-in-2026-competitive-landscape-comparison)
- [Braintrust — Eval-driven development](https://www.braintrust.dev/articles/eval-driven-development)
- [Braintrust — Langfuse alternatives 2026](https://www.braintrust.dev/articles/langfuse-alternatives-2026)
- [Latitude — Best AI Agent Evaluation Platforms in 2026](https://latitude.so/blog/best-ai-agent-evaluation-platforms-2026-comprehensive-comparison)
- [DEV — Top 5 AI Agent Eval Tools After Promptfoo's Exit](https://dev.to/nebulagg/top-5-ai-agent-eval-tools-after-promptfoos-exit-576i)
- [AppScale — AI-Native CI/CD for LLM Features 2026](https://appscale.blog/en/blog/ai-native-cicd-for-llm-features-eval-gates-prompt-diff-canary-rollouts-2026)
- [Red Hat Developer — Eval-driven development](https://developers.redhat.com/articles/2026/03/23/eval-driven-development-build-evaluate-ai-agents)
- [LangChain — LangSmith CI/CD pipeline example](https://docs.langchain.com/langsmith/cicd-pipeline-example)
- [Modulos — Master AI Compliance in 2026](https://www.modulos.ai/ai-compliance-guide/)
- [Modulos — Agentic AI governance](https://www.modulos.ai/blog/agentic-ai-governance/)
- [MarkTechPost — 9 Best AI Tools for Spec-Driven Development in 2026](https://www.marktechpost.com/2026/05/08/9-best-ai-tools-for-spec-driven-development-in-2026-kiro-bmad-gsd-and-more-compare/)
- [Kiro.dev](https://kiro.dev/)
- [byteiota — AWS Kiro Replaces Amazon Q Developer](https://byteiota.com/aws-kiro-replaces-amazon-q-developer-spec-driven-ide/)
- [Martinelli — Why Spec-Driven Development Tools Fail in the Enterprise](https://martinelli.ch/why-spec-driven-development-tools-fail-in-the-enterprise/)
- [Databricks — Unity AI Gateway docs](https://docs.databricks.com/aws/en/ai-gateway/)
- [Databricks Blog — Expanding Agent Governance](https://www.databricks.com/blog/ai-gateway-governance-layer-agentic-ai)
- [SiliconANGLE — Google Cloud Next 2026 control plane](https://siliconangle.com/2026/04/20/google-cloud-next-2026-preview-real-story-isnt-ai-control-plane/)
- [Precedence Research — AI Governance Market Size](https://www.precedenceresearch.com/ai-governance-market)
- [tech-insider — Agentic AI in Enterprise 2026 $9B Market Analysis](https://tech-insider.org/agentic-ai-enterprise-2026-market-analysis/)
