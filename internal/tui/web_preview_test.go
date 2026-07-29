package tui

import "testing"

// TestToolInputPreviewShowsWebBackend: which backend a web tool ran on is the
// difference between a free scrape and a billed API call, so it has to be
// visible on the card — and absent when the call used the default.
func TestToolInputPreviewShowsWebBackend(t *testing.T) {
	cases := []struct {
		name  string
		tool  string
		input string
		want  string
	}{
		{"fetch default", "fetch", `{"url":"https://example.com"}`, "https://example.com"},
		{"fetch anthropic", "fetch", `{"url":"https://example.com","provider":"anthropic"}`, "https://example.com [anthropic]"},
		{"search default", "web_search", `{"query":"go slices"}`, "go slices"},
		{"search anthropic", "web_search", `{"query":"go slices","provider":"anthropic"}`, "go slices [anthropic]"},
		{"ask grok", "web_ask", `{"query":"why","provider":"grok"}`, "why [grok]"},
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
