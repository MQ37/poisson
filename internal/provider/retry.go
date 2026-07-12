package provider

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"time"
)

// This file implements network-failure resilience for Stream(): retry with
// exponential backoff on connection failures and transient server errors,
// indefinitely, while staying observable (RetryTrace) and cancel-safe.
//
// Design note (revised after a council review of an earlier, more invasive
// draft): Stream() stays fully synchronous — DoWithRetry is called BEFORE
// any channel or SSE-pump goroutine exists, exactly like the existing
// one-shot 401-refresh-retry already does. This means: no new StreamEvent
// variant (retry status isn't "what the model said" — it doesn't belong on
// that channel), no channel-ownership/double-close risk, and the existing
// 401 retry-once logic keeps working completely unchanged, nested inside
// this loop rather than around it.

// retryBackoffBase/Cap and retryAttemptTimeout are package vars so tests can
// shrink them — the same pattern as agent.emptyResponseBackoff.
var (
	retryBackoffBase    = 1 * time.Second
	retryBackoffCap     = 30 * time.Second
	retryAttemptTimeout = 30 * time.Second
)

// retrySleep waits for d, returning false immediately (without waiting) if
// ctx is done first. A var so tests can replace it outright if shrinking
// the backoff schedule isn't enough.
var retrySleep = func(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// backoffDelay returns the delay before the n-th retry (n=1 is the delay
// before the 2nd overall attempt): base, doubling, capped, with +/-20%
// jitter so multiple retrying clients don't synchronize on the same
// schedule (a lightweight full-jitter variant).
func backoffDelay(n int) time.Duration {
	d := retryBackoffBase
	for i := 1; i < n; i++ {
		d *= 2
		if d >= retryBackoffCap {
			d = retryBackoffCap
			break
		}
	}
	if d > retryBackoffCap {
		d = retryBackoffCap
	}
	jitter := (rand.Float64()*0.4 - 0.2) * float64(d) // +/-20%
	d += time.Duration(jitter)
	if d < 0 {
		d = 0
	}
	return d
}

// DefaultRetryableStatus is the shared retryable-status predicate used by
// xAI, OpenAI, and Ollama: request timeout, rate limit, and generic 5xx.
func DefaultRetryableStatus(code int) bool {
	switch code {
	case 408, 429, 500, 502, 503, 504:
		return true
	}
	return false
}

// AnthropicRetryableStatus is DefaultRetryableStatus plus 529
// ("overloaded_error" — Anthropic's temporary-capacity-overload signal,
// distinct from a generic 5xx; see
// https://platform.claude.com/docs/en/api/errors).
func AnthropicRetryableStatus(code int) bool {
	return code == 529 || DefaultRetryableStatus(code)
}

// RetryTrace lets a caller observe backoff retries happening inside
// DoWithRetry without putting transport state on the StreamEvent channel
// (which carries model output, not transport plumbing) or on Request
// (which is pure wire data, cloned/marshaled as-is). Modeled on
// net/http/httptrace.ClientTrace: attach it to a context, DoWithRetry finds
// it there.
type RetryTrace struct {
	// OnRetry is called once per backoff sleep, right before it starts.
	// attempt is 1-indexed (this is the Nth retry); delay is how long the
	// upcoming sleep will be; reason is a short human-readable description
	// of what failed this attempt (an error string or an HTTP status line).
	OnRetry func(attempt int, delay time.Duration, reason string)
	// OnRecovered is called exactly once: the first successful attempt
	// after at least one retry. Never called if the first attempt succeeds.
	OnRecovered func()
	// MaxElapsed caps how long DoWithRetry keeps retrying ONE call before
	// giving up and returning an error, even though ctx itself isn't done.
	// Zero means unlimited: retry forever until ctx says otherwise. That's
	// the right policy for the interactive main session, where a human is
	// watching and Ctrl+C is their own give-up switch. It's the wrong policy
	// for a subagent: a background child process has no per-instance human
	// supervising just this one call, so it needs its own bounded budget
	// instead of inheriting the parent session's infinite patience — see
	// cmd/px/main.go's child mode, which sets this.
	MaxElapsed time.Duration
}

type retryTraceKey struct{}

// WithRetryTrace attaches t to ctx so any DoWithRetry call made with the
// returned context (or one derived from it) reports through it.
func WithRetryTrace(ctx context.Context, t *RetryTrace) context.Context {
	return context.WithValue(ctx, retryTraceKey{}, t)
}

// RetryTraceFromContext returns the RetryTrace attached to ctx via
// WithRetryTrace, or nil if none is attached. Exported for symmetry with
// WithRetryTrace and for callers (including tests) that need to read it back.
func RetryTraceFromContext(ctx context.Context) *RetryTrace {
	t, _ := ctx.Value(retryTraceKey{}).(*RetryTrace)
	return t
}

// attemptWithTimeout calls attempt once, cancelling its context if attempt
// itself doesn't return within timeout — bounding only "is this attempt's
// Do() ever going to come back", never the response body's subsequent
// lifetime. A plain context.WithTimeout can't express that distinction: its
// deadline is a fixed wall-clock point that fires regardless of whether the
// caller is still legitimately mid-read, silently aborting a slow-but-
// healthy SSE stream once the clock runs out. Here the timer is disarmed
// the moment attempt returns; on success, the response's Body is wrapped so
// releasing the now-permanently-live sub-context happens exactly once,
// whenever the caller finishes reading/closing it — not before.
func attemptWithTimeout(ctx context.Context, timeout time.Duration, attempt func(ctx context.Context) (*http.Response, error)) (*http.Response, error) {
	attemptCtx, cancel := context.WithCancel(ctx)
	timer := time.AfterFunc(timeout, cancel)
	resp, err := attempt(attemptCtx)

	if !timer.Stop() {
		// The timer already fired (or is firing concurrently): attemptCtx is
		// cancelled or about to be, so whatever attempt returned isn't safe to
		// keep — its body, if any, could be mid-read against a context that's
		// being torn down right now.
		if resp != nil {
			resp.Body.Close()
		}
		if err == nil {
			err = context.DeadlineExceeded
		}
		return nil, err
	}

	if err != nil {
		cancel() // attempt failed before the timeout — nothing left to keep attemptCtx alive for
		return nil, err
	}
	resp.Body = &cancelOnCloseBody{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

// cancelOnCloseBody releases a context.CancelFunc when the wrapped body is
// closed, instead of the moment the HTTP call that produced it returned —
// see attemptWithTimeout.
type cancelOnCloseBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b *cancelOnCloseBody) Close() error {
	err := b.ReadCloser.Close()
	b.cancel()
	return err
}

// DoWithRetry calls attempt repeatedly until it succeeds with a status
// retryableStatus doesn't flag, or fails in a way ctx being done explains,
// or ctx itself is done. Retries indefinitely otherwise — only the backoff
// delay between attempts is capped, not the attempt count — with
// exponential backoff between attempts.
//
// attempt is called with a context derived from ctx that's cancelled if
// attempt itself doesn't return within retryAttemptTimeout, so a connection
// that hangs rather than erroring outright (e.g. after a laptop sleep/wake,
// where the OS doesn't notice the peer is gone for a long time) can't block
// a single attempt forever and starve the retry loop. Critically, that
// bound applies ONLY to attempt's own call returning — once it returns a
// response, the timeout is disarmed and never fires later, so a slow-but-
// healthy response body (an SSE stream can legitimately run far longer than
// retryAttemptTimeout) is never aborted mid-read. The response's own
// sub-context is released when its Body is eventually Read to EOF or
// Closed — attempt should build its *http.Request fresh from the context
// it's given on every call rather than reusing one across calls, since an
// already-consumed request body must never be resent truncated or empty on
// a retry.
//
// A transport-level error (attempt returns err != nil) is always retried
// UNLESS ctx itself is what's done — checked explicitly so a per-attempt
// timeout (retryable: the connection just hung) is never confused with the
// caller's own context being cancelled or expiring (not retryable: stop
// immediately and silently — return ctx.Err() with no notification, since
// e.g. a user's Ctrl+C deciding to stop is not a failure to report). A
// response with a non-retryable status is returned exactly as attempt
// produced it, unchanged from what a single, non-retrying call would give.
//
// retryableStatus may be nil (equivalent to never retrying on status, only
// on transport errors).
func DoWithRetry(ctx context.Context, retryableStatus func(code int) bool, attempt func(ctx context.Context) (*http.Response, error)) (*http.Response, error) {
	trace := RetryTraceFromContext(ctx)
	n := 0
	var retryStart time.Time
	gaveUp := func(reason string) (*http.Response, error) {
		return nil, fmt.Errorf("gave up retrying after %s (last failure: %s)", time.Since(retryStart).Round(time.Second), reason)
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		resp, err := attemptWithTimeout(ctx, retryAttemptTimeout, attempt)

		if err != nil {
			if ctx.Err() != nil {
				// The caller's own context is what actually ended this —
				// not just this attempt's bounded sub-context timing out —
				// so stop immediately and silently; this isn't a failure of
				// DoWithRetry's to report.
				return nil, ctx.Err()
			}
			n++
			if n == 1 {
				retryStart = time.Now()
			}
			if trace != nil && trace.MaxElapsed > 0 && time.Since(retryStart) > trace.MaxElapsed {
				return gaveUp(err.Error())
			}
			delay := backoffDelay(n)
			if trace != nil && trace.OnRetry != nil {
				trace.OnRetry(n, delay, err.Error())
			}
			if !retrySleep(ctx, delay) {
				return nil, ctx.Err()
			}
			continue
		}

		if retryableStatus != nil && retryableStatus(resp.StatusCode) {
			n++
			if n == 1 {
				retryStart = time.Now()
			}
			reason := resp.Status
			resp.Body.Close()
			if trace != nil && trace.MaxElapsed > 0 && time.Since(retryStart) > trace.MaxElapsed {
				return gaveUp(reason)
			}
			delay := backoffDelay(n)
			if trace != nil && trace.OnRetry != nil {
				trace.OnRetry(n, delay, reason)
			}
			if !retrySleep(ctx, delay) {
				return nil, ctx.Err()
			}
			continue
		}

		if n > 0 && trace != nil && trace.OnRecovered != nil {
			trace.OnRecovered()
		}
		return resp, nil
	}
}
