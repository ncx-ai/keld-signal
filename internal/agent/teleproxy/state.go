package teleproxy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/ncx-ai/keld-signal/internal/paths"
)

// StatePath is where the proxy records when telemetry last reached Atlas.
func StatePath() string { return filepath.Join(paths.StateDir(), "telemetry.json") }

// MarkRunning records that a telemetry proxy runs on this machine, WITHOUT
// claiming anything has been forwarded.
//
// ⚠️ IT IS CALLED AT PROXY START, AND THE DETECTOR IS INERT WITHOUT IT. The file
// used to be written only by a successful forward, so `known` was false on
// exactly the population `keld signal doctor` exists for: a machine that migrated
// to the proxy but whose tools were never restarted has never forwarded, so it
// had no file, so it produced no finding — ever. Verified live: credential three
// hours old, no forward, doctor silent.
//
// `known` therefore means "a proxy runs here", which is what LastForwardOnDisk's
// doc always claimed. An existing recorded forward is preserved, so a daemon
// restart does not look like a machine that has never delivered.
func MarkRunning() error {
	if _, err := os.Stat(StatePath()); err == nil {
		return nil // already armed; keep whatever forward is recorded
	}
	if err := os.MkdirAll(filepath.Dir(StatePath()), 0o700); err != nil {
		return err
	}
	buf, err := json.Marshal(state{})
	if err != nil {
		return err
	}
	return os.WriteFile(StatePath(), buf, 0o600)
}

// LastForwardOnDisk reads the recorded instant, and whether a proxy runs here.
//
// ⚠️ known=false is a first-class answer and must never be reported as a fault.
// It means no proxy runs on this machine — a direct-push install, or one whose
// bind failed. A running proxy that has never forwarded answers (zero, true),
// which IS a reportable state: see MarkRunning.
func LastForwardOnDisk() (t time.Time, known bool) {
	data, err := os.ReadFile(StatePath())
	if err != nil {
		return time.Time{}, false
	}
	var s state
	if json.Unmarshal(data, &s) != nil {
		return time.Time{}, false
	}
	return s.LastForward, true
}

// SessionsOnDisk reads the per-session record: tool session id → when telemetry
// for it last reached Atlas. Empty (not nil-with-error) when nothing is
// recorded, so a caller cannot mistake "no proxy" for "no sessions" — that
// distinction is LastForwardOnDisk's `known`.
func SessionsOnDisk() map[string]time.Time {
	data, err := os.ReadFile(StatePath())
	if err != nil {
		return map[string]time.Time{}
	}
	var s state
	if json.Unmarshal(data, &s) != nil || s.Sessions == nil {
		return map[string]time.Time{}
	}
	return s.Sessions
}

// evictOldest trims m to at most max entries, dropping the oldest first.
func evictOldest(m map[string]time.Time, max int) {
	for len(m) > max {
		var oldestKey string
		var oldest time.Time
		first := true
		for k, v := range m {
			if first || v.Before(oldest) {
				oldestKey, oldest, first = k, v, false
			}
		}
		delete(m, oldestKey)
	}
}
