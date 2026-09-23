package watch

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/ncx-ai/keld-signal/internal/debuglog"
	"github.com/ncx-ai/keld-signal/internal/geminichat"
	"github.com/ncx-ai/keld-signal/internal/spool"
)

// Watcher tails Claude-Code-format transcript roots and, for each new genuine
// user prompt, synthesizes an enrich pointer and hands it to offer — the same
// pointer shape the hook produces, fed into the same daemon queue. It is the
// hook-free capture trigger for surfaces that don't fire command hooks (Cowork,
// and Claude Code launch surfaces where hooks may not run). It never reads or
// forwards prompt TEXT — only pointers.
type Watcher struct {
	offer   func(spool.Pointer)
	observe func(source, transcriptPath string, line []byte)
	// observeDoc is observe's sibling for a DOCUMENT source. ⚠️ A Gemini session
	// is one JSON file rewritten whole on every turn, so there are no appended
	// lines for the per-line hook to see and a telemetry mirror for it could not
	// exist on `observe` alone. It is handed coordinates only — a source and a
	// path — exactly like the ingest signal.
	observeDoc func(source, transcriptPath string)
	// notePrompt (may be nil) is told, with COORDINATES ONLY, that the watcher
	// extracted one genuine user prompt. ⚠️ It is the lane record's seam, and it
	// fires on EXTRACTION rather than on the offer — see WithPromptObserver.
	notePrompt func(source, transcriptPath string)
	// advanced reports whether the signal was TAKEN ON. A refused one must not be
	// dropped — see drainFirstSight.
	advanced func(source, transcriptPath string) bool
	// signalFirstSight makes a FIRST SIGHTING fire the ingest/blocks signal even
	// under forward-only, without offering any of the file's historical prompts.
	// See scanFile and WithFirstSightSignal.
	signalFirstSight bool
	// firstSight is the backlog of transcripts sighted for the first time and
	// not yet signalled. Drained a few per poll — see pollOnce.
	firstSight []advanceRef
	cursors    *CursorStore
	discover   func() []Root
	version    string
	poll       time.Duration
	backfill   bool
	extractors map[string]promptExtractor
	// started is when this watcher was constructed. ⚠️ It is what separates
	// HISTORY from a session happening NOW on a document source — see
	// scanDocument's first-sight branch.
	started time.Time
}

// promptExtractor detects a genuine user prompt within a single transcript
// line and, if found, projects it to the minimal id/cwd record needed to
// synthesize an enrich pointer. Implementations never see (or need) prompt
// text beyond what's required to decide genuineness.
type promptExtractor interface {
	extract(path string, line []byte) (promptRec, bool)
}

// claudeExtractor is the stateless Claude-Code-format extractor: it wraps the
// existing parsePrompt with no per-file state, so the Claude/cowork path's
// behavior is unchanged (byte-identical) by this indirection.
type claudeExtractor struct{}

func (claudeExtractor) extract(_ string, line []byte) (promptRec, bool) {
	return parsePrompt(line)
}

// extractorFor returns the promptExtractor for a capture source, defaulting
// to claudeExtractor for unknown/unset sources.
func (w *Watcher) extractorFor(source string) promptExtractor {
	if ex, ok := w.extractors[source]; ok {
		return ex
	}
	return claudeExtractor{}
}

// New builds a Watcher. offer receives each synthesized pointer (enrichment);
// observe (may be nil) receives every new complete transcript line (telemetry);
// version stamps Source.Version; poll is the scan cadence; backfill=false starts
// new files at EOF (forward-only), true enriches history.
func New(offer func(spool.Pointer), observe func(source, transcriptPath string, line []byte), version string, poll time.Duration, backfill bool) *Watcher {
	if poll <= 0 {
		poll = 5 * time.Second
	}
	return &Watcher{
		offer:    offer,
		observe:  observe,
		cursors:  NewCursorStore(),
		discover: DiscoverRoots,
		version:  version,
		poll:     poll,
		backfill: backfill,
		started:  time.Now(),
		extractors: map[string]promptExtractor{
			"claude_code": claudeExtractor{},
			"cowork":      claudeExtractor{},
			"codex":       newCodexExtractor(),
		},
	}
}

