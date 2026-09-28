package ledger

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The Overview's "By repo" split reads a block's repository off GET
// /v1/ledger. The ledger has stored it since the Projects pane needed it
// (Observe); these pin that the READ side now carries it too, and that a
// block with nothing stored says nothing rather than an empty object.

func TestReadCarriesStoredRepoAndBranch(t *testing.T) {
	setHome(t)
	s := New()
	k := BlockKey{Session: "s1", Start: 1000}
	s.Cut(k, 1060, "idle", "budget", "claude_code", time.Now())
	s.Observe(k, Dims{Repo: "github.com/acme/web", Branch: "main", Workspace: "web"}, time.Now())

	snap, err := s.Read(time.Time{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	d := snap.Blocks[0].Dims
	if d == nil || d.Repo != "github.com/acme/web" || d.Branch != "main" {
		t.Fatalf("want repo+branch on the block, got %#v", d)
	}
}

func TestReadOmitsDimsWhenNoneWereStored(t *testing.T) {
	setHome(t)
	s := New()
	k := BlockKey{Session: "s2", Start: 2000}
	s.Cut(k, 2060, "idle", "budget", "codex", time.Now())

	snap, _ := s.Read(time.Time{}, 10)
	raw, _ := json.Marshal(snap.Blocks[0])
	if strings.Contains(string(raw), `"dims"`) {
		t.Fatalf("a block with no stored dims must carry no dims key, got %s", raw)
	}
}

func TestReadCarriesBranchAlone(t *testing.T) {
	setHome(t)
	s := New()
	k := BlockKey{Session: "s3", Start: 3000}
	s.Cut(k, 3060, "idle", "budget", "claude_code", time.Now())
	s.Observe(k, Dims{Branch: "feat/x"}, time.Now())

	snap, _ := s.Read(time.Time{}, 10)
	raw, _ := json.Marshal(snap.Blocks[0])
	if !strings.Contains(string(raw), `"dims":{"branch":"feat/x"}`) {
		t.Fatalf("want only the stored branch under dims, got %s", raw)
	}
}
