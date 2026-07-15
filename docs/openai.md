# OpenAI provider (GPT-5.5 via ChatGPT Codex subscription)

Poisson can drive OpenAI's GPT models through a **ChatGPT Plus/Pro
subscription** — no `platform.openai.com` API key, no per-token billing. It
reuses the Codex CLI OAuth client and the ChatGPT backend, so usage is drawn
from your ChatGPT subscription quota.

This was reverse-engineered from pi.dev's Codex integration
(`repos/pi-mono/packages/ai/src/utils/oauth/openai-codex.ts` and
`.../providers/openai-codex-responses.ts`).

## Login

```
px login openai
```

PKCE (S256) authorization-code flow:

- Client ID `app_EMoamEEZ73f0CkXaXp7hrann` (the public Codex CLI client).
- Authorize at `https://auth.openai.com/oauth/authorize` with a loopback
  redirect `http://localhost:1455/auth/callback`, scope
  `openid profile email offline_access`, plus the Codex params
  `id_token_add_organizations=true`, `codex_cli_simplified_flow=true`,
  `originator=poisson`.
- A local callback server catches the code (state-validated); falls back to
  manual paste if port 1455 is busy.
- Token exchange/refresh is **form-encoded** at
  `https://auth.openai.com/oauth/token`.

Tokens are stored in `~/.poisson/auth.json` under the `openai` key
(`{access, refresh, expires}`) and auto-refreshed near expiry / on a 401.

## Requests

The subscription only works through the **Responses API** on the ChatGPT
backend (not `api.openai.com`):

- `POST https://chatgpt.com/backend-api/codex/responses` (SSE).
- Headers: `Authorization: Bearer <access>`, `chatgpt-account-id: <id>`
  (decoded from the access-token JWT claim
  `https://api.openai.com/auth.chatgpt_account_id`), `originator: poisson`,
  `OpenAI-Beta: responses=experimental`.
- Body: `{model, store:false, stream:true, instructions, input, tools,
  tool_choice:"auto", parallel_tool_calls:true, reasoning:{effort, summary}}`.

Poisson message blocks map onto Responses `input` items:

| Poisson block | Responses item |
|---|---|
| system blocks | `instructions` (joined) |
| user text / image | `message` (`input_text` / `input_image` data URL) |
| assistant text | `message` (`output_text`) |
| assistant tool_use | `function_call` (`call_id`, `name`, `arguments`) |
| tool result | `function_call_output` (`call_id`, `output`) |

SSE `response.*` events map to Poisson stream events: `output_text.delta` →
text, `reasoning_summary_text.delta` → thinking, `function_call_arguments.*` →
tool-use start/delta/stop (keyed by `output_index`), `response.completed` →
usage/done.

## Model

`gpt-5.5` — 400K context (the Codex subscription cap), vision, reasoning effort
`low | medium | high | xhigh` (Poisson's `max` maps to `xhigh`; default
`medium`). Registered in `internal/provider/models.go`.

`gpt-5.6-sol` / `gpt-5.6-terra` / `gpt-5.6-luna` — frontier / balanced /
cost-optimized tiers of the same family, 1.05M context, vision, full
`none | low | medium | high | xhigh | max` effort range. Same registration
file; pricing in `internal/pricing/pricing.go`.

## Known limitation

Encrypted reasoning items are **not** requested or replayed
(`include:["reasoning.encrypted_content"]` is omitted), so each turn re-reasons
from scratch — simpler, and avoids the "reasoning item required" pairing rules.
If OpenAI ever rejects tool-call turns that lack their reasoning items, the fix
is to request `reasoning.encrypted_content` and persist/replay the reasoning
item JSON (Poisson's `ThinkingSignature` field already exists for this).
