package tools

import "strings"

// CountLeakedInvokes reports how many `<invoke` tags appear in text — the
// shape of an invoke tag wrapping one or more parameter tags that several
// coding-agent harnesses use for tool calls. Poisson never emits this
// template itself (every provider path here uses structured, provider-
// native tool-calling, never a textual XML instruction) — but a weak or
// local model whose chat template doesn't reliably route to a real,
// provider-native tool call sometimes reproduces the shape anyway, most
// likely pattern-matched from training data that saw it used elsewhere,
// as plain assistant text instead of issuing an actual tool_use/tool_calls
// block. The API then reports zero tool calls: nothing the text describes
// ever runs, and without this check poisson would show the leaked XML to
// the user as if it were the model's real, finished answer.
//
// Deliberately a presence count, not a structural parser: the leaked shape
// seen in the wild is often itself garbled (tool name missing from the
// invoke tag, used as a bogus parameter name instead, unclosed tags) — not
// trustworthy enough to reconstruct into a real call and execute. Callers
// use the count to reject the response and ask the model to reissue it as
// a real tool call, the same corrective-retry shape validateToolInput
// already uses for a leak inside one tool call's own arguments (see
// validate.go).
func CountLeakedInvokes(text string) int {
	return strings.Count(text, "<invoke")
}
