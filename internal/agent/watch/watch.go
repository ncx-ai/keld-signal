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
	offer      func(spool.Pointer)
	observe    func(source, transcriptPath string, line []byte)
	cursors    *CursorStore
	discover   func() []Root
	version    string
	poll       time.Duration
	backfill   bool
	extractors map[string]promptExtractor
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
			"gemini":      geminiExtractor{},
		},
	}
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
