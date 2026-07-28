package tui

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

const toolCardSpinnerSlot = "◌" // replaced at paint time with animated spinner

// layoutToolCard renders a tool invocation. bash/read and other non-diff tools
// collapse to a single thinking-style line; edit/write always show a borderless
// full diff (green/red background + syntax highlight + line numbers).
func layoutToolCard(b *Block, width int, _ int) []ScreenRow {
	if width < 12 {
		width = 12
	}
	name := b.meta.ToolName
	if name == "" {
		name = "tool"
	}

	// edit/write: always-expanded borderless diff.
	if isDiffTool(name) && b.meta.ToolError == "" {
		return layoutDiffTool(b, width)
	}

	// Compact single-line tools (bash, read, fetch, …) — collapsed by default,
	// including while still running. Expand (click/Ctrl+E) reveals command + result.
	if !b.meta.Expanded {
		text := formatToolCollapsed(b, width)
		return []ScreenRow{{Text: text, Tag: RowTag{BlockID: b.id, RowIdx: 0}}}
	}

	var chunks []string
	chunks = append(chunks, formatToolExpandedHeader(b))

	// Body: input summary / command.
	for _, ln := range toolExpandedInputLines(name, b.meta.ToolInput, width) {
		chunks = append(chunks, ln)
	}

	if b.meta.ToolDone {
		style := fgGray
		if b.meta.ToolError != "" {
			style = fgRed
		}
		for _, ln := range toolCardExpandedResultLines(b, width) {
			chunks = append(chunks, style+ln+reset)
		}
	}

	rows := make([]ScreenRow, len(chunks))
	for i, chunk := range chunks {
		rows[i] = ScreenRow{Text: chunk, Tag: RowTag{BlockID: b.id, RowIdx: i}}
	}
	return rows
}

// layoutDiffTool renders edit/write: one header line + full colored diff, no box.
func layoutDiffTool(b *Block, width int) []ScreenRow {
	var chunks []string
	chunks = append(chunks, formatDiffToolHeader(b))

	lang := toolLangFromInput(b.meta.ToolName, b.meta.ToolInput)
	diff := toolDiffLines(b.meta.ToolName, b.meta.ToolInput, b.meta.DiffBase)
	for _, ln := range renderDiffLines(diff, width, lang) {
		chunks = append(chunks, ln)
	}

	// Still surface a failure path even for diff tools if ToolError is set —
	// callers gate on ToolError == "" before calling us, but keep a safety net.
	if b.meta.ToolError != "" {
		chunks = append(chunks, fgRed+"✗ "+b.meta.ToolError+reset)
	}

	rows := make([]ScreenRow, len(chunks))
	for i, chunk := range chunks {
		rows[i] = ScreenRow{Text: chunk, Tag: RowTag{BlockID: b.id, RowIdx: i}}
	}
	return rows
}

func formatDiffToolHeader(b *Block) string {
	name := b.meta.ToolName
	preview := toolInputPreview(name, b.meta.ToolInput)
	mark := "✓"
	if !b.meta.ToolDone {
		mark = toolCardSpinnerSlot
	} else if b.meta.ToolError != "" {
		mark = "✗"
	}
	title := titleCaseTool(name)
	meta := ""
	if b.meta.ToolDone {
		if b.meta.DurationMs > 0 {
			meta = fmt.Sprintf(" · %.1fs", float64(b.meta.DurationMs)/1000)
		}
		meta += toolCardSpeedSuffix(b)
	}
	return dim + italic + mark + " " + title + " · " + reset + dim + preview + meta + reset
}

