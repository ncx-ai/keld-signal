package promptlog

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The fixtures under testdata/ are REAL transcripts written by the three tools on
// this machine, with every text-bearing key deleted outright (message content,
// thoughts, tool inputs, tool results, Codex's base_instructions). Nothing is
// paraphrased: what is left is the record the tool wrote, minus the words.

// mirrorLines feeds a line-shaped fixture through Observe, one line at a time,
// exactly as watch.scanFrom does.
func mirrorLines(t *testing.T, tel *Telemetry, source, fixture string) string {
	t.Helper()
	path := filepath.Join("testdata", fixture)
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for sc.Scan() {
		tel.Observe(source, path, append([]byte(nil), sc.Bytes()...))
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return path
}

// emitted is one flattened log record: its event name plus its attributes.
type emitted struct {
	event string
	ts    string // timeUnixNano
	attrs map[string]anyVal
}

// flatten parses captured OTLP logs bodies into one entry per log record, in the
// order they were posted. Resource attributes are folded in the way Atlas's
// flatten_attributes + {**res, **rec} merge does.
func flatten(t *testing.T, bodies []string) []emitted {
	t.Helper()
	var out []emitted
	for _, b := range bodies {
		var p otlpLogs
		if err := json.Unmarshal([]byte(b), &p); err != nil {
			t.Fatalf("bad OTLP body: %v\n%s", err, b)
		}
		for _, rl := range p.ResourceLogs {
			res := map[string]anyVal{}
			for _, a := range rl.Resource.Attributes {
				res[a.Key] = a.Value
			}
			for _, sl := range rl.ScopeLogs {
				for _, lr := range sl.LogRecords {
					m := map[string]anyVal{}
					for k, v := range res {
						m[k] = v
					}
					for _, a := range lr.Attributes {
						m[a.Key] = a.Value
					}
					out = append(out, emitted{
						event: m["event.name"].StringValue,
						ts:    lr.TimeUnixNano,
						attrs: m,
					})
				}
			}
		}
	}
	return out
}

func only(t *testing.T, recs []emitted, event string) []emitted {
	t.Helper()
	var out []emitted
	for _, r := range recs {
		if r.event == event {
			out = append(out, r)
		}
	}
	return out
}

func wantStr(t *testing.T, r emitted, key, want string) {
	t.Helper()
	if got := r.attrs[key].StringValue; got != want {
		t.Errorf("%s: %s = %q, want %q", r.event, key, got, want)
	}
}

func wantInt(t *testing.T, r emitted, key, want string) {
	t.Helper()
	if got := string(r.attrs[key].IntValue); got != want {
		t.Errorf("%s: %s = %q, want %q", r.event, key, got, want)
	}
}

// ⚠️ CLAUDE CODE WRITES ONE ASSISTANT LINE PER CONTENT BLOCK, NOT ONE PER
// REQUEST, AND A RECORD PER LINE REPORTS THE REQUEST'S TOKENS ONCE PER BLOCK.
// Measured over the 40 largest real Claude Code transcripts on this machine:
// 13,755 assistant lines carrying a `message.usage` resolve to 7,683 distinct
// requestIds; 4,088 of those requests are written as more than one line (max
// 11), and 0 of them disagree with themselves about their token counts. A
// record per LINE therefore publishes 1.79x the tokens the work actually cost.
// The fixture holds one 3-line request and one 1-line request for exactly that
// reason.
func TestClaudeCodeMirrorsOneRecordPerRequest(t *testing.T) {
	c, srv := newCapSink()
	defer srv.Close()
	tel := telFor(srv.URL+"/v1/logs", srv.URL+"/v1/metrics", map[string]bool{"claude_code": true})
	mirrorLines(t, tel, "claude_code", "claude_code_session.jsonl")

	recs := flatten(t, c.bodies("/v1/logs"))
	api := only(t, recs, "api_request")
	if len(api) != 2 {
		t.Fatalf("expected 1 api_request per REQUEST (2 requests in the fixture, 4 assistant lines), got %d", len(api))
	}

	// The 3-line request. Every priced field is asserted against the transcript.
	a := api[0]
	wantStr(t, a, "request_id", "req_011CfB6RNF6hHR92p5Chjxx1")
	wantStr(t, a, "session.id", "a221290d-4a64-4af0-9c58-726cd4a8381b")
	wantStr(t, a, "prompt.id", "834b0332-177d-4bd8-a3cc-2f1078f881c1")
	wantStr(t, a, "model", "claude-opus-5")
	wantStr(t, a, "service.name", "claude-code")
	wantStr(t, a, "service.version", "2.1.274")
	wantStr(t, a, "tool", "claude_code")
	wantInt(t, a, "input_tokens", "2")
	wantInt(t, a, "output_tokens", "353")
	wantInt(t, a, "cache_read_tokens", "31921")
	wantInt(t, a, "cache_creation_tokens", "87180")
	wantInt(t, a, "cache_creation_1h_tokens", "87180")
	wantInt(t, a, "cache_creation_5m_tokens", "0")
	wantStr(t, a, "service_tier", "standard")
	wantStr(t, a, "effort", "high")
	// The instant is the request's FIRST line, not whichever block the daemon
	// happened to be reading — otherwise the same request lands at a different
	// event_ts on a re-read and Atlas stores it twice.
	wantStr(t, a, "event.timestamp", "2026-09-18T14:52:29.763Z")
	if a.ts != "1789743149763000000" {
		t.Errorf("timeUnixNano = %q, want the first line's own instant", a.ts)
	}

	b := api[1]
	wantStr(t, b, "request_id", "req_011CfB6VVMLwMfPXSRPRobNs")
	wantInt(t, b, "input_tokens", "2")
	wantInt(t, b, "output_tokens", "141")
	wantInt(t, b, "cache_read_tokens", "130838")
	wantInt(t, b, "cache_creation_tokens", "636")
	wantStr(t, b, "event.timestamp", "2026-09-18T14:53:24.791Z")

	// ⚠️ NO event.sequence ON THE PRICED RECORD. Atlas keys a Claude-Code row on
	// `session.id:event.sequence` and falls back to `request_id`
	// (services/api/app/services/otel.py::_dedup_key). A sequence is process
	// state: a restarted daemon re-numbers the same events, so a key built on it
	// cannot dedup a re-read. request_id is the tool's own id for the request and
	// is identical whichever path delivers it.
	if _, ok := a.attrs["event.sequence"]; ok {
		t.Error("api_request must not carry event.sequence — it is what blocks dedup on request_id")
	}

	// The human turn still reports, once.
	if n := len(only(t, recs, "user_prompt")); n != 1 {
		t.Errorf("expected 1 user_prompt, got %d", n)
	}
}

// ⚠️ 936 OF 10,061 REAL CODEX `token_count` RECORDS ARE RE-EMISSIONS. Measured
// over the 23 most recent rollouts on this machine: 936 records (9.3%) repeat
// the previous record's `total_token_usage` exactly while still carrying a
// non-zero `last_token_usage`, so pricing every record double-counts them.
// Taking a record only where `total_token_usage` ADVANCED reconciles the summed
// per-request usage with the session's own final total on 22 of 23 rollouts (the
// 23rd is the one rollout whose total decreased — a context reset).
func TestCodexMirrorsRealRollout(t *testing.T) {
	c, srv := newCapSink()
	defer srv.Close()
	tel := telFor(srv.URL+"/v1/logs", srv.URL+"/v1/metrics", map[string]bool{"codex": true})
	mirrorLines(t, tel, "codex", "codex_rollout.jsonl")

	recs := only(t, flatten(t, c.bodies("/v1/logs")), "codex.sse_event")
	if len(recs) != 4 {
		t.Fatalf("expected 4 codex.sse_event records (4 token_count lines), got %d", len(recs))
	}
	r := recs[0]
	wantStr(t, r, "event.kind", "response.completed")
	wantStr(t, r, "conversation.id", "01a0b5de-f165-7481-9a3f-a754e6000400")
	wantStr(t, r, "model", "gpt-6-astra")
	wantStr(t, r, "app.version", "0.155.0")
	wantStr(t, r, "event.timestamp", "2026-09-18T18:56:56.769Z")
	wantInt(t, r, "input_tokens", "18949")
	wantInt(t, r, "cached_tokens", "12032")
	wantInt(t, r, "cache_write_tokens", "0")
	wantInt(t, r, "output_tokens", "94")
	wantInt(t, r, "reasoning_output_tokens", "0")
	wantInt(t, r, "total_tokens", "19043")
	// Codex sends no request id of its own, so Atlas's only remaining natural key
	// is `request_id`; the mirror supplies one derived from the record alone.
	if r.attrs["request_id"].StringValue == "" {
		t.Error("codex.sse_event must carry a deterministic request_id")
	}
	// The SECOND record must be the second REQUEST's own usage, not the running
	// total: Codex's total_token_usage is cumulative and last_token_usage is not.
	wantInt(t, recs[1], "input_tokens", "19133")
	wantInt(t, recs[1], "output_tokens", "42")
	wantStr(t, recs[1], "service.name", "codex-tui")
	wantStr(t, recs[1], "tool", "codex")
}

func TestCodexSkipsReEmittedTokenCount(t *testing.T) {
	c, srv := newCapSink()
	defer srv.Close()
	tel := telFor(srv.URL+"/v1/logs", srv.URL+"/v1/metrics", map[string]bool{"codex": true})
	mirrorLines(t, tel, "codex", "codex_reemission.jsonl")

	recs := only(t, flatten(t, c.bodies("/v1/logs")), "codex.sse_event")
	if len(recs) != 1 {
		t.Fatalf("two adjacent real token_count records carrying the SAME total_token_usage "+
			"are one request; got %d records", len(recs))
	}
	wantInt(t, recs[0], "input_tokens", "41835")
	wantInt(t, recs[0], "output_tokens", "123")
}

func TestGeminiMirrorsRealChat(t *testing.T) {
	c, srv := newCapSink()
	defer srv.Close()
	tel := telFor(srv.URL+"/v1/logs", srv.URL+"/v1/metrics", map[string]bool{"gemini": true})
	tel.ObserveFile("gemini", filepath.Join("testdata", "gemini_session.json"))

	recs := only(t, flatten(t, c.bodies("/v1/logs")), "gemini_cli.api_response")
	if len(recs) != 4 {
		t.Fatalf("expected 4 gemini_cli.api_response records, got %d", len(recs))
	}
	r := recs[0]
	wantStr(t, r, "session.id", "40164b6f-75b0-4059-9392-c013bf2b5a67")
	wantStr(t, r, "model", "gemini-2.5-pro")
	wantStr(t, r, "service.name", "gemini-cli")
	wantStr(t, r, "tool", "gemini")
	wantStr(t, r, "event.timestamp", "2025-09-21T15:35:39.075Z")
	wantInt(t, r, "input_token_count", "5992")
	wantInt(t, r, "output_token_count", "65")
	wantInt(t, r, "cached_content_token_count", "4475")
	wantInt(t, r, "thoughts_token_count", "400")
	// The correlation id Gemini's own OTEL reports, so Atlas can join the row to
	// the enrichment the watcher published for the same prompt.
	wantStr(t, r, "prompt_id", "40164b6f-75b0-4059-9392-c013bf2b5a67########0")
	wantStr(t, r, "message.id", "64bfc674-f258-40a5-af11-33c8cae3315b")

	wantInt(t, recs[3], "input_token_count", "6626")
	wantInt(t, recs[3], "cached_content_token_count", "0")

	// Gemini rewrites the whole document every turn. A second pass over an
	// unchanged file must add nothing.
	before := len(c.bodies("/v1/logs"))
	tel.ObserveFile("gemini", filepath.Join("testdata", "gemini_session.json"))
	if after := len(c.bodies("/v1/logs")); after != before {
		t.Errorf("re-reading an unchanged chat posted again: %d -> %d", before, after)
	}
}
