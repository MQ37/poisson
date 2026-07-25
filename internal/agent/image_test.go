package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mq37/poisson/internal/provider"
	"github.com/mq37/poisson/internal/store"
	"github.com/mq37/poisson/internal/testutil"
)

// TestPromptWithImageAttachment verifies an image attachment becomes an image
// content block in the stored user message and reaches the provider request.
func TestPromptWithImageAttachment(t *testing.T) {
	testutil.TempHome(t)
	dir := testutil.TempDir(t)
	imgPath := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(imgPath, []byte("\x89PNG\r\n\x1a\n fake"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := newTestStore(t)
	sid := newTestSession(t, s, "test-model")
	fp := newFakeProvider()
	fp.SetResponses([][]provider.StreamEvent{provider.FakeTextResponse("a red square", nil)})
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, make(chan OutputEvent, 64), func(context.Context, string, string, string) (bool, string) { return true, "" })

	if err := a.PromptWithContext(context.Background(), "what is this?",
		ImageAttachment{Path: imgPath, MediaType: "image/png"}); err != nil {
		t.Fatalf("prompt: %v", err)
	}

	// Stored user message must carry an image block + the text.
	msgs, err := s.GetMessages(sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) == 0 || msgs[0].Role != "user" {
		t.Fatalf("no user message: %+v", msgs)
	}
	if !strings.Contains(msgs[0].Content, `"type":"image"`) || !strings.Contains(msgs[0].Content, imgPath) {
		t.Errorf("user message missing image block: %s", msgs[0].Content)
	}
	if !strings.Contains(msgs[0].Content, "what is this?") {
		t.Errorf("user message missing text: %s", msgs[0].Content)
	}

	// The provider saw the image block in its request.
	req := fp.LastRequest()
	var sawImage bool
	for _, m := range req.Messages {
		for _, b := range m.Content {
			if b.Type == "image" && b.ImagePath == imgPath {
				sawImage = true
			}
		}
	}
	if !sawImage {
		t.Error("provider request had no image block")
	}
}

// TestPromptImageOnlyNoText verifies an image with empty text produces an
// image-only user message (no empty text block).
func TestPromptImageOnlyNoText(t *testing.T) {
	testutil.TempHome(t)
	dir := testutil.TempDir(t)
	imgPath := filepath.Join(dir, "x.png")
	if err := os.WriteFile(imgPath, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	s := newTestStore(t)
	sid := newTestSession(t, s, "test-model")
	fp := newFakeProvider()
	fp.SetResponses([][]provider.StreamEvent{provider.FakeTextResponse("ok", nil)})
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, make(chan OutputEvent, 64), func(context.Context, string, string, string) (bool, string) { return true, "" })

	if err := a.PromptWithContext(context.Background(), "", ImageAttachment{Path: imgPath}); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	msgs, _ := s.GetMessages(sid)
	if !strings.Contains(msgs[0].Content, `"type":"image"`) {
		t.Errorf("missing image block: %s", msgs[0].Content)
	}
	if strings.Contains(msgs[0].Content, `"type":"text"`) {
		t.Errorf("should have no text block for image-only message: %s", msgs[0].Content)
	}
}

// TestReadToolImageBecomesSiblingImageBlock covers the read-tool image fix:
// before it, `read` on an image file inlined base64 as plain text in the
// tool_result — inert to every provider (and silently corrupted past
// maxToolOutputBytes). Now the image travels as a genuine sibling "image"
// content block in the same tool-role message, the same way a user-attached
// image does, and every provider already knows how to load + encode one.
func TestReadToolImageBecomesSiblingImageBlock(t *testing.T) {
	testutil.TempHome(t)
	dir := testutil.TempDir(t)
	imgPath := filepath.Join(dir, "shot.png")
	// Minimal 1x1 PNG.
	png := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
		0x89, 0x00, 0x00, 0x00, 0x0a, 0x49, 0x44, 0x41,
		0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
		0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00,
		0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
		0x42, 0x60, 0x82,
	}
	if err := os.WriteFile(imgPath, png, 0o644); err != nil {
		t.Fatal(err)
	}

	s := newTestStore(t)
	sid := newTestSession(t, s, "test-model")
	fp := newFakeProvider()
	fp.SetResponses([][]provider.StreamEvent{
		{
			{Type: provider.EventToolUseStart, ToolCall: &provider.ToolCall{
				ID: "call_1", Name: "read", Input: []byte(`{"path":"shot.png"}`),
			}},
			{Type: provider.EventToolUseStop, ToolCall: &provider.ToolCall{
				ID: "call_1", Name: "read", Input: []byte(`{"path":"shot.png"}`),
			}},
			{Type: provider.EventDone, Usage: &provider.Usage{InputTokens: 10, OutputTokens: 5}},
		},
		provider.FakeTextResponse("it's a red pixel", nil),
	})
	a := NewAgent(s, fp, newTestRegistry(dir), newTestConfig(), sid, make(chan OutputEvent, 64),
		func(context.Context, string, string, string) (bool, string) { return true, "" })

	if err := a.PromptWithContext(context.Background(), "what is shot.png?"); err != nil {
		t.Fatalf("prompt: %v", err)
	}

	// The persisted tool-role message must carry BOTH the tool_result and a
	// sibling image block — and the tool_result's own text must never
	// contain base64 data.
	msgs, err := s.GetMessages(sid)
	if err != nil {
		t.Fatal(err)
	}
	var toolMsg *store.Message
	for i := range msgs {
		if msgs[i].Role == "tool" {
			toolMsg = &msgs[i]
		}
	}
	if toolMsg == nil {
		t.Fatalf("no tool-role message found: %+v", msgs)
	}
	if !strings.Contains(toolMsg.Content, `"type":"tool_result"`) {
		t.Errorf("tool message missing tool_result block: %s", toolMsg.Content)
	}
	if !strings.Contains(toolMsg.Content, `"type":"image"`) {
		t.Errorf("tool message missing sibling image block: %s", toolMsg.Content)
	}
	if strings.Contains(toolMsg.Content, "base64") {
		t.Errorf("tool_result text must not inline base64 data anymore: %s", toolMsg.Content)
	}

	// The second round's request (the model's follow-up after the tool
	// call) must have actually received a real image block referencing a
	// readable file — not just metadata text.
	req := fp.LastRequest()
	var sawImage bool
	for _, m := range req.Messages {
		for _, b := range m.Content {
			if b.Type == "image" && b.ImagePath != "" {
				if _, statErr := os.Stat(b.ImagePath); statErr == nil {
					sawImage = true
				}
			}
		}
	}
	if !sawImage {
		t.Error("provider request had no real, readable image block")
	}
}

