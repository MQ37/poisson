package provider

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/mq37/poisson/internal/auth"
)

// codexWebBaseURL is chatgpt.com's web/session backend — hosts the "wham"
// usage + rate-limit-reset-credit endpoints Codex's own CLI polls (confirmed
// via a MITM capture of a real codex session), a separate concern from
// codexResponsesURL even though it's the same host.
const codexWebBaseURL = "https://chatgpt.com"

// CodexUsage is the parsed response of GET /backend-api/wham/usage — a
// single rolling weekly window (unlike Anthropic's separate 5h+7d windows),
// plus a count of free "reset this window early" credits the account has
// available (see ResetUsage).
type CodexUsage struct {
	UsedPercent           float64
	ResetAfterSeconds     int
	ResetAt               time.Time
	ResetCreditsAvailable int

	FetchedAt time.Time
}

// CodexResetResult is the outcome of successfully consuming a reset credit
// via ResetUsage.
type CodexResetResult struct {
	WindowsReset     int
	CreditsRemaining int
}

// UsageLimits returns the account's current Codex usage, refreshing from
// chatgpt.com at most every usageTTL (shared with anthropic_usage.go — same
// package, same reasoning: a background status-bar number isn't worth a
// request on every render). On a transient fetch error, the last
// successfully fetched snapshot is returned instead of failing the caller.
func (p *OpenAIProvider) UsageLimits(ctx context.Context) (*CodexUsage, error) {
	if cached := p.CachedUsageLimits(); cached != nil && time.Since(cached.FetchedAt) < usageTTL {
		return cached, nil
	}

	fresh, err := p.fetchUsage(ctx)
	if err != nil {
		if cached := p.CachedUsageLimits(); cached != nil {
			return cached, nil
		}
		return nil, err
	}

	p.usageMu.Lock()
	p.usageCache = fresh
	p.usageMu.Unlock()
	return fresh, nil
}

// CachedUsageLimits returns the last successfully fetched usage snapshot
// without a network call or TTL check — nil if none has been fetched yet.
// Safe to call from a render/status-sync path.
func (p *OpenAIProvider) CachedUsageLimits() *CodexUsage {
	p.usageMu.Lock()
	defer p.usageMu.Unlock()
	return p.usageCache
}

// ForceUsageRefresh drops the cached snapshot and fetches fresh data right
// now, ignoring usageTTL — see AnthropicProvider.ForceUsageRefresh for the
// reasoning (same idiom ResetUsage below already uses for its own cache
// invalidation after spending a reset credit).
func (p *OpenAIProvider) ForceUsageRefresh(ctx context.Context) (*CodexUsage, error) {
	p.usageMu.Lock()
	p.usageCache = nil
	p.usageMu.Unlock()
	return p.UsageLimits(ctx)
}

// oauthCreds resolves the current OpenAI OAuth entry (refreshing if near
// expiry) and its chatgpt-account-id claim — the same two pieces
// streamWithRetry resolves for the main Codex chat endpoint, duplicated here
// since usage/reset is a separate concern even though it hits the same host.
func (p *OpenAIProvider) oauthCreds() (accessToken, accountID string, err error) {
	p.authMu.Lock()
	entry, ok := p.auth["openai"]
	if ok && entry.Type == "oauth" && auth.IsExpired(entry, 5*60*1000) {
		if refreshed, rerr := auth.RefreshOpenAIToken(entry.Refresh); rerr == nil {
			p.auth["openai"] = *refreshed
			_ = auth.Save(p.auth)
			entry = *refreshed
		}
	}
	p.authMu.Unlock()
	if !ok || entry.Type != "oauth" {
		return "", "", fmt.Errorf("no OpenAI credentials — run: px login openai")
	}
	accountID, err = extractAccountID(entry.Access)
	if err != nil {
		return "", "", err
	}
	return entry.Access, accountID, nil
}

