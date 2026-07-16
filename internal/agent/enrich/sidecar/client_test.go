package sidecar

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

func stub(t *testing.T) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/extract", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"entities":[{"text":"a@b.com","label":"email","start":5,"end":12,"confidence":1.0}],"results":{"task_type":[{"label":"codegen","confidence":0.9}]}}`))
	})
	return httptest.NewServer(mux)
}

func TestExtractReturnsRawEntities(t *testing.T) {
	s := stub(t)
	defer s.Close()
	c := New(s.URL, 5*time.Second)
	res := c.Extract("email a@b.com", map[string]string{"email": "Email addresses"}, map[string][]string{"task_type": {"codegen"}})
	if len(res.Entities) != 1 {
		t.Fatalf("entities = %d, want 1", len(res.Entities))
	}
	e := res.Entities[0]
	if e.Text != "a@b.com" || e.Masked != "" { // RAW; masking is the pipeline's job
		t.Fatalf("want raw text unmasked, got Text=%q Masked=%q", e.Text, e.Masked)
	}
	if e.Start != 5 || e.End != 12 || e.Label != "email" {
		t.Fatalf("bad span: %+v", e)
	}
	if r := res.Results["task_type"]; len(r) != 1 || r[0].Label != "codegen" {
		t.Fatalf("bad results: %+v", res.Results)
	}
}

func TestHealthy(t *testing.T) {
	s := stub(t)
	defer s.Close()
	c := New(s.URL, time.Second)
	if !c.Healthy(context.Background()) {
		t.Fatal("stub should be healthy")
	}
	c2 := New("http://127.0.0.1:1", 200*time.Millisecond) // nothing listening
	if c2.Healthy(context.Background()) {
		t.Fatal("unreachable sidecar must be unhealthy")
	}
}

func TestClassifyRetriesThrough503ThenSucceeds(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls <= 2 { // simulate idle-evicted sidecar: 503 twice, then loaded
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(`{"results":{"task_type":[{"label":"codegen","confidence":0.9}]}}`))
	}))
	defer srv.Close()

	c := New(srv.URL, 2*time.Second)
	got := c.Classify("x", map[string][]string{"task_type": {"codegen"}})
	if got["task_type"][0].Label != "codegen" {
		t.Fatalf("expected retry through 503 to succeed on GLiNER2, got %+v (calls=%d)", got, calls)
	}
	if calls < 3 {
		t.Fatalf("expected >=3 calls (2 x 503 + success), got %d", calls)
	}
}

// TestWithContextCancelAbortsInFlight is the anti-wedge keystone: a per-job
// context (bound via WithContext) must abort an in-flight sidecar call the
// moment it is cancelled — not merely stop the retry backoff, and not wait out
// the http.Client timeout. Before the fix, postOnce used c.hc.Post (request not
// bound to c.ctx), so a slow/hung call ran to the 5s http timeout and the
// per-job deadline could not reclaim it — leaking work that kept retrying.
func TestWithContextCancelAbortsInFlight(t *testing.T) {
	// Handler hangs (request in flight) until teardown. started signals the call
	// reached the server; serverDone lets the handler return so srv.Close() never
	// blocks. defer order (LIFO) closes serverDone BEFORE srv.Close().
	started := make(chan struct{})
	serverDone := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-serverDone
	}))
	defer srv.Close()
	defer close(serverDone)

	// Long http timeout so ONLY ctx cancellation can end the call.
	base := New(srv.URL, 30*time.Second)
	jobCtx, cancel := context.WithCancel(context.Background())
	c := base.WithContext(jobCtx)

	done := make(chan map[string][]enrich.Ranked, 1)
	go func() { done <- c.Classify("x", map[string][]string{"task_type": {"codegen"}}) }()

	// Wait until the request is genuinely in flight at the server.
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("request never reached server")
	}
	// It must not return on its own before we cancel.
	select {
	case <-done:
		t.Fatal("Classify returned before ctx cancel — call not actually in flight")
	case <-time.After(150 * time.Millisecond):
	}

	cancel()
	select {
	case got := <-done:
		if got != nil {
			t.Fatalf("cancelled call must yield no result, got %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Classify did not abort promptly on ctx cancel — in-flight request not bound to ctx")
	}
}

func TestClassifyDoesNotSpinOnGenuineError(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError) // real error, not availability
	}))
	defer srv.Close()

	c := New(srv.URL, 2*time.Second)
	got := c.Classify("x", map[string][]string{"task_type": {"codegen"}})
	if got != nil {
		t.Fatalf("500 should return nil (no degrade, no spin), got %+v", got)
	}
	if calls != 1 {
		t.Fatalf("500 is non-retryable; expected exactly 1 call, got %d", calls)
	}
}

func TestWorkerReady(t *testing.T) {
	cases := []struct {
		name string
		code int
		body string
		want bool
	}{
		{"ready", 200, `{"worker":{"state":"ready"}}`, true},
		{"spawning", 200, `{"worker":{"state":"spawning"}}`, false},
		{"missing", 200, `{"worker":{}}`, false},
		{"malformed", 200, `not json`, false},
		{"http error", 503, `{}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/metrics" {
					t.Fatalf("unexpected path %q", r.URL.Path)
				}
				w.WriteHeader(tc.code)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c := New(srv.URL, time.Second)
			if got := c.WorkerReady(context.Background()); got != tc.want {
				t.Fatalf("WorkerReady = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestWorkerReadyTransportError(t *testing.T) {
	c := New("http://127.0.0.1:1", time.Second) // nothing listening
	if c.WorkerReady(context.Background()) {
		t.Fatal("WorkerReady should be false when the sidecar is unreachable")
	}
}
