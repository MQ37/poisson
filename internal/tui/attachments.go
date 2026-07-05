package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"poisson/internal/agent"
	"poisson/internal/imaging"
	"poisson/internal/provider"
)

// attachment is an image staged for the next user message (already downscaled
// and written to /tmp by the imaging package).
type attachment struct {
	Path      string // /tmp png
	MediaType string
	Name      string // display name (chip)
	Size      int    // bytes on disk
}

var imageExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true,
}

func isImagePath(p string) bool { return imageExts[strings.ToLower(filepath.Ext(p))] }

// attachImageBytes downscales raw image bytes, writes them to /tmp, and stages
// the result. Caller holds t.mu.
func (t *TUI) attachImageBytes(data []byte, name string) error {
	path, mt, err := imaging.Process(data)
	if err != nil {
		return err
	}
	return t.stageAttachment(path, mt, name)
}

// attachImageFile downscales an image file, writes the result to /tmp, and
// stages it. Caller holds t.mu.
func (t *TUI) attachImageFile(srcPath string) error {
	path, mt, err := imaging.ProcessFile(srcPath)
	if err != nil {
		return err
	}
	return t.stageAttachment(path, mt, filepath.Base(srcPath))
}

func (t *TUI) stageAttachment(path, mediaType, name string) error {
	size := 0
	if fi, err := os.Stat(path); err == nil {
		size = int(fi.Size())
	}
	if name == "" {
		name = filepath.Base(path)
	}
	t.pendingAttachments = append(t.pendingAttachments, attachment{
		Path: path, MediaType: mediaType, Name: name, Size: size,
	})
	return nil
}

// attachImageRefs stages every @image.png reference as an attachment and strips
// it from the returned text. Non-image @refs are left untouched for
// expandAtFiles to inline. Caller holds t.mu.
func (t *TUI) attachImageRefs(input string) (string, error) {
	var firstErr error
	cleaned := atFileRe.ReplaceAllStringFunc(input, func(match string) string {
		path := match[1:]
		if !isImagePath(path) {
			return match
		}
		if err := t.attachImageFile(path); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("attach @%s: %w", path, err)
			}
			return match
		}
		return ""
	})
	return cleaned, firstErr
}

// takeAttachmentsForSend clears the staged attachments and returns them as agent
// image attachments. If the current model has no vision support it warns and
// drops them. Caller holds t.mu.
func (t *TUI) takeAttachmentsForSend() []agent.ImageAttachment {
	atts := t.pendingAttachments
	t.pendingAttachments = nil
	if len(atts) == 0 {
		return nil
	}
	if !t.modelSupportsVision() {
		t.scroll.appendRaw(styleError, fmt.Sprintf(
			"⚠ %s does not support images — %d attachment(s) ignored",
			t.agent.Model(), len(atts)))
		return nil
	}
	out := make([]agent.ImageAttachment, len(atts))
	for i, a := range atts {
		out[i] = agent.ImageAttachment{Path: a.Path, MediaType: a.MediaType}
	}
	return out
}

func (t *TUI) modelSupportsVision() bool {
	if t.agent == nil {
		return false
	}
	s, ok := provider.GetModelSettings(t.agent.Provider().ID(), t.agent.Model())
	return ok && s.Vision
}

func (t *TUI) clearAttachments() { t.pendingAttachments = nil }

// attachmentRows is the number of rows the attachment chips occupy (0 or 1).
func (t *TUI) attachmentRows() int {
	if len(t.pendingAttachments) == 0 {
		return 0
	}
	return 1
}

// renderAttachmentRow renders the staged-image chips on one line.
func (t *TUI) renderAttachmentRow(width int) string {
	chips := make([]string, len(t.pendingAttachments))
	for i, a := range t.pendingAttachments {
		chips[i] = fmt.Sprintf("🖼 %s · %s", a.Name, humanBytes(a.Size))
	}
	label := "  " + strings.Join(chips, "   ")
	return dim + truncatePlain(label, width) + reset
}
