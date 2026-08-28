package features

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// cursorRetain drops a transcript's remembered cursor after this long with no
// activity. Without it the state file grows one entry per session forever, and
// a machine's corpus is measured in hundreds (496 in the reference corpus).
//
// SAFE, not merely bounded, because of what a re-seed does: a transcript that
// comes back after this long is re-seeded FORWARD-ONLY from the store's
// watermark, which for a long-quiet transcript is exactly where its last row
// was emitted — so nothing already collected is re-collected and nothing
// settled is skipped twice. The one case it could cost anything is a transcript
// with an unbuffered backlog that then went quiet for a month, and that cannot
// reach here: an entry stays in the ACTIVE set (and so out of the prune) until
// a sweep returns no rows at all, which is the same as saying nothing is
// pending.
const cursorRetain = 30 * 24 * time.Hour

// entry is one transcript's emitter state.
type entry struct {
	Session string `json:"session"`
	Source  string `json:"source"`
	// Cursor is the instant of the last row taken from the sidecar, in epoch
	// seconds, and it is passed straight back as the next call's `since_ts`.
	// The sidecar compares `since_ts` against a row's OWN instant with `>`, so
	// the last taken row's instant admits the next row and excludes the one
	// already held.
	//
	// ⚠️ IT ADVANCES ON BUFFERING, NOT ON DELIVERY, which is the opposite of
	// the block emitter's rule and is a deliberate difference rather than an
	// oversight. A block emitter has no buffer: it publishes inline, so
	// holding the cursor until a POST succeeds is both possible and free
	// (a block is re-derivable from the store for 400 days). This path
	// delivers through a batching transport with its own disk spool, so
	// "delivered" is not a fact the sweep can observe at all — the row is
	// handed off, and durability from that point is the spool's job. Advancing
	// on handoff is what makes the handoff the single durability boundary
	// rather than two half-boundaries that disagree.
	//
	// What makes that safe is BACKPRESSURE, not optimism: a sweep never takes
	// more rows than the buffer has room for, so the buffer cannot overflow and
	// the cursor cannot advance past a row that was dropped for space. See
	// Emitter.sweepOne.
	//
	// Nil means NEVER SEEDED, which is a different state from "seeded at zero":
	// a nil `since_ts` on the wire means BACKFILL FROM THE SESSION'S BEGINNING,
	// so the emitter must never send a nil it has not decided to send. See
	// Emitter.sweepOne's first-sight branch.
	Cursor *float64 `json:"cursor,omitempty"`
	// Seen is the last time the watcher told us this transcript advanced, unix
	// seconds. It drives both the settling rule (in memory) and the prune (on
	// disk).
	Seen int64 `json:"seen"`
}

// state is the emitter's persisted memory: how far each transcript has been
// collected.
//
// ⚠️ LOSING THIS FILE IS NOT FREE. A lost cursor makes every known transcript
// first-sight again, and first sight is FORWARD-ONLY: the rows between the old
// cursor and the current watermark are then never collected. That is the right
// way round — the alternative default, backfill, would emit a herd of history
// on every restart, and here that history is VECTORS, the heaviest row this
// client produces — but it is a loss, so the file is written atomically (temp +
// rename) after every sweep that moved anything.
//
// It holds cursors only. The ACTIVE SET — which transcripts might still have a
// row to collect — is in-memory and rebuilt from the watcher's advance signals,
// because "is this transcript live" is a fact about now, not about the last run.
type state struct {
	mu      sync.Mutex
	file    string
	entries map[string]*entry
	dirty   bool
}

func newState(file string) *state {
	s := &state{file: file, entries: map[string]*entry{}}
	b, err := os.ReadFile(file)
	if err != nil {
		return s
	}
	var on map[string]*entry
	if err := json.Unmarshal(b, &on); err != nil {
		// A corrupt file is not worth failing a daemon start over. The cost is
		// stated in the type comment: every transcript becomes first-sight and
		// resumes forward-only.
		log.Printf("keld-agent: feature emitter state unreadable, starting fresh: %v", err)
		return s
	}
	// NO PRUNE HERE. The prune is the sweep's (see prune), which is the only
	// place that knows the ACTIVE set — and the active set is what makes the
	// prune lossless.
	for k, v := range on {
		if v == nil {
			continue
		}
		s.entries[k] = v
	}
	return s
}

// note records that a transcript advanced, creating its entry on first sight.
func (s *state) note(source, session, path string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entries[path]
	if e == nil {
		e = &entry{Source: source, Session: session}
		s.entries[path] = e
		s.dirty = true
	}
	if e.Source == "" {
		e.Source = source
	}
	if e.Session == "" {
		e.Session = session
	}
	e.Seen = now.Unix()
}

// target is one transcript's state as the sweep sees it.
//
// It deliberately does NOT carry the last-advance instant. The settling rule is
// a statement about NOW, and a snapshot taken at the top of a sweep can be
// stale by the time the sweep reaches this transcript — a transcript that
// advanced mid-sweep would then be retired despite fresh activity, and nothing
// would put it back until the next advance signal. The sweep asks idleSince
// instead, which reads the live value.
type target struct {
	Path    string
	Source  string
	Session string
	Cursor  *float64
}

// targets snapshots the entries for the given paths, in path order so a sweep
// (and a log) is deterministic. A path with no entry is skipped rather than
// invented: the only way into this map is the advance signal.
func (s *state) targets(paths []string) []target {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]target, 0, len(paths))
	for _, p := range paths {
		e := s.entries[p]
		if e == nil {
			continue
		}
		out = append(out, target{
			Path: p, Source: e.Source, Session: e.Session, Cursor: e.Cursor,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// idleSince is how long it has been since the watcher last said this transcript
// advanced, read LIVE rather than off a sweep's snapshot — see target. An
// unknown path reads as zero, i.e. "just now", which is the safe direction: it
// keeps the transcript in the active set.
func (s *state) idleSince(path string, now time.Time) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entries[path]
	if e == nil {
		return 0
	}
	return now.Sub(time.Unix(e.Seen, 0))
}

// advance moves a transcript's cursor to the instant of the last row BUFFERED.
//
// MONOTONIC. A lower cursor is ignored rather than applied, so a sidecar
// answering from a rolled-back store, or a stale response arriving late, cannot
// make the emitter re-collect settled ground — which at ~1.4 KB a row and an
// encoder forward pass behind each text vector is not a free mistake.
func (s *state) advance(path string, cursor float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entries[path]
	if e == nil {
		return
	}
	if e.Cursor != nil && cursor <= *e.Cursor {
		return
	}
	c := cursor
	e.Cursor = &c
	s.dirty = true
}

// prune drops entries idle beyond cursorRetain, except any still in the active
// set — see cursorRetain for why that exception is what makes the prune
// lossless.
func (s *state) prune(active map[string]bool, now time.Time) {
	cutoff := now.Add(-cursorRetain).Unix()
	s.mu.Lock()
	defer s.mu.Unlock()
	for p, e := range s.entries {
		if active[p] || e.Seen >= cutoff {
			continue
		}
		delete(s.entries, p)
		s.dirty = true
	}
}

// save persists the state, atomically via temp file + rename. A torn write here
// reads back as corrupt on the next start, and although that is survivable it
// silently loses every cursor (see the type comment). A no-op when nothing
// changed, so a quiet machine does not rewrite the file every interval.
func (s *state) save() error {
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return nil
	}
	b, err := json.Marshal(s.entries)
	s.dirty = false
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
