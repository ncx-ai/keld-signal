package sidecar

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
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
