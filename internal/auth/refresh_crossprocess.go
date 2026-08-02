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
// copy turns out to need refreshing too (i.e. no other process beat us to
// it). store[provider] is updated to match the result before returning,
// whether or not a refresh actually happened. Caller must already hold
// StoreMu (this only adds cross-process coordination on top of the
// in-process one).
func refreshCrossProcessLocked(store AuthStore, provider string, skewMs int64, currentEntry AuthEntry, doRefresh func(refreshToken string) (*AuthEntry, error)) (AuthEntry, error) {
	var result AuthEntry
	var refreshErr error
	lockErr := withLock(func() error {
		fresh, err := Load()
		if err != nil {
			return err
		}
		if fe, ok := fresh[provider]; ok && !IsExpired(fe, skewMs) {
			// Another process already refreshed (and saved) this entry
			// while we were waiting for the lock.
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
	return refreshCrossProcessLocked(store, provider, skewMs, entry, doRefresh)
}

// ForceRefresh is RefreshIfExpired without the "is my in-memory copy
// expired" gate — used reactively after a request comes back 401, where
// the stored token is known bad regardless of what its expiry field
// claims. Still checks the freshly-reloaded disk copy first (via the same
// cross-process path) in case another process already refreshed in the
// meantime, so this doesn't force a redundant — and, if the token has
// rotated, actively failing — refresh when a perfectly good one is already
// sitting in auth.json. Caller must hold StoreMu.
func ForceRefresh(store AuthStore, provider string, skewMs int64, doRefresh func(refreshToken string) (*AuthEntry, error)) (AuthEntry, error) {
	entry, ok := store[provider]
	if !ok {
		return AuthEntry{}, fmt.Errorf("no %s credentials", provider)
	}
	return refreshCrossProcessLocked(store, provider, skewMs, entry, doRefresh)
}
