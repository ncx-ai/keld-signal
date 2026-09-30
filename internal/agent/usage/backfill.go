package usage

import (
	"bufio"
	"context"
	"log"
	"os"
	"sort"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/ledger"
	"github.com/ncx-ai/keld-signal/internal/agent/watch"
)

// Backfill reads every transcript still on disk from its start into the
// requests table, once per ledger file (docs/v3/contracts.md → `requests`).
//
// ⚠️ **IT WRITES TO THE TABLE ONLY, NEVER TO THE MIRROR.** Re-sending history to
// Atlas is the double count this codebase calls worse than a missing count, so
// the backfill holds a Recorder and nothing that can send (A2).
//
// ⚠️ **THE MARKER LIVES IN ledger.db, NOT BESIDE THE WATCHER'S CURSORS.** The
// cursors (~/.keld/watch) say what the watcher has READ; the marker says what
// the TABLE holds. Tying the backfill to the cursors would leave a deleted
// ledger empty forever while every cursor sat at EOF (T6b).
//
// It needs no hand-off with the live watcher. The watcher replays every line
// written after it started, even on a first sighting; this reads each file
// whole, which covers every line written before. The overlap is written twice
// and collapses on the request key. Construct it after watch.New so its reads
// always happen after that start.
//
// Paced (PerStep files per poll, the watcher's own first-sight rate), because a
// machine can hold thousands of transcripts and a 90 MB one. NEWEST FIRST, so
// the days the Overview shows by default fill before the old ones, and
// RESUMABLE: each finished file is recorded in the ledger, so a restart picks
// up where it stopped. A file is recorded only once its rows are written; a
// failed write leaves it to be read again.
type Backfill struct {
	PerStep int

	store Store
	rec   *Recorder
	roots func() []watch.Root

	listed bool
	done   bool
	todo   []watch.Root // one entry per FILE: the file's source and path

	// stopped is set by Run from its context, so a shutdown is not held up
	// behind a large transcript. A file cut short is read again from its start
	// next run, since the marker is not set.
	stopped func() bool
}

// Store is what the backfill needs from the ledger.
type Store interface {
	Sink
	RequestsBackfillDone() bool
	MarkRequestsBackfillDone(at time.Time)
	BackfilledFiles() (map[string]bool, error)
	MarkFileBackfilled(path string) error
}

// perStepDefault matches watch's firstSightPerPoll.
const perStepDefault = 4

func NewBackfill(store Store, price Price, roots func() []watch.Root) *Backfill {
	return &Backfill{PerStep: perStepDefault, store: store, rec: New(store, price), roots: roots}
}

// Remaining is how many files are still to read.
func (b *Backfill) Remaining() int { return len(b.todo) }

// Step reads up to PerStep files and reports whether the backfill is finished.
func (b *Backfill) Step() bool {
	if b.done {
		return true
	}
	if !b.listed {
		if b.store.RequestsBackfillDone() {
			b.listed, b.done = true, true
			return true
		}
		finished, err := b.store.BackfilledFiles()
		if err != nil {
			return false // the ledger is not readable yet; list again next poll
		}
		b.listed = true
		for _, root := range b.roots() {
			for _, p := range watch.TranscriptFiles(root) {
				if !finished[p] {
					b.todo = append(b.todo, watch.Root{SourceID: root.SourceID, Dir: p})
				}
			}
		}
		mtime := map[string]time.Time{}
		for _, f := range b.todo {
			if st, err := os.Stat(f.Dir); err == nil {
				mtime[f.Dir] = st.ModTime()
			}
		}
		sort.SliceStable(b.todo, func(i, j int) bool { return mtime[b.todo[i].Dir].After(mtime[b.todo[j].Dir]) })
	}
	n := b.PerStep
	if n <= 0 {
		n = perStepDefault
	}
	for ; n > 0 && len(b.todo) > 0 && !b.stop(); n-- {
		f := b.todo[0]
		if !b.read(f.SourceID, f.Dir) {
			return false // stopped part-way: this file is read again next time
		}
		if _, err := b.rec.Flush(); err != nil {
			return false // not written: keep the file, try again next poll
		}
		if b.store.MarkFileBackfilled(f.Dir) != nil {
			return false
		}
		b.todo = b.todo[1:]
	}
	if len(b.todo) == 0 && !b.stop() {
		b.store.MarkRequestsBackfillDone(time.Now())
		b.done = true
	}
	return b.done
}

// read feeds one whole transcript to the recorder and reports whether it read
// to the end. A file that vanished or cannot be opened counts as read — there
// is nothing more to get from it — and never stops the rest.
func (b *Backfill) read(source, path string) bool {
	if watch.IsDocumentSource(source) {
		b.rec.ObserveFile(source, path)
		return true
	}
	f, err := os.Open(path)
	if err != nil {
		return true
	}
	defer f.Close()
	// A Reader, not a Scanner: a transcript line carrying a tool result can
	// run to many megabytes, and a Scanner fails the whole file past its cap.
	br := bufio.NewReaderSize(f, 256*1024)
	for i := 0; ; i++ {
		if i%1000 == 0 && b.stop() {
			return false
		}
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			b.rec.Observe(source, path, line)
		}
		if err != nil {
			return true // io.EOF, or a read error: either way this file is done
		}
	}
}

func (b *Backfill) stop() bool { return b.stopped != nil && b.stopped() }

// Run steps every `every` until the backfill finishes or ctx ends.
func (b *Backfill) Run(ctx context.Context, every time.Duration) {
	b.stopped = func() bool { return ctx.Err() != nil }
	start := time.Now()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			wasListed := b.listed
			if b.Step() {
				if wasListed {
					log.Printf("keld-agent: per-request usage backfill finished in %s", time.Since(start).Round(time.Second))
				}
				return
			}
		}
	}
}

var _ Store = (*ledger.Store)(nil)
