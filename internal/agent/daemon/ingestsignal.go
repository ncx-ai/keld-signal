package daemon

import (
	"context"
	"sync"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/debuglog"
)

// ingestSignalDepth bounds how many advanced transcripts may be waiting to be
// signalled. A machine writes one or two transcripts at a time, and the queue
// coalesces repeats of the same path, so this is roomy for normal operation and
// generous even for a KELD_WATCH_BACKFILL sweep. It is a bound rather than an
// unbounded buffer because the thing on the other end can be down for hours: an
// unbounded queue of paths behind a dead sidecar is a slow leak whose only
// payoff would be signals that are stale by the time they are sent.
const ingestSignalDepth = 64

// ingestQueue is the non-blocking handoff between the watcher's poll loop and
// the sidecar's ingest.
//
// WHY A HANDOFF AT ALL. The poll loop is the daemon's hook-free capture path:
// every prompt from Cowork and from Claude Code's hookless surfaces arrives
// through it. A signal sent inline would put an HTTP round-trip — to a sidecar
// that may be unprovisioned, restarting or mid-recycle — between the watcher and
// the prompts it has not offered yet. Ingest latency is worth nothing next to
// captured prompts, so the signal is fire-and-forget: offer returns immediately,
// always, and a dedicated goroutine does the talking.
//
// WHY DROPPING IS SAFE. Ingest resumes from the byte offset the sidecar stored,
// so a signal is not a delivery of anything — it is a hint that there is work.
// The next signal for that file asks for everything appended since, and
// /analyze's on-demand ingest is the backstop if no further signal ever comes.
// That is what makes the two drop rules below cheap rather than lossy:
//
//   - COALESCE: a path already waiting is not queued twice. Two signals for one
//     unchanged-in-between file ask for exactly the same catch-up.
//   - DROP WHEN FULL: never block, never grow. A full queue means the sidecar is
//     not keeping up, and the correct response is to let the watcher carry on.
type ingestQueue struct {
	ch chan string

	mu sync.Mutex
	// pending is the set of paths sitting in ch. A path leaves it when it is
	// TAKEN, not when its signal completes, so a file appended to during its own
	// ingest can be signalled again — the dedup is "already waiting", never
	// "already seen".
	pending   map[string]struct{}
	coalesced int
	dropped   int
	failed    int
}

func newIngestQueue(depth int) *ingestQueue {
	if depth <= 0 {
		depth = ingestSignalDepth
	}
	return &ingestQueue{ch: make(chan string, depth), pending: map[string]struct{}{}}
}

// offer queues path for signalling. It never blocks. false means the signal was
// coalesced into one already waiting, or dropped because the queue is full —
// both recoverable (see the type comment).
func (q *ingestQueue) offer(path string) bool {
	q.mu.Lock()
	if _, dup := q.pending[path]; dup {
		q.coalesced++
		q.mu.Unlock()
		return false
	}
	q.pending[path] = struct{}{}
	q.mu.Unlock()

	select {
	case q.ch <- path:
		return true
	default:
		q.mu.Lock()
		delete(q.pending, path) // it never made it in; do not block the next offer
		q.dropped++
		q.mu.Unlock()
		return false
	}
}

// run sends queued paths to signal, one at a time, until ctx is cancelled.
//
// SERIAL, not fanned out: the sidecar parses a transcript in its own executor
// and holds a per-path lock, so concurrent signals would only queue threads
// there. Serial also means a slow ingest costs at most the queue's own depth in
// coalesced signals, never a burst on the sidecar — the same discipline the
// inference path's single-flight enforces.
func (q *ingestQueue) run(ctx context.Context, signal func(path string) bool) {
	for {
		select {
		case <-ctx.Done():
			return
		case path := <-q.ch:
			q.mu.Lock()
			delete(q.pending, path)
			q.mu.Unlock()
			q.send(signal, path)
		}
	}
}

// send makes one signal attempt under a recover. A panic in the sender would
// otherwise take this goroutine and with it EVERY later signal — the store would
// then fall silently behind and only /analyze's on-demand ingest would keep it
// correct, at the cost this whole task exists to remove. Same isolation the
// watcher's own poll has.
func (q *ingestQueue) send(signal func(path string) bool, path string) {
	defer func() {
		if r := recover(); r != nil {
			debuglog.Append("ingest signal: recovered from panic: %v", r)
		}
	}()
	if !signal(path) {
		q.mu.Lock()
		q.failed++
		q.mu.Unlock()
	}
}

// stats reports (coalesced, dropped) for tests and diagnostics.
func (q *ingestQueue) stats() (int, int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.coalesced, q.dropped
}

// ingestSignalHook wires the watcher's per-file advance signal to the sidecar's
// /ingest, and is the whole of the daemon-side policy: which advances are worth
// signalling, and the non-blocking handoff that keeps the watcher's poll loop
// clear of it. The returned func is what watch.WithIngestSignal takes.
//
// SCOPED TO THE SOURCES THE ANALYSIS CAN SERVE. /analyze cannot resolve a Codex
// or a Gemini prompt id (they key prompts differently over differently-shaped
// files — see enrich.WorkstreamsEligible), so the workstreams pass never asks
// for a window on them. Ingesting their transcripts would buy a whole-file parse
// and permanent store rows for a question nobody can ask. The gate is the SAME
// predicate the pass is gated on, deliberately: extending the analysis to a new
// source makes it eligible here in the same edit, with nothing to remember. Until
// then, a newly-eligible source's already-existing transcripts are picked up by
// /analyze's own on-demand ingest on the first window that asks for one — a
// once-per-file cost on the request path, not a permanent gap.
// ⚠️ THE SIGNAL NOW CARRIES THE DAEMON'S RESOLVED FACTS, AND THE RESOLUTION
// HAPPENS ON THE SENDER GOROUTINE, NOT ON THE WATCHER'S POLL LOOP. Ingest is
// where the sidecar's `repo` rows are written — it is a series level per turn,
// not a value overlaid on a digest — so a signal without the facts leaves the
// series unable to name the repository for the bytes it just consumed. But the
// facts cost a ReadDir chain plus a .git/config read, and `offer` is called from
// the poll loop that carries every hook-free prompt on the machine. So `offer`
// stays a channel send and nothing else; the resolution is done by the same
// serial goroutine that does the talking, where a slow filesystem costs ingest
// latency and nothing else. That is the same division of labour the queue itself
// exists for.
//
// A transcript whose directory does not decode sends EMPTY facts (see
// projectdir.go): the sidecar writes no `repo` rows for them and the dimension is
// simply unattributed, which is the honest answer for a transcript whose checkout
// is gone. A guessed path would be handed to `gitRemote` and could name some
// other repository entirely.
func ingestSignalHook(ctx context.Context,
	signal func(path string, resolved enrich.ResolvedFacts) bool) func(source, path string) {
	q := newIngestQueue(ingestSignalDepth)
	facts := newFactsCache()
	go q.run(ctx, func(path string) bool {
		return signal(path, facts.forTranscript(path).resolved())
	})
	return func(source, path string) {
		if !enrich.WorkstreamsEligible(source) {
			return
		}
		q.offer(path)
	}
}
