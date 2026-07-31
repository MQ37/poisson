package store

// DisplaySessionIDMaxLen bounds how many characters of a session id appear
// in space-constrained UI: `px sessions`, `px resume`'s own listing source,
// /search, /cost, recall results, the session picker, and the window/tab
// title. NewSessionID ("s-" + 8 hex = 10 chars) and NewSubagentID ("sub-" +
// 8 hex = 12 chars) already fit under this, so every id created today shows
// in full everywhere — this only bites an id from some other/legacy
// format. Before DisplaySessionID existed, each of those call sites picked
// its own cutoff (6, 8, or 12, with or without an ellipsis) with no shared
// reasoning, so an id copied from one listing couldn't be pasted into
// `px resume`/`px cost` with any confidence it matched what another
// listing showed for the same session.
const DisplaySessionIDMaxLen = 12

// DisplaySessionID is the single place that decides how a session id is
// shown. Returns id unchanged when it already fits; otherwise a truncated
// copy marked with "…" so it's never mistaken for the full, exact-match id
// GetSession requires.
func DisplaySessionID(id string) string {
	if len(id) <= DisplaySessionIDMaxLen {
		return id
	}
	return id[:DisplaySessionIDMaxLen] + "…"
}
