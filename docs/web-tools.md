# Web tools: `web_search`, `fetch`, `web_ask`

Three tools reach the web, each with selectable backends. Backends that spend a
provider's credits are only offered while that provider is the active session
provider, so a model can never bill an account the user already switched away
from.

| Tool | Backends | Default |
|---|---|---|
| `web_search` | `duckduckgo`, `anthropic`, `firecrawl`, `you` | `duckduckgo` |
| `fetch` | `curl`, `ollama`, `anthropic`, `firecrawl` | `ollama` when an Ollama session can use it, else `curl` |
| `web_ask` | `grok`, `exa`, `tavily` | `grok` when logged in to xAI, else `exa` |

`firecrawl`, `you`, and `tavily` are free, keyless, always-available backends —
no login, no config, opt-in only via `provider=` (none of them changed any
default above). They exist as alternatives when the always-on default hits a
rate limit or bot challenge (DuckDuckGo, exa).

## Provider gating

`agent.ReloadConfigDependentTools` re-registers `web_search` and `fetch` on
every provider switch (startup, `/provider`, `/model`, `/reload`, session
resume) and hands them only the backends the current provider supports:

- `anthropic` — an `AnthropicWebBackend` (the live `*provider.AnthropicProvider`)
  only when the session provider is Anthropic.
- `ollama` — a base URL only when the session provider is Ollama *and* Ollama
  answers on it.

An unavailable backend is refused loudly (`provider=anthropic needs an
Anthropic session ...`) rather than silently falling back, so the model never
believes an extraction or search ran that didn't. Unavailable backends are also
left out of the tool description, so they aren't advertised where they can't work.

## `web_search`

- `duckduckgo` (default) — scrapes DuckDuckGo's no-JS HTML SERP. Free, no auth,
  returns `[{title, url, snippet}]`. Subject to DDG's bot challenge when
  rate-limited.
- `anthropic` — Anthropic's server-side `web_search_20250305` tool, run on a
  small model, returning links plus a synthesized summary:

  ```
  Web search results for query: "suckless philosophy"

  Links: [{"title":"...","url":"https://..."}]

  <summary prose>
  ```

  Billed to the Anthropic account as `usage.server_tool_use.web_search_requests`,
  and immune to DDG's challenge. `num` trims the link list.
- `firecrawl` — Firecrawl's search API, called through a minimal MCP client
  (`internal/mcpclient`) against Firecrawl's hosted, keyless remote server
  (`https://mcp.firecrawl.dev/v2/mcp`). No account, no API key; rate-limited
  per IP. Returns Firecrawl's own JSON verbatim (ranked web/news/image result
  groups) — no synthesis, same passthrough convention as `exa`.
- `you` — you.com's Search API keyless tier (no account, no API key,
  IP-throttled to roughly 100 queries/day). Returns you.com's own JSON verbatim
  (`web`/`news` result arrays) — no synthesis.

## `fetch`

- `curl` — fetch in-process, convert HTML to Markdown with the hand-rolled
  converter in `internal/tools/html2md.go`. The name is the model-facing
  spelling for "plain HTTP GET, no model in the loop"; it does not exec `curl`.
- `ollama` — proxy to the local Ollama instance's `/api/fetch`, reusing its
  extraction.
- `anthropic` — fetch exactly like `curl`, then answer the `prompt` argument
  against the extracted Markdown with Anthropic's small model. Returns just the
  answer, so a long document costs a few hundred context tokens instead of the
  whole page. `prompt` defaults to "What does this page say?", and is rejected
  on the other backends rather than silently dropped.
- `firecrawl` — scrapes through the same keyless Firecrawl MCP server as the
  `web_search` backend above (`firecrawl_scrape`, `formats: ["markdown"]`).
  Its own JS rendering/extraction often succeeds where `curl`'s plain HTTP GET
  returns an empty or unrendered page. `prompt` is not supported (rejected,
  like `curl`/`ollama`).

Subagents get the same backends as their parent: the child process re-registers
both tools for its own provider (`cmd/px/main.go`, after `SetSkills`).

