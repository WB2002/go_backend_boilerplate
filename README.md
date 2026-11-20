# AI project config

Cursor AI-SDLC configuration for a product repository. Do not build the product in this repo.

- Start with the runbook: [docs/ai-sdlc.md](docs/ai-sdlc.md)
- Governance, risk tiers, and policies: [docs/governance.md](docs/governance.md)
- Agent rules and engineering principles: [AGENTS.md](AGENTS.md)
- Validate skill integrity: `scripts/check-skills.sh`

---

## Agents

Agents are specialized role definitions located in `.cursor/agents/`. They enforce strict role separation (e.g., builders never review their own diffs, and reviewers report only without modifying code).

### Invocation Mechanisms
- **Direct Mention**: Mention `@<agent-name>` in Cursor Chat to consult or delegate to that specific role.
- **Workflow Orchestration**: Assigned sequentially according to risk tier and diff scope via the `/orchestrate` workflow command.
- **Access Modes**:
  - `Builder`: Read/write access (`readonly: false`) to implement bounded tasks from approved plans.
  - `Reviewer / Analyst / Verifier / Diagnostics`: Read-only access (`readonly: true`) to audit, evaluate, verify, or debug without modifying code.

| Agent | Access / Type | How Invoked | Functionality & Scope |
| :--- | :--- | :--- | :--- |
| `backend-engineer` | Builder (`readonly: false`) | `@backend-engineer` or via `/orchestrate` | Implements approved server, API, domain logic, and data access tasks (`TASK-NNN`) from `plan.md` using framework best practices and TDD. Does not touch frontend UI, IaC, CI/CD, or self-review. |
| `code-reviewer` | Reviewer (`readonly: true`) | `@code-reviewer` or via `/orchestrate` | Audits pull request diffs for requirement violations, structural debt, AI-specific debt (hallucinations, excessive wrappers), and operational debt. Report only; does not edit code. |
| `competitor-analyst` | Analyst (`readonly: true`) | `@competitor-analyst` or via `/market-opportunity-research` | Maps direct competitors, indirect alternatives, open-source substitutes, and status-quo inertia to evaluate competitive defensibility. Report only. |
| `compliance-implementer` | Builder (`readonly: false`) | `@compliance-implementer` or via `/orchestrate` | Implements technical compliance controls (`COMP-NNN`) from approved compliance reports (immutable audit logs, data retention/erasure jobs, DSAR endpoints, encryption/masking). Does not audit compliance. |
| `compliance-reviewer` | Reviewer (`readonly: true`) | `@compliance-reviewer` or via `/orchestrate` | Audits codebase and feature intents against regulatory frameworks (GDPR, HIPAA, SOC 2, PCI-DSS, ISO 27001, CCPA, WCAG/EAA); reports required, covered, and missing controls. Report only. |
| `customer-dissatisfaction-analyst` | Analyst (`readonly: true`) | `@customer-dissatisfaction-analyst` or via `/market-opportunity-research` | Researches public user reviews, forums, issue trackers, and customer communities for recurring frustrations, unmet needs, and churn drivers. Report only. |
| `database-implementer` | Builder (`readonly: false`) | `@database-implementer` or via `/orchestrate` | Implements approved schema migrations, index optimizations, table constraints, and query rewrites using expand-migrate-contract practices. Never migrates production directly or reviews own diffs. |
| `database-reviewer` | Reviewer (`readonly: true`) | `@database-reviewer` or via `/orchestrate` | Audits schema changes, migrations, ORM models, and queries for lock contention, missing indexes, query latency, and expand-migrate-contract compliance. Report only. |
| `debugger` | Diagnostics (`readonly: true`) | `@debugger` | Diagnoses incidents, bugs, alert breaches, trace anomalies, and stack traces using telemetry and codebase. Report only; does not edit code or trigger rollbacks. |
| `devops` | Builder (`readonly: false`) | `@devops` or via `/orchestrate` | Authors and updates CI/CD pipelines, Dockerfiles, container builds, and release/rollback scripts; verifies release ordering and backward/forward compatibility. Does not provision cloud IaC. |
| `frontend-engineer` | Builder (`readonly: false`) | `@frontend-engineer` or via `/orchestrate` | Implements approved UI tasks (`TASK-NNN`) from `plan.md` and `experience.md` using frontend best practices, design tokens, and accessibility standards. Does not design UX or write backend logic. |
| `gtm-strategist` | Analyst (`readonly: true`) | `@gtm-strategist` or via `/market-opportunity-research` | Analyzes discovery channels, buyer procurement paths, acquisition loops, initial path to 10 customers, and validation experiments. Report only. |
| `infrastructure` | Builder (`readonly: false`) | `@infrastructure` or via `/orchestrate` | Creates and updates declarative Infrastructure-as-Code (Terraform/OpenTofu, Pulumi, Kubernetes manifests, Helm, CloudFormation) from approved plans. Does not apply live infra mutations. |
| `market-problem-analyst` | Analyst (`readonly: true`) | `@market-problem-analyst` or via `/market-opportunity-research` | Researches target customer personas, jobs-to-be-done (JTBD), acute pain points, workarounds, problem severity, and willingness to pay. Report only. |
| `observability` | Builder (`readonly: false`) | `@observability` or via `/orchestrate` | Implements structured logging, RED/USE metrics, distributed tracing spans, alert definitions, and operational runbooks per `docs/observability.md`. Does not alter core business logic. |
| `opportunity-red-team` | Analyst (`readonly: true`) | `@opportunity-red-team` or via `/market-opportunity-research` | Adversarially attempts to disprove commercial and product opportunities, exposing unvalidated assumptions, fatal product flaws, and fragile unit economics. Report only. |
| `orchestrator` | Orchestrator (`readonly: true`) | `@orchestrator` or via `/orchestrate <slug>` | Sequences SDLC lifecycle stages (discovery, UX, spec, planning, building, QA, and independent tier-proportional reviews) and logs next steps to `orchestration.md`. Does not code or approve. |
| `performance-implementer` | Builder (`readonly: false`) | `@performance-implementer` or via `/orchestrate` | Implements approved performance optimizations (`PERF-NNN`) from review reports (memory leaks, connection leaks, N+1 query elimination, I/O batching). Does not alter functional behavior. |
| `performance-reviewer` | Reviewer (`readonly: true`) | `@performance-reviewer` or via `/orchestrate` | Audits diffs and modules against `docs/nfr.md` for performance bottlenecks, latency/SLO violations, memory leaks, connection pool exhaustion, and inefficient queries. Report only. |
| `positioning-strategist` | Analyst (`readonly: true`) | `@positioning-strategist` or via `/market-opportunity-research` | Develops evidence-based positioning options, value propositions, and differentiation narratives; evaluates long-term defensibility. Report only. |
| `qa` | Verifier (`readonly: true`) | `@qa` or via `/orchestrate` | Independently verifies implemented changes; executes `scripts/verify.sh` and exploratory/browser tests; files bug reports in `reports/qa-bug-report.md`. Report only; does not edit code or tests. |
| `security-implementer` | Builder (`readonly: false`) | `@security-implementer` or via `/orchestrate` | Remediates approved security findings (`SEC-BASE-NNN`, `SEC-HARD-NNN`) from security reviews using industry-standard defenses. Does not review own code or mutate cloud infra. |
| `security-reviewer-baseline` | Reviewer (`readonly: true`) | `@security-reviewer-baseline` or via `/orchestrate` | Audits diffs against `docs/threat-model.md`, security policies, and OWASP Top 10 for mandatory baseline controls (auth, input validation, encryption, egress DLP). Report only. |
| `security-reviewer-hardening` | Reviewer (`readonly: true`) | `@security-reviewer-hardening` or via `/orchestrate` | Performs deep defense-in-depth hardening audits across Application, Infrastructure, and Operations for Tier 2 (HIGH) / CRITICAL features and pre-production milestones. Report only. |
| `system-architect` | Architect / Planner (`readonly: false`) | `@system-architect` or via `/plan <slug>` | Translates accepted specifications into step-by-step technical implementation plans (`plan.md`) with explicit tasks (`TASK-NNN`), dependency order, and test gates. Does not plan CRITICAL features. |
| `teacher` | Educational (`readonly: true`) | `@teacher` | Explains codebase architecture, module interactions, data flows, and requirement artifacts (intent, experience, spec) in plain, structured language. Report only. |
| `technical-defensibility-analyst` | Analyst (`readonly: true`) | `@technical-defensibility-analyst` or via `/market-opportunity-research` | Evaluates technical feasibility, third-party API dependencies, data pipeline costs, compliance obstacles, and long-term architectural defensibility. Report only. |
| `tests-planner` | Planner (`readonly: true`) | `@tests-planner` or via `/orchestrate` | Plans high-signal unit, integration, contract, and mutation/adversarial tests in `test-plan.md` targeting real production failure modes; audits test suites for mock tautologies. Report only. |
| `tests-writer` | Builder (`readonly: false`) | `@tests-writer` or via `/orchestrate` | Implements deterministic unit, integration, contract, and mutation tests (`TST-NNN`) following TDD prior to or alongside implementation. Does not modify application code. |
| `ui-ux-designer` | Designer (`readonly: false`) | `@ui-ux-designer` or via `/orchestrate` | Designs UI screens, component hierarchies, design tokens, and interaction states in Figma (via Figma MCP) and writes `figma-design-spec.md`. Does not write frontend code or review UX. |
| `ui-ux-researcher` | Researcher (`readonly: true`) | `@ui-ux-researcher` or via `/orchestrate` | Researches industry UI patterns, competitor interaction workflows, usability benchmarks, and user feedback before experience design. Report only. |
| `unit-economics-analyst` | Analyst (`readonly: true`) | `@unit-economics-analyst` or via `/market-opportunity-research` | Models pricing strategies, cost of goods sold (COGS: compute, storage, third-party APIs), gross margins, and customer lifetime value (LTV:CAC) scenarios. Report only. |
| `ux-reviewer` | Reviewer (`readonly: true`) | `@ux-reviewer` or via `/orchestrate` | Reviews draft experiences, Figma design specs, and implemented UI against accepted UX criteria (`UX-NNN`) focusing on interaction gaps and accessibility. Report only. |

