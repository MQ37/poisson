package tui

import (
	"context"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/term"
	"poisson/internal/agent"
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
	headerRows int // Grok-style top strip (cwd · tokens · time)
	scrollRows int // rows allotted to scrollback
	inputRows  int // rows for multi-line input
	statusRows int // legacy; 0 (status moved to headerRows)

	// Region content.
	scroll *scrollback
	editor *editor
	status StatusSnapshot
	dirty  dirtyTracker
	keyDec Decoder

	renderFrame    int
	activeTools    int
	nextToolID     int64
	lastInputRows  int
	activeOverlay  overlay

	// Lifecycle.
	stopped atomic.Bool
	done    chan struct{} // closed by input goroutine when it exits

	// History.
	history    []string
	histIdx    int
	draftSaved string // restore on arrow-down past newest

	// Completion dropdown (slash commands, @file paths).
	completion           *completion
	lastCompletionRows   int // scroll rows last painted by the dropdown

	// Focus: Tab toggles between input editor and conversation scroll.
	focusRegion focusRegion
	convUserIdx int // index into scroll.userBlockIndices()

	hintExpiry time.Time // when set, ephemeral status.Hint auto-clears

	// Approval coordination. The input goroutine is the sole stdin reader;
	// when approval is pending it routes the answer through this channel
	// instead of feeding it to the editor.
	approvalMu     sync.Mutex
	approving      atomic.Bool
	approvalAnswer chan bool
	overlayQuit    atomic.Bool

	// Submission signaling.
	lastCtrlC        time.Time
	turnCancelled    bool // set when user cancels an in-flight turn; cleared on OutputDone
	exitArmed        bool // next Ctrl+C should offer quit after cancel
	lastOverlayLines int  // rows painted by previous overlay (ghost clear)
	compacting       atomic.Bool
	searchHadConvFocus bool

	// cancelRun/cancelCtx are set while an agent prompt is in flight. The input
	// goroutine uses them to cancel a running request (and pending approval) on Ctrl+C.
	cancelCtx  context.Context
	cancelRun  context.CancelFunc
	cancelMu   sync.Mutex
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
		agent:          a,
		sessionID:      sessionID,
		output:         outputChan,
		writer:         os.Stdout,
		fd:             int(os.Stdin.Fd()),
		scroll:         newScrollback(8192),
		editor:         newEditor(),
		history:        []string{},
		histIdx:        -1,
		status: StatusSnapshot{
			SessionID:  sessionID,
			Cwd:        cwdOf(),
			Model:      modelLabel(a),
			ShowTokens: showTokens,
			ShowCost:   showCost,
		},
		dirty:          newDirtyTracker(),
		inputRows:      3,
		headerRows:     1,
		statusRows:     0,
		lastInputRows:  3,
		done:           make(chan struct{}),
		approvalAnswer: make(chan bool, 1),
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
	n := totalVisualLines(t.editor, width) + 2 // +1 separator, +1 hint
	if n < 4 {
		n = 4
	}
	max := t.rows / 3
	if max < 5 {
		max = 5
	}
	if n > max {
		n = max
	}
	return n
}

// running reports whether an agent prompt is in flight.
func (t *TUI) running() bool { return t.status.Thinking }

func (t *TUI) sessionBusyLocked() bool {
	return t.running() || t.compacting.Load()
}
