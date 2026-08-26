package blocks

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
// watermark, which for a long-quiet transcript is exactly where its last block
// ended — so nothing that had settled is skipped twice. The one case it could
// cost anything is a transcript with an unpublished backlog that then went
// quiet for a month, and that cannot reach here: an entry stays in the ACTIVE
// set (and so out of the prune) until a sweep returns no blocks at all, which
// is the same as saying nothing is pending.
const cursorRetain = 30 * 24 * time.Hour

// entry is one transcript's emitter state.
type entry struct {
	Session string `json:"session"`
	Source  string `json:"source"`
	// Cursor is where the last emitted block ENDED, in epoch seconds, and it is
	// passed straight back as the sidecar's `since_ts`. The sidecar compares
	// `since_ts` against a block's START with `>=`, and blocks abut inside an
	// active segment, so the last emitted block's END admits the next block and
	// excludes the one already sent.
	//
	// Nil means NEVER SEEDED, which is a different state from "seeded at zero":
	// a nil `since_ts` on the wire means BACKFILL FROM THE SESSION'S BEGINNING,
	// so the emitter must never send a nil it has not decided to send. See
	// Emitter.sweep's first-sight branch.
	Cursor *float64 `json:"cursor,omitempty"`
	// Seen is the last time the watcher told us this transcript advanced, unix
	// seconds. It drives both the settling rule (in memory) and the prune (on
	// disk).
	Seen int64 `json:"seen"`
}

// state is the emitter's persisted memory: how far each transcript has been
// emitted.
//
// ⚠️ LOSING THIS FILE IS NOT FREE, unlike the tick's equivalent, and the
// difference is worth stating. A lost cursor makes every known transcript
// first-sight again, and first sight is FORWARD-ONLY: the blocks between the
// old cursor and the current watermark are then never emitted. That is the
// right way round — the alternative default, backfill, would emit a herd of
// history on every restart — but it is a loss, so the file is written
// atomically (temp + rename) and written after every sweep that moved anything.
//
// It holds cursors only. The ACTIVE SET — which transcripts might still have an
// unsettled block — is in-memory and rebuilt from the watcher's advance
// signals, because "is this transcript live" is a fact about now, not about the
// last run.
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
		log.Printf("keld-agent: block emitter state unreadable, starting fresh: %v", err)
		return s
	}
	// NO PRUNE HERE. The prune is the sweep's (see prune), which is the only
	// place that knows the ACTIVE set — and the active set is what makes the
	// prune lossless. Dropping an entry at load, before anything can be active,
	// would discard the cursor of a transcript with an unpublished backlog.
	for k, v := range on {
		if v == nil {
			continue
		}
		s.entries[k] = v
	}
	return s
}

// note records that a transcript advanced, creating its entry on first sight.
// Returns the entry's path so the caller can add it to the active set.
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

// idleSince is how long it has been since the watcher last said this
// transcript advanced, read LIVE rather than off a sweep's snapshot — see
// target. An unknown path reads as zero, i.e. "just now", which is the safe
// direction: it keeps the transcript in the active set.
func (s *state) idleSince(path string, now time.Time) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.entries[path]
	if e == nil {
		return 0
	}
	return now.Sub(time.Unix(e.Seen, 0))
}

// advance moves a transcript's cursor to the end of the last block CONFIRMED
// PUBLISHED.
//
// MONOTONIC. A lower cursor is ignored rather than applied, so a sidecar
// answering from a rolled-back store, or a stale response arriving late, cannot
// make the emitter re-offer settled ground — and, more to the point, cannot
// make it look like the cursor moved backwards over blocks that did land.
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
