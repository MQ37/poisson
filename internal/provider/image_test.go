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
