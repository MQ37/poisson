package tui

import (
	"fmt"
	"strings"
	"time"

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
	h.Out(styleSystem, fmt.Sprintf("resumed session: %s (%s/%s)", sess.ID, sess.Provider, sess.Model))
	return nil
}

// cmdSessions lists recent sessions.
func cmdSessions(h commandHost) {
	a := h.Agent()
	sessions, err := a.Store().ListSessions(20, 0)
	if err != nil {
		h.Out(styleError, "error listing sessions: "+err.Error())
		return
	}
	if len(sessions) == 0 {
		h.Out(styleSystem, "no sessions")
		return
	}
	var b strings.Builder
	for _, sess := range sessions {
		msgCount := 0
		if msgs, err := a.Store().GetMessages(sess.ID); err == nil {
			msgCount = len(msgs)
		}
		date := time.Unix(sess.CreatedAt, 0).Format("2006-01-02")
		marker := " "
		if sess.ID == h.SessionID() {
			marker = ">"
		}
		b.WriteString(fmt.Sprintf("%s %s  %s  %d msgs  %s/%s\n",
			marker, sess.ID, date, msgCount, sess.Provider, sess.Model))
	}
	h.Out(styleSystem, b.String())
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

// cmdFork forks the current session up to a given sequence.
func cmdFork(h commandHost, args []string) error {
	a := h.Agent()
	s := a.Store()
	srcID := h.SessionID()
	if len(args) == 0 {
		return forkFromLatest(h)
	}
	var upToSeq int
	if n, err := fmt.Sscanf(args[0], "%d", &upToSeq); n != 1 || err != nil || upToSeq < 0 {
		h.Out(styleSystem, "usage: /fork [message-seq]")
		return nil
	}
	sess, err := s.GetSession(srcID)
	if err != nil {
		h.Out(styleError, "error: cannot get current session")
		return nil
	}
	newID := store.NewSessionID()
	if err := s.CreateSession(&store.Session{
		ID:        newID,
		ParentID:  &srcID,
		ForkPoint: &args[0],
		Cwd:       sess.Cwd,
		Provider:  sess.Provider,
		Model:     sess.Model,
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}); err != nil {
		h.Out(styleError, "error creating fork: "+err.Error())
		return nil
	}
	if err := s.CloneMessages(srcID, upToSeq, newID); err != nil {
		h.Out(styleError, "error cloning messages: "+err.Error())
		return nil
	}
	if sess.CompactionSummary != nil && strings.TrimSpace(*sess.CompactionSummary) != "" {
		s.SetCompactionSummary(newID, *sess.CompactionSummary)
	}
	newSess, _ := s.GetSession(newID)
	if !switchAgentToSession(h, newSess) {
		return nil
	}
	h.Out(styleSystem, fmt.Sprintf("forked to new session: %s (%s/%s)", newID, newSess.Provider, newSess.Model))
	return nil
}

// forkFromLatest forks up to the most recent message.
func forkFromLatest(h commandHost) error {
	a := h.Agent()
	s := a.Store()
	srcID := h.SessionID()
	msgs, err := s.GetMessages(srcID)
	if err != nil {
		h.Out(styleError, "error getting messages")
		return nil
	}
	if len(msgs) == 0 {
		h.Out(styleSystem, "nothing to fork (session is empty)")
		return nil
	}
	lastSeq := msgs[len(msgs)-1].Seq
	newID := store.NewSessionID()
	sess, err := s.GetSession(srcID)
	if err != nil {
		h.Out(styleError, "error getting current session")
		return nil
	}
	if err := s.CreateSession(&store.Session{
		ID:        newID,
		ParentID:  &srcID,
		ForkPoint: &msgs[len(msgs)-1].ID,
		Cwd:       sess.Cwd,
		Provider:  sess.Provider,
		Model:     sess.Model,
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}); err != nil {
		h.Out(styleError, "error creating fork: "+err.Error())
		return nil
	}
	if err := s.CloneMessages(srcID, lastSeq, newID); err != nil {
		h.Out(styleError, "error cloning messages: "+err.Error())
		return nil
	}
	if sess.CompactionSummary != nil && strings.TrimSpace(*sess.CompactionSummary) != "" {
		s.SetCompactionSummary(newID, *sess.CompactionSummary)
	}
	newSess, _ := s.GetSession(newID)
	if !switchAgentToSession(h, newSess) {
		return nil
	}
	h.Out(styleSystem, fmt.Sprintf("forked to new session: %s (%s/%s)", newID, newSess.Provider, newSess.Model))
	return nil
}

func switchAgentToSession(h commandHost, sess *store.Session) bool {
	if th, ok := h.(tuiCmdHost); ok && th.t.running() {
		h.Out(styleError, "cannot switch session while agent is running")
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
	a.SetProvider(p)
	a.SetModel(sess.Model)
	h.SetSessionID(sess.ID)
	resetHostSessionView(h)
	return true
}

func resetHostSessionView(h commandHost) {
	if th, ok := h.(tuiCmdHost); ok {
		th.t.resetSessionViewLocked()
	}
}

// cmdUndo soft-deletes the last user turn.
func cmdUndo(h commandHost) error {
	a := h.Agent()
	s := a.Store()
	sid := h.SessionID()
	msgs, err := s.GetMessages(sid)
	if err != nil {
		h.Out(styleError, "error: "+err.Error())
		return nil
	}
	var lastUserSeq int = -1
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			lastUserSeq = msgs[i].Seq
			break
		}
	}
	if lastUserSeq == -1 {
		h.Out(styleSystem, "no user message to undo")
		return nil
	}
	sess, _ := s.GetSession(sid)
	if sess != nil && sess.CompactionSummary != nil && strings.TrimSpace(*sess.CompactionSummary) != "" {
		userTurns := 0
		for _, m := range msgs {
			if m.Role == "user" {
				userTurns++
			}
		}
		if userTurns <= 1 {
			h.Out(styleError, "cannot undo past compaction point. Use /fork before compacting.")
			return nil
		}
	}
	count := 0
	for _, m := range msgs {
		if m.Seq >= lastUserSeq {
			count++
		}
	}
	if err := s.SoftDeleteMessages(sid, lastUserSeq); err != nil {
		h.Out(styleError, "error undoing: "+err.Error())
		return nil
	}
	if sess != nil && sess.CompactionSummary != nil {
		if remaining, _ := s.GetMessages(sid); len(remaining) == 0 {
			s.ClearCompactionSummary(sid)
		}
	}
	refreshHostScrollback(h)
	h.Out(styleSystem, fmt.Sprintf("undid last turn (%d messages soft-deleted)", count))
	return nil
}

