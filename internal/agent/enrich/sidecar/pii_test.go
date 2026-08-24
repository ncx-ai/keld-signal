package sidecar

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The /pii contract: offsets + types + scores, and the truncation flag. The
// matched VALUE never crosses the wire (the caller holds the text), so the
// client has nothing to decode into Entity.Text.
func TestDetectPIIDecodesSpansAndTruncation(t *testing.T) {
	var gotBody map[string]any
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/pii" {
			t.Errorf("path = %q, want /pii", r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &gotBody)
		w.Write([]byte(`{"spans":[{"type":"ssn","start":4,"end":15,"score":0.5}],"truncated":true}`))
	}))
	defer s.Close()

	res, ok := New(s.URL, 5*time.Second).DetectPII("ssn 321-54-9876")
	if !ok {
		t.Fatal("DetectPII reported failure on a 200")
	}
	if !res.Truncated {
		t.Fatal("truncated flag lost: the caller cannot tell a partial scan from a clean one")
	}
	if len(res.Spans) != 1 {
		t.Fatalf("spans = %+v, want 1", res.Spans)
	}
	e := res.Spans[0]
	if e.Label != "ssn" || e.Start != 4 || e.End != 15 || e.Confidence != 0.5 {
		t.Fatalf("span decoded wrong: %+v", e)
	}
	if e.Text != "" || e.Masked != "" {
		t.Fatalf("span must carry no value: %+v", e)
	}
	if gotBody["text"] != "ssn 321-54-9876" {
		t.Fatalf("request body = %v, want the text to scan", gotBody)
	}
}

// An unreachable service is reported as unavailable, never as a clean empty
// scan: "we looked and found nothing" is a positive claim the caller publishes.
func TestDetectPIIUnreachableIsNotAnEmptyResult(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	c := NewCtx(ctx, "http://127.0.0.1:1", 200*time.Millisecond) // nothing listening
	if _, ok := c.DetectPII("ssn 321-54-9876"); ok {
		t.Fatal("DetectPII must report failure when the service cannot be reached")
	}
}

// A genuine error from the scan (503 "pii scan unavailable" after a presidio
// failure) is retried like any temporary unavailability and then succeeds.
func TestDetectPIIRetriesThrough503(t *testing.T) {
	var calls int
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(`{"spans":[],"truncated":false}`))
	}))
	defer s.Close()

	res, ok := New(s.URL, 5*time.Second).DetectPII("nothing here")
	if !ok || len(res.Spans) != 0 || res.Truncated {
		t.Fatalf("res = %+v ok = %v; want a clean empty scan after the retry", res, ok)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2 (one 503, one success)", calls)
	}
}

// The region tier rides EACH REQUEST rather than the sidecar's startup env, so
// an org changing `pii_regions` takes effect on the next prompt. Three states
// have to survive the wire and they are three different answers:
//
//	nil        -> JSON null   -> the sidecar applies its own default (`us`)
//	[]         -> key present, empty -> the universal tier ONLY
//	["uk","au"] -> those tiers on top of the universal ones
func TestDetectPIIInSendsTheRegionTier(t *testing.T) {
	var bodies []map[string]any
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &b)
		bodies = append(bodies, b)
		w.Write([]byte(`{"spans":[],"truncated":false}`))
	}))
	defer s.Close()

	c := New(s.URL, 5*time.Second)
	if _, ok := c.DetectPIIIn("text", []string{"uk", "au"}); !ok {
		t.Fatal("DetectPIIIn failed on a 200")
	}
	if _, ok := c.DetectPIIIn("text", []string{}); !ok {
		t.Fatal("DetectPIIIn failed on a 200")
	}
	if _, ok := c.DetectPII("text"); !ok {
		t.Fatal("DetectPII failed on a 200")
	}
	if len(bodies) != 3 {
		t.Fatalf("got %d requests, want 3", len(bodies))
	}
	if got, _ := bodies[0]["regions"].([]any); len(got) != 2 || got[0] != "uk" || got[1] != "au" {
		t.Fatalf("regions = %v, want [uk au]", bodies[0]["regions"])
	}
	got, present := bodies[1]["regions"]
	if !present {
		t.Fatal("an EMPTY region list must be sent as [], not omitted: omitting it means 'no opinion' and the sidecar re-applies its default")
	}
	if arr, _ := got.([]any); len(arr) != 0 {
		t.Fatalf("regions = %v, want []", got)
	}
	// null, not []: PiiIn.regions is `list[str] | None`, so an explicit null is
	// the same "no opinion" an absent key would be, and it must NOT be [] or the
	// unregioned callers (the eval harness, keld-agent eval) would silently lose
	// the us tier.
	if v, present := bodies[2]["regions"]; !present || v != nil {
		t.Fatalf("DetectPII must send regions:null, got %v (present=%v)", v, present)
	}
}
