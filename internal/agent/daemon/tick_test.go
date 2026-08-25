package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/publish"
	"github.com/ncx-ai/keld-signal/internal/agent/queue"
	"os/exec"
)

// --- doubles ---------------------------------------------------------------

type fakeTicker struct {
	calls   []fakeTickCall
	windows []enrich.WindowCharacterisation
	cursor  float64
	ok      bool
}

type fakeTickCall struct {
	path      string
	session   string
	promptIDs []string
	cursor    *float64
	now       time.Time
	span      float64
	resolved  enrich.ResolvedFacts
}

func (f *fakeTicker) TickCharacterised(path, source, sessionID string, promptIDs []string,
	cursor *float64, now time.Time, spanMinutes float64, maxWindows int,
	resolved enrich.ResolvedFacts) ([]enrich.WindowCharacterisation, float64, bool) {
	f.calls = append(f.calls, fakeTickCall{path, sessionID, append([]string(nil), promptIDs...),
		cursor, now, spanMinutes, resolved})
	if !f.ok {
		return nil, 0, false
	}
	return f.windows, f.cursor, true
}

type fakeWindowSender struct {
	sent []publish.WindowEnrichment
	err  error
}

func (f *fakeWindowSender) SendWindow(e publish.WindowEnrichment) error {
	if f.err != nil {
		return f.err
	}
	f.sent = append(f.sent, e)
	return nil
}

func aWindow(session, end string) enrich.WindowCharacterisation {
	return enrich.WindowCharacterisation{
		SessionID: session, Source: "claude_code",
		Ref: enrich.WindowRef{Start: "2026-08-19T12:52:36Z", End: end,
			SpanMinutes: 54.1, Evidence: 63},
		Analysis: enrich.WindowAnalysis{
			Workstreams: map[string]enrich.Labeled{"branch": {Value: "main", Confidence: 1}},
		},
	}
}

func aJob(path, promptID string) queue.Job {
	return queue.Job{Source: "claude_code", TranscriptPath: path, PromptID: promptID,
		SessionID: "sess-1", ID: promptID, Scheme: "prompt_id"}
}

func tickFixture(t *testing.T) *tickState {
	t.Helper()
	return newTickState(filepath.Join(t.TempDir(), "tick.json"))
}

// --- what the tick remembers -----------------------------------------------

// The covered set is the prompts enrichment took on, and it must come from here
// rather than from the sidecar's store: that store's prompt index holds every
// user- AND assistant-shaped turn, so planning against it computes a covered set
// that swallows the session and the tick emits nothing at all.
func TestTheTickIsToldWhichPromptsEnrichmentAlreadyCovered(t *testing.T) {
	st := tickFixture(t)
	st.observe(aJob("/t/a.jsonl", "P1"))
	st.observe(aJob("/t/a.jsonl", "P2"))
	st.observe(aJob("/t/a.jsonl", "P1")) // a retry must not double-count

	tk := &fakeTicker{ok: true}
	tickOnce(context.Background(), st, tk, &fakeWindowSender{}, "me", time.Now(), nil, nil)

	if len(tk.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(tk.calls))
	}
	got := tk.calls[0]
	if len(got.promptIDs) != 2 || got.promptIDs[0] != "P1" || got.promptIDs[1] != "P2" {
		t.Fatalf("prompt ids = %v, want [P1 P2] exactly once each", got.promptIDs)
	}
	if got.session != "sess-1" || got.span != enrich.WindowSpanMinutes {
		t.Fatalf("call = %+v", got)
	}
	if got.cursor != nil {
		t.Errorf("a never-ticked transcript must start at the frontier (nil cursor), got %v",
			*got.cursor)
	}
}

// A source the window analysis cannot read (Codex, Gemini: differently-keyed
// prompts over differently-shaped files) must not enter the ticker's memory —
// the tick would ask for windows nobody can answer, once per interval forever.
func TestATranscriptTheAnalysisCannotReadIsNeverTicked(t *testing.T) {
	st := tickFixture(t)
	j := aJob("/t/c.jsonl", "P1")
	j.Source = "codex"
	st.observe(j)
	st.observe(queue.Job{Source: "claude_code", PromptID: "P2"}) // no transcript path

	tk := &fakeTicker{ok: true}
	tickOnce(context.Background(), st, tk, &fakeWindowSender{}, "me", time.Now(), nil, nil)
	if len(tk.calls) != 0 {
		t.Fatalf("ticked an unanalysable transcript: %+v", tk.calls)
	}
}

// --- publishing ------------------------------------------------------------

