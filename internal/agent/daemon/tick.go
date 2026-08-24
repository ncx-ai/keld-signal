package daemon

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/clientevents"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/publish"
	"github.com/ncx-ai/keld-signal/internal/agent/queue"
	"github.com/ncx-ai/keld-signal/internal/paths"
)

// THE TICK — characterising the work no prompt will ever characterise.
//
// Enrichment fires per prompt and every window looks BACK an hour, so the work a
// prompt CAUSES falls outside that prompt's own window; when the next prompt is
// more than an hour later, nothing characterises it at all. Measured over the
// frozen corpus (scripts/tick_coverage.py):
//
//	john (Cowork, 14 prompts over 7h)   56.4% of reference events -> 99.7%
//	claude code (496 transcripts)       55.0% of turns            -> 99.5%
//
// A third to a half of all work, invisible, and worse the more autonomous the
// agent. The planner and the proof that a tick-emitted window can never overlap
// a prompt's live in the sidecar (app/analysis/coverage.py); this file is the
// daemon half: when to ask, what to remember between asks, and what to publish.
//
// # A TIMER, NOT AN INGEST SIGNAL — and this was measured too
//
// The obvious design is to drive the tick off the watcher's ingest signal: a
// machine doing nothing produces no signals, which satisfies "idle ticks emit
// nothing" for free. It is wrong, and quantifiably so. A slice only becomes
// emittable once no future prompt can cover it, which is a whole span after the
// work happened — by which time the burst that produced it is typically over and
// the machine is quiet, so no further ingest signal is coming. Measured, the
// share of RECOVERED work that is emitted only after the transcript's last turn:
//
//	john         5.8%
//	claude code  79.5%
//
// An ingest-driven tick would silently drop that, and it would drop it in
// exactly the shape the tick exists for — a burst of autonomous work followed by
// silence. So the tick is a timer, and rule 2 is satisfied structurally instead:
// a silent interval plans cleanly and then produces no window, because every
// window it planned held no evidence and was dropped sidecar-side.
//
// # The interval is a latency parameter, not a coverage one
//
// Also measured: coverage is 99.5% at a 5-, 10-, 20- and 60-minute interval
// alike — identical to a tenth of a point. The cursor is monotonic and a gap
// stays a gap, so a slower tick emits the same time, later. Ten minutes is the
// default because the cost is latency (a window facet lands up to span+interval
// after the work) and the price is a handful of ~2 ms store queries an hour.
//
// # Nothing safety-relevant waits for a tick
//
// Only the WINDOW facets are tick-eligible. sensitivity, task_type and every
// other text-derived facet keep their per-prompt trigger unchanged — a PII
// detection must not sit in a buffer for an hour, and this file cannot make one
// do so because it never reads prompt text at all.

// tickPromptMemory bounds the prompt ids remembered per transcript. The planner
// needs the prompts whose look-backs could reach the interval being planned,
// which is a bounded recent set: at the corpus's densest, 20 prompts in an hour.
// 256 covers many hours over, and the bound matters because this is persisted
// state for a process that runs for weeks.
const tickPromptMemory = 256

// tickIdleRetire drops a transcript from the ticker's memory after this long
// with no new prompt. Retiring is safe rather than lossy: a later prompt puts
// the transcript back, and the cursor it comes back with starts at the frontier,
// so the tick is forward-only exactly as capture is. Without it, the state file
// grows one entry per session forever.
const tickIdleRetire = 48 * time.Hour

// tickSession is one transcript's tick state.
type tickSession struct {
	Session string `json:"session"`
	Source  string `json:"source"`
	// Cursor is where the last tick stopped, in epoch seconds. Nil means never
	// ticked, which starts the cursor at the frontier — forward-only, the same
	// default KELD_WATCH_BACKFILL sets for capture, so installing the daemon
	// does not back-fill months of history.
	Cursor *float64 `json:"cursor,omitempty"`
	// PromptIDs are the prompts enrichment has taken on, oldest first. They are
	// what defines the COVERED set, and they are recorded here rather than read
	// from the sidecar's store because the store's prompt index holds every
	// user- and assistant-shaped turn — planning against that computes a covered
	// set that swallows the session (see sidecar tickReq).
	PromptIDs []string `json:"prompt_ids"`
	Seen      int64    `json:"seen"`
}

