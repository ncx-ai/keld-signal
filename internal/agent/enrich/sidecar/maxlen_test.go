package sidecar_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich/sidecar"
)

// captureServer records the decoded JSON body of the last request.
func captureServer(t *testing.T, got *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		m := map[string]any{}
		if err := json.Unmarshal(b, &m); err != nil {
			t.Errorf("decode body: %v", err)
		}
		*got = m
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"entities":[],"results":{}}`))
	}))
}

// The adaptive cap only bounds memory if it actually reaches gliner2, which
// reads it from the request's max_len. Without this the sidecar falls back to
// gliner2's default of no truncation.
func TestClassifySendsMaxLenWhenSet(t *testing.T) {
	var body map[string]any
	srv := captureServer(t, &body)
	defer srv.Close()

	c := sidecar.New(srv.URL, 5*time.Second).WithMaxLen(1234)
	c.Classify("hello world", map[string][]string{"task_type": {"a", "b"}})

	v, ok := body["max_len"]
	if !ok {
		t.Fatalf("classify body has no max_len: %v", body)
	}
	if n, _ := v.(float64); int(n) != 1234 {
		t.Fatalf("max_len = %v, want 1234", v)
	}
}

func TestExtractSendsMaxLenWhenSet(t *testing.T) {
	var body map[string]any
	srv := captureServer(t, &body)
	defer srv.Close()

	c := sidecar.New(srv.URL, 5*time.Second).WithMaxLen(777)
	c.Extract("hello world", map[string]string{"person": "a person"}, nil)

	if n, _ := body["max_len"].(float64); int(n) != 777 {
		t.Fatalf("max_len = %v, want 777", body["max_len"])
	}
}

func TestEntitiesSendsMaxLenWhenSet(t *testing.T) {
	var body map[string]any
	srv := captureServer(t, &body)
	defer srv.Close()

	c := sidecar.New(srv.URL, 5*time.Second).WithMaxLen(99)
	c.Entities("hello world", map[string]string{"person": "a person"})

	if n, _ := body["max_len"].(float64); int(n) != 99 {
		t.Fatalf("max_len = %v, want 99", body["max_len"])
	}
}

// Unset must be omitted, not sent as 0 — a literal 0 would read as "truncate to
// nothing" rather than "no cap".
func TestMaxLenOmittedWhenUnset(t *testing.T) {
	var body map[string]any
	srv := captureServer(t, &body)
	defer srv.Close()

	c := sidecar.New(srv.URL, 5*time.Second)
	c.Classify("hello world", map[string][]string{"task_type": {"a"}})

	if _, present := body["max_len"]; present {
		t.Fatalf("max_len must be omitted when unset, got %v", body["max_len"])
	}
}

// WithMaxLen must compose with WithModelContext: the pipeline binds a per-pass
// deadline to a client the daemon already bound a cap to, and losing either
// would silently drop that protection.
func TestMaxLenSurvivesContextBinding(t *testing.T) {
	var body map[string]any
	srv := captureServer(t, &body)
	defer srv.Close()

	c := sidecar.New(srv.URL, 5*time.Second).WithMaxLen(555)
	bound := c.WithModelContext(t.Context())
	bound.Classify("hello world", map[string][]string{"task_type": {"a"}})

	if n, _ := body["max_len"].(float64); int(n) != 555 {
		t.Fatalf("max_len = %v after context binding, want 555", body["max_len"])
	}
}
