package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"poisson/internal/provider"
	"poisson/internal/store"
	"poisson/internal/testutil"
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
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, make(chan OutputEvent, 64), func(context.Context, string, string, string) bool { return true })

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
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, make(chan OutputEvent, 64), func(context.Context, string, string, string) bool { return true })

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
	fp.SetResponses([][]provider.StreamEvent{provider.FakeTextResponse("summary", nil)})
	a := NewAgent(s, fp, newTestRegistry("."), newTestConfig(), sid, make(chan OutputEvent, 8), func(context.Context, string, string, string) bool { return true })

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