// formatToolCollapsed is the one-line thinking-style header for bash/read/etc.
// Shape: "▸ Bash - reason (0.4s, 55 tok/s)" — reason is the bash description
// or the path/url/etc. for other tools. While still running the mark is the
// spinner slot and duration is live elapsed.
func formatToolCollapsed(b *Block, width int) string {
	name := b.meta.ToolName
	if name == "" {
		name = "tool"
	}
	title := titleCaseTool(name)
	reason := toolCollapsedReason(name, b.meta.ToolInput)
	if reason == "" {
		reason = "…"
	}

	mark := "▸"
	switch {
	case !b.meta.ToolDone:
		mark = toolCardSpinnerSlot
	case b.meta.ToolError != "":
		mark = "✗"
	}

	metaParts := []string{}
	durMs := b.meta.DurationMs
	if !b.meta.ToolDone && !b.meta.StartedAt.IsZero() {
		durMs = time.Since(b.meta.StartedAt).Milliseconds()
	}
	if durMs > 0 {
		metaParts = append(metaParts, fmt.Sprintf("%.1fs", float64(durMs)/1000))
	}
	if b.meta.TokensPerSec > 0 {
		metaParts = append(metaParts, fmt.Sprintf("%.0f tok/s", b.meta.TokensPerSec))
	}
	meta := ""
	if len(metaParts) > 0 {
		meta = " (" + strings.Join(metaParts, ", ") + ")"
	}

	// "▸ Bash - reason (meta)" — trim reason to fit width.
	prefix := mark + " " + title + " - "
	suffix := meta
	avail := width - visibleWidth(prefix) - visibleWidth(suffix)
	if avail < 8 {
		avail = 8
	}
	reason = truncateToWidth(reason, avail)

	style := dim + italic
	if b.meta.ToolError != "" {
		style = fgRed + italic
	}
	return style + prefix + reason + suffix + reset
}

func formatToolExpandedHeader(b *Block) string {
	name := b.meta.ToolName
	if name == "" {
		name = "tool"
	}
	title := titleCaseTool(name)
	reason := toolCollapsedReason(name, b.meta.ToolInput)

	mark := "▾"
	if !b.meta.ToolDone {
		mark = toolCardSpinnerSlot
	} else if b.meta.ToolError != "" {
		mark = "✗"
	}

	metaParts := []string{}
	if b.meta.ToolDone {
		if b.meta.DurationMs > 0 {
			metaParts = append(metaParts, fmt.Sprintf("%.1fs", float64(b.meta.DurationMs)/1000))
		}
		if b.meta.TokensPerSec > 0 {
			metaParts = append(metaParts, fmt.Sprintf("%.0f tok/s", b.meta.TokensPerSec))
		}
	} else {
		metaParts = append(metaParts, "…")
	}
	meta := ""
	if len(metaParts) > 0 {
		meta = " (" + strings.Join(metaParts, ", ") + ")"
	}

	head := mark + " " + title
	if reason != "" {
		head += " - " + reason
	}
	style := dim + italic
	if b.meta.ToolError != "" {
		style = fgRed + italic
	}
	return style + head + meta + reset
}

// toolCollapsedReason is the short purpose shown on the collapsed line:
// bash description, read path, fetch url, etc.
func toolCollapsedReason(toolName string, input []byte) string {
	if len(input) == 0 {
		return ""
	}
	switch toolName {
	case "bash":
		var in struct {
			Description string `json:"description"`
			Command     string `json:"command"`
		}
		if json.Unmarshal(input, &in) == nil {
			if d := strings.TrimSpace(in.Description); d != "" {
				return previewText(d, 120)
			}
			if in.Command != "" {
				return previewText(in.Command, 80)
			}
		}
	case "read", "@file":
		var in struct {
			Path string `json:"path"`
		}
		if json.Unmarshal(input, &in) == nil && in.Path != "" {
			return previewText(in.Path, 100)
		}
	case "write", "edit":
		return toolInputPreview(toolName, input)
	case "grep", "glob", "search":
		var in struct {
			Pattern string `json:"pattern"`
		}
		if json.Unmarshal(input, &in) == nil && in.Pattern != "" {
			return previewText(in.Pattern, 100)
		}
	case "batch":
		return toolInputPreview(toolName, input)
	case "fetch":
		var in struct {
			URL string `json:"url"`
		}
		if json.Unmarshal(input, &in) == nil && in.URL != "" {
			return previewText(in.URL, 100)
		}
	case "web_search", "web_ask":
		var in struct {
			Query string `json:"query"`
		}
		if json.Unmarshal(input, &in) == nil && in.Query != "" {
			return previewText(in.Query, 100)
		}
	case "skill":
		var in struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(input, &in) == nil && in.Name != "" {
			return previewText(in.Name, 80)
		}
	case "subagent":
		var in struct {
			Name string `json:"name"`
			Task string `json:"task"`
		}
		if json.Unmarshal(input, &in) == nil {
			if in.Name != "" {
				return previewText(in.Name, 80)
			}
			return previewText(in.Task, 80)
		}
	case "@image":
		return toolInputPreview(toolName, input)
	}
	return toolInputPreview(toolName, input)
}