---

## Skills

Skills are modular capability and policy playbooks located in `.cursor/skills/`. They either guide multi-step workflows or enforce engineering invariants.

### Invocation Mechanisms
- **Workflow Slash Commands (`disable-model-invocation: true`)**: Explicitly triggered by typing `/<skill-name> [args]` in chat. These manage lifecycle transitions, artifact authoring, and human approval gates.
- **Automatic / Model-Invoked (`disable-model-invocation: false`)**: Implicitly triggered by the AI model whenever conversation context, tasks, or file changes match the skill's domain (e.g., modifying database queries auto-activates `database`). Can also be invoked explicitly via `/<skill-name>`.

| Skill | Type | How Invoked | Functionality & Trigger Context |
| :--- | :--- | :--- | :--- |
| `add-dependency` | Workflow | `/<skill-name>` | Justifies a new runtime or development dependency, verifies package registry reputation, and installs only upon explicit human approval. |
| `ai-engineering` | Constitution | Automatic / `/<skill-name>` | Auto-applies when integrating AI models, LLM APIs, prompt engineering, RAG pipelines, or autonomous agent tool execution to enforce cost caps, schema validation, injection defense, and fallbacks. |
| `api` | Constitution | Automatic / `/<skill-name>` | Auto-applies when adding or modifying REST/RPC endpoints, queries, mutations, domain events, webhooks, or public API contracts (enforces idempotency, versioning, input validation). |
| `architecture-gate` | Workflow | `/<skill-name> <slug>` | Evaluates architecture risk checklist for Tier 2 (HIGH) changes before planning and recommends PASS or FAIL in `architecture-gate.md`; stops for human acceptance. |
| `architecture` | Constitution | Automatic / `/<skill-name>` | Auto-applies when designing modules, defining service/domain boundaries, routing data flow, or establishing structural abstraction and error-handling rules. |
| `billing-webhooks` | Constitution | Automatic / `/<skill-name>` | Auto-applies when designing or implementing payment providers (Stripe, LemonSqueezy), subscriptions, webhook handlers (signature verification, idempotency), and entitlement gating. |
| `bootstrap-stack` | Workflow | `/<skill-name>` | Scaffolds the walking skeleton, wires build/test/lint/CI pipelines, and activates baseline architecture policies based on an accepted `docs/adr/0001-stack.md`. |
| `database` | Constitution | Automatic / `/<skill-name>` | Auto-applies when altering schemas, queries, migrations, transactions, connection pools, retention, backups, or data boundaries (enforces expand-migrate-contract). |
| `design-experience` | Workflow | `/<skill-name> <slug>` | Drafts user journey, UI states (empty, loading, error, success), wireframe specs, trade-offs, and testable `UX-NNN` acceptance criteria in `experience.md` for human approval. |
| `doc-sync` | Workflow | `/<skill-name>` | Audits code diffs against documentation and synchronizes architecture docs, runbooks, and ADRs based on the plan's Docs Impact section. |
| `doubt-driven-development` | Epistemic | Automatic / `/<skill-name>` | Auto-applies during high-stakes architectural, security, concurrency, data migration, or unfamiliar codebase work to force an adversarial self-check before committing changes. |
| `fast-track` | Workflow | `/<skill-name> <slug>` | Single-pass discovery, UX experience, spec, and plan generator for low/medium-risk (Tier 0 and Tier 1) changes, pausing at one unified human acceptance gate before coding. |
| `fix-bug` | Workflow | `/<skill-name>` | Governs bug fixes: reproduces bug with a failing test first, freezes test files with `.cursor/.fix-mode`, and fixes implementation code only. |
| `frontend-ui-engineering` | Constitution | Automatic / `/<skill-name>` | Auto-applies when building or modifying frontend UI components, styles, forms, or client state (enforces semantic design tokens, <200-line components, 7 UX states, WCAG 2.1 AA, responsive layouts). |
| `market-opportunity-research` | Workflow | `/<skill-name>` | Coordinates the 6 discovery analyst agents to assess market demand, competitive landscape, commercial viability, and differentiation for product ideas. |
| `new-intent` | Workflow | `/<skill-name>` | Kicks off product discovery for an idea: captures problem statement, user persona, evidence, Must/Should/Later scope, and writes draft `intent/<slug>/intent.md`. |
| `observability` | Constitution | Automatic / `/<skill-name>` | Auto-applies when changing production behavior, APIs, background jobs, logs, metrics (RED/USE), distributed traces, alert rules, or operational dashboards. |
| `operations` | Constitution | Automatic / `/<skill-name>` | Auto-applies when writing CI/CD pipelines, container specs, cloud/edge infrastructure, release/rollback scripts, feature flags, or runbooks. |
| `orchestrate` | Workflow | `/<skill-name> <slug>` | Analyzes risk tier and git diff, sequences the next required agent role (planner, builder, verifier, reviewer), and maintains the audit trail in `orchestration.md`. |
| `plan` | Workflow | `/<skill-name> <slug>` | Used in Plan mode to translate an accepted `spec.md` into `plan.md` with numbered tasks (`TASK-NNN`), dependency ordering, test criteria, and docs impact. |
| `project-brief` | Workflow | `/<skill-name>` | Conducts a structured discovery interview to produce `docs/product-brief.md`, `docs/nfr.md`, architecture stubs, and initial proposed stack ADR `docs/adr/0001-stack.md`. |
| `record-debt` | Workflow | `/<skill-name>` | Writes a standardized technical debt record to `docs/debt/` to document known compromises, shortcuts, and deferred work with remediation triggers. |
| `record-decision` | Workflow | `/<skill-name>` | Drafts the next numbered Architecture Decision Record (`docs/adr/NNNN-title.md`) capturing context, options considered, decision, and consequences. |
| `resilience` | Constitution | Automatic / `/<skill-name>` | Auto-applies when designing network, queue, cache, or external service interactions to enforce circuit breakers, timeouts, exponential backoff, DLQs, and graceful shutdown. |
| `security-audit` | Workflow | `/<skill-name> [<slug>]` | Executes a multi-phase adversarial security audit combining Cloudflare `coverage-ledger.json` reconnaissance, coverage-led hunting, and Trail of Bits disprover false-positive gates. |
| `security-baseline` | Constitution | Automatic / `/<skill-name>` | Auto-applies when creating or modifying endpoints, authentication, sensitive data handling, external API calls, configuration, or dependencies. |
| `security-sharp-edges` | Constitution | Automatic / `/<skill-name>` | Auto-applies when designing configuration parsers, auth helpers, SQL/ORM queries, multi-tenant contexts, cryptography, or internal APIs to eliminate fail-open defaults. |
| `ship-check` | Workflow | `/<skill-name> [<slug>]` | Pre-merge release readiness gate: executes deterministic checks (`verify.sh`), differential security blast-radius analysis, migration/rollback safety audits, and doc sync. |
| `source-driven-development` | Epistemic | Automatic / `/<skill-name>` | Auto-applies when writing calls to frameworks, libraries, CLIs, or runtime APIs, requiring direct citation and consultation of official documentation over memory. |
| `spec` | Workflow | `/<skill-name> <slug>` | Converts an accepted `intent.md` (and `experience.md`) into falsifiable technical requirements (`spec.md`) with Gherkin-style Given/When/Then criteria (`REQ-NNN`). |
| `tdd` | Practice | Automatic / `/<skill-name>` | Auto-applies when building features or fixing bugs test-first ("red-green-refactor", integration testing, contract tests). |
| `testing` | Constitution | Automatic / `/<skill-name>` | Auto-applies when writing or modifying tests, browser UI flows, invariant property-based tests, or implementing behavior requiring verification. |
| `verification-before-completion` | Epistemic | Automatic / `/<skill-name>` | Auto-applies before reporting any task, bugfix, build, or check complete, requiring fresh execution output evidence before completion claims. |
