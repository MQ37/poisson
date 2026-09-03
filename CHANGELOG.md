# Changelog

All notable changes to this project are documented here.
Format follows [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).
## [0.1.0] - 2026-09-03


### Added

- V2 alt-screen REPL with Ollama v1 token counting

- Phase A smoothness — dirty render, spinners, approval modal

- Phase B1 block document model for scrollback

- Phase B2 inline markdown rendering for assistant blocks

- Phase B3-B5 rich scrollback content

- Phase C interactive UX, GFM tables, and /btw side-steer

- Grok-style header, input chrome, and Ctrl+F search

- PR-16 expandable tool result cards

- PR-15 mouse wheel scroll and click handlers

- PR-18 OSC 52 clipboard yank via Ctrl+Y

- Full-width btw box and Tab conversation focus mode

- UX polish — session hydration, overlays, hints, history

- Inject skills into system prompt and complete /reload

- Effort picker on Ctrl+L, remove Ctrl+Y from help

- Propagate reasoning effort and default it to a visible level

- Show hint when toggling thinking blocks with Ctrl+T

- Fast-path package-install commands to human approval

- Fast-path destructive commands (rm, rmdir, find -delete) to high

- Fast-path untrusted-exec commands (npx, pnpm dlx, pipx run) to high

- Compact subagent widget; isolate child from main convo

- Pin running subagents above convo, fix runtime visibility, kill process group on cancel

- Give children every tool except subagent

- Queue messages typed during a turn, send them after it ends

- Add /status command

- Load a file's AGENTS.md on demand, once per epoch

- Add kimi-k2.7-code:cloud, fix minimax-m3 effort metadata

- Image content blocks + downscaling + provider serialization

- Paste images via @file and Ctrl+V

- Add gpt-5.5 via ChatGPT Codex subscription

- Add headless -p/--print single-prompt mode

- -p provider selection via --model provider/model

- Raise inline tool-output cap 5KB -> 50KB

- Ctrl+G to expedite running subagents

- Show empty-response retries in the conversation

- Delete sessions from the Ctrl+S picker (Ctrl+D + confirm)

- Show 'Ctrl+D del' in the session picker footer

- Make /btw a full-width scrollable panel like bash approval

- Answer side questions with full conversation context + cache reuse

- Allow Tab-to-convo + scrolling while a bash approval is pending

- Anchor+trailing context estimate; fixed-reserve compaction limit

- Deny stops the turn; show turns + context in running status bar

- Show 'wrapping up' on subagent cards when expedited (Ctrl+G)

- Add claude-sonnet-5 + adaptive reasoning; config model = provider/model

- Mouse drag-select + Ctrl+Y copy; harden Ctrl+G; live subagent turns

- Colored expandable diffs for edit/write tool cards

- @file references show a collapsible card, not an inline dump

- Optional reason prompt when denying a bash command

- Always-on terse "caveman" communication style

- Declare custom model metadata via config.toml [models.*]

- Network-failure resilience with exponential backoff

- A non-empty deny reason continues the turn instead of stopping it

- Add gpt-5.5 shadow pricing

- Set the terminal window title to "px - <session>"

- Prefix the window title with "O" while approval is pending

- /btw can call read-only tools to ground its answer

- Nudge the model toward read/ls/glob/search over bash

- Cancel a running turn with Esc instead of Ctrl+C

- /btw renders answers with the same markdown renderer as the main conversation

- Search supports before/after context lines like grep -A/-B

- Read output is always numbered like search's own

- Fetch works on every provider, not just Ollama

- Show Anthropic 5h/7-day usage limits in the header

- OpenAI/Codex usage limits + /openai-reset-usage

- Add GPT-5.6 Sol/Terra/Luna

- Show ~ prefix in window title while agent is processing

- Ctrl+N filters session picker to explicitly named sessions

- Advisory hints for bash tool misuse, fix WaitDelay false failure, edit fuzzy-match hint