// WithIngestSignal installs the hook called once per transcript that ADVANCED in
// a poll — the coarse sibling of observe. Where observe fires per line (each one
// is a telemetry event), this fires per file per poll: its consumer is the
// sidecar's reference-series ingest, which resumes from its own byte-offset
// checkpoint and catches up on everything appended since it last ran. One signal
// per line would ask for the same whole-tail parse once per line of it.
//
// It carries COORDINATES ONLY — a source and a path, never a line, never text.
// The bytes are the thing the consumer re-reads on its own side; sending them
// would both duplicate the read and put prompt text on a wire.
//
// It is the same seam observe is (a nil-able func supplied by the daemon at
// wiring time), for the same reason: the watcher decides WHAT happened, the
// daemon decides what to do about it. The daemon's hook is where the policy
// lives — which sources are worth ingesting, and the non-blocking handoff that
// keeps an unreachable sidecar off this loop (internal/agent/daemon/
// ingestsignal.go). Chainable rather than a sixth positional argument to New,
// which every existing construction and test would otherwise have to pass.
// WithFirstSightSignal makes a first sighting fire the ingest signal even under
// forward-only. The daemon turns this on when block backfill is on: that is the
// feature that needs a transcript to be ingestable before it next grows. It
// never offers historical prompts — see scanFile.
func (w *Watcher) WithFirstSightSignal(on bool) *Watcher {
	w.signalFirstSight = on
	return w
}

// WithDocumentObserver installs the whole-file telemetry hook for document
// sources. It fires once per poll for a transcript this watcher is actively
// reading, and never for one it has classed as history — the same rule
// scanDocument applies to prompts, because mirroring an old session's usage
// would publish spend that was never reported.
func (w *Watcher) WithDocumentObserver(fn func(source, transcriptPath string)) *Watcher {
	w.observeDoc = fn
	return w
}

// WithPromptObserver installs the lane record's seam: coordinates only, once
// per genuine user prompt this watcher EXTRACTED from a transcript.
//
// ⚠️ ON EXTRACTION, NOT ON THE OFFER, AND THE DIFFERENCE IS A FALSE `broken`.
// A first sighting under forward-only offers nothing — the cursor jumps to EOF
// — so a session created and finished entirely between two polls recorded no
// watcher lane at all, while the very same branch was replaying that file to
// the usage mirror. Every `codex exec` and every `claude -p` has that shape,
// and so does any session whose first prompt lands inside the 5s poll gap.
// Silence on an expected lane is one half of `broken`, so the pane blamed a
// watcher that had just read the prompt it was accused of missing. "Did the
// watcher see this prompt" is answered yes by one it then chose not to enrich.
func (w *Watcher) WithPromptObserver(fn func(source, transcriptPath string)) *Watcher {
	w.notePrompt = fn
	return w
}

// notePromptSeen reports one extracted prompt, if anyone is listening.
func (w *Watcher) notePromptSeen(source, path string) {
	if w.notePrompt != nil {
		w.notePrompt(source, path)
	}
}

func (w *Watcher) WithIngestSignal(fn func(source, transcriptPath string) bool) *Watcher {
	w.advanced = fn
	return w
}

// Run polls until ctx is cancelled. Each poll is panic-isolated so a malformed
// transcript or unexpected filesystem state can never crash the daemon (and with
// it the hook capture path and enrichment worker).
func (w *Watcher) Run(ctx context.Context) {
	t := time.NewTicker(w.poll)
	defer t.Stop()
	w.safePollOnce() // initial pass so forward-only cursors are set promptly
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.safePollOnce()
		}
	}
}

// safePollOnce runs one poll under a recover, so a panic in any single poll is
// logged and swallowed rather than taking down the daemon.
func (w *Watcher) safePollOnce() {
	defer func() {
		if r := recover(); r != nil {
			debuglog.Append("watch: poll recovered from panic: %v", r)
		}
	}()
	w.pollOnce()
}

func (w *Watcher) pollOnce() {
	changed := false
	for _, root := range w.discover() {
		for _, path := range transcriptFiles(root.Dir, root.SourceID) {
			if w.scanFile(root.SourceID, path) {
				changed = true
			}
		}
	}
	if changed {
		if err := w.cursors.Save(); err != nil {
			debuglog.Append("watch: cursor save failed: %v", err)
		}
	}
	w.drainFirstSight()
}

