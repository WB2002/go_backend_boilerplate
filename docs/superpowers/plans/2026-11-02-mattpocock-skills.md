# Matt Pocock Skills Import Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Vendor Matt Pocock's published engineering and productivity skills into `.cursor/skills/` at a pinned commit, with the local AI-SDLC gates winning wherever a skill would commit, merge, self-review, or mark work accepted.

**Architecture:** Copy the skill folders from `mattpocock/skills` at commit `4ae2317ddb22b046e26455e4f9c9b3aaaf62212d` (package version 1.3.2) into `.cursor/skills/<name>/`, drop Codex-only `agents/openai.yaml` files, and insert one shared constraint block after each skill's frontmatter. Skills whose later steps contradict those constraints get an exact text replacement in the same task as the copy, so a forbidden instruction never lands alone. `scripts/check-skills.sh` keeps validating frontmatter and relative links. A new `scripts/check-mattpocock-skills.sh` checks the pin, the MIT notice, the manifest, and the guard strings. This config repo is the skill source. Do not run `/setup-matt-pocock-skills` here and do not create a product issue tracker for it.

**Tech Stack:** Markdown skills, bash checkers, a pinned git checkout of https://github.com/mattpocock/skills (MIT).

## Global Constraints

- Skill directory name equals frontmatter `name`. `scripts/check-skills.sh` enforces this.
- Every `SKILL.md` has a `description`.
- A relative markdown link in `SKILL.md` must resolve to a file next to that skill. `http:`, `https:`, `mailto:`, and `#...` links are ignored.
- User-invoked skills keep `disable-model-invocation: true`. Model-invoked skills keep that flag absent.
- Do not set Status to `accepted`. Only the human owner does.
- Do not set lifecycle to `APPROVED`, `MERGED`, or `CANARY`.
- Do not review your own change. Readonly reviewers report failures first.
- Do not merge or deploy.
- Do not commit, push, or open a pull request unless the user explicitly asks in that turn.
- Do not invent product scope or visible interaction behavior. Stop at the missing human gate.
- `/spec` writes `intent/<slug>/spec.md` from an accepted intent. A draft from `/to-spec` does not replace it.
- ADRs go through `/record-decision` and `docs/adr/0000-template.md`. Leave Status `proposed`.
- Never weaken or delete a test to get green. If `.cursor/.fix-mode` exists, do not edit test files.
- Do not edit `scripts/verify.sh`, `scripts/verify-full.sh`, or `.github/` unless the user has created `.cursor/.guardrails-edit`.
- `scripts/verify.sh` stays a failing TODO until `docs/adr/0001-stack.md` is accepted. Proof for this plan is `scripts/check-skills.sh`. Paste the `verify.sh` TODO failure and do not change that script.
- CI `pr-size` fails a pull request above 400 changed lines unless it has the `large-change-approved` label. Do not raise that limit. This import is larger than 400 lines. The human adds the label.
- Do not overwrite an existing `.cursor/skills/<name>/` directory unless that name is already in `scripts/mattpocock-skill-manifest.txt`.
- Do not import `skills/in-progress/`, `skills/deprecated/`, or `skills/misc/`.
- MIT notice required: `Copyright (c) 2026 Matt Pocock`, kept in `docs/third-party/mattpocock-skills-LICENSE`.

## File structure

Created:

- `docs/third-party/mattpocock-skills.pin` — one line, the pinned SHA.
- `docs/third-party/mattpocock-skills-LICENSE` — upstream MIT license text.
- `.cursor/skills/_overlay/setup-constraints.md` — the block inserted into every imported skill. Not itself a skill (no `SKILL.md`).
- `scripts/check-mattpocock-skills.sh` — pin, license, manifest, guards, excluded names.
- `scripts/mattpocock-skill-manifest.txt` — one imported skill name per line.
- `scripts/mattpocock-skill-guards.txt` — `absent|name|needle` or `present|name|needle` lines.
- `scripts/import-mattpocock-skill.sh` — copies one skill from the pin and inserts the constraint block.
- `.cursor/skills/<name>/` — the 27 skills listed in the coverage table.

Modified:

- `scripts/check-skills.sh` — calls the new checker after its own pass.
- `docs/ai-sdlc.md` — lists the imported commands and the gates they do not bypass.
- `AGENTS.md` — one SDLC bullet that the imported skills do not bypass the gates.
- `CHANGELOG.md` — Unreleased / Added entry.

Left alone:

- Every existing skill under `.cursor/skills/` (none share a name with an imported skill).
- `scripts/verify.sh`, `scripts/verify-full.sh`, `.github/workflows/ci.yml`.
- `docs/agents/` is not created. Product repos run `/setup-matt-pocock-skills` themselves.

## Coverage

Import these 27. The group is the upstream directory.

| Skill | Group | Invocation | Local overlap the patch keeps authoritative |
| --- | --- | --- | --- |
| ask-matt | engineering | user | Router must send gates to `/spec`, `/record-decision`, and a separate review |
| grill-with-docs | engineering | user | Calls `grilling` and `domain-modeling`; does not accept artifacts |
| triage | engineering | user | May label; must not merge |
| improve-codebase-architecture | engineering | user | Survey only; `/debt-sweep` records debt |
| setup-matt-pocock-skills | engineering | user | Must not rewrite the SDLC section of `AGENTS.md` |
| to-spec | engineering | user | Draft only; `/spec` stays the accepted-intent path |
| to-tickets | engineering | user | Tickets stay `draft`, not `ready-for-agent` |
| implement | engineering | user | No commit, no self-review |
| implement-spec | engineering | user | One ticket, then stop; no merge and no self-review |
| wayfinder | engineering | user | No push; `/plan` still plans an accepted spec |
| retro | engineering | user | Suggest only; do not edit guardrail files |
| prototype | engineering | model | No commit unless the user asks |
| diagnosing-bugs | engineering | model | `/fix-bug` owns a single frozen failing test |
| research | engineering | model | Write `docs/research/<slug>.md` |
| tdd | engineering | model | `/testing` and `/fix-bug`; never weaken a test |
| domain-modeling | engineering | model | `/record-decision`; Status stays `proposed` |
| codebase-design | engineering | model | Vocabulary only; `/architecture` still applies |
| code-review | engineering | model | Report only; author does not run it on their own diff |
| pr | engineering | model | Body shape only until the user asks for a PR |
| wizard | engineering | model | Ask before committing the generated script |
| grill-me | productivity | user | Stateless interview |
| handoff | productivity | user | Writes a handoff document |
| teach | productivity | user | Teaching workspace |
| to-questionnaire | productivity | user | Writes a questionnaire |
| wait-what | productivity | user | Re-explains the last message |
| grilling | productivity | model | Interview primitive; does not act before confirmation |
| writing-for-agents | productivity | model | Skill files still match this repo's checker |

Do not import:

