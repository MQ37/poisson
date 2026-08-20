package tools

import (
	"encoding/json"
	"fmt"
	"strings"
)

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
func CountLeakedInvokes(text string) int {
	return strings.Count(text, "<invoke")
}

// ParseLeakedInvokes recovers as many invoke blocks from text as it can
// confidently resolve against reg's registered tools, returning them as
// real ToolCalls dispatchable exactly like a normal tool_use block — the
// same guard/approval path Registry.Execute already runs for every tool
// call applies here too, so recovery is no less safe than the model
// calling the tool correctly. A block that doesn't resolve cleanly (unknown
// tool name, a field name that isn't in that tool's schema, an ambiguous
// shape) is left out rather than guessed — see resolveInvoke.
//
// cleaned is text with exactly the blocks that turned into a real call
// removed, leaving any unresolved block untouched. A recovered leak that
// stayed in the assistant's own stored text would read as "leak this
// template and it still works" — the same imitation risk
// validateToolInput's doc comment describes for a leak inside one tool
// call's own arguments — so a successful recovery strips it from history
// instead of rewarding the pattern. An unresolved block never ran, so
// there's nothing to hide by leaving it visible as-is.
func ParseLeakedInvokes(text string, reg *Registry) (cleaned string, calls []ToolCall) {
	var b strings.Builder
	last := 0
	for _, block := range extractInvokeBlocks(text) {
		call, ok := resolveInvoke(block, reg)
		if !ok {
			continue
		}
		call.ID = fmt.Sprintf("recovered_%d", len(calls))
		calls = append(calls, call)
		b.WriteString(text[last:block.start])
		last = block.end
	}
	b.WriteString(text[last:])
	return strings.TrimSpace(b.String()), calls
}

// leakedInvoke is one <invoke>...</invoke> block extracted from leaked text,
// before it's resolved against any tool's real schema. start/end are its
// byte range in the original text (including both tags), used to strip a
// successfully recovered block back out of that text.
type leakedInvoke struct {
	name       string // the invoke tag's own name attribute; "" if absent
	params     []leakedParam
	start, end int
}

type leakedParam struct {
	name, value string
}

// resolveInvoke maps one leaked block onto a real, registered tool. Two
// shapes are recognized, both observed in the wild:
//
//   - Well-formed: the invoke tag carries the tool's real name, and each
//     parameter tag names one of that tool's real schema fields.
//   - Garbled: the invoke tag has no name attribute at all, and the tool
//     name leaked as the sole parameter's name instead (e.g. a bash call
//     rendered as one parameter tag named "bash" holding the command text) —
//     that parameter's value stands in for the tool's primary argument
//     (schemaPrimaryField).
//
// Anything else (unknown tool name, a field name outside the tool's schema,
// more than one parameter in the garbled shape) is rejected: not enough
// signal to safely reconstruct a real call.
func resolveInvoke(block leakedInvoke, reg *Registry) (ToolCall, bool) {
	if reg == nil {
		return ToolCall{}, false
	}
	if block.name != "" {
		tool, ok := reg.Get(block.name)
		if !ok {
			return ToolCall{}, false
		}
		return buildCall(tool, block.params)
	}
	if len(block.params) != 1 {
		return ToolCall{}, false
	}
	tool, ok := reg.Get(block.params[0].name)
	if !ok {
		return ToolCall{}, false
	}
	return buildCallFromPrimaryValue(tool, block.params[0].value)
}

// buildCall maps params directly onto tool's schema fields (the well-formed
// shape). Rejects the whole block if any param name isn't one of the tool's
// real fields, or if nothing recognizable came through at all.
func buildCall(tool Tool, params []leakedParam) (ToolCall, bool) {
	props := schemaTopLevelProperties(tool.Schema())
	fields := make(map[string]string, len(params))
	for _, p := range params {
		if props != nil && !props[p.name] {
			return ToolCall{}, false
		}
		fields[p.name] = p.value
	}
	if len(fields) == 0 {
		return ToolCall{}, false
	}
	input, err := json.Marshal(fields)
	if err != nil {
		return ToolCall{}, false
	}
	return ToolCall{Name: tool.Name(), Input: input}, true
}

