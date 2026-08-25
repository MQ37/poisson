package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mq37/poisson/internal/provider"
)

// TestInteg_RenderTagFailureAutoRetries: a final answer citing a <render>
// file that doesn't exist must not just sit broken — the model gets one
// automatic follow-up round reporting the failure, visibly marked as
// synthetic (not something a human typed), before the turn actually ends.
func TestInteg_RenderTagFailureAutoRetries(t *testing.T) {
	e := newIntegEnv(t, [][]provider.StreamEvent{
		// The tag must be alone on its own line to be recognized at all
		// (see citetag.ParseTag) — matching the "here's the file:\n<render
		// .../>" shape the system prompt itself teaches, not inlined
		// mid-sentence.
		provider.FakeTextResponse("here's the entry point:\n"+`<render file="/no/such/file.go"/>`, nil),
		provider.FakeTextResponse("fixed, thanks", nil),
	})

	events := e.send("show me the entry point")

	if e.prov.CallCount() != 2 {
		t.Fatalf("CallCount = %d, want 2 (retry round + follow-up)", e.prov.CallCount())
	}

	var sawNotice bool
	for _, ev := range events {
		if ev.Type == OutputError && strings.Contains(ev.Text, "citation") {
			sawNotice = true
		}
	}
	if !sawNotice {
		t.Error("expected a live OutputError notice announcing the failed citation retry")
	}

	msgs := e.msgs()
	var sawSyntheticNote bool
	for _, m := range msgs {
		if m.Role == "user" && strings.Contains(msgText(m), "[poisson: automatic check]") {
			sawSyntheticNote = true
			if !strings.Contains(msgText(m), "/no/such/file.go") {
				t.Errorf("synthetic note = %q, want it to name the failed citation", msgText(m))
			}
		}
	}
	if !sawSyntheticNote {
		t.Error("expected a visibly-marked synthetic user message reporting the failed citation")
	}
}

// TestInteg_RenderTagInsideFenceNotFlagged: a <render> tag shown as a
// literal syntax example inside a fenced code block is never expanded into
// a widget by the TUI (splitFenceSegments never even calls parseRenderTag
// on a fenced line) — the retry check must agree, or a model merely
// documenting the syntax would burn a retry round over a citation nobody
// ever tried to resolve.
func TestInteg_RenderTagInsideFenceNotFlagged(t *testing.T) {
	e := newIntegEnv(t, [][]provider.StreamEvent{
		provider.FakeTextResponse("here's the tag syntax:\n```\n"+`<render file="/no/such/file.go"/>`+"\n```\nthat's the syntax.", nil),
	})

	events := e.send("how does the render tag work?")

	if e.prov.CallCount() != 1 {
		t.Fatalf("CallCount = %d, want 1 (a fenced example must never trigger a retry)", e.prov.CallCount())
	}
	for _, ev := range events {
		if ev.Type == OutputError && strings.Contains(ev.Text, "citation") {
			t.Errorf("unexpected citation-retry notice for a tag inside a code fence: %q", ev.Text)
		}
	}
}

// TestInteg_RenderTagSuccessNoRetry: a citation that actually resolves must
// not trigger any retry machinery — the turn ends normally after one round,
// exactly as if this feature didn't exist.
func TestInteg_RenderTagSuccessNoRetry(t *testing.T) {
	e := newIntegEnv(t, [][]provider.StreamEvent{
		provider.FakeTextResponse("", nil), // placeholder, replaced below
	})
	path := filepath.Join(e.dir, "real.go")
	if err := os.WriteFile(path, []byte("package main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	e.prov.SetResponses([][]provider.StreamEvent{
		provider.FakeTextResponse(`<render file="`+path+`"/>`, nil),
	})

	events := e.send("show me the entry point")

	if e.prov.CallCount() != 1 {
		t.Fatalf("CallCount = %d, want 1 (no retry for a citation that resolves)", e.prov.CallCount())
	}
	for _, ev := range events {
		if ev.Type == OutputError && strings.Contains(ev.Text, "citation") {
			t.Errorf("unexpected citation-retry notice for a citation that resolves: %q", ev.Text)
		}
	}
}

// TestInteg_RenderTagRetryBounded: a model that keeps citing a broken path
// every round must not loop forever — capped at maxRenderTagRetries, then
// the turn ends with the model's own (still-broken) answer, same as any
// other bounded retry in this codebase (max_tokens continuations, empty
// response retries).
func TestInteg_RenderTagRetryBounded(t *testing.T) {
	bad := `<render file="/no/such/file.go"/>`
	responses := make([][]provider.StreamEvent, 0, maxRenderTagRetries+1)
	for i := 0; i <= maxRenderTagRetries; i++ {
		responses = append(responses, provider.FakeTextResponse(bad, nil))
	}
	e := newIntegEnv(t, responses)

	e.send("show me the entry point")

	if got, want := e.prov.CallCount(), maxRenderTagRetries+1; got != want {
		t.Fatalf("CallCount = %d, want %d (initial + %d bounded retries, then stop)", got, want, maxRenderTagRetries)
	}
}
