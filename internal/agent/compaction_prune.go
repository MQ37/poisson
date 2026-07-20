package agent

// Stale-tool-result pruning: called once from compact(), right after it has
// decided the kept (surviving) active tail. Scans that tail for a `read`
// result made stale by a LATER call on the same path within the same
// tail — either an edit/write (content changed under it) or a later read
// whose range fully covers it (redundant duplicate) — and rewrites just
// that one tool_result to a short placeholder via store.UpdateMessageContent.
//
// This is a different, smaller cleanup than compact()'s own summarization:
// it never touches which messages get summarized away, only shrinks bytes
// within messages that are staying active regardless. It's only safe to do
// here, timing-wise: compact() is already about to force a full prompt-cache
// rewrite for everything from the compaction boundary onward (new summary
// system block + shrunk message set change the request's bytes regardless),
// so mutating kept-tail message content in place doesn't cause any
// *additional* cache invalidation beyond what compact() already pays for.
// Done at any other time, editing an already-cached earlier message would
// force a full-price cache miss on the very next request for no reason.

import (
	"encoding/json"
	"fmt"
	"log"

	"github.com/mq37/poisson/internal/store"
	"github.com/mq37/poisson/internal/tools"
)

// pruneMinBytes is the minimum size of a `read` tool_result worth pruning —
// below this, the bookkeeping isn't worth it.
const pruneMinBytes = 500

// readOp is one read/edit/write call found in the kept tail, correlating a
// tool_use call (from an assistant message) with the tool_result block that
// answered it (from the following tool message).
type readOp struct {
	kind      string // "read", "edit", "write"
	path      string
	offset    int
	limit     int
	truncated bool // read only: was the returned content cut short by a cap?
	isImage   bool // read only
	msgIdx    int  // index into the kept slice holding the tool_result block
	blkIdx    int  // index into that message's content blocks
	pruned    bool
}

// pruneStaleToolResults mutates kept's stale `read` results in place (via
// store.UpdateMessageContent) and logs any per-message rewrite failure —
// never fatal, since a failed prune just leaves that one result unshrunk.
func (a *Agent) pruneStaleToolResults(cwd string, kept []store.Message) {
	toolCallInfo := map[string]readOp{} // tool_call_id -> parsed call, no msg/blk location yet
	var ops []*readOp

	for i, m := range kept {
		var blocks []contentBlockJSON
		if json.Unmarshal([]byte(m.Content), &blocks) != nil {
			continue
		}
		switch m.Role {
		case "assistant":
			for _, b := range blocks {
				if b.Type != "tool_use" {
					continue
				}
				if op, ok := parseToolUseOp(b.ToolName, b.ToolInput, cwd); ok {
					toolCallInfo[b.ToolCallID] = op
				}
			}
		case "tool":
			for j, b := range blocks {
				if b.Type != "tool_result" || b.ToolIsError {
					continue
				}
				info, ok := toolCallInfo[b.ToolCallID]
				if !ok {
					continue
				}
				if info.kind == "read" {
					if len(b.ToolResult) < pruneMinBytes {
						continue
					}
					info.truncated = tools.ReadWasTruncated(b.ToolResult)
					info.isImage = tools.ReadIsImage(b.ToolResult)
				}
				info.msgIdx, info.blkIdx = i, j
				ops = append(ops, &info)
			}
		}
	}

	// activeReads holds, per path, every not-yet-superseded read seen so
	// far — not just the single most recent one: two earlier reads of
	// disjoint ranges (neither covering the other) must BOTH be caught by a
	// later edit/write of that path, not just whichever happened to be
	// tracked most recently.
	activeReads := map[string][]*readOp{}
	for _, op := range ops {
		switch op.kind {
		case "edit", "write":
			for _, r := range activeReads[op.path] {
				r.pruned = true
			}
			delete(activeReads, op.path)
		case "read":
			if op.isImage {
				continue
			}
			var stillActive []*readOp
			for _, r := range activeReads[op.path] {
				if !op.truncated && rangeCovers(op.offset, op.limit, r.offset, r.limit) {
					r.pruned = true
				} else {
					stillActive = append(stillActive, r)
				}
			}
			activeReads[op.path] = append(stillActive, op)
		}
	}

	for _, op := range ops {
		if !op.pruned {
			continue
		}
		if err := a.rewriteToolResultBlock(&kept[op.msgIdx], op.blkIdx, op.kind, op.path); err != nil {
			log.Printf("warning: prune stale tool result: %v", err)
		}
	}
}

// parseToolUseOp extracts the (kind, path, offset, limit) a read/edit/write
// tool_use call targets, resolving path the same way the tools themselves
// (and read_memo.go) do. ok is false for any other tool or unparseable input.
func parseToolUseOp(name string, input json.RawMessage, cwd string) (op readOp, ok bool) {
	switch name {
	case "read", "edit", "write":
	default:
		return readOp{}, false
	}
	var in readCallInput
	if json.Unmarshal(input, &in) != nil || in.Path == "" {
		return readOp{}, false
	}
	return readOp{kind: name, path: resolveMemoPath(cwd, in.Path), offset: int(in.Offset), limit: int(in.Limit)}, true
}

// rewriteToolResultBlock replaces one tool_result block's content with a
// short placeholder and persists the message.
func (a *Agent) rewriteToolResultBlock(m *store.Message, blkIdx int, supersededKind, path string) error {
	var blocks []contentBlockJSON
	if err := json.Unmarshal([]byte(m.Content), &blocks); err != nil {
		return err
	}
	if blkIdx >= len(blocks) {
		return nil
	}
	blocks[blkIdx].ToolResult = fmt.Sprintf(
		"(superseded by a later %s of %s — pruned to save space)", supersededKind, path)
	data, err := json.Marshal(blocks)
	if err != nil {
		return err
	}
	m.Content = string(data)
	return a.store.UpdateMessageContent(m.ID, m.Content)
}
