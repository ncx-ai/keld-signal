package watch

import (
	"testing"
	"time"
)

func TestEnabledFromEnv(t *testing.T) {
	t.Setenv("KELD_WATCH", "")
	if !EnabledFromEnv() {
		t.Error("default should be enabled")
	}
	t.Setenv("KELD_WATCH", "off")
	if EnabledFromEnv() {
		t.Error("off should disable")
	}
}

func TestPollFromEnv(t *testing.T) {
	t.Setenv("KELD_WATCH_POLL", "")
	if PollFromEnv() != 5*time.Second {
		t.Errorf("default poll = %v", PollFromEnv())
	}
	t.Setenv("KELD_WATCH_POLL", "2s")
	if PollFromEnv() != 2*time.Second {
		t.Errorf("poll = %v", PollFromEnv())
	}
}

func TestBackfillFromEnv(t *testing.T) {
	t.Setenv("KELD_WATCH_BACKFILL", "")
	if BackfillFromEnv() {
		t.Error("default should be forward-only")
	}
	t.Setenv("KELD_WATCH_BACKFILL", "on")
	if !BackfillFromEnv() {
		t.Error("on should enable backfill")
	}
}
