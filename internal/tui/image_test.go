package tui

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"poisson/internal/provider"
)

func testPNGBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 200, 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func writeTestImage(t *testing.T, dir, name string, w, h int) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, testPNGBytes(t, w, h), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// makeVision registers the fake test model as vision-capable for the test.
func makeVision(t *testing.T) {
	t.Helper()
	provider.KnownModels["fake/test-model"] = provider.ModelSettings{ContextWindow: 8192, Vision: true}
	t.Cleanup(func() { delete(provider.KnownModels, "fake/test-model") })
}

func lockRun(t *TUI, fn func()) {
	t.mu.Lock()
	defer t.mu.Unlock()
	fn()
}

// TestAttachImageRef stages an @image and leaves text-file @refs for inlining.
func TestAttachImageRef(t *testing.T) {
	env := newTUIIntegEnv(t, nil)
	t.Setenv("TMPDIR", env.dir)
	img := writeTestImage(t, env.dir, "shot.png", 1500, 900)
	txt := filepath.Join(env.dir, "notes.txt")
	if err := os.WriteFile(txt, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	var cleaned string
	lockRun(env.tui, func() {
		var err error
		cleaned, err = env.tui.attachImageRefs("@" + img + " and @" + txt + " look")
		if err != nil {
			t.Fatalf("attachImageRefs: %v", err)
		}
	})
	if strings.Contains(cleaned, img) {
		t.Errorf("image ref not stripped from text: %q", cleaned)
	}
	if !strings.Contains(cleaned, txt) {
		t.Errorf("text-file ref should remain for inlining: %q", cleaned)
	}
	if len(env.tui.pendingAttachments) != 1 {
		t.Fatalf("want 1 staged attachment, got %d", len(env.tui.pendingAttachments))
	}
	if env.tui.pendingAttachments[0].Name != "shot.png" {
		t.Errorf("attachment name = %q", env.tui.pendingAttachments[0].Name)
	}
	// The staged file must be a downscaled /tmp png (<=1024 long edge).
	cfg := decodeConfig(t, env.tui.pendingAttachments[0].Path)
	if cfg.Width != 1024 {
		t.Errorf("staged width = %d, want 1024 (downscaled)", cfg.Width)
	}
}

// TestCtrlVAttachesClipboardImage verifies Ctrl+V grabs an image via the
// injectable grabber and stages it (no real wl-paste/xclip).
func TestCtrlVAttachesClipboardImage(t *testing.T) {
	env := newTUIIntegEnv(t, nil)
	t.Setenv("TMPDIR", env.dir)
	env.tui.grabImage = func() ([]byte, error) { return testPNGBytes(t, 800, 600), nil }

	lockRun(env.tui, env.tui.grabClipboardImageLocked)
	if len(env.tui.pendingAttachments) != 1 {
		t.Fatalf("Ctrl+V: want 1 attachment, got %d", len(env.tui.pendingAttachments))
	}
	if env.tui.pendingAttachments[0].Name != "clipboard" {
		t.Errorf("name = %q, want clipboard", env.tui.pendingAttachments[0].Name)
	}
}

// TestCtrlVAsyncDoesNotBlockLock verifies feedKey's Ctrl+V dispatch (the real
// key-press path, unlike the direct grabClipboardImageLocked calls above)
// returns immediately instead of blocking on the clipboard read — regression
// test for the render-freeze bug where grabClipboardImageLocked used to run
// synchronously while feedKey held t.mu for its whole call.
func TestCtrlVAsyncDoesNotBlockLock(t *testing.T) {
	env := newTUIIntegEnv(t, nil)
	t.Setenv("TMPDIR", env.dir)
	release := make(chan struct{})
	env.tui.grabImage = func() ([]byte, error) {
		<-release // never returns until the test says so
		return testPNGBytes(t, 800, 600), nil
	}
	defer close(release)

	done := make(chan struct{})
	go func() {
		_, _ = env.tui.feedKey(Key{Kind: KeyCtrl, Byte: 22})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("feedKey(Ctrl+V) blocked on the clipboard read instead of returning immediately")
	}

	// The render lock must still be free while the clipboard read is stuck.
	acquired := make(chan struct{})
	go func() {
		env.tui.mu.Lock()
		env.tui.mu.Unlock()
		close(acquired)
	}()
	select {
	case <-acquired:
	case <-time.After(2 * time.Second):
		t.Fatal("t.mu still held while clipboard read is in flight — Ctrl+V froze rendering")
	}
}

func TestCtrlVNoImageInClipboard(t *testing.T) {
	env := newTUIIntegEnv(t, nil)
	env.tui.grabImage = func() ([]byte, error) { return nil, nil }
	lockRun(env.tui, env.tui.grabClipboardImageLocked)
	if len(env.tui.pendingAttachments) != 0 {
		t.Errorf("no clipboard image should stage nothing, got %d", len(env.tui.pendingAttachments))
	}
}

// TestAttachmentChipRendered verifies the staged image shows as a chip in the
// input region (UI).
func TestAttachmentChipRendered(t *testing.T) {
	env := newTUIIntegEnv(t, nil)
	t.Setenv("TMPDIR", env.dir)
	env.tui.grabImage = func() ([]byte, error) { return testPNGBytes(t, 300, 200), nil }
	lockRun(env.tui, env.tui.grabClipboardImageLocked)

	out := env.render()
	if !strings.Contains(out, "🖼") || !strings.Contains(out, "clipboard") {
		t.Errorf("chip not rendered; screen:\n%s", out)
	}
}

// TestNonVisionModelWarnsAndDrops verifies attachments are dropped with a
// warning when the model has no vision support, and never reach the provider.
func TestNonVisionModelWarnsAndDrops(t *testing.T) {
	env := newTUIIntegEnv(t, [][]provider.StreamEvent{provider.FakeTextResponse("ok", nil)})
	t.Setenv("TMPDIR", env.dir)
	// fake/test-model has no KnownModels entry → not vision-capable.
	env.tui.grabImage = func() ([]byte, error) { return testPNGBytes(t, 400, 400), nil }
	lockRun(env.tui, env.tui.grabClipboardImageLocked)

	var imgs []interface{}
	lockRun(env.tui, func() {
		for _, a := range env.tui.takeAttachmentsForSend() {
			imgs = append(imgs, a)
		}
	})
	if len(imgs) != 0 {
		t.Errorf("non-vision model should drop images, got %d", len(imgs))
	}
	if !strings.Contains(env.scrollText(), "does not support images") {
		t.Errorf("expected warning in scrollback, got:\n%s", env.scrollText())
	}
}

// TestSubmitSendsImageToProvider drives the real submit() path end-to-end and
// verifies the image block reaches the (mocked) provider.
func TestSubmitSendsImageToProvider(t *testing.T) {
	makeVision(t)
	env := newTUIIntegEnv(t, [][]provider.StreamEvent{provider.FakeTextResponse("a gradient", nil)})
	t.Setenv("TMPDIR", env.dir)
	img := writeTestImage(t, env.dir, "pic.png", 640, 480)

	lockRun(env.tui, func() {
		if err := env.tui.submit("@" + img + " describe"); err != nil {
			t.Fatalf("submit: %v", err)
		}
	})

	// submit runs the turn in a goroutine; wait until it finishes (Thinking
	// flips false under t.mu, which synchronizes the provider write for us).
	waitFor(t, func() bool {
		env.tui.mu.Lock()
		defer env.tui.mu.Unlock()
		return !env.tui.status.Thinking
	})

	req := env.prov.LastRequest()
	if req == nil {
		t.Fatal("provider was never called")
	}
	var sawImage bool
	for _, m := range req.Messages {
		for _, b := range m.Content {
			if b.Type == "image" {
				sawImage = true
			}
		}
	}
	if !sawImage {
		t.Error("provider request had no image block")
	}
	// Attachments cleared after submit.
	lockRun(env.tui, func() {
		if len(env.tui.pendingAttachments) != 0 {
			t.Errorf("attachments not cleared after submit: %d", len(env.tui.pendingAttachments))
		}
	})
}

func decodeConfig(t *testing.T, path string) image.Config {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		t.Fatalf("decode config: %v", err)
	}
	return cfg
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timeout waiting for condition")
}

