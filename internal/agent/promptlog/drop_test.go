package promptlog

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

const dropLine = `{"type":"assistant","requestId":"req_d","uuid":"a1","sessionId":"S1","timestamp":"2026-09-21T11:00:05Z","message":{"role":"assistant","model":"claude-opus-5","usage":{"input_tokens":10,"output_tokens":5},"content":[{"type":"text","text":"ok"}]}}`

// ⚠️ THE MIRROR HAS NO SPOOL, SO A RECORD IT CANNOT DELIVER IS GONE — and until
// this the only trace was a sentence in docs/durability.md. Each way a record is
// lost is counted under its own reason, and a hook hears it, so the daemon can
// say it out loud.
func TestEveryWayARecordIsLostIsCountedAndReported(t *testing.T) {
	var status int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }))
	defer srv.Close()
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	dead.Close() // a listener nothing answers on

	cases := []struct {
		name, logs, want string
		status           int
	}{
		{"not paired", "", DropNotPaired, 0},
		{"unreachable", dead.URL + "/v1/logs", DropUnreachable, 0},
		{"rejected", srv.URL + "/v1/logs", DropRejected, 401},
		{"unavailable", srv.URL + "/v1/logs", DropUnavailable, 503},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status = tc.status
			var heard []string
			tel := New(tc.logs, tc.logs, func() string { return "tok" }, map[string]bool{"claude_code": true})
			tel.OnDrop(func(r string) { heard = append(heard, r) })
			tel.Observe("claude_code", filepath.Join(t.TempDir(), "s.jsonl"), []byte(dropLine))
			// One api_request is up to TWO posts (a log record and a metric),
			// and each undelivered post is one drop — the count is per POST,
			// which is what "how much did not arrive" means on the wire.
			all := tel.Dropped()
			if all[tc.want] < 1 || len(all) != 1 {
				t.Fatalf("Dropped() = %v, want only %q counted", all, tc.want)
			}
			if len(heard) == 0 {
				t.Fatal("the hook heard nothing")
			}
			for _, r := range heard {
				if r != tc.want {
					t.Fatalf("hook heard %v, want only %q", heard, tc.want)
				}
			}
		})
	}
}

// A delivered record is not a drop.
func TestADeliveredRecordIsNotCounted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer srv.Close()
	tel := New(srv.URL+"/v1/logs", srv.URL+"/v1/metrics", func() string { return "tok" }, map[string]bool{"claude_code": true})
	tel.OnDrop(func(string) { t.Fatal("a 200 was reported as a drop") })
	tel.Observe("claude_code", filepath.Join(t.TempDir(), "s.jsonl"), []byte(dropLine))
	if n := len(tel.Dropped()); n != 0 {
		t.Fatalf("Dropped() = %v, want empty", tel.Dropped())
	}
}