// buildCallFromPrimaryValue handles the garbled shape: value is assigned to
// tool's first required schema field. The value itself sometimes still
// carries that field's real name as a "field=value" prefix (seen in the
// wild for a grep call: pattern leaked as the tool name, then its own value
// re-prefixed with "pattern=" anyway) — stripped if present so it doesn't
// end up baked into the recovered value.
func buildCallFromPrimaryValue(tool Tool, value string) (ToolCall, bool) {
	field, ok := schemaPrimaryField(tool.Schema())
	if !ok {
		return ToolCall{}, false
	}
	if rest, ok := strings.CutPrefix(value, field+"="); ok {
		value = rest
	}
	input, err := json.Marshal(map[string]string{field: value})
	if err != nil {
		return ToolCall{}, false
	}
	return ToolCall{Name: tool.Name(), Input: input}, true
}

// schemaPrimaryField returns the first entry of a JSON Schema's "required"
// array — by convention in this codebase's tool schemas, the field a tool
// is meaningless without (bash's command, grep's pattern, read's path).
func schemaPrimaryField(schema json.RawMessage) (string, bool) {
	var parsed struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(schema, &parsed); err != nil || len(parsed.Required) == 0 {
		return "", false
	}
	return parsed.Required[0], true
}

// extractInvokeBlocks splits text into invoke blocks by hand rather than
// with regexp: Go's RE2 engine has no lookahead, and the boundary of an
// unclosed parameter tag (stop at either a literal closing tag or the next
// parameter tag, whichever comes first) needs one to express without
// re-consuming input. Blocks are not expected to nest. Tracks each block's
// byte range in text (not just its content) so a caller can strip exactly
// the recovered ones back out afterward.
func extractInvokeBlocks(text string) []leakedInvoke {
	const (
		openTag  = "<invoke"
		closeTag = "</invoke>"
	)
	var blocks []leakedInvoke
	pos := 0
	for {
		i := strings.Index(text[pos:], openTag)
		if i < 0 {
			return blocks
		}
		start := pos + i
		afterOpen := start + len(openTag)
		tagEnd := strings.IndexByte(text[afterOpen:], '>')
		if tagEnd < 0 {
			return blocks // unterminated opening tag — nothing more to recover
		}
		attrs := text[afterOpen : afterOpen+tagEnd]
		bodyStart := afterOpen + tagEnd + 1
		name, _ := extractAttr(attrs, "name")
		var body string
		var end int
		if closeAt := strings.Index(text[bodyStart:], closeTag); closeAt >= 0 {
			body = text[bodyStart : bodyStart+closeAt]
			end = bodyStart + closeAt + len(closeTag)
		} else {
			body = text[bodyStart:]
			end = len(text)
		}
		blocks = append(blocks, leakedInvoke{name: name, params: extractParams(body), start: start, end: end})
		if end >= len(text) {
			return blocks
		}
		pos = end
	}
}

// extractParams scans one invoke block's body for parameter tags. A tag
// missing its closing counterpart (the garbled shape seen in the wild) runs
// until the next parameter tag or the end of the body instead.
func extractParams(body string) []leakedParam {
	const (
		openTag  = "<parameter"
		closeTag = "</parameter>"
	)
	var params []leakedParam
	rest := body
	for {
		i := strings.Index(rest, openTag)
		if i < 0 {
			return params
		}
		rest = rest[i+len(openTag):]
		tagEnd := strings.IndexByte(rest, '>')
		if tagEnd < 0 {
			return params
		}
		attrs := rest[:tagEnd]
		rest = rest[tagEnd+1:]
		name, ok := extractAttr(attrs, "name")
		if !ok {
			continue // malformed parameter tag with no name — skip, keep scanning
		}
		end := strings.Index(rest, closeTag)
		nextOpen := strings.Index(rest, openTag)
		var value string
		switch {
		case end >= 0 && (nextOpen < 0 || end < nextOpen):
			value, rest = rest[:end], rest[end+len(closeTag):]
		case nextOpen >= 0:
			value, rest = rest[:nextOpen], rest[nextOpen:]
		default:
			value, rest = rest, ""
		}
		params = append(params, leakedParam{name: name, value: strings.TrimSpace(value)})
		if rest == "" {
			return params
		}
	}
}

// extractAttr finds key="value" inside a tag's raw attribute text and
// returns value.
func extractAttr(attrs, key string) (string, bool) {
	needle := key + `="`
	i := strings.Index(attrs, needle)
	if i < 0 {
		return "", false
	}
	rest := attrs[i+len(needle):]
	j := strings.IndexByte(rest, '"')
	if j < 0 {
		return "", false
	}
	return rest[:j], true
}
