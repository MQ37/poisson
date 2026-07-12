package tui

import "time"

// BlockKind categorizes a scrollback document block.
type BlockKind uint8

const (
	blockUser BlockKind = iota
	blockAssistant
	blockThinking
	blockToolCall
	blockToolResult
	blockCode
	blockSystem
	blockError
	blockCompacting
	blockApproval
	blockSubagent // compact subagent lifecycle widget (never a full tool card)
	blockIntro    // startup splash; raw may contain inline ANSI
)

// BlockMeta holds optional metadata for rich rendering (tool cards, collapse, etc.).
type BlockMeta struct {
	ToolName              string
	ProviderCallID        string
	ToolInput             []byte
	SubagentTask          string // subagent widget: the task prompt (truncated for display)
	SubagentModel         string // subagent widget: provider/model label
	SubagentTurns         int    // subagent widget: live/final turn count reported by the child
	SubagentContextTokens int    // subagent widget: live/final context tokens used, reported by the child
	SubagentContextWindow int    // subagent widget: the child's own model context window
	SubagentStatus        string // subagent widget: non-empty while the child is retrying a network failure (see agent.OutputRetrying) — shown in place of the turn/context line
	Expediting            bool   // subagent widget: user pressed Ctrl+G, child is wrapping up
	ToolResult            string
	ToolError             string
	ToolDone              bool
	Expanded              bool // tool result body expanded (PR-16)
	ToolScroll            int  // scroll offset inside expanded result
	Collapsed             bool
	ThinkingRedacted      bool
	Streaming             bool // true while assistant/thinking/tool stream is in flight
	StartedAt             time.Time
	DurationMs            int64
}

// Block is one logical document unit in the scrollback.
type Block struct {
	id   int64
	kind BlockKind
	raw  string
	meta BlockMeta

	// Layout cache — invalidated when raw or width changes.
	cacheWidth int
	cachedRows []ScreenRow
}

// RowTag identifies a wrapped screen row for incremental dirty rendering.
type RowTag struct {
	BlockID int64
	RowIdx  int
}

// ScreenRow is one terminal row after layout (may include ANSI).
type ScreenRow struct {
	Text string
	Tag  RowTag
}

// streamingKinds are block kinds that merge consecutive append chunks.
var streamingKinds = map[BlockKind]bool{
	blockAssistant: true,
	blockThinking:  true,
}

func styleToKind(st LineStyle) BlockKind {
	switch st {
	case styleUser:
		return blockUser
	case styleAssistant:
		return blockAssistant
	case styleThinking:
		return blockThinking
	case styleToolStart:
		return blockToolCall
	case styleToolResult:
		return blockToolResult
	case styleApproval:
		return blockApproval
	case styleError:
		return blockError
	case styleSystem:
		return blockSystem
	case styleCompacting:
		return blockCompacting
	case styleStatus:
		return blockSystem
	default:
		return blockSystem
	}
}

func kindToStyle(k BlockKind) LineStyle {
	switch k {
	case blockUser:
		return styleUser
	case blockAssistant:
		return styleAssistant
	case blockThinking:
		return styleThinking
	case blockToolCall:
		return styleToolStart
	case blockToolResult:
		return styleToolResult
	case blockApproval:
		return styleApproval
	case blockError:
		return styleError
	case blockSystem, blockCode:
		return styleSystem
	case blockCompacting:
		return styleCompacting
	default:
		return styleSystem
	}
}

func kindStylePrefix(k BlockKind) string {
	return stylePrefix(kindToStyle(k)) //nolint: style maps 1:1 to LineStyle
}

// invalidateLayout drops the layout cache so the next paint recomputes rows.
func (b *Block) invalidateLayout() {
	b.cacheWidth = 0
	b.cachedRows = nil
}

// layoutPlain renders a block to screen rows with per-kind styling.
func (b *Block) layoutPlain(width int) []ScreenRow {
	if width < 1 {
		width = 1
	}
	// Running subagent widgets show a live elapsed timer, so their layout must
	// not be cached — recompute every paint while streaming.
	if b.cacheWidth == width && b.cachedRows != nil && !(b.kind == blockSubagent && b.meta.Streaming) {
		return b.cachedRows
	}
	var rows []ScreenRow
	switch b.kind {
	case blockAssistant:
		chunks := layoutRichMarkdown(b.raw, width, kindStylePrefix(b.kind))
		rows = screenRowsFromChunks(b.id, chunks)
	case blockThinking:
		rows = layoutThinking(b, width, 0)
	case blockToolCall:
		rows = layoutToolCard(b, width, 0)
	case blockSubagent:
		rows = layoutSubagentCard(b, width)
	case blockIntro:
		for _, chunk := range wrapANSI(b.raw, width) {
			rows = append(rows, ScreenRow{Text: chunk + reset, Tag: RowTag{BlockID: b.id, RowIdx: len(rows)}})
		}
	default:
		// Every wrapped line repeats the style prefix, not just the first: rows
		// can be repainted individually (dirty-row tracking positions the
		// cursor per row, it doesn't replay a full top-to-bottom stream), so a
		// continuation line can't rely on color state "carrying over" from a
		// row that isn't part of that repaint — it would render in whatever
		// SGR state happens to be ambient, i.e. unstyled.
		prefix := kindStylePrefix(b.kind)
		var chunks []string
		for _, chunk := range wrapLine(b.raw, width) {
			chunks = append(chunks, prefix+chunk+reset)
		}
		rows = screenRowsFromChunks(b.id, chunks)
	}
	b.cacheWidth = width
	b.cachedRows = rows
	return rows
}

func screenRowsFromChunks(id int64, chunks []string) []ScreenRow {
	rows := make([]ScreenRow, len(chunks))
	for i, chunk := range chunks {
		rows[i] = ScreenRow{Text: chunk, Tag: RowTag{BlockID: id, RowIdx: i}}
	}
	return rows
}