func TestAnEmittedWindowIsPublishedUnderItsOwnCorrelation(t *testing.T) {
	st := tickFixture(t)
	st.observe(aJob("/t/a.jsonl", "P1"))
	tk := &fakeTicker{ok: true, cursor: 1000,
		windows: []enrich.WindowCharacterisation{aWindow("sess-1", "2026-08-19T13:46:44Z")}}
	pub := &fakeWindowSender{}

	if n := tickOnce(context.Background(), st, tk, pub, "me", time.Now(), nil, nil); n != 1 {
		t.Fatalf("published = %d, want 1", n)
	}
	if len(pub.sent) != 1 {
		t.Fatalf("sent = %d", len(pub.sent))
	}
	got := pub.sent[0]
	if got.Correlation.Scheme == "prompt_id" {
		t.Fatal("a window row under scheme prompt_id would OVERWRITE a prompt's enrichment")
	}
	if got.Window.Evidence != 63 || got.Actor != "me" {
		t.Fatalf("row = %+v", got)
	}
}

// The cursor is what stops a window being characterised twice. It advances only
// on a tick that answered AND published; both halves asserted, because a cursor
// that advanced on failure would silently skip work and one that never advanced
// would republish it every interval.
func TestTheCursorAdvancesOnlyOnAnAnsweredAndPublishedTick(t *testing.T) {
	base := func() (*tickState, *fakeTicker) {
		st := tickFixture(t)
		st.observe(aJob("/t/a.jsonl", "P1"))
		return st, &fakeTicker{ok: true, cursor: 5000,
			windows: []enrich.WindowCharacterisation{aWindow("sess-1", "2026-08-19T13:46:44Z")}}
	}

	st, tk := base()
	tickOnce(context.Background(), st, tk, &fakeWindowSender{}, "me", time.Now(), nil, nil)
	tickOnce(context.Background(), st, tk, &fakeWindowSender{}, "me", time.Now(), nil, nil)
	if len(tk.calls) != 2 || tk.calls[1].cursor == nil || *tk.calls[1].cursor != 5000 {
		t.Fatalf("cursor was not carried into the next tick: %+v", tk.calls)
	}

	st, tk = base()
	tk.ok = false // the sidecar could not answer
	tickOnce(context.Background(), st, tk, &fakeWindowSender{}, "me", time.Now(), nil, nil)
	tickOnce(context.Background(), st, tk, &fakeWindowSender{}, "me", time.Now(), nil, nil)
	if tk.calls[1].cursor != nil {
		t.Errorf("cursor advanced on a tick that was never answered: %v", *tk.calls[1].cursor)
	}

	st, tk = base()
	tickOnce(context.Background(), st, tk, &fakeWindowSender{err: errors.New("atlas down")},
		"me", time.Now(), nil, nil)
	tickOnce(context.Background(), st, tk, &fakeWindowSender{}, "me", time.Now(), nil, nil)
	if tk.calls[1].cursor != nil {
		t.Errorf("cursor advanced past a window that was never published: %v", *tk.calls[1].cursor)
	}
}

func TestTheCursorNeverRewinds(t *testing.T) {
	st := tickFixture(t)
	st.observe(aJob("/t/a.jsonl", "P1"))
	st.advance("/t/a.jsonl", 9000)
	st.advance("/t/a.jsonl", 100) // a stale response, or a rolled-back store
	tk := &fakeTicker{ok: true}
	tickOnce(context.Background(), st, tk, &fakeWindowSender{}, "me", time.Now(), nil, nil)
	if c := tk.calls[0].cursor; c == nil || *c != 9000 {
		t.Fatalf("cursor rewound: %v", c)
	}
}

// --- persistence -----------------------------------------------------------

func TestTheCursorSurvivesARestart(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tick.json")
	st := newTickState(file)
	st.observe(aJob("/t/a.jsonl", "P1"))
	tk := &fakeTicker{ok: true, cursor: 4242}
	tickOnce(context.Background(), st, tk, &fakeWindowSender{}, "me", time.Now(), nil, nil)

	again := newTickState(file)
	tk2 := &fakeTicker{ok: true}
	tickOnce(context.Background(), again, tk2, &fakeWindowSender{}, "me", time.Now(), nil, nil)
	if len(tk2.calls) != 1 {
		t.Fatalf("the restarted ticker forgot the transcript: %+v", tk2.calls)
	}
	if c := tk2.calls[0].cursor; c == nil || *c != 4242 {
		t.Fatalf("cursor did not survive: %v", c)
	}
	if ids := tk2.calls[0].promptIDs; len(ids) != 1 || ids[0] != "P1" {
		t.Fatalf("prompt memory did not survive: %v", ids)
	}
}

