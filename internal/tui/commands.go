package tui

import (
	"errors"
	"fmt"
	"strings"

	"poisson/internal/agent"
	"poisson/internal/config"
	"poisson/internal/provider"
	"poisson/internal/store"
)

// commandHost is the small interface the slash commands use. The TUI wraps
// itself in tuiCmdHost before dispatching.
type commandHost interface {
	Agent() *agent.Agent
	SessionID() string
	SetSessionID(string)
	Out(LineStyle, string)
}

// tuiCmdHost wraps *TUI.
type tuiCmdHost struct{ t *TUI }

func (h tuiCmdHost) Agent() *agent.Agent    { return h.t.agent }
func (h tuiCmdHost) SessionID() string      { return h.t.sessionID }
func (h tuiCmdHost) SetSessionID(id string) { h.t.sessionID = id; h.t.status.SessionID = id }
func (h tuiCmdHost) Out(style LineStyle, s string) {
	h.t.scroll.appendRaw(style, s)
	h.t.markScrollDirty()
}

// cmdName sets or shows the session display title.
func cmdName(h commandHost, args []string) error {
	a := h.Agent()
	sid := h.SessionID()
	if len(args) == 0 {
		sess, err := a.Store().GetSession(sid)
		if err != nil {
			h.Out(styleSystem, "title: (unsaved session — send a message or /name <title> first)")
			return nil
		}
		if sess.Title != nil && strings.TrimSpace(*sess.Title) != "" {
			h.Out(styleSystem, "title: "+*sess.Title)
		} else {
			h.Out(styleSystem, "title: (unset)")
		}
		return nil
	}
	title := strings.TrimSpace(strings.Join(args, " "))
	if title == "" {
		h.Out(styleSystem, "usage: /name <title>")
		return nil
	}
	if err := a.EnsureSession(); err != nil {
		h.Out(styleError, "session error: "+err.Error())
		return nil
	}
	if err := a.Store().SetSessionTitle(sid, title); err != nil {
		h.Out(styleError, "error setting title: "+err.Error())
		return nil
	}
	h.Out(styleSystem, "session title: "+title)
	return nil
}

// cmdNew switches to a fresh session id. The row is persisted on first message.
func cmdNew(h commandHost) error {
	a := h.Agent()
	id := store.NewSessionID()
	a.SwitchSession(id)
	h.SetSessionID(id)
	resetHostSessionView(h)
	h.Out(styleSystem, "new session: "+id)
	return nil
}

// cmdResume switches to an existing session.
func cmdResume(h commandHost, args []string) error {
	if len(args) == 0 {
		h.Out(styleSystem, "usage: /resume <session-id>")
		return nil
	}
	a := h.Agent()
	sess, err := a.Store().GetSession(args[0])
	if err != nil {
		h.Out(styleError, "session not found: "+args[0])
		return nil
	}
	if !switchAgentToSession(h, sess) {
		return nil
	}
	label := sess.ID
	if sess.Title != nil && strings.TrimSpace(*sess.Title) != "" {
		label = *sess.Title + " (" + sess.ID + ")"
	}
	h.Out(styleSystem, fmt.Sprintf("resumed session: %s (%s/%s)", label, sess.Provider, sess.Model))
	return nil
}

// cmdSearch searches message content.
func cmdSearch(h commandHost, args []string) error {
	if len(args) == 0 {
		h.Out(styleSystem, "usage: /search <query>")
		return nil
	}
	results, err := h.Agent().Store().Search(strings.Join(args, " "), 20)
	if err != nil {
		h.Out(styleError, "search error: "+err.Error())
		return nil
	}
	if len(results) == 0 {
		h.Out(styleSystem, "no results")
		return nil
	}
	var b strings.Builder
	for _, r := range results {
		short := r.SessionID
		if len(short) > 6 {
			short = short[:6]
		}
		b.WriteString(fmt.Sprintf("  [%s] %s: %s\n", short, r.Role, r.Snippet))
	}
	h.Out(styleSystem, b.String())
	return nil
}

func switchAgentToSession(h commandHost, sess *store.Session) bool {
	if th, ok := h.(tuiCmdHost); ok && th.t.sessionBusyLocked() {
		h.Out(styleError, "cannot switch session while agent is running or compacting")
		return false
	}
	if sess == nil {
		h.Out(styleError, "session not found")
		return false
	}
	a := h.Agent()
	p := provider.NewProviderFromDisk(sess.Provider, a.Config())
	if p == nil {
		h.Out(styleError, "unknown provider in session: "+sess.Provider)
		return false
	}
	a.SwitchSession(sess.ID)
	warnPersist(h, a.SetProvider(p))
	warnPersist(h, a.SetModel(sess.Model))
	h.SetSessionID(sess.ID)
	resetHostSessionView(h)
	return true
}

func resetHostSessionView(h commandHost) {
	if th, ok := h.(tuiCmdHost); ok {
		th.t.resetSessionViewLocked()
	}
}

