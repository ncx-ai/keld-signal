package sidecar

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/settings"
)

// TestPostProjectsSendsTheDeclaredList pins the /projects wire shape: a single
// "projects" key holding the JSON of settings.RemoteProject as-is.
func TestPostProjectsSendsTheDeclaredList(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/projects" {
			t.Errorf("path = %q", r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&got)
		json.NewEncoder(w).Encode(map[string]any{"count": 1, "hash": "abc"})
	}))
	defer srv.Close()

	projects := []settings.RemoteProject{{ID: "proj_pay", Title: "Payments", Description: "billing"}}
	if err := New(srv.URL, 5*time.Second).PostProjects(projects); err != nil {
		t.Fatalf("PostProjects failed: %v", err)
	}
	ps, ok := got["projects"].([]any)
	if !ok || len(ps) != 1 {
		t.Fatalf("projects not sent: %v", got)
	}
	one := ps[0].(map[string]any)
	if one["id"] != "proj_pay" || one["title"] != "Payments" {
		t.Errorf("project = %v", one)
	}
}

func TestPostProjectsFailsOnTransportError(t *testing.T) {
	old := postProjectsCallTimeout
	postProjectsCallTimeout = 300 * time.Millisecond
	defer func() { postProjectsCallTimeout = old }()

	c := New("http://127.0.0.1:1", 200*time.Millisecond) // nothing listening
	if err := c.PostProjects(nil); err == nil {
		t.Fatal("want an error against an unreachable sidecar")
	}
}

// TestAttributeSendsCoordinatesAndDims pins the /attribute request shape: path,
// session_id, start, end, dims — no message text ever crosses.
func TestAttributeSendsCoordinatesAndDims(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/attribute" {
			t.Errorf("path = %q", r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&got)
		json.NewEncoder(w).Encode(map[string]any{
			"status": "attributed",
			"projects": []map[string]any{
				{"id": "proj_pay", "confidence": 0.9, "source": "embedding"},
			},
			"attribution": map[string]any{"embed_ms": 12, "encoder_state": "warm", "verifier": "not_needed"},
		})
	}))
	defer srv.Close()

	res, ok := New(srv.URL, 5*time.Second).Attribute("/tmp/x.jsonl", "sess-1", 100, 700,
		map[string]string{"branch": "main"})
	if !ok {
		t.Fatal("Attribute reported failure on a 200")
	}
	if got["path"] != "/tmp/x.jsonl" || got["session_id"] != "sess-1" ||
		got["start"] != 100.0 || got["end"] != 700.0 {
		t.Errorf("request = %v", got)
	}
	dims, _ := got["dims"].(map[string]any)
	if dims["branch"] != "main" {
		t.Errorf("dims not sent: %v", got)
	}
	if res.Status != "attributed" || len(res.Projects) != 1 || res.Projects[0].ID != "proj_pay" {
		t.Fatalf("result = %+v", res)
	}
	if res.Attribution == nil || res.Attribution.EncoderState != "warm" {
		t.Fatalf("attribution meta = %+v", res.Attribution)
	}
}

// TestAttributePendingIsAnOkTerminalStatus — pending is a normal 200 answer,
// not a transport failure: ok=true, Status="pending". The caller (attrib
// package) is what decides pending must not consume a retry attempt.
func TestAttributePendingIsAnOkTerminalStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"status": "pending", "projects": nil, "attribution": nil})
	}))
	defer srv.Close()

	res, ok := New(srv.URL, 5*time.Second).Attribute("/tmp/x.jsonl", "sess-1", 1, 2, nil)
	if !ok || res.Status != "pending" {
		t.Fatalf("ok=%v res=%+v", ok, res)
	}
}

func TestAttributeFailsOnTransportError(t *testing.T) {
	old := attributeCallTimeout
	attributeCallTimeout = 300 * time.Millisecond
	defer func() { attributeCallTimeout = old }()

	c := New("http://127.0.0.1:1", 200*time.Millisecond) // nothing listening
	if _, ok := c.Attribute("/tmp/x.jsonl", "sess-1", 1, 2, nil); ok {
		t.Fatal("want ok=false against an unreachable sidecar")
	}
}

// TestAttributeBoundsItsOwnCallDeadline: a client whose base ctx never expires
// must still give up against a sidecar that keeps answering 503 (retryable),
// because Attribute binds its own per-call timeout rather than retrying
// through post()'s backoff forever. Regression guard for the death-spiral fix
// (client.go:46's WithContext pattern, applied per attribute call instead of
// per job). attributeCallTimeout is overridden here so the test does not wait
// out the real 2-minute production bound.
func TestAttributeBoundsItsOwnCallDeadline(t *testing.T) {
	old := attributeCallTimeout
	attributeCallTimeout = 300 * time.Millisecond
	defer func() { attributeCallTimeout = old }()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable) // retryable forever without a bound
	}))
	defer srv.Close()

	c := NewCtx(context.Background(), srv.URL, 5*time.Second) // base ctx never expires
	done := make(chan bool, 1)
	go func() {
		_, ok := c.Attribute("/tmp/x.jsonl", "sess-1", 1, 2, nil)
		done <- ok
	}()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("a 503-forever sidecar must report failure once the per-call deadline expires")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Attribute did not bound its own deadline against a sidecar stuck at 503")
	}
}
