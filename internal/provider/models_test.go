package provider

import "testing"

func TestCuratedModels(t *testing.T) {
	got := CuratedModels("ollama")
	byID := map[string]int{}
	for _, m := range got {
		byID[m.ID] = m.ContextWindow
	}
	for id, wantCtx := range map[string]int{
		"glm-5.2:cloud":        976000,
		"minimax-m3:cloud":     512000,
		"kimi-k2.7-code:cloud": 256000,
	} {
		if byID[id] != wantCtx {
			t.Errorf("CuratedModels(ollama)[%s] ctx=%d, want %d (all: %v)", id, byID[id], wantCtx, byID)
		}
	}
	// Sorted by ID.
	for i := 1; i < len(got); i++ {
		if got[i-1].ID > got[i].ID {
			t.Errorf("not sorted: %q before %q", got[i-1].ID, got[i].ID)
		}
	}
	// Uncurated provider falls through to empty.
	if len(CuratedModels("nope")) != 0 {
		t.Error("unknown provider should have no curated models")
	}
}
