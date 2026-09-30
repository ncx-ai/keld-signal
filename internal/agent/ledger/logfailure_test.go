package ledger

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/paths"
)

func debugLog(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(paths.DebugLogPath())
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}

// A ledger that keeps failing must keep SAYING so. It used to log the first
// failure per process and nothing after it, so a store that began refusing
// writes an hour in went silent. Each operation now reports at most once per
// failLogEvery, with a count of what it held back — bounded, never silent.
func TestRepeatedFailuresAreReportedAtALimitedRate(t *testing.T) {
	setHome(t)
	s := New()
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	boom := errors.New("disk I/O error")

	for i := 0; i < 5; i++ {
		s.logFailure("InsertRequests", boom)
	}
	if got := strings.Count(debugLog(t), "ledger: InsertRequests failed"); got != 1 {
		t.Fatalf("5 failures inside one window wrote %d lines, want 1", got)
	}

	s.logFailure("MarkFileBackfilled", boom) // another operation is not held back
	if !strings.Contains(debugLog(t), "ledger: MarkFileBackfilled failed") {
		t.Fatal("a second operation's first failure was swallowed")
	}

	now = now.Add(failLogEvery)
	s.logFailure("InsertRequests", boom)
	log := debugLog(t)
	if got := strings.Count(log, "ledger: InsertRequests failed"); got != 2 {
		t.Fatalf("after the window, %d lines, want 2", got)
	}
	if !strings.Contains(log, "4 more since the last report") {
		t.Fatalf("the held-back count is not reported:\n%s", log)
	}
}
