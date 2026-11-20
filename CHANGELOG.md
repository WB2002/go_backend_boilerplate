# Changelog

All notable changes to this project are documented in this file.

The format is based on Keep a Changelog.

## [Unreleased]

### Added
- Complete metadata tables in root `README.md` indexing all 33 specialized agents and 33 skills with functionality, access permissions, invocation patterns (slash commands vs. model-invoked triggers), and operational boundaries.
- `bootstrap-stack` (`/bootstrap-stack`) skill: Day-0 walking skeleton scaffolding, build/test/lint wiring, CI workflow population, and architecture policy activation from an accepted `docs/adr/0001-stack.md`.
- `resilience` constitution skill: Circuit breaking, network timeouts, jittered exponential backoff, poison-pill/DLQ routing, cache stampede prevention, distributed lock TTLs, and `SIGTERM` graceful draining.
- `ai-engineering` constitution skill: Structured output schema validation, bounded repair loops, per-user/tenant token and cost budgets, prompt injection boundaries, and deterministic fallbacks.
- `billing-webhooks` constitution skill: Cryptographic signature verification, idempotent event deduplication, out-of-order event tolerance, and server-side entitlement enforcement.
- `protect-status-accepted.sh` preToolUse hook: Mechanically prevents agents from setting `Status: accepted` on `intent/**/*.md` and `docs/adr/*.md`.
- `security-audit` (`/security-audit`) skill: Cloudflare's 6-phase adversarial security audit workflow (`coverage-ledger.json` reconnaissance, coverage-led hunting, disprover/Trail of Bits `fp-check` validation, `findings.json`, and verified reporting).
- `security-sharp-edges` skill: Trail of Bits `insecure-defaults` and `sharp-edges` guardrails banning fail-open defaults and enforcing misuse-resistant internal APIs (`TenantContext`).
- `frontend-ui-engineering` skill: semantic design tokens, <200-line component modularity, 7 UX states, WCAG 2.1 AA, and 4-breakpoint responsive engineering rules.
- `doubt-driven-development` and `verification-before-completion` epistemic skills with eval checks under `evals/cases/`.
- `fast-track` (`/fast-track`) skill for single-pass Tier 0/1 artifact drafting and `ship-check` (`/ship-check`) skill for pre-merge release readiness verification.
- 23 specialized SDLC sub-agents in `.cursor/agents/` (`ui-ux-researcher`, `ui-ux-designer`, `ux-reviewer`, `system-architect`, `frontend-engineer`, `backend-engineer`, `devops`, `infrastructure`, `qa`, `performance-reviewer`, `performance-implementer`, `security-reviewer-baseline`, `security-reviewer-hardening`, `security-implementer`, `database-reviewer`, `database-implementer`, `compliance-reviewer`, `compliance-implementer`, `teacher`, `observability`, `tests-planner`, `tests-writer`, `debugger`) with trigger-based delegation boundaries, minimal tools, structured checklists, and report templates under `intent/<slug>/reports/`.
- Matt Pocock engineering and productivity skills (`tdd`), pinned in `docs/third-party/mattpocock-skills.pin`, with local gates overriding commit, merge, self-review, and acceptance.
- `source-driven-development` skill: cite current official documentation before writing a library or runtime call.
- Root `README.md` and `scripts/check-skills.sh` for skill frontmatter, folder names, and relative references.
- Constitution skills, architecture gate, review agents, policy declarations, debt ledger, and engineering-memory templates for the Phase 1 control plane.
- Orchestrator, lifecycle stages, risk classes (LOW, MEDIUM, HIGH, CRITICAL), and autonomy levels L0–L5 for the Phase 2 control plane.
- How to use this setup, in `docs/ai-sdlc.md`.
- Product discovery fields for users, evidence, Must/Should/Later scope,
  assumptions, validation, and product risks in intent creation.
- `design-experience` skill and experience template for user journeys,
  interaction states, wireframes, trade-offs, and UX acceptance criteria.
- Readonly `ux-reviewer` for failure-focused review before experience acceptance
  and after verification.
- UX traceability from accepted `UX-NNN` criteria into specs and plans, with
  `Experience: none` for non-interactive work.
### Changed
- Hardened `production-gate.sh` to intercept all mutating cloud, IaC, deployment, publishing, and migration CLI commands (`terraform`, `tofu`, `kubectl`, `helm`, `wrangler`, `fly`, `vercel`, `npm/cargo publish`, `gh release`, `prisma/supabase/alembic`).
- Enhanced `manifest-guard.sh` to permit manifest and script updates when `.cursor/.guardrails-edit` is explicitly enabled by the human owner.
- Optimized `/orchestrate` and `orchestrator` with a diff-triggered, tier-proportional review matrix, eliminating redundant 10-agent sequential review chains on low/medium-risk tasks.
- Upgraded the 7 constitution skills (`architecture`, `api`, `database`, `security-baseline`, `testing`, `observability`, `operations`) in-place into tiered `Invariants / Defaults / Decisions / Exceptions` playbooks incorporating Cloudflare Zero-Trust egress/DLP controls, Anthropic Playwright browser E2E verification, and Trail of Bits property-based invariant testing.
- Enhanced `security-reviewer-baseline` and `security-reviewer-hardening` with coverage-ledger accounting and adversarial disprover false-positive validation.
### Deprecated
### Removed
- 18 non-essential or overlapping skills: 13 vendored Matt Pocock skills (`wizard`, `prototype`, `writing-for-agents`, `retro`, `handoff`, `pr`, `grilling`, `grill-with-docs`, `domain-modeling`, `improve-codebase-architecture`, `research`, `diagnosing-bugs`, `codebase-design`) and 5 redundant first-party skills (`dependency-policy`, `incident-to-intent`, `debt-sweep`, `code-simplification`, `constraint-driven-development`).
- 13 overlapping or low-ROI vendored Matt Pocock skills (`setup-matt-pocock-skills`, `to-spec`, `to-tickets`, `implement`, `implement-spec`, `triage`, `wayfinder`, `ask-matt`, `grill-me`, `code-review`, `teach`, `to-questionnaire`, `wait-what`) that competed with the `intent/<slug>/` control-plane SDLC.
- Superseded legacy agents in `.cursor/agents/` (`architect`, `doc-auditor`, `plan-auditor`, `security-reviewer`, `test-engineer`, `verifier`).
### Fixed
### Security
