package tools

import (
	"encoding/json"
	"fmt"
	"strings"
)

// validateToolInput catches a specific model failure mode observed in
// production sessions: Claude occasionally leaks literal fragments of its
// own internal tool-call template (`<parameter name="X">value</parameter>`)
// into the JSON tool_input it sends, either as a bogus extra key —
// `{"parameter name": "edits"}` instead of actually filling `edits` — or as
// stray `">` / `="` glued onto the end of a real string value. The former
// silently drops a required field (edit tool: "no edits provided"); the
// latter breaks regexes (search tool: "the literal \n is not allowed") or,
// worse, produces a false "no matches found" with no hint anything was
// wrong. Once one malformed call lands in the conversation, the model tends
// to imitate its own prior turn and repeats it for the rest of the session
// (seen recurring ~100 times in a single session in the wild) — so this is
// checked and rejected with a corrective message before the tool ever runs,
// to stop it propagating past the first occurrence.
func validateToolInput(schema, input json.RawMessage) error {
	props := schemaTopLevelProperties(schema)
	if props == nil {
		return nil
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(input, &fields); err != nil {
		return nil // let the tool's own Unmarshal produce the error
	}

	for key, raw := range fields {
		if !props[key] {
			if strings.HasPrefix(key, "parameter") {
				return fmt.Errorf("malformed tool call: unexpected field %q — this looks like a leaked tool-call template fragment (e.g. `<parameter name=...>`), not a real argument. Send plain JSON key/value pairs only, no XML/HTML tags.", key)
			}
			return fmt.Errorf("malformed tool call: unexpected field %q is not one of this tool's parameters — check the tool's schema and resend with the correct field name.", key)
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			continue // not a string value, nothing to check
		}
		if leaksToolCallTemplate(s) {
			return fmt.Errorf("malformed tool call: field %q contains a leaked tool-call template fragment (literal `<parameter name=...>` / trailing `\">` or `=\"`). Resend it as a clean value with no XML/HTML tags.", key)
		}
	}
	return nil
}

// leaksToolCallTemplate flags the two shapes seen leaking into real string
// values: the literal opening tag, or the tag's closing punctuation stuck
// onto the end of the string (`..."` from closing a quoted attribute, then
// `>` from closing the tag).
//
// The trailing-punctuation check only fires when that punctuation is itself
// followed by trailing whitespace/newline that was trimmed off — every
// observed corruption sample looked like `...">\n` or `...="\n`, never a
// bare `...">` with nothing after it. That trailing whitespace is the actual
// signal: a model has no legitimate reason to end a single-line pattern,
// command, or path with a gratuitous newline, whereas ending it with a bare
// `">` or `="` is completely ordinary when the value is itself a fragment of
// HTML/JSX source (e.g. searching for `href="` or `<img src="`). Requiring
// the trim to have actually removed something keeps those legitimate
// searches working while still catching the leaked artifact.
func leaksToolCallTemplate(s string) bool {
	if strings.Contains(s, `<parameter name=`) {
		return true
	}
	trimmed := strings.TrimRight(s, "\n\r\t ")
	if trimmed == s {
		return false
	}
	return strings.HasSuffix(trimmed, `">`) || strings.HasSuffix(trimmed, `="`)
}

// schemaTopLevelProperties extracts the set of allowed top-level property
// names from a JSON Schema object. Returns nil if the schema has no
// "properties" (nothing to validate against).
func schemaTopLevelProperties(schema json.RawMessage) map[string]bool {
	var parsed struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(schema, &parsed); err != nil || parsed.Properties == nil {
		return nil
	}
	names := make(map[string]bool, len(parsed.Properties))
	for name := range parsed.Properties {
		names[name] = true
	}
	return names
}
