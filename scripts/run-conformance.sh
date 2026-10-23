#!/usr/bin/env bash
# Runs the scaffold's conformance gate: build, vet, full tests.
set -euo pipefail
cd "$(dirname "$0")/.."
go build ./...
go vet ./...
go test ./... -count=1