| Name | Why |
| --- | --- |
| chief-of-staff, claude-handoff, loop-me, setup-ts-deep-modules, software-factory, writing-beats, writing-fragments, writing-shape | Under `skills/in-progress/`. Not published. |
| git-guardrails-claude-code | Claude Code hooks. This repo already has its own hooks. |
| migrate-to-shoehorn | Test-library migration for a package this repo does not use. |
| scaffold-exercises | Course exercise layout, not this config repo. |
| setup-pre-commit | Adds Husky and edits a package manifest. Dependencies here go through `/add-dependency`. |

---

### Task 1: Pin, license, checker, importer

**Files:**

- Create: `docs/third-party/mattpocock-skills.pin`
- Create: `docs/third-party/mattpocock-skills-LICENSE`
- Create: `.cursor/skills/_overlay/setup-constraints.md`
- Create: `scripts/check-mattpocock-skills.sh`
- Create: `scripts/mattpocock-skill-manifest.txt`
- Create: `scripts/mattpocock-skill-guards.txt`
- Create: `scripts/import-mattpocock-skill.sh`
- Modify: `scripts/check-skills.sh`
- Test: `scripts/check-mattpocock-skills.sh`

**Interfaces:**

- Consumes: nothing from earlier tasks.
- Produces: `scripts/import-mattpocock-skill.sh <engineering|productivity> <skill-name>` copies that skill, refuses an unknown destination collision, and inserts the constraint block. `scripts/mattpocock-skill-manifest.txt` is the list of imported names. `scripts/mattpocock-skill-guards.txt` lines are `absent|<skill>|<fixed text>` or `present|<skill>|<fixed text>`. Guards for a skill that is not on disk yet are skipped.

- [ ] **Step 1: Write the failing checker**

Create `scripts/check-mattpocock-skills.sh` with this exact content and `chmod +x` it:

```bash
#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")/.."

fail=0
pin_file=docs/third-party/mattpocock-skills.pin
license=docs/third-party/mattpocock-skills-LICENSE
expected_pin=4ae2317ddb22b046e26455e4f9c9b3aaaf62212d

if [[ ! -f "$pin_file" ]]; then
  echo "missing pin file: $pin_file"
  fail=1
elif ! grep -qx "$expected_pin" "$pin_file"; then
  echo "pin mismatch in $pin_file"
  fail=1
fi

if [[ ! -f "$license" ]] || ! grep -q "Copyright (c) 2026 Matt Pocock" "$license"; then
  echo "missing MIT notice: $license"
  fail=1
fi

if ! grep -q "Permission is hereby granted, free of charge" "$license" 2>/dev/null; then
  echo "license text incomplete: $license"
  fail=1
fi

if [[ ! -f .cursor/skills/_overlay/setup-constraints.md ]]; then
  echo "missing .cursor/skills/_overlay/setup-constraints.md"
  fail=1
fi

if [[ ! -f scripts/mattpocock-skill-manifest.txt ]]; then
  echo "missing scripts/mattpocock-skill-manifest.txt"
  fail=1
else
  while IFS= read -r name || [[ -n "${name:-}" ]]; do
    [[ -z "${name:-}" || "$name" == \#* ]] && continue
    skill=".cursor/skills/$name/SKILL.md"
    if [[ ! -f "$skill" ]]; then
      echo "missing skill: $name"
      fail=1
      continue
    fi
    if ! grep -q '^## Setup constraints$' "$skill"; then
      echo "missing setup constraints: $skill"
      fail=1
    fi
    got=$(awk '/^name: /{print $2; exit}' "$skill")
    if [[ "$got" != "$name" ]]; then
      echo "name mismatch: $skill has $got"
      fail=1
    fi
  done < scripts/mattpocock-skill-manifest.txt
fi

if [[ ! -f scripts/mattpocock-skill-guards.txt ]]; then
  echo "missing scripts/mattpocock-skill-guards.txt"
  fail=1
else
  while IFS= read -r line || [[ -n "${line:-}" ]]; do
    [[ -z "${line:-}" || "$line" == \#* ]] && continue
    kind=${line%%|*}
    rest=${line#*|}
    name=${rest%%|*}
    needle=${rest#*|}
    skill=".cursor/skills/$name/SKILL.md"
    [[ -f "$skill" ]] || continue
    if [[ "$kind" == absent ]]; then
      if grep -F -q -- "$needle" "$skill"; then
        echo "forbidden text in $name: $needle"
        fail=1
      fi
    elif [[ "$kind" == present ]]; then
      if ! grep -F -q -- "$needle" "$skill"; then
        echo "missing required text in $name: $needle"
        fail=1
      fi
    else
      echo "bad guard: $line"
      fail=1
    fi
  done < scripts/mattpocock-skill-guards.txt
fi

excluded=(
  chief-of-staff
  claude-handoff
  loop-me
  setup-ts-deep-modules
  software-factory
  writing-beats
  writing-fragments
  writing-shape
  git-guardrails-claude-code
  migrate-to-shoehorn
  scaffold-exercises
  setup-pre-commit
)
for name in "${excluded[@]}"; do
  if [[ -d ".cursor/skills/$name" ]]; then
    echo "excluded skill present: $name"
    fail=1
  fi
done

if find .cursor/skills -path '*/agents/openai.yaml' -print | grep -q .; then
  echo "codex agents/openai.yaml must not be copied"
  fail=1
fi

if [[ "$fail" -ne 0 ]]; then
  exit 1
fi
echo "mattpocock skills: OK"
```

- [ ] **Step 2: Run the checker to verify it fails**

Run: `scripts/check-mattpocock-skills.sh`

Expected: exit 1, and the output contains `missing pin file: docs/third-party/mattpocock-skills.pin`.

- [ ] **Step 3: Add the pin, the license, the overlay, and the empty lists**

Create `docs/third-party/mattpocock-skills.pin` with this single line and a trailing newline:

```text
4ae2317ddb22b046e26455e4f9c9b3aaaf62212d
```

Create `docs/third-party/mattpocock-skills-LICENSE` with this command, then confirm the file contains both `MIT License` and `Copyright (c) 2026 Matt Pocock`:

```bash
git clone --depth 1 https://github.com/mattpocock/skills.git /tmp/mattpocock-skills-license
git -C /tmp/mattpocock-skills-license fetch --depth 1 origin 4ae2317ddb22b046e26455e4f9c9b3aaaf62212d
git -C /tmp/mattpocock-skills-license checkout --detach 4ae2317ddb22b046e26455e4f9c9b3aaaf62212d
mkdir -p docs/third-party
cp /tmp/mattpocock-skills-license/LICENSE docs/third-party/mattpocock-skills-LICENSE
```

The copied file must contain both of these lines:

```text
MIT License

Copyright (c) 2026 Matt Pocock
```

and the line `Permission is hereby granted, free of charge, to any person obtaining a copy`.

