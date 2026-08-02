package auth

import (
	"fmt"
	"sync"
	"testing"

	"github.com/mq37/poisson/internal/testutil"
)

// rotatingRefresher simulates an OAuth provider that ROTATES refresh tokens
// on every use (common in practice, and what makes the cross-process race
// actually dangerous rather than merely wasteful): calling it with a
// refresh token that has already been consumed once returns invalid_grant,
// exactly like a real token endpoint would.
type rotatingRefresher struct {
	mu     sync.Mutex
	used   map[string]bool
	nextID int
}

func (r *rotatingRefresher) refresh(refreshToken string) (*AuthEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.used == nil {
		r.used = map[string]bool{}
	}
	if r.used[refreshToken] {
		return nil, fmt.Errorf("invalid_grant: refresh token already used")
	}
	r.used[refreshToken] = true
	r.nextID++
	return &AuthEntry{
		Type:    "oauth",
		Access:  fmt.Sprintf("access-%d", r.nextID),
		Refresh: fmt.Sprintf("refresh-%d", r.nextID),
		Expires: farFutureMs,
	}, nil
}

const farFutureMs = 99999999999999

// TestRefreshIfExpired_CrossProcessLoserAdoptsWinnersToken reproduces the
// exact scout finding: two independent "processes" (simulated here as two
// separate in-memory AuthStore snapshots, each Load()ing/Save()ing through
// the same on-disk auth.json — exactly what a parent px and a subagent
// child process do) both see the same expired entry and race to refresh
// it against a provider that ROTATES refresh tokens. Before this fix, the
// loser would call doRefresh with its own now-consumed refresh_token and
// get invalid_grant, permanently failing that request even though a
// perfectly valid, freshly-rotated token was sitting in auth.json. After
// the fix, the loser's cross-process check (fresh Load() under the flock)
// sees the winner's already-refreshed, non-expired entry and adopts it
// instead of attempting its own doomed refresh at all.
func TestRefreshIfExpired_CrossProcessLoserAdoptsWinnersToken(t *testing.T) {
	testutil.TempHome(t)

	staleEntry := AuthEntry{Type: "oauth", Access: "stale-access", Refresh: "refresh-shared", Expires: 1}
	if err := Save(AuthStore{"anthropic": staleEntry}); err != nil {
		t.Fatalf("seed Save: %v", err)
	}

	refresher := &rotatingRefresher{}

	// "Process A": Loads its own snapshot first.
	storeA, err := Load()
	if err != nil {
		t.Fatalf("Load A: %v", err)
	}
	// "Process B": Loads its own, separate snapshot (same on-disk state).
	storeB, err := Load()
	if err != nil {
		t.Fatalf("Load B: %v", err)
	}

	// Process A refreshes first and wins — its call to RefreshIfExpired
	// takes the flock, sees no fresher entry on disk yet, so it actually
	// calls doRefresh and persists the result.
	gotA, errA := RefreshIfExpired(storeA, "anthropic", 5*60*1000, refresher.refresh)
	if errA != nil {
		t.Fatalf("process A refresh: %v", errA)
	}
	if gotA.Access != "access-1" {
		t.Fatalf("process A access = %q, want access-1", gotA.Access)
	}

	// Process B refreshes second, using ITS OWN stale in-memory snapshot
	// (storeB["anthropic"].Refresh == the same original "refresh-shared"
	// process A already consumed) — the exact race condition. Without the
	// fix this would call refresher.refresh("refresh-shared") again and
	// get invalid_grant. With the fix, B's cross-process check re-Loads
	// fresh from disk under the flock, sees A's already-refreshed
	// non-expired entry, and adopts it instead.
	gotB, errB := RefreshIfExpired(storeB, "anthropic", 5*60*1000, refresher.refresh)
	if errB != nil {
		t.Fatalf("process B refresh: %v (this is exactly the bug: B raced its own stale refresh_token instead of adopting A's already-refreshed token)", errB)
	}
	if gotB.Access != gotA.Access {
		t.Errorf("process B access = %q, want it to match process A's winning refresh (%q)", gotB.Access, gotA.Access)
	}

	// Only ONE real refresh call should have ever happened — B must not
	// have triggered a second, wasted (and here, failing) refresh.
	if refresher.nextID != 1 {
		t.Errorf("refresher.nextID = %d, want exactly 1 (process B should have adopted A's result, not refreshed again)", refresher.nextID)
	}

	// storeB's in-memory copy must also be updated to match, so a caller
	// that reads storeB["anthropic"] afterward (e.g. the next request on
	// process B) sees the fresh token too, not the stale one it started with.
	if storeB["anthropic"].Access != gotA.Access {
		t.Errorf("storeB[\"anthropic\"].Access = %q, want it updated to %q", storeB["anthropic"].Access, gotA.Access)
	}
}

