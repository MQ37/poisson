package auth

import "fmt"

// refreshCrossProcessLocked is the shared body of RefreshIfExpired and
// ForceRefresh: under the cross-process auth.json flock (withLock), it
// re-loads the store FRESH FROM DISK before deciding whether to refresh —
// not the caller's possibly-stale in-memory copy — so a process that lost
// a refresh race to another process (the parent px and a subagent child it
// spawned, two independent px instances, a concurrent `px login`) sees
// that winner's ALREADY-refreshed token and adopts it instead of blindly
// reusing its own stale refresh_token. This matters because several OAuth
// providers ROTATE refresh tokens on use: without this check, the loser's
// refresh call fails outright (invalid_grant) even though a perfectly
// valid, freshly-rotated token is sitting in auth.json moments away — and
// since the in-memory entry never gets updated on that failure, a
// subsequent 401-triggered force-refresh keeps reusing the same dead
// refresh_token and keeps failing identically forever.
//
// currentEntry.Refresh is only actually used if the freshly-loaded disk
// copy turns out to be the SAME entry we already hold (i.e. no other
// process beat us to refreshing it). store[provider] is updated to match
// the result before returning, whether or not a refresh actually happened.
// Caller must already hold StoreMu (this only adds cross-process
// coordination on top of the in-process one).
//
// "Someone else already refreshed it" is decided by VALUE — the disk copy
// differs from currentEntry — not by re-checking the disk copy's own
// expiry field. That distinction matters: ForceRefresh calls this
// specifically because the server just said, via a live 401, that
// currentEntry is dead RIGHT NOW — but currentEntry's own client-computed
// Expires timestamp can still be in the future at that moment (early
// server-side revocation, clock skew, or the access token's real TTL
// simply being shorter than the expires_in the token endpoint reported).
// An earlier version of this function used an expiry check here instead:
// it re-Loaded the disk copy — which, with no other process having
// refreshed anything, was byte-identical to the already-dead
// currentEntry — saw that its Expires field hadn't technically passed
// yet, and concluded "still fresh, no refresh needed", handing the
// caller back the exact same broken token with no error at all. The
// caller retried once with it, 401'd again, and gave up — surfacing as
// an occasional, seemingly random forced full re-login (`px login
// anthropic`) instead of the automatic recovery ForceRefresh exists to
// provide. Comparing by value instead is correct for both callers:
// RefreshIfExpired only ever reaches here after its OWN expiry check on
// currentEntry already said "needs refreshing", so an identical disk copy
// still needs refreshing too, regardless of its self-reported expiry.
func refreshCrossProcessLocked(store AuthStore, provider string, currentEntry AuthEntry, doRefresh func(refreshToken string) (*AuthEntry, error)) (AuthEntry, error) {
	var result AuthEntry
	var refreshErr error
	lockErr := withLock(func() error {
		fresh, err := Load()
		if err != nil {
			return err
		}
		if fe, ok := fresh[provider]; ok && fe != currentEntry {
			// Another process already refreshed (and saved) a genuinely
			// different entry while we were waiting for the lock.
			result = fe
			return nil
		}
		refreshed, err := doRefresh(currentEntry.Refresh)
		if err != nil {
			refreshErr = err
			result = currentEntry // stale but usable; caller decides how to handle refreshErr
			return nil
		}
		fresh[provider] = *refreshed
		if serr := Save(fresh); serr != nil {
			return serr
		}
		result = *refreshed
		return nil
	})
	if lockErr != nil {
		return currentEntry, lockErr
	}
	store[provider] = result
	return result, refreshErr
}

// RefreshIfExpired returns store[provider], proactively refreshing and
// persisting it first if it's within skewMs of expiring — cross-process
// safe (see refreshCrossProcessLocked's doc). Caller must hold StoreMu.
func RefreshIfExpired(store AuthStore, provider string, skewMs int64, doRefresh func(refreshToken string) (*AuthEntry, error)) (AuthEntry, error) {
	entry, ok := store[provider]
	if !ok {
		return AuthEntry{}, fmt.Errorf("no %s credentials", provider)
	}
	if !IsExpired(entry, skewMs) {
		return entry, nil
	}
	return refreshCrossProcessLocked(store, provider, entry, doRefresh)
}

// ForceRefresh is RefreshIfExpired without the "is my in-memory copy
// expired" gate — used reactively after a request comes back 401, where
// the stored token is known bad regardless of what its expiry field
// claims. Always actually attempts a refresh unless the freshly-reloaded
// disk copy has genuinely changed since currentEntry (see
// refreshCrossProcessLocked's doc for why that check is by value, not by
// re-checking expiry — using expiry here specifically would defeat the
// whole point of a FORCED refresh). Caller must hold StoreMu.
func ForceRefresh(store AuthStore, provider string, doRefresh func(refreshToken string) (*AuthEntry, error)) (AuthEntry, error) {
	entry, ok := store[provider]
	if !ok {
		return AuthEntry{}, fmt.Errorf("no %s credentials", provider)
	}
	return refreshCrossProcessLocked(store, provider, entry, doRefresh)
}
