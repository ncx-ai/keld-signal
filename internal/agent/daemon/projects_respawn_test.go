package daemon

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/publish"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
)

// C4 — A SIDECAR RESTART LOSES THE PROJECT LIST AND THE DAEMON MUST SAY IT
// AGAIN.
//
// attribution._projects is module state in the sidecar PARENT process, so the
// supervisor's crash-restart takes it. The daemon's own POST is gated on
// `!state.changed(next)`, so it concluded there was nothing to say: every
// /attribute answered skipped:no_projects, which was TERMINAL — publish and
// delete — so every block after the crash was permanently attributed to
// nothing until the DAEMON itself restarted.

type recordingPoster struct {
	mu    sync.Mutex
	posts [][]settings.RemoteProject
	fail  error
}

func (r *recordingPoster) post(p []settings.RemoteProject) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail != nil {
		return r.fail
	}
	r.posts = append(r.posts, p)
	return nil
}

func (r *recordingPoster) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.posts)
}

func TestARespawnRePostsTheProjectListEvenThoughNothingChanged(t *testing.T) {
	list := []settings.RemoteProject{{ID: "proj_a", Title: "A"}}
	state := &projectsState{}
	rec := &recordingPoster{}

	// Normal startup: the list is resolved, believed, and posted once.
	postProjectsIfKnownNonEmpty(rec.post, state, projectsResolution{list: list, ok: true}, "%v")
	if rec.count() != 1 {
		t.Fatalf("startup should post once, got %d", rec.count())
	}
	// Nothing changed, so an ordinary re-resolve stays silent — this is the
	// guard that made the defect permanent, and it is still correct.
	postProjectsIfKnownNonEmpty(rec.post, state, projectsResolution{list: list, ok: true}, "%v")
	if rec.count() != 1 {
		t.Fatalf("an unchanged list must not be re-posted on the ordinary path, got %d", rec.count())
	}

	// The sidecar restarts.
	repostProjectsAfterRespawn(rec.post, state)
	if rec.count() != 2 {
		t.Fatalf("a respawn must re-post the list the sidecar lost, got %d posts", rec.count())
	}
	if rec.posts[1][0].ID != "proj_a" {
		t.Fatalf("the re-post carried %+v", rec.posts[1])
	}
	// And the state is consistent again afterwards: a subsequent unchanged
	// resolve is still silent rather than posting forever.
	postProjectsIfKnownNonEmpty(rec.post, state, projectsResolution{list: list, ok: true}, "%v")
	if rec.count() != 2 {
		t.Fatalf("the re-post must restore the change-gate, got %d", rec.count())
	}
}

// Never invent a POST out of nothing: a machine that has declared no projects
// has nothing to re-tell a restarted sidecar, and posting an empty list is the
// exact clobber postProjectsIfKnownNonEmpty's empty-skip guard exists to stop.
func TestARespawnPostsNothingWhenNoProjectsWereEverDeclared(t *testing.T) {
	rec := &recordingPoster{}
	repostProjectsAfterRespawn(rec.post, &projectsState{})
	if rec.count() != 0 {
		t.Fatalf("nothing was ever declared; got %d posts", rec.count())
	}
	// And a nil poster (no sidecar this run) must not panic.
	repostProjectsAfterRespawn(nil, &projectsState{})
}

// A FAILED re-post must not be remembered as success — the same rule
// projectsState.set already follows — so the next respawn (or the next real
// change) tries again.
func TestAFailedRePostLeavesTheStateAbleToTryAgain(t *testing.T) {
	list := []settings.RemoteProject{{ID: "proj_a"}}
	state := &projectsState{}
	rec := &recordingPoster{fail: errors.New("sidecar unreachable")}
	state.observe(projectsResolution{list: list, ok: true})

	repostProjectsAfterRespawn(rec.post, state)
	rec.fail = nil
	repostProjectsAfterRespawn(rec.post, state)
	if rec.count() != 1 {
		t.Fatalf("the second attempt should have landed, got %d", rec.count())
	}
}

// I8 — THE STARTUP RACE. `observe` runs synchronously, before the POST
// goroutine and before the attributor's first drain, so the daemon's BELIEF is
// established even while the POST is still in flight. That is what makes the
// attributor hold a `skipped:no_projects` answer in that window instead of
// publishing the block attributed to nothing and deleting it.
func TestTheDaemonBelievesItsProjectListBeforeTheFirstPostLands(t *testing.T) {
	state := &projectsState{}
	if state.knownNonEmpty() {
		t.Fatal("a fresh state believes nothing")
	}
	state.observe(projectsResolution{list: []settings.RemoteProject{{ID: "proj_a"}}, ok: true})
	if !state.knownNonEmpty() {
		t.Fatal("the belief must exist before any POST has succeeded — that is the whole of I8")
	}
	// The two facts stay distinct: nothing has been successfully told yet, so
	// the list still reads as changed.
	if !state.changed([]settings.RemoteProject{{ID: "proj_a"}}) {
		t.Fatal("observing a list must not be mistaken for having posted it")
	}
}

