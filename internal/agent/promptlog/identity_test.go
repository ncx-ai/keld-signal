package promptlog

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCoworkIdentityFromPathAndMeta(t *testing.T) {
	home := t.TempDir()
	base := filepath.Join(home, "Library", "Application Support", "Claude", "local-agent-mode-sessions")
	acct, org, sess := "acct-uuid", "org-uuid", "local_sess1"
	proj := filepath.Join(base, acct, org, sess, ".claude", "projects", "enc")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	// session metadata one level above the local_ dir
	meta := filepath.Join(base, acct, org, sess+".json")
	if err := os.WriteFile(meta, []byte(`{"emailAddress":"dg@keld.co"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	tp := filepath.Join(proj, "sess.jsonl")
	id := coworkIdentity(tp)
	if id.AccountUUID != acct || id.OrgID != org || id.Email != "dg@keld.co" {
		t.Fatalf("got %+v", id)
	}
}

func TestCoworkIdentityNonCoworkPath(t *testing.T) {
	if id := coworkIdentity("/Users/x/.claude/projects/p/s.jsonl"); id != (Identity{}) {
		t.Fatalf("expected zero identity, got %+v", id)
	}
}

func TestIdentityCacheMemoizes(t *testing.T) {
	c := newIdentityCache()
	a := c.forCowork("/no/match.jsonl")
	b := c.forCowork("/no/match.jsonl")
	if a != b {
		t.Fatal("cache should return stable value")
	}
}
