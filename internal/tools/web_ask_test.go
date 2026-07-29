package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/mq37/poisson/internal/auth"
)

func TestHasXAIAuth(t *testing.T) {
	cases := []struct {
		name  string
		store auth.AuthStore
		want  bool
	}{
		{"nil store", nil, false},
		{"empty store", auth.AuthStore{}, false},
		{"no xai entry", auth.AuthStore{"anthropic": {Type: "oauth", Access: "tok"}}, false},
		{"xai api_key type", auth.AuthStore{"xai": {Type: "api_key", Access: "tok"}}, false},
		{"xai oauth no access token", auth.AuthStore{"xai": {Type: "oauth"}}, false},
		{"xai oauth with access token", auth.AuthStore{"xai": {Type: "oauth", Access: "tok"}}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := hasXAIAuth(c.store); got != c.want {
				t.Errorf("hasXAIAuth() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestWebAskTool_SchemaAndName(t *testing.T) {
	tool := NewWebAskTool(nil)
	if tool.Name() != "web_ask" {
		t.Errorf("Name() = %q, want web_ask", tool.Name())
	}
	if tool.Description() == "" {
		t.Error("Description() is empty")
	}
	if len(tool.Schema()) == 0 {
		t.Error("Schema() is empty")
	}
}

func TestWebAskTool_Execute_RequiresQuery(t *testing.T) {
	tool := NewWebAskTool(nil)
	res, err := tool.Execute(context.Background(), []byte(`{}`))
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if res.Error == "" {
		t.Error("expected error for missing query, got none")
	}
}

// TestWebAskTool_GrokRecordsSpend covers the accounting path end to end
// through the public Execute entry point: an explicit provider=grok call
// must bank the xAI Responses API's own reported cost.
func TestWebAskTool_GrokRecordsSpend(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"output":[{"type":"message","content":[{"type":"output_text","text":"{\"results\":[]}"}]}],"usage":{"input_tokens":100,"output_tokens":10,"cost_in_usd_ticks":500000}}`))
	}))
	defer srv.Close()
	defer swapGrokResponsesURL(t, srv.URL)()

	store := auth.AuthStore{"xai": auth.AuthEntry{Type: "oauth", Access: "tok", Expires: 1 << 62}}
	tool := NewWebAskTool(store)
	var recorded []WebCall
	tool.SetUsageFn(func(c WebCall) { recorded = append(recorded, c) })

	res, err := tool.Execute(context.Background(), mustJSON(t, map[string]any{
		"query": "q", "provider": "grok",
	}))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if res.Error != "" {
		t.Fatalf("unexpected error: %s", res.Error)
	}
	if len(recorded) != 1 || recorded[0].Provider != "xai" || recorded[0].Cost != 0.00005 || recorded[0].Purpose != webPurposeAsk {
		t.Errorf("recorded = %+v, want xAI's own cost banked as %s", recorded, webPurposeAsk)
	}
}

func TestWebAskTool_Execute_UnknownProvider(t *testing.T) {
	tool := NewWebAskTool(nil)
	res, err := tool.Execute(context.Background(), []byte(`{"query":"test","provider":"bing"}`))
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if res.Error == "" {
		t.Error("expected error for unknown provider, got none")
	}
}
