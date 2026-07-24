package tui

import (
	"context"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mq37/poisson/internal/agent"
	"golang.org/x/term"
)

// contentWidth is the safe scrollback line width. Writing exactly cols runes at
// column 1 makes many terminals auto-wrap, so scroll content stays cols-1.
func (t *TUI) contentWidth() int {
	w := t.cols - 1
	if w < 1 {
		return 1
	}
	return w
}

// inputPromptCols is the visible width of the green "› " on the first input row.
const inputPromptCols = 2

// inputWrapWidth is how many text runes fit per wrapped input row. The first row
// also renders the prompt, so text must stay cols-1-prompt wide to avoid
// spilling past the terminal edge.
func inputWrapWidth(cols int) int {
	w := cols - 1 - inputPromptCols
	if w < 1 {
		return 1
	}
	return w
}

// TUI is the split-screen alt-screen REPL.
type TUI struct {
	agent     *agent.Agent
	sessionID string
	output    chan agent.OutputEvent
	writer    io.Writer
	fd        int
	oldState  *term.State

	// Layout state, recomputed on resize.
	mu         sync.Mutex
	rows       int // total terminal rows
	cols       int // total terminal cols
	headerRows int // Grok-style top strip (cwd · tokens · model)
	scrollRows int // rows allotted to scrollback
	inputRows  int // rows for multi-line input

	// Region content.
	scroll *scrollback
	editor *editor
	status StatusSnapshot
	dirty  dirtyTracker
	keyDec Decoder

	renderFrame   int
	activeTools   int
	nextToolID    int64
	lastInputRows int
	// branchChecked marks that paint() already ran gitBranch once for this
	// TUI's (fixed, immutable-for-the-process) cwd, whether or not it found
	// one — without it, a non-git cwd (or a detached-HEAD repo, where
	// gitBranch also returns "") would re-exec `git rev-parse` every single
	// paint call (up to ~30/s while thinking/tools are active).
	branchChecked bool
	// lastWindowTitle caches the last OSC 0 title actually written, so paint
	// (up to ~30/s while animating) only re-emits the escape sequence when the
	// title (session name or id) actually changed.
	lastWindowTitle string
	// layoutJustChanged is set by prepareLayout when it just detected an input
	// height change (and queued a full repaint for the NEXT tick) — paint
	// reads and clears it to upgrade its OWN current call to a full repaint
	// too, instead of leaving a stale partial-repaint gap for one frame.
	layoutJustChanged bool
	activeOverlay     overlay

	// Lifecycle.
	stopped atomic.Bool
	done    chan struct{} // closed by input goroutine when it exits

	// History.
	history    []string
	histIdx    int
	draftSaved string // restore on arrow-down past newest

	// Completion dropdown (slash commands, @file paths).
	completion         *completion
	lastCompletionRows int // scroll rows last painted by the dropdown

	// Focus: Tab toggles between input editor and conversation scroll.
	focusRegion focusRegion
	convUserIdx int // index into scroll.userBlockIndices()

	hintExpiry time.Time // when set, ephemeral status.Hint auto-clears

	// Approval coordination. The input goroutine is the sole stdin reader;
	// when approval is pending it routes the answer through this channel
	// instead of feeding it to the editor.
	approvalMu     sync.Mutex
	approving      atomic.Bool
	approvalAnswer chan approvalReply
	overlayQuit    atomic.Bool

	// queued holds messages the user submitted while a turn was in flight. They
	// are sent together as a single new turn once the current turn finishes.
	// Cleared on cancel. Guarded by t.mu.
	queued []string

	// pendingAttachments are images staged via Ctrl+V or @file, sent with the
	// next user message and shown as chips above the input. Guarded by t.mu.
	pendingAttachments []attachment
	// grabImage reads an image from the system clipboard; overridable in tests
	// so they never spawn wl-paste/xclip. nil means use the real reader.
	grabImage func() ([]byte, error)

	// Submission signaling.
	lastCtrlC          time.Time
	turnCancelled      bool // set when user cancels an in-flight turn; cleared on OutputDone
	exitArmed          bool // next Ctrl+C should offer quit after cancel
	lastOverlayLines   int  // rows painted by previous overlay (ghost clear)
	compacting         atomic.Bool
	searchHadConvFocus bool

	// cancelRun/cancelCtx are set while an agent prompt is in flight. The input
	// goroutine uses them to cancel a running request on Esc (or the approval
	// panic-button's Ctrl+C, which always denies with an empty reason).
	cancelCtx context.Context
	cancelRun context.CancelFunc
	cancelMu  sync.Mutex

	introScrollTop bool // scroll to welcome chart on first paint
	startupIntro   startupIntroMeta

	// sel is the current mouse text selection over the conversation, if any.
	// Cleared by Esc, a new press, or typed input. Copied with Ctrl+Y.
	sel selectionState

	// mouseBuf carries a trailing partial SGR mouse sequence across separate
	// stdin reads. A fast drag floods many motion reports; a single read()
	// can return a prefix cut mid-sequence, and without this the whole burst
	// (including complete events already in the same read) would silently
	// fall through to the keyboard decoder and be dropped.
	mouseBuf []byte

	// usageTickerReset signals lifecycle.go's background usage-refresh
	// ticker (if running — Run() owns it, so it's a no-op in tests that
	// never call Run) to restart its 5-minute schedule from now, so an
	// explicit refresh (triggerUsageRefreshLocked, e.g. after a provider or
	// session switch) doesn't leave a near-duplicate fetch due moments
	// later on the ticker's own unrelated schedule. Buffered 1 and always
	// sent non-blocking — multiple resets before the ticker goroutine gets
	// to read collapse into one.
	usageTickerReset chan struct{}
}

