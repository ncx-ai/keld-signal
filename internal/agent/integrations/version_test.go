package integrations

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeTranscript writes lines to <dir>/<name> and gives it a modtime, so a
// test can control which transcript is the NEWEST without sleeping.
func writeTranscript(t *testing.T, dir, name string, mod time.Time, lines ...string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	body := ""
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mod, mod); err != nil {
		t.Fatal(err)
	}
	return p
}

// The three head shapes, captured from real transcripts on 2026-09-15 (see
// testdata/). Claude Code opens with THREE untimestamped records and carries
// `version` only from its fourth line; Codex carries everything on its first.
const (
	claudeHead = `{"mode":"normal","sessionId":"s1","type":"mode"}
{"sessionId":"s1","title":"t","type":"custom-title"}
{"isSnapshotUpdate":false,"messageId":"seed","snapshot":{"messageId":"seed"},"type":"file-history-snapshot"}
{"type":"user","sessionId":"s1","promptId":"p1","uuid":"u1","version":"2.1.271","timestamp":"2026-09-15T14:31:44.754Z"}`

	codexHead = `{"timestamp":"2025-09-20T21:38:35.534Z","type":"session_meta","payload":{"id":"95d6c92c","cwd":"/tmp","originator":"codex_cli_rs","cli_version":"0.153.4"}}
{"timestamp":"2025-09-20T21:38:36.000Z","type":"response_item","payload":{"type":"message","role":"user"}}`

	piHead = `{"version":3,"timestamp":"2026-09-14T08:00:00Z","model":"mock/mock-1"}
{"role":"user","timestamp":"2026-09-14T08:00:01Z"}`
)

func TestToolVersionReadsEachToolsOwnField(t *testing.T) {
	root := t.TempDir()
	claude := filepath.Join(root, "claude")
	codex := filepath.Join(root, "codex")
	pi := filepath.Join(root, "pi")
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	writeTranscript(t, claude, "a.jsonl", now, claudeHead)
	writeTranscript(t, codex, "rollout.jsonl", now, codexHead)
	writeTranscript(t, pi, "s.jsonl", now, piHead)

	dirs := map[string][]string{"claude_code": {claude}, "codex": {codex}, "pi": {pi}}
	d := Deps{TranscriptDirs: func(e Entry) []string { return dirs[e.ID] }}

	for _, tc := range []struct{ id, want string }{
		{"claude_code", "2.1.271"},
		{"codex", "0.153.4"},
		{"pi", "3"},
	} {
		e, ok := Get(tc.id)
		if !ok {
			t.Fatalf("catalogue has no %s", tc.id)
		}
		if got := ToolVersion(e, d); got != tc.want {
			t.Errorf("%s: ToolVersion = %q, want %q", tc.id, got, tc.want)
		}
	}
}

// AC-7: absent reads as "", never a guess. Three ways to be absent — no
// transcript directory at all, an empty directory, and a transcript whose
// records simply do not carry the field.
func TestToolVersionIsEmptyWhenUnknownNeverGuessed(t *testing.T) {
	root := t.TempDir()
	empty := filepath.Join(root, "empty")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	noField := filepath.Join(root, "nofield")
	writeTranscript(t, noField, "a.jsonl", time.Now(),
		`{"type":"user","sessionId":"s1","timestamp":"2026-09-15T14:31:44.754Z"}`)

	e, _ := Get("claude_code")
	for name, dirs := range map[string][]string{
		"no directory":    {filepath.Join(root, "missing")},
		"empty directory": {empty},
		"field absent":    {noField},
	} {
		d := Deps{TranscriptDirs: func(Entry) []string { return dirs }}
		if got := ToolVersion(e, d); got != "" {
			t.Errorf("%s: ToolVersion = %q, want \"\"", name, got)
		}
	}
}

// The newest transcript wins, not the first one listed.
func TestToolVersionReadsTheNEWESTTranscript(t *testing.T) {
	dir := t.TempDir()
	old := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	writeTranscript(t, dir, "old.jsonl", old,
		`{"type":"user","version":"2.0.0","timestamp":"2026-09-01T00:00:00Z"}`)
	writeTranscript(t, dir, "new.jsonl", old.Add(48*time.Hour),
		`{"type":"user","version":"2.1.271","timestamp":"2026-09-03T00:00:00Z"}`)

	e, _ := Get("claude_code")
	d := Deps{TranscriptDirs: func(Entry) []string { return []string{dir} }}
	if got := ToolVersion(e, d); got != "2.1.271" {
		t.Fatalf("ToolVersion = %q, want the newest transcript's 2.1.271", got)
	}
}

// ⚠️ The trap `capture.scan` documents, one package over: Claude Code opens a
// transcript with `mode` / `custom-title` / `file-history-snapshot` records
// that carry NO top-level timestamp, and `file-history-snapshot` carries a
// NESTED one. Pattern-matching the first line — or the first `"timestamp"`
// anywhere in it — reads neither the session's start nor any real instant.
func TestNewestSessionStartDecodesForATopLevelTimestamp(t *testing.T) {
	dir := t.TempDir()
	writeTranscript(t, dir, "a.jsonl", time.Now(),
		`{"mode":"normal","sessionId":"s1","type":"mode"}`,
		`{"isSnapshotUpdate":false,"snapshot":{"timestamp":"1999-01-01T00:00:00Z"},"type":"file-history-snapshot"}`,
		`{"type":"user","sessionId":"s1","timestamp":"2026-09-15T14:31:44.754Z"}`,
		`{"type":"user","sessionId":"s1","timestamp":"2026-09-15T14:40:00Z"}`)

	got := newestSessionStart([]string{dir})
	want := time.Date(2026, 9, 15, 14, 31, 44, 754000000, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("newestSessionStart = %v, want %v (the first TOP-LEVEL timestamp)", got, want)
	}
}

func TestNewestSessionStartIsZeroWhenNothingIsTimestamped(t *testing.T) {
	dir := t.TempDir()
	writeTranscript(t, dir, "a.jsonl", time.Now(),
		`{"mode":"normal","type":"mode"}`,
		`not json at all`)
	if got := newestSessionStart([]string{dir}); !got.IsZero() {
		t.Fatalf("newestSessionStart = %v, want the zero instant (unknown)", got)
	}
}
