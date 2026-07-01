# Bash risk classifier evals

Live evaluation suite for `AssessBashRisk` — tests real LLM output, prompt-injection resistance, and guard fallback. **Not** part of `go test ./...` (unit tests use `FakeProvider`).

## Case file

`cases.json` — stdlib JSON only (no extra dependencies). Fields:

| Field | Meaning |
|-------|---------|
| `expect` | Required result: `low`, `medium`, or `high` |
| `must_not` | Hard fail if result equals this (injection cases) |
| `retries` | Re-run on failure; injection cases use 2+ |
| `category` | `read_only`, `destructive`, `mutating`, `prompt_injection`, `long_inline`, `ambiguous`, `guard_only` |

## Quick start

```bash
# List cases (no API)
./evals/bash-risk/run.sh --dry-run

# Guard heuristics only (no API)
./evals/bash-risk/run.sh --guard-only

# Live LLM eval (costs tokens)
./evals/bash-risk/run.sh

# Model quality only (no guard fallback)
./evals/bash-risk/run.sh --mode llm

# Filter
./evals/bash-risk/run.sh --tag gh
./evals/bash-risk/run.sh --category prompt_injection

# Re-print a saved report
./evals/bash-risk/run.sh --summarize evals/bash-risk/reports/latest.json
```

## Environment

| Variable | Purpose |
|----------|---------|
| `POISSON_EVAL_PROVIDER` | Override provider (`anthropic`, `ollama`, `xai`) |
| `POISSON_EVAL_MODEL` | Override model id |

Uses `~/.poisson/config.toml` and `~/.poisson/auth.json` when overrides are unset.

## Modes

| Mode | What it tests |
|------|----------------|
| `full` | Production path: dual LLM runs (max risk) → guard fallback |
| `llm` | Model only — two LLM calls, strictest wins |
| `guard` | `GuardRiskFallback` only — free, no network |

Each live assessment runs the classifier **twice** and keeps the higher band (`high` > `medium` > `low`). Reports include `llm_runs` per case.

## Reports

Written to `evals/bash-risk/reports/<timestamp>.json`. Symlink `latest.json` points at the most recent run. Exit code 1 if any case fails; `critical` counts injection cases that returned `must_not`.

## Adding cases

1. Add an entry to `cases.json`.
2. Run filtered eval: `./evals/bash-risk/run.sh --id your-case-id`
3. Commit the case; reports stay gitignored.