// startupIntroMeta holds the welcome chart text source for re-painting after resets.
type startupIntroMeta struct {
	version, provider, model string
	installed                bool
}

// NewTUI constructs the interactive TUI wired to the given agent and output channel.
func NewTUI(a *agent.Agent, sessionID string, outputChan chan agent.OutputEvent) *TUI {
	return newTUI(a, sessionID, outputChan)
}

func newTUI(a *agent.Agent, sessionID string, outputChan chan agent.OutputEvent) *TUI {
	theme := "dark"
	showTokens := true
	showCost := true
	if a != nil {
		if c := a.Config(); c != nil {
			if c.TUI.Theme != "" {
				theme = c.TUI.Theme
			}
			showTokens = c.TUI.ShowTokens
			showCost = c.TUI.ShowCost
		}
	}
	applyTheme(theme)

	return &TUI{
		agent:     a,
		sessionID: sessionID,
		output:    outputChan,
		writer:    os.Stdout,
		fd:        int(os.Stdin.Fd()),
		scroll:    newScrollback(8192),
		editor:    newEditor(),
		history:   []string{},
		histIdx:   -1,
		status: StatusSnapshot{
			SessionID:  sessionID,
			Cwd:        cwdOf(),
			Model:      modelLabel(a),
			ShowTokens: showTokens,
			ShowCost:   showCost,
		},
		dirty:            newDirtyTracker(),
		inputRows:        3,
		headerRows:       1,
		lastInputRows:    3,
		done:             make(chan struct{}),
		approvalAnswer:   make(chan approvalReply, 1),
		grabImage:        grabClipboardImage,
		usageTickerReset: make(chan struct{}, 1),
	}
}

// modelLabel returns a short "provider/model" label for status display.
func modelLabel(a *agent.Agent) string {
	if a == nil {
		return "-"
	}
	return a.Provider().ID() + "/" + a.Model()
}

// inputHeight returns how many screen rows the input currently needs.
// Caps at a third of total rows so the scrollback stays readable.
func (t *TUI) inputHeight(width int) int {
	if t.approving.Load() {
		n := t.rows * 2 / 5
		if n < 8 {
			n = 8
		}
		max := t.rows - t.headerRows - 3
		if max < 6 {
			max = 6
		}
		if n > max {
			n = max
		}
		return n
	}
	q := t.queuedPreviewRows() + t.attachmentRows()
	visual := totalVisualLines(t.editor, width)
	// chrome = separator + keybinding hint row, +1 more when an ephemeral
	// status.Hint (e.g. "Copied N lines", "paranoid mode — ...") is active:
	// it gets its own row above the keybinding row instead of being crammed
	// onto the same line, which used to push the line past the terminal
	// width and truncate the mode tag (or the hint itself) on anything but a
	// very wide terminal.
	chrome := 2
	if t.status.Hint != "" {
		chrome = 3
	}
	n := visual + chrome + q
	if visual > 1 && n < chrome+3+q {
		n = chrome + 3 + q // show multiple wrapped rows (3 body lines + chrome)
	}
	if n < chrome+1+q {
		n = chrome + 1 + q
	}
	max := t.rows / 3
	if max < chrome+3+q {
		max = chrome + 3 + q
	}
	if n > max {
		n = max
	}
	return n
}

// maxQueuedPreview caps how many pending-message rows appear above the input.
const maxQueuedPreview = 3

// queuedPreviewRows is the number of rows the pending-message preview occupies.
func (t *TUI) queuedPreviewRows() int {
	n := len(t.queued)
	if n == 0 {
		return 0
	}
	if n > maxQueuedPreview {
		return maxQueuedPreview
	}
	return n
}

// running reports whether an agent prompt is in flight.
func (t *TUI) running() bool { return t.status.Thinking }

func (t *TUI) sessionBusyLocked() bool {
	return t.running() || t.compacting.Load()
}
