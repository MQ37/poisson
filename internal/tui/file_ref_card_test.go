package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"poisson/internal/agent"
	"poisson/internal/config"
	"poisson/internal/provider"
	"poisson/internal/store"
	"poisson/internal/testutil"
	"poisson/internal/tools"
)

// TestStripFenceRecoversDisplayBody checks the shape expandAtFilesSegments
// always produces for a FileRef segment (fence, body, matching fence) is
// correctly unwrapped, and that unrelated text is left alone.
func TestStripFenceRecoversDisplayBody(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"fenced", "```\nhello\nworld\n```", "hello\nworld"},
		{"escalated fence", "````\ninner ``` fence\n````", "inner ``` fence"},
		{"not fenced", "plain text, no fence", "plain text, no fence"},
		{"single line", "```", "```"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stripFence(c.in); got != c.want {
				t.Errorf("stripFence(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestSubmitAppendsCollapsibleFileRefCard is the live-send side: submitting
// "@path" text appends the user's message with the literal token intact
// (unchanged from before this feature) plus a separate collapsible card
// holding the file's content \u2014 the message itself never carries the dump.
func TestSubmitAppendsCollapsibleFileRefCard(t *testing.T) {
	dir := testutil.TempDir(t)
	path := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(path, []byte("file body content"), 0644); err != nil {
		t.Fatal(err)
	}

	st, err := store.Open(dir + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sid := "file-ref-test"
	st.CreateSession(&store.Session{ID: sid, Cwd: ".", Provider: "fake", Model: "m"})
	a := agent.NewAgent(st, provider.NewFakeProvider("fake", nil), tools.NewRegistry(),
		config.DefaultConfig(), sid, nil, nil)

	tui := newTUI(a, sid, nil)
	tui.mu.Lock()
	tui.resetSessionViewLocked()
	if err := tui.submit("check @" + path); err != nil {
		t.Fatal(err)
	}
	tui.mu.Unlock()

	var userText, cardResult string
	var sawCard bool
	for _, b := range tui.scroll.blocks {
		if b.kind == blockUser {
			userText = b.raw
		}
		if b.kind == blockToolCall && b.meta.ToolName == "@file" {
			sawCard = true
			cardResult = b.meta.ToolResult
		}
	}
	if userText != "check @"+path {
		t.Errorf("user bubble = %q, want literal @path preserved, no dump", userText)
	}
	if !sawCard {
		t.Fatal("expected a collapsible @file card")
	}
	if cardResult != "file body content" {
		t.Errorf("card content = %q, want unfenced file body", cardResult)
	}
	if strings.Contains(userText, "file body content") {
		t.Error("file content leaked into the user message bubble")
	}
}

// TestFileRefRoundTripLiveToResume is the full loop: what actually gets
// persisted when submit() sends an @path reference must hydrate back into
// the exact same collapsible-card presentation, not a dump of the file
// inline into the resumed user message (the reported bug).
func TestFileRefRoundTripLiveToResume(t *testing.T) {
	dir := testutil.TempDir(t)
	path := filepath.Join(dir, "main.go")
	content := "package main\n\nfunc main() {}\n"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	st, err := store.Open(dir + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	sid := "file-ref-resume-test"
	st.CreateSession(&store.Session{ID: sid, Cwd: ".", Provider: "fake", Model: "m"})
	prov := provider.NewFakeProvider("fake", nil)
	prov.SetResponses([][]provider.StreamEvent{
		provider.FakeTextResponse("looks fine", &provider.Usage{InputTokens: 10, OutputTokens: 5}),
	})
	a := agent.NewAgent(st, prov, tools.NewRegistry(), config.DefaultConfig(), sid, nil, nil)

	segs, err := expandAtFilesSegments("check @" + path + " for bugs")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.PromptSegmentsWithContext(context.Background(), segs); err != nil {
		t.Fatal(err)
	}

	tui := newTUI(a, sid, nil)
	tui.mu.Lock()
	tui.resetSessionViewLocked()
	tui.mu.Unlock()

	var userText, cardResult string
	var sawCard bool
	for _, b := range tui.scroll.blocks {
		if b.kind == blockUser {
			userText = b.raw
		}
		if b.kind == blockToolCall && b.meta.ToolName == "@file" {
			sawCard = true
			cardResult = b.meta.ToolResult
		}
	}
	if !strings.Contains(userText, "@"+path) {
		t.Errorf("resumed user bubble = %q, want the literal @path token reconstructed", userText)
	}
	if strings.Contains(userText, "package main") {
		t.Errorf("resumed user bubble dumped the file inline instead of a collapsible card: %q", userText)
	}
	if !sawCard {
		t.Fatal("expected a collapsible @file card on resume")
	}
	if !strings.Contains(cardResult, "package main") {
		t.Errorf("card content = %q, want the file body", cardResult)
	}
}
