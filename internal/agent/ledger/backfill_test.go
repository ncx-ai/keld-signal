package ledger

import (
	"testing"
	"time"
)

// ⚠️ **The race this closes was found by running the Playwright suite twice.**
// The same generated corpus produced a repository-keyed suggestion on one run
// and a workspace-keyed one on the next, because the sidecar resolves a
// checkout in a whole-file pre-pass and the first blocks of a session close
// before it knows the repository. Their dims are written once and never
// revised, so that work stayed grouped under a bare directory name forever.
func TestBackfillTeachesEarlierBlocksTheRepository(t *testing.T) {
	setHome(t)
	s := New()
	now := time.Now().UTC()
	early := BlockKey{Session: "sess-1", Start: 1000}
	late := BlockKey{Session: "sess-1", Start: 2000}

	// The first block closes before the sidecar has resolved the checkout.
	s.Cut(early, 1600, "session_start", "budget", "claude_code", now)
	s.Observe(early, Dims{Workspace: "web"}, now)
	// The next one knows the remote.
	s.Cut(late, 2600, "budget", "idle", "claude_code", now)
	s.Observe(late, Dims{Repo: "github.com/acme/web", Workspace: "web"}, now)
	s.BackfillSessionDims("sess-1", Dims{Repo: "github.com/acme/web", Workspace: "web"}, now)

	recs, err := s.BlocksSince(time.Unix(0, 0), 10)
	if err != nil {
		t.Fatalf("BlocksSince: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("want 2 blocks, got %d", len(recs))
	}
	for _, r := range recs {
		if r.Repo != "github.com/acme/web" {
			t.Fatalf("every block of the session must carry the repository, got %+v", r)
		}
	}
}

// A branch genuinely changes inside a session, so copying a later one backwards
// would claim work happened on a branch that did not exist yet.
func TestBackfillNeverTouchesTheBranch(t *testing.T) {
	setHome(t)
	s := New()
	now := time.Now().UTC()
	k := BlockKey{Session: "sess-2", Start: 1000}
	s.Cut(k, 1600, "session_start", "budget", "claude_code", now)
	s.Observe(k, Dims{Branch: "main"}, now)

	s.BackfillSessionDims("sess-2", Dims{Repo: "github.com/acme/web", Branch: "feature/later"}, now)

	recs, _ := s.BlocksSince(time.Unix(0, 0), 10)
	if len(recs) != 1 {
		t.Fatalf("want 1 block, got %d", len(recs))
	}
	if recs[0].Branch != "main" {
		t.Fatalf("the branch must be left exactly as observed, got %q", recs[0].Branch)
	}
	if recs[0].Repo != "github.com/acme/web" {
		t.Fatalf("the repository should still be filled, got %q", recs[0].Repo)
	}
}

// It fills empties; it never rewrites what the cutter actually saw.
func TestBackfillNeverOverwritesAnObservedValue(t *testing.T) {
	setHome(t)
	s := New()
	now := time.Now().UTC()
	k := BlockKey{Session: "sess-3", Start: 1000}
	s.Cut(k, 1600, "session_start", "budget", "claude_code", now)
	s.Observe(k, Dims{Repo: "github.com/acme/first", Workspace: "first"}, now)

	s.BackfillSessionDims("sess-3", Dims{Repo: "github.com/acme/second", Workspace: "second"}, now)

	recs, _ := s.BlocksSince(time.Unix(0, 0), 10)
	if recs[0].Repo != "github.com/acme/first" || recs[0].Workspace != "first" {
		t.Fatalf("an observed value must win over a backfill, got %+v", recs[0])
	}
}

// Another session's blocks are untouched.
func TestBackfillIsScopedToItsSession(t *testing.T) {
	setHome(t)
	s := New()
	now := time.Now().UTC()
	mine := BlockKey{Session: "sess-4", Start: 1000}
	theirs := BlockKey{Session: "sess-5", Start: 1000}
	s.Cut(mine, 1600, "session_start", "budget", "claude_code", now)
	s.Cut(theirs, 1600, "session_start", "budget", "claude_code", now)
	s.Observe(theirs, Dims{Workspace: "other"}, now)

	s.BackfillSessionDims("sess-4", Dims{Repo: "github.com/acme/web"}, now)

	recs, _ := s.BlocksSince(time.Unix(0, 0), 10)
	for _, r := range recs {
		if r.Session == "sess-5" && r.Repo != "" {
			t.Fatalf("another session must be untouched, got %+v", r)
		}
	}
}