- Split exa_search into web_ask + web_search, add grok provider

- Memoize repeat reads, prune stale reads at compaction, deliver queued messages at the next turn iteration

- Bake 8 skills into the binary via go:embed

- Rename module to github.com/mq37/poisson, install via go install

- Hard-gate git commit — always human, never LLM

- Update create pr and issue skills

- Deterministic bash fast-path, symlink-aware guard, fast/paranoid mode, /btw bash + approval origin

- Harden edit; add grep, glob, batch

- Sticky bash cwd and env in RAM (tier 1)

- Require bash description, allow subagent in batch, lenient read ranges

- Add /classifier-model to change the bash-risk classifier model

- Allow bash inside batch, keep nested batch denied

- Track schema version via PRAGMA user_version

- Average the widget's tok/s, bank classifier spend before approvals

- Add cost-eval for per-model token and cost comparison

- Per-provider bash-risk classifier model

- Show the classifier model in /status and allow /status mid-turn

- Default to gpt-5.6-terra and refresh model docs

- Make list modals span the full terminal width

- Add feature-impact for blast-radius scouting

- Add Anthropic backends to web_search and fetch

- Show recorded cost in the subagent widget

- Sandbox_cp recurses into directories; bash tool cards show sandboxId

- Create_sandbox no longer auto-provisions a /tmp workspace

- Add builtin sandbox skill

- Add Firecrawl (search+fetch) and Tavily (web_ask) keyless providers

- Add px resume <session-id>, unify session id display

- Sandbox_resurrect tool, stopped-container nudge on bash

- Add OpenRouter provider

- Add <render> citation tag for file/git excerpts

- Agent-callable set_title tool with title history

- Auto-resolve <render ref=...> path found under a nested repo

- Redact secret-shaped values from tool output

- Classifier auto-denies a command that leaks a secret to stdout

- Auto-retry a <render> citation that fails to resolve

- Replace startup chart with sigma banner

- Add claude-fable-5 model

- Add --manual flag to px login for headless/SSH sessions

- Per-call model/effort override, same provider only

- Show per-subagent model/effort in the widget

- Add Yolo approval mode, third Shift+Tab speed

- Manual GitHub Actions release workflow + CHANGELOG.md


### Changed

- Centralize provider bootstrap in factory.go

- Unify tool registry construction in BuildRegistry

- Extract shared filterableListOverlay for picker/palette

- Unify keyboard input through Decoder.Push

- Share OpenAI-compatible SSE pump for xAI and Ollama

- Split tui.go into focused modules

- Make bash risk LLM-only, drop deterministic allowlist

- Remove vestigial extra_safe allowlist config

- Remove dead code (orphaned renderer, unused guard API, duplicated constants)

- Remove 25 more dead functions

- Remove 15 prod-dead functions and their tests

- Remove dead struct fields

- Fix the valid minors from the code review

- Drop dead fork db fields; sync README with code

- Centralize provider list in config.Providers registry

- Dedup 4 scattered-hardcode maintainability issues

- Fix 8 scattered-hardcode/duplication maintainability issues


### Documentation

- Add TUI redesign plan (split-screen alt-screen layout)

- Mark PR-24 acceptance criteria [x], reconcile 🚧/stale items, update status and effort to 100% complete

- Add README landing page + Poisson-distribution logo

- Document automatic caching + keep_alive limitation

- Add TODO.md with the editor.feed duplicate-parser cleanup

- Read's description also nudges away from bash sed -n

- Fetch's description nudges toward doc retrieval, not HTTP testing

- Fix two stale claims — fetch scope and cancel keybind

- Message queueing now delivered at next iteration, not turn-end

- Document adding a custom / unlisted model to config.toml

- Clarify [<provider>].model sets that provider's default

- Note edit/write diff paint semantics; drop stale TUI redesign plan

- Note bash sticky vs session-cwd file tools

