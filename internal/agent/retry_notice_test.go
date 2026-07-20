package agent

import (
	"context"
	"time"

	"github.com/mq37/poisson/internal/provider"
)

// retryScriptProvider is a minimal provider.Provider used only to test
// streamWithRetryNotice's translation of provider.RetryTrace callbacks into
// OutputRetrying events: it reads the RetryTrace attached to ctx (via
// provider.WithRetryTrace, exactly as DoWithRetry does inside a real
// provider's Stream()) and fires it per a fixed script before returning a
// normal successful response — no real network, no timing dependency, no
// need to reach into DoWithRetry's unexported backoff-schedule vars from
// another package.
type retryScriptProvider struct {
	// retries lists (attempt, delay, reason) to feed to OnRetry, in order.
	retries []retryScriptStep
	// recovered, if true, calls OnRecovered after the retries.
	recovered bool
	response  []provider.StreamEvent
}

type retryScriptStep struct {
	attempt int
	delay   time.Duration
	reason  string
}

func (p *retryScriptProvider) ID() string { return "retry-script" }

func (p *retryScriptProvider) Models() ([]provider.Model, error) { return nil, nil }

func (p *retryScriptProvider) Stream(ctx context.Context, req *provider.Request) (<-chan provider.StreamEvent, error) {
	if trace := provider.RetryTraceFromContext(ctx); trace != nil {
		for _, step := range p.retries {
			if trace.OnRetry != nil {
				trace.OnRetry(step.attempt, step.delay, step.reason)
			}
		}
		if p.recovered && trace.OnRecovered != nil {
			trace.OnRecovered()
		}
	}
	ch := make(chan provider.StreamEvent, len(p.response)+1)
	go func() {
		defer close(ch)
		for _, ev := range p.response {
			ch <- ev
		}
	}()
	return ch, nil
}