// TestCompactionReplacesImagesWithPlaceholder verifies image blocks are not fed
// to the summarizer as bytes.
func TestCompactionReplacesImagesWithPlaceholder(t *testing.T) {
	testutil.TempHome(t)
	s := newTestStore(t)
	sid := newTestSession(t, s, "test-model")
	now := time.Now().Unix()
	// A user turn with an image, then assistant + a trailing user turn to keep.
	imgMsg, _ := contentBlocksToJSON([]provider.ContentBlock{
		{Type: "image", MediaType: "image/png", ImagePath: "/tmp/whatever.png"},
		{Type: "text", Text: "look"},
	})
	for _, m := range []store.Message{
		{SessionID: sid, Role: "user", Content: imgMsg, CreatedAt: now},
		{SessionID: sid, Role: "assistant", Content: `[{"type":"text","text":"ok"}]`, CreatedAt: now + 1},
		{SessionID: sid, Role: "user", Content: `[{"type":"text","text":"more"}]`, CreatedAt: now + 2},
	} {
		mm := m
		if err := s.AppendMessage(&mm); err != nil {
			t.Fatal(err)
		}
	}
	fp := newFakeProvider()
	fp.SetResponses([][]provider.StreamEvent{provider.FakeTextResponse(padSummary("summary"), nil)})
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, make(chan OutputEvent, 8), func(context.Context, string, string, string) (bool, string) { return true, "" })

	if err := a.Compact(); err != nil {
		t.Fatalf("compact: %v", err)
	}
	// The summarization request must contain the placeholder, not an image block.
	req := fp.LastRequest()
	var sawImage, sawPlaceholder bool
	for _, m := range req.Messages {
		for _, b := range m.Content {
			if b.Type == "image" {
				sawImage = true
			}
			if b.Type == "text" && strings.Contains(b.Text, "[image]") {
				sawPlaceholder = true
			}
		}
	}
	if sawImage {
		t.Error("summarizer received a raw image block")
	}
	if !sawPlaceholder {
		t.Error("summarizer missing [image] placeholder")
	}
}