// tickState is the ticker's persisted memory: which transcripts to tick, how far
// each has been characterised, and which prompts already cover part of it.
//
// Losing this file costs no correctness. The cursor restarts at the frontier
// (forward-only) and a re-published window upserts itself under Atlas's unique
// key, so the worst case is that some already-characterised ground is skipped
// rather than duplicated — which is the right way round for a rule that says
// never double-publish.
type tickState struct {
	mu       sync.Mutex
	file     string
	sessions map[string]*tickSession
}

func newTickState(file string) *tickState {
	s := &tickState{file: file, sessions: map[string]*tickSession{}}
	b, err := os.ReadFile(file)
	if err != nil {
		return s
	}
	var on map[string]*tickSession
	if err := json.Unmarshal(b, &on); err != nil {
		// A corrupt file is not worth failing a daemon start over: the state is
		// re-derivable from the next prompt and the cursor is forward-only.
		log.Printf("keld-agent: tick state unreadable, starting fresh: %v", err)
		return s
	}
	for k, v := range on {
		if v != nil {
			s.sessions[k] = v
		}
	}
	return s
}

// observe records that enrichment has taken on a prompt, so the tick will not
// characterise the hour that prompt's own window covers.
//
// Called for every job the worker resolves. A prompt whose text does NOT resolve
// is deliberately not recorded: nothing characterised its hour, so the tick
// should. The direction that matters is the other one — marking an hour covered
// when it is not merely leaves it uncharacterised, whereas failing to mark a
// covered hour publishes a window over ground a prompt row already describes and
// counts the spend twice.
func (s *tickState) observe(j queue.Job) {
	if j.TranscriptPath == "" || j.PromptID == "" || !enrich.WorkstreamsEligible(j.Source) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.sessions[j.TranscriptPath]
	if e == nil {
		e = &tickSession{Session: j.SessionID, Source: j.Source}
		s.sessions[j.TranscriptPath] = e
	}
	if e.Session == "" {
		e.Session = j.SessionID
	}
	e.Seen = time.Now().Unix()
	for _, id := range e.PromptIDs {
		if id == j.PromptID {
			return
		}
	}
	e.PromptIDs = append(e.PromptIDs, j.PromptID)
	if n := len(e.PromptIDs) - tickPromptMemory; n > 0 {
		e.PromptIDs = append([]string(nil), e.PromptIDs[n:]...)
	}
}

// tickTarget is one transcript's tick, as the driver sees it.
type tickTarget struct {
	Path      string
	Session   string
	Source    string
	Cursor    *float64
	PromptIDs []string
}

// targets snapshots what to tick, retiring transcripts idle beyond
// tickIdleRetire. Sorted by path so a batch bound (and a log) is deterministic.
func (s *tickState) targets(now time.Time) []tickTarget {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]tickTarget, 0, len(s.sessions))
	for path, e := range s.sessions {
		if now.Sub(time.Unix(e.Seen, 0)) > tickIdleRetire {
			delete(s.sessions, path)
			continue
		}
		out = append(out, tickTarget{
			Path: path, Session: e.Session, Source: e.Source, Cursor: e.Cursor,
			PromptIDs: append([]string(nil), e.PromptIDs...),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// advance moves a transcript's cursor. MONOTONIC: a lower cursor is ignored
// rather than applied, so a sidecar that answered from a rolled-back store, or a
// stale response arriving late, cannot make the ticker replan settled ground.
func (s *tickState) advance(path string, cursor float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.sessions[path]
	if e == nil {
		return
	}
	if e.Cursor != nil && cursor <= *e.Cursor {
		return
	}
	c := cursor
	e.Cursor = &c
}

// save persists the state. Atomic via a temp file + rename: a torn write here
// would be read back as corrupt on the next start, and although that is
// recoverable (see newTickState) it silently loses every cursor.
func (s *tickState) save() error {
	s.mu.Lock()
	b, err := json.Marshal(s.sessions)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.file), 0o700); err != nil {
		return err
	}
	tmp := s.file + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.file)
}