Create `.cursor/skills/_overlay/setup-constraints.md`:

```markdown
## Setup constraints

These rules override the rest of this skill when they conflict.

- Do not set Status to `accepted`. Only the human owner does.
- Do not set lifecycle to `APPROVED`, `MERGED`, or `CANARY`.
- Do not review your own change. Readonly reviewers report failures first.
- Do not merge or deploy.
- Do not commit, push, or open a pull request unless the user explicitly asks in this turn.
- Do not invent product scope or visible interaction behavior. Stop at the missing human gate.
- `/spec` writes `intent/<slug>/spec.md` from an accepted intent. Do not replace that path or treat a draft as accepted.
- ADRs go through `/record-decision` and `docs/adr/0000-template.md`. Leave Status `proposed`.
- Never weaken or delete a test to get green. If `.cursor/.fix-mode` exists, do not edit test files.
- Out-of-scope findings become an intent or a debt record, not part of the current diff.
- Do not edit `scripts/verify.sh`, `scripts/verify-full.sh`, or `.github/` unless the user has created `.cursor/.guardrails-edit`.
```

Create `scripts/mattpocock-skill-manifest.txt` and `scripts/mattpocock-skill-guards.txt` as empty files.

Create `scripts/import-mattpocock-skill.sh`, `chmod +x` it:

```bash
#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")/.."

if [[ $# -ne 2 ]]; then
  echo "usage: scripts/import-mattpocock-skill.sh <engineering|productivity> <skill-name>" >&2
  exit 2
fi

group="$1"
name="$2"
pin=4ae2317ddb22b046e26455e4f9c9b3aaaf62212d
src=/tmp/mattpocock-skills
dest=".cursor/skills/$name"
overlay=.cursor/skills/_overlay/setup-constraints.md

case "$group" in
  engineering|productivity) ;;
  *) echo "group must be engineering or productivity" >&2; exit 2 ;;
esac

if [[ -d "$dest" ]] && ! grep -qx "$name" scripts/mattpocock-skill-manifest.txt; then
  echo "refusing to overwrite existing skill: $name" >&2
  exit 1
fi

if [[ ! -d "$src/.git" ]] || [[ "$(git -C "$src" rev-parse HEAD)" != "$pin" ]]; then
  rm -rf "$src"
  git clone --depth 1 https://github.com/mattpocock/skills.git "$src"
  git -C "$src" fetch --depth 1 origin "$pin"
  git -C "$src" checkout --detach "$pin"
fi

upstream="$src/skills/$group/$name"
if [[ ! -f "$upstream/SKILL.md" ]]; then
  echo "missing upstream skill: $upstream/SKILL.md" >&2
  exit 1
fi

rm -rf "$dest"
mkdir -p "$dest"
while IFS= read -r rel; do
  mkdir -p "$dest/$(dirname "$rel")"
  cp "$upstream/$rel" "$dest/$rel"
done < <(cd "$upstream" && find . -type f ! -path './agents/*' | sed 's|^\./||')

python3 - "$dest/SKILL.md" "$overlay" <<'PY'
import pathlib, sys
skill = pathlib.Path(sys.argv[1])
overlay = pathlib.Path(sys.argv[2]).read_text()
text = skill.read_text()
if "## Setup constraints" in text:
    sys.exit(0)
parts = text.split("---", 2)
if len(parts) < 3:
    raise SystemExit(f"no frontmatter: {skill}")
body = parts[2]
if not body.startswith("\n"):
    body = "\n" + body
skill.write_text("---" + parts[1] + "---\n\n" + overlay.strip() + "\n" + body)
PY
```

- [ ] **Step 4: Point the existing skill checker at the new one**

In `scripts/check-skills.sh`, replace the closing block:

```bash
if [[ "$fail" -ne 0 ]]; then
  exit 1
fi
echo "skills: OK"
```

with:

```bash
if [[ "$fail" -ne 0 ]]; then
  exit 1
fi
echo "skills: OK"
scripts/check-mattpocock-skills.sh
```

- [ ] **Step 5: Run the checkers**

Run: `scripts/check-skills.sh`

Expected:

```text
skills: OK
mattpocock skills: OK
```

- [ ] **Step 6: Commit**

```bash
git add docs/third-party/mattpocock-skills.pin \
  docs/third-party/mattpocock-skills-LICENSE \
  .cursor/skills/_overlay/setup-constraints.md \
  scripts/check-mattpocock-skills.sh \
  scripts/mattpocock-skill-manifest.txt \
  scripts/mattpocock-skill-guards.txt \
  scripts/import-mattpocock-skill.sh \
  scripts/check-skills.sh
git commit -m "$(cat <<'EOF'
Added a pinned checker for imported Matt Pocock skills.

EOF
)"
```

---

### Task 2: Productivity skills

**Files:**

- Create: `.cursor/skills/grill-me/`
- Create: `.cursor/skills/grilling/`
- Create: `.cursor/skills/handoff/`
- Create: `.cursor/skills/teach/`
- Create: `.cursor/skills/to-questionnaire/`
- Create: `.cursor/skills/wait-what/`
- Create: `.cursor/skills/writing-for-agents/`
- Modify: `.cursor/skills/writing-for-agents/SKILL.md`
- Modify: `scripts/mattpocock-skill-manifest.txt`
- Modify: `scripts/mattpocock-skill-guards.txt`
- Test: `scripts/check-skills.sh`

**Interfaces:**

- Consumes: `scripts/import-mattpocock-skill.sh productivity <name>` from Task 1.
- Produces: those seven skill names on disk, each with `## Setup constraints` after the frontmatter. `writing-for-agents` also contains the sentence `Skill folders under .cursor/skills/<name>/ must use name equal to the folder.`

- [ ] **Step 1: Add the guard before the skill exists, then import**

Append this line to `scripts/mattpocock-skill-guards.txt`:

```text
present|writing-for-agents|Skill folders under .cursor/skills/<name>/ must use name equal to the folder.
```

Run: `scripts/check-mattpocock-skills.sh`

Expected: `mattpocock skills: OK` (the guard is skipped until the skill file exists).

Import:

```bash
scripts/import-mattpocock-skill.sh productivity grill-me
scripts/import-mattpocock-skill.sh productivity grilling
scripts/import-mattpocock-skill.sh productivity handoff
scripts/import-mattpocock-skill.sh productivity teach
scripts/import-mattpocock-skill.sh productivity to-questionnaire
scripts/import-mattpocock-skill.sh productivity wait-what
scripts/import-mattpocock-skill.sh productivity writing-for-agents
```

Append these lines to `scripts/mattpocock-skill-manifest.txt`:

```text
grill-me
grilling
handoff
teach
to-questionnaire
wait-what
writing-for-agents
```

- [ ] **Step 2: Run the checker to verify the writing-for-agents guard fails**

Run: `scripts/check-mattpocock-skills.sh`

