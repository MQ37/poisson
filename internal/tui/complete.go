package tui

// completionKind distinguishes slash command completion from @file completion.
type completionKind uint8

const (
	completionNone completionKind = iota
	completionSlash
	completionAtFile
)

// completion is the candidate list shown above the input box.
type completion struct {
	kind       completionKind
	prefix     string // the partial token the user typed (with leading / or @)
	cands      []string
	idx        int  // -1 = no selection; otherwise 0..len-1
	truncated  bool // true when file matches were capped at fuzzyResultCap
}

// empty reports whether the completion has nothing to show.
func (c *completion) empty() bool { return c == nil || len(c.cands) == 0 }

// selected returns the current selection or "" if none.
func (c *completion) selected() string {
	if c.empty() || c.idx < 0 {
		return ""
	}
	return c.cands[c.idx]
}

// cycle moves the selection forward (dir=+1) or backward (dir=-1).
func (c *completion) cycle(dir int) {
	if c.empty() {
		return
	}
	c.idx += dir
	if c.idx < 0 {
		c.idx = len(c.cands) - 1
	} else if c.idx >= len(c.cands) {
		c.idx = 0
	}
}

// reset clears the selection without losing the candidate list.
func (c *completion) reset() { c.idx = -1 }

// slashCommands is the canonical list exposed to the user. Keep in sync with
// commands.go.
var slashCommands = []string{
	"/quit", "/clear", "/help", "/name", "/new", "/resume", "/sessions",
	"/search", "/compact", "/model", "/effort",
	"/providers", "/reload", "/cost", "/btw",
}

// matchSlash returns slash commands matching partial (prefix or fuzzy).
func matchSlash(partial string) []string {
	return matchSlashFuzzy(partial)
}


// commonPrefix returns the longest common prefix of all strings, or "" if
// the slice is empty.
func commonPrefixCands(xs []string) string {
	if len(xs) == 0 {
		return ""
	}
	if len(xs) == 1 {
		return xs[0]
	}
	lcp := xs[0]
	for _, s := range xs[1:] {
		lcp = commonPrefix(lcp, s)
		if lcp == "" {
			return ""
		}
	}
	return lcp
}