// refreshHostHeader re-syncs the header from the agent and forces a repaint.
// Used after model/provider switches so the context window and model label
// reflect the new selection immediately. Commands run with t.mu held, so this
// calls the locked variant directly (matching resetHostSessionView).
func refreshHostHeader(h commandHost) {
	if th, ok := h.(tuiCmdHost); ok {
		th.t.syncHeaderFromAgentLocked()
		th.t.dirty.markFull()
		// A freshly switched-in provider's usage cache always starts empty
		// (see triggerUsageRefreshLocked) — fetch it now instead of leaving
		// the header blank until the next scheduled 5-minute tick.
		th.t.triggerUsageRefreshLocked()
	}
}

// warnPersist surfaces a best-effort session-persist failure to the user rather
// than dropping it silently.
func warnPersist(h commandHost, err error) {
	if err != nil {
		h.Out(styleError, "warning: session not persisted: "+err.Error())
	}
}

// cmdModel switches provider/model.
func cmdModel(h commandHost, args []string) error {
	a := h.Agent()
	if len(args) == 0 {
		h.Out(styleSystem, fmt.Sprintf("current model: %s/%s", a.Provider().ID(), a.Model()))
		return nil
	}
	target := strings.Join(args, " ")
	if !strings.Contains(target, "/") {
		provName := strings.TrimSpace(target)
		newProv := a.Provider()
		if provName != a.Provider().ID() {
			newProv = provider.NewProviderFromDisk(provName, a.Config())
			if newProv == nil {
				h.Out(styleError, "unknown provider: "+provName)
				return nil
			}
			if !provider.IsConfiguredFromDisk(provName, a.Config()) {
				h.Out(styleError, "provider "+provName+" is not configured — run: px login "+provName)
				return nil
			}
			warnPersist(h, a.SetProvider(newProv))
		}
		model := provider.DefaultModel(provName, a.Config())
		if model == "" {
			h.Out(styleError, "no default model configured for provider: "+provName)
			return nil
		}
		warnPersist(h, a.SetModel(model))
		a.ReloadConfigDependentTools()
		refreshHostHeader(h)
		h.Out(styleSystem, fmt.Sprintf("model: %s/%s", provName, model))
		return nil
	}
	parts := strings.SplitN(target, "/", 2)
	provName, modelName := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	if provName == "" || modelName == "" {
		h.Out(styleSystem, "usage: /model <provider>/<model> or /model <provider>")
		return nil
	}
	if provName != a.Provider().ID() {
		newProv := provider.NewProviderFromDisk(provName, a.Config())
		if newProv == nil {
			h.Out(styleError, "unknown provider: "+provName)
			return nil
		}
		if !provider.IsConfiguredFromDisk(provName, a.Config()) {
			h.Out(styleError, "provider "+provName+" is not configured — run: px login "+provName)
			return nil
		}
		warnPersist(h, a.SetProvider(newProv))
	}
	warnPersist(h, a.SetModel(modelName))
	a.ReloadConfigDependentTools()
	refreshHostHeader(h)
	h.Out(styleSystem, fmt.Sprintf("model: %s/%s", provName, modelName))
	return nil
}

// cmdReload reloads configuration.
func cmdReload(h commandHost) error {
	a := h.Agent()
	cfg, err := config.Load()
	if err != nil {
		h.Out(styleError, "error loading config: "+err.Error())
		return nil
	}
	a.SetConfig(cfg)
	if p := provider.NewProviderFromDisk(a.Provider().ID(), cfg); p != nil {
		warnPersist(h, a.SetProvider(p))
	}
	n, err := a.ReloadSkills()
	if err != nil {
		h.Out(styleError, "reloaded config; skills error: "+err.Error())
		return nil
	}
	a.ReloadConfigDependentTools()
	if a.SkillsEnabled() {
		h.Out(styleSystem, fmt.Sprintf("reloaded: config, provider, %d skills (AGENTS.md on next message)", n))
	} else {
		h.Out(styleSystem, "reloaded: config, provider (AGENTS.md on next message)")
	}
	return nil
}

