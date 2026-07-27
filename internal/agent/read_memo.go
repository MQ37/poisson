package agent

// In-memory, per-session memoization of `read` tool calls. If the model
// re-reads a path at the same (or a narrower) line range and the file is
// byte-for-byte unchanged since the last real read, the second call is
// answered with a short pointer instead of resending the file — the model
// already has that content verbatim earlier in the conversation.
//
// Scoped to `read` only. bash/search/ls/etc. are deliberately not memoized:
// their output isn't a stable function of the input (git status, test runs,
// directory listings all change between calls), so caching them risks
// feeding stale state back to the model as if it were fresh — a correctness
// hazard, not just a missed optimization.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mq37/poisson/internal/tools"
)

// readMemo is what tryMemoizedRead compares a new call against.
type readMemo struct {
	modTime time.Time
	size    int64
	offset  int // effective request offset (0 = from line 1), as sent to the read tool
	limit   int // lines requested from offset (0 = unbounded); see rangeCovers
}

// tryMemoizedRead reports whether input (a `read` tool call) is fully
// covered by an unchanged prior read of the same path in this session,
// returning a stub result if so.
func (a *Agent) tryMemoizedRead(cwd string, input json.RawMessage) (string, bool) {
	reqPath, reqOffset, reqLimit, ok := tools.ParseReadCall(input)
	if !ok || reqPath == "" {
		return "", false
	}
	path := resolveMemoPath(cwd, reqPath)

	a.contextMu.Lock()
	prev, ok := a.readMemos[path]
	a.contextMu.Unlock()
	if !ok {
		return "", false
	}

	st, err := os.Stat(path)
	if err != nil || !st.ModTime().Equal(prev.modTime) || st.Size() != prev.size {
		return "", false // file changed (or is gone) since the memoized read
	}
	if !rangeCovers(prev.offset, prev.limit, reqOffset, reqLimit) {
		return "", false // asking for lines outside what was memoized
	}
	return fmt.Sprintf(
		"(unchanged since an earlier read of %s covering this range — reusing that content, no need to re-read)",
		reqPath), true
}

// recordRead remembers a successful, non-truncated, non-image real read so
// later identical/narrower reads of the same unchanged file can be
// memoized. content is the tool's returned content (used only to check
// whether the read was truncated or is an image — never stored).
func (a *Agent) recordRead(cwd string, input json.RawMessage, content string) {
	if tools.ReadWasTruncated(content) || tools.ReadIsImage(content) {
		return // don't know the true extent covered (or it's not line-ranged) — skip
	}
	reqPath, reqOffset, reqLimit, ok := tools.ParseReadCall(input)
	if !ok || reqPath == "" {
		return
	}
	path := resolveMemoPath(cwd, reqPath)
	st, err := os.Stat(path)
	if err != nil {
		return
	}
	a.contextMu.Lock()
	if a.readMemos == nil {
		a.readMemos = map[string]readMemo{}
	}
	a.readMemos[path] = readMemo{
		modTime: st.ModTime(),
		size:    st.Size(),
		offset:  reqOffset,
		limit:   reqLimit,
	}
	a.contextMu.Unlock()
}

// invalidateReadMemo drops any memoized read for the path an edit/write
// tool call targets. Belt-and-suspenders on top of recordRead's mtime/size
// check (which already catches this on the next read) — coarse filesystem
// mtime resolution could in theory leave both unchanged across a
// same-length in-place edit within the same tick.
func (a *Agent) invalidateReadMemo(cwd string, input json.RawMessage) {
	var in struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(input, &in) != nil || in.Path == "" {
		return
	}
	path := resolveMemoPath(cwd, in.Path)
	a.contextMu.Lock()
	delete(a.readMemos, path)
	a.contextMu.Unlock()
}

// resolveMemoPath mirrors the read tool's own (unexported) path resolution
// so memo keys line up with what it actually opens.
func resolveMemoPath(cwd, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(cwd, p)
}

// rangeCovers reports whether a memoized read [haveOffset, haveOffset+haveLimit)
// fully contains a requested read [wantOffset, wantOffset+wantLimit). offset=0
// means "from line 1" and limit=0 means "unbounded" on input, matching the
// read tool's own defaulting (internal/tools/read.go) — but a memoized
// unbounded read only counts as covering "to EOF" when recordRead already
// confirmed it wasn't truncated.
func rangeCovers(haveOffset, haveLimit, wantOffset, wantLimit int) bool {
	ho := haveOffset
	if ho == 0 {
		ho = 1
	}
	wo := wantOffset
	if wo == 0 {
		wo = 1
	}
	if wo < ho {
		return false // wants lines before what was memoized
	}
	if haveLimit == 0 {
		return true // memoized read was unbounded and untruncated — covers anything from ho onward
	}
	if wantLimit == 0 {
		return false // wants "to EOF"; memoized read was capped — can't guarantee coverage
	}
	haveEnd := ho + haveLimit // exclusive
	wantEnd := wo + wantLimit // exclusive
	return wantEnd <= haveEnd
}