Every backend that fetches locally goes through `safeFetchDialContext`, which
resolves the host itself and refuses loopback, private, link-local, multicast,
and unspecified addresses — a model-supplied (or prompt-injected) URL cannot
reach cloud metadata endpoints or services on the host. Choosing
`provider=anthropic` does not move the fetch off the machine, so that guard
still applies.

## Firecrawl's MCP client

`firecrawl` (both tools) goes through `internal/mcpclient`, a minimal
Model Context Protocol client written for exactly this — `tools/call` only, no
general-purpose SDK. The [2026-07-28 MCP spec](https://blog.modelcontextprotocol.io/posts/2026-07-28/)
drops the `initialize`/`initialized` handshake and `Mcp-Session-Id` entirely,
making every request self-contained; `mcp.firecrawl.dev` was probed live and
doesn't speak it yet (`HTTP 400: Unsupported protocol version: 2026-07-28`,
supported versions top out at `2025-11-25`). Its keyless tier already accepts
a bare `tools/call` POST with no prior `initialize` and no session ID under the
older spec, though, so the client skips both anyway — one POST per call,
matching the new spec's spirit without needing its version number yet. Update
`protocolVersion` in `client.go` once Firecrawl (or another server this talks
to) advertises `2026-07-28`.

## Where this came from

Both Anthropic backends are ports of Claude Code's own `WebSearch`/`WebFetch`,
reverse-engineered from live traffic — the wire shapes, prompts, and beta
headers are documented in
[`cc-sniff/docs/claude-code-web-tools.md`](../../cc-sniff/docs/claude-code-web-tools.md):

- `WebSearch` is not a server tool in Claude Code's main loop. The main loop
  calls a plain client tool, and the CLI answers it with a separate small-model
  `/v1/messages` request carrying `web_search_20250305`. Poisson does the same
  in `provider.(*AnthropicProvider).WebSearch`.
- `WebFetch` fetches the page on the client machine, converts it to Markdown,
  and hands it to the same small model with a fixed guardrail block. Poisson
  keeps the fetch in `tools/fetch.go` (SSRF guard, HTML→Markdown) and only the
  summarization pass in `provider.(*AnthropicProvider).WebFetchSummarize`.

Deliberate differences from Claude Code:

- No `GET /api/web/domain_info` preflight. That is an Anthropic-side allow/deny
  policy gate on a fetch poisson performs locally anyway.

## Cost accounting

Every backend that spends real tokens or dollars bypasses `provider.Stream`
(the Anthropic search/summarize helper model, and `web_ask`'s Grok backend),
which is where the main turn loop's usage accounting normally lives. Each one
still reports its own spend back out — `provider.WebHelperUsage` for the
Anthropic backends, `tools.WebCall` for the Grok backend — and
`agent.RecordWebToolCall` banks it as an `api_calls` row under a dedicated
purpose (`web_search`, `web_fetch`, `web_ask`), so `/cost`, `px cost`, and a
subagent's reported spend all include it. `tools.BindWebUsage` wires that sink
onto fetch/web_search/web_ask from `agent.ReloadConfigDependentTools`, which
already runs on every entry point and provider switch.

Pricing differs by backend:

- Anthropic (`web_search`/`fetch`): priced from the local rate table against
  `claude-haiku-4-5*` (the helper model, never the session model), plus
  Anthropic's $10/1000 `search_per_request` fee on top for each server-side
  search `usage.server_tool_use.web_search_requests` reports.
- Grok (`web_ask`): xAI's Responses API returns its own exact
  `cost_in_usd_ticks` per call (1e10 ticks = $1, tool fees and cache discounts
  already included), recorded verbatim instead of re-priced locally.
- exa (`web_ask` fallback), tavily (`web_ask`), DuckDuckGo (`web_search`
  default), firecrawl (`web_search`/`fetch`), and you (`web_search`) are all
  free, keyless backends and record nothing.

A row is only ever banked with real tokens or a real cost attached — a helper
call that errored before any HTTP request went out (e.g. missing xAI
credentials) reports nothing, since nothing was actually spent.