Expected: exit 1, and the output contains `missing required text in writing-for-agents`.

- [ ] **Step 3: Teach writing-for-agents this repo's skill rules**

Append this section to the end of `.cursor/skills/writing-for-agents/SKILL.md`:

```markdown

## This setup

Skill folders under .cursor/skills/<name>/ must use name equal to the folder. Every skill needs a description. A workflow skill keeps disable-model-invocation: true. Do not remove that flag from grill-me, handoff, teach, to-questionnaire, wait-what, or any other user-invoked skill. Do not set an ADR or intent Status to accepted.
```

- [ ] **Step 4: Run the checkers**

Run: `scripts/check-skills.sh`

Expected:

```text
skills: OK
mattpocock skills: OK
```

Confirm `teach` kept its sibling format files and no `agents/openai.yaml` was copied:

Run: `find .cursor/skills/teach .cursor/skills/writing-for-agents -type f | sort`

Expected includes:

```text
.cursor/skills/teach/MISSION-FORMAT.md
.cursor/skills/teach/LEARNING-RECORD-FORMAT.md
.cursor/skills/teach/RESOURCES-FORMAT.md
.cursor/skills/teach/GLOSSARY-FORMAT.md
.cursor/skills/writing-for-agents/SKILL-MECHANICS.md
```

Expected does not include `openai.yaml`.

- [ ] **Step 5: Commit**

```bash
git add .cursor/skills/grill-me .cursor/skills/grilling .cursor/skills/handoff \
  .cursor/skills/teach .cursor/skills/to-questionnaire .cursor/skills/wait-what \
  .cursor/skills/writing-for-agents \
  scripts/mattpocock-skill-manifest.txt scripts/mattpocock-skill-guards.txt
git commit -m "$(cat <<'EOF'
Added Matt Pocock productivity skills under the local skill checker.

EOF
)"
```

---

### Task 3: Survey, research, prototype, and wizard

**Files:**

- Create: `.cursor/skills/research/`
- Create: `.cursor/skills/retro/`
- Create: `.cursor/skills/prototype/`
- Create: `.cursor/skills/wizard/`
- Create: `.cursor/skills/codebase-design/`
- Create: `.cursor/skills/improve-codebase-architecture/`
- Modify: the `SKILL.md` files patched below
- Modify: `scripts/mattpocock-skill-manifest.txt`
- Modify: `scripts/mattpocock-skill-guards.txt`
- Test: `scripts/check-skills.sh`

**Interfaces:**

- Consumes: `scripts/import-mattpocock-skill.sh engineering <name>`.
- Produces: the six skills, with these required sentences present: `docs/research/<slug>.md`, `Do not edit scripts/verify.sh`, `Do not commit or push unless the user asks.`, `ask before committing`, `Do not implement a candidate in this session.` `prototype` must not still contain `commit it to a throwaway branch`. `wizard` must not still contain `commit it and link it from the README`.

- [ ] **Step 1: Record the guards, then import**

Append to `scripts/mattpocock-skill-guards.txt`:

```text
present|research|docs/research/<slug>.md
present|retro|Do not edit scripts/verify.sh
present|prototype|Do not commit or push unless the user asks.
absent|prototype|commit it to a throwaway branch
present|wizard|ask before committing
absent|wizard|commit it and link it from the README
present|improve-codebase-architecture|Do not implement a candidate in this session.
```

Import and append the names to `scripts/mattpocock-skill-manifest.txt`:

```bash
scripts/import-mattpocock-skill.sh engineering research
scripts/import-mattpocock-skill.sh engineering retro
scripts/import-mattpocock-skill.sh engineering prototype
scripts/import-mattpocock-skill.sh engineering wizard
scripts/import-mattpocock-skill.sh engineering codebase-design
scripts/import-mattpocock-skill.sh engineering improve-codebase-architecture
```

```text
research
retro
prototype
wizard
codebase-design
improve-codebase-architecture
```

- [ ] **Step 2: Run the checker to verify it fails**

Run: `scripts/check-mattpocock-skills.sh`

Expected: exit 1. The output contains `missing required text in research` and `forbidden text in prototype`.

- [ ] **Step 3: Patch the six behaviors**

In `.cursor/skills/research/SKILL.md`, replace:

```markdown
3. Save it where the repo already keeps such notes; match the existing convention, and if there is none, put it somewhere sensible and say where.
```

with:

```markdown
3. Save it to `docs/research/<slug>.md`. Create `docs/research/` if it is missing. Cite each claim's source. Do not set Status to `accepted`.
```

In `.cursor/skills/retro/SKILL.md`, replace:

```markdown
4. Present these candidates to the user, in order of severity.
```

with:

```markdown
4. Present these candidates to the user, in order of severity. Suggest only. Do not edit scripts/verify.sh, scripts/verify-full.sh, or .github/. Do not apply a suggestion in this session.
```

In `.cursor/skills/prototype/SKILL.md`, replace:

```markdown
6. **Capture it when done.** Fold any validated decision into the real code, then capture the prototype itself as a **primary source**: commit it to a throwaway branch, out of main, and leave a context pointer to that branch on the implementation issue. Capture the answer too (the verdict and the question it settled) in the issue or a commit. The main branch keeps only the validated decision.
```

with:

```markdown
6. **Capture it when done.** Fold any validated decision into the real code only when the user asks. Do not commit or push unless the user asks. When they ask, commit the prototype only on a throwaway branch, do not merge it, and do not put it on the default branch. Leave a context pointer to that branch on the implementation issue, and record the verdict there too.
```

In `.cursor/skills/wizard/SKILL.md`, replace:

```markdown
- Tell the user how to run it. If it's a repeatable setup path, commit it and link it from the README so the next person runs the script instead of asking an AI.
```

with:

```markdown
- Tell the user how to run it. If it's a repeatable setup path, show the script and ask before committing. Do not commit, push, or edit the README unless the user asks.
```

In `.cursor/skills/improve-codebase-architecture/SKILL.md`, immediately after the heading `# Improve Codebase Architecture`, insert:

```markdown

Do not implement a candidate in this session. Present the survey, then stop. A chosen candidate becomes its own intent. `/debt-sweep` records debt; this skill does not.
```

Leave `codebase-design` unchanged apart from the constraint block the importer already inserted. Its sibling files `DEEPENING.md` and `DESIGN-IT-TWICE.md` must remain, because `SKILL.md` links to them.

- [ ] **Step 4: Run the checkers**

Run: `scripts/check-skills.sh`

Expected:

```text
skills: OK
mattpocock skills: OK
```

Run: `test -f .cursor/skills/wizard/template.sh && test -f .cursor/skills/codebase-design/DEEPENING.md && echo siblings-ok`

Expected: `siblings-ok`

- [ ] **Step 5: Commit**