// An untrustworthy or empty resolution must never become a belief: that is the
// same read-error-is-not-an-empty-list rule projectsResolution exists for, and
// believing an empty one would make `skipped:no_projects` terminal again.
func TestAnUntrustworthyOrEmptyResolutionIsNeverBelieved(t *testing.T) {
	state := &projectsState{}
	state.observe(projectsResolution{list: []settings.RemoteProject{{ID: "proj_a"}}, ok: true})
	state.observe(projectsResolution{ok: false}) // a KELD_PROJECTS_FILE read error
	state.observe(projectsResolution{ok: true})  // a trustworthy empty
	if !state.knownNonEmpty() {
		t.Fatal("neither a read error nor an empty resolution may erase a known list")
	}
	if got := state.declaredList(); len(got) != 1 || got[0].ID != "proj_a" {
		t.Fatalf("the believed list changed: %+v", got)
	}
}

// The supervisor half: the hook fires when a REPLACEMENT child becomes healthy,
// and never for the first one (startup already posts on its own path).
func TestTheSupervisorFiresOnRespawnOnlyForAReplacementChild(t *testing.T) {
	dir := t.TempDir()
	// The child exits as soon as this file appears — and REMOVES it on the way
	// out, so exactly one generation dies and the replacement stays up long
	// enough to become healthy.
	die := filepath.Join(dir, "die")
	spawn := func(int) (*exec.Cmd, error) {
		return exec.Command("sh", "-c",
			`while [ ! -f "$D" ]; do sleep 0.02; done; rm -f "$D"; exit 1`), nil
	}
	spawnWithEnv := func(p int) (*exec.Cmd, error) {
		c, err := spawn(p)
		if err != nil {
			return nil, err
		}
		c.Env = append(os.Environ(), "D="+die)
		return c, nil
	}
	s := NewSupervisor(spawnWithEnv, 0, func() bool { return true }, 5*time.Second)
	s.stopGrace = 200 * time.Millisecond

	var mu sync.Mutex
	fired := 0
	s.SetOnRespawn(func() { mu.Lock(); fired++; mu.Unlock() })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Start(ctx)

	// First generation healthy: no hook.
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline) && !s.Ready(); {
		time.Sleep(10 * time.Millisecond)
	}
	if !s.Ready() {
		t.Fatal("the first child never became healthy")
	}
	mu.Lock()
	first := fired
	mu.Unlock()
	if first != 0 {
		t.Fatalf("the hook must not fire for the FIRST child; startup posts on its own path (fired=%d)", first)
	}

	// Kill generation one; the replacement must fire the hook.
	if err := os.WriteFile(die, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := fired
		mu.Unlock()
		if n > 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("a restarted sidecar did not fire the respawn hook — the project list it lost is never re-posted")
}

// I7 — 4.2 GB OF MODELS MUST NOT BE FETCHED FOR AN ORG THAT HAS DECLARED
// NOTHING. Every /attribute on such a machine answers skipped:no_projects
// without loading a model, and Atlas does not serve `projects` yet, so
// ungated this charged both downloads to every machine in a fleet that had
// switched attribution on early.
func TestModelDemandIsGatedOnAKnownNonEmptyProjectList(t *testing.T) {
	var demanded, forwarded int
	known := false
	hook := demandModelsForAttribution(
		func(_ []publish.BlockEnrichment, _ string) { forwarded++ },
		func() bool { return known },
		func() { demanded++ }, func() { demanded++ })

	hook(nil, "/tmp/x.jsonl")
	if demanded != 0 {
		t.Fatalf("no projects declared: nothing may be downloaded, got %d demands", demanded)
	}
	if forwarded != 1 {
		t.Fatal("the wrapped hook must still run — attribution jobs are scheduled either way")
	}

	// A list arriving on a later settings poll must start the fetch with no
	// restart: the gate is read live, per block, never captured.
	known = true
	hook(nil, "/tmp/x.jsonl")
	if demanded != 2 {
		t.Fatalf("both provisioners must be demanded once a list is known, got %d", demanded)
	}
	if forwarded != 2 {
		t.Fatalf("forwarded=%d", forwarded)
	}
}

func TestModelDemandWrapperIsNilWhenAttributionIsOff(t *testing.T) {
	if got := demandModelsForAttribution(nil, func() bool { return true }, func() {}); got != nil {
		t.Fatal("no attributor means no wrapper — a machine with attribution off pays nothing")
	}
}
