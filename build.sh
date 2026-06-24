#!/bin/bash
set -euo pipefail

cd "$(dirname "$0")"

echo "Building Poisson..."
go build -o px ./cmd/px

echo "Done: ./px"
