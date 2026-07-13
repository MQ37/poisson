package tui

import (
	"testing"

	"poisson/internal/agent"
	"poisson/internal/config"
	"poisson/internal/provider"
	"poisson/internal/store"
	"poisson/internal/testutil"
	"poisson/internal/tools"
)

func TestHydrateScrollbackFromSession(t *testing.T) {
	dir := testutil.TempDir(t)
	st, err := store.Open(dir + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	sid := "hydrate-test"
	st.CreateSession(&store.Session{ID: sid, Cwd: ".", Provider: "fake", Model: "m"})
	st.AppendMessage(&store.Message{
		SessionID: sid, Role: "user", Content: `[{"type":"text","text":"hello"}]`,
	})
	st.AppendMessage(&store.Message{
		SessionID: sid, Role: "assistant", Content: `[{"type":"text","text":"world"}]`,
	})

	a := agent.NewAgent(st, provider.NewFakeProvider("fake", nil), tools.NewRegistry(),
		config.DefaultConfig(), sid, nil, nil)
	tui := newTUI(a, sid, nil)
	tui.mu.Lock()
	tui.resetSessionViewLocked()
	tui.mu.Unlock()

	out := testScrollOutput(tui)
	if !containsPlain(out, "hello") || !containsPlain(out, "world") {
		t.Fatalf("hydrated scrollback = %q", out)
	}
}

// TestHydrateScrollbackShowsFullHistoryAfterCompaction reproduces a live user
// report: resuming a compacted session used to show only an opaque
// placeholder line for everything before the compaction, discarding the
// actual detail even though ApplyCompaction never deletes a message, only
// flags it. Hydration should now show the full history (compacted messages
// included), a boundary marker at the exact compaction point, and the
// summary text itself, followed by whatever was sent after compaction.
func TestHydrateScrollbackShowsFullHistoryAfterCompaction(t *testing.T) {
	dir := testutil.TempDir(t)
	st, err := store.Open(dir + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	sid := "hydrate-compact-test"
	st.CreateSession(&store.Session{ID: sid, Cwd: ".", Provider: "fake", Model: "m"})
	st.AppendMessage(&store.Message{
		SessionID: sid, Role: "user", Content: `[{"type":"text","text":"old question before compaction"}]`,
	})
	lastCompacted := &store.Message{
		SessionID: sid, Role: "assistant", Content: `[{"type":"text","text":"old answer before compaction"}]`,
	}
	if err := st.AppendMessage(lastCompacted); err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyCompaction(sid, lastCompacted.Seq, "summary of the earlier conversation"); err != nil {
		t.Fatal(err)
	}
	st.AppendMessage(&store.Message{
		SessionID: sid, Role: "user", Content: `[{"type":"text","text":"new question after compaction"}]`,
	})
	st.AppendMessage(&store.Message{
		SessionID: sid, Role: "assistant", Content: `[{"type":"text","text":"new answer after compaction"}]`,
	})

	a := agent.NewAgent(st, provider.NewFakeProvider("fake", nil), tools.NewRegistry(),
		config.DefaultConfig(), sid, nil, nil)
	tui := newTUI(a, sid, nil)
	tui.mu.Lock()
	tui.resetSessionViewLocked()
	tui.mu.Unlock()

	out := testScrollOutput(tui)
	for _, want := range []string{
		"old question before compaction",
		"old answer before compaction",
		"compacted here",
		"summary of the earlier conversation",
		"new question after compaction",
		"new answer after compaction",
	} {
		if !containsPlain(out, want) {
			t.Errorf("hydrated scrollback missing %q:\n%s", want, out)
		}
	}
}

// TestHydrateScrollbackShowsSummaryWhenEverythingIsCompacted covers the case
// where /compact just ran and nothing has been sent since — the boundary
// marker must still appear (at the end), not be silently dropped.
func TestHydrateScrollbackShowsSummaryWhenEverythingIsCompacted(t *testing.T) {
	dir := testutil.TempDir(t)
	st, err := store.Open(dir + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	sid := "hydrate-all-compacted-test"
	st.CreateSession(&store.Session{ID: sid, Cwd: ".", Provider: "fake", Model: "m"})
	last := &store.Message{
		SessionID: sid, Role: "user", Content: `[{"type":"text","text":"only message"}]`,
	}
	if err := st.AppendMessage(last); err != nil {
		t.Fatal(err)
	}
	if err := st.ApplyCompaction(sid, last.Seq, "the only summary"); err != nil {
		t.Fatal(err)
	}

	a := agent.NewAgent(st, provider.NewFakeProvider("fake", nil), tools.NewRegistry(),
		config.DefaultConfig(), sid, nil, nil)
	tui := newTUI(a, sid, nil)
	tui.mu.Lock()
	tui.resetSessionViewLocked()
	tui.mu.Unlock()

	out := testScrollOutput(tui)
	if !containsPlain(out, "only message") || !containsPlain(out, "the only summary") || !containsPlain(out, "compacted here") {
		t.Fatalf("hydrated scrollback = %q", out)
	}
}
