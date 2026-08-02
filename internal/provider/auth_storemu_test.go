package provider

import (
	"context"
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/mq37/poisson/internal/auth"
)

// TestAnthropicSharesAuthStoreMu proves AnthropicProvider's own auth-map
// access (setHeaders, a pure read with no network call) blocks on
// auth.StoreMu — the same process-wide lock auth.EnsureXAIFresh/
// ForceRefreshXAI use — rather than some private-to-this-struct mutex. See
// auth.StoreMu's doc comment for the concurrent-map-write crash this
// prevents.
func TestAnthropicSharesAuthStoreMu(t *testing.T) {
	store := auth.AuthStore{"anthropic": {Type: "oauth", Access: "tok"}}
	p := NewAnthropicProvider(store, nil)

	auth.StoreMu.Lock()
	done := make(chan struct{})
	go func() {
		req := httptest.NewRequest("POST", "http://example.com", nil)
		p.setHeaders(req, true, false, 0, "req-id")
		close(done)
	}()

	select {
	case <-done:
		auth.StoreMu.Unlock()
		t.Fatal("setHeaders returned before auth.StoreMu was released — AnthropicProvider isn't using the shared lock")
	case <-time.After(150 * time.Millisecond):
		// Still blocked, as expected: setHeaders is waiting on the lock we hold.
	}
	auth.StoreMu.Unlock()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("setHeaders never completed after auth.StoreMu was released")
	}
}

// TestOpenAISharesAuthStoreMu is the same proof for OpenAIProvider. An empty
// store makes streamWithRetry return its "no OpenAI credentials" error
// immediately after releasing the lock, with no network call at all — the
// lock acquisition itself, unconditional before that check, is what's
// under test.
func TestOpenAISharesAuthStoreMu(t *testing.T) {
	store := auth.AuthStore{}
	p := NewOpenAIProvider(store, nil)

	auth.StoreMu.Lock()
	done := make(chan struct{})
	go func() {
		_, _ = p.streamWithRetry(context.Background(), &Request{}, 0)
		close(done)
	}()

	select {
	case <-done:
		auth.StoreMu.Unlock()
		t.Fatal("streamWithRetry proceeded before auth.StoreMu was released — OpenAIProvider isn't using the shared lock")
	case <-time.After(150 * time.Millisecond):
	}
	auth.StoreMu.Unlock()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("streamWithRetry never completed after auth.StoreMu was released")
	}
}

// TestAuthStoreMuSerializesConcurrentWritesAcrossProviders is a stress test
// (run with -race) hammering the same shared AuthStore map from three
// concurrent goroutines — one writing "xai" directly under auth.StoreMu
// (standing in for auth.EnsureXAIFresh/ForceRefreshXAI), one repeatedly
// calling AnthropicProvider's locked read path, one repeatedly calling
// OpenAIProvider's locked path — reproducing the exact shape of the
// scouted bug (different mutexes touching the same map) without needing
// any real network refresh. Before the fix (three separate mutexes) this
// reliably trips `go test -race` or Go's own "fatal error: concurrent map
// writes"; after it, every access is serialized through one lock.
func TestAuthStoreMuSerializesConcurrentWritesAcrossProviders(t *testing.T) {
	store := auth.AuthStore{"anthropic": {Type: "oauth", Access: "a"}}
	ap := NewAnthropicProvider(store, nil)
	op := NewOpenAIProvider(auth.AuthStore{}, nil)
	req := httptest.NewRequest("POST", "http://example.com", nil)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(3)

	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			auth.StoreMu.Lock()
			store["xai"] = auth.AuthEntry{Access: fmt.Sprintf("tok-%d", i)}
			auth.StoreMu.Unlock()
		}
	}()
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			ap.setHeaders(req, false, false, 0, "id")
		}
	}()
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			_, _ = op.streamWithRetry(context.Background(), &Request{}, 0)
		}
	}()

	time.Sleep(200 * time.Millisecond)
	close(stop)
	wg.Wait()
}