// toolExpandedInputLines is the body shown under an expanded compact tool
// header: the bash command (highlighted) or a plain input preview.
func toolExpandedInputLines(toolName string, input []byte, width int) []string {
	inner := width
	if inner < 1 {
		inner = 1
	}
	if toolName == "bash" {
		var in struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(input, &in) == nil && in.Command != "" {
			return bashToolCommandLines(in.Command, inner)
		}
	}
	if toolName == "batch" {
		if lines := batchExpandedCallLines(input, inner); lines != nil {
			return lines
		}
	}
	preview := toolInputPreview(toolName, input)
	if preview == "" || preview == "..." {
		return nil
	}
	var out []string
	for _, chunk := range wrapLine(preview, inner) {
		out = append(out, dim+"  "+chunk+reset)
	}
	return out
}

// batchExpandedCallLines lists each nested call's tool name plus its own
// short reason (path/pattern/task/…, via the same lookup a top-level card of
// that tool would use) — e.g. "1. subagent — explore checkout flow". Unlike
// the card header's "N calls: tool, tool, ..." summary, this actually
// differs call to call, which is the point of expanding in the first place.
func batchExpandedCallLines(input []byte, inner int) []string {
	var in struct {
		Calls json.RawMessage `json:"calls"`
	}
	if json.Unmarshal(input, &in) != nil || len(in.Calls) == 0 {
		return nil
	}
	var calls []struct {
		Tool  string          `json:"tool"`
		Input json.RawMessage `json:"input"`
	}
	if json.Unmarshal(in.Calls, &calls) != nil || len(calls) == 0 {
		return nil
	}
	var out []string
	for i, c := range calls {
		line := fmt.Sprintf("%d. %s", i+1, c.Tool)
		if reason := toolCollapsedReason(c.Tool, c.Input); reason != "" {
			line += " — " + reason
		}
		for _, chunk := range wrapLine(line, inner) {
			out = append(out, dim+"  "+chunk+reset)
		}
	}
	return out
}

func titleCaseTool(name string) string {
	if name == "" {
		return "Tool"
	}
	// Keep @file / @image readable.
	if strings.HasPrefix(name, "@") {
		return name
	}
	runes := []rune(name)
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}

// toolCardSpeedSuffix renders the average output tokens/sec of the streaming
// round that produced this tool call's arguments (see
// agent.OutputInferenceSpeed) — "" when unknown (still in flight, or a
// resumed session, which never replays the original round's timing).
func toolCardSpeedSuffix(b *Block) string {
	if b.meta.TokensPerSec <= 0 {
		return ""
	}
	return fmt.Sprintf(" · %.0f tok/s", b.meta.TokensPerSec)
}

// appendToolCall adds a tool invocation block.
func (s *scrollback) appendToolCall(id int64, providerCallID, name string, input []byte) {
	b := s.newBlock(blockToolCall, "")
	// edit/write are always fully visible — mark Expanded so click/Ctrl+E is a no-op.
	alwaysOpen := isDiffTool(name)
	b.meta = BlockMeta{
		ToolName:       name,
		ProviderCallID: providerCallID,
		ToolInput:      append([]byte(nil), input...),
		Streaming:      true,
		StartedAt:      time.Now(),
		Expanded:       alwaysOpen,
	}
	// Snapshot the target file BEFORE the tool mutates it so edit diffs can
	// keep absolute line numbers after oldText is gone from disk.
	if name == "edit" {
		b.meta.DiffBase = readFileForDiff(toolPathFromInput(input))
	}
	s.blocks = append(s.blocks, b)
	s.markRoundBlock(b.id)
	s.totalAdded++
	s.trim()
}

// appendFileRefCard adds a collapsible card for an @path reference the user
// typed — same compact look as a "read" call, expandable via Ctrl+E. Synthetic
// (no matching tool_use/tool_result): @-expansion happens client-side before
// the turn starts. Used live and on resume (hydrate.go).
func (s *scrollback) appendFileRefCard(id int64, path, content string) {
	b := s.newBlock(blockToolCall, "")
	b.meta = BlockMeta{
		ToolName:   "@file",
		ToolInput:  toolInputJSON("@file", map[string]string{"path": path}),
		ToolResult: content,
		ToolDone:   true,
		StartedAt:  time.Now(),
	}
	s.blocks = append(s.blocks, b)
	s.totalAdded++
	s.trim()
}

