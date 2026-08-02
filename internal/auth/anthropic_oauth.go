package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// Anthropic OAuth constants (from SPEC §4.2).
const (
	anthropicClientID     = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	anthropicAuthorizeURL = "https://claude.ai/oauth/authorize"
	anthropicScopes       = "org:create_api_key user:profile user:inference user:sessions:claude_code user:mcp_servers user:file_upload"
	anthropicRedirectPort = 53692
	anthropicRedirectURI  = "http://localhost:53692/callback"
)

// anthropicTokenURL is a var (not a const) so tests can point it at a local
// httptest server instead of the real Anthropic endpoint — same idiom as
// internal/tools/web_ask_grok.go's grokResponsesURL.
var anthropicTokenURL = "https://platform.claude.com/v1/oauth/token"

// generatePKCE creates a PKCE verifier and S256 challenge.
func generatePKCE() (verifier, challenge string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(b)
	h := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(h[:])
	return verifier, challenge, nil
}

// openBrowser tries to open the given URL in the default browser.
func openBrowser(url string) {
	switch runtime.GOOS {
	case "linux":
		exec.Command("xdg-open", url).Start()
	case "darwin":
		exec.Command("open", url).Start()
	case "windows":
		exec.Command("start", url).Start()
	}
}

// LoginAnthropic performs the full OAuth flow: PKCE, callback server,
// browser redirect, token exchange. Uses fixed port 53692 (registered
// with Anthropic). Falls back to manual paste if the port is busy.
func LoginAnthropic() (*AuthEntry, error) {
	verifier, challenge, err := generatePKCE()
	if err != nil {
		return nil, fmt.Errorf("pkce: %w", err)
	}

	// Try to start callback server on the fixed registered port.
	usedServer := false
	var codeCh chan string
	var errCh chan error

	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", anthropicRedirectPort))
	if err == nil {
		usedServer = true
		codeCh = make(chan string, 1)
		errCh = make(chan error, 1)

		mux := http.NewServeMux()
		mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
			q := r.URL.Query()
			if e := q.Get("error"); e != "" {
				desc := q.Get("error_description")
				if desc == "" {
					desc = e
				}
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprintf(w, "<html><body><h1>Authorization Failed</h1><p>%s</p></body></html>", html.EscapeString(desc))
				errCh <- fmt.Errorf("oauth error: %s", desc)
				return
			}
			code := q.Get("code")
			state := q.Get("state")
			if code == "" {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprintf(w, "<html><body><h1>Missing code</h1></body></html>")
				errCh <- fmt.Errorf("missing code parameter")
				return
			}
			if state != verifier {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprintf(w, "<html><body><h1>State mismatch</h1></body></html>")
				errCh <- fmt.Errorf("state mismatch")
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

	// Build authorize URL with the fixed redirect URI.
	params := url.Values{}
	params.Set("code", "true")
	params.Set("client_id", anthropicClientID)
	params.Set("response_type", "code")
	params.Set("redirect_uri", anthropicRedirectURI)
	params.Set("scope", anthropicScopes)
	params.Set("code_challenge", challenge)
	params.Set("code_challenge_method", "S256")
	params.Set("state", verifier)
	authURL := anthropicAuthorizeURL + "?" + params.Encode()

	if usedServer {
		fmt.Printf("Open this URL in your browser:\n%s\n", authURL)
		openBrowser(authURL)
	} else {
		fmt.Printf("Port %d is busy. Open this URL manually:\n%s\n", anthropicRedirectPort, authURL)
		fmt.Printf("After login, paste the redirect URL or just the code here:\n")
	}

	if usedServer {
		select {
		case code := <-codeCh:
			return exchangeAnthropicCode(code, verifier, anthropicRedirectURI)
		case err := <-errCh:
			return nil, err
		case <-time.After(5 * time.Minute):
			return nil, fmt.Errorf("oauth timeout")
		}
	}

	// Manual paste fallback.
	var input string
	fmt.Scanln(&input)
	code := parseRedirectInput(input)
	if code == "" {
		return nil, fmt.Errorf("no authorization code provided")
	}
	return exchangeAnthropicCode(code, verifier, anthropicRedirectURI)
}

// parseRedirectInput extracts the authorization code from a pasted URL or raw code.
func parseRedirectInput(input string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return ""
	}
	if u, err := url.Parse(input); err == nil && u.Query().Get("code") != "" {
		return u.Query().Get("code")
	}
	return input
}

// exchangeAnthropicCode exchanges the authorization code for tokens.
func exchangeAnthropicCode(code, verifier, redirectURI string) (*AuthEntry, error) {
	body := map[string]string{
		"grant_type":    "authorization_code",
		"client_id":     anthropicClientID,
		"code":          code,
		"state":         verifier,
		"redirect_uri":  redirectURI,
		"code_verifier": verifier,
	}
	return postTokenRequest(anthropicTokenURL, body, "")
}

// RefreshAnthropicToken refreshes an expired access token.
func RefreshAnthropicToken(refreshToken string) (*AuthEntry, error) {
	body := map[string]string{
		"grant_type":    "refresh_token",
		"client_id":     anthropicClientID,
		"refresh_token": refreshToken,
	}
	return postTokenRequest(anthropicTokenURL, body, refreshToken)
}

// postTokenRequest sends a token exchange/refresh request and parses the response.
func postTokenRequest(tokenURL string, body map[string]string, keepRefresh string) (*AuthEntry, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", tokenURL, bytesReader(jsonBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	// Real Claude Code's token exchange/refresh runs through its bundled
	// axios client, not the SDK's own fetch — a bare Go User-Agent on this
	// endpoint is a fingerprintable tell even though /v1/messages is spoofed
	// (see opencode-anthropic-auth's index.ts, same header/value).
	req.Header.Set("User-Agent", "axios/1.13.6")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("token exchange failed (status %d): %s", resp.StatusCode, string(raw))
	}

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
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

// bytesReader wraps a []byte as an io.Reader.
func bytesReader(b []byte) io.Reader {
	return &bytesReaderImpl{b: b}
}

type bytesReaderImpl struct {
	b []byte
	i int
}

func (r *bytesReaderImpl) Read(p []byte) (int, error) {
	if r.i >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.i:])
	r.i += n
	return n, nil
}
