package tui

import "testing"

func TestParseRenderTagFileOnly(t *testing.T) {
	file, ref, from, to, ok := parseRenderTag(`<render file="internal/tui/block.go"/>`)
	if !ok || file != "internal/tui/block.go" || ref != "" || from != 0 || to != 0 {
		t.Fatalf("got file=%q ref=%q from=%d to=%d ok=%v", file, ref, from, to, ok)
	}
}

func TestParseRenderTagWithRange(t *testing.T) {
	file, ref, from, to, ok := parseRenderTag(`<render file="foo.go" from="10" to="50"/>`)
	if !ok || file != "foo.go" || ref != "" || from != 10 || to != 50 {
		t.Fatalf("got file=%q ref=%q from=%d to=%d ok=%v", file, ref, from, to, ok)
	}
}

// TestParseRenderTagUnquotedNumbers matches the shape from the original
// feature request (from=0 to=50, no quotes on the numeric attributes).
func TestParseRenderTagUnquotedNumbers(t *testing.T) {
	file, _, from, to, ok := parseRenderTag(`<render file="foo.go" from=0 to=50/>`)
	if !ok || file != "foo.go" || from != 0 || to != 50 {
		t.Fatalf("got file=%q from=%d to=%d ok=%v", file, from, to, ok)
	}
}

func TestParseRenderTagWithRef(t *testing.T) {
	file, ref, from, to, ok := parseRenderTag(`<render file="foo.go" ref="HEAD~2" from="1" to="20"/>`)
	if !ok || file != "foo.go" || ref != "HEAD~2" || from != 1 || to != 20 {
		t.Fatalf("got file=%q ref=%q from=%d to=%d ok=%v", file, ref, from, to, ok)
	}
}

// TestParseRenderTagAttributeOrderIndependent guards against a model that
// emits ref/from/to in a different order than this repo's own examples.
func TestParseRenderTagAttributeOrderIndependent(t *testing.T) {
	file, ref, from, to, ok := parseRenderTag(`<render to="20" file="foo.go" from="1" ref="main"/>`)
	if !ok || file != "foo.go" || ref != "main" || from != 1 || to != 20 {
		t.Fatalf("got file=%q ref=%q from=%d to=%d ok=%v", file, ref, from, to, ok)
	}
}

func TestParseRenderTagMissingFileNotOk(t *testing.T) {
	if _, _, _, _, ok := parseRenderTag(`<render from="1" to="20"/>`); ok {
		t.Fatal("expected not-ok with no file attribute")
	}
}

// TestParseRenderTagMidSentenceNotOk is the exact rule the system prompt
// must teach the model: the tag has to be alone on its own line. Anything
// else on the same line means the whole line is left as literal text
// instead of expanding into a widget.
func TestParseRenderTagMidSentenceNotOk(t *testing.T) {
	if _, _, _, _, ok := parseRenderTag(`see <render file="foo.go"/> for details`); ok {
		t.Fatal("expected not-ok when the tag shares a line with other text")
	}
}

func TestParseRenderTagNotATagNotOk(t *testing.T) {
	if _, _, _, _, ok := parseRenderTag(`just some prose`); ok {
		t.Fatal("expected not-ok for plain text")
	}
}