func TestACorruptStateFileStartsFreshRatherThanFailing(t *testing.T) {
	file := filepath.Join(t.TempDir(), "tick.json")
	if err := os.WriteFile(file, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	st := newTickState(file)
	st.observe(aJob("/t/a.jsonl", "P1"))
	tk := &fakeTicker{ok: true}
	tickOnce(context.Background(), st, tk, &fakeWindowSender{}, "me", time.Now(), nil, nil)
	if len(tk.calls) != 1 {
		t.Fatalf("a corrupt state file wedged the ticker: %+v", tk.calls)
	}
}

func TestPromptMemoryIsBoundedAndKeepsTheRecentPrompts(t *testing.T) {
	st := tickFixture(t)
	for i := 0; i < tickPromptMemory+50; i++ {
		st.observe(aJob("/t/a.jsonl", string(rune('a'+i%26))+string(rune('0'+i/26))))
	}
	tk := &fakeTicker{ok: true}
	tickOnce(context.Background(), st, tk, &fakeWindowSender{}, "me", time.Now(), nil, nil)
	ids := tk.calls[0].promptIDs
	if len(ids) > tickPromptMemory {
		t.Fatalf("prompt memory is unbounded: %d entries", len(ids))
	}
	// The bound must drop the OLDEST: the planner needs the recent prompts, and
	// keeping the oldest instead would mark ancient hours covered and recent ones
	// not — publishing windows over ground a prompt row already describes.
	last := aJob("/t/a.jsonl", string(rune('a'+(tickPromptMemory+49)%26))+string(rune('0'+(tickPromptMemory+49)/26)))
	if ids[len(ids)-1] != last.PromptID {
		t.Fatalf("newest prompt %q is not last in %v", last.PromptID, ids[len(ids)-1])
	}
}

func TestAnIdleTranscriptIsRetiredFromMemory(t *testing.T) {
	st := tickFixture(t)
	st.observe(aJob("/t/a.jsonl", "P1"))
	tk := &fakeTicker{ok: true}
	tickOnce(context.Background(), st, tk, &fakeWindowSender{}, "me",
		time.Now().Add(tickIdleRetire+time.Hour), nil, nil)
	if len(tk.calls) != 0 {
		t.Fatalf("a transcript idle past the retire horizon was still ticked: %+v", tk.calls)
	}
}

// --- the switch ------------------------------------------------------------

// OFF by default, and that is not caution: a tick row carries corr_scheme
// "window" and every Atlas consumer joins on corr_scheme "prompt_id", so these
// rows are stored and join to nothing. The instruction is not to silently emit
// rows Atlas will orphan, and off-by-default is what makes that explicit.
func TestWindowCharacterisationIsOffUnlessSwitchedOn(t *testing.T) {
	t.Setenv("KELD_TICK", "")
	if tickEnabled() {
		t.Fatal("window characterisation is on by default; its rows do not join at Atlas yet")
	}
	for _, v := range []string{"1", "true", "on", "YES"} {
		t.Setenv("KELD_TICK", v)
		if !tickEnabled() {
			t.Errorf("KELD_TICK=%q did not enable the tick", v)
		}
	}
	for _, v := range []string{"0", "off", "no", "nonsense"} {
		t.Setenv("KELD_TICK", v)
		if tickEnabled() {
			t.Errorf("KELD_TICK=%q enabled the tick", v)
		}
	}
}

func TestTheTickIntervalIsConfigurableWithASaneDefault(t *testing.T) {
	t.Setenv("KELD_TICK_INTERVAL", "")
	if got := tickIntervalFromEnv(); got != defaultTickInterval {
		t.Fatalf("default = %s, want %s", got, defaultTickInterval)
	}
	t.Setenv("KELD_TICK_INTERVAL", "3m")
	if got := tickIntervalFromEnv(); got != 3*time.Minute {
		t.Fatalf("interval = %s", got)
	}
	for _, bad := range []string{"nope", "-5m", "0"} {
		t.Setenv("KELD_TICK_INTERVAL", bad)
		if got := tickIntervalFromEnv(); got != defaultTickInterval {
			t.Errorf("KELD_TICK_INTERVAL=%q gave %s, want the default", bad, got)
		}
	}
}

func TestStartTickerReturnsNoObserverWhenItIsOff(t *testing.T) {
	t.Setenv("KELD_TICK", "")
	if startTicker(context.Background(), &fakeTicker{}, &fakeWindowSender{}, "me", nil) != nil {
		t.Fatal("the ticker wired an observer while switched off")
	}
	// And with no analysis service to ask, being switched on changes nothing.
	t.Setenv("KELD_TICK", "1")
	if startTicker(context.Background(), nil, &fakeWindowSender{}, "me", nil) != nil {
		t.Fatal("the ticker started with no analysis service")
	}
}

// The worker's side of the seam: a job reaches the ticker's memory only while a
// ticker exists, and the call is a no-op otherwise.
func TestTheWorkerFeedsPromptsToTheTickerOnlyWhenOneIsRunning(t *testing.T) {
	setTickObserver(nil)
	noteTickPrompt(aJob("/t/a.jsonl", "P1")) // must not panic

	st := tickFixture(t)
	setTickObserver(st.observe)
	t.Cleanup(func() { setTickObserver(nil) })
	noteTickPrompt(aJob("/t/a.jsonl", "P1"))

	tk := &fakeTicker{ok: true}
	tickOnce(context.Background(), st, tk, &fakeWindowSender{}, "me", time.Now(), nil, nil)
	if len(tk.calls) != 1 || len(tk.calls[0].promptIDs) != 1 {
		t.Fatalf("the worker's prompt did not reach the ticker: %+v", tk.calls)
	}
}

// A TICK CARRIES THE CHECKOUT'S IDENTITY TOO, and it has to recover it without a
// queue.Job: there is no cwd here, only a transcript path. So the directory comes
// out of the projects-directory NAME (see projectdir.go) and the facts are
// resolved per TRANSCRIPT — the granularity they have, since a transcript is
// scoped to one project directory and every window in the batch sits in the same
// checkout.
//
// Without this, a tick-emitted window would answer with one fewer dimension than
// a prompt's window over the same hour, which is exactly the "a tick window is
// not a lesser window" rule broken.
func TestATickResolvesTheCheckoutFromTheTranscriptPath(t *testing.T) {
	git := gitInitFixture(t)
	// A transcript whose projects-directory name encodes the checkout.
	projects := filepath.Join(t.TempDir(), encodeLike(git)+"")
	if err := os.MkdirAll(projects, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(projects, "0badc0de-0000.jsonl")

	st := newTickState(filepath.Join(t.TempDir(), "tick.json"))
	st.observe(queue.Job{TranscriptPath: path, PromptID: "P1", Source: "claude_code",
		SessionID: "sess-1"})
	tk := &fakeTicker{ok: true, cursor: 5}
	tickOnce(context.Background(), st, tk, &fakeWindowSender{}, "me", time.Now(), nil,
		newFactsCache())

	if len(tk.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(tk.calls))
	}
	got := tk.calls[0].resolved
	if got.Repo != "github.com/ncx-ai/keld-atlas" {
		t.Errorf("resolved.Repo = %q, want the checkout's normalised remote — the tick did not "+
			"recover the working directory from the transcript path", got.Repo)
	}
	if got.GitBranch != "main" {
		t.Errorf("resolved.GitBranch = %q, want \"main\"", got.GitBranch)
	}
}

// A transcript whose directory does not decode sends EMPTY facts rather than a
// guess. Transcripts routinely outlive their directories (the pre-VM Cowork
// session dirs are never cleaned up; /tmp work is deleted), and a guessed path
// handed to gitRemote could publish some other repository's identity for this
// work. The tick must still RUN — an unresolvable checkout costs one dimension,
// not the characterisation.
func TestATickWithAnUndecodableTranscriptPathSendsEmptyFacts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "-gone-forever", "0badc0de-0000.jsonl")
	st := newTickState(filepath.Join(t.TempDir(), "tick.json"))
	st.observe(queue.Job{TranscriptPath: path, PromptID: "P1", Source: "claude_code",
		SessionID: "sess-1"})
	tk := &fakeTicker{ok: true, cursor: 5}
	tickOnce(context.Background(), st, tk, &fakeWindowSender{}, "me", time.Now(), nil,
		newFactsCache())

	if len(tk.calls) != 1 {
		t.Fatalf("calls = %d, want 1 — an unresolvable checkout must not skip the tick",
			len(tk.calls))
	}
	if !tk.calls[0].resolved.Zero() {
		t.Errorf("resolved = %+v, want the zero value; a guess here could name another "+
			"repository entirely", tk.calls[0].resolved)
	}
}

// gitInitFixture makes a real single-commit checkout with an `origin` remote and
// returns its path. Real git rather than a hand-written .git, for the reason
// gitremote_test.go gives: the shapes gitRemote reads are git-created.
func gitInitFixture(t *testing.T) string {
	t.Helper()
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(git, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main", ".")
	run("remote", "add", "origin", "git@github.com:ncx-ai/keld-atlas.git")
	return dir
}
