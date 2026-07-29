package tui

import (
	"strings"
	"testing"
)

// The Anthropic stealth path advertises tools under Claude Code's MCP naming
// convention (bash -> mcp_Bash). The dispatch path unwraps those names, but
// names the model buries inside batch's own arguments used to reach the screen
// verbatim — cards read "2 calls: mcp_Bash, mcp_Read" instead of "bash, read".

func TestBatchPreviewShowsBareToolNames(t *testing.T) {
	input := []byte(`{"calls":[{"tool":"mcp_Bash","input":{"command":"ls"}},{"tool":"mcp_Read","input":{"path":"go.mod"}}]}`)
	got := toolInputPreview("batch", input)
	if want := "2 calls: bash, read"; got != want {
		t.Fatalf("preview = %q, want %q", got, want)
	}
}

func TestBatchPreviewLeavesBareNamesAlone(t *testing.T) {
	input := []byte(`{"calls":[{"tool":"bash","input":{"command":"ls"}},{"tool":"read","input":{"path":"go.mod"}}]}`)
	if got, want := toolInputPreview("batch", input), "2 calls: bash, read"; got != want {
		t.Fatalf("preview = %q, want %q", got, want)
	}
}

// TestBatchExpandedLinesUseBareNames covers the second effect of the same bug:
// the wire name also missed every per-tool preview lookup, so an expanded batch
// card showed a bare number and name with no reason at all.
func TestBatchExpandedLinesUseBareNames(t *testing.T) {
	input := []byte(`{"calls":[{"tool":"mcp_Read","input":{"path":"internal/tui/render.go"}}]}`)
	lines := batchExpandedCallLines(input, 120)
	if len(lines) != 1 {
		t.Fatalf("lines = %v, want 1", lines)
	}
	if strings.Contains(lines[0], "mcp_") {
		t.Errorf("line still shows the wire name: %q", lines[0])
	}
	if !strings.Contains(lines[0], "1. read") || !strings.Contains(lines[0], "internal/tui/render.go") {
		t.Errorf("line = %q, want \"1. read — internal/tui/render.go\"", lines[0])
	}
}

// TestTitleCaseToolStripsWirePrefix: a session recorded before the dispatch
// path canonicalized names still has "mcp_Bash" on disk, and its card title
// must not read "Mcp_Bash" after a resume.
func TestTitleCaseToolStripsWirePrefix(t *testing.T) {
	cases := map[string]string{
		"mcp_Bash":    "Bash",
		"mcp_Web_ask": "Web_ask",
		"bash":        "Bash",
		"@file":       "@file",
		"":            "Tool",
	}
	for in, want := range cases {
		if got := titleCaseTool(in); got != want {
			t.Errorf("titleCaseTool(%q) = %q, want %q", in, got, want)
		}
	}
}
