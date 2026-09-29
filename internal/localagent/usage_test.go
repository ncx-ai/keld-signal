package localagent

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/ledger"
)

// T10: doctor and status say how many requests the table holds and how big
// ledger.db is — read off disk, read-only.
func TestUsageLineReadsTheTableOffDisk(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	if got := ReadUsage().Line(); got != "  · per-request usage: nothing recorded yet" {
		t.Fatalf("fresh machine: %q", got)
	}
	if _, err := os.Stat(ledgerPath()); !os.IsNotExist(err) {
		t.Fatal("reading usage created the ledger")
	}
	s := ledger.New()
	at := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	var rows []ledger.RequestRow
	for i := 0; i < 1234; i++ {
		rows = append(rows, ledger.RequestRow{Source: "claude_code", Session: "s1", Key: "r" + strconv.Itoa(i), At: at.Add(time.Duration(i) * time.Second)})
	}
	s.InsertRequests(rows)
	u := ReadUsage()
	if !u.Known || u.Rows != 1234 || !u.Oldest.Equal(at) || u.Bytes <= 0 {
		t.Fatalf("state %+v", u)
	}
	line := u.Line()
	if !strings.Contains(line, "1,234 requests since 3 Sep 2026") || !strings.Contains(line, "MB in use") {
		t.Fatalf("line %q", line)
	}
}

// A ledger from before the table existed is a known zero, not an unreadable one.
func TestUsageLineOnALedgerWithoutTheTable(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "ledger.db")
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if u := usageAt(p); !u.Known || u.Rows != 0 {
		t.Fatalf("state %+v", u)
	}
}