// appendImageRefCard adds a collapsible card for an image attachment.
func (s *scrollback) appendImageRefCard(id int64, name, mediaType string, size int) {
	b := s.newBlock(blockToolCall, "")
	b.meta = BlockMeta{
		ToolName:  "@image",
		ToolInput: toolInputJSON("@image", map[string]any{"name": name, "media_type": mediaType, "size": size}),
		ToolDone:  true,
		StartedAt: time.Now(),
	}
	s.blocks = append(s.blocks, b)
	s.totalAdded++
	s.trim()
}

// completeToolCall attaches a result to the matching open tool card.
// When providerCallID is set, pair by id so parallel results can arrive out of order.
// Otherwise fall back to oldest open card (FIFO).
func (s *scrollback) completeToolCall(providerCallID, result, err string, durationMs int64) {
	for i := range s.blocks {
		if s.blocks[i].kind != blockToolCall || s.blocks[i].meta.ToolDone {
			continue
		}
		if providerCallID != "" && s.blocks[i].meta.ProviderCallID != providerCallID {
			continue
		}
		b := &s.blocks[i]
		b.meta.Streaming = false
		b.meta.ToolDone = true
		b.meta.ToolResult = result
		b.meta.ToolError = err
		// Diff tools stay expanded; everything else collapses to one line.
		if isDiffTool(b.meta.ToolName) {
			b.meta.Expanded = true
		} else {
			b.meta.Expanded = false
		}
		if durationMs > 0 {
			b.meta.DurationMs = durationMs
		} else if !b.meta.StartedAt.IsZero() {
			b.meta.DurationMs = time.Since(b.meta.StartedAt).Milliseconds()
		}
		b.invalidateLayout()
		return
	}
	// Orphan result — plain fallback line.
	s.appendRaw(styleToolResult, toolResultFallback("", result, err))
}

// finalizeOrphanToolCalls marks replayed tool cards without results as done.
func (s *scrollback) finalizeOrphanToolCalls() {
	for i := range s.blocks {
		b := &s.blocks[i]
		if b.kind != blockToolCall || b.meta.ToolDone {
			continue
		}
		b.meta.Streaming = false
		b.meta.ToolDone = true
		if b.meta.ToolResult == "" {
			b.meta.ToolResult = "(no result)"
		}
		if isDiffTool(b.meta.ToolName) {
			b.meta.Expanded = true
		} else {
			b.meta.Expanded = false
		}
		b.invalidateLayout()
	}
}

func toolResultFallback(name, result, err string) string {
	if err != "" {
		return "  ✗ " + previewText(err, 400)
	}
	return "  ✓ " + toolResultPreview(name, result)
}

// toolCardSpinnerRows returns scroll rows containing animated tool spinners.
func toolCardSpinnerRows(visible []ScreenRow) []int {
	var rows []int
	for i, row := range visible {
		if strings.Contains(stripANSI(row.Text), toolCardSpinnerSlot) {
			rows = append(rows, i)
		}
	}
	return rows
}

// animateSpinnerInLine swaps the spinner placeholder in a line.
func animateSpinnerInLine(text string, frame int) string {
	// Replace the spinner-slot glyph in place. The glyph (U+25CC) only ever
	// appears in visible content, never inside ANSI escapes, so a direct rune
	// swap is correct even when the line has ANSI codes interspersed (e.g. the
	// subagent widget styles the name, timer, and task separately).
	return strings.Replace(text, toolCardSpinnerSlot, spinnerChar(frame), 1)
}

// toolInputJSON is a helper for tests.
func toolInputJSON(toolName string, v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// toolLangFromInput guesses a highlight language from the tool's path argument.
func toolLangFromInput(name string, input []byte) string {
	var in struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(input, &in) != nil || in.Path == "" {
		return ""
	}
	return langFromPath(in.Path)
}

func langFromPath(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".js", ".mjs", ".cjs":
		return "javascript"
	case ".ts", ".tsx":
		return "typescript"
	case ".json":
		return "json"
	case ".yml", ".yaml":
		return "yaml"
	case ".sh", ".bash", ".zsh":
		return "bash"
	case ".rs":
		return "rust"
	case ".md", ".markdown":
		return "text"
	default:
		return ""
	}
}
