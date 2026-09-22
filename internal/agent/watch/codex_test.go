package watch

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// codexFixturePath resolves a captured rollout. The fixtures are shared with
// the resolver's tests, which read them through the same relative path.
func codexFixturePath(name string) string { return filepath.Join(codexFixtureDir, name) }

// runCodexExtractor feeds a whole fixture through the extractor line by line,
// exactly as the watcher's scan does, and returns the pointers it produced.
func runCodexExtractor(t *testing.T, name string) (*codexExtractor, []promptRec) {
	t.Helper()
	f, err := os.Open(codexFixturePath(name))
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	defer f.Close()
	ex := newCodexExtractor()
	var got []promptRec
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	for sc.Scan() {
		line := append([]byte(nil), sc.Bytes()...)
		if rec, ok := ex.extract(codexFixturePath(name), line); ok {
			got = append(got, rec)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan %s: %v", name, err)
	}
	return ex, got
}

// codexExpectedTurns walks a fixture the way a person would and returns, for
// each human turn, the `<session>#<turn_id>` the watcher must produce and the
// cwd it must carry. It is deliberately a second, independent reading of the
// file: an oracle that shares the implementation proves nothing, which is the
// lesson `analyze_window_by_parse` was already carrying when the prompt index
// held the wrong id.
func codexExpectedTurns(t *testing.T, name string) (session string, ids []string, cwds []string) {
	t.Helper()
	f, err := os.Open(codexFixturePath(name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	var pendingTurn, pendingCwd, sessionCwd string
	for sc.Scan() {
		var line map[string]any
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			t.Fatal(err)
		}
		p, _ := line["payload"].(map[string]any)
		switch lt, _ := line["type"].(string); lt {
		case "session_meta":
			session, _ = p["id"].(string)
			sessionCwd, _ = p["cwd"].(string)
		case "turn_context":
			pendingTurn, _ = p["turn_id"].(string)
			pendingCwd, _ = p["cwd"].(string)
		case "event_msg":
			if !codexIsUserMessageEvent(line) && !codexIsUserMessageItem(line) {
				continue
			}
			turn, _ := p["turn_id"].(string)
			if turn == "" {
				turn = pendingTurn
			}
			cwd := pendingCwd
			if cwd == "" {
				cwd = sessionCwd
			}
			ids = append(ids, session+"#"+turn)
			cwds = append(cwds, cwd)
		}
	}
	return session, ids, cwds
}

// TestCodexExtractorRealRollout is AC-3 / TR-AC-3: over the captured 0.153.4
// rollout the watcher emits one pointer per `event_msg`/`user_message`, named
// `<session_meta.id>#<turn_context.turn_id>`, with the cwd from turn_context.
func TestCodexExtractorRealRollout(t *testing.T) {
	assertCodexFixtureYieldsItsTurns(t, "rollout-0.153.4.jsonl", 4)
}

// TestCodexExtractorReadsTheItemShape is the half a 0.148-0.151 machine lives
// on: the human turn arrives only as an `item_completed` `UserMessage` item.
// The old extractor read `user_message` alone and so captured NOTHING on those
// releases.
func TestCodexExtractorReadsTheItemShape(t *testing.T) {
	assertCodexFixtureYieldsItsTurns(t, "rollout-0.151.jsonl", 3)
}

// TestCodexExtractorOlderRollout keeps the oldest captured shape working: the
// classic `user_message` with no ordinal anywhere in the file.
func TestCodexExtractorOlderRollout(t *testing.T) {
	assertCodexFixtureYieldsItsTurns(t, "rollout-0.125.jsonl", 2)
}

func assertCodexFixtureYieldsItsTurns(t *testing.T, name string, wantN int) {
	t.Helper()
	ex, got := runCodexExtractor(t, name)
	session, wantIDs, wantCwds := codexExpectedTurns(t, name)

	if len(got) != wantN {
		t.Fatalf("%s: %d pointers, want %d", name, len(got), wantN)
	}
	if len(wantIDs) != wantN {
		t.Fatalf("%s: oracle found %d turns, want %d", name, len(wantIDs), wantN)
	}
	for i, rec := range got {
		if rec.PromptID != wantIDs[i] {
			t.Errorf("%s turn %d: id=%q, want %q", name, i, rec.PromptID, wantIDs[i])
		}
		if rec.SessionID != session {
			t.Errorf("%s turn %d: session=%q, want %q", name, i, rec.SessionID, session)
		}
		if rec.Cwd != wantCwds[i] {
			t.Errorf("%s turn %d: cwd=%q, want %q", name, i, rec.Cwd, wantCwds[i])
		}
		if strings.HasSuffix(rec.PromptID, "#") {
			t.Errorf("%s turn %d: empty turn id", name, i)
		}
	}
	if n := ex.TurnIDFallbacks(); n != 0 {
		t.Errorf("%s: %d timestamp fallbacks, want 0 — every turn here has a turn_context", name, n)
	}
}

// TestCodexPromptWithoutTurnContextFallsBackToTimestamp: 2 of 1,848 turns on
// the newest rollouts, and 320 of 4,076 (7.9%) across all 287, have no
// turn_context before them — 0.36-era rollouts carry no `turn_id` at all. The
// prompt is named by its own instant and COUNTED, never dropped: dropping it
// loses real work, and dropping it silently is indistinguishable from the
// ordinal bug returning.
func TestCodexPromptWithoutTurnContextFallsBackToTimestamp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rollout.jsonl")
	body := `{"timestamp":"2026-09-15T10:00:00.000Z","type":"session_meta","payload":{"id":"thread_1","cwd":"/work"}}
{"timestamp":"2026-09-15T10:00:01.000Z","type":"event_msg","payload":{"type":"user_message","message":"x"}}
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	ex := newCodexExtractor()
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	var got []promptRec
	for _, l := range lines {
		if rec, ok := ex.extract(path, []byte(l)); ok {
			got = append(got, rec)
		}
	}
	if len(got) != 1 {
		t.Fatalf("%d pointers, want 1 (the prompt must not be dropped)", len(got))
	}
	if want := "thread_1#2026-09-15T10:00:01.000Z"; got[0].PromptID != want {
		t.Errorf("id=%q, want %q", got[0].PromptID, want)
	}
	if got[0].Cwd != "/work" {
		t.Errorf("cwd=%q, want the session's", got[0].Cwd)
	}
	if n := ex.TurnIDFallbacks(); n != 1 {
		t.Errorf("fallbacks=%d, want 1", n)
	}
}

// TestCodexExtractorRecoversTurnContextFromFileHead: an incremental scan that
// starts past the head never sees session_meta OR the turn_context. Recovering
// both from the file matters — with only the session recovered the prompt
// would take the timestamp fallback while `keld __hook` named the same prompt
// by its turn id, and the queue would publish it twice under two names.
func TestCodexExtractorRecoversTurnContextFromFileHead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rollout.jsonl")
	prompt := `{"timestamp":"2026-09-15T10:00:02.000Z","type":"event_msg","payload":{"type":"user_message","message":"x"}}`
	body := `{"timestamp":"2026-09-15T10:00:00.000Z","type":"session_meta","payload":{"id":"thread_9","cwd":"/repo"}}
{"timestamp":"2026-09-15T10:00:01.000Z","type":"turn_context","payload":{"turn_id":"turn_abc","cwd":"/repo/sub"}}
` + prompt + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	ex := newCodexExtractor()
	rec, ok := ex.extract(path, []byte(prompt)) // ONLY the later line, as a tail scan would
	if !ok {
		t.Fatal("prompt not recognised")
	}
	if rec.PromptID != "thread_9#turn_abc" {
		t.Errorf("id=%q, want thread_9#turn_abc", rec.PromptID)
	}
	if rec.Cwd != "/repo/sub" {
		t.Errorf("cwd=%q, want the turn's", rec.Cwd)
	}
	if n := ex.TurnIDFallbacks(); n != 0 {
		t.Errorf("fallbacks=%d, want 0", n)
	}
}

// TestCodexExtractorRecoveryStopsAtThePrompt: a LATER turn_context describes a
// later turn. Taking the file's last one would name this prompt after work
// that had not happened yet.
func TestCodexExtractorRecoveryStopsAtThePrompt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rollout.jsonl")
	prompt := `{"timestamp":"2026-09-15T10:00:02.000Z","type":"event_msg","payload":{"type":"user_message","message":"first"}}`
	body := `{"timestamp":"2026-09-15T10:00:00.000Z","type":"session_meta","payload":{"id":"thread_9","cwd":"/repo"}}
{"timestamp":"2026-09-15T10:00:01.000Z","type":"turn_context","payload":{"turn_id":"turn_one","cwd":"/repo"}}
` + prompt + `
{"timestamp":"2026-09-15T10:00:03.000Z","type":"turn_context","payload":{"turn_id":"turn_two","cwd":"/repo"}}
{"timestamp":"2026-09-15T10:00:04.000Z","type":"event_msg","payload":{"type":"user_message","message":"second"}}
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	ex := newCodexExtractor()
	rec, ok := ex.extract(path, []byte(prompt))
	if !ok {
		t.Fatal("prompt not recognised")
	}
	if rec.PromptID != "thread_9#turn_one" {
		t.Fatalf("id=%q, want thread_9#turn_one (a later turn_context must not be used)", rec.PromptID)
	}
}

// TestCodexExtractorRejectsNonPrompts: the injected `response_item` user-role
// records are context, not human turns — 5,300 of them against 4,076 real
// prompts on the reference corpus, so reading them would more than double the
// signal with text nobody typed.
func TestCodexExtractorRejectsNonPrompts(t *testing.T) {
	ex := newCodexExtractor()
	path := "/x/rollout.jsonl"
	if _, ok := ex.extract(path, []byte(`{"type":"session_meta","payload":{"id":"thread_1","cwd":"/work"}}`)); ok {
		t.Fatal("session_meta is not a prompt")
	}
	for _, l := range []string{
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"injected"}]}}`,
		`{"type":"response_item","payload":{"type":"message","role":"assistant"}}`,
		`{"type":"event_msg","payload":{"type":"token_count","info":{}}}`,
		`{"type":"event_msg","payload":{"type":"agent_message","message":"reply"}}`,
		`{"type":"event_msg","payload":{"type":"item_completed","item":{"type":"AgentMessage","content":[{"type":"Text","text":"reply"}]}}}`,
		`{"type":"event_msg","payload":{"type":"user_message","message":""}}`,
		`not json at all`,
	} {
		if _, ok := ex.extract(path, []byte(l)); ok {
			t.Errorf("line should be rejected: %s", l)
		}
	}
}

// TestCodexIdentityNeverComesFromOrdinal: no Go code on the Codex capture path
// may read the field the old scheme was built on. `ordinal` is absent from two
// of the three captured releases and present on every line of the third,
// including its human turns — so any reader of it is either resolving nothing
// or resolving something that is not a turn.
func TestCodexIdentityNeverComesFromOrdinal(t *testing.T) {
	for _, path := range []string{"codex.go", "../resolve/codex.go"} {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if strings.Contains(string(b), `"ordinal"`) || strings.Contains(string(b), "Ordinal") {
			t.Errorf("%s still reads `ordinal`; identity is <session>#<turn_id>", path)
		}
	}
}
