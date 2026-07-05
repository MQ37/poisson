package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// OpenAI Codex (ChatGPT subscription) OAuth constants. The client ID is the
// official Codex CLI public client; the authorize flow uses PKCE (S256) and a
// fixed loopback redirect on port 1455. Reverse-engineered from the pi.dev
// Codex flow (packages/ai/src/utils/oauth/openai-codex.ts).
const (
	openaiClientID     = "app_EMoamEEZ73f0CkXaXp7hrann"
	openaiAuthorizeURL = "https://auth.openai.com/oauth/authorize"
	openaiTokenURL     = "https://auth.openai.com/oauth/token"
	openaiScopes       = "openid profile email offline_access"
	openaiRedirectPort = 1455
	openaiRedirectURI  = "http://localhost:1455/auth/callback"
	// openaiOriginator identifies the client to the ChatGPT backend. The Codex
	// backend accepts arbitrary originators (pi.dev uses "pi").
	openaiOriginator = "poisson"
)

// createState returns a random hex state token for CSRF protection.
func createState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// LoginOpenAI performs the OpenAI Codex OAuth flow: PKCE, a loopback callback
// server on the fixed port 1455, browser redirect, then code→token exchange.
// Falls back to manual code paste if the port is busy.
func LoginOpenAI() (*AuthEntry, error) {
	verifier, challenge, err := generatePKCE()
	if err != nil {
		return nil, fmt.Errorf("pkce: %w", err)
	}
	state, err := createState()
	if err != nil {
		return nil, fmt.Errorf("state: %w", err)
	}

	usedServer := false
	var codeCh chan string
	var errCh chan error

	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", openaiRedirectPort))
	if err == nil {
		usedServer = true
		codeCh = make(chan string, 1)
		errCh = make(chan error, 1)

		mux := http.NewServeMux()
		mux.HandleFunc("/auth/callback", func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			if e := q.Get("error"); e != "" {
				desc := q.Get("error_description")
				if desc == "" {
					desc = e
				}
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprintf(w, "<html><body><h1>Authorization Failed</h1><p>%s</p></body></html>", desc)
				errCh <- fmt.Errorf("oauth error: %s", desc)
				return
			}
			if q.Get("state") != state {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprintf(w, "<html><body><h1>State mismatch</h1></body></html>")
				errCh <- fmt.Errorf("state mismatch")
				return
			}
			code := q.Get("code")
			if code == "" {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprintf(w, "<html><body><h1>Missing code</h1></body></html>")
				errCh <- fmt.Errorf("missing code parameter")
				return
			}
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusOK)
			fmt.Fprintf(w, "<html><body><h1>Authorization Successful</h1><p>You can close this window and return to Poisson.</p></body></html>")
			codeCh <- code
		})

		go (&http.Server{Handler: mux}).Serve(listener)
		defer listener.Close()
	}

	params := url.Values{}
	params.Set("response_type", "code")
	params.Set("client_id", openaiClientID)
	params.Set("redirect_uri", openaiRedirectURI)
	params.Set("scope", openaiScopes)
	params.Set("code_challenge", challenge)
	params.Set("code_challenge_method", "S256")
	params.Set("state", state)
	params.Set("id_token_add_organizations", "true")
	params.Set("codex_cli_simplified_flow", "true")
	params.Set("originator", openaiOriginator)
	authURL := openaiAuthorizeURL + "?" + params.Encode()

	if usedServer {
		fmt.Printf("Open this URL in your browser:\n%s\n", authURL)
		openBrowser(authURL)
		select {
		case code := <-codeCh:
			return exchangeOpenAICode(code, verifier, openaiRedirectURI)
		case err := <-errCh:
			return nil, err
		case <-time.After(5 * time.Minute):
			return nil, fmt.Errorf("oauth timeout")
		}
	}

	fmt.Printf("Port %d is busy. Open this URL manually:\n%s\n", openaiRedirectPort, authURL)
	fmt.Printf("After login, paste the redirect URL or just the code here:\n")
	var input string
	fmt.Scanln(&input)
	code := parseRedirectInput(input)
	if code == "" {
		return nil, fmt.Errorf("no authorization code provided")
	}
	return exchangeOpenAICode(code, verifier, openaiRedirectURI)
}

// exchangeOpenAICode exchanges the authorization code for tokens.
func exchangeOpenAICode(code, verifier, redirectURI string) (*AuthEntry, error) {
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", openaiClientID)
	form.Set("code", code)
	form.Set("code_verifier", verifier)
	form.Set("redirect_uri", redirectURI)
	return postOpenAITokenForm(form, "")
}

// RefreshOpenAIToken refreshes an expired access token.
func RefreshOpenAIToken(refreshToken string) (*AuthEntry, error) {
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", openaiClientID)
	form.Set("refresh_token", refreshToken)
	return postOpenAITokenForm(form, refreshToken)
}

// postOpenAITokenForm posts a form-encoded token request and parses the JSON
// response. keepRefresh is returned when the response omits a new refresh token.
func postOpenAITokenForm(form url.Values, keepRefresh string) (*AuthEntry, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", openaiTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("openai token request failed (status %d): %s", resp.StatusCode, string(raw))
	}

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, fmt.Errorf("decode token response: %w", err)
	}
	if tokenResp.AccessToken == "" {
		return nil, fmt.Errorf("openai token response missing access_token")
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