func tickStatePath() string { return filepath.Join(paths.StateDir(), "tick.json") }

// windowTicker is the capability the ticker needs from the analysis service. An
// interface, not a *sidecar.Client, so the driver is testable without a sidecar
// — the same way windowAnalyzer/piiDetector are declared.
type windowTicker interface {
	TickCharacterised(path, source, sessionID string, promptIDs []string, cursor *float64,
		now time.Time, spanMinutes float64, maxWindows int) ([]enrich.WindowCharacterisation, float64, bool)
}

// WindowSender publishes a tick-emitted row. Separate from Sender because a
// window row is a different wire shape, not an Enrichment with empty facets —
// see publish.WindowEnrichment for why that distinction is structural.
type WindowSender interface {
	SendWindow(publish.WindowEnrichment) error
}

// tickMaxWindows bounds one transcript's batch per tick. Matches the sidecar's
// own default; stated here too because the daemon is the party that would feel
// an unbounded batch, as a burst of rows on the wire.
const tickMaxWindows = 12

// runTicker drives the tick until ctx ends. One pass per interval over every
// transcript enrichment has seen.
func runTicker(ctx context.Context, st *tickState, tk windowTicker, pub WindowSender,
	actor string, interval time.Duration, emitter *clientevents.Emitter) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tickOnce(ctx, st, tk, pub, actor, time.Now(), emitter)
		}
	}
}

// tickOnce is one pass. Split out so a test can drive it with a fixed clock
// rather than waiting on a timer.
//
// A failed tick is not retried within the pass and does not advance the cursor:
// the next interval asks for the same ground, which is exactly what the cursor's
// monotonicity buys. A failed PUBLISH does not advance it either, so the window
// is re-offered next pass and upserts itself if the first attempt did in fact
// land (Atlas's uq_enrichment_corr; see publish.WindowCorrID).
func tickOnce(ctx context.Context, st *tickState, tk windowTicker, pub WindowSender,
	actor string, now time.Time, emitter *clientevents.Emitter) (published int) {
	for _, tgt := range st.targets(now) {
		if ctx.Err() != nil {
			return published
		}
		if len(tgt.PromptIDs) == 0 {
			continue
		}
		wins, cursor, ok := tk.TickCharacterised(tgt.Path, tgt.Source, tgt.Session,
			tgt.PromptIDs, tgt.Cursor, now, enrich.WindowSpanMinutes, tickMaxWindows)
		if !ok {
			// The sidecar could not answer (not ready, restarting, behind). Do
			// not advance: queue rather than degrade, the same rule the
			// enrichment path follows.
			continue
		}
		sent, failed := 0, false
		for _, w := range wins {
			if err := pub.SendWindow(publish.BuildWindow(w, actor, time.Now())); err != nil {
				log.Printf("keld-agent: window publish failed for %s: %v", w.SessionID, err)
				if emitter != nil {
					emitter.Emit("window.publish_failed", clientevents.SevError,
						map[string]any{"error": clientevents.RedactError(err)})
				}
				// Stop at the first failure and leave the cursor where it was:
				// advancing past a window that never landed is the one way this
				// path can silently lose characterisation.
				failed = true
				break
			}
			sent++
		}
		if failed {
			continue
		}
		published += sent
		st.advance(tgt.Path, cursor)
	}
	if err := st.save(); err != nil {
		log.Printf("keld-agent: tick state not persisted: %v", err)
	}
	return published
}

