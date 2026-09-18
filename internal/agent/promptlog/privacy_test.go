package promptlog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// canaries are placed in EVERY text-bearing position each tool's transcript has.
// None of them may appear in anything the mirror posts — the invariant is that
// text, a span and an offset never cross, and a mirror that reads a transcript
// in order to report tokens is the place that would break it first.
var canaries = []string{
	"CANARY-USER-PROMPT", "CANARY-ASSISTANT-TEXT", "CANARY-THINKING",
	"CANARY-TOOL-INPUT", "CANARY-TOOL-RESULT", "CANARY-SYSTEM-INSTRUCTIONS",
	"CANARY-DISPLAY-CONTENT",
}

func assertNoText(t *testing.T, c *capSink) {
	t.Helper()
	var bodies []string
	bodies = append(bodies, c.bodies("/v1/logs")...)
	bodies = append(bodies, c.bodies("/v1/metrics")...)
	if len(bodies) == 0 {
		t.Fatal("nothing was posted — a privacy assertion over an empty payload proves nothing")
	}
	for _, b := range bodies {
		for _, canary := range canaries {
			if strings.Contains(b, canary) {
				t.Fatalf("text leaked (%s): %s", canary, b)
			}
		}
	}
}

func TestNoTextCrossesForClaudeCode(t *testing.T) {
	c, srv := newCapSink()
	defer srv.Close()
	tel := telFor(srv.URL+"/v1/logs", srv.URL+"/v1/metrics", map[string]bool{"claude_code": true})
	lines := []string{
		`{"type":"user","promptId":"P1","uuid":"U1","sessionId":"S1","version":"2.1.274","timestamp":"2026-09-18T10:00:00.000Z",` +
			`"message":{"role":"user","content":[{"type":"text","text":"CANARY-USER-PROMPT"}]}}`,
		`{"type":"assistant","requestId":"R1","uuid":"A1","sessionId":"S1","version":"2.1.274","timestamp":"2026-09-18T10:00:05.000Z",` +
			`"wireToolInputs":{"toolu_1":{"command":"CANARY-TOOL-INPUT"}},` +
			`"message":{"role":"assistant","model":"claude-opus-5","id":"msg_1",` +
			`"content":[{"type":"thinking","thinking":"CANARY-THINKING"},{"type":"text","text":"CANARY-ASSISTANT-TEXT"}],` +
			`"usage":{"input_tokens":1,"output_tokens":2,"cache_read_input_tokens":3,"cache_creation_input_tokens":4}}}`,
		`{"type":"user","promptId":"P1","uuid":"U2","sessionId":"S1","timestamp":"2026-09-18T10:00:06.000Z",` +
			`"toolUseResult":{"stdout":"CANARY-TOOL-RESULT"},"message":{"role":"user","content":[{"type":"tool_result","content":"CANARY-TOOL-RESULT"}]}}`,
	}
	for _, l := range lines {
		tel.Observe("claude_code", "/tmp/s.jsonl", []byte(l))
	}
	assertNoText(t, c)
}

func TestNoTextCrossesForCodex(t *testing.T) {
	c, srv := newCapSink()
	defer srv.Close()
	tel := telFor(srv.URL+"/v1/logs", srv.URL+"/v1/metrics", map[string]bool{"codex": true})
	lines := []string{
		`{"timestamp":"2026-09-18T10:00:00.000Z","type":"session_meta","payload":{"id":"SESS","cli_version":"0.155.0",` +
			`"originator":"codex-tui","base_instructions":{"text":"CANARY-SYSTEM-INSTRUCTIONS"}}}`,
		`{"timestamp":"2026-09-18T10:00:01.000Z","type":"turn_context","payload":{"turn_id":"T1","model":"gpt-6-astra"}}`,
		`{"timestamp":"2026-09-18T10:00:02.000Z","type":"event_msg","payload":{"type":"user_message","message":"CANARY-USER-PROMPT"}}`,
		`{"timestamp":"2026-09-18T10:00:03.000Z","type":"response_item","payload":{"type":"message","content":[{"type":"output_text","text":"CANARY-ASSISTANT-TEXT"}]}}`,
		`{"timestamp":"2026-09-18T10:00:04.000Z","type":"event_msg","payload":{"type":"token_count","info":{` +
			`"total_token_usage":{"input_tokens":10,"cached_input_tokens":2,"cache_write_input_tokens":0,"output_tokens":5,"reasoning_output_tokens":1,"total_tokens":15},` +
			`"last_token_usage":{"input_tokens":10,"cached_input_tokens":2,"cache_write_input_tokens":0,"output_tokens":5,"reasoning_output_tokens":1,"total_tokens":15}}}}`,
	}
	for _, l := range lines {
		tel.Observe("codex", "/tmp/rollout.jsonl", []byte(l))
	}
	assertNoText(t, c)
}

func TestNoTextCrossesForGemini(t *testing.T) {
	c, srv := newCapSink()
	defer srv.Close()
	tel := telFor(srv.URL+"/v1/logs", srv.URL+"/v1/metrics", map[string]bool{"gemini": true})
	dir := t.TempDir()
	path := filepath.Join(dir, "session-2026-09-18T10-00-abcdabcd.json")
	doc := `{"sessionId":"G1","messages":[` +
		`{"id":"m1","timestamp":"2026-09-18T10:00:00.000Z","type":"user","content":"CANARY-USER-PROMPT",` +
		`"displayContent":[{"text":"CANARY-DISPLAY-CONTENT"}]},` +
		`{"id":"m2","timestamp":"2026-09-18T10:00:04.000Z","type":"gemini","content":"CANARY-ASSISTANT-TEXT",` +
		`"thoughts":[{"subject":"CANARY-THINKING","description":"CANARY-THINKING"}],` +
		`"toolCalls":[{"name":"run","args":{"cmd":"CANARY-TOOL-INPUT"},"result":"CANARY-TOOL-RESULT"}],` +
		`"model":"gemini-2.5-pro","tokens":{"input":9,"output":3,"cached":4,"thoughts":2,"tool":0,"total":14}}]}`
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	tel.ObserveFile("gemini", path)
	assertNoText(t, c)
}