// TestForceRefresh_AlwaysRefreshesEvenWhenNotYetExpiredByClock reproduces a
// real regression this fix caught in itself: ForceRefresh's whole purpose
// is "the server just returned a live 401, refresh regardless of what the
// stored entry's own Expires field claims" (server-side early revocation,
// clock skew, or the access token's real TTL simply being shorter than
// expires_in reported). An earlier version of refreshCrossProcessLocked
// decided "someone else already refreshed it" by re-checking the disk
// copy's OWN expiry instead of comparing it by value against the entry
// the caller already knows is dead — so when no other process had
// actually refreshed anything (disk still held the exact same,
// not-yet-"expired"-by-the-clock entry), it wrongly concluded "still
// fresh" and handed back the same broken token with no error and no real
// refresh call at all. The caller retried once with it, got 401 again,
// and gave up — surfacing as an occasional forced full re-login instead
// of the automatic recovery ForceRefresh exists to provide.
func TestForceRefresh_AlwaysRefreshesEvenWhenNotYetExpiredByClock(t *testing.T) {
	testutil.TempHome(t)

	// The stored entry is NOT expired by its own Expires field (far future)
	// — exactly what happens when the server revokes early or the real TTL
	// is shorter than what expires_in claimed, yet the caller still got a
	// live 401 on it right now.
	notYetExpired := AuthEntry{Type: "oauth", Access: "dead-but-not-expired", Refresh: "refresh-dead", Expires: farFutureMs}
	if err := Save(AuthStore{"anthropic": notYetExpired}); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	store := AuthStore{"anthropic": notYetExpired}

	refresher := &rotatingRefresher{}
	got, err := ForceRefresh(store, "anthropic", refresher.refresh)
	if err != nil {
		t.Fatalf("ForceRefresh: %v", err)
	}
	if got.Access == "dead-but-not-expired" {
		t.Fatal("ForceRefresh returned the same known-bad token unchanged — it must always actually attempt a refresh, not skip based on the entry's own (unreliable, since we just got a live 401 on it) expiry field")
	}
	if refresher.nextID != 1 {
		t.Errorf("refresher.nextID = %d, want exactly 1 (ForceRefresh must call doRefresh here — disk holds the identical, not-actually-refreshed-by-anyone entry)", refresher.nextID)
	}
	if got.Access != "access-1" {
		t.Errorf("access = %q, want access-1 (the real refresh result)", got.Access)
	}
}

// TestForceRefresh_CrossProcessAdoptsAlreadyFreshEntry proves ForceRefresh
// (the post-401 reactive path) has the same protection: if another process
// already refreshed the entry (so it's no longer actually expired) by the
// time this one gets the flock, it must adopt that result instead of
// forcing a redundant refresh with a stale refresh_token.
func TestForceRefresh_CrossProcessAdoptsAlreadyFreshEntry(t *testing.T) {
	testutil.TempHome(t)

	// Disk already holds a FRESH entry (as if another process just won a
	// race and saved) — this process's own in-memory copy is what's stale
	// (e.g. it read the 401 based on an old cached Access token).
	if err := Save(AuthStore{"anthropic": {Type: "oauth", Access: "fresh-from-other-process", Refresh: "refresh-fresh", Expires: farFutureMs}}); err != nil {
		t.Fatalf("seed Save: %v", err)
	}
	staleLocal := AuthStore{"anthropic": {Type: "oauth", Access: "stale-local", Refresh: "refresh-shared-stale", Expires: 1}}

	refresher := &rotatingRefresher{}
	got, err := ForceRefresh(staleLocal, "anthropic", refresher.refresh)
	if err != nil {
		t.Fatalf("ForceRefresh: %v", err)
	}
	if got.Access != "fresh-from-other-process" {
		t.Errorf("access = %q, want the already-fresh disk entry adopted instead of forcing a new refresh", got.Access)
	}
	if refresher.nextID != 0 {
		t.Errorf("refresher.nextID = %d, want 0 (no refresh call should have been made)", refresher.nextID)
	}
}
