package tui

import (
	"strings"
	"testing"
)

// TestCreateSandboxPreviewShowsFullHostPath is a regression test: the
// default fallback (raw truncated JSON) put the hostPath value near the end
// of the line, behind the JSON keys/braces, so a normal-width card cut the
// path off after a handful of characters — exactly the value the tool's own
// human approval prompt exists to show in full. name/hostPath must come
// through as plain text with the path intact.
func TestCreateSandboxPreviewShowsFullHostPath(t *testing.T) {
	input := []byte(`{"name":"test-ondrej-sojka-docker","hostPath":"/home/mq/workdir/apify/hiring/hiring-ai-ondrej-sojka"}`)
	want := "test-ondrej-sojka-docker — /home/mq/workdir/apify/hiring/hiring-ai-ondrej-sojka"
	if got := toolInputPreview("create_sandbox", input); got != want {
		t.Errorf("preview = %q, want %q", got, want)
	}
	if got := toolCollapsedReason("create_sandbox", input); got != want {
		t.Errorf("card preview = %q, want %q", got, want)
	}
}

// TestCreateSandboxPreviewIncludesMounts: extra mounts (beyond hostPath) are
// part of what human approval is granting, so they belong in the preview too.
func TestCreateSandboxPreviewIncludesMounts(t *testing.T) {
	input := []byte(`{"name":"s","hostPath":"/a","mounts":[{"hostPath":"/b","containerPath":"/c"}]}`)
	want := "s — /a +/b"
	if got := toolInputPreview("create_sandbox", input); got != want {
		t.Errorf("preview = %q, want %q", got, want)
	}
}

// TestCreateSandboxPreviewNameOnly: a sandbox with no hostPath/mounts (no
// workspace) still shows its name instead of falling back to raw JSON.
func TestCreateSandboxPreviewNameOnly(t *testing.T) {
	input := []byte(`{"name":"isolated"}`)
	if got, want := toolInputPreview("create_sandbox", input), "isolated"; got != want {
		t.Errorf("preview = %q, want %q", got, want)
	}
}

// TestSandboxCpPreviewShowsBothPaths covers both copy directions: "in" reads
// hostPath -> workspacePath, "out" reads the reverse, matching the tool's
// own semantics rather than always printing hostPath first.
func TestSandboxCpPreviewShowsBothPaths(t *testing.T) {
	in := []byte(`{"sandboxId":"s","direction":"in","hostPath":"/host/data","workspacePath":"data"}`)
	if got, want := toolInputPreview("sandbox_cp", in), "/host/data → data"; got != want {
		t.Errorf("in preview = %q, want %q", got, want)
	}
	out := []byte(`{"sandboxId":"s","direction":"out","hostPath":"/host/data","workspacePath":"data"}`)
	if got, want := toolInputPreview("sandbox_cp", out), "data → /host/data"; got != want {
		t.Errorf("out preview = %q, want %q", got, want)
	}
}

// TestToolInputPreviewFullDropsCaps is a regression test: the collapsed
// preview intentionally caps every field (e.g. create_sandbox's mount list
// at 200 bytes) to fit one line, but toolInputPreviewFull backs the
// expanded body — expanding a card must show the whole value, not the same
// capped text with the "..." moved further out.
func TestToolInputPreviewFullDropsCaps(t *testing.T) {
	longPath := "/host/" + strings.Repeat("a", 300)
	capped := []byte(`{"name":"s","hostPath":"` + longPath + `"}`)
	if got := toolInputPreview("create_sandbox", capped); strings.Contains(got, longPath) {
		t.Fatalf("collapsed preview unexpectedly contains the full long path: %q", got)
	}
	full := toolInputPreviewFull("create_sandbox", capped)
	if !strings.Contains(full, longPath) {
		t.Errorf("full preview = %q, want it to contain the untruncated path %q", full, longPath)
	}
	if strings.Contains(full, "...") {
		t.Errorf("full preview = %q, should not be marked truncated", full)
	}
}

// TestToolExpandedInputLinesShowsFullText: the card's expanded body
// (toolExpandedInputLines, what Ctrl+E reveals) must render a long field in
// full across as many wrapped lines as it needs, not truncate it the same
// way the always-visible collapsed line does.
func TestToolExpandedInputLinesShowsFullText(t *testing.T) {
	longTask := strings.Repeat("do the thing carefully and report back. ", 20)
	input := []byte(`{"name":"scout","task":"` + longTask + `"}`)
	lines := toolExpandedInputLines("subagent", input, 40)
	joined := strings.Join(lines, " ")
	// toolCollapsedReason prefers name over task for subagent, but the
	// expanded body has no such shortcut field to fall back to — it must
	// carry the actual (long) input, not a truncated stand-in.
	if strings.Contains(joined, "...") {
		t.Errorf("expanded input lines truncated: %q", joined)
	}
}
