package watch

import (
	"os"
	"path/filepath"
	"testing"
)

// writeGeminiTranscript writes a Gemini chat JSONL file with the given lines and
// returns its path.
func writeGeminiTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "session.jsonl")
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestGeminiExtractorEmitsTelemetryCorrID: the extractor emits
// "<sessionId>########<0-based user ordinal>" — matching Gemini's OTEL prompt_id
// — not the record UUID. $set, gemini turns, and blank prompts don't advance the
// ordinal.
func TestGeminiExtractorEmitsTelemetryCorrID(t *testing.T) {
	ex := geminiExtractor{}
	metaLine := `{"sessionId":"sess_1","projectHash":"abc","kind":"main"}`
	u0 := `{"id":"uuid-0","type":"user","content":[{"text":"first"}]}`
	g0 := `{"id":"g-0","type":"gemini","content":[{"text":"reply"}]}`
	setLine := `{"$set":{"lastUpdated":"t"}}`
	u1 := `{"id":"uuid-1","type":"user","content":[{"text":"second"}]}`
	u2 := `{"id":"uuid-2","type":"user","content":[{"text":"third"}]}`
	path := writeGeminiTranscript(t, metaLine, u0, g0, setLine, u1, u2)

	cases := []struct {
		line string
		want string
	}{
		{u0, "sess_1########0"},
		{u1, "sess_1########1"},
		{u2, "sess_1########2"},
	}
	for _, c := range cases {
		rec, ok := ex.extract(path, []byte(c.line))
		if !ok || rec.PromptID != c.want {
			t.Fatalf("extract(%s): rec=%+v ok=%v, want PromptID=%q", c.line, rec, ok, c.want)
		}
		if rec.SessionID != "sess_1" {
			t.Errorf("SessionID=%q, want sess_1", rec.SessionID)
		}
	}

	// Non-prompts are rejected.
	for _, bad := range []string{metaLine, g0, setLine, `{invalid`,
		`{"type":"user","content":[{"text":"no id"}]}`,
		`{"id":"e","type":"user","content":[]}`,
		`{"id":"w","type":"user","content":[{"text":"   "}]}`} {
		if _, ok := ex.extract(path, []byte(bad)); ok {
			t.Errorf("expected non-prompt rejected: %s", bad)
		}
	}
}

// TestGeminiExtractorForwardOnlyOrdinal: even when the extractor is handed a
// later user line first (as the forward-only watcher does when it starts
// mid-session), the ordinal is absolute because geminiPromptIndex scans from the
// file start.
func TestGeminiExtractorForwardOnlyOrdinal(t *testing.T) {
	ex := geminiExtractor{}
	path := writeGeminiTranscript(t,
		`{"sessionId":"S","kind":"main"}`,
		`{"id":"a","type":"user","content":[{"text":"one"}]}`,
		`{"id":"b","type":"user","content":[{"text":"two"}]}`,
		`{"id":"c","type":"user","content":[{"text":"three"}]}`,
	)
	// Hand it the 3rd user line directly — ordinal must still be 2.
	rec, ok := ex.extract(path, []byte(`{"id":"c","type":"user","content":[{"text":"three"}]}`))
	if !ok || rec.PromptID != "S########2" {
		t.Fatalf("rec=%+v ok=%v, want S########2", rec, ok)
	}
}
