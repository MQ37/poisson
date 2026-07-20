package tools

import (
	"context"
	"testing"
)

// sampleDDGHTML is a trimmed excerpt of a real html.duckduckgo.com/html/
// response (two results), captured 2026-07-19 while validating the scrape
// target still works keylessly.
const sampleDDGHTML = `
<div class="links_main links_deep result__body">
  <h2 class="result__title">
    <a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fen.wikipedia.org%2Fwiki%2FPoisson_distribution&amp;rut=abc">Poisson distribution - Wikipedia</a>
  </h2>
  <a class="result__snippet" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fen.wikipedia.org%2Fwiki%2FPoisson_distribution&amp;rut=abc">Learn about the <b>Poisson</b> <b>distribution</b>, a discrete probability distribution.</a>
</div>
<div class="links_main links_deep result__body">
  <h2 class="result__title">
    <a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fgithub.com%2Fexample%2Fpoisson&amp;rut=def">GitHub - example/poisson</a>
  </h2>
  <a class="result__snippet" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fgithub.com%2Fexample%2Fpoisson&amp;rut=def">A minimal coding agent CLI.</a>
</div>
`

func TestParseWebSearchResults(t *testing.T) {
	results := parseWebSearchResults(sampleDDGHTML, 10)
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2: %+v", len(results), results)
	}

	if results[0].Title != "Poisson distribution - Wikipedia" {
		t.Errorf("title = %q", results[0].Title)
	}
	if results[0].URL != "https://en.wikipedia.org/wiki/Poisson_distribution" {
		t.Errorf("url = %q", results[0].URL)
	}
	if results[0].Snippet != "Learn about the Poisson distribution, a discrete probability distribution." {
		t.Errorf("snippet = %q", results[0].Snippet)
	}

	if results[1].URL != "https://github.com/example/poisson" {
		t.Errorf("second url = %q", results[1].URL)
	}
}

func TestParseWebSearchResults_RespectsNumLimit(t *testing.T) {
	results := parseWebSearchResults(sampleDDGHTML, 1)
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
}

func TestParseWebSearchResults_NoMatches(t *testing.T) {
	if results := parseWebSearchResults("<html><body>no results here</body></html>", 10); len(results) != 0 {
		t.Errorf("got %d results from HTML with no result blocks, want 0", len(results))
	}
}

func TestDecodeDDGRedirect(t *testing.T) {
	got := decodeDDGRedirect("//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fpath%3Fq%3D1&amp;rut=xyz")
	want := "https://example.com/path?q=1"
	if got != want {
		t.Errorf("decodeDDGRedirect() = %q, want %q", got, want)
	}
}

func TestDecodeDDGRedirect_NoUddgParam(t *testing.T) {
	if got := decodeDDGRedirect("//duckduckgo.com/l/?rut=xyz"); got != "" {
		t.Errorf("decodeDDGRedirect() with no uddg param = %q, want empty", got)
	}
}

func TestWebSearchTool_SchemaAndName(t *testing.T) {
	tool := NewWebSearchTool()
	if tool.Name() != "web_search" {
		t.Errorf("Name() = %q, want web_search", tool.Name())
	}
	if tool.Description() == "" {
		t.Error("Description() is empty")
	}
	if len(tool.Schema()) == 0 {
		t.Error("Schema() is empty")
	}
}

func TestWebSearchTool_Execute_RequiresQuery(t *testing.T) {
	tool := NewWebSearchTool()
	res, err := tool.Execute(context.Background(), []byte(`{}`))
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if res.Error == "" {
		t.Error("expected error for missing query, got none")
	}
}
