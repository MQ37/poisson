package tools

import "testing"

func TestSanitizeHTTPErrorBody_PlainText(t *testing.T) {
	if got := sanitizeHTTPErrorBody([]byte(`{"error":"rate limited"}`)); got != `{"error":"rate limited"}` {
		t.Errorf("got %q, want passthrough JSON", got)
	}
}

func TestSanitizeHTTPErrorBody_HTMLWithTitle(t *testing.T) {
	// Trimmed excerpt of the Vercel bot-check checkpoint page returned by
	// exa.ai's token-issue endpoint on 429.
	html := `<!DOCTYPE html><html lang="en"><head><meta charset="utf-8">` +
		`<title>Vercel Security Checkpoint</title></head><body>...</body></html>`
	got := sanitizeHTTPErrorBody([]byte(html))
	want := "Vercel Security Checkpoint (HTML error page, not JSON)"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSanitizeHTTPErrorBody_HTMLNoTitle(t *testing.T) {
	got := sanitizeHTTPErrorBody([]byte(`<!DOCTYPE html><html><body>blocked</body></html>`))
	if got != "HTML error page (no title)" {
		t.Errorf("got %q", got)
	}
}

func TestLooksLikeHTML(t *testing.T) {
	cases := []struct {
		body string
		want bool
	}{
		{`<!DOCTYPE html><html></html>`, true},
		{`<html><head></head></html>`, true},
		{`  <!doctype html>`, true},
		{`{"ok":true}`, false},
		{`plain text error`, false},
	}
	for _, c := range cases {
		if got := looksLikeHTML([]byte(c.body)); got != c.want {
			t.Errorf("looksLikeHTML(%q) = %v, want %v", c.body, got, c.want)
		}
	}
}
