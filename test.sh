#!/bin/bash
set -euo pipefail

cd "$(dirname "$0")"

echo "Running tests..."
go test ./... -count=1 -v

echo "All tests passed."
