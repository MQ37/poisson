package provider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// shrinkRetryTiming makes the backoff schedule test-fast and restores it on
// cleanup, so tests never depend on real wall-clock delays.
func shrinkRetryTiming(t *testing.T) {
	t.Helper()
	oldBase, oldCap, oldAttempt := retryBackoffBase, retryBackoffCap, retryAttemptTimeout
	retryBackoffBase = time.Millisecond
	retryBackoffCap = 5 * time.Millisecond
	retryAttemptTimeout = 200 * time.Millisecond
	t.Cleanup(func() {
		retryBackoffBase, retryBackoffCap, retryAttemptTimeout = oldBase, oldCap, oldAttempt
	})
}

func statusResp(code int) *http.Response {
	rec := httptest.NewRecorder()
	rec.Code = code
	resp := rec.Result()
	resp.StatusCode = code
	return resp
}

func TestDoWithRetrySucceedsFirstTryNoRetry(t *testing.T) {
	shrinkRetryTiming(t)
	calls := 0
	resp, err := DoWithRetry(context.Background(), DefaultRetryableStatus, func(ctx context.Context) (*http.Response, error) {
		calls++
		return statusResp(200), nil
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (no retry on first-try success)", calls)
	}
}

func TestDoWithRetrySucceedsAfterRetryableStatusFailures(t *testing.T) {
	shrinkRetryTiming(t)
	calls := 0
	var retries []int
	var delays []time.Duration
	recovered := false
	trace := &RetryTrace{
		OnRetry: func(attempt int, delay time.Duration, reason string) {
			retries = append(retries, attempt)
			delays = append(delays, delay)
		},
		OnRecovered: func() { recovered = true },
	}
	ctx := WithRetryTrace(context.Background(), trace)

	resp, err := DoWithRetry(ctx, DefaultRetryableStatus, func(ctx context.Context) (*http.Response, error) {
		calls++
		if calls < 3 {
			return statusResp(503), nil
		}
		return statusResp(200), nil
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
	if len(retries) != 2 || retries[0] != 1 || retries[1] != 2 {
		t.Errorf("retries = %v, want [1 2]", retries)
	}
	if len(delays) != 2 {
		t.Fatalf("delays = %v", delays)
	}
	// Second delay should be >= first (exponential, modulo jitter overlap at
	// tiny test scale) — check both are non-negative and within the capped
	// range rather than asserting exact monotonicity under jitter.
	for _, d := range delays {
		if d < 0 || d > retryBackoffCap {
			t.Errorf("delay %v out of [0, %v]", d, retryBackoffCap)
		}
	}
	if !recovered {
		t.Error("OnRecovered was not called after a successful retry")
	}
}

func TestDoWithRetrySucceedsAfterNetworkErrors(t *testing.T) {
	shrinkRetryTiming(t)
	calls := 0
	var reasons []string
	trace := &RetryTrace{OnRetry: func(attempt int, delay time.Duration, reason string) {
		reasons = append(reasons, reason)
	}}
	ctx := WithRetryTrace(context.Background(), trace)

	resp, err := DoWithRetry(ctx, DefaultRetryableStatus, func(ctx context.Context) (*http.Response, error) {
		calls++
		if calls < 2 {
			return nil, errors.New("dial tcp: connection refused")
		}
		return statusResp(200), nil
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
	if len(reasons) != 1 || reasons[0] != "dial tcp: connection refused" {
		t.Errorf("reasons = %v", reasons)
	}
}

// TestRetryAfterDelayParsesSecondsAndDate verifies retryAfterDelay reads
// both Retry-After spellings (delay-seconds, HTTP-date), caps at
// retryAfterMaxDelay, and returns 0 for anything empty/unparseable/elapsed
// so the caller falls back to its own exponential backoff.
func TestRetryAfterDelayParsesSecondsAndDate(t *testing.T) {
	if got := retryAfterDelay(""); got != 0 {
		t.Errorf("empty = %v, want 0", got)
	}
	if got := retryAfterDelay("not-a-number"); got != 0 {
		t.Errorf("garbage = %v, want 0", got)
	}
	if got := retryAfterDelay("5"); got != 5*time.Second {
		t.Errorf("\"5\" = %v, want 5s", got)
	}
	if got := retryAfterDelay("-1"); got != 0 {
		t.Errorf("negative seconds = %v, want 0", got)
	}
	if got := retryAfterDelay("999999"); got != retryAfterMaxDelay {
		t.Errorf("huge seconds = %v, want capped at %v", got, retryAfterMaxDelay)
	}
	future := time.Now().Add(10 * time.Second).UTC().Format(http.TimeFormat)
	if got := retryAfterDelay(future); got <= 0 || got > 10*time.Second {
		t.Errorf("HTTP-date 10s out = %v, want (0, 10s]", got)
	}
	past := time.Now().Add(-10 * time.Second).UTC().Format(http.TimeFormat)
	if got := retryAfterDelay(past); got != 0 {
		t.Errorf("HTTP-date in the past = %v, want 0", got)
	}
}

// TestDoWithRetryHonorsRetryAfterHeader verifies a 429's Retry-After header
// overrides the computed exponential backoff delay — previously ignored
// entirely, so DoWithRetry always retried on its own fixed schedule
// regardless of what the server asked for.
func TestDoWithRetryHonorsRetryAfterHeader(t *testing.T) {
	shrinkRetryTiming(t)
	oldSleep := retrySleep
	retrySleep = func(ctx context.Context, d time.Duration) bool { return true } // don't actually wait 1s
	defer func() { retrySleep = oldSleep }()
	var delays []time.Duration
	trace := &RetryTrace{
		OnRetry: func(attempt int, delay time.Duration, reason string) {
			delays = append(delays, delay)
		},
	}
	ctx := WithRetryTrace(context.Background(), trace)

	calls := 0
	resp, err := DoWithRetry(ctx, DefaultRetryableStatus, func(ctx context.Context) (*http.Response, error) {
		calls++
		if calls == 1 {
			r := statusResp(429)
			r.Header = http.Header{"Retry-After": []string{"1"}}
			return r, nil
		}
		return statusResp(200), nil
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
	if len(delays) != 1 || delays[0] != 1*time.Second {
		t.Errorf("delays = %v, want [1s] (from Retry-After, not the shrunk exponential backoff)", delays)
	}
}

func TestDoWithRetryNonRetryableStatusReturnsImmediately(t *testing.T) {
	shrinkRetryTiming(t)
	calls := 0
	sleepCalled := false
	oldSleep := retrySleep
	retrySleep = func(ctx context.Context, d time.Duration) bool {
		sleepCalled = true
		return oldSleep(ctx, d)
	}
	defer func() { retrySleep = oldSleep }()

	resp, err := DoWithRetry(context.Background(), DefaultRetryableStatus, func(ctx context.Context) (*http.Response, error) {
		calls++
		return statusResp(400), nil
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if resp.StatusCode != 400 {
		t.Errorf("status = %d, want 400 (returned as-is)", resp.StatusCode)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (no retry on non-retryable status)", calls)
	}
	if sleepCalled {
		t.Error("retrySleep was called for a non-retryable status — should return immediately")
	}
}

func TestDoWithRetryAnthropicRetries529ButDefaultDoesNot(t *testing.T) {
	shrinkRetryTiming(t)

	// Default classifier: 529 is NOT retryable — returned as-is.
	calls := 0
	resp, err := DoWithRetry(context.Background(), DefaultRetryableStatus, func(ctx context.Context) (*http.Response, error) {
		calls++
		return statusResp(529), nil
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if calls != 1 || resp.StatusCode != 529 {
		t.Errorf("default classifier should not retry 529: calls=%d status=%d", calls, resp.StatusCode)
	}

	// Anthropic classifier: 529 retries then succeeds.
	calls = 0
	resp, err = DoWithRetry(context.Background(), AnthropicRetryableStatus, func(ctx context.Context) (*http.Response, error) {
		calls++
		if calls == 1 {
			return statusResp(529), nil
		}
		return statusResp(200), nil
	})
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if calls != 2 || resp.StatusCode != 200 {
		t.Errorf("anthropic classifier should retry 529: calls=%d status=%d", calls, resp.StatusCode)
	}
}

func TestDoWithRetryCtxCancelledBeforeFirstAttempt(t *testing.T) {
	shrinkRetryTiming(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	calls := 0
	_, err := DoWithRetry(ctx, DefaultRetryableStatus, func(ctx context.Context) (*http.Response, error) {
		calls++
		return statusResp(200), nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if calls != 0 {
		t.Errorf("calls = %d, want 0 (ctx already done, never attempt)", calls)
	}
}

func TestDoWithRetryCtxCancelledDuringBackoffSleepStopsImmediately(t *testing.T) {
	// Use a real (small but non-trivial) backoff so we can prove
	// cancellation interrupts the sleep rather than waiting it out.
	oldBase, oldCap, oldAttempt := retryBackoffBase, retryBackoffCap, retryAttemptTimeout
	retryBackoffBase = 2 * time.Second
	retryBackoffCap = 2 * time.Second
	retryAttemptTimeout = 5 * time.Second
	defer func() { retryBackoffBase, retryBackoffCap, retryAttemptTimeout = oldBase, oldCap, oldAttempt }()

	ctx, cancel := context.WithCancel(context.Background())
	var mu sync.Mutex
	calls := 0

	done := make(chan struct{})
	var retErr error
	go func() {
		_, retErr = DoWithRetry(ctx, DefaultRetryableStatus, func(ctx context.Context) (*http.Response, error) {
			mu.Lock()
			calls++
			mu.Unlock()
			return nil, errors.New("connection refused")
		})
		close(done)
	}()

	time.Sleep(20 * time.Millisecond) // let the first attempt fail and enter backoff
	cancel()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("DoWithRetry did not return promptly after ctx cancellation during backoff sleep")
	}
	if !errors.Is(retErr, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", retErr)
	}
	mu.Lock()
	n := calls
	mu.Unlock()
	if n != 1 {
		t.Errorf("calls = %d, want exactly 1 (cancelled during backoff before a 2nd attempt)", n)
	}
}

func TestDoWithRetryPerAttemptTimeoutIsRetryableNotOuterCancellation(t *testing.T) {
	oldBase, oldCap, oldAttempt := retryBackoffBase, retryBackoffCap, retryAttemptTimeout
	retryBackoffBase = time.Millisecond
	retryBackoffCap = time.Millisecond
	retryAttemptTimeout = 20 * time.Millisecond
	defer func() { retryBackoffBase, retryBackoffCap, retryAttemptTimeout = oldBase, oldCap, oldAttempt }()

	// Outer ctx has no deadline — only the per-attempt sub-context should
	// ever expire here. The first attempt hangs past its own bounded
	// deadline (simulating a silently dead connection); the second succeeds.
	calls := 0
	resp, err := DoWithRetry(context.Background(), DefaultRetryableStatus, func(ctx context.Context) (*http.Response, error) {
		calls++
		if calls == 1 {
			<-ctx.Done() // wait out this attempt's own timeout, not the outer ctx
			return nil, ctx.Err()
		}
		return statusResp(200), nil
	})
	if err != nil {
		t.Fatalf("err = %v, want nil (per-attempt timeout must be retried, not treated as outer cancellation)", err)
	}
	if calls != 2 || resp.StatusCode != 200 {
		t.Errorf("calls=%d status=%d, want calls=2 status=200", calls, resp.StatusCode)
	}
}

func TestBackoffDelayIsMonotonicUpToCapWithinJitterBounds(t *testing.T) {
	retryBackoffBase = 1 * time.Second
	retryBackoffCap = 30 * time.Second
	for n, wantBase := range map[int]time.Duration{
		1: 1 * time.Second,
		2: 2 * time.Second,
		3: 4 * time.Second,
		4: 8 * time.Second,
		5: 16 * time.Second,
		6: 30 * time.Second, // capped
		7: 30 * time.Second, // still capped
	} {
		lo := time.Duration(float64(wantBase) * 0.8)
		hi := time.Duration(float64(wantBase) * 1.2)
		for i := 0; i < 20; i++ {
			d := backoffDelay(n)
			if d < lo-time.Millisecond || d > hi+time.Millisecond {
				t.Errorf("backoffDelay(%d) = %v, want within [%v, %v] (base %v +/-20%%)", n, d, lo, hi, wantBase)
			}
		}
	}
}

func TestDoWithRetryGivesUpAfterMaxElapsed(t *testing.T) {
	oldBase, oldCap, oldAttempt := retryBackoffBase, retryBackoffCap, retryAttemptTimeout
	retryBackoffBase = 5 * time.Millisecond
	retryBackoffCap = 5 * time.Millisecond
	retryAttemptTimeout = 100 * time.Millisecond
	defer func() { retryBackoffBase, retryBackoffCap, retryAttemptTimeout = oldBase, oldCap, oldAttempt }()

	trace := &RetryTrace{MaxElapsed: 30 * time.Millisecond}
	ctx := WithRetryTrace(context.Background(), trace)

	calls := 0
	start := time.Now()
	_, err := DoWithRetry(ctx, DefaultRetryableStatus, func(ctx context.Context) (*http.Response, error) {
		calls++
		return nil, errors.New("connection refused")
	})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected a give-up error")
	}
	if !strings.Contains(err.Error(), "gave up retrying") {
		t.Errorf("err = %v, want a \"gave up retrying\" message", err)
	}
	if calls < 2 {
		t.Errorf("calls = %d, want at least 2 (some retries happened before giving up)", calls)
	}
	// Should give up reasonably close to MaxElapsed, not run away.
	if elapsed > 500*time.Millisecond {
		t.Errorf("took %v to give up, want well under 500ms given MaxElapsed=30ms", elapsed)
	}
}

func TestDoWithRetryZeroMaxElapsedMeansUnlimited(t *testing.T) {
	oldBase, oldCap, oldAttempt := retryBackoffBase, retryBackoffCap, retryAttemptTimeout
	retryBackoffBase = time.Millisecond
	retryBackoffCap = time.Millisecond
	retryAttemptTimeout = 200 * time.Millisecond
	defer func() { retryBackoffBase, retryBackoffCap, retryAttemptTimeout = oldBase, oldCap, oldAttempt }()

	trace := &RetryTrace{} // MaxElapsed zero value
	ctx := WithRetryTrace(context.Background(), trace)

	calls := 0
	resp, err := DoWithRetry(ctx, DefaultRetryableStatus, func(ctx context.Context) (*http.Response, error) {
		calls++
		if calls < 20 {
			return nil, errors.New("connection refused")
		}
		return statusResp(200), nil
	})
	if err != nil {
		t.Fatalf("err = %v, want nil (zero MaxElapsed must never give up)", err)
	}
	if resp.StatusCode != 200 || calls != 20 {
		t.Errorf("calls=%d status=%d, want calls=20 status=200", calls, resp.StatusCode)
	}
}

// TestDoWithRetryDoesNotAbortSlowButHealthySSEBodyAfterAttemptTimeout
// reproduces the exact reported regression: a real bash-command turn whose
// response takes longer to fully stream than retryAttemptTimeout produced
// "sse read: context canceled" partway through, because the original
// implementation bound the WHOLE attempt (Do() + the subsequent body read)
// to one context.WithTimeout deadline — which fires at a fixed wall-clock
// point regardless of whether the caller is still legitimately mid-read.
// The fix (attemptWithTimeout) only bounds "does attempt() itself return in
// time"; once it does, the response's body must be readable for as long as
// the caller needs, deadline or not.
func TestDoWithRetryDoesNotAbortSlowButHealthySSEBodyAfterAttemptTimeout(t *testing.T) {
	oldAttempt := retryAttemptTimeout
	retryAttemptTimeout = 30 * time.Millisecond // tiny, so the body legitimately outlives it
	defer func() { retryAttemptTimeout = oldAttempt }()

	// A REAL net/http round trip, not a hand-built *http.Response: this is
	// what actually reproduces the regression. Go's http.Client ties Body
	// reads to the *http.Request's context for the request's whole
	// lifetime, not just until headers arrive — a hand-built response with
	// a bare io.Pipe body (no real client involved) never exercised that
	// binding at all and would pass even with the bug present.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		w.WriteHeader(200)
		flusher.Flush()                     // headers land immediately — attempt() returns right away
		time.Sleep(3 * retryAttemptTimeout) // body write spans well past the attempt timeout
		w.Write([]byte("hello from a slow but healthy stream"))
	}))
	defer srv.Close()

	client := &http.Client{}
	resp, err := DoWithRetry(context.Background(), DefaultRetryableStatus, func(ctx context.Context) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
		if err != nil {
			return nil, err
		}
		return client.Do(req)
	})
	if err != nil {
		t.Fatalf("DoWithRetry: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading a slow-but-healthy body must not fail once the attempt itself already returned: %v", err)
	}
	if string(body) != "hello from a slow but healthy stream" {
		t.Errorf("body = %q, want the full slow write", body)
	}
}

func TestDoWithRetryStillTimesOutAHungAttempt(t *testing.T) {
	oldAttempt := retryAttemptTimeout
	oldBase, oldCap := retryBackoffBase, retryBackoffCap
	retryAttemptTimeout = 20 * time.Millisecond
	retryBackoffBase = time.Millisecond
	retryBackoffCap = time.Millisecond
	defer func() {
		retryAttemptTimeout = oldAttempt
		retryBackoffBase, retryBackoffCap = oldBase, oldCap
	}()

	calls := 0
	resp, err := DoWithRetry(context.Background(), DefaultRetryableStatus, func(ctx context.Context) (*http.Response, error) {
		calls++
		if calls == 1 {
			<-ctx.Done() // attempt() itself never returns until its own timeout fires
			return nil, ctx.Err()
		}
		return statusResp(200), nil
	})
	if err != nil {
		t.Fatalf("err = %v, want nil (retried past the hung first attempt)", err)
	}
	if resp.StatusCode != 200 || calls != 2 {
		t.Errorf("calls=%d status=%d, want calls=2 status=200", calls, resp.StatusCode)
	}
}
