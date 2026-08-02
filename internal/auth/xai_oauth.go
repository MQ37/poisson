package auth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// xAI OAuth constants (from SPEC §4.3).
const (
	xaiClientID      = "b1a00492-073a-47ea-816f-4c329264a828"
	xaiDeviceCodeURL = "https://auth.x.ai/oauth2/device/code"
	xaiScopes        = "openid profile email offline_access grok-cli:access api:access"
)

// xaiTokenURL is a var (not a const) so tests can point it at a local
// httptest server instead of the real xAI endpoint — same idiom as
// internal/tools/web_ask_grok.go's grokResponsesURL.
var xaiTokenURL = "https://auth.x.ai/oauth2/token"

// xaiHTTPClient bounds every token request (connection + body read) so a stalled
// device-code poll or a hung refresh can't block the provider auth flow forever.
var xaiHTTPClient = &http.Client{Timeout: 30 * time.Second}

// LoginXAI performs the xAI device-code OAuth flow (for headless/VPS/desktop).
// xAI does not support arbitrary loopback redirect URIs, so device-code is
// the only reliable flow.
func LoginXAI() (*AuthEntry, error) {
	return loginXAIDeviceCode()
}

// loginXAIDeviceCode performs the device-code OAuth flow.
func loginXAIDeviceCode() (*AuthEntry, error) {
	// Request device code.
	form := url.Values{}
	form.Set("client_id", xaiClientID)
	form.Set("scope", xaiScopes)

	resp, err := xaiHTTPClient.PostForm(xaiDeviceCodeURL, form)
	if err != nil {
		return nil, fmt.Errorf("device code request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("device code HTTP %d: %s", resp.StatusCode, string(raw))
	}

	var device struct {
		DeviceCode      string `json:"device_code"`
		UserCode        string `json:"user_code"`
		VerificationURI string `json:"verification_uri"`
		ExpiresIn       int    `json:"expires_in"`
		Interval        int    `json:"interval"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&device); err != nil {
		return nil, fmt.Errorf("decode device code: %w", err)
	}

	fmt.Printf("Go to %s and enter code: %s\n", device.VerificationURI, device.UserCode)

	// Poll for token.
	interval := 5
	if device.Interval > 0 {
		interval = device.Interval
	}
	deadline := time.Now().Add(time.Duration(device.ExpiresIn) * time.Second)

	for time.Now().Before(deadline) {
		time.Sleep(time.Duration(interval) * time.Second)

		form := url.Values{}
		form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
		form.Set("client_id", xaiClientID)
		form.Set("device_code", device.DeviceCode)

		resp, err := xaiHTTPClient.PostForm(xaiTokenURL, form)
		if err != nil {
			continue
		}

		if resp.StatusCode == 200 {
			entry, parseErr := parseXAITokenResponse(resp.Body, "")
			resp.Body.Close()
			return entry, parseErr
		}

		var errResp struct {
			Error string `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&errResp)
		resp.Body.Close()

		if errResp.Error == "authorization_pending" {
			continue
		}
		if errResp.Error == "slow_down" {
			interval += 5
			continue
		}
		if errResp.Error == "access_denied" || errResp.Error == "authorization_denied" {
			return nil, fmt.Errorf("device authorization denied")
		}
		if errResp.Error == "expired_token" {
			return nil, fmt.Errorf("device code expired — please re-run login")
		}
		return nil, fmt.Errorf("device code error: %s", errResp.Error)
	}

	return nil, fmt.Errorf("device code expired")
}

// StoreMu guards every in-process read/write of a shared AuthStore map.
// AnthropicProvider, OpenAIProvider, XAIProvider, and WebAskTool's grok
// backend all hold a reference to the *same* AuthStore map instance (wired
// once in main.go) — but each used to guard it with its OWN, unrelated
// mutex (each provider struct's private authMu field, or this package's
// own now-renamed xaiRefreshMu). Different mutexes give zero mutual
// exclusion for the map itself: a batched web_ask(xai) call refreshing
// "xai" under the old xaiRefreshMu, concurrent with the main provider
// refreshing its own near-expiry token under its own authMu, both write
// the same underlying map with no shared lock between them — an
// unsynchronized concurrent map write, which Go treats as an
// unrecoverable fatal error (not a catchable panic), killing the whole
// process. Every provider now locks this single, exported mutex instead of
// one of its own, so any two callers touching the same AuthStore instance
// — regardless of which provider or package they belong to — are
// correctly serialized.
var StoreMu sync.Mutex

// EnsureXAIFresh returns the current "xai" OAuth entry, proactively
// refreshing and persisting it first if it's within skewMs of expiring.
// Safe for concurrent callers sharing the same AuthStore instance, AND
// across independent processes sharing the same auth.json — see
// RefreshIfExpired's doc for why a plain in-process lock alone isn't
// enough (two processes racing to refresh the same rotating refresh_token).
func EnsureXAIFresh(store AuthStore, skewMs int64) (AuthEntry, error) {
	StoreMu.Lock()
	defer StoreMu.Unlock()
	entry, ok := store["xai"]
	if !ok || entry.Type != "oauth" {
		return AuthEntry{}, fmt.Errorf("no xAI OAuth credentials — run: px login xai")
	}
	refreshed, err := RefreshIfExpired(store, "xai", skewMs, RefreshXAIToken)
	if err != nil {
		return entry, nil // stale but usable; caller's request may still 401 and retry via ForceRefreshXAI
	}
	return refreshed, nil
}

// ForceRefreshXAI unconditionally refreshes and persists the "xai" entry —
// used reactively after a request comes back 401 despite EnsureXAIFresh's
// proactive check (e.g. the token was revoked server-side early). Safe for
// concurrent callers sharing the same AuthStore instance, and across
// processes (see RefreshIfExpired's doc). Reads the refresh token straight
// from store["xai"] (kept in sync by EnsureXAIFresh/this function itself
// under StoreMu) rather than taking one as a parameter, so a caller can
// never accidentally force-refresh with an already-stale copy it cached
// itself before this function's own cross-process check had a chance to
// pick up a fresher one.
func ForceRefreshXAI(store AuthStore) (AuthEntry, error) {
	StoreMu.Lock()
	defer StoreMu.Unlock()
	return ForceRefresh(store, "xai", RefreshXAIToken)
}

// RefreshXAIToken refreshes an expired xAI access token.
func RefreshXAIToken(refreshToken string) (*AuthEntry, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", xaiClientID)

	resp, err := xaiHTTPClient.PostForm(xaiTokenURL, form)
	if err != nil {
		return nil, fmt.Errorf("refresh: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("refresh HTTP %d: %s", resp.StatusCode, string(raw))
	}

	return parseXAITokenResponse(resp.Body, refreshToken)
}

// parseXAITokenResponse parses the token response.
func parseXAITokenResponse(body io.Reader, keepRefresh string) (*AuthEntry, error) {
	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.NewDecoder(body).Decode(&tokenResp); err != nil {
		return nil, fmt.Errorf("decode token response: %w", err)
	}
	refresh := tokenResp.RefreshToken
	if refresh == "" {
		refresh = keepRefresh
	}
	return &AuthEntry{
		Type:    "oauth",
		Access:  tokenResp.AccessToken,
		Refresh: refresh,
		Expires: nowMillis() + int64(tokenResp.ExpiresIn)*1000 - 5*60*1000,
	}, nil
}
