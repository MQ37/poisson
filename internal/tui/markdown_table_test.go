package tui

import "testing"

func TestRenderMarkdownTable(t *testing.T) {
	src := `| # | Name |
|---|------|
| 1 | Alice |
| 2 | Bob |`
	lines := renderMarkdown(src, 40, "")
	if len(lines) < 5 {
		t.Fatalf("lines = %d, want >= 5 (top+header+sep+rows+bottom)", len(lines))
	}
	plain := ""
	for _, ln := range lines {
		plain += stripANSI(ln) + "\n"
	}
	if stringsContains(plain, "|---|") || stringsContains(plain, "| 1 |") {
		t.Fatalf("raw markdown pipes still visible:\n%s", plain)
	}
	for _, want := range []string{"╭", "├", "╰", "Alice", "│"} {
		if !stringsContains(plain, want) {
			t.Fatalf("missing %q in:\n%s", want, plain)
		}
	}
}

func TestRenderMarkdownTableInAssistantBlock(t *testing.T) {
	raw := "| A | B |\n|---|---|\n| 1 | 2 |"
	b := Block{id: 1, kind: blockAssistant, raw: raw}
	rows := b.layoutPlain(30)
	if len(rows) < 5 {
		t.Fatalf("rows = %d", len(rows))
	}
}

func TestPhaseATableLikeReference(t *testing.T) {
	raw := `**Phase A (landed)**

| Work package | What changed |
|---|---|
| Incremental render | dirty.go + render_v2.go |
| Animated spinners | spinner.go |`
	lines := layoutRichMarkdown(raw, 90, fgGreen)
	combined := ""
	for _, ln := range lines {
		combined += stripANSI(ln) + "\n"
	}
	if stringsContains(combined, "|---|") {
		t.Fatalf("raw table markdown visible:\n%s", combined)
	}
	if !stringsContains(combined, "Work package") || !stringsContains(combined, "╭") {
		t.Fatalf("expected bordered table:\n%s", combined)
	}
}

func TestUserRandomDataTable(t *testing.T) {
	raw := "Sure! Here's a nice table:\n\n| # | Name | City |\n|---|---|---|\n| 1 | Alice | Reykjavik |\n"
	lines := layoutRichMarkdown(raw, 100, fgGreen)
	combined := ""
	for _, ln := range lines {
		combined += stripANSI(ln) + "\n"
	}
	if stringsContains(combined, "|---|") {
		t.Fatalf("raw markdown table still visible:\n%s", combined)
	}
	if !stringsContains(combined, "╭") || !stringsContains(combined, "│") {
		t.Fatalf("expected box table, got:\n%s", combined)
	}
}

func TestWideTableFitsTerminal(t *testing.T) {
	raw := `| # | Name | City | Score | Status | Joined |
|---|---|---|---|---|---|
| 1 | Alice | Reykjavik | 94.2 | Active | 2023-03-12 |`
	// Terminals auto-wrap at cols; scrollback layout uses cols-1.
	lines := layoutRichMarkdown(raw, 79, "")
	if len(lines) < 5 {
		t.Fatalf("lines = %d", len(lines))
	}
	for _, ln := range lines {
		if visibleWidth(ln) > 79 {
			t.Fatalf("row too wide (%d): %q", visibleWidth(ln), stripANSI(ln))
		}
	}
}

func TestTableBorderRowsMatchDataWidth(t *testing.T) {
	raw := `| ID | Name | Age | City | Score | Active |
|---|---|---|---|---|---|
| 1 | Alice | 29 | Prague | 87.5 | ✓ |
| 2 | Bob | 34 | Reykjavík | 92.1 | ✗ |`
	lines := renderMarkdown(raw, 120, "")
	if len(lines) < 6 {
		t.Fatalf("lines = %d", len(lines))
	}
	want := visibleWidth(lines[1])
	for i, ln := range lines {
		if w := visibleWidth(ln); w != want {
			t.Fatalf("row %d width %d != %d: %q", i, w, want, stripANSI(ln))
		}
	}
}
