# AGENTS.md

## Project
Cursor AI-SDLC configuration workspace. No application source or package manifest.

## Commands
- Build: TODO: no package.json, Makefile, pyproject.toml, or similar
- Test: TODO: no test runner found
- Lint: TODO: no linter config found
- Run: TODO: no run command found

## Verifying your work
Run `scripts/verify.sh` before reporting any task complete and paste its output. If a test fails, fix the code, never the test. Never skip or delete a failing test.

## Conventions
- No application conventions yet. The tree started as an empty `AGENTS.md` and an empty `.agents/skills/`.

## Things the agent gets wrong
- (add a line here whenever the same mistake happens twice)

## SDLC workflow
- Initial project setup starts with `/project-brief` to produce `docs/adr/0001-stack.md`. Once accepted, `/bootstrap-stack` (with `.cursor/.guardrails-edit`) scaffolds the walking skeleton and configures build, test, lint, and CI.
- Work starts with product discovery in `intent/<slug>/intent.md`.
- A human accepts the product scope in `intent.md`.
- For changes to information a user sees or actions a user takes,
  `/design-experience <slug>` produces a draft `experience.md`; `ux-reviewer`
  reports gaps before the human accepts it.
- `/spec <slug>` produces `spec.md` after the intent and, when applicable, the
  experience are accepted. Non-interactive requirements use `Experience: none`.
- `/plan <slug>` (in Plan mode) produces `plan.md`. HIGH (tier 2) requires an accepted `intent/<slug>/architecture-gate.md` first. CRITICAL: the human writes the plan.
- For LOW (tier 0) and MEDIUM (tier 1) changes, `/fast-track <slug>` drafts `intent.md`, `experience.md` (when visible), `spec.md`, and `plan.md` in one pass and stops at a single human acceptance gate.
- `/orchestrate <slug>` names the next role and writes `intent/<slug>/orchestration.md`. It does not implement, review, merge, or deploy.
- `/security-audit [<slug>]` runs Cloudflare's multi-phase `coverage-ledger.json` + adversarial disprover security audit. `/ship-check [<slug>]` runs the pre-merge release readiness gate.
- Implementation follows the plan and updates `plan.md` in the same change if it departs.
- Bug fixes use `/fix-bug`.
- Imported Matt Pocock skills do not bypass human acceptance. They live in `.cursor/skills/` beside the control-plane skills. `/spec`, `/plan`, `/record-decision`, and `/fix-bug` stay authoritative where they overlap.

## Correctness
- AI generates implementation. Intent, accepted experience, spec, tests, and policies define what is correct. Agents may prepare evidence and must not manufacture an approval.
- Default autonomy is L2. Do not merge or deploy.
- Do not review your own change. Hand it to the readonly review agents.
- Do not set Status to `accepted`. Only the human owner does.
- Do not invent product scope or visible interaction behavior in a spec, plan,
  or implementation. Stop at the missing human gate.
- A HIGH (tier 2) plan requires an accepted architecture gate. CRITICAL design and plan stay with the human.

## Engineering principles
- Search for existing utilities before writing new ones. Reuse; do not duplicate.
- No drive-by refactors or unrelated edits. Out-of-scope findings become an intent, not part of the diff.
- No abstraction until there are two real callers. Prefer deleting code to adding it.
- No commented-out code, no dead code, no `TODO` without an intent/issue reference.
- Dependencies only via `/add-dependency`. Never hand-edit manifests or lockfiles.
- Every behavior change has a test that fails without it. Never weaken or delete a test to get green.
- Public behavior change means docs updated in the same change (see plan.md Docs impact).
- Stay inside plan.md scope. If scope must grow, stop and update the plan first.
- Tier 2 changes: stop for human design review before implementing, and do not write `plan.md` until the architecture gate is accepted.
