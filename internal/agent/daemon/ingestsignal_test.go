package daemon

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/sidecar"
	"os"
	"path/filepath"
)

// The watcher's poll loop is the daemon's capture path: every prompt from every
// hook-free surface arrives through it. A signal that could block it would trade
// ingest latency for captured prompts, which is not a trade worth making — hence
// the handoff, and hence this test, which pins the property rather than the
// implementation: the hook returns even when the sender never does.
func TestIngestSignalNeverBlocksTheWatcher(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	blocked := make(chan struct{}, 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hook := ingestSignalHook(ctx, func(path string, _ enrich.ResolvedFacts) bool {
		select {
		case blocked <- struct{}{}:
		default:
		}
		<-release // never completes for the duration of this test
		return true
	})

	hook("claude_code", "/w/first.jsonl")
	select { // the sender is now parked inside that first signal
	case <-blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("the first signal never reached the sender")
	}

	done := make(chan struct{})
	go func() {
		// DISTINCT paths, and far more of them than the queue depth: repeats of
		// one path would coalesce and never fill the queue, so this is what
		// actually exercises the full-queue case — the one where a blocking
		// handoff would park the poll loop. Every one of these must return.
		for i := 0; i < ingestSignalDepth*4; i++ {
			hook("claude_code", "/w/f"+strconv.Itoa(i)+".jsonl")
			hook("cowork", "/w/g"+strconv.Itoa(i)+".jsonl")
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the ingest signal blocked the watcher's poll loop")
	}
}

// /analyze cannot resolve a Codex or Gemini prompt id, so the workstreams pass
// is gated to WorkstreamsEligible sources. Ingesting a transcript whose windows
// can never be served is pure cost — a whole-file parse and permanent store rows
// for an answer nobody can ask for.
func TestIngestSignalOnlyForSourcesTheAnalysisCanServe(t *testing.T) {
	var mu sync.Mutex
	var got []string
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hook := ingestSignalHook(ctx, func(path string, _ enrich.ResolvedFacts) bool {
		mu.Lock()
		got = append(got, path)
		mu.Unlock()
		return true
	})

	for _, s := range []string{"claude_code", "cowork", "codex", "gemini_cli", "made_up"} {
		hook(s, "/w/"+s+".jsonl")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond) // give an ineligible source time to leak through
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("want exactly the 2 eligible sources signalled, got %v", got)
	}
	for _, p := range got {
		if p != "/w/claude_code.jsonl" && p != "/w/cowork.jsonl" {
			t.Errorf("ineligible source signalled: %q", p)
		}
	}
	// The gate is the same predicate the pass itself uses, so a source becoming
	// eligible needs no change here.
	if !enrich.WorkstreamsEligible("claude_code") || enrich.WorkstreamsEligible("codex") {
		t.Error("this test is asserting against the wrong predicate")
	}
}

// The queue is where the "never block" policy is decided, so its two drop rules
// are tested directly (no consumer running): a path already queued is coalesced,
// and a full queue drops rather than waits. Both are safe because ingest resumes
// from the stored byte offset.
func TestIngestQueueCoalescesAndDropsRatherThanWaiting(t *testing.T) {
	q := newIngestQueue(2)
	if !q.offer("/a.jsonl") {
		t.Fatal("first offer must be accepted")
	}
	if q.offer("/a.jsonl") {
		t.Fatal("a path already queued must coalesce, not queue twice")
	}
	if !q.offer("/b.jsonl") {
		t.Fatal("a different path must be accepted")
	}
	if q.offer("/c.jsonl") {
		t.Fatal("a full queue must drop, not block")
	}
	co, dr := q.stats()
	if co != 1 || dr != 1 {
		t.Fatalf("coalesced=%d dropped=%d, want 1 and 1", co, dr)
	}
}

// A file appended to WHILE its own ingest is running must be signal-able again —
// the dedup is "already waiting", never "already seen" or "already sending".
// A transcript under active use grows during the seconds a first whole-file
// ingest takes, and if that window were deduped away the appended turns would
// wait for the NEXT append to be noticed. The signal is asserted as offerable
// while the previous one is still in flight, which is the only state that tells
// the two policies apart.
func TestAPathIsOfferableAgainWhileItsSignalIsStillInFlight(t *testing.T) {
	q := newIngestQueue(2)
	inFlight := make(chan string, 1)
	release := make(chan struct{})
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go q.run(ctx, func(path string) bool {
		inFlight <- path
		<-release
		return true
	})

	if !q.offer("/a.jsonl") {
		t.Fatal("first offer must be accepted")
	}
	select {
	case p := <-inFlight:
		if p != "/a.jsonl" {
			t.Fatalf("sent %q", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run never drained the queue")
	}
	// The sender is parked inside that signal right now.
	if !q.offer("/a.jsonl") {
		co, dr := q.stats()
		t.Fatalf("a path in flight must be offerable again (coalesced=%d dropped=%d)", co, dr)
	}
}

// A sender that panics must not take the dispatcher goroutine (and with it every
// later signal) down — the same isolation the watcher's own poll has.
func TestIngestQueueSurvivesAPanickingSender(t *testing.T) {
	q := newIngestQueue(4)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ok := make(chan string, 2)
	go q.run(ctx, func(path string) bool {
		if path == "/boom.jsonl" {
			panic("sender exploded")
		}
		ok <- path
		return true
	})
	q.offer("/boom.jsonl")
	deadline := time.Now().Add(5 * time.Second)
	for !q.offer("/after.jsonl") {
		if time.Now().After(deadline) {
			t.Fatal("could not queue after the panic")
		}
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case p := <-ok:
		if p != "/after.jsonl" {
			t.Fatalf("got %q", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the dispatcher died with the panicking sender")
	}
}

// The production wiring: the signal is a SERVICE facet, like /analyze and /pii —
// non-inference, and wired in ml_backend "deterministic" too, where the Model is
// nil. Asserted the way TestFacetsForRequiresTheCapability asserts the others.
func TestSignalIngestIsAServiceFacetOfTheRealClient(t *testing.T) {
	var _ transcriptIngester = (*sidecar.Client)(nil)
	if f := facetsFor(nil, nil); f.SignalIngest != nil {
		t.Error("no client means no ingest signal")
	}
	c := sidecar.New("http://127.0.0.1:1", time.Second)
	if f := facetsFor(c, nil); f.SignalIngest == nil {
		t.Error("the real client must advertise the ingest signal")
	}
}

// ⚠️ THE SIGNAL CARRIES THE FACTS, AND THE RESOLUTION MUST NOT HAPPEN ON THE
// WATCHER'S POLL LOOP. Ingest is where the sidecar WRITES the repository rows — a
// series level per turn, not a value overlaid on a digest — so a signal without
// them leaves the series unable to name the repository for the bytes it just
// consumed. But resolving costs a ReadDir chain plus a .git/config read, and
// `offer` is called from the loop that carries every hook-free prompt on the
// machine.
//
// So the resolution belongs to the serial sender goroutine. This asserts both
// halves: the facts arrive at the sender, and `offer` returned before the sender
// had done any of that work.
func TestTheIngestSignalCarriesTheResolvedFactsWithoutBlockingTheWatcher(t *testing.T) {
	git := gitInitFixture(t)
	projects := filepath.Join(t.TempDir(), encodeLike(git))
	if err := os.MkdirAll(projects, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(projects, "0badc0de-0000.jsonl")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type call struct {
		path     string
		resolved enrich.ResolvedFacts
	}
	got := make(chan call, 4)
	hook := ingestSignalHook(ctx, func(p string, r enrich.ResolvedFacts) bool {
		got <- call{p, r}
		return true
	})
	hook("claude_code", path)

	select {
	case c := <-got:
		if c.path != path {
			t.Errorf("path = %q, want %q", c.path, path)
		}
		if c.resolved.Repo != "github.com/ncx-ai/keld-atlas" {
			t.Errorf("resolved.Repo = %q, want the checkout's normalised remote — ingest is "+
				"where the repository rows are written, so a signal without it leaves the "+
				"series unable to name the repo", c.resolved.Repo)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the signal never reached the sender")
	}
}

// A transcript whose directory does not decode still gets signalled, with EMPTY
// facts. The ingest itself is what keeps the series current for every OTHER
// level, so withholding the signal over an unresolvable repository would trade a
// missing dimension for a stale window.
func TestAnUndecodableTranscriptIsStillSignalledWithEmptyFacts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan enrich.ResolvedFacts, 4)
	hook := ingestSignalHook(ctx, func(_ string, r enrich.ResolvedFacts) bool {
		got <- r
		return true
	})
	hook("claude_code", filepath.Join(t.TempDir(), "-gone-forever", "s.jsonl"))

	select {
	case r := <-got:
		if !r.Zero() {
			t.Errorf("resolved = %+v, want the zero value", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("an unresolvable checkout must not suppress the ingest signal")
	}
}
