package teleproxy

import (
	"encoding/json"
	"os"
	"testing"
)

// TestStripTextKeepsCorrelationIDs pins the distinction the text gate got wrong:
// it must remove PROSE while leaving the IDENTIFIERS Atlas joins on.
//
// ⚠️ THE FIXTURE IS A REAL CLAUDE CODE PAYLOAD, captured off the wire, with the
// two values this test cares about restored and every identity value (email,
// account/org ids, request ids) replaced with placeholders — the SHAPE is what
// the gate is judged against, and a fixture is not a place for real identities. An invented payload is what let
// the original gate ship: `prompt.id` was never in a test, so over-matching
// "prompt" looked like it cost one dropped attribute. Measured against the live
// dev Atlas it cost every correlation — 244 tool_result and 19 user_prompt rows
// from one proxied session, 0 with a non-empty prompt_id, while unproxied seed
// rows kept theirs.
func TestStripTextKeepsCorrelationIDs(t *testing.T) {
	body, err := os.ReadFile("testdata/claude_code_logs.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	out := StripText(body)

	if Contains(out, "refactor the billing module") {
		t.Error("prompt TEXT survived StripText — the privacy invariant is broken")
	}
	const promptID = "b7a1e3c0-11d2-4f8e-9a3b-5c6d7e8f9012"
	if !Contains(out, promptID) {
		t.Errorf("prompt.id was erased; Atlas joins Enrichment.corr_id to "+
			"ToolEvent.prompt_id, so blanking it silently empties every "+
			"activity view (looked for %s)", promptID)
	}
	// Still valid OTLP after the rewrite.
	var v any
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("StripText produced invalid JSON: %v", err)
	}
	// The session id is an identifier too and must be untouched.
	if !Contains(out, "39b953bc-6e27-45f5-9b80-820a08c984a3") {
		t.Error("session.id was erased")
	}
}

func TestTextKeySeparatesProseFromIdentifiers(t *testing.T) {
	text := []string{
		"prompt", "completion", "message.content", "response.text",
		"input.text", "output.text", "user_text", "assistant_text",
		"user_prompt_text", "gen_ai.prompt",
	}
	notText := []string{
		"prompt.id", "prompt_id", "prompt.ids", "prompt_length",
		"prompt.length", "prompt_tokens", "completion_tokens",
		"prompt_count", "prompt.hash", "session.id", "model", "duration_ms",
	}
	for _, k := range text {
		if !textKey(k) {
			t.Errorf("textKey(%q) = false, want true (prose must be stripped)", k)
		}
	}
	for _, k := range notText {
		if textKey(k) {
			t.Errorf("textKey(%q) = true, want false (identifiers and measures must survive)", k)
		}
	}
}
