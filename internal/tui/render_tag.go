package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/mq37/poisson/internal/citetag"
)

// parseRenderTag and the disk/git resolution behind renderFileWidget live in
// internal/citetag now — shared with internal/agent's render-tag validation
// (see agent.go's turn loop), which needs the exact same resolver but can't
// import this package (tui already imports agent, so the reverse would be
// circular). This file keeps only the ANSI/box-painting part.
func parseRenderTag(line string) (file, ref string, from, to int, ok bool) {
	return citetag.ParseTag(line)
}

// renderFileWidget turns one <render> citation into a bordered code box —
// same shape as a fenced code block, but the content is read fresh (disk,
// or a git ref via `git show`) rather than anything already in the model's
// context, so showing a snippet to the human costs no output tokens. Errors
// (missing file, bad ref, sensitive path, timeout) render as a one-line
// message inside the box instead of failing the whole message.
func renderFileWidget(file, ref string, from, to, width int, prefix string) []string {
	body, effFrom, effTo, err := citetag.Resolve(file, ref, from, to)
	title := file
	if ref != "" {
		title = ref + ":" + file
	}
	if err != nil {
		return renderCodeBlock(title, "", "render error: "+err.Error(), width, prefix)
	}
	if effTo > 0 {
		title = fmt.Sprintf("%s:%d-%d", title, effFrom, effTo)
	}
	return renderCodeBlock(title, langFromExt(file), body, width, prefix)
}

// langFromExt maps a file extension to the highlight/lang key
// layoutRichMarkdown already recognizes (see langKeywords) — "" falls back
// to the same plain-yellow rendering an unknown fenced-code language gets.
func langFromExt(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".js", ".jsx", ".mjs":
		return "javascript"
	case ".ts", ".tsx":
		return "typescript"
	case ".sh", ".bash", ".zsh":
		return "bash"
	case ".yml", ".yaml":
		return "yaml"
	case ".json":
		return "json"
	case ".md":
		return "markdown"
	default:
		return ""
	}
}
