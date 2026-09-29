package localagent

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
	"time"
)

// UsageState is what doctor and status can say about the per-request table
// (docs/v3/contracts.md → `requests`): how many requests it holds, since when,
// and how much of ledger.db is in use. Disk-only and read-only, like
// AbandonedSessions: a stopped daemon still answers, and nothing is created.
type UsageState struct {
	Known  bool
	Rows   int64
	Oldest time.Time
	Bytes  int64
}

func ReadUsage() UsageState { return usageAt(ledgerPath()) }

func usageAt(path string) UsageState {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return UsageState{Known: true} // nothing recorded on this machine yet
		}
		return UsageState{}
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(2000)")
	if err != nil {
		return UsageState{}
	}
	defer db.Close()
	var st UsageState
	var oldest sql.NullInt64
	if err := db.QueryRow(`SELECT COUNT(*), MIN(ts) FROM requests`).Scan(&st.Rows, &oldest); err != nil {
		// A ledger written before the table existed holds no requests yet.
		if strings.Contains(err.Error(), "no such table") {
			return UsageState{Known: true}
		}
		return UsageState{}
	}
	if oldest.Valid {
		st.Oldest = time.UnixMilli(oldest.Int64)
	}
	var pages, free, size int64
	if err := db.QueryRow(`SELECT page_count, freelist_count, page_size FROM pragma_page_count, pragma_freelist_count, pragma_page_size`).
		Scan(&pages, &free, &size); err == nil {
		st.Bytes = (pages - free) * size
	}
	st.Known = true
	return st
}

// Line is the one informational line doctor and status print. Never a
// problem: an empty table on a new machine is the expected state.
func (u UsageState) Line() string {
	switch {
	case !u.Known:
		return "  · per-request usage: could not read the ledger"
	case u.Rows == 0:
		return "  · per-request usage: nothing recorded yet"
	}
	noun := "requests"
	if u.Rows == 1 {
		noun = "request"
	}
	return fmt.Sprintf("  · per-request usage: %s %s since %s · ledger.db %.1f MB in use",
		groupThousands(u.Rows), noun, u.Oldest.Format("2 Jan 2006"), float64(u.Bytes)/(1<<20))
}

func groupThousands(n int64) string {
	s := fmt.Sprintf("%d", n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
