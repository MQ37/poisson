#!/bin/bash
set -euo pipefail

cd "$(dirname "$0")"

# Keep all test temp I/O under /tmp even when TMPDIR is set elsewhere.
export TMPDIR=/tmp

echo "Running tests..."
go test ./... -count=1 -v

echo "All tests passed."
