# Ollama provider

Poisson talks to Ollama through its **OpenAI-compatible** endpoint
(`{baseURL}/v1/chat/completions`, default `http://localhost:11434`). This
covers both local models and Ollama Cloud/Turbo `:cloud` models (default
`glm-5.2:cloud`, plus `minimax-m3:cloud` and `kimi-k2.7-code:cloud`).

## Prompt caching — nothing to implement

Unlike Anthropic (`cache_control` breakpoints) and OpenAI (`prompt_cache_key`),
**the client does not drive Ollama caching.** Ollama (via llama.cpp) does
implicit **KV / prompt-prefix caching** automatically: consecutive requests
that share a byte-identical prefix reuse the already-computed cache while the
model stays resident, skipping re-evaluation of the common prefix. There is no
cache key, breakpoint, or flag to send — so the Anthropic/OpenAI-style caching
work does not apply here, and none is wired into the Ollama provider.

## Why the token-cost concern doesn't apply

The Anthropic and OpenAI caching fixes mattered because those backends are
pay-per-token / usage-limited. Ollama is neither:

- **Local** — free. Caching only affects latency, and already works
  automatically within the keep-alive window.
- **Cloud / Turbo** (the default) — flat subscription, GPU-time billing, **no
  per-token charges and no cache-token reporting**. There is no per-token cost
  to optimise and nothing to surface in the status line. (`ollama.com/pricing`)

## The one lever (`keep_alive`) is not reachable from this endpoint

Ollama unloads a model — and drops its KV cache — after 5 minutes of
inactivity by default. `keep_alive` overrides that, but **it is ignored on the
`/v1/chat/completions` endpoint** Poisson uses:

- Ollama issues [#11458](https://github.com/ollama/ollama/issues/11458)
  (Ollama 0.9.6, Jul 2025), #3645, #2508 all confirm `keep_alive` in the
  request body is ignored there, defaulting to 5 min.
- PR [#11249](https://github.com/ollama/ollama/pull/11249) to expose
  `keep_alive`/`think` through the OpenAI API is still unmerged as of mid-2026.

The only client-independent control is the server-side env var. For a **local**
model you run heavily, keep the KV cache warm across turns (latency only, not
cost) with:

```bash
OLLAMA_KEEP_ALIVE=60m ollama serve
```

## Context length

`num_ctx`/`options` are also ignored on the OpenAI-compatible endpoint, but
this does not hurt Poisson's default `:cloud` models: Ollama sets **cloud
models to their maximum context length by default**
(`docs.ollama.com/context-length`). Local models fall back to Ollama's
VRAM-based default (4k–256k); to raise it, set `OLLAMA_CONTEXT_LENGTH` on the
server or bake it into a Modelfile — it cannot be set per request here.

## Not done (would require the native API)

Client-controlled `keep_alive`/`num_ctx` would mean rewriting the provider onto
Ollama's native `/api/chat` (different request/response, streaming, and
tool-call schemas) for a latency-only benefit on local models. Not worth it
while Cloud is the default: it already auto-maxes context and bills flat.
