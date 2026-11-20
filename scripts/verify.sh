#!/bin/bash
set -euo pipefail

export PATH="$HOME/.local/bin:$HOME/go/bin:$PATH"

echo "== build =="
go build ./...

echo "== test  =="
go test -v ./...

echo "== lint  =="
if command -v golangci-lint >/dev/null 2>&1; then
    golangci-lint run ./...
else
    echo "golangci-lint not found, running go vet ./..."
    go vet ./...
fi

echo "verify: OK"
