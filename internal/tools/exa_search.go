package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const (
	exaTokenURL  = "https://exa.ai/api/token/issue"
	exaSearchURL = "https://exa.ai/api/search"
	exaUserAgent = "Mozilla/5.0 (X11; Linux x86_64; rv:151.0) Gecko/20100101 Firefox/151.0"
)

// ExaSearchTool searches the web via exa.ai's free landing-page API.
type ExaSearchTool struct{}

func NewExaSearchTool() *ExaSearchTool { return &ExaSearchTool{} }

func (t *ExaSearchTool) Name() string { return "exa_search" }

func (t *ExaSearchTool) Description() string {
	return "Search the web via exa.ai. Returns results with titles, URLs, and excerpts. Also returns an AI-generated summary of the results."
}

func (t *ExaSearchTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": {"type": "string", "description": "Search query"},
			"num": {"type": "integer", "description": "Number of results (default: 10, max: 100)"},
			"type": {"type": "string", "description": "Search type: keyword | neural (default: keyword)"},
			"verbose": {"type": "boolean", "description": "Include full text excerpts (default: false)"}
		},
		"required": ["query"]
	}`)
}

func (t *ExaSearchTool) Execute(ctx context.Context, input json.RawMessage) (ToolResult, error) {
	var params struct {
		Query   string `json:"query"`
		Num     int    `json:"num"`
		Type    string `json:"type"`
		Verbose bool   `json:"verbose"`
	}
	if err := json.Unmarshal(input, &params); err != nil {
		return ToolResult{Error: "invalid input: " + err.Error()}, nil
	}
	if params.Query == "" {
		return ToolResult{Error: "query is required"}, nil
	}
	if params.Num == 0 {
		params.Num = 10
	}
	if params.Type == "" {
		params.Type = "keyword"
	}

	// Get JWT token.
	token, err := getExaToken()
	if err != nil {
		return ToolResult{Error: "token issue failed: " + err.Error()}, nil
	}

	// Search.
	result, retryErr := doExaSearch(ctx, params.Query, token, params.Num, params.Type, params.Verbose)
	if retryErr != nil {
		// On 401, re-issue JWT and retry once.
		if retryErr.StatusCode == 401 {
			token, err = issueExaToken()
			if err != nil {
				return ToolResult{Error: "token re-issue failed: " + err.Error()}, nil
			}
			result, retryErr = doExaSearch(ctx, params.Query, token, params.Num, params.Type, params.Verbose)
		}
		if retryErr != nil {
			if retryErr.StatusCode == 429 {
				return ToolResult{Error: "exa_search rate limited. Try again later or use fetch + manual parsing."}, nil
			}
			return ToolResult{Error: retryErr.Error()}, nil
		}
	}

	return ToolResult{Content: result}, nil
}

type exaHTTPError struct {
	StatusCode int
	Body       string
}

func (e *exaHTTPError) Error() string {
	return fmt.Sprintf("exa.ai HTTP %d: %s", e.StatusCode, e.Body)
}

func doExaSearch(ctx context.Context, query, token string, num int, searchType string, verbose bool) (string, *exaHTTPError) {
	body := map[string]interface{}{
		"query":      query,
		"numResults": num,
		"type":       searchType,
	}
	if verbose {
		body["contents"] = map[string]interface{}{"text": true}
	}
	jsonBody, _ := json.Marshal(body)

	req, _ := http.NewRequestWithContext(ctx, "POST", exaSearchURL, bytes.NewReader(jsonBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", exaUserAgent)
	req.Header.Set("Origin", "https://exa.ai")
	req.Header.Set("Referer", "https://exa.ai/")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", &exaHTTPError{StatusCode: 0, Body: err.Error()}
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(resp.Body)
		return "", &exaHTTPError{StatusCode: resp.StatusCode, Body: string(raw)}
	}

	data, _ := io.ReadAll(resp.Body)
	return string(data), nil
}

func getExaToken() (string, error) {
	// Check cache.
	home, _ := os.UserHomeDir()
	cachePath := filepath.Join(home, ".poisson", "exa-token.json")
	if data, err := os.ReadFile(cachePath); err == nil {
		var cache struct {
			Token     string `json:"token"`
			ExpiresAt int64  `json:"expiresAt"`
		}
		if json.Unmarshal(data, &cache) == nil && cache.Token != "" {
			if cache.ExpiresAt/1000-10 > time.Now().Unix() {
				return cache.Token, nil
			}
		}
	}
	return issueExaToken()
}

func issueExaToken() (string, error) {
	req, _ := http.NewRequest("POST", exaTokenURL, nil)
	req.Header.Set("User-Agent", exaUserAgent)
	req.Header.Set("Origin", "https://exa.ai")
	req.Header.Set("Referer", "https://exa.ai/")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("token issue HTTP %d: %s", resp.StatusCode, string(raw))
	}

	var tokenResp struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expiresAt"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return "", err
	}
	if tokenResp.Token == "" {
		return "", fmt.Errorf("no token in response")
	}

	// Cache token.
	home, _ := os.UserHomeDir()
	cachePath := filepath.Join(home, ".poisson", "exa-token.json")
	cacheData, _ := json.Marshal(tokenResp)
	os.MkdirAll(filepath.Dir(cachePath), 0o700)
	os.WriteFile(cachePath, cacheData, 0o600)

	return tokenResp.Token, nil
}
