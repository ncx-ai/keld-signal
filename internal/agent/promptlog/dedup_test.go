package promptlog

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// THE DEDUP CONTRACT.
//
// Atlas stores one tool event per `(event_ts, dedup_key)` and upserts
// (`models.py`: UniqueConstraint("event_ts", "dedup_key"); `services/telemetry.py`:
// on_conflict index_elements=["event_ts","dedup_key"]). The three parsers build
// that key differently, and the rules below are a faithful Go replica of them:
//
//	claude_code  services/api/app/services/otel.py::_dedup_key
//	             f"{session.id}:{event.sequence}" if both present, else request_id
//	codex        services/api/app/services/codex.py::_dedup_key
//	             f"codex:{session}:{seq}", else f"codex:{request_id}",
//	             else a content hash of the merged attributes
//	gemini_cli   services/api/app/services/gemini.py::_dedup_key
//	             always a content hash of the merged attributes
//
// Two of the three hash their attributes, so for those the mirror's dedup IS the
// determinism of its payload: anything that varies between two readings of the
// same transcript — a wall clock, a process-local counter — produces a second
// row for work that happened once.

// atlasRow is Atlas's (event_ts, dedup_key) for one emitted record.
func atlasRow(source string, r emitted) (string, string) {
	ts := r.attrs["event.timestamp"].StringValue
	if ts == "" {
		ts = r.ts
	}
	session := r.attrs["session.id"].StringValue
	if session == "" {
		session = r.attrs["conversation.id"].StringValue
	}
	seq := string(r.attrs["event.sequence"].IntValue)
	if seq == "" {
		seq = r.attrs["event.sequence"].StringValue
	}
	req := r.attrs["request_id"].StringValue
	switch source {
	case "codex":
		switch {
		case session != "" && seq != "":
			return ts, fmt.Sprintf("codex:%s:%s", session, seq)
		case req != "":
			return ts, "codex:" + req
		}
		return ts, "codex:h:" + contentHash(r)
	case "gemini":
		// event_ts comes from the timeUnixNano the record carries when the ISO
		// attribute is absent; gemini_cli always sends the ISO one.
		return ts, "gemini:h:" + contentHash(r)
	default:
		if session != "" && seq != "" {
			return ts, session + ":" + seq
		}
		return ts, req
	}
}