// envTick is the switch that turns window characterisation on, and it is OFF by
// default for a stated reason rather than out of caution.
//
// A tick row is correlated under enrich.WindowCorrScheme, and every Atlas
// consumer today joins `Enrichment.corr_id == ToolEvent.prompt_id` with
// `corr_scheme == "prompt_id"` (14 join sites, plus enrichment_for_event's
// explicit scheme filter). So a window row is ACCEPTED and STORED — Atlas's
// EnrichmentIn ignores unknown fields and persists the whole body in
// `enrichments.raw` — and joins to NOTHING until Atlas learns to join a window
// by time and identity. The client half therefore ships INERT.
//
// It ships off rather than on because the rule this was built to is explicit: do
// not silently emit rows Atlas will orphan. Off-by-default plus the announcement
// in startTicker is what makes that visible rather than implied. Flipping the
// default is a one-line change the day the Atlas join lands; the coverage it
// recovers is already measured (see this file's header).
const envTick = "KELD_TICK"

// envTickInterval overrides the pass interval. Coverage is invariant to it
// (measured identical at 5/10/20/60 minutes), so this trades publish latency
// against a handful of ~2 ms store queries an hour and nothing else.
const envTickInterval = "KELD_TICK_INTERVAL"

const defaultTickInterval = 10 * time.Minute

// tickEnabled reports whether window characterisation is switched on. Default
// off; "1"/"true"/"on"/"yes" enable it.
func tickEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(envTick))) {
	case "1", "true", "on", "yes":
		return true
	}
	return false
}

func tickIntervalFromEnv() time.Duration {
	if v := os.Getenv(envTickInterval); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return defaultTickInterval
}

// startTicker wires and starts window characterisation when it is switched on,
// and returns the observer the worker feeds prompts to (nil when it is off, so
// the worker's call is a cheap nil check).
//
// The client-event on start is not decoration. These rows join to nothing at
// Atlas yet (see envTick), and an operator who switched this on deserves to see
// that stated once per run rather than discover it by finding a table of
// unjoinable rows. Emitted exempt from the severity floor for the same reason
// the lifecycle events are: it describes what this run WILL do.
func startTicker(ctx context.Context, tk windowTicker, pub WindowSender, actor string,
	emitter *clientevents.Emitter) func(queue.Job) {
	if !tickEnabled() || tk == nil || pub == nil {
		return nil
	}
	st := newTickState(tickStatePath())
	interval := tickIntervalFromEnv()
	log.Printf("keld-agent: window characterisation ON (every %s). These rows carry "+
		"corr_scheme=%q and DO NOT JOIN to tool events until Atlas supports a window join.",
		interval, enrich.WindowCorrScheme)
	if emitter != nil {
		emitter.EmitExempt("window.tick_enabled", clientevents.SevWarn, map[string]any{
			"interval_s":     int(interval.Seconds()),
			"corr_scheme":    enrich.WindowCorrScheme,
			"joins_at_atlas": false,
		})
	}
	go runTicker(ctx, st, tk, pub, actor, interval, emitter)
	return st.observe
}

// tickObserver is how a job reaches the ticker's memory. A package-level atomic
// rather than another parameter threaded through Worker -> process: the ticker
// is optional, off by default, and reads nothing a job carries except its
// coordinates, so widening two hot signatures (and every test that calls them)
// to pass a usually-nil hook would cost more than it buys. Atomic because Run
// sets it before the worker starts and tests set it per case.
var tickObserver atomic.Pointer[func(queue.Job)]

func setTickObserver(fn func(queue.Job)) {
	if fn == nil {
		tickObserver.Store(nil)
		return
	}
	tickObserver.Store(&fn)
}

// noteTickPrompt records that enrichment has taken this prompt on, so the tick
// will not characterise the hour that prompt's own window already covers. A
// no-op when the ticker is off.
func noteTickPrompt(j queue.Job) {
	if fn := tickObserver.Load(); fn != nil {
		(*fn)(j)
	}
}
