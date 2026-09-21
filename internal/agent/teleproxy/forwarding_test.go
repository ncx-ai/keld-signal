package teleproxy

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

const claudeBatch = `{"resourceLogs":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"claude-code"}}]},` +
	`"scopeLogs":[{"logRecords":[{"attributes":[{"key":"session.id","value":{"stringValue":"S1"}},{"key":"event.name","value":{"stringValue":"api_request"}}]}]}]}]}`

// ⚠️ TURNING THE SWITCH OFF USED TO DOUBLE-COUNT EVERY RUNNING TOOL. A tool
// reads its telemetry config once, so one configured before the switch went off
// keeps posting here from memory; the mirror is on for that same tool; Atlas
// keys the two rows differently. The proxy is where the bytes arrive, so it is
// where the rule has to be read: forward iff the switch is on.
func TestSwitchOffAcceptsAndDiscardsRatherThanForwarding(t *testing.T) {
	var upstream atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream.Add(1)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	p := New(srv.URL, srv.URL, func() string { return "t" }, "s", t.TempDir())
	var discardedFor []string
	p.OnDiscard(func(src string) { discardedFor = append(discardedFor, src) })
	on := false
	p.Forwarding(func() bool { return on })

	rr := post(t, p, "/v1/logs", "s", claudeBatch)
	p.WaitIdle()
	if rr.Code != http.StatusAccepted {
		t.Fatalf("code = %d, want 202 — the tool must not retry what we chose to drop", rr.Code)
	}
	if n := upstream.Load(); n != 0 {
		t.Fatalf("switch off: %d batch(es) forwarded, want 0 — this is the double count", n)
	}
	if p.DiscardedSwitchOff() != 1 {
		t.Fatalf("DiscardedSwitchOff = %d, want 1 — a drop without a number beside it is silent", p.DiscardedSwitchOff())
	}
	if at := p.LastForward(); !at.IsZero() {
		t.Fatal("a discarded batch was recorded as a forward; the pane's otel lane would read 'arrived' off bytes that went nowhere")
	}
	post(t, p, "/v1/logs", "s", claudeBatch)
	p.WaitIdle()
	if len(discardedFor) != 1 || discardedFor[0] != "claude_code" {
		t.Fatalf("OnDiscard = %v, want exactly one call for claude_code — once per source per run", discardedFor)
	}

	// The switch is read PER REQUEST: flip it and the next batch goes through
	// with no restart, the same way the token is read per request.
	on = true
	rr = post(t, p, "/v1/logs", "s", claudeBatch)
	p.WaitIdle()
	if rr.Code != http.StatusAccepted || upstream.Load() != 1 {
		t.Fatalf("switch on: code %d, forwarded %d, want 202 and 1", rr.Code, upstream.Load())
	}
}

// Every existing caller and test constructs a Proxy without Forwarding; they
// must keep forwarding, or a nil hook would be the switch-off nobody set.
func TestNoForwardingHookMeansForward(t *testing.T) {
	var upstream atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream.Add(1)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	p := New(srv.URL, srv.URL, func() string { return "t" }, "s", t.TempDir())
	post(t, p, "/v1/logs", "s", claudeBatch)
	p.WaitIdle()
	if upstream.Load() != 1 {
		t.Fatalf("forwarded %d, want 1", upstream.Load())
	}
}
