# Intent: Production engineering system

Author: · Status: Accepted · Date: 2026-11-02  
Lifecycle: Accepted

## Problem

Delivery speed comes from not re-deciding standards, quality gates, architecture, and operations on every project. Those decisions are not captured as executable playbooks. The current skills name a rule and stop. They do not say what to run, which rule is mandatory and which is a default, or which skill wins when two apply.

## User and evidence

Primary user: the owner, delivering healthcare B2B systems and Upwork projects, and building the ability to take a system from requirements to production operations alone.
Job to be done: deliver in days what takes others weeks, without giving up correctness, security, or maintainability.
Current workaround: ad-hoc agent instructions plus short constitution skills.
Evidence: user-reported

## Proposed outcome

Playbooks that make the correct engineering decision easier for an agent to carry out, and make the wrong decision harder to get through verification.

Conceptual layers. These keep responsibilities separate. They are not five subsystems to build now:

1. Task understanding: requirements, constraints, acceptance criteria.
2. Skill orchestration: selection, dependencies, precedence.
3. Engineering playbooks: productivity, application, security, database, infrastructure.
4. Independent verification: structural, behavioral, security, adversarial.
5. Evidence and release decision: test results, verification evidence, unresolved risks.

## Why now

Healthcare B2B, client delivery, and production ownership depend on the same gates. Later AI, RAG, HIPAA, and GDPR work attaches to them.

## Scope

Must:

- Playbooks live in `.cursor/skills/<domain>-<name>/`, flat, with the domain as the name prefix.
- Domains in this milestone: `productivity-`, `application-`, `security-`, `database-`, `infrastructure-`. Scope is frozen to these five.
- Each playbook separates mandatory invariants, recommended defaults, context-dependent decisions, and exceptions that need a written justification. A context-dependent choice is never written as a universal rule.
- Each playbook names the playbooks it depends on.
- One routing playbook selects the skills that apply to a task, expands their dependencies, applies a written precedence order, and stops on an unresolved conflict instead of picking a side.
- Two kinds of verification, reported separately:
  - Structural: the playbook exists and meets its contract.
  - Behavioral: an agent given a realistic task follows the playbook where it did not before. Behavioral verification is mandatory in this milestone for discipline playbooks and for the routing playbook.
- An agent that wrote a change cannot mark its playbook complete. A behavioral result is graded by someone other than the agent that ran the task.
- Existing production-grade skills keep their content and identity. Routing may reference them.
- Thin skills keep their names and contain only pointers to the authoritative playbooks. They do not repeat playbook instructions.
- One shared checker and one eval case format. No other eval infrastructure until a real need shows up.

Behavioral evaluation integrity:

- Red and green runs use the same prompt, the same starting repository state apart from the playbook under test, the pinned model `grok-4.7-high-fast`, and the same tool permissions. A model family name is not a version. That slug is recorded in every baseline.
- Each baseline records that model version, the execution conditions, both outputs, and the grading evidence.
- Each behavioral case has an explicit rubric. Every rubric item cites the playbook sentence it is derived from. The grader does not receive those citations.
- The blind grader receives the anonymized outputs, the rubric, and only the minimum neutral task context required to assess them. The original execution prompt, the run labels, and the playbook condition are withheld. The grader must not be able to infer which output came from which run.
- A rubric item is pass, fail, or ambiguous. An ambiguous mark names the evidence and why the expected outcome cannot be determined. Any ambiguous item makes the case inconclusive. Inconclusive does not meet the behavioral bar. Resolve that ambiguity, then rerun. Disagreement alone is not ambiguity. A grade has to rest on the rubric.
- The agent that authored a playbook cannot approve it. The agent that executed a behavioral run cannot grade its own result.

Routing integrity:

- The dependency graph is acyclic.
- Dependency resolution and precedence resolution are separate operations, recorded separately.
- An unresolved conflict produces the conflicting instructions, their sources, and the decision required. The router does not choose a side.
- The routing behavioral case exercises dependency expansion, precedence, and a seeded unresolved conflict.
- The routing case is graded blind first. When a routing item is ambiguous, the owner adjudicates, and the owner is not the runner of either output. Adjudication resolves the evaluation. It does not add a routing rule after seeing the result. If the routing specification is incomplete, fix the specification first, then rerun.

Structural verification:

- The shared checker names the exact missing or invalid playbook in every failure.
- The checker's missing-SKILL.md behavior is tested in temporary fixtures, without modifying the actual repository to simulate absence.

Should:

- A behavioral end-to-end case: a healthcare provider integration with persistence and an authenticated endpoint, routed through the playbooks.

Later:

- Behavioral evals for reference playbooks. The first ones cover application integration, database integrity, and security validation.
- `ai-` playbooks, including RAG, evaluation, model serving, and on-premises deployment.
- `healthcare-` playbooks, including HIPAA, GDPR, data governance, and audit trails.
- `review-` playbooks that only verify.
- An automated eval runner. `evals/run.sh` stays as it is.

Explicit exclusions:

- No product application in this repo.
- No claim that an organization is HIPAA or GDPR compliant.
- No replacement of `/spec`, `/plan`, `/record-decision`, or `/fix-bug`.
- No merge and no deploy.

## Outcome

Behavioral success metric: For every behavioral playbook, a blind grader, who is neither the author nor a runner, scores two anonymized replies against the case rubric. The reply from the run without the playbook fails at least one item. The reply from the run with the playbook passes every item. An ambiguous item makes the case inconclusive, and inconclusive does not meet this bar. Both runs use the same prompt, starting state, pinned model `grok-4.7-high-fast`, and permissions. For the routing case, the rubric covers dependency expansion, precedence, and the seeded conflict. An ambiguous routing item is adjudicated by the owner, who did not run either reply, and the specification is fixed before a rerun.
Structural check (not evidence of behavior): `scripts/check-playbooks.sh` passes, and `scripts/test-check-playbooks.sh` shows in fixtures that it fails for a missing or invalid playbook.
Product risks: a playbook that passes structure and does not change what an agent does. Behavioral grading exists for that. A second risk is evaluation machinery that costs more to maintain than the playbooks. The single-checker limit exists for that.
Assumptions requiring validation: reference playbooks change agent behavior without their own behavioral evals. This is untested until those evals are added. The first evals cover application integration, database integrity, and security validation.

## Affected systems

`.cursor/skills/`, `scripts/check-skills.sh`, `evals/cases/`, `docs/playbooks/`, `docs/ai-sdlc.md`, `AGENTS.md`, `CHANGELOG.md`.

## Constraints

- Experience: none. This changes agent instructions and checks, not a screen a user operates.
- Do not set this Status to accepted. That is a human action.
- `scripts/verify.sh` stays the failing configure-build stub until `docs/adr/0001-stack.md` is accepted.
- Implementation waits until this intent is accepted and implementation is explicitly requested.

## Open questions

None.
