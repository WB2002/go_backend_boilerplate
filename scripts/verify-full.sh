#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")/.."
echo "== verify =="
scripts/verify.sh
if [ "${SKIP_INTEGRATION:-0}" != "1" ]; then
  echo "== integration/e2e =="
  go test -race -tags=integration ./test/integration/... ./internal/...
fi
