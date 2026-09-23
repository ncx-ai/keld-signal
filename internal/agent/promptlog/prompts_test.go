package promptlog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ⚠️ ONE user_prompt PER PROMPT, NOT PER LINE. A Claude Code human turn is several
// user lines sharing one promptId, and two of them routinely pass the
// genuine-prompt filter. Measured 2026-09-21: 19 user_prompt rows in Atlas for
// 16 distinct prompt.ids — the Prompts KPI over-counting by 19%.
func TestClaudeUserPromptIsEmittedOncePerPromptId(t *testing.T) {
	c, srv := newCapSink()
	defer srv.Close()
	tel := telFor(srv.URL+"/v1/logs", srv.URL+"/v1/metrics", map[string]bool{"claude_code": true})
	tp := filepath.Join(t.TempDir(), "s.jsonl")
	user := func(pid, uuid, ts, text string) string {
		return `{"type":"user","promptId":"` + pid + `","uuid":"` + uuid + `","sessionId":"S1","version":"2.1.216","timestamp":"` + ts + `","message":{"role":"user","content":"` + text + `"}}`
	}
	// The same prompt, three lines: the text, and two continuations minutes later.
	tel.Observe("claude_code", tp, []byte(user("P1", "u1", "2026-09-21T11:16:49Z", "please fix it")))
	tel.Observe("claude_code", tp, []byte(user("P1", "u2", "2026-09-21T11:16:49Z", "please fix it")))
	tel.Observe("claude_code", tp, []byte(user("P1", "u3", "2026-09-21T11:19:02Z", "continued")))
	// A genuinely new prompt.
	tel.Observe("claude_code", tp, []byte(user("P2", "u4", "2026-09-21T11:25:00Z", "next thing")))

	recs := only(t, flatten(t, c.bodies("/v1/logs")), "user_prompt")
	if len(recs) != 2 {
		t.Fatalf("expected 2 user_prompt records for 2 prompts across 4 lines, got %d — the Prompts KPI over-counts", len(recs))
	}
	wantStr(t, recs[0], "prompt.id", "P1")
	wantStr(t, recs[1], "prompt.id", "P2")
}

// A continuation line that is NOT re-emitted must still re-anchor the prompt.id
// linkage, or the api_requests that follow it lose their correlation.
func TestClaudeContinuationStillAnchorsLaterRequests(t *testing.T) {
	c, srv := newCapSink()
	defer srv.Close()
	tel := telFor(srv.URL+"/v1/logs", srv.URL+"/v1/metrics", map[string]bool{"claude_code": true})
	tp := filepath.Join(t.TempDir(), "s.jsonl")
	tel.Observe("claude_code", tp, []byte(`{"type":"user","promptId":"P1","uuid":"u1","sessionId":"S1","timestamp":"2026-09-21T11:00:00Z","message":{"role":"user","content":"go"}}`))
	tel.Observe("claude_code", tp, []byte(`{"type":"user","promptId":"P1","uuid":"u2","sessionId":"S1","timestamp":"2026-09-21T11:00:01Z","message":{"role":"user","content":"go"}}`))
	tel.Observe("claude_code", tp, []byte(`{"type":"assistant","requestId":"req_1","uuid":"a1","sessionId":"S1","timestamp":"2026-09-21T11:00:05Z","message":{"role":"assistant","model":"claude-opus-5","usage":{"input_tokens":10,"output_tokens":5},"content":[{"type":"text","text":"ok"}]}}`))

	recs := only(t, flatten(t, c.bodies("/v1/logs")), "api_request")
	if len(recs) != 1 {
		t.Fatalf("expected 1 api_request, got %d", len(recs))
	}
	wantStr(t, recs[0], "prompt.id", "P1")
}

func codexMeta(id, threadSource string) string {
	return `{"timestamp":"2026-09-21T10:23:04.143Z","ordinal":0,"type":"session_meta","payload":{"id":"` + id + `","session_id":"01a0c37b-parent","cli_version":"0.155.1","originator":"codex-tui","thread_source":"` + threadSource + `","source":"cli"}}`
}

const codexTurn = `{"timestamp":"2026-09-21T10:23:05.000Z","ordinal":1,"type":"turn_context","payload":{"turn_id":"t1","model":"gpt-6-astra","cwd":"/w"}}`
const codexPrompt = `{"timestamp":"2026-09-21T10:23:06.000Z","ordinal":2,"type":"event_msg","payload":{"type":"user_message","message":"CANARY-CODEX-PROMPT please refactor"}}`
const codexTokens = `{"timestamp":"2026-09-21T10:23:15.277Z","ordinal":17,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":27686,"cached_input_tokens":23680,"output_tokens":68,"total_tokens":27754},"last_token_usage":{"input_tokens":27686,"cached_input_tokens":23680,"output_tokens":68,"total_tokens":27754}}}}`

