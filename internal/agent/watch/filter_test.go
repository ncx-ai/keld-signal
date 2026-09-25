package watch

import "testing"

func TestParsePrompt(t *testing.T) {
	cases := []struct {
		name string
		line string
		ok   bool
		id   string
		cwd  string
	}{
		{"string content", `{"type":"user","promptId":"P1","cwd":"/w","sessionId":"S1","message":{"role":"user","content":"hello"}}`, true, "P1", "/w"},
		{"text block", `{"type":"user","promptId":"P2","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}`, true, "P2", ""},
		{"tool_result block rejected", `{"type":"user","promptId":"P3","message":{"role":"user","content":[{"type":"tool_result","content":"out"}]}}`, false, "", ""},
		{"toolUseResult record rejected", `{"type":"user","promptId":"P3b","toolUseResult":{"ok":true},"message":{"role":"user","content":"result text"}}`, false, "", ""},
		{"sidechain rejected", `{"type":"user","promptId":"P3c","isSidechain":true,"message":{"role":"user","content":"subagent turn"}}`, false, "", ""},
		{"meta rejected", `{"type":"user","promptId":"P3d","isMeta":true,"message":{"role":"user","content":"injected caveat"}}`, false, "", ""},
		{"assistant rejected", `{"type":"assistant","promptId":"P4","message":{"role":"assistant","content":"x"}}`, false, "", ""},
		{"missing promptId rejected", `{"type":"user","message":{"role":"user","content":"hi"}}`, false, "", ""},
		{"empty string content rejected", `{"type":"user","promptId":"P5","message":{"role":"user","content":""}}`, false, "", ""},
		{"malformed rejected", `{not json`, false, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec, ok := parsePrompt([]byte(c.line))
			if ok != c.ok {
				t.Fatalf("ok=%v want %v", ok, c.ok)
			}
			if ok && (rec.PromptID != c.id || rec.Cwd != c.cwd) {
				t.Fatalf("rec=%+v want id=%q cwd=%q", rec, c.id, c.cwd)
			}
		})
	}
}

// TestHumanPromptIDIsThePromptIdFieldNotTheUUID pins the daemon's half of the
// Go<->sidecar prompt-id contract.
//
// ⚠️ THIS SEAM HAD NO TEST ON EITHER SIDE, and the gap cost every workstream
// facet on every prompt. A Claude Code user line carries TWO ids: `uuid`
// (unique per line) and `promptId` (the human TURN's identity, shared by its
// follow-on lines). The daemon names a prompt by `promptId` — this function,
// the spool pointer, the queue dedup key, and `corr_id` on the wire, which
// Atlas joins against ToolEvent.prompt_id. The sidecar's `prompt` index used
// to hold only `uuid`, so every /analyze lookup 404'd, the workstreams pass
// failed, and 8 of 8 live prompts published `pipeline_status:"partial"` with
// no workstreams, no dynamics and no prior.
//
// Nothing caught it: the sidecar was self-consistent (its index AND its oracle
// scan both used uuid, so their equality test compared two identical wrong
// answers), and its fixtures built user turns carrying no `promptId` at all.
// The counterpart test is sidecar/app/test_prompt_id_seam.py.
//
// If this ever needs to return `uuid` instead, `sidecar/app/analysis/ingest.py`
// must change in the same commit — and note `resolve.claude` already accepts
// EITHER id when reading the prompt's text, which is the shape to copy.
func TestHumanPromptIDIsThePromptIdFieldNotTheUUID(t *testing.T) {
	line := []byte(`{"type":"user","uuid":"U-should-not-be-used","promptId":"P-the-contract",` +
		`"cwd":"/w","sessionId":"S1","message":{"role":"user","content":"hello"}}`)

	got, ok := HumanPromptID(line)
	if !ok {
		t.Fatal("HumanPromptID rejected a genuine human prompt line")
	}
	if got == "U-should-not-be-used" {
		t.Fatal("HumanPromptID returned the line uuid; the sidecar's prompt index and " +
			"Atlas's corr_id join are both keyed on promptId")
	}
	if got != "P-the-contract" {
		t.Fatalf("HumanPromptID = %q, want the promptId field %q", got, "P-the-contract")
	}
}

// A line with a uuid but NO promptId is not a human prompt the daemon can name.
// Stated so the fix for the seam is never "fall back to uuid here": the sidecar
// indexes both ids, so the daemon does not need a fallback, and adding one would
// start firing enrichment on lines the human-prompt filter exists to exclude.
func TestAUUIDIsNotAFallbackForAMissingPromptId(t *testing.T) {
	line := []byte(`{"type":"user","uuid":"U-only","message":{"role":"user","content":"hi"}}`)
	if id, ok := HumanPromptID(line); ok {
		t.Fatalf("accepted a line with no promptId, returning %q", id)
	}
}