- Flag scattered provider registry in TODO

- Document llamacpp provider in README

- Link alpaca for llama.cpp session management

- Trim Features and other verbose sections, keep the facts

- Document firecrawl, tavily, you.com providers and the MCP client

- Fix stale/false claims, cut redundant prose

- Fix drift across docs/*.md, archive shipped TUI UX plan

- Harden review lenses against categories caught in apify-mcp-server#1202/#709

- Mention <render> file citation feature

- Add leaked tool-call recovery to README features

- Add OpenRouter, expand custom-provider docs, trim, drop stale count

- Trim Providers & models to a provider list, add bastion example

- Mention Tab focus-switch keybind

- Shrink Dependencies section to one line

- Trim skills table, config duplication, classifier explainer

- Name <invoke> tag, fix whose template it is

- Compress Features section

- Fix retry-count wording (maxRenderTagRetries=2, not one chance)

- Document top-level options

- Server mode + web UI architecture plan


### Fixed

- Provider accuracy and REPL usability

- Approval prompt auto-denied during running prompt

- Approval wiring and Phase A edge-case bugs

- Child bash approval + kitty Enter in approval modal

- Phase B bugs found in verification pass

- Row-based scrollback navigation for rich blocks

- Kitty scroll keys and completion overlay ghosting

- Scout bug fixes for Kitty keys, scroll, and rendering

- Address reviewer findings for PR-24 - always synthesize purpose from guard reason on missing desc (show in Purpose:); nonblock+early store+unknown-as-deny for approval chan to prevent deadlock/hang; update v2 render to match plan target modal (title inline, 3 body lines); harmonize allow text; update AC text

- Round2 polish - indent box body content with 2 spaces to match plan target modal example; sync plan step wording for where guard reason fallback is synthesized

- Polish command palette and picker overlays

- Address UX review findings across overlays and approval

- Unfreeze bash approval and restore editor arrow keys

- Scout round 2 across tui, agent, store, tools, and cli

- Centralize keyboard decoding and overlay input UX

- Decode Kitty Ctrl+S and open session picker

- Fix mutex deadlocks in feedKey shortcut paths

- Scrollable approval overlay for long bash scripts

- Address scout UX bugs and long-script approval UI

- Ctrl+C priority, clean exit, and UX polish

- Scout round 2 polish, remove Ctrl+Y, drop /models

- Address scout round 3 findings

- Repaint full viewport when streaming at bottom

- Scout fixes, intro preservation, remove classic TUI

- Compact full conversation, auto-trigger at 85%

- Wait indefinitely for human approval, serialize round-trips

- Guard OAuth auth map and handle truncated streams

- Restore terminal on SIGTERM/SIGHUP

- Kill bash process group on timeout/cancel

- Treat newline as a command separator

- Resolve first-release blockers

- Bound HTTP response reads and add timeouts

- Parse reasoning content from OpenAI-compatible SSE streams

- Apply italic+dim to all thinking text, not just first line

- Don't truncate model name in header bar

- Guard nil provider, empty assistant, compaction growth, FTS atomicity

- Validate default effort against model's supported levels

- Keep conversation visible on cancel instead of wiping it

- Remaining minor bugs from scout report

- Show Ollama as 'no auth needed' instead of 'not configured'

- Dedup stuttered subagent name in widget

- Stop spinner animation from duplicating subagent widget content

- Don't auto-load ancestor AGENTS.md from a subdirectory

- Guard nil context tracker on struct-literal Agents

- Keep the post-compaction tail starting with a user turn

- Don't hard-fail on files with a very long single line

- Drive the model picker from the curated registry, not /api/tags

- Show gpt-5.5 context window before first message

- Parse YAML block-scalar frontmatter descriptions

- Enable prompt caching to cut token cost

- Send prompt_cache_key to enable Codex prompt caching

- Count cached tokens in context-window usage

- Use 1-hour cache TTL to stop fast quota burn

- Deterministic tool order so Anthropic prompt cache hits

- Classify bash risk in one round at the model's lowest effort

- Cap tool output at 5KB, spill full result to /tmp

- Reset tool counts on /new and refresh context window on model switch

- Don't cap classifier tokens — always-thinking models returned empty

- Count system prompt + tool defs in the context counter

- Harden ls hidden-file filter, xAI OAuth timeouts, provider error reads

- Accept multiple space-separated paths like ripgrep

- Block switching to unconfigured providers; fix stale/unsaved UX

- Add /status to the Ctrl+P command palette

- Show Ctrl+G:expedite in the idle hint line too

- Expand @dir to a one-level listing instead of erroring

- Retry transient empty model responses instead of erroring

- Stop strangling output with a tiny max_tokens; continue on max_tokens

- Let /btw launch during a live turn (its whole point)

- Animate the /btw spinner while it streams

- Session picker lists all sessions (scroll with arrows)

- Use max(real, estimate) for context usage, drop stale post-compaction usage

- Style every wrapped line, not just the first

- Make auto-compaction actually trigger for subagents

- Stop the input separator from duplicating on shrink

- Up/down arrows no longer swallowed in multi-line input

- SanitizeControls was eating every newline alongside a tab

- Multi-line message no longer counts as multiple conversation turns

- Drained queue renders as one bubble, matching how it's sent+resumed

- A keystroke sharing a chunk with a mouse-wheel event isn't dropped

- Only send the effort beta flag on adaptive-thinking requests

- Subagent no longer falls back to a hardcoded model

- Stop auto-scrolling one line at a time while scrolled up mid-stream

- Close bash-guard bypasses and gate sensitive-file tool access

- Nil-provider panic, compaction livelock, approval TOCTOU, SSE leak

- Silent AGENTS.md truncation, TUI render freeze/spam, xai silent stream close

- Compaction request must always end with a user message

- Compaction notice's token counts must include the summary

- Footer hint line goes stale after a turn finishes

- /name works while busy; harden turn-completion repaint

- /name from the Ctrl+P palette prefills instead of auto-running

- Revert turn-completion repaint from full back to targeted

- Footer hint line was never truncated to the terminal width

- Resuming a compacted session now shows the full history

- Pasted/attached images vanished from the conversation history

- Session picker showed 0 messages for a fully-compacted session

- Edit accepts a flat single-edit shape and recovers double-encoded arrays

- A fence marker inside inline code corrupted the rest of the message

- Cancelling mid-response now persists the partial answer

- Header spinner now animates while compacting

- Mouse wheel scroll was inverted inside expanded tool cards

- Edit diff card showed zero edits for the flat/string-encoded shapes

- Add missing claude-sonnet-5 rates

- Guard context across model switches

- Restore session model in print mode

- Support provider-qualified models

- Charge one-hour Anthropic cache writes

- Record auxiliary model calls

- Ignore auxiliary usage anchors

- Preserve API error details

- Correct GPT-5.6 context window to 272K (confirmed live)

- Repaint header spinner during compaction

- Count flat oldText/newText shorthand in edit card header

- Image name lost on resume + tool-input normalization race

- Wrap long table cells instead of truncating

- Send placeholder content instead of null for filtered-empty messages

- Don't dump oversized @file into message, note it instead

- Approval deny-reason field supports word-wise editing

- Accept numeric-string integer params, reject ranges clearly

- Block bash stand-ins for read/search/glob/ls instead of only hinting

- Give child agents the same skill set as the main session

- Propagate --no-skills from parent to spawned children

- Don't dump raw HTML when DDG blocks with bot challenge

- Sanitize HTML bodies in HTTP error messages

- Keep 'output' field on empty tool results

- Roll subagent API cost into parent session cost

- /btw no longer sends orphaned tool_use when a tool call is mid-flight

- Retry transient mid-stream errors, audit cross-provider history replay

- Guard against leaked tool-call template corruption, add search fixed_strings

- Repair session left with dangling tool_use after a crash

- Also repair dangling tool_use retried through the broken history

- Close deterministic-escalation gap for obfuscated destructive commands

- Close 12 red-team bash-guard bypasses + fuzz-found gaps

- Force-fetch usage limits and reset ticker on switch

- Make compaction summaries detailed instead of vague

- Close secret-path auto-approve holes

- Budget against compaction model's context window

- Split cache breakpoint so compaction summary doesn't bust the stable system-prefix cache

- Remove stray truncated-byte line breaking UTF-8 validity

- Recover panicking tools instead of crashing the run

- Read tool loads images correctly for all providers

- Self-heal stale sticky cwd / bad workdir before exec

- Give the bash-risk classifier the same retry resilience as a turn

- Extend mid-stream/empty-response retry to /btw and compaction

- Close 5 resource/security/concurrency gaps found in scout pass

- Give batched subagent calls their own live widget + Ctrl+G reach

- Close stealth fingerprint gaps found via cc-sniff captures

- Stop /btw dying to a bash approval, add write/edit path expand

- Make /classifier-model instance-wide and price the child's spend correctly

- Resolve mcp_-prefixed tool names at every dispatch point

- Stop elapsed timers while an approval prompt is waiting

- Never inherit POISSON_SUBAGENT_* into a spawned child

- Show bare tool names in batch cards, not the mcp_ wire form

- Resolve capitalized bare tool names like "Glob"

- Fetch prompt-rejection message; account web tool spend

- Tag nested batch calls' provider in the collapsed summary

- Tag tool cards with the backend that actually ran, incl. default

- Serialize approval-gated tool calls, notify cancelled batched subagents

- Close batch approval-ordering gap, reap sandbox zombies, fix message seq race

- Run parallel subagents concurrently, not one at a time

- Close data race in drainEvents test helper

- TUI signal/panic terminal restore, api_calls seq race, auth.json atomicity, glob DoS, compaction double-run, seq unique constraints, risk cost logging

- Guarantee t.mu release on panic across the package

- Run batched subagents concurrently too (second half of the serial-scout bug)

- Risk-classifier bypasses, uncapped Codex tokens, batch fan-out cap, and 6 more findings

- Btw overlay leak, UTF-8 rune split, DB migration rework, CJK width, EXIF orientation, symlink dedup, risk-eval retry counting

- Replace go-runewidth dependency with a hand-rolled width table

- Guard quote-splice bypass, subagent kill escaping process group, shared auth-store mutex, html2md panic, global subagent concurrency cap, config model override, cross-process refresh race

- Force-refresh silently returned dead OAuth token unchanged on live 401

- Stop cross-process "database is locked" on shared db

- Resolve <render ref> citations via the file's own repo root

- Name resolved absolute path on <render ref=...> git-repo miss

- Freeze timers during bash risk classification too

- Reject reversed <render> line range, clarify render prompt

- /help and command palette gaps found by audit

- Add px login hint to token-refresh-failed errors

- Show subagent's model as of when it actually ran

- Model/effort silently dropped by long subagent task text

- Strip box-drawing borders from copied text; redact secrets before bash command display

- Stop redacting non-secret prose and code-shaped text

- Drop secret redaction from agent-authored command text


### Miscellaneous

- Initial commit

- Remove dead code found via deadcode -test

- Remove abandoned /undo write-path (SoftDeleteMessages)

- Remove dead locals/fields flagged by staticcheck

- Remove dead code (cmdSessions, OSC-52 clipboard, mergeScrollRows)

- Gofmt the whole tree

- Replace the SVG logo with the parchment Poisson-curve photo

- Nudge skill tool over read/bash for SKILL.md files

- Rename default Anthropic model to claude-opus-5; document grok-4.5

- Update logo

- Shrink logo 30%

- Shrink logo another 30%


