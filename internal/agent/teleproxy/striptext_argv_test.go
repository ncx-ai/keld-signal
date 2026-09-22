package teleproxy

import (
	"encoding/json"
	"strings"
	"testing"
)

// A tool's own ARGV is not metadata: it is whatever the person typed.
//
// ⚠️ MEASURED BREACH, 2026-09-15. Gemini CLI sets the OTLP resource attribute
// `process.command_args` to its full argv, and `gemini -p "<prompt>"` puts the
// prompt there. `textKey` matched none of its text words against that key, so
// the proxy forwarded the prompt to Atlas verbatim — a breach of this repo's
// top-line invariant, from a tool `keld signal setup` configures by default.
//
// It is the same class as the `prompt.id` incident one direction over: that gate
// over-matched and broke correlation, this one under-matched and leaked text.
// Both were invisible because a captured payload was in no fixture. The fixture
// is `testdata/otlp-gemini-cli-logs.json`, and the attribute is blanked there
// precisely because a real capture carried a real prompt.
func TestArgvAttributesAreStripped(t *testing.T) {
	for _, k := range []string{
		"process.command_args",
		"process.command",
		"process.command_line",
		"process.executable.path",
		"PROCESS.COMMAND_ARGS",
	} {
		if !textKey(k) {
			t.Errorf("textKey(%q) = false; argv carries whatever the person typed and must never cross", k)
		}
	}
}

// Neighbouring process attributes are not argv and stay: they are how a fleet
// view tells one tool from another, and blanking them buys no privacy.
// Claude Code's assistant-response attribute is the bare key `response`, and the
// gate only knew `response.text` — a spelling from imagination rather than from a
// capture. The tool redacts it by default, so the value in our fixture is
// `<REDACTED>`; a managed settings file that sets OTEL_LOG_ASSISTANT_RESPONSES=1
// makes it real, and the proxy would have forwarded it. The proxy must not depend
// on the tool's own default to uphold this repo's invariant.
func TestBareResponseAndPromptKeysAreStripped(t *testing.T) {
	for _, k := range []string{"response", "prompt", "user_prompt", "assistant_response"} {
		if !textKey(k) {
			t.Errorf("textKey(%q) = false; this is message text", k)
		}
	}
}

// …but their measured siblings are not text and must survive, or the proxy
// blanks the counts Atlas bills on. This is the `prompt.id` lesson: the rule is
// two-sided, and dropping the second half costs correlation and cost data.
func TestResponseAndPromptMeasuresSurvive(t *testing.T) {
	for _, k := range []string{
		"prompt.id", "prompt_length", "response_length", "response_id",
		"input_tokens", "output_tokens", "cost_usd", "duration_ms",
	} {
		if textKey(k) {
			t.Errorf("textKey(%q) = true; this is a measure or an identifier, not text", k)
		}
	}
}

func TestNonArgvProcessAttributesSurvive(t *testing.T) {
	for _, k := range []string{
		"process.pid", "process.runtime.name", "process.runtime.version",
		"process.executable.name", "service.name", "service.version",
	} {
		if textKey(k) {
			t.Errorf("textKey(%q) = true; this is tool identity, not text", k)
		}
	}
}

// End to end over the real captured Gemini resource shape: a prompt planted in
// the argv attribute does not survive StripText.
func TestGeminiArgvPromptDoesNotSurviveTheProxy(t *testing.T) {
	const canary = "CANARYPHRASE quantum badger"
	body := []byte(`{"resourceLogs":[{"resource":{"attributes":[
      {"key":"service.name","value":{"stringValue":"gemini-cli"}},
      {"key":"process.command_args","value":{"arrayValue":{"values":[
        {"stringValue":"/opt/homebrew/bin/gemini"},
        {"stringValue":"-p"},
        {"stringValue":"` + canary + `"}]}}}
    ]},"scopeLogs":[{"logRecords":[{"body":{"stringValue":"ok"}}]}]}]}`)

	out := StripText(body)
	if strings.Contains(string(out), "CANARYPHRASE") {
		t.Fatalf("the prompt survived the proxy and would reach Atlas:\n%s", out)
	}
	var v any
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("stripped payload is not valid JSON: %v", err)
	}
	if !strings.Contains(string(out), "gemini-cli") {
		t.Fatal("service.name was stripped too; the source would become unknown")
	}
}
