package provider

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mq37/poisson/internal/testutil"
)

// writeTestPNG writes a tiny PNG and returns its path.
func writeTestPNG(t *testing.T) string {
	t.Helper()
	dir := testutil.TempDir(t)
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(0, 0, color.RGBA{255, 0, 0, 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "pic.png")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func imageUserMsg(path string) Message {
	return Message{Role: "user", Content: []ContentBlock{
		{Type: "image", MediaType: "image/png", ImagePath: path},
		{Type: "text", Text: "what is this?"},
	}}
}

func TestAnthropicSerializesImageBlock(t *testing.T) {
	path := writeTestPNG(t)
	p := &AnthropicProvider{}
	ar := p.buildAnthropicRequest(&Request{
		Model: "claude-opus-5", MaxTokens: 100, Messages: []Message{imageUserMsg(path)},
	}, false)
	last := ar.Messages[len(ar.Messages)-1]
	var img *anthropicContentBlock
	for i := range last.Content {
		if last.Content[i].Type == "image" {
			img = &last.Content[i]
		}
	}
	if img == nil {
		t.Fatalf("no image block; got %+v", last.Content)
	}
	if img.Source == nil || img.Source.Type != "base64" || img.Source.MediaType != "image/png" || img.Source.Data == "" {
		t.Fatalf("bad image source: %+v", img.Source)
	}
}

func TestOllamaSerializesImageBlock(t *testing.T) {
	path := writeTestPNG(t)
	p := NewOllamaProvider("http://localhost:11434", "minimax-m3:cloud")
	body := p.buildOllamaRequest(&Request{Model: "minimax-m3:cloud", Messages: []Message{imageUserMsg(path)}})
	assertImageURLContent(t, body.Messages[len(body.Messages)-1].Content)
}

func TestXAISerializesImageBlock(t *testing.T) {
	path := writeTestPNG(t)
	p := &XAIProvider{}
	body := p.buildRequest(&Request{Model: "grok-build", Messages: []Message{imageUserMsg(path)}})
	assertImageURLContent(t, body.Messages[len(body.Messages)-1].Content)
}

// assertImageURLContent checks an OpenAI-style content value carries a text part
// and an image_url data URL.
func assertImageURLContent(t *testing.T, content any) {
	t.Helper()
	parts, ok := content.([]oaiContentPart)
	if !ok {
		t.Fatalf("content is %T, want []oaiContentPart", content)
	}
	var hasText, hasImage bool
	for _, part := range parts {
		if part.Type == "text" && part.Text != "" {
			hasText = true
		}
		if part.Type == "image_url" && part.ImageURL != nil && strings.HasPrefix(part.ImageURL.URL, "data:image/png;base64,") {
			hasImage = true
		}
	}
	if !hasText || !hasImage {
		t.Fatalf("content missing text/image: %+v", parts)
	}
	// Must marshal to a JSON array (OpenAI multimodal shape).
	raw, err := json.Marshal(content)
	if err != nil || !strings.HasPrefix(string(raw), "[") {
		t.Fatalf("content JSON = %s (err %v)", raw, err)
	}
}

// toolResultWithImageMsg builds a genuine "tool"-role message the way
// agent.go persists one when a tool (currently only `read` on an image
// file) loaded an image: a tool_result block immediately followed by a
// sibling "image" block. See ToolResult's doc comment for why the image
// isn't inlined as base64 text in the tool_result itself.
func toolResultWithImageMsg(path, toolCallID string) Message {
	return Message{Role: "tool", Content: []ContentBlock{
		{Type: "tool_result", ToolCallID: toolCallID, ToolResult: "Image: pic.png (image/png, 42 bytes) — see attached image."},
		{Type: "image", MediaType: "image/png", ImagePath: path},
	}}
}

// TestAnthropicEmbedsImageInToolResult verifies a tool_result carrying an
// image nests it INSIDE that tool_result's own "content" array (Anthropic's
// documented shape for e.g. its computer-use tool), not as some unrelated
// top-level block — and that the sibling text still survives alongside it.
func TestAnthropicEmbedsImageInToolResult(t *testing.T) {
	path := writeTestPNG(t)
	p := &AnthropicProvider{}
	ar := p.buildAnthropicRequest(&Request{
		Model: "claude-opus-5", MaxTokens: 100,
		Messages: []Message{toolResultWithImageMsg(path, "call_1")},
	}, false)

	if len(ar.Messages) != 1 || ar.Messages[0].Role != "user" {
		t.Fatalf("expected one coalesced user message, got %+v", ar.Messages)
	}
	blocks := ar.Messages[0].Content
	if len(blocks) != 1 || blocks[0].Type != "tool_result" || blocks[0].ToolUseID != "call_1" {
		t.Fatalf("expected exactly one tool_result block, got %+v", blocks)
	}
	var nested []anthropicContentBlock
	if err := json.Unmarshal(blocks[0].Content, &nested); err != nil {
		t.Fatalf("tool_result.content isn't a nested array: %s (%v)", blocks[0].Content, err)
	}
	if len(nested) != 2 || nested[0].Type != "text" || nested[0].Text == "" {
		t.Fatalf("expected [text, image] nested content, got %+v", nested)
	}
	if nested[1].Type != "image" || nested[1].Source == nil || nested[1].Source.Data == "" ||
		nested[1].Source.MediaType != "image/png" {
		t.Fatalf("expected a real image block nested in tool_result.content, got %+v", nested[1])
	}
}

// TestAnthropicToolResultImageMissingFileFallsBackToTextOnly proves a
// vanished image file degrades to a plain-string tool_result instead of
// failing the whole request (same graceful-skip policy as a user-attached
// image; see TestImageBlockSkippedWhenFileMissing).
func TestAnthropicToolResultImageMissingFileFallsBackToTextOnly(t *testing.T) {
	p := &AnthropicProvider{}
	ar := p.buildAnthropicRequest(&Request{
		Model: "claude-opus-5", MaxTokens: 100,
		Messages: []Message{toolResultWithImageMsg("/tmp/does-not-exist-poisson.png", "call_1")},
	}, false)
	blocks := ar.Messages[0].Content
	var text string
	if err := json.Unmarshal(blocks[0].Content, &text); err != nil {
		t.Fatalf("expected tool_result.content to fall back to a plain string, got %s (%v)", blocks[0].Content, err)
	}
	if text == "" {
		t.Fatalf("expected the original tool_result text to survive")
	}
}

// TestOpenAIEmitsImageInFunctionCallOutput verifies the Responses API's
// function_call_output.output becomes an input_text/input_image parts
// array when the tool result carries an image (per OpenAI's documented
// image-as-function-output support), not a plain string.
func TestOpenAIEmitsImageInFunctionCallOutput(t *testing.T) {
	path := writeTestPNG(t)
	p := &OpenAIProvider{}
	body := p.buildRequest(&Request{
		Model:    "gpt-5.5",
		Messages: []Message{toolResultWithImageMsg(path, "call_1")},
	})

	if len(body.Input) != 1 || body.Input[0].Type != "function_call_output" || body.Input[0].CallID != "call_1" {
		t.Fatalf("expected one function_call_output item, got %+v", body.Input)
	}
	var parts []openaiRespPart
	if err := json.Unmarshal(body.Input[0].Output, &parts); err != nil {
		t.Fatalf("Output isn't a parts array: %s (%v)", body.Input[0].Output, err)
	}
	if len(parts) != 2 || parts[0].Type != "input_text" || parts[0].Text == "" {
		t.Fatalf("expected [input_text, input_image] parts, got %+v", parts)
	}
	if parts[1].Type != "input_image" || !strings.HasPrefix(parts[1].ImageURL, "data:image/png;base64,") {
		t.Fatalf("expected a real input_image part, got %+v", parts[1])
	}
}

// TestOpenAIToolResultImageMissingFileFallsBackToPlainString mirrors the
// Anthropic fallback test — a vanished file must not corrupt or fail the
// function_call_output, just degrade to its plain text.
func TestOpenAIToolResultImageMissingFileFallsBackToPlainString(t *testing.T) {
	p := &OpenAIProvider{}
	body := p.buildRequest(&Request{
		Model:    "gpt-5.5",
		Messages: []Message{toolResultWithImageMsg("/tmp/does-not-exist-poisson.png", "call_1")},
	})
	var text string
	if err := json.Unmarshal(body.Input[0].Output, &text); err != nil {
		t.Fatalf("expected Output to fall back to a plain JSON string, got %s (%v)", body.Input[0].Output, err)
	}
	if text == "" {
		t.Fatalf("expected the original tool_result text to survive")
	}
}

// TestOllamaEmitsFollowupImageMessageForToolResult verifies a tool_result's
// sibling image arrives as a separate role:"user" message right after the
// role:"tool" message — chat-completions-style APIs don't reliably support
// image content inside a tool-role message, but an ordinary user-role image
// message always works, and this format has no alternation constraint.
func TestOllamaEmitsFollowupImageMessageForToolResult(t *testing.T) {
	path := writeTestPNG(t)
	p := NewOllamaProvider("http://localhost:11434", "minimax-m3:cloud")
	body := p.buildOllamaRequest(&Request{
		Model:    "minimax-m3:cloud",
		Messages: []Message{toolResultWithImageMsg(path, "call_1")},
	})

	if len(body.Messages) != 2 {
		t.Fatalf("expected [tool, user(image)], got %d messages: %+v", len(body.Messages), body.Messages)
	}
	if body.Messages[0].Role != "tool" || body.Messages[0].ToolCallID != "call_1" {
		t.Fatalf("message 0 should be the tool result, got %+v", body.Messages[0])
	}
	if body.Messages[1].Role != "user" {
		t.Fatalf("message 1 should be the follow-up image message, got %+v", body.Messages[1])
	}
	assertImageOnlyContent(t, body.Messages[1].Content)
}

// TestXAIEmitsFollowupImageMessageForToolResult mirrors the Ollama test —
// xAI uses the same chat-completions-style shape.
func TestXAIEmitsFollowupImageMessageForToolResult(t *testing.T) {
	path := writeTestPNG(t)
	p := &XAIProvider{}
	body := p.buildRequest(&Request{
		Model:    "grok-build",
		Messages: []Message{toolResultWithImageMsg(path, "call_1")},
	})

	if len(body.Messages) != 2 {
		t.Fatalf("expected [tool, user(image)], got %d messages: %+v", len(body.Messages), body.Messages)
	}
	if body.Messages[0].Role != "tool" || body.Messages[0].ToolCallID != "call_1" {
		t.Fatalf("message 0 should be the tool result, got %+v", body.Messages[0])
	}
	if body.Messages[1].Role != "user" {
		t.Fatalf("message 1 should be the follow-up image message, got %+v", body.Messages[1])
	}
	assertImageOnlyContent(t, body.Messages[1].Content)
}

// assertImageOnlyContent checks an OpenAI-style content value carries an
// image_url data URL and no text part — the follow-up image message built
// for a tool_result's sibling image has no caption text (openAIUserContent
// omits the text part entirely when text == "").
func assertImageOnlyContent(t *testing.T, content any) {
	t.Helper()
	parts, ok := content.([]oaiContentPart)
	if !ok {
		t.Fatalf("content is %T, want []oaiContentPart", content)
	}
	if len(parts) != 1 || parts[0].Type != "image_url" || parts[0].ImageURL == nil ||
		!strings.HasPrefix(parts[0].ImageURL.URL, "data:image/png;base64,") {
		t.Fatalf("expected exactly one image_url part, got %+v", parts)
	}
}

func TestImageBlockSkippedWhenFileMissing(t *testing.T) {
	// A vanished /tmp file must not fail the request — the block is dropped.
	p := NewOllamaProvider("http://localhost:11434", "minimax-m3:cloud")
	body := p.buildOllamaRequest(&Request{Model: "minimax-m3:cloud", Messages: []Message{
		{Role: "user", Content: []ContentBlock{
			{Type: "image", MediaType: "image/png", ImagePath: "/tmp/does-not-exist-poisson.png"},
			{Type: "text", Text: "hi"},
		}},
	}})
	// No usable image → content collapses to the plain text string.
	if got := body.Messages[len(body.Messages)-1].Content; got != "hi" {
		t.Fatalf("content = %v, want \"hi\" (missing image dropped)", got)
	}
}
