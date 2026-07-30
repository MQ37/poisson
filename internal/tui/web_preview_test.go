package tui

import "testing"

// TestToolInputPreviewOmitsBackend: which backend a web tool ran on now
// renders on the card title (formatToolCollapsed/formatToolExpandedHeader:
// "Web_ask[anthropic] - why"), not appended to the query/URL preview text —
// a preview gets truncated to fit width, which could clip a suffix tag right
// off the card; the title never does. toolInputPreview/toolCollapsedReason
// stay plain regardless of provider.
func TestToolInputPreviewOmitsBackend(t *testing.T) {
	cases := []struct {
		name  string
		tool  string
		input string
		want  string
	}{
		{"fetch default", "fetch", `{"url":"https://example.com"}`, "https://example.com"},
		{"fetch anthropic", "fetch", `{"url":"https://example.com","provider":"anthropic"}`, "https://example.com"},
		{"search default", "web_search", `{"query":"go slices"}`, "go slices"},
		{"search anthropic", "web_search", `{"query":"go slices","provider":"anthropic"}`, "go slices"},
		{"ask grok", "web_ask", `{"query":"why","provider":"grok"}`, "why"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := toolInputPreview(tc.tool, []byte(tc.input)); got != tc.want {
				t.Errorf("preview = %q, want %q", got, tc.want)
			}
			if got := toolCollapsedReason(tc.tool, []byte(tc.input)); got != tc.want {
				t.Errorf("card preview = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestProviderTagOnCardTitle: the provider lands on the tool name itself, in
// both the collapsed and expanded headers, so it survives width truncation
// of the reason text that follows it.
func TestProviderTagOnCardTitle(t *testing.T) {
	b := &Block{meta: BlockMeta{
		ToolName:  "web_ask",
		ToolInput: []byte(`{"query":"why is the sky blue","provider":"anthropic"}`),
		ToolDone:  true,
	}}
	if got, want := stripANSI(formatToolCollapsed(b, 80)), "▸ Web_ask[anthropic] - why is the sky blue"; got != want {
		t.Errorf("collapsed = %q, want %q", got, want)
	}
	b.meta.Expanded = true
	if got, want := stripANSI(formatToolExpandedHeader(b)), "▾ Web_ask[anthropic] - why is the sky blue"; got != want {
		t.Errorf("expanded header = %q, want %q", got, want)
	}
}

// TestProviderTagAbsentWithoutProvider: the default backend (no "provider"
// field) must not show an empty "[]" tag anywhere.
func TestProviderTagAbsentWithoutProvider(t *testing.T) {
	b := &Block{meta: BlockMeta{
		ToolName:  "web_ask",
		ToolInput: []byte(`{"query":"why is the sky blue"}`),
		ToolDone:  true,
	}}
	if got, want := stripANSI(formatToolCollapsed(b, 80)), "▸ Web_ask - why is the sky blue"; got != want {
		t.Errorf("collapsed = %q, want %q", got, want)
	}
}

// TestSandboxTagOnCardTitle: a bash call carrying a sandboxId shows
// "Bash[my-sandbox]" on the card title, same slot/mechanism as the provider
// tag, so it survives width truncation of the reason text.
func TestSandboxTagOnCardTitle(t *testing.T) {
	b := &Block{meta: BlockMeta{
		ToolName:  "bash",
		ToolInput: []byte(`{"description":"list files","command":"ls","sandboxId":"my-sandbox"}`),
		ToolDone:  true,
	}}
	if got, want := stripANSI(formatToolCollapsed(b, 80)), "▸ Bash[my-sandbox] - list files"; got != want {
		t.Errorf("collapsed = %q, want %q", got, want)
	}
	b.meta.Expanded = true
	if got, want := stripANSI(formatToolExpandedHeader(b)), "▾ Bash[my-sandbox] - list files"; got != want {
		t.Errorf("expanded header = %q, want %q", got, want)
	}
}

// TestSandboxTagAbsentWithoutSandboxId: a bash call with no sandboxId (the
// common host-run case) must not show an empty "[]" tag.
func TestSandboxTagAbsentWithoutSandboxId(t *testing.T) {
	b := &Block{meta: BlockMeta{
		ToolName:  "bash",
		ToolInput: []byte(`{"description":"list files","command":"ls"}`),
		ToolDone:  true,
	}}
	if got, want := stripANSI(formatToolCollapsed(b, 80)), "▸ Bash - list files"; got != want {
		t.Errorf("collapsed = %q, want %q", got, want)
	}
}

// TestSandboxTagOnBatchNestedCall: batch's expanded per-call lines are tool-
// agnostic (same providerTagFromInput mechanism), so a nested bash call
// carrying a sandboxId must show the tag there too.
func TestSandboxTagOnBatchNestedCall(t *testing.T) {
	input := []byte(`{"calls":[{"tool":"bash","input":{"description":"list files","command":"ls","sandboxId":"my-sandbox"}}]}`)
	lines := batchExpandedCallLines(input, 80)
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d: %v", len(lines), lines)
	}
	if got, want := stripANSI(lines[0]), "  1. bash[my-sandbox] — list files"; got != want {
		t.Errorf("batch nested line = %q, want %q", got, want)
	}
}
