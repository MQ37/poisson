package tools

import (
	"context"
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
