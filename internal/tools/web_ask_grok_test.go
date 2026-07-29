package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// grokUsageFixture is a trimmed real Responses API reply (probed live against
// api.x.ai for this feature): cost_in_usd_ticks is xAI's own exact per-call
// price, 1e10 ticks per USD.
const grokUsageFixture = `{
	"output": [{"type":"message","content":[{"type":"output_text","text":"{\"results\":[]}"}]}],
	"usage": {
		"input_tokens": 2505,
		"input_tokens_details": {"cached_tokens": 1408},
		"output_tokens": 191,
		"cost_in_usd_ticks": 71303500
	}
}`

// TestGrokSpend_ParsesRealUsageShape pins grokSpend against the wire shape
// probed from api.x.ai, including the ticks-to-USD conversion (71303500 ticks
// = $0.0071303500).
func TestGrokSpend_ParsesRealUsageShape(t *testing.T) {
	spend := grokSpend([]byte(grokUsageFixture))
	if spend.Provider != "xai" || spend.Model != grokModel || spend.Purpose != webPurposeAsk {
		t.Fatalf("spend = %+v", spend)
	}
	if spend.Usage.InputTokens != 2505-1408 || spend.Usage.CacheReadTokens != 1408 || spend.Usage.OutputTokens != 191 {
		t.Errorf("usage = %+v, want input=1097 cacheRead=1408 output=191", spend.Usage)
	}
	if got, want := spend.Cost, 0.00713035; got < want-1e-9 || got > want+1e-9 {
		t.Errorf("cost = %v, want %v", got, want)
	}
}

// TestGrokSpend_MalformedJSONIsZeroValue: a reply that isn't even valid JSON
// must not panic and must not fabricate a billable call.
func TestGrokSpend_MalformedJSONIsZeroValue(t *testing.T) {
	if spend := grokSpend([]byte("not json")); spend != (WebCall{}) {
		t.Errorf("spend = %+v, want the zero value", spend)
	}
}

// TestDoGrokSearch_BillsEvenWhenResultsUnusable: the reply is parsed for
// spend before extractGrokResults runs, so a response the extractor can't
// make sense of (empty output) is still billed — xAI charged for it either
// way.
func TestDoGrokSearch_BillsEvenWhenResultsUnusable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"output":[],"usage":{"input_tokens":100,"output_tokens":10,"cost_in_usd_ticks":500000}}`))
	}))
	defer srv.Close()
	defer swapGrokResponsesURL(t, srv.URL)()

	result, spend, _, err := doGrokSearch(context.Background(), "q", 5, "tok")
	if err != nil {
		t.Fatalf("doGrokSearch: %v", err)
	}
	if !strings.Contains(result, `"results":null`) {
		t.Errorf("result = %q, want an empty (nil) results list", result)
	}
	if spend.Cost != 0.00005 || spend.Usage.InputTokens != 100 {
		t.Errorf("spend = %+v", spend)
	}
}

// TestDoGrokSearch_APIErrorStillBills: xAI's own error field (200 status,
// error inside the body) still comes with a usage object — the call must be
// billed even though the caller reports the error and drops the result.
func TestDoGrokSearch_APIErrorStillBills(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"error":{"message":"boom"},"usage":{"input_tokens":50,"output_tokens":0,"cost_in_usd_ticks":100000}}`))
	}))
	defer srv.Close()
	defer swapGrokResponsesURL(t, srv.URL)()

	_, spend, _, err := doGrokSearch(context.Background(), "q", 5, "tok")
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want the API error", err)
	}
	if spend.Cost != 0.00001 {
		t.Errorf("spend.Cost = %v, want 0.00001", spend.Cost)
	}
}

// swapGrokResponsesURL points doGrokSearch at an httptest server for one
// test, returning a func that restores the real URL — call via defer.
func swapGrokResponsesURL(t *testing.T, url string) func() {
	t.Helper()
	orig := grokResponsesURL
	grokResponsesURL = url
	return func() { grokResponsesURL = orig }
}