func refreshHostScrollback(h commandHost) {
	if th, ok := h.(tuiCmdHost); ok {
		th.t.refreshScrollbackFromStoreLocked()
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
			a.SetProvider(newProv)
		}
		model := provider.DefaultModel(provName, a.Config())
		if model == "" {
			h.Out(styleError, "no default model configured for provider: "+provName)
			return nil
		}
		a.SetModel(model)
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
		a.SetProvider(newProv)
	}
	a.SetModel(modelName)
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
		a.SetProvider(p)
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

// cmdProviders lists available providers.
func cmdProviders(h commandHost) {
	var b strings.Builder
	b.WriteString("Providers:\n")
	b.WriteString("  anthropic  Anthropic Claude (API key or OAuth stealth)\n")
	b.WriteString("  ollama     Local Ollama instance\n")
	b.WriteString("  xai        xAI Grok (SuperGrok OAuth)\n")
	b.WriteString(fmt.Sprintf("  current:   %s/%s\n", h.Agent().Provider().ID(), h.Agent().Model()))
	h.Out(styleSystem, b.String())
}

// cmdModels lists models for the current provider.
func cmdModels(h commandHost) error {
	a := h.Agent()
	models, err := a.Provider().Models()
	if err != nil {
		h.Out(styleError, "error listing models: "+err.Error())
		return nil
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Models (%s):\n", a.Provider().ID()))
	for _, m := range models {
		b.WriteString(fmt.Sprintf("  %s  ctx=%d  %s\n", m.ID, m.ContextWindow, m.Name))
	}
	h.Out(styleSystem, b.String())
	return nil
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


