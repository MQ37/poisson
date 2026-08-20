package agent

import (
	"strings"
	"testing"

	"github.com/mq37/poisson/internal/provider"
	"github.com/mq37/poisson/internal/tools"
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

// TestInteg_LeakedInvokeRecoveredAndDispatched: a round with no real tool
// calls, whose text leaks a recoverable invoke tag, must have that call
// actually dispatched — not just flagged for the model to redo. grep's
// schema has a single required field (pattern), so this recovers cleanly
// and the tool genuinely runs, in the same round's dispatch loop as any
// normal tool_use block.
func TestInteg_LeakedInvokeRecoveredAndDispatched(t *testing.T) {
	e := newIntegEnv(t, [][]provider.StreamEvent{
		provider.FakeTextResponse(garbledInvoke("grep", "pattern=hello"), nil),
		provider.FakeTextResponse("found it", nil),
	})
	e.reg.Register(tools.NewGrepTool(e.dir, alwaysApprove))

	events := e.send("search for hello")

	if e.prov.CallCount() != 2 {
		t.Fatalf("CallCount = %d, want 2 (recovered dispatch round + follow-up)", e.prov.CallCount())
	}

	var sawRecoveryNotice, sawToolStart, sawToolResult bool
	for _, ev := range events {
		switch {
		case ev.Type == OutputError && strings.Contains(ev.Text, "recovered"):
			sawRecoveryNotice = true
		case ev.Type == OutputToolStart && ev.ToolName == "grep":
			sawToolStart = true
		case ev.Type == OutputToolResult && ev.ToolName == "grep":
			sawToolResult = true
		}
	}
	if !sawRecoveryNotice {
		t.Error("expected an OutputError notice announcing the recovered call")
	}
	if !sawToolStart {
		t.Error("expected an OutputToolStart for the recovered grep call — it must actually dispatch, not just get flagged")
	}
	if !sawToolResult {
		t.Error("expected an OutputToolResult for the recovered grep call")
	}

	msgs := e.msgs()
	var sawToolUse, sawCorrectiveText bool
	for _, m := range msgs {
		if hasBlock(m, "tool_use") {
			sawToolUse = true
			if strings.Contains(msgText(m), "<invoke") {
				t.Errorf("recovered round's stored text still contains the leaked XML (%q) — it should be stripped so the model isn't shown a rewarded example to imitate", msgText(m))
			}
		}
		if m.Role == "user" && strings.Contains(msgText(m), "tool-call") {
			sawCorrectiveText = true
		}
	}
	if !sawToolUse {
		t.Error("expected a real tool_use block recorded in history for the recovered call")
	}
	if sawCorrectiveText {
		t.Error("must not append a synthetic 'please retry' correction message — the call should just run")
	}
}

// TestInteg_LeakedInvokeUnrecoverableFallsThroughSilently: a leaked invoke
// tag that doesn't resolve to any registered tool (garbled beyond safe
// recovery, or naming a tool that doesn't exist) must not trigger a
// corrective retry loop — the turn ends normally with the model's own text,
// exactly as if no detection existed, since asking the model to redo it is
// exactly what this feature must NOT do.
func TestInteg_LeakedInvokeUnrecoverableFallsThroughSilently(t *testing.T) {
	leaked := garbledInvoke("does_not_exist", "whatever")
	e := newIntegEnv(t, [][]provider.StreamEvent{
		provider.FakeTextResponse(leaked, nil),
	})

	events := e.send("do the thing")

	if e.prov.CallCount() != 1 {
		t.Fatalf("CallCount = %d, want 1 — an unrecoverable leak must not trigger any retry round", e.prov.CallCount())
	}

	var done bool
	for _, ev := range events {
		if ev.Type == OutputDone {
			done = true
		}
	}
	if !done {
		t.Fatal("expected the turn to end (OutputDone)")
	}

	msgs := e.msgs()
	for _, m := range msgs {
		if m.Role == "user" && strings.Contains(msgText(m), "tool-call") {
			t.Errorf("must not append any synthetic correction/retry message, found: %q", msgText(m))
		}
	}
	last := msgs[len(msgs)-1]
	if last.Role != "assistant" || msgText(last) != leaked {
		t.Fatalf("last message role/text = %q/%q, want the model's own leaked text left on record unchanged", last.Role, msgText(last))
	}
}