```bash
git add .cursor/skills/research .cursor/skills/retro .cursor/skills/prototype \
  .cursor/skills/wizard .cursor/skills/codebase-design \
  .cursor/skills/improve-codebase-architecture \
  scripts/mattpocock-skill-manifest.txt scripts/mattpocock-skill-guards.txt
git commit -m "$(cat <<'EOF'
Added Matt Pocock survey and prototype skills without auto-commit.

EOF
)"
```

---

### Task 4: Tests, diagnosis, and domain language

**Files:**

- Create: `.cursor/skills/tdd/`
- Create: `.cursor/skills/diagnosing-bugs/`
- Create: `.cursor/skills/domain-modeling/`
- Create: `.cursor/skills/grill-with-docs/`
- Modify: `scripts/mattpocock-skill-manifest.txt`
- Modify: `scripts/mattpocock-skill-guards.txt`
- Test: `scripts/check-skills.sh`

**Interfaces:**

- Consumes: the importer.
- Produces: `tdd` contains `Never weaken a test.` `diagnosing-bugs` contains `.cursor/skills/diagnosing-bugs/scripts/hitl-loop.template.sh` and does not contain the bare instruction `with \`scripts/hitl-loop.template.sh`.` domain-modeling`contains`Do not set Status to accepted. ``grill-with-docs` is the two-skill wrapper plus the constraint block.

- [ ] **Step 1: Record the guards, then import**

Append to `scripts/mattpocock-skill-guards.txt`:

```text
present|tdd|Never weaken a test.
present|diagnosing-bugs|.cursor/skills/diagnosing-bugs/scripts/hitl-loop.template.sh
absent|diagnosing-bugs|with `scripts/hitl-loop.template.sh`
absent|diagnosing-bugs|via `scripts/hitl-loop.template.sh`
present|domain-modeling|Do not set Status to accepted.
```

Import and append to the manifest:

```bash
scripts/import-mattpocock-skill.sh engineering tdd
scripts/import-mattpocock-skill.sh engineering diagnosing-bugs
scripts/import-mattpocock-skill.sh engineering domain-modeling
scripts/import-mattpocock-skill.sh engineering grill-with-docs
```

```text
tdd
diagnosing-bugs
domain-modeling
grill-with-docs
```

- [ ] **Step 2: Run the checker to verify it fails**

Run: `scripts/check-mattpocock-skills.sh`

Expected: exit 1. The output contains `missing required text in tdd` and `forbidden text in diagnosing-bugs`.

- [ ] **Step 3: Patch the test, diagnosis, and ADR rules**

In `.cursor/skills/tdd/SKILL.md`, add this bullet at the end of `## Rules of the loop`:

```markdown
- **Never weaken a test.** Do not change a failing test so the implementation passes. If `.cursor/.fix-mode` exists, do not edit test files. For a single reproduced bug, `/fix-bug` is the procedure.
```

In `.cursor/skills/diagnosing-bugs/SKILL.md`, replace both occurrences of ``scripts/hitl-loop.template.sh`` with ``.cursor/skills/diagnosing-bugs/scripts/hitl-loop.template.sh``.

There are two occurrences: the phrase `drive _them_ with \`scripts/hitl-loop.template.sh`and the checklist line`a human in the loop only via scripts/hitl-loop.template.sh`. After the replacement, the first reads` drive _them_ with .cursor/skills/diagnosing-bugs/scripts/hitl-loop.template.sh`.

Then, under `## Phase 5: Fix + regression test`, before step 1, insert:

```markdown
Do not change an existing test to make it pass. When `.cursor/.fix-mode` exists, do not edit test files. A single reproduced bug uses `/fix-bug` after the failing test is frozen.
```

In `.cursor/skills/domain-modeling/SKILL.md`, replace:

```markdown
If any of the three is missing, skip the ADR. Use the format in [ADR-FORMAT.md](./ADR-FORMAT.md).
```

with:

```markdown
If any of the three is missing, skip the ADR. Use the format in [ADR-FORMAT.md](./ADR-FORMAT.md). In this setup, write the ADR through `/record-decision` and `docs/adr/0000-template.md`. Do not set Status to accepted.
```

In `.cursor/skills/domain-modeling/ADR-FORMAT.md`, under the optional **Status** bullet, add this sentence:

```markdown
An agent must not set Status to `accepted`. The human owner does that.
```

Leave `grill-with-docs/SKILL.md` as imported. It only calls `grilling` and `domain-modeling`.

- [ ] **Step 4: Run the checkers**

Run: `scripts/check-skills.sh`

Expected:

```text
skills: OK
mattpocock skills: OK
```

Run: `test -f .cursor/skills/tdd/tests.md && test -f .cursor/skills/tdd/mocking.md && test -x .cursor/skills/diagnosing-bugs/scripts/hitl-loop.template.sh && echo tdd-diag-ok`

Expected: `tdd-diag-ok`

The template is copied from upstream. If the executable bit did not survive `cp`, run `chmod +x .cursor/skills/diagnosing-bugs/scripts/hitl-loop.template.sh` and re-run the test.

- [ ] **Step 5: Commit**

```bash
git add .cursor/skills/tdd .cursor/skills/diagnosing-bugs \
  .cursor/skills/domain-modeling .cursor/skills/grill-with-docs \
  scripts/mattpocock-skill-manifest.txt scripts/mattpocock-skill-guards.txt
git commit -m "$(cat <<'EOF'
Added Matt Pocock test and domain skills that leave acceptance to the human.

EOF
)"
```

---

### Task 5: Review, pull requests, triage, setup, and drafts

**Files:**

- Create: `.cursor/skills/code-review/`
- Create: `.cursor/skills/pr/`
- Create: `.cursor/skills/triage/`
- Create: `.cursor/skills/setup-matt-pocock-skills/`
- Create: `.cursor/skills/to-spec/`
- Create: `.cursor/skills/to-tickets/`
- Modify: `scripts/mattpocock-skill-manifest.txt`
- Modify: `scripts/mattpocock-skill-guards.txt`
- Test: `scripts/check-skills.sh`

**Interfaces:**

- Consumes: the importer.
- Produces: `code-review` contains `Report only. Do not edit the diff.` `pr` contains `Do not create, push, or open a pull request unless the user explicitly asks.` `triage` contains `Do not merge a pull request.` `setup-matt-pocock-skills` contains `do not rewrite that section`. `to-spec` contains `Leave the spec as a draft.` and does not contain `Apply the` ready-for-agent `triage label`. `to-tickets` contains `**Status:** draft` and does not contain `**Status:** ready-for-agent`.

- [ ] **Step 1: Record the guards, then import**

Append to `scripts/mattpocock-skill-guards.txt`:

```text
present|code-review|Report only. Do not edit the diff.
present|pr|Do not create, push, or open a pull request unless the user explicitly asks.
present|triage|Do not merge a pull request.
present|setup-matt-pocock-skills|do not rewrite that section
present|to-spec|Leave the spec as a draft.
absent|to-spec|Apply the `ready-for-agent` triage label
present|to-tickets|**Status:** draft
absent|to-tickets|**Status:** ready-for-agent
absent|to-tickets|Apply the `ready-for-agent` triage label
```

