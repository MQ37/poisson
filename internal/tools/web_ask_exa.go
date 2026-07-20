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

	exaMaxBytes    = 1 << 20 // 1 MiB: cap search response (OOM guard)
	exaErrMaxBytes = 4 << 10 // 4 KiB: cap error bodies
)

// execExaSearch runs the exa.ai backend for WebAskTool: token issue (cached)
// + search, with one retry after re-issuing the token on 401. Returns the
// raw exa.ai JSON response (results + AI-generated summary) unmodified.
func execExaSearch(ctx context.Context, query string, num int, searchType string, verbose bool) (string, error) {
	if searchType == "" {
		searchType = "keyword"
	}

	token, err := getExaToken(ctx)
	if err != nil {
		return "", fmt.Errorf("token issue failed: %w", err)
	}

	result, retryErr := doExaSearch(ctx, query, token, num, searchType, verbose)
	if retryErr != nil {
		if retryErr.StatusCode == 401 {
			token, err = issueExaToken(ctx)
			if err != nil {
				return "", fmt.Errorf("token re-issue failed: %w", err)
			}
			result, retryErr = doExaSearch(ctx, query, token, num, searchType, verbose)
		}
		if retryErr != nil {
			if retryErr.StatusCode == 429 {
				return "", fmt.Errorf("exa.ai rate limited. Try again later or use fetch + manual parsing")
			}
			return "", retryErr
		}
	}

	return result, nil
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

	searchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(searchCtx, "POST", exaSearchURL, bytes.NewReader(jsonBody))
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
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, exaErrMaxBytes))
		return "", &exaHTTPError{StatusCode: resp.StatusCode, Body: string(raw)}
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, exaMaxBytes))
	if err != nil {
		return "", &exaHTTPError{StatusCode: 0, Body: "read response: " + err.Error()}
	}
	return string(data), nil
}

func getExaToken(ctx context.Context) (string, error) {
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
	return issueExaToken(ctx)
}

func issueExaToken(ctx context.Context) (string, error) {
	tokenCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(tokenCtx, "POST", exaTokenURL, nil)
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
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, exaErrMaxBytes))
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
