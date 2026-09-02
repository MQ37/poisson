package tui

import (
	"strings"
	"testing"
)

func TestFormatBashCommandHighlightRm(t *testing.T) {
	got := formatBashCommandHighlight("rm -rf /tmp/x")
	plain := stripANSI(got)
	if plain != "rm -rf /tmp/x" {
		t.Fatalf("plain = %q", plain)
	}
	if !strings.Contains(got, fgRed) {
		t.Fatal("expected red danger styling")
	}
}

func TestFormatBashCommandHighlightSudoPartial(t *testing.T) {
	got := formatBashCommandHighlight("sudo ls")
	if !strings.Contains(got, fgRed) || !strings.Contains(got, fgYellow) {
		t.Fatalf("expected mixed styling: %q", got)
	}
	if stripANSI(got) != "sudo ls" {
		t.Fatalf("plain = %q", stripANSI(got))
	}
}

func TestApprovalCommandLinesHaveBar(t *testing.T) {
	lines := approvalCommandLines("rm -rf x", 40)
	if len(lines) == 0 {
		t.Fatal("expected lines")
	}
	if !strings.Contains(lines[0], "█") {
		t.Fatalf("missing approval bar: %q", lines[0])
	}
	if !strings.Contains(lines[0], "$") {
		t.Fatalf("missing prompt: %q", lines[0])
	}
}

// TestFormatBashCommandHighlightRedactsSecret guards the approval-preview
// and tool-card command display (both funnel through this one function —
// see wrapHighlightedBashMultiline) against showing a live credential a
// prior command echoed into this one's arguments.
func TestFormatBashCommandHighlightRedactsSecret(t *testing.T) {
	token := "apify_api_0000000000000000000000000000000000"
	got := stripANSI(formatBashCommandHighlight(`curl -H "Authorization: Bearer ` + token + `" https://example.com`))
	if strings.Contains(got, token) {
		t.Fatalf("secret leaked into command display: %q", got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("expected redaction marker, got %q", got)
	}
}

func TestApprovalCommandLinesRedactSecret(t *testing.T) {
	token := "apify_api_0000000000000000000000000000000000"
	lines := approvalCommandLines(`curl -H "Authorization: Bearer `+token+`" https://example.com`, 200)
	for _, ln := range lines {
		if strings.Contains(ln, token) {
			t.Fatalf("secret leaked into approval overlay line: %q", ln)
		}
	}
}

func TestBashToolCardHighlight(t *testing.T) {
	// Expanded view shows the command body with risk highlighting.
	b := Block{
		id:   1,
		kind: blockToolCall,
		meta: BlockMeta{
			ToolName: "bash",
			Expanded: true,
			ToolInput: toolInputJSON("bash", map[string]string{
				"command":     "sudo rm -rf /",
				"description": "wipe root",
			}),
		},
	}
	rows := layoutToolCard(&b, 50, 0)
	foundRed := false
	for _, row := range rows {
		if strings.Contains(row.Text, fgRed) {
			foundRed = true
		}
	}
	if !foundRed {
		t.Fatal("expected danger highlight in tool card body")
	}
}
