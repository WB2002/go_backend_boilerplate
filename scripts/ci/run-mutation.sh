#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

export PATH="$HOME/go/bin:$HOME/.local/bin:$PATH"

CHANGED_PKGS=()
while IFS= read -r file; do
  if [ -n "${file}" ]; then
    pkg="./$(dirname "${file}")"
    already_seen=0
    for existing in "${CHANGED_PKGS[@]+"${CHANGED_PKGS[@]}"}"; do
      if [ "${existing}" = "${pkg}" ]; then
        already_seen=1
        break
      fi
    done
    if [ "${already_seen}" -eq 0 ]; then
      CHANGED_PKGS+=("${pkg}")
    fi
  fi
done < <(./scripts/ci/changed-go-files.sh)

if [ "${#CHANGED_PKGS[@]}" -eq 0 ]; then
  echo "mutation skipped: no eligible changed files"
  exit 0
fi

if ! command -v gremlins >/dev/null 2>&1; then
  echo "Installing gremlins mutation testing tool..."
  go install github.com/go-gremlins/gremlins/cmd/gremlins@v0.6.0
fi

mkdir -p reports/mutation
REPORT_FILE="reports/mutation/gremlins-report.txt"
: > "${REPORT_FILE}"

echo "Running mutation tests on changed packages: ${CHANGED_PKGS[*]}"
STATUS=0
for pkg in "${CHANGED_PKGS[@]}"; do
  echo "=== gremlins unleash ${pkg} ===" | tee -a "${REPORT_FILE}"
  if ! gremlins unleash "${pkg}" 2>&1 | tee -a "${REPORT_FILE}"; then
    STATUS=1
  fi
done

exit "${STATUS}"
