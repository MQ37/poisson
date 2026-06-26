package tui

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
)

// BlockMeta holds optional metadata for rich rendering (tool cards, collapse, etc.).
type BlockMeta struct {
	ToolName  string
	ToolID    int64
	Collapsed bool
	Streaming bool // true while assistant/thinking chunk stream is in flight
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

// layoutPlain renders a block as hard-wrapped plain text with kind styling.
// Used until markdown/tool-card renderers take over per-kind layout.
func (b *Block) layoutPlain(width int) []ScreenRow {
	if width < 1 {
		width = 1
	}
	if b.cacheWidth == width && b.cachedRows != nil {
		return b.cachedRows
	}
	prefix := kindStylePrefix(b.kind)
	chunks := wrapLine(b.raw, width)
	rows := make([]ScreenRow, len(chunks))
	for i, chunk := range chunks {
		rows[i] = ScreenRow{
			Text: prefix + chunk + reset,
			Tag:  RowTag{BlockID: b.id, RowIdx: i},
		}
	}
	b.cacheWidth = width
	b.cachedRows = rows
	return rows
}