func (p *OpenAIProvider) fetchUsage(ctx context.Context) (*CodexUsage, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	do := func() (*http.Response, error) {
		access, accountID, err := p.oauthCreds()
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(fetchCtx, "GET", p.webBaseURL+"/backend-api/wham/usage", nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Authorization", "Bearer "+access)
		req.Header.Set("chatgpt-account-id", accountID)
		return p.client.Do(req)
	}

	resp, err := do()
	if err != nil {
		return nil, fmt.Errorf("fetch usage: %w", err)
	}
	if resp.StatusCode == 401 {
		resp.Body.Close()
		p.authMu.Lock()
		refreshed, rerr := auth.RefreshOpenAIToken(p.auth["openai"].Refresh)
		if rerr == nil {
			p.auth["openai"] = *refreshed
			_ = auth.Save(p.auth)
		}
		p.authMu.Unlock()
		if rerr != nil {
			return nil, fmt.Errorf("token expired, refresh failed: %w", rerr)
		}
		resp, err = do()
		if err != nil {
			return nil, fmt.Errorf("fetch usage: %w", err)
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := readCappedBody(resp)
		return nil, fmt.Errorf("usage API error (status %d): %s", resp.StatusCode, string(body))
	}

	var raw struct {
		RateLimit struct {
			PrimaryWindow struct {
				UsedPercent       float64 `json:"used_percent"`
				ResetAfterSeconds int     `json:"reset_after_seconds"`
				ResetAt           int64   `json:"reset_at"`
			} `json:"primary_window"`
		} `json:"rate_limit"`
		RateLimitResetCredits struct {
			AvailableCount int `json:"available_count"`
		} `json:"rate_limit_reset_credits"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode usage response: %w", err)
	}

	return &CodexUsage{
		UsedPercent:           raw.RateLimit.PrimaryWindow.UsedPercent,
		ResetAfterSeconds:     raw.RateLimit.PrimaryWindow.ResetAfterSeconds,
		ResetAt:               time.Unix(raw.RateLimit.PrimaryWindow.ResetAt, 0),
		ResetCreditsAvailable: raw.RateLimitResetCredits.AvailableCount,
		FetchedAt:             time.Now(),
	}, nil
}

// resetCredit is one entry from GET /backend-api/wham/rate-limit-reset-credits.
type resetCredit struct {
	ID     string `json:"id"`
	Status string `json:"status"` // "available" | "redeemed"
}

// fetchResetCredits lists the account's reset credits (available + already
// redeemed) and the count currently available.
func (p *OpenAIProvider) fetchResetCredits(ctx context.Context, access, accountID string) ([]resetCredit, int, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", p.webBaseURL+"/backend-api/wham/rate-limit-reset-credits", nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("chatgpt-account-id", accountID)
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("list reset credits: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := readCappedBody(resp)
		return nil, 0, fmt.Errorf("list reset credits failed (status %d): %s", resp.StatusCode, string(body))
	}
	var raw struct {
		Credits        []resetCredit `json:"credits"`
		AvailableCount int           `json:"available_count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, 0, fmt.Errorf("decode reset credits: %w", err)
	}
	return raw.Credits, raw.AvailableCount, nil
}

// ResetUsage spends one of the account's free "rate limit reset" credits
// (Codex grants these periodically) to reset the weekly usage window early.
// Fails clearly when none are available rather than guessing. Confirmed
// against a real MITM-captured redemption: POST .../consume with a fresh
// redeem_request_id + the chosen credit's id, response carries windows_reset.
func (p *OpenAIProvider) ResetUsage(ctx context.Context) (*CodexResetResult, error) {
	access, accountID, err := p.oauthCreds()
	if err != nil {
		return nil, err
	}
	fetchCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	credits, _, err := p.fetchResetCredits(fetchCtx, access, accountID)
	if err != nil {
		return nil, err
	}
	var creditID string
	for _, c := range credits {
		if c.Status == "available" {
			creditID = c.ID
			break
		}
	}
	if creditID == "" {
		return nil, fmt.Errorf("no reset credits available")
	}

	body, _ := json.Marshal(map[string]string{
		"redeem_request_id": newUUIDv4(),
		"credit_id":         creditID,
	})
	req, err := http.NewRequestWithContext(fetchCtx, "POST", p.webBaseURL+"/backend-api/wham/rate-limit-reset-credits/consume", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("chatgpt-account-id", accountID)
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("consume reset credit: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		respBody, _ := readCappedBody(resp)
		return nil, fmt.Errorf("consume reset credit failed (status %d): %s", resp.StatusCode, string(respBody))
	}
	var raw struct {
		WindowsReset int `json:"windows_reset"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode reset response: %w", err)
	}

	// The reset just changed usage/credits — drop the cache so the next
	// UsageLimits call refetches instead of serving a stale pre-reset value
	// for up to usageTTL.
	p.usageMu.Lock()
	p.usageCache = nil
	p.usageMu.Unlock()

	_, remaining, _ := p.fetchResetCredits(fetchCtx, access, accountID) // best-effort, display only
	return &CodexResetResult{WindowsReset: raw.WindowsReset, CreditsRemaining: remaining}, nil
}

// newUUIDv4 generates a v4 UUID using crypto/rand (no external library) —
// mirrors internal/store/message.go's newUUID, duplicated here rather than
// imported since internal/provider has no business depending on the storage
// layer for a one-off request id.
func newUUIDv4() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	s := hex.EncodeToString(b[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", s[0:8], s[8:12], s[12:16], s[16:20], s[20:32])
}
