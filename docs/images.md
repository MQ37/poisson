# Image input (paste & @file)

Lets the user send images to vision-capable models. Two entry points only:
**`@path.png`** in the prompt and **Ctrl+V** (grab an image from the system
clipboard). Images are downscaled to save tokens, stored in `/tmp`, and sent as
provider-native image blocks.

## Decisions

| Topic | Choice |
|---|---|
| Entry points | `@file` + Ctrl+V only (no `/paste`, no keybind config) |
| Storage | `/tmp` temp files, ephemeral (lost on reboot — acceptable) |
| Downscale | long edge ≤ **1024 px**, downscale-only, aspect preserved, re-encode PNG |
| Compression | none — vision cost is pixel-driven, not byte-driven; we resize, not compress |
| Non-vision model | print a warning that the image is unsupported and **drop it** (send text only) |
| Formats in | png, jpeg, gif, webp → always re-encoded to png |

Why 1024: legible for screenshots/code, ~800–1000 vision tokens, under every
provider's own downscale cap so token cost is predictable.

## Model vision capability (`ModelSettings.Vision`)

| Model | Vision |
|---|---|
| anthropic/claude-opus-5 | ✅ |
| xai/grok-build | ✅ |
| xai/grok-4.5 | ✅ |
| ollama/minimax-m3:cloud | ✅ |
| ollama/kimi-k2.7-code:cloud | ✅ |
| ollama/glm-5.2:cloud | ❌ |

## Data model

`provider.ContentBlock` gains `Type=="image"` with `MediaType` (`image/png`) and
`ImagePath` (the `/tmp` file). `contentBlockJSON` persists `media_type` +
`image_path` (path only — the DB never stores base64, keeping `GetMessages`
cheap). Providers read the file and encode at request-build time; a missing file
(e.g. after reboot) is skipped, not fatal.

## Pipeline

```
Ctrl+V ──► grabClipboardImage() ─┐
@file  ──► imaging.ProcessFile() ─┼─► /tmp png (≤1024) ─► pendingAttachment (chip)
clipboard bytes ─ imaging.Process()┘

submit ─► vision? ── no ──► warn + drop
             │ yes
             ▼
   agent.PromptWithContext(text, images…) ─► user msg = [image blocks…, text]
             ▼
   buildRequest ─► provider serialization:
      anthropic: {type:image, source:{base64, media_type, data}}
      ollama/xai (OpenAI): content:[{type:text},{type:image_url, image_url:{url:"data:…;base64,…"}}]
```

- **imaging** (`internal/imaging`): `Process([]byte)` / `ProcessFile(path)` →
  `(path, mediaType, err)`. Decode (png/jpeg/gif/webp), downscale long edge to
  1024 (CatmullRom, `golang.org/x/image/draw`), encode png, write `/tmp`.
- **clipboard read** (`internal/tui`): Wayland `wl-paste`, X11 `xclip`
  (image/png then image/jpeg). Injectable in the TUI (`grabImage` field) so
  tests never spawn a real command.
- **token estimate**: flat per-image estimate (`imageTokenEstimate ≈ 800`), not
  the tiny path string.
- **compaction**: image blocks fed to the summarizer are replaced with a
  `[image]` text placeholder (never send base64/data URLs to it).

## Chips (UI)

Pending attachments render as a dim row above the editor:
`🖼 shot.png · png · 24KB`. Cleared on submit (or when the input is cleared).

## Tests (all mocked — FakeProvider, `testutil.TempDir/TempHome`, `/tmp`)

- imaging: downscale (long edge/aspect), no upscale, jpeg→png, temp file exists.
- persistence: image `ContentBlock` JSON round-trip.
- providers: anthropic base64 source; ollama/xai `image_url` data-URL array.
- agent: `PromptWithContext` with images → stored msg has image+text blocks;
  vision gate drops for glm-5.2.
- TUI: `@shot.png` → attachment (+ text files still inline); Ctrl+V via injected
  fake grabber → chip added; chip render output; submit to non-vision warns+drops
  (FakeProvider `LastRequest` has no image block).

## Out of scope (follow-ups)

- Rendering images inline in the terminal (Kitty/iTerm2 protocols).
- `read` tool returning a real image block (today it must not dump base64 text).
- Persisting attachments across reboot / a cleanup/prune story for `/tmp`.
