#!/bin/bash
set -euo pipefail

sharp=".cursor/skills/security-sharp-edges/SKILL.md"
baseline=".cursor/skills/security-baseline/SKILL.md"
audit=".cursor/skills/security-audit/SKILL.md"

test -f "$sharp"
test -f "$baseline"
test -f "$audit"

grep -q '^name: security-sharp-edges$' "$sharp"
grep -q "No Fallback Secrets or Keys" "$sharp"
grep -q "No Fail-Open Catch Blocks" "$sharp"
grep -q "Make Tenant & Auth Context Unforgettable" "$sharp"
grep -q "TenantContext" "$sharp"

grep -q '^name: security-baseline$' "$baseline"
grep -q "Zero-Trust Outbound Egress & DLP Guardrails" "$baseline"
grep -q "169.254.169.254" "$baseline"

grep -q '^name: security-audit$' "$audit"
grep -q '^disable-model-invocation: true$' "$audit"
grep -q "coverage-ledger.json" "$audit"
grep -q "findings.json" "$audit"
grep -q "Adversarial Candidate Validation" "$audit"

echo "security sharp edges & audit: OK"
