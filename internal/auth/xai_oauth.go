package auth

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// xAI OAuth constants (from SPEC §4.3).
const (
	xaiClientID      = "b1a00492-073a-47ea-816f-4c329264a828"
	xaiTokenURL      = "https://auth.x.ai/oauth2/token"
	xaiDeviceCodeURL = "https://auth.x.ai/oauth2/device/code"
	xaiScopes        = "openid profile email offline_access grok-cli:access api:access"
)

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

	resp, err := http.PostForm(xaiDeviceCodeURL, form)
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

		resp, err := http.PostForm(xaiTokenURL, form)
		if err != nil {
			continue
		}

		if resp.StatusCode == 200 {
			entry, parseErr := parseXAITokenResponse(resp.Body)
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

// RefreshXAIToken refreshes an expired xAI access token.
func RefreshXAIToken(refreshToken string) (*AuthEntry, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	form.Set("client_id", xaiClientID)

	resp, err := http.PostForm(xaiTokenURL, form)
	if err != nil {
		return nil, fmt.Errorf("refresh: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("refresh HTTP %d: %s", resp.StatusCode, string(raw))
	}

	return parseXAITokenResponse(resp.Body)
}

// parseXAITokenResponse parses the token response.
func parseXAITokenResponse(body io.Reader) (*AuthEntry, error) {
	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.NewDecoder(body).Decode(&tokenResp); err != nil {
		return nil, fmt.Errorf("decode token response: %w", err)
	}
	return &AuthEntry{
		Type:    "oauth",
		Access:  tokenResp.AccessToken,
		Refresh: tokenResp.RefreshToken,
		Expires: nowMillis() + int64(tokenResp.ExpiresIn)*1000 - 5*60*1000,
	}, nil
}
