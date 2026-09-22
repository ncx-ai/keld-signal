package publish

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// ⚠️ **AN UNPAIRED SEND MUST FAIL, LOUDLY AND SPECIFICALLY.**
//
// The daemon now constructs every publisher before the machine is paired, so
// each of these is called with no endpoint at all for as long as somebody has
// not finished signing in. Two wrong answers are available and both lose work:
// reporting success would advance the block emitter's cursor past rows Atlas
// never received (the emitter only re-offers what it did not publish), and
// building a request from a derived-but-baseless URL like "/v1/signal/blocks"
// fails in a way retry.IsTransient calls PERMANENT — which is how a spooled
// batch becomes a deleted one.
func TestAnUnpairedPublisherRefusesEveryRouteWithErrNotPaired(t *testing.T) {
	var reached atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	unpaired := NewDeferred(func() string { return "" }, func() string { return "tok" }, "actor")

	if err := unpaired.Send(Enrichment{}); !errors.Is(err, ErrNotPaired) {
		t.Fatalf("Send on an unpaired publisher = %v, want ErrNotPaired", err)
	}
	if err := unpaired.SendBlocks([]BlockEnrichment{{}}); !errors.Is(err, ErrNotPaired) {
		t.Fatalf("SendBlocks on an unpaired publisher = %v, want ErrNotPaired", err)
	}
	if err := unpaired.SendWindow(WindowEnrichment{}); !errors.Is(err, ErrNotPaired) {
		t.Fatalf("SendWindow on an unpaired publisher = %v, want ErrNotPaired", err)
	}
	if got := reached.Load(); got != 0 {
		t.Fatalf("an unpaired publisher made %d requests; it must not dial at all", got)
	}
}

// And the same publisher starts working the moment the pairing lands, with
// nothing reconstructed — which is what lets the daemon adopt a pairing mid-run
// without a restart.
func TestADeferredPublisherAdoptsThePairingWithoutBeingRebuilt(t *testing.T) {
	var reached atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	var endpoint atomic.Value
	endpoint.Store("")
	p := NewDeferred(func() string { return endpoint.Load().(string) }, func() string { return "tok" }, "actor")

	if err := p.Send(Enrichment{}); !errors.Is(err, ErrNotPaired) {
		t.Fatalf("before pairing: %v, want ErrNotPaired", err)
	}
	endpoint.Store(srv.URL + "/v1/enrichments")
	if err := p.Send(Enrichment{}); err != nil {
		t.Fatalf("after pairing: %v, want a successful publish", err)
	}
	if got := reached.Load(); got != 1 {
		t.Fatalf("Atlas received %d requests, want exactly the one sent after pairing", got)
	}
}
