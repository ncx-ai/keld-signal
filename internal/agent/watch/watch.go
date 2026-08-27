package watch

import (
	"bufio"
	"context"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/ncx-ai/keld-signal/internal/debuglog"
	"github.com/ncx-ai/keld-signal/internal/spool"
)

// Watcher tails Claude-Code-format transcript roots and, for each new genuine
// user prompt, synthesizes an enrich pointer and hands it to offer — the same
// pointer shape the hook produces, fed into the same daemon queue. It is the
// hook-free capture trigger for surfaces that don't fire command hooks (Cowork,
// and Claude Code launch surfaces where hooks may not run). It never reads or
// forwards prompt TEXT — only pointers.
type Watcher struct {
	offer    func(spool.Pointer)
	observe  func(source, transcriptPath string, line []byte)
	advanced func(source, transcriptPath string)
	// signalFirstSight makes a FIRST SIGHTING fire the ingest/blocks signal even
	// under forward-only, without offering any of the file's historical prompts.
	// See scanFile and WithFirstSightSignal.
	signalFirstSight bool
	cursors          *CursorStore
	discover         func() []Root
	version          string
	poll             time.Duration
	backfill         bool
	extractors       map[string]promptExtractor
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
		extractors: map[string]promptExtractor{
			"claude_code": claudeExtractor{},
			"cowork":      claudeExtractor{},
			"codex":       newCodexExtractor(),
			"gemini_cli":  geminiExtractor{},
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

func (w *Watcher) WithIngestSignal(fn func(source, transcriptPath string)) *Watcher {
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
		for _, path := range transcriptFiles(root.Dir) {
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
}

// scanFile reads new complete lines from path's cursor, offers each genuine
// prompt, and advances the cursor. Returns true if the cursor moved.
func (w *Watcher) scanFile(source, path string) bool {
	off, known := w.cursors.Get(path)
	if !known {
		// First sighting. Forward-only: skip existing content by starting the
		// cursor at EOF (unless backfill is on).
		if !w.backfill {
			if st, err := os.Stat(path); err == nil {
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
					w.advanced(source, path)
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
		w.offer(spool.Pointer{
			Source:      spool.Source{ID: source, Origin: "watch", Version: w.version},
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

// transcriptFiles returns *.jsonl under dir (recursively). Best-effort.
func transcriptFiles(dir string) []string {
	var out []string
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable subtrees
		}
		if !d.IsDir() && filepath.Ext(p) == ".jsonl" {
			out = append(out, p)
		}
		return nil
	})
	return out
}

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
