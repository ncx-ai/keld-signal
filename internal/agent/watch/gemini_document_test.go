package watch

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/spool"
)

// ⚠️ **THIS FILE REPLACES gemini_test.go, WHICH TESTED A LINE EXTRACTOR FOR A
// FORMAT GEMINI HAS NEVER WRITTEN.** Those tests passed for the life of the
// feature while the watcher could not see a single real Gemini chat: the walk
// filtered on `.jsonl` and Gemini writes ONE JSON DOCUMENT per session. So the
// extractor, its whole-file ordinal rescan, and their fixtures are gone rather
// than ported — there was nothing to port them to.
//
// Measured before deleting them: 55 real chat files on one machine, 262
// messages, zero `.jsonl`.

// geminiDoc renders a chat document with the given user prompts, interleaving a
// model turn after each one the way Gemini does.
func geminiDoc(session string, prompts ...string) string {
	out := `{"sessionId":"` + session + `","projectHash":"p","messages":[`
	for i, p := range prompts {
		if i > 0 {
			out += ","
		}
		out += `{"id":"u` + itoa(i) + `","type":"user","content":"` + p + `"},`
		out += `{"id":"g` + itoa(i) + `","type":"gemini","content":"ok"}`
	}
	return out + `]}`
}

func itoa(i int) string { return string(rune('0' + i)) }

