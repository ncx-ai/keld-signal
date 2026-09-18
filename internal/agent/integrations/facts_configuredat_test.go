package integrations

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/config"
	"github.com/ncx-ai/keld-signal/internal/paths"
)

// ⚠️ **THE TOOLS REWRITE THEIR OWN CONFIG FILES, SO THE FILE'S MTIME IS NOT
// "WHEN SIGNAL CONFIGURED THIS TOOL".** Measured on the maintainer's machine on
// 2026-09-18: Codex wrote `hooks.state` trust entries into its own config.toml
// at session start (20:56), and Claude Code rewrote settings.json on its own
// (21:21). Each write moved the instant the restart rule treated as "when keld
// wrote this", so a tool nobody had touched started asking for a restart — and
// it asks again every time the tool writes, which is every session.
//
// The truth is keld's own record of when it last applied that tool's adapter.
// Only keld knows it, so only keld can store it: `configured_at` in
// ~/.keld/manifest.json.
//
// claudeHome lays down the machine these tests describe: a Claude Code config
// file and one session transcript, with both mtimes and the session's own start
// instant chosen by the caller.
func claudeHome(t *testing.T, configWritten, sessionStart, sessionWritten time.Time) (home, transcripts string) {
	t.Helper()
	home = isolate(t)

	cfg := filepath.Join(home, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(cfg), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg, []byte(`{"env":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(cfg, configWritten, configWritten); err != nil {
		t.Fatal(err)
	}

	transcripts = t.TempDir()
	writeSession(t, transcripts, "sess-stale", sessionStart, sessionWritten)
	return home, transcripts
}

// deps wires ReadWiring at a fixed instant against one transcript directory,
// with no session ever having forwarded telemetry — so nothing but the
// configured-at instant decides the restart question.
func configuredAtDeps(m *config.Manifest, transcripts string) Deps {
	return Deps{
		Now:            func() time.Time { return now },
		Manifest:       m,
		TranscriptDirs: func(Entry) []string { return []string{transcripts} },
		SessionForward: func(string) *time.Time { return nil },
	}
}

// The defect itself: a tool rewrites its own config AFTER its session started,
// and the row must not move. Nothing keld wrote changed.
func TestAToolRewritingItsOwnConfigDoesNotAskForARestart(t *testing.T) {
	// keld configured the tool three hours ago; the session started two hours
	// ago (so it already has the config); the tool rewrote its own file one
	// minute ago, exactly as Codex and Claude Code were observed doing.
	_, transcripts := claudeHome(t, now.Add(-time.Minute), now.Add(-2*time.Hour), now.Add(-time.Minute))
	keldWrote := now.Add(-3 * time.Hour)
	m := &config.Manifest{Tools: map[string]config.ToolManifest{
		"claude_code": {Name: "claude_code", ConfiguredAt: &keldWrote},
	}}

	e := entry(t, "claude_code")
	w := ReadWiring(e, configuredAtDeps(m, transcripts))
	got := one(t, e, Facts{Configured: true, Wiring: w})
	if got.State == RestartRequired {
		t.Fatalf("state = %q after the TOOL rewrote its own config. keld last wrote at %v and the "+
			"session started at %v, so there is nothing to restart; the rule is reading the file's "+
			"mtime (%v) as if keld had written it.",
			got.State, keldWrote, w.NewestSessionStart, w.ConfiguredAt)
	}
}

// The other side, so the fix is not just a way of never saying restart: a
// genuine keld write moves `configured_at`, and a session that predates it is
// still running on what it launched with.
func TestAKeldWriteAfterTheSessionStartedStillAsksForARestart(t *testing.T) {
	// The config FILE is old — older than the session — so only the manifest
	// can carry the fact that keld wrote an hour ago.
	_, transcripts := claudeHome(t, now.Add(-3*time.Hour), now.Add(-2*time.Hour), now.Add(-time.Minute))
	keldWrote := now.Add(-time.Hour)
	m := &config.Manifest{Tools: map[string]config.ToolManifest{
		"claude_code": {Name: "claude_code", ConfiguredAt: &keldWrote},
	}}

	e := entry(t, "claude_code")
	w := ReadWiring(e, configuredAtDeps(m, transcripts))
	got := one(t, e, Facts{Configured: true, Wiring: w})
	if got.State != RestartRequired {
		t.Fatalf("state = %q, want %q — keld wrote this tool's config at %v and the live session "+
			"started at %v, so that session is still using what it launched with",
			got.State, RestartRequired, keldWrote, w.NewestSessionStart)
	}
}

// Step 2 of the chain: a manifest with no per-tool `configured_at` — every
// machine keld configured before this field existed — falls back to the
// MANIFEST FILE's own mtime. keld is its only writer, so it is still a fact
// about keld rather than about the tool.
func TestConfiguredAtFallsBackToTheManifestFileMtime(t *testing.T) {
	_, transcripts := claudeHome(t, now.Add(-3*time.Hour), now.Add(-2*time.Hour), now.Add(-time.Minute))

	// A manifest on disk that records the tool but carries no instant.
	m := &config.Manifest{Tools: map[string]config.ToolManifest{
		"claude_code": {Name: "claude_code"},
	}}
	if err := m.Save(); err != nil {
		t.Fatal(err)
	}
	manifestWritten := now.Add(-time.Hour)
	if err := os.Chtimes(paths.ManifestPath(), manifestWritten, manifestWritten); err != nil {
		t.Fatal(err)
	}

	w := ReadWiring(entry(t, "claude_code"), configuredAtDeps(m, transcripts))
	if !w.ConfiguredAt.Equal(manifestWritten.UTC()) {
		t.Fatalf("ConfiguredAt = %v, want the manifest file's mtime %v — with no per-tool instant, "+
			"the file keld alone writes is the closest thing to the truth", w.ConfiguredAt, manifestWritten.UTC())
	}
}

// Step 3, and the last: no manifest file at all, so the tool's own config mtime
// is all there is. It is the proxy this work replaces — kept deliberately,
// because it is what every machine written by an older keld still has, and a
// zero here would turn `restart_required` off for all of them at once.
func TestConfiguredAtFallsBackToTheToolConfigMtime(t *testing.T) {
	configWritten := now.Add(-90 * time.Minute)
	_, transcripts := claudeHome(t, configWritten, now.Add(-2*time.Hour), now.Add(-time.Minute))

	// Nothing saved: paths.ManifestPath() does not exist in this HOME.
	if _, err := os.Stat(paths.ManifestPath()); !os.IsNotExist(err) {
		t.Fatalf("this test needs no manifest on disk; stat said %v", err)
	}
	m := &config.Manifest{Tools: map[string]config.ToolManifest{"claude_code": {Name: "claude_code"}}}

	w := ReadWiring(entry(t, "claude_code"), configuredAtDeps(m, transcripts))
	if !w.ConfiguredAt.Equal(configWritten.UTC()) {
		t.Fatalf("ConfiguredAt = %v, want the tool config's mtime %v — the last resort must still "+
			"answer, or an older keld's machines stop reporting restarts entirely",
			w.ConfiguredAt, configWritten.UTC())
	}
}