Import and append to the manifest:

```bash
scripts/import-mattpocock-skill.sh engineering code-review
scripts/import-mattpocock-skill.sh engineering pr
scripts/import-mattpocock-skill.sh engineering triage
scripts/import-mattpocock-skill.sh engineering setup-matt-pocock-skills
scripts/import-mattpocock-skill.sh engineering to-spec
scripts/import-mattpocock-skill.sh engineering to-tickets
```

```text
code-review
pr
triage
setup-matt-pocock-skills
to-spec
to-tickets
```

- [ ] **Step 2: Run the checker to verify it fails**

Run: `scripts/check-mattpocock-skills.sh`

Expected: exit 1. The output contains `missing required text in code-review` and `forbidden text in to-tickets`.

- [ ] **Step 3: Patch review, triage, setup, and draft publishing**

In `.cursor/skills/code-review/SKILL.md`, immediately after the line `Both axes run as **parallel sub-agents** so they don't pollute each other's context, then this skill aggregates their findings. Start every sub-agent with a fresh context window.`, insert:

```markdown

Report only. Do not edit the diff. If you produced this diff in the current session, stop and tell the user to hand it to the readonly code-reviewer agent.
```

In `.cursor/skills/pr/SKILL.md`, immediately after the line `Use this template for writing the PR body:`, insert:

```markdown

Do not create, push, or open a pull request unless the user explicitly asks. When they do, write the body in this shape and use `gh`.
```

In `.cursor/skills/triage/SKILL.md`, immediately after the line `Move issues on the project issue tracker through a small state machine of triage roles.`, insert:

```markdown

Do not merge a pull request. `ready-for-human` means stop for the human.
```

In `.cursor/skills/setup-matt-pocock-skills/SKILL.md`, immediately after the line `This is a prompt-driven skill, not a deterministic script. Explore, present what you found, confirm with the user, then write.`, insert:

```markdown

## This setup

If `AGENTS.md` already has an SDLC workflow, do not rewrite that section. Append an `## Agent skills` block only when it is missing, and do not delete surrounding sections. Do not run `git init` or `git remote add` unless the user explicitly asks in this turn. Do not set Status to `accepted`.

This config repository is the skill source. Do not create GitHub issues for it during setup. A product repo that copies this setup runs this skill once there.
```

In `.cursor/skills/to-spec/SKILL.md`, replace:

```markdown
3. Write the spec using the template below, then publish it to the project issue tracker. Apply the `ready-for-agent` triage label - no need for additional triage.
```

with:

```markdown
3. Write the spec using the template below, then publish it to the project issue tracker as a draft. Leave the spec as a draft. Do not apply a ready-for-agent label. Do not set Status to `accepted`. If `intent/<slug>/intent.md` exists for this work, this draft does not replace `/spec`.
```

In `.cursor/skills/to-tickets/SKILL.md`, replace:

```markdown
Apply the `ready-for-agent` triage label unless instructed otherwise; the tickets are agent-grabbable by construction.
```

with:

```markdown
Do not apply a ready-for-agent label. Leave each ticket as a draft the human can accept. Do not set Status to `accepted`.
```

And replace:

```markdown
**Status:** ready-for-agent
```

with:

```markdown
**Status:** draft
```

- [ ] **Step 4: Run the checkers**

Run: `scripts/check-skills.sh`

Expected:

```text
skills: OK
mattpocock skills: OK
```

Run: `test -f .cursor/skills/triage/AGENT-BRIEF.md && test -f .cursor/skills/triage/OUT-OF-SCOPE.md && test -f .cursor/skills/setup-matt-pocock-skills/issue-tracker-local.md && test -f .cursor/skills/pr/CREDITS.md && echo review-setup-ok`

Expected: `review-setup-ok`

- [ ] **Step 5: Commit**

```bash
git add .cursor/skills/code-review .cursor/skills/pr .cursor/skills/triage \
  .cursor/skills/setup-matt-pocock-skills .cursor/skills/to-spec \
  .cursor/skills/to-tickets \
  scripts/mattpocock-skill-manifest.txt scripts/mattpocock-skill-guards.txt
git commit -m "$(cat <<'EOF'
Added Matt Pocock draft and review skills that stop before acceptance.

EOF
)"
```

---

### Task 6: Build flow and the router

**Files:**

- Create: `.cursor/skills/implement/`
- Create: `.cursor/skills/implement-spec/`
- Create: `.cursor/skills/wayfinder/`
- Create: `.cursor/skills/ask-matt/`
- Modify: `scripts/mattpocock-skill-manifest.txt`
- Modify: `scripts/mattpocock-skill-guards.txt`
- Test: `scripts/check-skills.sh`

**Interfaces:**

- Consumes: `tdd` from Task 4 and `code-review` from Task 5. The router names them but does not tell the author to run `code-review` on their own diff.
- Produces: `implement` contains `Leave the changes uncommitted` and `Do not review this diff yourself.` and does not contain `Commit your work to the current branch`. `implement-spec` contains `One ticket, then stop.` and `Do not merge to the default branch.` and does not contain `Fix all issues raised by the code review` or `maximum concurrency`. `wayfinder` contains `Leave the branch local.` and does not contain `Push the branch` or `](link)`. `ask-matt` contains `## Human gates in this setup`.

- [ ] **Step 1: Record the guards, then import**

Append to `scripts/mattpocock-skill-guards.txt`:

```text
present|implement|Leave the changes uncommitted
present|implement|Do not review this diff yourself.
absent|implement|Commit your work to the current branch
absent|implement|call the Skill tool with "code-review"
present|implement-spec|One ticket, then stop.
present|implement-spec|Do not merge to the default branch.
absent|implement-spec|Fix all issues raised by the code review
absent|implement-spec|maximum concurrency
present|wayfinder|Leave the branch local.
absent|wayfinder|Push the branch
absent|wayfinder|](link)
present|ask-matt|## Human gates in this setup
```

Import and append to the manifest:

```bash
scripts/import-mattpocock-skill.sh engineering implement
scripts/import-mattpocock-skill.sh engineering implement-spec
scripts/import-mattpocock-skill.sh engineering wayfinder
scripts/import-mattpocock-skill.sh engineering ask-matt
```

```text
implement
implement-spec
wayfinder
ask-matt
```

- [ ] **Step 2: Run the checker to verify it fails**

Run: `scripts/check-mattpocock-skills.sh`

Expected: exit 1. The output contains `forbidden text in implement` and `forbidden text in wayfinder`.

Also run: `scripts/check-skills.sh`

Expected: exit 1, and the output contains `broken reference: .cursor/skills/wayfinder/SKILL.md -> link`. That failure clears in Step 3 when the example link is removed. Do not change `scripts/check-skills.sh` to ignore it.

