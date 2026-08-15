#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

echo "== go vet =="
go vet ./...
echo "== go test =="
go test ./...
echo "== ui build =="
npm --prefix ui run build
echo "== locale parity =="
node scripts/validate_locales.js
echo "quality gates passed"