func writeChat(t *testing.T, dir, body string) string {
	t.Helper()
	chats := filepath.Join(dir, "tmp", "proj", "chats")
	if err := os.MkdirAll(chats, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(chats, "session-2026-09-17T11-54-219a0a4b.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// newDocWatcher returns a watcher over one Gemini root, collecting offers.
func newDocWatcher(t *testing.T, dir string, backfill bool) (*Watcher, *[]spool.Pointer) {
	t.Helper()
	var got []spool.Pointer
	w := New(func(p spool.Pointer) { got = append(got, p) }, nil, "test", time.Second, backfill)
	w.cursors = newCursorStoreAt(filepath.Join(t.TempDir(), "cursors.json"))
	w.discover = func() []Root {
		return []Root{{SourceID: "gemini_cli", Dir: filepath.Join(dir, "tmp", "proj", "chats")}}
	}
	return w, &got
}

// The correlation id MUST be Gemini's own OTEL id — "<sessionId>########<0-based
// ordinal>" — because Atlas joins Enrichment.corr_id to ToolEvent.prompt_id. The
// record uuid appears in no telemetry, so keying on it orphans every enrichment.
func TestGeminiDocumentOffersTheTelemetryCorrID(t *testing.T) {
	dir := t.TempDir()
	writeChat(t, dir, geminiDoc("sess-abc", "first", "second"))
	w, got := newDocWatcher(t, dir, true)

	w.pollOnce()

	if len(*got) != 2 {
		t.Fatalf("got %d offers, want one per genuine prompt: %+v", len(*got), *got)
	}
	for i, want := range []string{"sess-abc########0", "sess-abc########1"} {
		if (*got)[i].Correlation.ID != want {
			t.Errorf("offer %d corr id = %q, want %q", i, (*got)[i].Correlation.ID, want)
		}
		if (*got)[i].Correlation.SessionID != "sess-abc" {
			t.Errorf("offer %d session = %q", i, (*got)[i].Correlation.SessionID)
		}
	}
}

// ⚠️ A DOCUMENT IS REWRITTEN WHOLE ON EVERY TURN, so a cursor that counted
// bytes would re-offer the whole session each time. The cursor counts PROMPTS
// ALREADY OFFERED; only the new one is offered on the second poll.
func TestGeminiDocumentOffersEachPromptOnce(t *testing.T) {
	dir := t.TempDir()
	writeChat(t, dir, geminiDoc("sess-abc", "first"))
	w, got := newDocWatcher(t, dir, true)

	w.pollOnce()
	w.pollOnce() // unchanged file: nothing new
	if len(*got) != 1 {
		t.Fatalf("an unchanged document re-offered: %+v", *got)
	}

	writeChat(t, dir, geminiDoc("sess-abc", "first", "second"))
	w.pollOnce()
	if len(*got) != 2 {
		t.Fatalf("got %d offers after a new turn, want 2: %+v", len(*got), *got)
	}
	if (*got)[1].Correlation.ID != "sess-abc########1" {
		t.Errorf("the new turn's id = %q, want ordinal 1", (*got)[1].Correlation.ID)
	}
}

// Forward-only first sight, the same rule the line path applies at EOF: the
// prompts already on disk are history.
func TestGeminiDocumentFirstSightIsForwardOnly(t *testing.T) {
	dir := t.TempDir()
	writeChat(t, dir, geminiDoc("sess-abc", "old one", "old two"))
	w, got := newDocWatcher(t, dir, false)

	w.pollOnce()
	if len(*got) != 0 {
		t.Fatalf("forward-only first sight offered history: %+v", *got)
	}

	writeChat(t, dir, geminiDoc("sess-abc", "old one", "old two", "new"))
	w.pollOnce()
	if len(*got) != 1 || (*got)[0].Correlation.ID != "sess-abc########2" {
		t.Fatalf("want only the new prompt at ordinal 2, got %+v", *got)
	}
}

// A file caught mid-rewrite has no valid prefix. It must be skipped silently and
// re-read next poll — never offered from, and never allowed to advance a cursor
// past prompts that were really there.
func TestGeminiDocumentSkipsAHalfWrittenFile(t *testing.T) {
	dir := t.TempDir()
	writeChat(t, dir, `{"sessionId":"sess-abc","messages":[{"id":"u0",`)
	w, got := newDocWatcher(t, dir, true)

	w.pollOnce()
	if len(*got) != 0 {
		t.Fatalf("offered from a half-written document: %+v", *got)
	}

	writeChat(t, dir, geminiDoc("sess-abc", "first"))
	w.pollOnce()
	if len(*got) != 1 {
		t.Fatalf("the completed document must be read on the next poll, got %+v", *got)
	}
}

// Only Gemini's own chat files: the same tree holds a project marker, and one
// level up a vendored ripgrep binary.
func TestGeminiDocumentWalkTakesOnlyChatFiles(t *testing.T) {
	dir := t.TempDir()
	chats := filepath.Join(dir, "tmp", "proj", "chats")
	writeChat(t, dir, geminiDoc("sess-abc", "first"))
	for _, name := range []string{"notes.json", ".project_root", "rg"} {
		if err := os.WriteFile(filepath.Join(chats, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got := transcriptFiles(chats, "gemini_cli"); len(got) != 1 {
		t.Fatalf("walk returned %v, want only the session file", got)
	}
	// And a Claude root is unaffected by any of this.
	if got := transcriptFiles(chats, "claude_code"); len(got) != 0 {
		t.Fatalf("a .jsonl source must not pick up Gemini's documents: %v", got)
	}
}

// ⚠️ **A SESSION THAT BEGAN AFTER THE DAEMON DID IS NOT HISTORY, AND TREATING IT
// AS HISTORY DROPPED EVERY ONE-SHOT GEMINI RUN.** `gemini -p` writes a whole new
// session file per invocation, so its only prompt is already in the file the
// first time the watcher sees it — and forward-only first sight then skips it
// forever. There is no second chance and no hook to cover it: Gemini's
// BeforeAgent event carries no prompt id, which internal/hook treats as a silent
// no-op. Measured in the conformance chain: transcripts found and read, 2 prompt
// ids in them, 0 enrichments published.
func TestGeminiDocumentCapturesASessionStartedAfterTheWatcher(t *testing.T) {
	dir := t.TempDir()
	// The watcher exists FIRST — then the session appears, as it does when
	// somebody runs the tool on a machine Keld is already watching.
	w, got := newDocWatcher(t, dir, false)
	writeChat(t, dir, geminiDoc("sess-live", "the only prompt"))

	w.pollOnce()

	if len(*got) != 1 {
		t.Fatalf("got %d offers, want the one prompt of a session that began "+
			"after the daemon: %+v", len(*got), *got)
	}
	if (*got)[0].Correlation.ID != "sess-live########0" {
		t.Errorf("corr id = %q, want sess-live########0", (*got)[0].Correlation.ID)
	}
}

// And the rule it must not break: a session that predates the watcher IS
// history, and installing Keld must not enrich a machine's past.
func TestGeminiDocumentStillSkipsAPreexistingSession(t *testing.T) {
	dir := t.TempDir()
	writeChat(t, dir, geminiDoc("sess-old", "yesterday's prompt"))
	// Backdate it: the file must look older than the watcher that is about to
	// exist, which is what "history" means here.
	p := filepath.Join(dir, "tmp", "proj", "chats", "session-2026-09-17T11-54-219a0a4b.json")
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	w, got := newDocWatcher(t, dir, false)

	w.pollOnce()

	if len(*got) != 0 {
		t.Fatalf("a session older than the watcher was offered as new: %+v", *got)
	}
}