- [ ] **Step 3: Patch implement, implement-spec, wayfinder, and ask-matt**

In `.cursor/skills/implement/SKILL.md`, replace:

```markdown
Once done, call the Skill tool with "code-review" to review the work.

Commit your work to the current branch.
```

with:

```markdown
Leave the changes uncommitted and paste the verification output. Do not review this diff yourself. Do not commit, push, merge, or open a pull request unless the user explicitly asks in this turn.
```

In `.cursor/skills/implement-spec/SKILL.md`, replace:

```markdown
The goal is the entire spec implemented on a single **integration branch**, with every ticket resolved the way the issue tracker closes work.
```

with:

```markdown
The goal is one frontier ticket implemented in the current working tree. The human decides when to continue, commit, or merge.
```

Replace:

```markdown
**Implementer subagents** should be run in the background where possible for maximum concurrency.
```

with:

```markdown
One ticket, then stop. Do not fan out background implementers.
```

Replace steps 3 through 9:

```markdown
3. Create the integration branch. If the issue tracker closes work through PRs, or the user asks for one, open a draft PR after the first merge in step 5 (a branch with no commits ahead of main can't open one), marked as closing the spec and tickets.

4. Use a fresh **implementer subagent** for each ticket, each in its own worktree on its own branch. Each implementer subagent:
   - confirms its worktree is based on the integration branch before starting, and resets onto it if not;
   - calls the Skill tool with `tdd` to build the ticket;
   - merges the integration branch tip into its own branch before reporting done

5. Once an **implementer subagent** completes, merge its work to the integration branch with a **merger subagent**.

6. If this changes the **frontier** of available tickets, kick off more **implementer subagents** to work on the new tickets. This allows for maximum concurrency.

7. Once all tickets are complete, call the Skill tool with `code-review` on the integration branch. Fix all issues raised by the code review in a single fresh **implementer subagent**.

8. If a draft PR exists, mark it ready for review. Otherwise, resolve each ticket the way the issue tracker closes work, and report the integration branch.

9. Clean up all **implementer subagent** worktrees.
```

with:

```markdown
3. Work the frontier one ticket at a time, in this working tree. Do not open a pull request and do not push.

4. For that ticket, call the Skill tool with `tdd`. Do not commit.

5. Stop after that ticket. Report what changed and the verification output. Do not merge to the default branch. Do not review this diff yourself.

6. Do not close tracker tickets as done. The user accepts the work.
```

In `.cursor/skills/wayfinder/SKILL.md`, replace:

```markdown
5. **Fire the research subagents.** For each `research` ticket you just created, spin up a subagent with a fresh context window that calls the Skill tool with "research" to resolve it in parallel, capturing its findings on a throwaway `research/<name>` branch with a context pointer from the ticket. Push the branch but open no PR: it is never merged.
```

with:

```markdown
5. **Fire the research subagents.** For each `research` ticket you just created, spin up a subagent with a fresh context window that calls the Skill tool with "research" to resolve it in parallel, capturing its findings on a throwaway `research/<name>` branch with a context pointer from the ticket. Leave the branch local. Do not push, open a pull request, or merge.
```

Replace the example map line:

```markdown
- [<closed ticket title>](link): <one-line gist of the answer>
```

with:

```markdown
- `<closed ticket title>`: <one-line gist of the answer>
```

In `.cursor/skills/ask-matt/SKILL.md`, immediately after the line `Before stating what a skill does or recommending a step be skipped, read that skill's SKILL.md: the summaries here are for orientation only.`, insert:

```markdown

## Human gates in this setup

When this repo contains the AI-SDLC workflow, those gates win:

- `/grill-with-docs` may sharpen a design. It does not accept an intent, experience, spec, ADR, or plan.
- `/to-spec` writes a draft. It does not replace `/spec` and does not set Status to `accepted`.
- `/implement` and `/implement-spec` do not commit, push, merge, or review their own diff. One bounded task, then stop.
- `/code-review` reports only. The author of the diff does not run it.
- `/record-decision` is how ADRs are written. Status stays `proposed`.
```

Replace:

```markdown
     - **`/implement`** per ticket, **`/clear`ing context between each one**. On a local tracker that's one file per ticket under `.scratch/<feature>/issues/`, worked blockers-first by hand; on a real tracker the edges become native blocking links, so any ticket whose blockers are done can be grabbed. Each ticket is self-contained, so the last one's context is disposable.
     - **`/implement-spec`** for the whole spec in one run. It reads the tickets as a **task graph**, runs implementer subagents across the ready **frontier** in parallel, and lands everything on one **integration branch**. Reach for it when you'd rather orchestrate the build than drive each ticket yourself.
```

with:

```markdown
     - **`/implement`** per ticket, **`/clear`ing context between each one**. On a local tracker that's one file per ticket under `.scratch/<feature>/issues/`, worked blockers-first by hand; on a real tracker the edges become native blocking links, so any ticket whose blockers are done can be grabbed. Each ticket is self-contained, so the last one's context is disposable. It leaves the work uncommitted.
     - **`/implement-spec`** for the whole spec in one run. It reads the tickets as a **task graph** and works the ready **frontier** one ticket at a time. It does not commit, push, merge, or review its own diff. Reach for it when you want one session to walk the graph, then stop for a human.
```

Replace:

```markdown
   Either way, the code gets built by driving **`/tdd`** (one red-green slice at a time) and closes out with **`/code-review`**, a two-axis review (Standards + Spec) of the diff. `/implement` runs both per ticket; `/implement-spec`'s implementers each drive `/tdd`, and it runs one `/code-review` over the integration branch. Reach for **`/tdd`** on its own when you just want to build a concrete behaviour test-first without a full spec, and **`/code-review`** on its own whenever you want to review a branch or PR against a fixed point.
```

with:

```markdown
   Either way, the code gets built by driving **`/tdd`** (one red-green slice at a time). The author does not review their own diff. A separate session may run **`/code-review`**, a two-axis review (Standards + Spec). Reach for **`/tdd`** on its own when you just want to build a concrete behaviour test-first without a full spec, and **`/code-review`** on its own whenever you want to review a branch or PR against a fixed point.
```

- [ ] **Step 4: Run the checkers**

Run: `scripts/check-skills.sh`

Expected:

```text
skills: OK
mattpocock skills: OK
```

Run: `test -f .cursor/skills/ask-matt/PHASE-BOUNDARIES.md && echo router-ok`

Expected: `router-ok`

- [ ] **Step 5: Commit**

```bash
git add .cursor/skills/implement .cursor/skills/implement-spec \
  .cursor/skills/wayfinder .cursor/skills/ask-matt \
  scripts/mattpocock-skill-manifest.txt scripts/mattpocock-skill-guards.txt
git commit -m "$(cat <<'EOF'
Added Matt Pocock build-flow skills that stop before commit and review.

EOF
)"
```