// drainFirstSight signals up to firstSightPerPoll backlogged first sightings.
// See the constant for why this is paced rather than fired in one burst.
func (w *Watcher) drainFirstSight() {
	if w.advanced == nil {
		w.firstSight = nil
		return
	}
	n := len(w.firstSight)
	if n > firstSightPerPoll {
		n = firstSightPerPoll
	}
	// ⚠️ POP ONLY WHAT WAS TAKEN ON. The hook behind `advanced` drops when the
	// bounded ingest queue is full; popping regardless made the pacing rate a
	// GUESS against the sidecar's real drain throughput, and an overrun lost the
	// sighting permanently — a first sighting has no next signal, which is the
	// entire reason this backlog exists. Reporting acceptance turns the guess
	// into backpressure: a refused entry stays at the head for the next poll.
	taken := 0
	for _, a := range w.firstSight[:n] {
		if !w.advanced(a.source, a.path) {
			break
		}
		taken++
	}
	w.firstSight = append(w.firstSight[:0], w.firstSight[taken:]...)
}

// advanceRef is one backlogged first sighting: coordinates only.
type advanceRef struct{ source, path string }

// firstSightPerPoll bounds how many backlogged first sightings are signalled per
// poll.
//
// ⚠️ THE BOUND IS THE WHOLE POINT. The ingest signal rides a 64-slot,
// path-coalescing queue whose policy is DROP rather than retry — which is safe
// for a growing transcript because the next signal catches up, and unsafe for a
// first sighting, which has no next signal: the cursor is at EOF and a dormant
// transcript never grows again. Firing every sighting at once on a machine with
// 2,152 known transcripts filled the 64 slots and dropped ~2,088 PERMANENTLY.
// A few per poll never overflows it, and the backlog drains across polls; the
// work is one-time per transcript.
//
// Four rather than a larger number because the real limit is downstream: the
// signal queue has ONE serial sender and a first whole-file ingest measured 5.1s
// on a 90 MB transcript. Handing it work faster than it can drain would only
// refill the queue it is meant to keep clear.
const firstSightPerPoll = 4

