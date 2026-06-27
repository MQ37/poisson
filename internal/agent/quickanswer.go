package agent

import (
	"context"
	"fmt"

	"poisson/internal/provider"
)

const quickAnswerSystem = `You answer brief side questions while the user continues a main coding session. ` +
	`Be concise and direct. Do not ask follow-up questions unless essential.`

// StreamQuickAnswer issues a one-off provider request without touching session
// history or the agent output channel. Text deltas are sent on textCh until the
// stream ends; a terminal error (if any) is sent on errCh. Both channels close
// when the goroutine exits. Cancelling ctx stops the stream without an error.
func (a *Agent) StreamQuickAnswer(ctx context.Context, question string) (<-chan string, <-chan error, error) {
	if a == nil || a.provider == nil {
		return nil, nil, fmt.Errorf("agent not configured")
	}
	question = trimSpace(question)
	if question == "" {
		return nil, nil, fmt.Errorf("empty question")
	}

	req := &provider.Request{
		Model: a.currentModel(),
		System: []provider.SystemBlock{{
			Text: quickAnswerSystem,
		}},
		Messages: []provider.Message{{
			Role: "user",
			Content: []provider.ContentBlock{{
				Type: "text",
				Text: question,
			}},
		}},
		Effort: "low",
	}

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