---

### Task 7: Document the imported commands

**Files:**

- Modify: `scripts/check-mattpocock-skills.sh`
- Modify: `AGENTS.md`
- Modify: `docs/ai-sdlc.md`
- Modify: `CHANGELOG.md`
- Test: `scripts/check-skills.sh`, `scripts/check-docs.sh`, `scripts/verify.sh`

**Interfaces:**

- Consumes: all 27 names in `scripts/mattpocock-skill-manifest.txt`.
- Produces: `AGENTS.md` contains the sentence `Imported Matt Pocock skills do not bypass human acceptance.` `docs/ai-sdlc.md` lists `/ask-matt`, `/setup-matt-pocock-skills`, `/grill-with-docs`, `/to-spec`, `/implement`, and `/code-review`. `CHANGELOG.md` has an Unreleased Added bullet naming the import.

- [ ] **Step 1: Extend the checker so the docs are required, then watch it fail**

In `scripts/check-mattpocock-skills.sh`, immediately before the final `if [[ "$fail" -ne 0 ]]; then`, insert:

```bash
docs_ok=0
grep -F -q "Imported Matt Pocock skills do not bypass human acceptance." AGENTS.md && docs_ok=$((docs_ok + 1))
grep -F -q "/setup-matt-pocock-skills" docs/ai-sdlc.md && docs_ok=$((docs_ok + 1))
grep -F -q "Matt Pocock engineering and productivity skills" CHANGELOG.md && docs_ok=$((docs_ok + 1))
if [[ "$docs_ok" -ne 3 ]]; then
  echo "missing imported-skill docs in AGENTS.md, docs/ai-sdlc.md, or CHANGELOG.md"
  fail=1
fi
```

Run: `scripts/check-mattpocock-skills.sh`

Expected: exit 1, and the output contains `missing imported-skill docs`.

- [ ] **Step 2: Update AGENTS.md, the runbook, and the changelog**

In `AGENTS.md`, under `## SDLC workflow`, after the bullet `Bug fixes use` /fix-bug`.`, add:

```markdown
- Imported Matt Pocock skills do not bypass human acceptance. They live in `.cursor/skills/` beside the control-plane skills. `/spec`, `/plan`, `/record-decision`, and `/fix-bug` stay authoritative where they overlap. `/setup-matt-pocock-skills` runs in a product repo, not in this config repo.
```

In `docs/ai-sdlc.md`, immediately before `## Architecture gate`, add:

```markdown
## Imported Matt Pocock skills

These are vendored from `mattpocock/skills` at the SHA in `docs/third-party/mattpocock-skills.pin`. The MIT notice is `docs/third-party/mattpocock-skills-LICENSE`. User-invoked commands:

- `/ask-matt` — which imported skill fits this situation.
- `/setup-matt-pocock-skills` — once per product repo, configure its issue tracker, triage labels, and domain-doc layout. Do not run it to reconfigure this config repo.
- `/grill-me` — interview without writing a glossary.
- `/grill-with-docs` — interview, and update the glossary and ADRs as terms settle. ADRs still go through `/record-decision` and stay `proposed`.
- `/to-spec` — turn the current conversation into a draft spec. It does not replace `/spec` and does not set Status to `accepted`.
- `/to-tickets` — split a draft into tracer-bullet tickets at Status `draft`.
- `/implement` — build one ticket with `/tdd`, leave the diff uncommitted, and do not self-review.
- `/implement-spec` — walk a ticket graph one frontier ticket at a time, then stop.
- `/wayfinder` — map a large decision space. Do not push the research branch.
- `/triage` — move incoming issues through triage roles. Do not merge.
- `/improve-codebase-architecture` — survey deepening candidates. Do not implement one in that session.
- `/retro` — suggest environment improvements. Do not edit guardrail files in that session.
- `/handoff`, `/teach`, `/to-questionnaire`, `/wait-what` — productivity commands. They do not accept product artifacts.

Model-invoked skills (`tdd`, `diagnosing-bugs`, `code-review`, `codebase-design`, `domain-modeling`, `grilling`, `pr`, `prototype`, `research`, `wizard`, `writing-for-agents`) apply from their descriptions. `code-review` reports only. The author of a diff does not run it.

```

In `CHANGELOG.md`, under `## [Unreleased]` / `### Added`, add this bullet at the top of that list:

```markdown
- Matt Pocock engineering and productivity skills, pinned in `docs/third-party/mattpocock-skills.pin`, with local gates overriding commit, merge, self-review, and acceptance.
```

- [ ] **Step 3: Run the checks and count the manifest**

Run: `scripts/check-skills.sh`

Expected:

```text
skills: OK
mattpocock skills: OK
```

Run: `grep -cve '^\s*$' scripts/mattpocock-skill-manifest.txt`

Expected: `27`

Run: `scripts/check-docs.sh`

Expected: `check-docs: docs updated` when the working tree contains both skill files and these doc files. If the tree is clean because Step 5 already committed, the command prints `check-docs: no source changes` and exits 0. Either result is success. A missing docs update exits 1; fix the docs, do not weaken the script.

Run: `scripts/verify.sh`

Expected: exit 1, and the output contains `TODO: configure build`. Do not edit `scripts/verify.sh`.

- [ ] **Step 4: Commit**

```bash
git add scripts/check-mattpocock-skills.sh AGENTS.md docs/ai-sdlc.md CHANGELOG.md
git commit -m "$(cat <<'EOF'
Documented the imported Matt Pocock skills and the gates they do not bypass.

EOF
)"
```

---

## Plan self-review notes

Spec coverage: all 27 published engineering and productivity skills are in the coverage table and in a task. In-progress, deprecated, and misc skills are excluded by name in the checker. The pin, MIT notice, constraint block, broken `wayfinder` link, HITL script path, and the commit / merge / self-review / acceptance conflicts each have a guard.

Placeholder scan: no task defers a patch to a later unnamed edit. Each conflicting sentence has the replacement text.

Type consistency: skill folder names match the manifest, the importer argument, and the guard's second field. The constraint heading is `## Setup constraints` in the overlay, the importer, and the checker.

## Departures recorded during implementation

- `improve-codebase-architecture`: the description no longer says to grill, the report step does not ask which candidate to explore, and the grilling section is now "Stop".
- `prototype/UI.md` step 6 follows SKILL.md and does not fold a variant into real code unless the user asks.
- `implement-spec` no longer tells the agent to spawn implementer subagents. It works one ticket in this session, then stops.
- `wayfinder` research notes stay on a local branch and are not pushed. Decision tickets are closed when resolved, which is how the frontier advances. The agent does not set Status to accepted.
- `setup-matt-pocock-skills` asks before `git init` or `git remote add`.
- The checker compares each skill's setup-constraints block to the overlay file and rejects a guard whose skill name is not on the manifest.