// ⚠️ THE OTLP LANE SENT user_prompt FOR CODEX AND THE MIRROR DID NOT, so the
// Prompts KPI read ZERO for Codex on every transcript-first machine while its
// spend stayed exact.
func TestCodexMirrorsHumanTurnsAsUserPromptWithoutText(t *testing.T) {
	c, srv := newCapSink()
	defer srv.Close()
	tel := telFor(srv.URL+"/v1/logs", srv.URL+"/v1/metrics", map[string]bool{"codex": true})
	tp := filepath.Join(t.TempDir(), "rollout.jsonl")
	for _, l := range []string{codexMeta("01a0c37b-parent", "user"), codexTurn, codexPrompt, codexTokens} {
		tel.Observe("codex", tp, []byte(l))
	}
	all := flatten(t, c.bodies("/v1/logs"))
	prompts := only(t, all, "codex.user_prompt")
	if len(prompts) != 1 {
		t.Fatalf("expected 1 codex.user_prompt, got %d", len(prompts))
	}
	r := prompts[0]
	wantStr(t, r, "conversation.id", "01a0c37b-parent")
	wantStr(t, r, "model", "gpt-6-astra")
	wantStr(t, r, "app.version", "0.155.1")
	wantStr(t, r, "turn.id", "t1")
	wantStr(t, r, "request_id", "01a0c37b-parent@2026-09-21T10:23:06.000Z#2")
	wantInt(t, r, "prompt_length", "35")
	for _, b := range c.bodies("/v1/logs") {
		if strings.Contains(b, "CANARY-CODEX-PROMPT") || strings.Contains(b, "refactor") {
			t.Fatalf("prompt text crossed the wire: %s", b)
		}
	}
	if n := len(only(t, all, "codex.sse_event")); n != 1 {
		t.Fatalf("the priced record must still be emitted beside the prompt; got %d sse_event", n)
	}
}

// The item-model shape (0.148+) is a human turn too — same predicate as the watcher.
func TestCodexItemCompletedUserMessageIsAPrompt(t *testing.T) {
	c, srv := newCapSink()
	defer srv.Close()
	tel := telFor(srv.URL+"/v1/logs", srv.URL+"/v1/metrics", map[string]bool{"codex": true})
	tp := filepath.Join(t.TempDir(), "rollout.jsonl")
	item := `{"timestamp":"2026-09-21T10:23:06.000Z","ordinal":2,"type":"event_msg","payload":{"type":"item_completed","turn_id":"t9","item":{"type":"UserMessage","content":[{"type":"text","text":"hello there"}]}}}`
	for _, l := range []string{codexMeta("01a0c37b-parent", "user"), item} {
		tel.Observe("codex", tp, []byte(l))
	}
	prompts := only(t, flatten(t, c.bodies("/v1/logs")), "codex.user_prompt")
	if len(prompts) != 1 {
		t.Fatalf("expected 1 codex.user_prompt from the item_completed shape, got %d", len(prompts))
	}
	wantStr(t, prompts[0], "turn.id", "t9")
	wantInt(t, prompts[0], "prompt_length", "11")
}

// ⚠️ A SUB-AGENT ROLLOUT'S user_message IS THE PARENT AGENT TALKING, NOT A PERSON.
// Measured 2026-09-21: 3 of one afternoon's 4 rollouts were sub-agents carrying 10
// user_message lines against the person's own 9. Their spend is still priced.
func TestCodexSubagentRolloutPricesButDoesNotCountPrompts(t *testing.T) {
	c, srv := newCapSink()
	defer srv.Close()
	tel := telFor(srv.URL+"/v1/logs", srv.URL+"/v1/metrics", map[string]bool{"codex": true})
	tp := filepath.Join(t.TempDir(), "sub.jsonl")
	for _, l := range []string{codexMeta("01a0c390-child", "subagent"), codexTurn, codexPrompt, codexTokens} {
		tel.Observe("codex", tp, []byte(l))
	}
	all := flatten(t, c.bodies("/v1/logs"))
	if n := len(only(t, all, "codex.user_prompt")); n != 0 {
		t.Fatalf("a sub-agent's instruction was counted as a human prompt (%d user_prompt)", n)
	}
	priced := only(t, all, "codex.sse_event")
	if len(priced) != 1 {
		t.Fatalf("sub-agent spend must still be priced; got %d sse_event", len(priced))
	}
}

// Started reading mid-file (a daemon restart): the head seed must carry the
// sub-agent marker too, or a restart turns every sub-agent into a person.
func TestCodexSubagentMarkerSurvivesAHeadSeed(t *testing.T) {
	c, srv := newCapSink()
	defer srv.Close()
	tel := telFor(srv.URL+"/v1/logs", srv.URL+"/v1/metrics", map[string]bool{"codex": true})
	tp := filepath.Join(t.TempDir(), "sub.jsonl")
	writeLines(t, tp, codexMeta("01a0c390-child", "subagent"), codexTurn, codexPrompt)
	// The mirror is handed ONLY the prompt line — it never saw the head.
	tel.Observe("codex", tp, []byte(codexPrompt))
	if n := len(only(t, flatten(t, c.bodies("/v1/logs")), "codex.user_prompt")); n != 0 {
		t.Fatalf("head-seeded sub-agent rollout counted %d prompt(s)", n)
	}
}

func writeLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}
