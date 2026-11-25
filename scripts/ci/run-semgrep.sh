#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

SEMGREP_SCOPE="${SEMGREP_SCOPE:-full}"
INCLUDE_ARGS=(
  --include 'cmd/**/*.go'
  --include 'internal/**/*.go'
  --include 'test/**/*.go'
  --include 'scripts/**/*.sh'
  --include 'scripts/**/*.bash'
)

TARGETS=()
if [ "${SEMGREP_SCOPE}" = 'changed' ]; then
  while IFS= read -r line; do
    [ -n "${line}" ] && TARGETS+=("${line}")
  done < <(./scripts/ci/changed-app-files.sh || true)
  if [ "${#TARGETS[@]}" -eq 0 ]; then
    echo "semgrep skipped: no changed app files"
    exit 0
  fi
else
  while IFS= read -r line; do
    [ -n "${line}" ] && TARGETS+=("${line}")
  done < <(
    find cmd internal test scripts -type f \( -name '*.go' -o -name '*.sh' -o -name '*.bash' \) \
      ! -path 'test/security/semgrep-fixtures/*' -print 2>/dev/null || true
  )
fi

FILTERED=()
for f in "${TARGETS[@]}"; do
  case "${f}" in
    test/security/semgrep-fixtures/*) continue ;;
    *) FILTERED+=("${f}") ;;
  esac
done
TARGETS=("${FILTERED[@]}")

if [ "${#TARGETS[@]}" -eq 0 ]; then
  echo "semgrep skipped: no target files"
  exit 0
fi

SEMGREP_LOCAL_CONFIG="${ROOT_DIR}/semgrep/rules/obfuscated-exfil.yaml"

run_semgrep() {
  semgrep scan --error --config "${SEMGREP_LOCAL_CONFIG}" \
    --config p/golang \
    "${INCLUDE_ARGS[@]}" "$@"
}

if command -v semgrep >/dev/null 2>&1; then
  run_semgrep "${TARGETS[@]}"
  exit 0
fi

if command -v docker >/dev/null 2>&1; then
  docker run --rm -v "${ROOT_DIR}:/src" -w /src returntocorp/semgrep \
    semgrep scan --error --config semgrep/rules/obfuscated-exfil.yaml \
    --config p/golang \
    "${INCLUDE_ARGS[@]}" "${TARGETS[@]}"
  exit 0
fi

echo "semgrep: install semgrep CLI or Docker to run locally" >&2
exit 1