// scanFile reads new complete lines from path's cursor, offers each genuine
// prompt, and advances the cursor. Returns true if the cursor moved.
func (w *Watcher) scanFile(source, path string) bool {
	if isDocumentSource(source) {
		return w.scanDocument(source, path)
	}
	off, known := w.cursors.Get(path)
	if !known {
		// First sighting. Forward-only: skip existing content by starting the
		// cursor at EOF (unless backfill is on).
		if !w.backfill {
			if st, err := os.Stat(path); err == nil {
				// ⚠️ A THIRD CONSUMER OF THIS SIGHTING EXISTS NOW, AND IT WAS
				// LOSING WHOLE SESSIONS. The comment below enumerates two paths
				// because two was all there were; the usage MIRROR
				// (promptlog, via w.observe) is the third, and it only ever
				// sees lines handed to it live. A session written entirely
				// between two polls -- which is every `codex exec` and every
				// `claude -p` -- was therefore never mirrored at all.
				//
				// Measured: conformance chain A for codex published its
				// enrichments and its blocks and forwarded ZERO usage, while
				// the rollout on disk carried two `token_count` records. Fed
				// that same file directly, the mirror emitted both.
				//
				// So a first sighting replays the file to the mirror, and to
				// the mirror ONLY -- the prompt path stays forward-only,
				// because offering every historical prompt is the herd this
				// branch exists to prevent (measured elsewhere: 2 enrichments
				// against 2,152).
				//
				// Bounded by each LINE'S OWN instant, not the file's. A file
				// mtime is fresh for a session that has been open for days, so
				// a daemon restart would re-mirror its whole history and
				// double-count spend -- the one failure this codebase calls
				// worse than missing spend. Lines written before this watcher
				// started were either already mirrored or belong to the tool's
				// own lane; either way they are not ours to send again.
				//
				// ⚠️ THE LANE RECORD IS THE FOURTH CONSUMER, and it was missing
				// for the same reason the mirror was: this branch offers no
				// pointer, and the offer was the only place a watcher lane fact
				// was written. So the pane read `broken · watcher` about a
				// session this very loop had just read. The freshness bound is
				// the line's own instant, exactly as for the mirror — a lane
				// vouched for by a prompt written before this watcher started
				// would be reporting on somebody else's history.
				if (w.observe != nil || w.notePrompt != nil) && w.startedBefore(path) {
					ex := w.extractorFor(source)
					fresh := func(line []byte) {
						if !lineWrittenAfter(line, w.started) {
							return
						}
						if w.observe != nil {
							w.observe(source, path, line)
						}
						if _, ok := ex.extract(path, line); ok {
							w.notePromptSeen(source, path)
						}
					}
					scanFrom(path, 0, ex, fresh)
				}
				w.cursors.Set(path, st.Size())
				// ⚠️ THE TWO PATHS WANT DIFFERENT THINGS FROM THIS SIGHTING.
				// The PROMPT path must stay forward-only: offering every
				// historical prompt for enrichment is the herd this branch
				// exists to prevent, and the cursor jumping to EOF is what
				// prevents it. But the INGEST/BLOCKS path needs to know the file
				// exists, or the block emitter never sees a transcript that is
				// not still being written — a session that ended yesterday could
				// never have its history backfilled. The signal carries
				// coordinates only, so it is independent of where the cursor
				// sits.
				if w.signalFirstSight && w.advanced != nil {
					w.firstSight = append(w.firstSight, advanceRef{source, path})
				}
				return true
			}
			return false
		}
		off = 0
	}
	// Stat once: skip untouched files without opening them (most files, most
	// polls), and reset the cursor if the file shrank (truncation/rotation).
	if st, err := os.Stat(path); err == nil {
		switch {
		case st.Size() == off:
			return false // nothing appended since last poll
		case st.Size() < off:
			off = 0 // shrank: re-scan from the start
		}
	}
	var observe func(line []byte)
	if w.observe != nil {
		observe = func(line []byte) { w.observe(source, path, line) }
	}
	recs, consumed := scanFrom(path, off, w.extractorFor(source), observe)
	for _, rec := range recs {
		w.notePromptSeen(source, path)
		w.offer(spool.Pointer{
			Source:      spool.Source{ID: source, Origin: spool.OriginWatch, Version: w.version},
			Correlation: spool.Correlation{Scheme: "prompt_id", ID: rec.PromptID, SessionID: rec.SessionID},
			Pointer:     &spool.Ptr{TranscriptPath: path, PromptID: rec.PromptID, Cwd: rec.Cwd},
		})
	}
	if consumed > 0 {
		w.cursors.Set(path, off+consumed)
		// Signal AFTER the cursor moves and exactly once, on the same condition
		// the cursor advances on: complete lines were consumed. Deliberately
		// keyed on bytes rather than on len(recs) — the reference series ingests
		// TURNS, not only genuine user prompts, and a tail of assistant or
		// tool_result lines changes what a window means (workspace evidence and
		// reconcile are whole-file pre-passes). A prompt-keyed signal would
		// leave those appends unindexed.
		//
		// A first sighting under forward-only (the KELD_WATCH_BACKFILL default)
		// consumes nothing — the cursor jumps to EOF above and returns early —
		// so a daemon restart signals nothing at all and cannot become a
		// thundering herd of whole-file ingests. Only a file that grows after
		// the daemon came up is signalled. With backfill ON, the first sighting
		// does read from 0 and does signal, which is precisely what that mode
		// asks for.
		if w.advanced != nil {
			w.advanced(source, path)
		}
		return true
	}
	return false
}

