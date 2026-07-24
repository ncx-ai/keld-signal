package daemon

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/enrichtest"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/sidecar"
)

// The adaptive cap is only worth computing if the daemon actually binds it to
// the backend for the job's inferences.
func TestBindMaxLenAppliesCapToSidecarRequests(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		_, _ = w.Write([]byte(`{"entities":[],"results":{}}`))
	}))
	defer srv.Close()

	m := bindMaxLen(sidecar.New(srv.URL, 5*time.Second), 1600)
	m.Classify("hi", map[string][]string{"task_type": {"a"}})

	if n, _ := body["max_len"].(float64); int(n) != 1600 {
		t.Fatalf("max_len = %v, want 1600", body["max_len"])
	}
}

// A non-positive cap means "no cap" and must leave the model untouched rather
// than sending a zero window that would truncate every prompt to nothing.
func TestBindMaxLenIgnoresNonPositiveCap(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &body)
		_, _ = w.Write([]byte(`{"entities":[],"results":{}}`))
	}))
	defer srv.Close()

	m := bindMaxLen(sidecar.New(srv.URL, 5*time.Second), 0)
	m.Classify("hi", map[string][]string{"task_type": {"a"}})

	if _, present := body["max_len"]; present {
		t.Fatalf("max_len must be absent for a zero cap, got %v", body["max_len"])
	}
}

// Backends that are not the sidecar client (test fakes, the eval harness) have
// no cap to bind and must pass through unchanged.
func TestBindMaxLenPassesThroughNonSidecarModel(t *testing.T) {
	fake := enrichtest.NewFake()
	if got := bindMaxLen(fake, 512); got != enrich.Model(fake) {
		t.Fatal("non-sidecar model must pass through unchanged")
	}
}

// Binding the cap must not drop the job context: losing it would leave a
// timed-out pass's inference running instead of aborting it.
func TestBindMaxLenPreservesJobContext(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer srv.Close()
	defer close(release)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	m := bindMaxLen(withJobCtx(sidecar.New(srv.URL, 30*time.Second), ctx), 512)

	done := make(chan struct{})
	go func() {
		m.Classify("hi", map[string][]string{"task_type": {"a"}})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("cap binding dropped the job context: call outlived it")
	}
}
