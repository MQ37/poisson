package tui

import (
	"strings"
	"testing"
)

func TestRenderHeader(t *testing.T) {
	s := StatusSnapshot{
		Cwd:           "/home/mq/workdir/poisson",
		Model:         "ollama/glm-5.2:cloud",
		ContextTokens: 87000,
		ContextWindow: 280000,
		ShowTokens:    true,
	}
	line := stripANSI(s.RenderHeader(80))
	if !strings.Contains(line, "workdir") {
		t.Fatalf("missing cwd: %q", line)
	}
	if !strings.Contains(line, "87") || !strings.Contains(line, "280") {
		t.Fatalf("missing token counts: %q", line)
	}
}
