// Package update moves this machine to the release Atlas names: fetch, verify,
// swap, restart, and — if the new version does not come up — put the old one
// back.
//
// Every dependency that is OS-specific or network-specific is injected (the
// HTTP client, the clock, the process restarter, the exec probe, the
// destination directories), so the failure modes that matter here — a checksum
// mismatch, an unwritable destination, a rename that fails halfway, a restart
// that never returns — are exercisable on any CI host rather than only on the
// platform that happens to exhibit them.
package update

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// maxFailedVersions bounds the rolled-back-version memory. The file is read at
// every daemon start, so a machine that has been fighting a bad pin for a long
// time must not accumulate an unbounded list. Oldest entries are evicted; the
// newest is always kept, because it is the one the next decision is about.
const maxFailedVersions = 16

// State is the on-disk record at ~/.keld/update/state.json.
//
// PendingConfirm is the load-bearing field: it is written BEFORE the restart
// and cleared only by a daemon that came up as the new version and reached a
// healthy state. Anything else that reads it — the old binary still running,
// or any binary starting after a crash — treats it as "an update is in flight
// and unproven" and rolls back.
type State struct {
	From           string    `json:"from,omitempty"`
	To             string    `json:"to,omitempty"`
	InstallDir     string    `json:"install_dir,omitempty"`
	Prev           []string  `json:"prev,omitempty"`
	PendingConfirm bool      `json:"pending_confirm,omitempty"`
	AttemptedAt    time.Time `json:"attempted_at,omitempty"`
	// FailedVersions are versions that were applied and rolled back. Atlas
	// still pins the bad version, so without this the next settings poll
	// re-applies it: swap, crash, roll back, swap. This is the update-loop
	// equivalent of KELD_ENRICH_MAX_ATTEMPTS quarantining a job rather than
	// retrying it forever.
	FailedVersions []string `json:"failed_versions,omitempty"`

	// Reporting only — read by `keld signal status`/`doctor` off disk.
	LastAttempt time.Time `json:"last_attempt,omitempty"`
	LastOutcome string    `json:"last_outcome,omitempty"`
	LastError   string    `json:"last_error,omitempty"`
	LastTarget  string    `json:"last_target,omitempty"`
}

// NormalizeVersion strips one leading "v" and surrounding space so a pin
// written "0.4.2" and a build stamped "v0.4.2" compare equal.
func NormalizeVersion(v string) string {
	return strings.TrimPrefix(strings.TrimSpace(v), "v")
}

// HasFailed reports whether v has already been applied and rolled back.
func (s State) HasFailed(v string) bool {
	n := NormalizeVersion(v)
	for _, f := range s.FailedVersions {
		if NormalizeVersion(f) == n {
			return true
		}
	}
	return false
}

// MarkFailed records v as rolled back, deduping and bounding the list.
func (s *State) MarkFailed(v string) {
	if v == "" || s.HasFailed(v) {
		return
	}
	s.FailedVersions = append(s.FailedVersions, v)
	if n := len(s.FailedVersions); n > maxFailedVersions {
		s.FailedVersions = append([]string(nil), s.FailedVersions[n-maxFailedVersions:]...)
	}
}

// LoadState reads the marker. A missing file is a zero State and a nil error.
//
// So is a CORRUPT one: the file is renamed to <path>.bad for diagnosis and the
// caller is told there is no update in flight. Returning an error here would
// mean a daemon that refuses to start because of a file it wrote itself, which
// is a worse failure than the one being reported.
func LoadState(path string) (State, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return State{}, nil
		}
		return State{}, err
	}
	var s State
	if err := json.Unmarshal(b, &s); err != nil {
		_ = os.Rename(path, path+".bad")
		return State{}, nil
	}
	return s, nil
}

// SaveState writes the marker atomically (temp file in the same directory,
// then rename) with owner-only permissions. Atomicity matters because this is
// the file that decides whether to roll back: a half-written one is read as
// corrupt, i.e. as "no update in flight", which would strand a swap.
func SaveState(path string, s State) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".state-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