// contentHash stands in for Atlas's sha256 over the merged attribute dict. The
// digest value does not have to match Python's byte for byte — what the test
// needs is that identical attributes hash identically and different ones do not.
func contentHash(r emitted) string {
	keys := make([]string, 0, len(r.attrs))
	for k := range r.attrs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		v := r.attrs[k]
		fmt.Fprintf(h, "%s=%s|%s\n", k, v.StringValue, string(v.IntValue))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func rowsOf(t *testing.T, source string, recs []emitted, event string) map[[2]string]int {
	t.Helper()
	out := map[[2]string]int{}
	for _, r := range only(t, recs, event) {
		ts, key := atlasRow(source, r)
		if key == "" {
			t.Errorf("%s: record has NO dedup key — Postgres treats every NULL as distinct, so it can never dedup", event)
		}
		out[[2]string{ts, key}]++
	}
	return out
}

// TestMirrorIsIdempotentPerRequest feeds each tool's real session through TWO
// independent mirrors — the daemon restarting and re-reading the same transcript
// — and requires the two runs to land on exactly the same Atlas rows, one per
// request.
func TestMirrorIsIdempotentPerRequest(t *testing.T) {
	cases := []struct {
		source, fixture, event string
		doc                    bool
		wantRequests           int
	}{
		{source: "claude_code", fixture: "claude_code_session.jsonl", event: "api_request", wantRequests: 2},
		{source: "codex", fixture: "codex_rollout.jsonl", event: "codex.sse_event", wantRequests: 4},
		{source: "gemini", fixture: "gemini_session.json", event: "gemini_cli.api_response", doc: true, wantRequests: 4},
	}
	for _, tc := range cases {
		t.Run(tc.source, func(t *testing.T) {
			run := func() map[[2]string]int {
				c, srv := newCapSink()
				defer srv.Close()
				tel := telFor(srv.URL+"/v1/logs", srv.URL+"/v1/metrics", map[string]bool{tc.source: true})
				if tc.doc {
					tel.ObserveFile(tc.source, filepath.Join("testdata", tc.fixture))
				} else {
					mirrorLines(t, tel, tc.source, tc.fixture)
				}
				return rowsOf(t, tc.source, flatten(t, c.bodies("/v1/logs")), tc.event)
			}
			first, second := run(), run()
			if len(first) != tc.wantRequests {
				t.Fatalf("%d distinct Atlas rows, want %d (one per request)", len(first), tc.wantRequests)
			}
			for k, n := range first {
				if n != 1 {
					t.Errorf("one mirror pass produced %d records for row %v", n, k)
				}
			}
			if len(second) != len(first) {
				t.Fatalf("a second pass produced %d rows, first produced %d", len(second), len(first))
			}
			for k := range first {
				if _, ok := second[k]; !ok {
					t.Errorf("row %v from the first pass is absent from the second — the key is not derived from the transcript alone", k)
				}
			}
		})
	}
}

// TestMirroredAndToolSentCollapseToOneRow is the cross-path half: the SAME
// request, once as the tool's own captured OTLP export and once as the mirror
// reads it out of the transcript, must be one row in Atlas.
//
// ⚠️ THE TRANSCRIPT LINE BELOW IS DERIVED FROM THE CAPTURED OTLP RECORD, NOT
// CAPTURED BESIDE IT. Every value in it (request id, session id, prompt id,
// model, the four token buckets, the instant) is read straight off the real
// `claude_code.api_request` record in testdata/claude_code_native_api_request.json
// — which is what Claude Code exported — and rewritten in the shape Claude Code
// writes to `~/.claude/projects`. The one link that is ASSUMED rather than
// measured is that the transcript line's `timestamp` is the same instant as the
// OTLP record's `event.timestamp`; no capture on this machine holds both halves
// of one request, so that assumption is stated rather than proven.
func TestMirroredAndToolSentCollapseToOneRow(t *testing.T) {
	native := flatten(t, []string{readFixture(t, "claude_code_native_api_request.json")})
	if len(native) != 1 {
		t.Fatalf("expected 1 captured native record, got %d", len(native))
	}
	n := native[0]

	transcript := []string{
		`{"type":"user","promptId":"b7a1e3c0-11d2-4f8e-9a3b-5c6d7e8f9012","uuid":"00000000-0000-4000-8000-000000000004",` +
			`"sessionId":"39b953bc-6e27-45f5-9b80-820a08c984a3","version":"2.1.271","timestamp":"2026-08-28T01:00:50.000Z",` +
			`"message":{"role":"user","content":[{"type":"text","text":"x"}]}}`,
		`{"type":"assistant","requestId":"req_000000000000000000000000","uuid":"00000000-0000-4000-8000-000000000003",` +
			`"effort":"high","sessionId":"39b953bc-6e27-45f5-9b80-820a08c984a3","version":"2.1.271",` +
			`"timestamp":"2026-08-28T01:00:56.891Z","message":{"role":"assistant","model":"claude-opus-5","id":"msg_1",` +
			`"content":[],"usage":{"input_tokens":2,"output_tokens":667,"cache_read_input_tokens":176254,` +
			`"cache_creation_input_tokens":723,"service_tier":"standard"}}}`,
	}
	c, srv := newCapSink()
	defer srv.Close()
	tel := telFor(srv.URL+"/v1/logs", srv.URL+"/v1/metrics", map[string]bool{"claude_code": true})
	for _, line := range transcript {
		tel.Observe("claude_code", "/tmp/sess.jsonl", []byte(line))
	}
	mirrored := only(t, flatten(t, c.bodies("/v1/logs")), "api_request")
	if len(mirrored) != 1 {
		t.Fatalf("expected 1 mirrored api_request, got %d", len(mirrored))
	}
	m := mirrored[0]

	// The identifiers and the instant must be the tool's own, or no key built
	// from them could ever agree.
	for _, k := range []string{"request_id", "session.id", "prompt.id", "model", "event.timestamp",
		"input_tokens", "output_tokens", "cache_read_tokens", "cache_creation_tokens"} {
		if m.attrs[k] != n.attrs[k] {
			t.Errorf("%s: mirrored %+v, tool-sent %+v", k, m.attrs[k], n.attrs[k])
		}
	}
	if m.ts != n.ts {
		t.Errorf("timeUnixNano: mirrored %q, tool-sent %q", m.ts, n.ts)
	}

	// ⚠️ THE MIRROR EMITS NO event.sequence AND THE TOOL DOES, so under Atlas's
	// CURRENT preference order (`session:sequence` first) the two rows do not
	// collapse — closing that needs the one-line preference flip named in
	// otel.py::_dedup_key, not a change here. What the client owes is the natural
	// key, and this asserts it: keyed on `request_id`, the two paths are one row.
	rows := map[[2]string]int{}
	for _, r := range []emitted{m, n} {
		rows[[2]string{r.attrs["event.timestamp"].StringValue, r.attrs["request_id"].StringValue}]++
	}
	if len(rows) != 1 {
		t.Fatalf("mirrored and tool-sent produced %d rows on (event_ts, request_id), want 1: %v", len(rows), rows)
	}
	if _, ok := m.attrs["event.sequence"]; ok {
		t.Error("the mirror must not emit event.sequence")
	}
}

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var probe any
	if err := json.Unmarshal(b, &probe); err != nil {
		t.Fatalf("%s is not JSON: %v", name, err)
	}
	return string(b)
}
