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
    else
      if ! python3 - "$skill" .cursor/skills/_overlay/setup-constraints.md <<'PY'
import pathlib, sys
skill = pathlib.Path(sys.argv[1]).read_text()
overlay = pathlib.Path(sys.argv[2]).read_text().strip()
if overlay not in skill:
    sys.exit(1)
PY
      then
        echo "overlay drift: $name"
        fail=1
      fi
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
    if ! grep -qxF "$name" scripts/mattpocock-skill-manifest.txt; then
      echo "guard for unknown skill: $name"
      fail=1
      continue
    fi
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
  setup-matt-pocock-skills
  to-spec
  to-tickets
  implement
  implement-spec
  triage
  wayfinder
  ask-matt
  grill-me
  code-review
  teach
  to-questionnaire
  wait-what
  grilling
  handoff
  writing-for-agents
  research
  retro
  prototype
  wizard
  codebase-design
  improve-codebase-architecture
  diagnosing-bugs
  domain-modeling
  grill-with-docs
  pr
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

docs_ok=0
grep -F -q "Imported Matt Pocock skills do not bypass human acceptance." AGENTS.md && docs_ok=$((docs_ok + 1))
grep -F -q "\`tdd\`" docs/ai-sdlc.md && docs_ok=$((docs_ok + 1))
grep -F -q "Matt Pocock engineering and productivity skills" CHANGELOG.md && docs_ok=$((docs_ok + 1))
if [[ "$docs_ok" -ne 3 ]]; then
  echo "missing imported-skill docs in AGENTS.md, docs/ai-sdlc.md, or CHANGELOG.md"
  fail=1
fi

if [[ "$fail" -ne 0 ]]; then
  exit 1
fi
echo "mattpocock skills: OK"
