package tui

import (
	"context"
	"os/exec"
	"strings"
	"time"
)

// grabClipboardImage reads an image from the system clipboard and returns its
// raw bytes. It supports Wayland (wl-paste) and X11 (xclip). It returns nil, nil
// when the clipboard holds no image (not an error). The imaging layer re-encodes
// whatever format it gets, so PNG is preferred but JPEG is accepted.
func grabClipboardImage() ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if _, err := exec.LookPath("wl-paste"); err == nil {
		return waylandClipboardImage(ctx)
	}
	if _, err := exec.LookPath("xclip"); err == nil {
		return x11ClipboardImage(ctx)
	}
	return nil, nil
}

func waylandClipboardImage(ctx context.Context) ([]byte, error) {
	types, err := exec.CommandContext(ctx, "wl-paste", "--list-types").Output()
	if err != nil {
		return nil, nil // nothing to paste / clipboard tool unhappy — treat as no image
	}
	mt := pickImageType(string(types))
	if mt == "" {
		return nil, nil
	}
	data, err := exec.CommandContext(ctx, "wl-paste", "--type", mt).Output()
	if err != nil {
		return nil, nil
	}
	return data, nil
}

func x11ClipboardImage(ctx context.Context) ([]byte, error) {
	targets, err := exec.CommandContext(ctx, "xclip", "-selection", "clipboard", "-t", "TARGETS", "-o").Output()
	if err != nil {
		return nil, nil
	}
	mt := pickImageType(string(targets))
	if mt == "" {
		return nil, nil
	}
	data, err := exec.CommandContext(ctx, "xclip", "-selection", "clipboard", "-t", mt, "-o").Output()
	if err != nil {
		return nil, nil
	}
	return data, nil
}

// pickImageType returns the preferred image MIME type present in a newline- or
// whitespace-separated list of clipboard types (PNG first, then JPEG).
func pickImageType(list string) string {
	has := func(mt string) bool {
		for _, line := range strings.Fields(list) {
			if strings.EqualFold(strings.TrimSpace(line), mt) {
				return true
			}
		}
		return false
	}
	if has("image/png") {
		return "image/png"
	}
	if has("image/jpeg") {
		return "image/jpeg"
	}
	if has("image/jpg") {
		return "image/jpg"
	}
	return ""
}

// grabClipboardImageLocked reads the clipboard (via the injectable grabImage),
// stages any image found, and gives feedback. Caller holds t.mu. Runs the
// clipboard read synchronously — used directly by tests for deterministic
// behavior. Ctrl+V goes through grabClipboardImageAsync instead (see below),
// which does the same work without blocking the caller's lock.
func (t *TUI) grabClipboardImageLocked() {
	grab := t.grabImage
	if grab == nil {
		grab = grabClipboardImage
	}
	data, err := grab()
	t.stageClipboardResultLocked(data, err)
}

// grabClipboardImageAsync reads the clipboard off t.mu, in a goroutine, then
// re-takes the lock only to stage the result. grabClipboardImageLocked used
// to run synchronously inside feedKey, which holds t.mu for its entire call
// — since a clipboard read shells out to wl-paste/xclip (up to a 2s
// timeout), every Ctrl+V froze rendering (paint() also takes t.mu) and all
// other input for up to 2 seconds.
func (t *TUI) grabClipboardImageAsync() {
	grab := t.grabImage
	if grab == nil {
		grab = grabClipboardImage
	}
	t.setEphemeralHintLocked("reading clipboard\u2026", 5*time.Second)
	go func() {
		data, err := grab()
		t.mu.Lock()
		defer t.mu.Unlock()
		t.stageClipboardResultLocked(data, err)
	}()
}

// stageClipboardResultLocked attaches a successfully read clipboard image (or
// reports why it couldn't) and marks the screen dirty. Caller holds t.mu.
func (t *TUI) stageClipboardResultLocked(data []byte, err error) {
	if err != nil {
		t.setEphemeralHintLocked("clipboard image failed: "+err.Error(), 3*time.Second)
		return
	}
	if len(data) == 0 {
		t.setEphemeralHintLocked("no image in clipboard", 2*time.Second)
		return
	}
	if err := t.attachImageBytes(data, "clipboard"); err != nil {
		t.setEphemeralHintLocked("image error: "+err.Error(), 3*time.Second)
		return
	}
	t.setEphemeralHintLocked("image attached", 2*time.Second)
	t.dirty.markFull()
}
