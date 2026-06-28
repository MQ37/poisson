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
	st.SeedPricing()

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
	tui.resetSessionView()

	out := testScrollOutput(tui)
	if !containsPlain(out, "hello") || !containsPlain(out, "world") {
		t.Fatalf("hydrated scrollback = %q", out)
	}
}