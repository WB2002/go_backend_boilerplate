#!/bin/bash
set -euo pipefail

verify_skill=".cursor/skills/verification-before-completion/SKILL.md"
doubt_skill=".cursor/skills/doubt-driven-development/SKILL.md"
ui_skill=".cursor/skills/frontend-ui-engineering/SKILL.md"
fast_track=".cursor/skills/fast-track/SKILL.md"
ship_check=".cursor/skills/ship-check/SKILL.md"

test -f "$verify_skill"
test -f "$doubt_skill"
test -f "$ui_skill"
test -f "$fast_track"
test -f "$ship_check"

grep -q '^name: verification-before-completion$' "$verify_skill"
grep -q "IDENTIFY → RUN → READ → VERIFY → CLAIM" "$verify_skill"
grep -q "scripts/verify.sh" "$verify_skill"

grep -q '^name: doubt-driven-development$' "$doubt_skill"
grep -q "CLAIM → EXTRACT → DOUBT → RECONCILE → STOP" "$doubt_skill"

grep -q '^name: frontend-ui-engineering$' "$ui_skill"
grep -q "Implement All 7 Applicable Interaction States" "$ui_skill"
grep -q "Semantic Tokens Over Magic Values" "$ui_skill"
grep -q "WCAG 2.1 AA" "$ui_skill"

grep -q '^name: fast-track$' "$fast_track"
grep -q '^disable-model-invocation: true$' "$fast_track"
grep -q "forbidden for \*\*Tier 2 (\`HIGH\`)\*\* and \*\*\`CRITICAL\`\*\*" "$fast_track"

grep -q '^name: ship-check$' "$ship_check"
grep -q '^disable-model-invocation: true$' "$ship_check"
grep -q "Release Readiness Scorecard" "$ship_check"

echo "verification, doubt, frontend-ui, fast-track & ship-check: OK"
