// Package tools contains byte-golden parity tests that verify the Go adapters
// produce output identical to the Python CLI reference for the same inputs.
package tools

import (
	"fmt"
	"os"
	"testing"
)

// TestGoldenParity asserts byte-for-byte equality between the Go adapters'
// Apply output and the golden files captured from the Python CLI.
//
// The goldens were captured with Python's telemetry.hook_command monkeypatched
// to return the same string as Go's HookCommand so that the one intentional
// difference (binary hook vs python3 script) is resolved, and any remaining
// diff is a real parity bug in the Go side.
//
// Inputs used for capture (must be identical here):
//   - endpoint=https://atlas.keld.co, ingest_token=tok, actor=me
//   - claude CUR: "{\n  \"model\": \"x\"\n}\n"
//   - codex CUR:  nil (fresh file)
//   - gemini CUR: "{\n  \"theme\": \"dark\"\n}\n"
func TestGoldenParity(t *testing.T) {
	p := SetupParams{
		Endpoint:    "https://atlas.keld.co",
		IngestToken: "tok",
		Actor:       "me",
	}

	t.Run("claude", func(t *testing.T) {
		want, err := os.ReadFile("testdata/golden/claude_apply.json")
		if err != nil {
			t.Fatalf("could not read golden: %v", err)
		}
		cur := "{\n  \"model\": \"x\"\n}\n"
		got := (&ClaudeAdapter{}).Apply(&cur, p, false).AfterText
		if got != string(want) {
			t.Fatalf("claude mismatch:\n--got--\n%s\n--want--\n%s\n--diff--\n%s",
				got, string(want), byteDiff(got, string(want)))
		}
	})

	t.Run("codex", func(t *testing.T) {
		want, err := os.ReadFile("testdata/golden/codex_apply.toml")
		if err != nil {
			t.Fatalf("could not read golden: %v", err)
		}
		got := (&CodexAdapter{}).Apply(nil, p, false).AfterText
		if got != string(want) {
			t.Fatalf("codex mismatch:\n--got--\n%s\n--want--\n%s\n--diff--\n%s",
				got, string(want), byteDiff(got, string(want)))
		}
	})

	t.Run("gemini", func(t *testing.T) {
		want, err := os.ReadFile("testdata/golden/gemini_apply.json")
		if err != nil {
			t.Fatalf("could not read golden: %v", err)
		}
		// Sandbox $HOME: GeminiAdapter.Apply performs a real read/write of
		// ~/.gemini/.env as a side effect. Without this, the test would touch
		// the developer's real .env (which may hold a live GEMINI_API_KEY).
		t.Setenv("HOME", t.TempDir())
		cur := "{\n  \"theme\": \"dark\"\n}\n"
		got := (&GeminiAdapter{}).Apply(&cur, p, false).AfterText
		if got != string(want) {
			t.Fatalf("gemini mismatch:\n--got--\n%s\n--want--\n%s\n--diff--\n%s",
				got, string(want), byteDiff(got, string(want)))
		}
	})
}

// byteDiff returns a simple line-by-line diff hint showing the first
// position where got and want diverge, to assist debugging mismatches.
func byteDiff(got, want string) string {
	if got == want {
		return "(identical)"
	}
	// Find first differing byte position.
	minLen := len(got)
	if len(want) < minLen {
		minLen = len(want)
	}
	for i := 0; i < minLen; i++ {
		if got[i] != want[i] {
			return fmt.Sprintf("first difference at byte %d: got 0x%02x (%q), want 0x%02x (%q)",
				i, got[i], string(got[i]), want[i], string(want[i]))
		}
	}
	return fmt.Sprintf("got len=%d, want len=%d (got is %s)",
		len(got), len(want),
		func() string {
			if len(got) > len(want) {
				return "longer"
			}
			return "shorter"
		}())
}
