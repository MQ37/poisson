package provider

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
)

// fakeSSEServer is a test HTTP server that returns a canned SSE response
// and captures the last request for header inspection.
type fakeSSEServer struct {
	*httptest.Server
	lastRequest *http.Request
}

func newFakeSSEServer(response string) *fakeSSEServer {
	f := &fakeSSEServer{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.lastRequest = r
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		io.Copy(w, strings.NewReader(response))
	}))
	return f
}
