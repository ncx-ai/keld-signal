package sidecar_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/sidecar"
)

// The pipeline gives one pass its own deadline by type-asserting the backend to
// enrich.ContextModel. If the client stops satisfying that interface, pass
// deadlines silently stop aborting in-flight inference and degrade to merely
// abandoning it — the leak that saturates the single-flight sidecar.
func TestClientSatisfiesContextModel(t *testing.T) {
	var _ enrich.ContextModel = sidecar.New("http://127.0.0.1:1", time.Second)
}

// A pass deadline must actually abort the in-flight request, not wait for the
// sidecar to answer.
func TestWithModelContextAbortsInFlightCall(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // hold the request open until the test is done
	}))
	defer srv.Close()
	defer close(release)

	c := sidecar.New(srv.URL, 30*time.Second)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	bound := c.WithModelContext(ctx)

	done := make(chan struct{})
	go func() {
		bound.Classify("hello", map[string][]string{"task_type": {"a", "b"}})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Classify did not return after its bound context expired")
	}
}