// cmdCost displays token/cost breakdown.
func cmdCost(h commandHost) {
	a := h.Agent()
	sid := h.SessionID()
	if _, err := a.Store().GetSession(sid); errors.Is(err, store.ErrNotFound) {
		h.Out(styleSystem, fmt.Sprintf("session not saved yet — send a message first\n  (ephemeral id: %s)", sid))
		return
	}
	cost, err := a.Store().GetSessionCost(sid)
	if err != nil {
		h.Out(styleError, "error reading cost: "+err.Error())
		return
	}
	breakdown, err := a.Store().GetSessionTokenBreakdown(sid)
	if err != nil {
		h.Out(styleError, "error reading token breakdown: "+err.Error())
		return
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Session %s:\n", sid))
	if breakdown.InputUnknownCalls > 0 {
		b.WriteString(fmt.Sprintf("  Input:  %d tokens + unknown (%d call(s))\n", breakdown.InputTokens, breakdown.InputUnknownCalls))
	} else {
		b.WriteString(fmt.Sprintf("  Input:  %d tokens\n", breakdown.InputTokens))
	}
	b.WriteString(fmt.Sprintf("  Output: %d tokens\n", breakdown.OutputTokens))
	if breakdown.CacheReadTokens > 0 {
		b.WriteString(fmt.Sprintf("  Cache read:  %d tokens\n", breakdown.CacheReadTokens))
	}
	if breakdown.CacheWriteTokens > 0 {
		b.WriteString(fmt.Sprintf("  Cache write: %d tokens\n", breakdown.CacheWriteTokens))
	}
	b.WriteString(fmt.Sprintf("  Calls:  %d\n", breakdown.CallCount))
	b.WriteString(fmt.Sprintf("  Cost:   $%.4f\n", cost))
	h.Out(styleSystem, b.String())
}

// cmdStatus prints basic session info plus the context files (AGENTS.md) and
// skills currently loaded into the system prompt for this session.
func cmdStatus(h commandHost) {
	a := h.Agent()
	sid := h.SessionID()

	// Resolve cwd + title from the session row (falls back to the process cwd
	// for an unsaved session, mirroring what the prompt builder would use).
	cwd := cwdOf()
	title := ""
	saved := false
	if sess, err := a.Store().GetSession(sid); err == nil {
		saved = true
		if sess.Cwd != "" {
			cwd = sess.Cwd
		}
		if sess.Title != nil {
			title = strings.TrimSpace(*sess.Title)
		}
	}

	var b strings.Builder
	head := "Session " + sid
	if title != "" {
		head += "  · " + title
	}
	if !saved {
		head += "  · unsaved (send a message to persist)"
	}
	b.WriteString(head + "\n")
	b.WriteString(fmt.Sprintf("  Model:    %s/%s\n", a.Provider().ID(), a.Model()))
	if eff := a.Effort(); eff != "" {
		b.WriteString(fmt.Sprintf("  Effort:   %s\n", eff))
	}
	b.WriteString(fmt.Sprintf("  Cwd:      %s\n", cwd))
	used, total := a.ContextTokens()
	if total > 0 {
		b.WriteString(fmt.Sprintf("  Context:  %s / %s tokens (%.1f%%)\n",
			formatNum(used), formatNum(total), float64(used)/float64(total)*100))
	} else {
		b.WriteString(fmt.Sprintf("  Context:  %s tokens\n", formatNum(used)))
	}
	if saved {
		if cost, err := a.Store().GetSessionCost(sid); err == nil {
			b.WriteString(fmt.Sprintf("  Cost:     $%.4f\n", cost))
		}
	}
	if names := a.ToolNames(); len(names) > 0 {
		b.WriteString(fmt.Sprintf("  Tools (%d): %s\n", len(names), strings.Join(names, ", ")))
	}

	// Context files (AGENTS.md / CLAUDE.md) that get injected each turn.
	files := a.LoadedContextFiles()
	b.WriteString(fmt.Sprintf("\nContext files (%d):\n", len(files)))
	if len(files) == 0 {
		b.WriteString("  (none)\n")
	}
	for _, cf := range files {
		b.WriteString(fmt.Sprintf("  %s  (%s)\n", cf.Path, humanBytes(len(cf.Content))))
	}

	// Skills loaded into the prompt.
	if !a.SkillsEnabled() {
		b.WriteString("\nSkills: (disabled)\n")
	} else {
		sk := a.Skills()
		b.WriteString(fmt.Sprintf("\nSkills (%d):\n", len(sk)))
		if len(sk) == 0 {
			b.WriteString("  (none)\n")
		}
		for _, s := range sk {
			desc := collapseWhitespace(s.Description)
			if len(desc) > 70 {
				desc = desc[:69] + "…"
			}
			if desc != "" {
				b.WriteString(fmt.Sprintf("  %s — %s\n", s.Name, desc))
			} else {
				b.WriteString(fmt.Sprintf("  %s\n", s.Name))
			}
		}
	}

	h.Out(styleSystem, strings.TrimRight(b.String(), "\n"))
}

// humanBytes formats a byte count as B/KB/MB.
func humanBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// cmdEffort sets the reasoning/effort level.
func cmdEffort(h commandHost, args []string) error {
	a := h.Agent()
	if len(args) == 0 {
		h.Out(styleSystem, fmt.Sprintf("current effort: %s", a.Effort()))
		return nil
	}
	level := strings.ToLower(args[0])
	switch level {
	case "low", "medium", "high", "xhigh", "max":
		a.SetEffort(level)
		h.Out(styleSystem, "effort: "+level)
	default:
		h.Out(styleError, "unknown effort level: "+level+" (low|medium|high|xhigh|max)")
	}
	return nil
}
