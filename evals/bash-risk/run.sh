#!/usr/bin/env bash
# Run live bash-risk classifier evals against cases.json.
#
# Usage:
#   ./evals/bash-risk/run.sh
#   ./evals/bash-risk/run.sh --mode llm
#   ./evals/bash-risk/run.sh --tag gh
#   ./evals/bash-risk/run.sh --category prompt_injection
#   ./evals/bash-risk/run.sh --dry-run
#   ./evals/bash-risk/run.sh --guard-only
#   ./evals/bash-risk/run.sh --summarize evals/bash-risk/reports/latest.json
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
cd "$ROOT"

CASES="evals/bash-risk/cases.json"
MODE="full"
PROVIDER="${POISSON_EVAL_PROVIDER:-}"
MODEL="${POISSON_EVAL_MODEL:-}"
DRY_RUN=0
GUARD_ONLY=0
SUMMARIZE=""
EXTRA=()

while [[ $# -gt 0 ]]; do
  case "$1" in
    --mode) MODE="$2"; shift 2 ;;
    --provider) PROVIDER="$2"; shift 2 ;;
    --model) MODEL="$2"; shift 2 ;;
    --cases) CASES="$2"; shift 2 ;;
    --tag) EXTRA+=(--tag "$2"); shift 2 ;;
    --category) EXTRA+=(--category "$2"); shift 2 ;;
    --id) EXTRA+=(--id "$2"); shift 2 ;;
    --dry-run) DRY_RUN=1; shift ;;
    --guard-only) GUARD_ONLY=1; MODE="guard"; shift ;;
    --summarize) SUMMARIZE="$2"; shift 2 ;;
    -h|--help)
      sed -n '2,12p' "$0"
      exit 0
      ;;
    *)
      echo "unknown arg: $1" >&2
      exit 2
      ;;
  esac
done

BIN="${ROOT}/.cache/risk-eval"
mkdir -p "${ROOT}/.cache"
go build -o "$BIN" ./cmd/risk-eval

if [[ -n "$SUMMARIZE" ]]; then
  exec "$BIN" --summarize "$SUMMARIZE"
fi

if [[ "$DRY_RUN" -eq 1 ]]; then
  exec "$BIN" --cases "$CASES" --mode "$MODE" --dry-run "${EXTRA[@]}"
fi

if [[ "$GUARD_ONLY" -eq 1 || "$MODE" == "guard" ]]; then
  MODE="guard"
fi

STAMP="$(date +%Y%m%d-%H%M%S)"
OUT="evals/bash-risk/reports/${STAMP}.json"
mkdir -p evals/bash-risk/reports

ARGS=(--cases "$CASES" --mode "$MODE" --out "$OUT" "${EXTRA[@]}")
[[ -n "$PROVIDER" ]] && ARGS+=(--provider "$PROVIDER")
[[ -n "$MODEL" ]] && ARGS+=(--model "$MODEL")

set +e
"$BIN" "${ARGS[@]}"
CODE=$?
set -e

ln -sf "$(basename "$OUT")" evals/bash-risk/reports/latest.json
exit "$CODE"