// scanDocument is scanFile for a source whose session is ONE JSON DOCUMENT
// (Gemini). It keeps every rule the line path keeps — forward-only first
// sighting, the first-sight ingest signal, one offer per genuine prompt — and
// differs only where the format forces it.
//
// ⚠️ **THE CURSOR HERE COUNTS PROMPTS, NOT BYTES, and it has to.** Gemini
// rewrites the whole file on every turn, so the byte offset the line path
// stores is meaningless: the file's size changes in places other than the end,
// and "bytes appended" names nothing. The stored number is therefore how many
// GENUINE USER PROMPTS this transcript has already been offered — which is also
// the next prompt's ordinal, so a resumed daemon needs nothing else. The two
// meanings never meet, because a path is only ever scanned by one of the two
// branches.
//
// Re-offering is harmless but not free (the queue dedups on prompt id), so the
// cursor exists to keep a 50-turn session from re-offering 50 prompts on every
// single turn.
func (w *Watcher) scanDocument(source, path string) bool {
	done, known := w.cursors.Get(path)
	s, ok := geminichat.Read(path)
	if !ok {
		// Unreadable, or caught mid-rewrite. A document has no valid prefix, so
		// there is nothing to salvage and nothing to say: the file is rewritten
		// whole on the next turn and the next poll reads it.
		return false
	}
	if !known {
		if watchDebug {
			log.Printf("keld-agent: watch: first sight of %s — %d prompt(s), fresh=%v (file %s, watcher started %s)",
				filepath.Base(path), len(s.Prompts), w.startedBefore(path),
				fileModTime(path).Format(time.RFC3339), w.started.Format(time.RFC3339))
		}
		// ⚠️ **A SESSION THAT BEGAN AFTER THIS DAEMON DID IS NOT HISTORY, AND
		// TREATING IT AS HISTORY DROPPED EVERY ONE-SHOT GEMINI RUN.** Forward-only
		// exists so that installing Keld does not enrich a machine's entire past.
		// On a LINE source that rule is cheap, because a transcript is appended to
		// over time: first sight lands on a file that is still growing and the
		// next prompt is captured. A DOCUMENT is different — `gemini -p` creates a
		// whole new session file per invocation, so its only prompt is already
		// there the first time the watcher sees the file, and forward-only skips
		// it FOREVER. There is no second chance and no hook to cover it: Gemini's
		// BeforeAgent event carries no prompt id, which internal/hook already
		// treats as a silent no-op. Measured in the conformance chain: transcripts
		// found and read, 2 prompt ids in them, and 0 enrichments published.
		//
		// A file whose mtime is newer than this watcher's start cannot be the
		// history that rule protects against: it is being written now. So it is
		// read from the beginning. The bound on that is one SESSION — tens of
		// prompts, not a machine's corpus — and the queue dedups by prompt id, so
		// the cost of the one ambiguous case (a session that predates the daemon
		// and is appended to afterwards) is that its earlier turns are offered
		// once.
		if !w.backfill && !w.startedBefore(path) {
			w.cursors.Set(path, int64(len(s.Prompts)))
			if w.signalFirstSight && w.advanced != nil {
				w.firstSight = append(w.firstSight, advanceRef{source, path})
			}
			return true
		}
		done = 0
	}
	// Past the history branch, so this transcript is one being written now.
	// Fired every poll rather than only when the PROMPT cursor moves: a session
	// can gain model turns (and therefore cost) after its last human prompt, and
	// a mirror keyed on new prompts would never see them. The mirror keeps its
	// own cursor, so a poll that finds nothing new costs one parse and no POST.
	if w.observeDoc != nil {
		w.observeDoc(source, path)
	}
	if int64(len(s.Prompts)) < done {
		// Fewer prompts than we have offered: a new session reusing the path, or
		// a truncation. Re-read from the start rather than stall forever.
		done = 0
	}
	if int64(len(s.Prompts)) == done {
		return false
	}
	if watchDebug {
		log.Printf("keld-agent: watch: offering %d prompt(s) of %s (cursor %d -> %d)",
			int64(len(s.Prompts))-done, filepath.Base(path), done, len(s.Prompts))
	}
	for _, p := range s.Prompts[done:] {
		w.notePromptSeen(source, path)
		w.offer(spool.Pointer{
			Source:      spool.Source{ID: source, Origin: spool.OriginWatch, Version: w.version},
			Correlation: spool.Correlation{Scheme: "prompt_id", ID: s.CorrID(p.Ordinal), SessionID: s.ID},
			Pointer:     &spool.Ptr{TranscriptPath: path, PromptID: s.CorrID(p.Ordinal)},
		})
	}
	w.cursors.Set(path, int64(len(s.Prompts)))
	if w.advanced != nil {
		w.advanced(source, path)
	}
	return true
}

// watchDebug prints what the DOCUMENT lane decided per transcript. Off unless
// KELD_WATCH_DEBUG is set.
//
// ⚠️ **"FORWARD-ONLY SKIPPED IT" AND "OFFERED IT" LEAVE THE SAME CURSOR**, which
// is how an afternoon went: a Gemini session showed cursor 1 in cursors.json and
// published nothing, and 1 is exactly what BOTH paths write — the skip records
// the prompts it declined to offer, the offer records the ones it sent. Nothing
// else distinguished them, so the question "did the watcher offer this?" could
// not be answered from the machine's own state.
var watchDebug = os.Getenv("KELD_WATCH_DEBUG") != ""

