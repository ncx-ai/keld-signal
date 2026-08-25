package sidecar

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

func TestSignalIngestSendsThePathAndNothingElse(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ingest" {
			t.Errorf("path = %q", r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&got)
		json.NewEncoder(w).Encode(map[string]any{"new_lines": 12, "reparsed": false})
	}))
	defer srv.Close()

	if !New(srv.URL, 5*time.Second).SignalIngest("/tmp/t.jsonl", enrich.ResolvedFacts{}) {
		t.Fatal("SignalIngest reported failure on a 200")
	}
	if got["path"] != "/tmp/t.jsonl" {
		t.Errorf("path not sent: %v", got)
	}
	if len(got) != 1 {
		t.Errorf("the signal is coordinates only, got %v", got)
	}
}

// The whole point of moving ingest off the request path is that it costs the
// watcher nothing. post() waits and retries through 503 with backoff, forever
// until its context ends — correct for a facet whose answer is needed, wrong for
// a signal whose only job is to be timely: the watcher's poll loop would sit in
// a backoff sleep while a dead sidecar accumulated advanced transcripts. One
// attempt. A dropped signal is recoverable: ingest resumes from the stored byte
// offset, so the next signal (or /analyze's own on-demand ingest) catches up.
func TestSignalIngestDoesNotRetry(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	start := time.Now()
	if New(srv.URL, 5*time.Second).SignalIngest("/tmp/t.jsonl", enrich.ResolvedFacts{}) {
		t.Error("a 503 must report failure, not success")
	}
	if n := atomic.LoadInt32(&hits); n != 1 {
		t.Fatalf("want exactly 1 attempt, got %d — the signal must not retry", n)
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("SignalIngest slept for %s; it must not back off", el)
	}
}

func TestSignalIngestReportsFailureOnErrors(t *testing.T) {
	for _, code := range []int{http.StatusNotFound, http.StatusForbidden, http.StatusInternalServerError} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
		}))
		if New(srv.URL, 5*time.Second).SignalIngest("/tmp/t.jsonl", enrich.ResolvedFacts{}) {
			t.Errorf("status %d must report failure", code)
		}
		srv.Close()
	}
}

// An unreachable sidecar is the common case (not provisioned, restarting,
// mid-recycle). It must return promptly and never wedge the caller.
func TestSignalIngestOnAnUnreachableSidecarReturnsPromptly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing is listening now

	done := make(chan bool, 1)
	go func() { done <- New(url, 2*time.Second).SignalIngest("/tmp/t.jsonl", enrich.ResolvedFacts{}) }()
	select {
	case ok := <-done:
		if ok {
			t.Error("a dead sidecar must report failure")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("SignalIngest never returned against a dead sidecar")
	}
}
