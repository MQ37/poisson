package tui

import (
	"fmt"
	"os"
	"strings"
	"time"

	"poisson/internal/agent"
	"poisson/internal/auth"
	"poisson/internal/config"
	"poisson/internal/provider"
	"poisson/internal/store"
)

// commandHost is the small interface shared by *TUI and *tuiV2 so the slash
// commands live in one place. The TUIs wrap themselves in this shape before
// dispatching.
type commandHost interface {
	Agent() *agent.Agent
	SessionID() string
	SetSessionID(string)
	Out(LineStyle, string)
}

// tuiCmdHost wraps *TUI.
type tuiCmdHost struct{ t *TUI }

func (h tuiCmdHost) Agent() *agent.Agent           { return h.t.agent }
func (h tuiCmdHost) SessionID() string             { return h.t.sessionID }
func (h tuiCmdHost) SetSessionID(id string)        { h.t.sessionID = id }
func (h tuiCmdHost) Out(style LineStyle, s string) { h.t.writeString(s + "\r\n") }

// v2CmdHost wraps *tuiV2.
type v2CmdHost struct{ t *tuiV2 }

func (h v2CmdHost) Agent() *agent.Agent    { return h.t.agent }
func (h v2CmdHost) SessionID() string      { return h.t.sessionID }
func (h v2CmdHost) SetSessionID(id string) { h.t.sessionID = id; h.t.status.SessionID = id }
func (h v2CmdHost) Out(style LineStyle, s string) {
	h.t.scroll.appendRaw(style, s)
	h.t.dirty.Store(true)
}

// cmdNew creates a new session and switches the agent to it.
func cmdNew(h commandHost) error {
	a := h.Agent()
	s := a.Store()
	id := store.NewSessionID()
	cwd, _ := os.Getwd()
	prov := a.Provider().ID()
	model := a.Model()
	if err := s.CreateSession(&store.Session{
		ID: id, Cwd: cwd, Provider: prov, Model: model,
		CreatedAt: time.Now().Unix(), UpdatedAt: time.Now().Unix(),
	}); err != nil {
		h.Out(styleError, "error creating session: "+err.Error())
		return nil
	}
	a.SwitchSession(id)
	h.SetSessionID(id)
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
	if sess.CompactionSummary != nil {
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
	if sess.CompactionSummary != nil {
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
	if sess == nil {
		h.Out(styleError, "session not found")
		return false
	}
	a := h.Agent()
	p := makeProvider(sess.Provider, a.Config())
	if p == nil {
		h.Out(styleError, "unknown provider in session: "+sess.Provider)
		return false
	}
	a.SwitchSession(sess.ID)
	a.SetProvider(p)
	a.SetModel(sess.Model)
	h.SetSessionID(sess.ID)
	return true
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
	for _, m := range msgs {
		if m.Seq == lastUserSeq && m.Compacted {
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
	sess, _ := s.GetSession(sid)
	if sess != nil && sess.CompactionSummary != nil {
		if remaining, _ := s.GetMessages(sid); len(remaining) == 0 {
			s.ClearCompactionSummary(sid)
		}
	}
	h.Out(styleSystem, fmt.Sprintf("undid last turn (%d messages soft-deleted)", count))
	return nil
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
			newProv = makeProvider(provName, a.Config())
			if newProv == nil {
				h.Out(styleError, "unknown provider: "+provName)
				return nil
			}
			a.SetProvider(newProv)
		}
		model := defaultModelFor(newProv, a.Config())
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
		newProv := makeProvider(provName, a.Config())
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
	if p := makeProvider(a.Provider().ID(), cfg); p != nil {
		a.SetProvider(p)
	}
	h.Out(styleSystem, "reloaded config")
	return nil
}

// cmdCost displays token/cost breakdown.
func cmdCost(h commandHost) {
	a := h.Agent()
	sid := h.SessionID()
	cost, _ := a.Store().GetSessionCost(sid)
	breakdown, _ := a.Store().GetSessionTokenBreakdown(sid)
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

// makeProvider creates a provider by name from the config.
func makeProvider(name string, cfg *config.Config) provider.Provider {
	authStore, _ := auth.Load()
	switch name {
	case "anthropic":
		return provider.NewAnthropicProvider(authStore, cfg)
	case "ollama":
		baseURL := cfg.Ollama.BaseURL
		if baseURL == "" {
			baseURL = "http://localhost:11434"
		}
		return provider.NewOllamaProvider(baseURL, cfg.Ollama.Model)
	case "xai":
		return provider.NewXAIProvider(authStore, cfg)
	default:
		return nil
	}
}

// defaultModelFor returns the configured default model name for a provider.
func defaultModelFor(p provider.Provider, cfg *config.Config) string {
	if cfg == nil || p == nil {
		return ""
	}
	switch p.ID() {
	case "ollama":
		return cfg.Ollama.Model
	case "anthropic":
		return cfg.Anthropic.Model
	case "xai":
		return cfg.XAI.Model
	default:
		return ""
	}
}
