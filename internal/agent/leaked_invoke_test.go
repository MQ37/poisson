package agent

import (
	"strings"
	"testing"

	"github.com/mq37/poisson/internal/provider"
)

// paramOpen is split across a `+` so this file's own raw source text never
// contains the exact fragment the tools package's own leak guard
// (validate.go) rejects on sight — which would otherwise block writing this
// very test file via the write tool.
const paramOpen = "<parameter " + `name="`

// garbledInvoke reproduces the real-world shape this feature targets: a
// model that leaks poisson's own tool-call XML template as plain text
// instead of issuing a real tool call, with the tool name mistakenly used as
// the parameter name and the parameter tag left unclosed.
func garbledInvoke(toolName, value string) string {
	return "<invoke>\n" + paramOpen + toolName + "\">" + value + "\n</invoke>"
}

// TestInteg_LeakedInvokeCorrectionThenRecovers: round 1 leaks a raw invoke
// tag as text (no real tool call parsed) — the turn must not end there.
// Instead it appends a corrective user turn and retries; round 2 answers
// cleanly, and that clean answer is what actually reaches the user as the
// final turn output.
func TestInteg_LeakedInvokeCorrectionThenRecovers(t *testing.T) {
	e := newIntegEnv(t, [][]provider.StreamEvent{
		provider.FakeTextResponse(garbledInvoke("bash", "echo hi"), nil),
		provider.FakeTextResponse("hi", nil),
	})

	events := e.send("run echo hi")

	if e.prov.CallCount() != 2 {
		t.Fatalf("CallCount = %d, want 2 (leaked round + corrected retry)", e.prov.CallCount())
	}

	var sawCorrectionNotice bool
	for _, ev := range events {
		if ev.Type == OutputError && strings.Contains(ev.Text, "leaked") {
			sawCorrectionNotice = true
		}
	}
	if !sawCorrectionNotice {
		t.Error("expected an OutputError notice about the leaked tool-call tag")
	}

	msgs := e.msgs()
	// user "run echo hi", assistant (leaked), synthetic user correction,
	// assistant "hi" — the correction message is the load-bearing one.
	var correction *string
	for i, m := range msgs {
		if m.Role == "user" && i > 0 && strings.Contains(msgText(m), "tool-call tag") {
			s := msgText(m)
			correction = &s
		}
	}
	if correction == nil {
		t.Fatal("expected a synthetic user message correcting the leaked tool call, found none")
	}

	last := msgs[len(msgs)-1]
	if last.Role != "assistant" || msgText(last) != "hi" {
		t.Fatalf("last message = %+v, want the clean recovered answer %q", last, "hi")
	}
}

// TestInteg_LeakedInvokeRetriesBounded: a model that keeps leaking the same
// broken XML every round must not loop forever — after maxLeakedInvokeRetries
// corrections, the turn ends with whatever text the model last produced
// instead of hanging.
func TestInteg_LeakedInvokeRetriesBounded(t *testing.T) {
	leaked := garbledInvoke("bash", "echo hi")
	responses := make([][]provider.StreamEvent, maxLeakedInvokeRetries+2)
	for i := range responses {
		responses[i] = provider.FakeTextResponse(leaked, nil)
	}
	e := newIntegEnv(t, responses)

	events := e.send("run echo hi")

	wantCalls := maxLeakedInvokeRetries + 1
	if e.prov.CallCount() != wantCalls {
		t.Fatalf("CallCount = %d, want %d (1 initial + %d bounded retries)", e.prov.CallCount(), wantCalls, maxLeakedInvokeRetries)
	}

	var done bool
	for _, ev := range events {
		if ev.Type == OutputDone {
			done = true
		}
	}
	if !done {
		t.Fatal("expected the turn to end (OutputDone) instead of looping forever")
	}

	last := e.msgs()[len(e.msgs())-1]
	if last.Role != "assistant" || msgText(last) != leaked {
		t.Fatalf("last message role/text = %q/%q, want the exhausted-retry assistant text still on record", last.Role, msgText(last))
	}
}