// fileModTime is path's mtime, or the zero time when it cannot be stat'd. For
// the debug line only.
func fileModTime(path string) time.Time {
	st, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return st.ModTime()
}

// lineWrittenAfter reports whether a transcript line's OWN top-level timestamp
// is after t. Undatable lines answer false: mirroring a line we cannot place in
// time risks re-sending usage, and missing one costs a record — the safe
// direction when the choice is between duplicated and absent spend.
//
// ⚠️ The timestamp must be the TOP-LEVEL one. Nested timestamps exist on
// records that carry no turn of their own; `capture.scan` documents measuring
// 1,135 lines in 73,449 where a bare first-match regex took a nested one.
// Decoding is affordable here because this runs once per file, on its first
// sighting, and never on the steady-state path.
func lineWrittenAfter(line []byte, t time.Time) bool {
	var rec struct {
		Timestamp string `json:"timestamp"`
	}
	if json.Unmarshal(line, &rec) != nil || rec.Timestamp == "" {
		return false
	}
	at, err := time.Parse(time.RFC3339Nano, rec.Timestamp)
	if err != nil {
		return false
	}
	return at.After(t)
}

// startedBefore reports whether path was written after this watcher started —
// i.e. whether the session is happening now rather than being history. A file
// that cannot be stat'd reads as history, the conservative direction.
func (w *Watcher) startedBefore(path string) bool {
	st, err := os.Stat(path)
	if err != nil {
		return false
	}
	return st.ModTime().After(w.started)
}

// transcriptFiles returns the transcripts of `source` under dir (recursively).
// Best-effort.
//
// ⚠️ **IT FILTERED ON `.jsonl` ALONE, WHICH MATCHED NOTHING GEMINI HAS EVER
// WRITTEN.** Gemini keeps a session as ONE JSON DOCUMENT at
// `~/.gemini/tmp/<project>/chats/session-<ts>-<id>.json`, so this walk returned
// an empty list for every Gemini root on every machine — and an empty list is
// indistinguishable from a quiet tool. Measured: 55 real chat files on one
// developer machine, none of them a `.jsonl`. The extension is therefore a
// property of the SOURCE, not a constant.
func transcriptFiles(dir, source string) []string {
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable subtrees
		}
		if d.IsDir() {
			return nil
		}
		if isDocumentSource(source) {
			if geminichat.IsChatFile(p) {
				out = append(out, p)
			}
			return nil
		}
		if filepath.Ext(p) == ".jsonl" {
			out = append(out, p)
		}
		return nil
	})
	return out
}

// isDocumentSource reports whether a source keeps a session as ONE JSON
// DOCUMENT rather than as appended lines.
//
// The distinction is structural, not cosmetic: a line-oriented transcript can be
// TAILED from a byte offset, and a document is rewritten whole on every turn, so
// its cursor cannot be a byte count and its content cannot be parsed a line at a
// time. Everything this package does for Claude Code, Cowork and Codex assumes
// the first shape; Gemini is the second.
func isDocumentSource(source string) bool { return source == "gemini_cli" }

// scanFrom reads complete (newline-terminated) lines from byte offset off. It
// invokes observe (if non-nil) with every complete line — for telemetry that
// mirrors all transcript events — and returns the genuine prompts found (via
// ex.extract, for enrichment) plus the number of bytes of complete lines
// consumed. A trailing partial line (write in progress) is not consumed, so
// it is re-read next poll.
func scanFrom(path string, off int64, ex promptExtractor, observe func(line []byte)) (recs []promptRec, consumed int64) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0
	}
	defer f.Close()
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return nil, 0
	}
	br := bufio.NewReaderSize(f, 64*1024)
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			break // EOF: `line` is a partial trailing line; do not consume it
		}
		consumed += int64(len(line))
		if observe != nil {
			observe([]byte(line))
		}
		if rec, ok := ex.extract(path, []byte(line)); ok {
			recs = append(recs, rec)
		}
	}
	return recs, consumed
}
