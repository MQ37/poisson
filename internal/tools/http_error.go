package tools

import (
	"regexp"
	"strings"
)

// htmlTitleRe extracts a document's <title> for summarizing HTML error pages.
var htmlTitleRe = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)

// looksLikeHTML reports whether body is an HTML document rather than a
// plain-text/JSON API error body.
func looksLikeHTML(body []byte) bool {
	lower := strings.ToLower(strings.TrimSpace(string(body)))
	return strings.HasPrefix(lower, "<!doctype") || strings.HasPrefix(lower, "<html")
}

// sanitizeHTTPErrorBody turns a non-200 response body into a short, readable
// error string. Some upstreams (Vercel's bot-check checkpoint, Cloudflare
// block pages, DDG's captcha page, ...) return a full HTML document as the
// error body instead of JSON/plain text; dumping that verbatim into a tool
// error is unreadable noise. If body isn't HTML, it's returned as-is
// (trimmed) since it's presumably a useful JSON/plain-text API error.
func sanitizeHTTPErrorBody(raw []byte) string {
	if !looksLikeHTML(raw) {
		return strings.TrimSpace(string(raw))
	}
	if m := htmlTitleRe.FindSubmatch(raw); m != nil {
		if title := cleanResultText(string(m[1])); title != "" {
			return title + " (HTML error page, not JSON)"
		}
	}
	return "HTML error page (no title)"
}
