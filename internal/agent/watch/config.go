package watch

import (
	"os"
	"strings"
	"time"
)

// EnabledFromEnv reports whether the transcript watcher should run. On by
// default; disabled with KELD_WATCH in {off,0,false} (case-insensitive).
func EnabledFromEnv() bool {
	switch strings.ToLower(os.Getenv("KELD_WATCH")) {
	case "off", "0", "false":
		return false
	default:
		return true
	}
}

// PollFromEnv returns the poll cadence (KELD_WATCH_POLL, a Go duration), default 5s.
func PollFromEnv() time.Duration {
	if v := os.Getenv("KELD_WATCH_POLL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return 5 * time.Second
}

// BackfillFromEnv reports whether pre-existing transcripts should be enriched
// from the start (KELD_WATCH_BACKFILL in {on,1,true}); default false (forward-only).
func BackfillFromEnv() bool {
	switch strings.ToLower(os.Getenv("KELD_WATCH_BACKFILL")) {
	case "on", "1", "true":
		return true
	default:
		return false
	}
}
