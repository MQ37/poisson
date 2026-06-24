package tui

import (
	"fmt"
	"os"
	"strings"
	"time"

	"poisson/internal/auth"
	"poisson/internal/config"
	"poisson/internal/provider"
	"poisson/internal/store"
)

// generateID produces a short unique ID for sessions.
func generateID() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

// --- /new ---

func (t *TUI) cmdNew() error {
	s := t.agent.Store()
	id := generateID()
	cwd, _ := os.Getwd()
	prov := t.agent.Provider().ID()
	model := t.agent.Config().Provider.Default

	if err := s.CreateSession(&store.Session{
		ID:        id,
		Cwd:       cwd,
		Provider:  prov,
		Model:     model,
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	}); err != nil {
		t.writeString("error creating session: " + err.Error() + "\r\n")
		return nil
	}
	t.agent.SwitchSession(id)
	t.sessionID = id
	t.writeString("new session: " + id + "\r\n")
	return nil
}

// --- /resume <id> ---

func (t *TUI) cmdResume(args []string) error {
	if len(args) == 0 {
		t.writeString("usage: /resume <session-id>\r\n")
		return nil
	}
	id := args[0]
	sess, err := t.agent.Store().GetSession(id)
	if err != nil {
		t.writeString("session not found: " + id + "\r\n")
		return nil
	}
	t.agent.SwitchSession(sess.ID)
	t.sessionID = sess.ID
	t.writeString("resumed session: " + sess.ID + "\r\n")
	return nil
}

// --- /sessions ---

func (t *TUI) cmdSessions() error {
	sessions, err := t.agent.Store().ListSessions(20, 0)
	if err != nil {
		t.writeString("error listing sessions: " + err.Error() + "\r\n")
		return nil
	}
	if len(sessions) == 0 {
		t.writeString("no sessions\r\n")
		return nil
	}
	var b strings.Builder
	for _, sess := range sessions {
		msgCount := 0
		msgs, err := t.agent.Store().GetMessages(sess.ID)
		if err == nil {
			msgCount = len(msgs)
		}
		date := time.Unix(sess.CreatedAt, 0).Format("2006-01-02")
		marker := " "
		if sess.ID == t.sessionID {
			marker = ">"
		}
		b.WriteString(fmt.Sprintf("%s %s  %s  %d msgs  %s/%s\r\n",
			marker, shortID(sess.ID), date, msgCount, sess.Provider, sess.Model))
	}
	t.writeString(b.String())
	return nil
}

// --- /search <query> ---

func (t *TUI) cmdSearch(args []string) error {
	if len(args) == 0 {
		t.writeString("usage: /search <query>\r\n")
		return nil
	}
	query := strings.Join(args, " ")
	results, err := t.agent.Store().Search(query, 20)
	if err != nil {
		t.writeString("search error: " + err.Error() + "\r\n")
		return nil
	}
	if len(results) == 0 {
		t.writeString("no results\r\n")
		return nil
	}
	var b strings.Builder
	for _, r := range results {
		b.WriteString(fmt.Sprintf("  [%s] %s: %s\r\n",
			shortID(r.SessionID), r.Role, r.Snippet))
	}
	t.writeString(b.String())
	return nil
}

// --- /fork [seq] ---

func (t *TUI) cmdFork(args []string) error {
	s := t.agent.Store()
	srcID := t.sessionID

	// If no arg, fork from the latest message.
	if len(args) == 0 {
		return t.forkFromLatest()
	}

	// Try to parse as seq number.
	var upToSeq int
	fmt.Sscanf(args[0], "%d", &upToSeq)

	// Create the new session.
	newID := generateID()
	sess, err := s.GetSession(srcID)
	if err != nil {
		t.writeString("error: cannot get current session\r\n")
		return nil
	}
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
		t.writeString("error creating fork: " + err.Error() + "\r\n")
		return nil
	}

	if err := s.CloneMessages(srcID, upToSeq, newID); err != nil {
		t.writeString("error cloning messages: " + err.Error() + "\r\n")
		return nil
	}

	// Copy compaction summary if fork point is after compaction.
	if sess.CompactionSummary != nil {
		s.SetCompactionSummary(newID, *sess.CompactionSummary)
	}

	t.agent.SwitchSession(newID)
	t.sessionID = newID
	t.writeString("forked to new session: " + newID + "\r\n")
	return nil
}

