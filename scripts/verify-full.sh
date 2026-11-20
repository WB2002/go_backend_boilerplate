#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")/.."
echo "== verify =="
scripts/verify.sh
echo "== integration/e2e =="
go test -race -tags=integration ./...
