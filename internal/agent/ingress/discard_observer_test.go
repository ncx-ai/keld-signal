package ingress

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/spool"
)

// ⚠️ A DISCARDED POINTER STILL FIRED THE HOOK, and losing that fact reports a
// working machine as broken.
//
// Under `ml_backend: "off"` the daemon runs no enrichment worker and /enrich
// accepts-and-discards. Telemetry is explicitly UNAFFECTED by that mode, so the
// otel lane keeps reporting while the hook lane records nothing — and "one
// expected lane active, another expected lane silent" is exactly the predicate
// for `broken`. Every machine with enrichment off would have read
// `broken · hook`, on a hook that fired correctly every time.
//
// The lane answers "did the hook fire", not "was the prompt enriched", so the
// observation belongs at ACCEPTANCE. This is the same reading the worker's own
// call site already documents: recorded on arrival, before the resolve.
func TestDiscardedPointerIsStillObserved(t *testing.T) {
	var seen []spool.Pointer
	prev := OnPointer
	OnPointer = func(p spool.Pointer) { seen = append(seen, p) }
	t.Cleanup(func() { OnPointer = prev })

	srv := httptest.NewServer(DiscardHandler("s3cret"))
	t.Cleanup(srv.Close)

	p := spool.Pointer{
		Source:      spool.Source{ID: "codex", Origin: "hook"},
		Correlation: spool.Correlation{Scheme: "prompt_id", ID: "sess#turn-3", SessionID: "sess"},
	}
	body, _ := json.Marshal(p)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/enrich", bytes.NewReader(body))
	req.Header.Set("x-keld-agent-secret", "s3cret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
	if len(seen) != 1 {
		t.Fatalf("observer called %d times, want 1; a machine with enrichment off would read broken on a hook that fired", len(seen))
	}
	if seen[0].Source.ID != "codex" || seen[0].Source.Origin != "hook" {
		t.Errorf("observed %+v, want the source and origin the hook sent", seen[0].Source)
	}
}

// A rejected pointer is NOT observed: an unauthenticated or malformed POST did
// not establish that the tool's hook is working, and recording it would make
// the lane unfalsifiable.
func TestARejectedPointerIsNotObserved(t *testing.T) {
	called := false
	prev := OnPointer
	OnPointer = func(spool.Pointer) { called = true }
	t.Cleanup(func() { OnPointer = prev })

	srv := httptest.NewServer(DiscardHandler("s3cret"))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/enrich", bytes.NewReader([]byte(`{"bad`)))
	req.Header.Set("x-keld-agent-secret", "s3cret")
	resp, _ := http.DefaultClient.Do(req)
	if resp != nil {
		resp.Body.Close()
	}
	if called {
		t.Error("a malformed pointer was observed; the lane must stay falsifiable")
	}

	req2, _ := http.NewRequest(http.MethodPost, srv.URL+"/enrich", bytes.NewReader([]byte(`{}`)))
	req2.Header.Set("x-keld-agent-secret", "wrong")
	resp2, _ := http.DefaultClient.Do(req2)
	if resp2 != nil {
		resp2.Body.Close()
	}
	if called {
		t.Error("an unauthenticated pointer was observed")
	}
}
