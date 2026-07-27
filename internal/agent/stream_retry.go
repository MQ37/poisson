package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mq37/poisson/internal/provider"
)

// Retry policy for failures a provider reports AFTER the response already
// started with HTTP 200 — the layer provider.DoWithRetry cannot see, since by
// then it has handed back a live SSE body (see its doc comment). Two callers
// drive this policy:
//
//   - runTurn, which streams incrementally to the user and re-runs the whole
//     round on retry;
//   - streamAndCollect below, for one-shot calls whose output is consumed
//     whole rather than displayed as it arrives (bash-risk classification).
//
// They differ only in how a retry is carried out, so the decision itself
// lives here once instead of being restated at each call site.

// shouldRetryMidStream reports whether a mid-stream provider error is worth
// another attempt. noContentYet must be false once any text/thinking/tool
// content from this attempt has already reached the user: retrying then would
// re-emit it from scratch and duplicate what's visible. retries is how many
// mid-stream retries this call has already spent.
func shouldRetryMidStream(ev provider.StreamEvent, noContentYet bool, retries int) bool {
	return ev.Retryable && noContentYet && retries < maxMidStreamErrorRetries
}

// midStreamRetryDelay is the backoff before the retries-th mid-stream retry
// (1-indexed), matching the empty-response schedule's shape: linear in the
// attempt number, deliberately gentler than DoWithRetry's exponential
// transport backoff because a mid-stream overload usually clears fast.
func midStreamRetryDelay(retries int) time.Duration {
	return time.Duration(retries) * midStreamErrorBackoff
}

// emptyResponseRetryDelay is the backoff before the attempts-th retry of a
// complete-but-empty response (1-indexed).
func emptyResponseRetryDelay(attempts int) time.Duration {
	return time.Duration(attempts) * emptyResponseBackoff
}

// sleepOrDone waits for d, returning ctx.Err() if the context finishes first.
func sleepOrDone(ctx context.Context, d time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// collectedStream is a whole provider response gathered in one go.
type collectedStream struct {
	Text     string
	Thinking string
}

// Any returns the response text, falling back to thinking content for models
// that emit their answer there (some local models can't disable thinking).
func (c collectedStream) Any() string {
	if t := strings.TrimSpace(c.Text); t != "" {
		return t
	}
	return strings.TrimSpace(c.Thinking)
}

// streamAndCollect runs one non-incremental provider call and returns its
// whole text/thinking output, retrying under the same policy runTurn applies:
// retryable mid-stream provider errors (overloaded_error and friends) and
// complete-but-empty responses. Transport-level failures — connection loss,
// 429/5xx/529 — are already retried inside p.Stream by provider.DoWithRetry,
// so they never surface here.
//
// onUsage, if non-nil, is called once per attempt that reports usage, so
// every attempt's tokens are accounted for rather than only the last one's.
// A retryable error that runs out of retries is returned as an error, same as
// a non-retryable one: callers decide what a failed auxiliary call means (the
// classifier, for instance, treats it as "unknown risk — ask the human").
func streamAndCollect(ctx context.Context, p provider.Provider, req *provider.Request, onUsage func(*provider.Usage)) (collectedStream, error) {
	midStreamRetries, emptyAttempts := 0, 0
	for {
		if err := ctx.Err(); err != nil {
			return collectedStream{}, err
		}

		ch, err := p.Stream(ctx, req)
		if err != nil {
			return collectedStream{}, err
		}

		var text, thinking strings.Builder
		var streamErr error
		var retryable bool
		for ev := range ch {
			switch ev.Type {
			case provider.EventTextDelta:
				text.WriteString(ev.Text)
			case provider.EventThinkingDelta:
				thinking.WriteString(ev.Text)
			case provider.EventError:
				streamErr = ev.Error
				retryable = shouldRetryMidStream(ev, text.Len() == 0 && thinking.Len() == 0, midStreamRetries)
			case provider.EventDone:
				if ev.Usage != nil && onUsage != nil {
					onUsage(ev.Usage)
				}
			}
		}

		out := collectedStream{Text: text.String(), Thinking: thinking.String()}

		if streamErr != nil {
			if !retryable {
				return out, streamErr
			}
			midStreamRetries++
			if err := sleepOrDone(ctx, midStreamRetryDelay(midStreamRetries)); err != nil {
				return out, err
			}
			continue
		}

		if out.Any() == "" {
			if emptyAttempts >= maxEmptyResponseRetries {
				return out, fmt.Errorf("provider returned no content after %d attempts", emptyAttempts+1)
			}
			emptyAttempts++
			if err := sleepOrDone(ctx, emptyResponseRetryDelay(emptyAttempts)); err != nil {
				return out, err
			}
			continue
		}

		return out, nil
	}
}
