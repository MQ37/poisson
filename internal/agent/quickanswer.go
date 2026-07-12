package agent

import (
	"context"
	"fmt"

	"poisson/internal/provider"
)

const quickAnswerSystem = `You answer brief side questions while the user continues a main coding session. ` +
	`Be concise and direct. Do not ask follow-up questions unless essential.`

// btwQuestionPrefix wraps a /btw question. It rides in the appended user turn
// (never the system prompt) so the cached system+tools+messages prefix stays
// byte-identical to the main agent's request and hits the cache.
const btwQuestionPrefix = "[Side question from the user — answer directly and concisely using the conversation above for context. " +
	"This is a quick aside; do NOT call any tools, just answer.]\n\n"

// StreamQuickAnswer answers a /btw side question with the full conversation as
// context. It reuses buildRequest's exact system + tools + messages prefix so
// the request hits the main conversation's prompt cache, then appends the
// question as a new user turn. Nothing is written to the session/store or the
// agent output channel. Text deltas stream on textCh; a terminal error (if any)
// on errCh; both close when the goroutine exits. Cancelling ctx stops it.
func (a *Agent) StreamQuickAnswer(ctx context.Context, question string) (<-chan string, <-chan error, error) {
	if a == nil || a.provider == nil {
		return nil, nil, fmt.Errorf("agent not configured")
	}
	question = trimSpace(question)
	if question == "" {
		return nil, nil, fmt.Errorf("empty question")
	}

	// Reuse the live conversation prefix (system + tools + messages + effort +
	// cache key) so the side question is answered in context and reuses the
	// prompt cache. Keep the session effort so history thinking-blocks serialize
	// identically (a different thinking-enabled state would change the cached
	// bytes and miss). Fall back to a standalone request before any session
	// exists (e.g. /btw as the very first thing).
	req, err := a.buildRequest()
	if err != nil {
		req = &provider.Request{
			Model:  a.currentModel(),
			System: []provider.SystemBlock{{Text: quickAnswerSystem}},
			Effort: "low",
		}
	}
	req.Messages = append(req.Messages, provider.Message{
		Role: "user",
		Content: []provider.ContentBlock{{
			Type: "text",
			Text: btwQuestionPrefix + question,
		}},
	})

	ch, err := a.provider.Stream(ctx, req)
	if err != nil {
		return nil, nil, err
	}

	textCh := make(chan string, 32)
	errCh := make(chan error, 1)
	go func() {
		defer close(textCh)
		defer close(errCh)
		var streamErr error
		for ev := range ch {
			switch ev.Type {
			case provider.EventTextDelta:
				if ev.Text != "" {
					textCh <- ev.Text
				}
			case provider.EventError:
				streamErr = ev.Error
			case provider.EventDone:
			}
		}
		if streamErr != nil {
			errCh <- streamErr
		}
	}()
	return textCh, errCh, nil
}

func trimSpace(s string) string {
	i, j := 0, len(s)
	for i < j && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n') {
		i++
	}
	for j > i && (s[j-1] == ' ' || s[j-1] == '\t' || s[j-1] == '\n') {
		j--
	}
	return s[i:j]
}