func (t *TUI) forkFromLatest() error {
	s := t.agent.Store()
	srcID := t.sessionID

	msgs, err := s.GetMessages(srcID)
	if err != nil {
		t.writeString("error getting messages\r\n")
		return nil
	}
	if len(msgs) == 0 {
		t.writeString("nothing to fork (session is empty)\r\n")
		return nil
	}

	lastSeq := msgs[len(msgs)-1].Seq
	newID := generateID()
	sess, _ := s.GetSession(srcID)
	s.CreateSession(&store.Session{
		ID:        newID,
		ParentID:  &srcID,
		ForkPoint: &msgs[len(msgs)-1].ID,
		Cwd:       sess.Cwd,
		Provider:  sess.Provider,
		Model:     sess.Model,
		CreatedAt: time.Now().Unix(),
		UpdatedAt: time.Now().Unix(),
	})
	s.CloneMessages(srcID, lastSeq, newID)
	if sess.CompactionSummary != nil {
		s.SetCompactionSummary(newID, *sess.CompactionSummary)
	}
	t.agent.SwitchSession(newID)
	t.sessionID = newID
	t.writeString("forked to new session: " + newID + "\r\n")
	return nil
}

// --- /undo ---

func (t *TUI) cmdUndo() error {
	s := t.agent.Store()
	sid := t.sessionID

	msgs, err := s.GetMessages(sid)
	if err != nil {
		t.writeString("error: " + err.Error() + "\r\n")
		return nil
	}

	// Find the last user message.
	var lastUserSeq int = -1
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			lastUserSeq = msgs[i].Seq
			break
		}
	}
	if lastUserSeq == -1 {
		t.writeString("no user message to undo\r\n")
		return nil
	}

	// Check if there's a compaction boundary we can't cross.
	for _, m := range msgs {
		if m.Seq == lastUserSeq && m.Compacted {
			t.WriteString("cannot undo past compaction point. Use /fork before compacting.\r\n")
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
		t.writeString("error undoing: " + err.Error() + "\r\n")
		return nil
	}

	// Clear compaction summary if the undo removed it.
	sess, _ := s.GetSession(sid)
	if sess != nil && sess.CompactionSummary != nil {
		// Check if the last user message was before the compaction summary.
		// If so, the compaction summary is still valid. If after, clear it.
		// Simple heuristic: if we undid everything, clear it.
		remaining, _ := s.GetMessages(sid)
		if len(remaining) == 0 {
			s.ClearCompactionSummary(sid)
		}
	}

	t.writeString(fmt.Sprintf("undid last turn (%d messages soft-deleted)\r\n", count))
	return nil
}

// --- /model <provider/model> ---

func (t *TUI) cmdModel(args []string) error {
	if len(args) == 0 {
		t.writeString("usage: /model <provider/model> or /model <model>\r\n")
		return nil
	}
	input := args[0]

	var provName, modelName string
	if idx := strings.Index(input, "/"); idx >= 0 {
		provName = input[:idx]
		modelName = input[idx+1:]
	} else {
		modelName = input
		provName = t.agent.Provider().ID()
	}

	// Switch provider if needed.
	if provName != t.agent.Provider().ID() {
		newProv := makeProvider(provName, t.agent.Config())
		if newProv == nil {
			t.writeString("unknown provider: " + provName + "\r\n")
			return nil
		}
		t.agent.SetProvider(newProv)
	}

	// Update session.
	sess, _ := t.agent.Store().GetSession(t.sessionID)
	if sess != nil {
		sess.Provider = provName
		sess.Model = modelName
		t.agent.Store().UpdateSession(sess)
	}
	t.writeString(fmt.Sprintf("switched to %s/%s\r\n", provName, modelName))
	return nil
}

