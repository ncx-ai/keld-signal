package integrations

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ncx-ai/keld-signal/internal/paths"
)

// Origins a lane fact can be recorded under. They are the spool pointer's
// `Source.Origin` values, so the record joins to what the daemon already
// writes rather than to a second vocabulary.
const (
	OriginHook    = "hook"
	OriginWatcher = "watcher"
)

// maxLaneSources bounds the file. The catalogue is seven entries and the
// daemon only ever records catalogue ids, so this is a guard against a future
// caller passing something unbounded, not a working limit.
const maxLaneSources = 64

// StateFilePath is where lane instants and the last emitted state live.
func StateFilePath() string { return filepath.Join(paths.StateDir(), "integrations.json") }

// laneFile is the on-disk shape.
//
// `last_state` is written by the client-events emitter (WS-C2) and read by
// nothing here; it is declared so that the two writers share one file and one
// load/save, rather than the emitter inventing a second file or — worse —
// rewriting this one from a struct that does not know about the lanes and
// silently dropping them.
type laneFile struct {
	Sources   map[string]map[string]time.Time `json:"sources"`
	LastState map[string]string               `json:"last_state,omitempty"`
}

// Lanes is the persistent record of when each lane last carried something for
// each source: `{source, origin} -> last pointer instant`.
//
// ⚠️ IT IS AN INSTANT ON DISK, NOT A COUNTER IN MEMORY, and that is what makes
// `idle` distinguishable from `broken` across a restart. A daemon that started
// ten minutes ago has seen nothing on any lane; if the record lived in memory,
// every machine would read `idle` for its first day and the window would be
// measured from the process, not from the work.
//
// ⚠️ AND IT IS LOADED AT CONSTRUCTION, NEVER STARTED EMPTY. The same mistake
// one package over — teleproxy's session record — would have made a daemon
// restart erase the history rather than merely not read it, because the first
// write after an empty start persists the empty map back over the file.
type Lanes struct {
	mu   sync.Mutex
	path string
	file laneFile
}

// LoadLanes reads the record at StateFilePath. A missing or unparseable file
// yields an EMPTY record rather than an error: the first daemon on a machine
// has no record, and that is the ordinary state, not a fault.
func LoadLanes() *Lanes { return LoadLanesAt(StateFilePath()) }

// LoadLanesAt is LoadLanes against an explicit path, for tests.
func LoadLanesAt(path string) *Lanes {
	l := &Lanes{path: path, file: laneFile{Sources: map[string]map[string]time.Time{}}}
	data, err := os.ReadFile(path)
	if err != nil {
		return l
	}
	var f laneFile
	if json.Unmarshal(data, &f) != nil {
		return l
	}
	if f.Sources != nil {
		l.file.Sources = f.Sources
	}
	l.file.LastState = f.LastState
	return l
}

// RecordPointer notes that `source` carried something on `origin` at `at`.
//
// It only ever moves an instant FORWARD: the drain of a spool written
// yesterday must not make a lane look like it went quiet, and a re-offered
// pointer must not rewrite a later arrival.
func (l *Lanes) RecordPointer(source, origin string, at time.Time) {
	if l == nil || source == "" || origin == "" || at.IsZero() {
		return
	}
	l.mu.Lock()
	byOrigin := l.file.Sources[source]
	if byOrigin == nil {
		if len(l.file.Sources) >= maxLaneSources {
			l.mu.Unlock()
			return
		}
		byOrigin = map[string]time.Time{}
		l.file.Sources[source] = byOrigin
	}
	if prev, ok := byOrigin[origin]; ok && !at.After(prev) {
		l.mu.Unlock()
		return
	}
	byOrigin[origin] = at.UTC()
	l.mu.Unlock()
	l.save()
}

// Last returns the instant this lane last carried something, or nil when it
// never has. A nil is NOT "long ago" — Compute treats it as a lane that has
// been silent for the whole window, which is a fact only because the record
// survives restarts.
func (l *Lanes) Last(source, origin string) *time.Time {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	t, ok := l.file.Sources[source][origin]
	if !ok || t.IsZero() {
		return nil
	}
	cp := t
	return &cp
}

// LastState / SetLastState carry the emitter's per-source memory of what it
// last said (WS-C2's `integration.broken` / `integration.recovered` pair).
// They live beside the lanes so both writers go through one load and one save.
func (l *Lanes) LastState(source string) string {
	if l == nil {
		return ""
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.file.LastState[source]
}

func (l *Lanes) SetLastState(source, state string) {
	if l == nil || source == "" {
		return
	}
	l.mu.Lock()
	if l.file.LastState == nil {
		l.file.LastState = map[string]string{}
	}
	if l.file.LastState[source] == state {
		l.mu.Unlock()
		return
	}
	l.file.LastState[source] = state
	l.mu.Unlock()
	l.save()
}

// save writes the whole record, 0600, best-effort. A failed write costs the
// next restart's history and must never fail a pointer.
func (l *Lanes) save() {
	l.mu.Lock()
	buf, err := json.Marshal(l.file)
	path := l.path
	l.mu.Unlock()
	if err != nil || path == "" {
		return
	}
	if os.MkdirAll(filepath.Dir(path), 0o700) != nil {
		return
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, buf, 0o600) != nil {
		return
	}
	_ = os.Rename(tmp, path)
}
