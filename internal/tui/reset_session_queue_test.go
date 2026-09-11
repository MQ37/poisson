package tui

import (
	"testing"

	"github.com/mq37/poisson/internal/agent"
	"github.com/mq37/poisson/internal/config"
	"github.com/mq37/poisson/internal/provider"
	"github.com/mq37/poisson/internal/store"
	"github.com/mq37/poisson/internal/testutil"
	"github.com/mq37/poisson/internal/tools"
)

// TestResetSessionViewDropsQueuedMessages is the regression guard for bug
// 10: a message queued while a turn was running (or a subagent-done nudge
// parked behind a busy/overlay-active session) belongs to the session being
// left. resetSessionViewLocked (/new, /resume) used to clear scrollback,
// overlay, and completion state but never t.queued — leaving it to be
// spliced into whatever session comes next by the following
// drainQueueLocked.
func TestResetSessionViewDropsQueuedMessages(t *testing.T) {
	dir := testutil.TempDir(t)
	st, err := store.Open(dir + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	sid := "reset-queue-test"
	if err := st.CreateSession(&store.Session{ID: sid, Cwd: ".", Provider: "fake", Model: "m"}); err != nil {
		t.Fatal(err)
	}

	a := agent.NewAgent(st, provider.NewFakeProvider("fake", nil), tools.NewRegistry(), config.DefaultConfig(), sid, nil, nil)
	tui := newTUI(a, sid, nil)

	tui.mu.Lock()
	tui.queued = []string{"unsent message meant for the old session"}
	tui.resetSessionViewLocked()
	left := len(tui.queued)
	hint := tui.status.Hint
	tui.mu.Unlock()

	if left != 0 {
		t.Fatalf("queued messages after resetSessionViewLocked = %d, want 0 (must not leak into the new session)", left)
	}
	if hint == "" {
		t.Fatal("expected an ephemeral hint noting the discarded queue, got none")
	}
}

// TestResetSessionViewNoHintWhenQueueEmpty covers the common case: no
// discard hint should ever appear for an ordinary /new or /resume with
// nothing queued.
func TestResetSessionViewNoHintWhenQueueEmpty(t *testing.T) {
	dir := testutil.TempDir(t)
	st, err := store.Open(dir + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	sid := "reset-queue-empty-test"
	if err := st.CreateSession(&store.Session{ID: sid, Cwd: ".", Provider: "fake", Model: "m"}); err != nil {
		t.Fatal(err)
	}

	a := agent.NewAgent(st, provider.NewFakeProvider("fake", nil), tools.NewRegistry(), config.DefaultConfig(), sid, nil, nil)
	tui := newTUI(a, sid, nil)

	tui.mu.Lock()
	tui.resetSessionViewLocked()
	hint := tui.status.Hint
	tui.mu.Unlock()

	if hint != "" {
		t.Fatalf("unexpected hint with nothing queued: %q", hint)
	}
}