// --- /reload ---

func (t *TUI) cmdReload() error {
	cfg, err := config.Load()
	if err != nil {
		t.writeString("error loading config: " + err.Error() + "\r\n")
		return nil
	}
	t.agent.SetConfig(cfg)
	t.writeString("reloaded config\r\n")
	return nil
}

// --- /cost ---

func (t *TUI) cmdCost() error {
	s := t.agent.Store()
	sid := t.sessionID

	breakdown, err := s.GetSessionTokenBreakdown(sid)
	if err != nil {
		t.writeString("error: " + err.Error() + "\r\n")
		return nil
	}
	cost, _ := s.GetSessionCost(sid)

	var b strings.Builder
	b.WriteString(fmt.Sprintf("Input:  %s tokens\r\n", formatNum(breakdown.InputTokens)))
	b.WriteString(fmt.Sprintf("Output: %s tokens\r\n", formatNum(breakdown.OutputTokens)))
	if breakdown.CacheReadTokens > 0 {
		b.WriteString(fmt.Sprintf("Cache read:  %s tokens\r\n", formatNum(breakdown.CacheReadTokens)))
	}
	if breakdown.CacheWriteTokens > 0 {
		b.WriteString(fmt.Sprintf("Cache write: %s tokens\r\n", formatNum(breakdown.CacheWriteTokens)))
	}
	b.WriteString(fmt.Sprintf("Calls:  %d\r\n", breakdown.CallCount))
	b.WriteString(fmt.Sprintf("Cost:   $%.4f\r\n", cost))
	t.writeString(b.String())
	return nil
}

// WriteString is an exported wrapper for testing.
func (t *TUI) WriteString(s string) {
	t.writeString(s)
}

// --- /providers ---

func (t *TUI) cmdProviders() error {
	var b strings.Builder
	b.WriteString("Providers:\r\n")
	b.WriteString("  anthropic  Anthropic Claude (API key or OAuth stealth)\r\n")
	b.WriteString("  ollama     Local Ollama instance\r\n")
	b.WriteString("  xai        xAI Grok (SuperGrok OAuth)\r\n")
	b.WriteString(fmt.Sprintf("  current:   %s/%s\r\n", t.agent.Provider().ID(), t.currentModel()))
	t.writeString(b.String())
	return nil
}

// --- /models ---

func (t *TUI) cmdModels() error {
	prov := t.agent.Provider()
	models, err := prov.Models()
	if err != nil {
		t.writeString("error listing models: " + err.Error() + "\r\n")
		return nil
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Models (%s):\r\n", prov.ID()))
	for _, m := range models {
		b.WriteString(fmt.Sprintf("  %s  ctx=%d  %s\r\n", m.ID, m.ContextWindow, m.Name))
	}
	t.writeString(b.String())
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

// currentModel returns the model name from the current session.
func (t *TUI) currentModel() string {
	sess, _ := t.agent.Store().GetSession(t.sessionID)
	if sess != nil {
		return sess.Model
	}
	return ""
}

// --- /effort <level> ---

func (t *TUI) cmdEffort(args []string) error {
	if len(args) == 0 {
		t.writeString("usage: /effort <low|medium|high|xhigh|max>\r\n")
		return nil
	}
	level := args[0]
	valid := map[string]bool{"low": true, "medium": true, "high": true, "xhigh": true, "max": true}
	if !valid[level] {
		t.writeString("invalid effort level: " + level + " (use: low, medium, high, xhigh, max)\r\n")
		return nil
	}
	t.agent.SetEffort(level)
	t.writeString("effort set to: " + level + "\r\n")
	return nil
}
