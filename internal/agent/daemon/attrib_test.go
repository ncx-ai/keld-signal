package daemon

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/attrib"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/sidecar"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
)

// The attribution path must ship OFF, mirroring blocks.Enabled's shape
// exactly: neither KELD_ATTRIBUTION nor the `attribution` config key set.
func TestTheAttributorIsOffByDefault(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	t.Setenv(attrib.EnvEnabled, "")
	if got := startAttributor(context.Background(), stubDigester{}, &fakeAttribClient{},
		"https://a/v1/x", func() string { return "tok" }, "actor", nil, false); got != nil {
		t.Fatal("the attributor started with KELD_ATTRIBUTION unset and no config key")
	}
}

// The installer's path: agent-config.json says attribution:true, no
// environment variable set anywhere — the same reason blocks.Enabled needs a
// config key at all (no service definition on any OS carries an environment
// block).
func TestTheAttributorStartsFromTheConfigKeyAlone(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	t.Setenv(attrib.EnvEnabled, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if got := startAttributor(ctx, stubDigester{}, &fakeAttribClient{},
		"https://a/v1/x", func() string { return "tok" }, "actor", nil, true); got == nil {
		t.Fatal("the attributor stayed off with attribution:true in agent-config.json")
	}
}

// KELD_ATTRIBUTION=0 must override a true config key either way.
func TestKeldAttributionZeroOverridesTheConfigKey(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	t.Setenv(attrib.EnvEnabled, "0")
	if got := startAttributor(context.Background(), stubDigester{}, &fakeAttribClient{},
		"https://a/v1/x", func() string { return "tok" }, "actor", nil, true); got != nil {
		t.Fatal("KELD_ATTRIBUTION=0 did not override attribution:true in agent-config.json")
	}
}

// With no digester or no attribute client there is nothing to schedule
// against or ask, so the path stays off rather than starting a loop that can
// only fail.
func TestTheAttributorNeedsADigesterAndClient(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	t.Setenv(attrib.EnvEnabled, "1")
	if got := startAttributor(context.Background(), nil, &fakeAttribClient{},
		"https://a/v1/x", func() string { return "tok" }, "actor", nil, false); got != nil {
		t.Fatal("the attributor started with no digester")
	}
	if got := startAttributor(context.Background(), stubDigester{}, nil,
		"https://a/v1/x", func() string { return "tok" }, "actor", nil, false); got != nil {
		t.Fatal("the attributor started with no attribute client")
	}
}

type fakeAttribClient struct{}

func (fakeAttribClient) Attribute(path, sessionID string, start, end float64, dims map[string]string) (sidecar.AttributeResult, bool) {
	return sidecar.AttributeResult{}, false
}

// The REAL sidecar client must satisfy both new capabilities, or facetsFor
// would silently leave them nil and the path would never start.
func TestTheAttributionCapabilitiesAreServiceFacetsOfTheRealClient(t *testing.T) {
	var _ attributionClient = (*sidecar.Client)(nil)
	var _ projectsPoster = (*sidecar.Client)(nil)
	var _ attrib.AttributeClient = (*sidecar.Client)(nil)
	if f := facetsFor(nil, nil); f.Attribution != nil || f.PostProjects != nil {
		t.Error("no client means no attribution capability")
	}
	c := sidecar.New("http://127.0.0.1:1", time.Second)
	f := facetsFor(c, nil)
	if f.Attribution == nil {
		t.Error("the real client must advertise the attribution client")
	}
	if f.PostProjects == nil {
		t.Error("the real client must advertise PostProjects")
	}
}

// KELD_PROJECTS_FILE wins over the remote settings key.
func TestResolveProjectsPrecedence(t *testing.T) {
	t.Run("env file wins", func(t *testing.T) {
		dir := t.TempDir()
		p := dir + "/projects.json"
		if err := writeFile(p, `[{"id":"proj_env","title":"Env"}]`); err != nil {
			t.Fatal(err)
		}
		t.Setenv(settings.EnvProjectsFile, p)
		remote := &settings.Remote{Projects: &[]settings.RemoteProject{{ID: "proj_remote"}}}
		got := resolveProjects(remote)
		if !got.ok || len(got.list) != 1 || got.list[0].ID != "proj_env" {
			t.Fatalf("got %+v, want the env file's project", got)
		}
	})
	t.Run("remote key when no env file", func(t *testing.T) {
		t.Setenv(settings.EnvProjectsFile, "")
		remote := &settings.Remote{Projects: &[]settings.RemoteProject{{ID: "proj_remote"}}}
		got := resolveProjects(remote)
		if !got.ok || len(got.list) != 1 || got.list[0].ID != "proj_remote" {
			t.Fatalf("got %+v, want the remote project", got)
		}
	})
	t.Run("none when neither is set", func(t *testing.T) {
		t.Setenv(settings.EnvProjectsFile, "")
		if got := resolveProjects(nil); !got.ok || got.list != nil {
			t.Fatalf("got %+v, want ok=true, list=nil", got)
		}
		if got := resolveProjects(&settings.Remote{}); !got.ok || got.list != nil {
			t.Fatalf("got %+v, want ok=true, list=nil (remote.Projects unset)", got)
		}
	})
	// Finding 1 (round 3): a read error is a DIFFERENT fact from "the file
	// says there are no projects" — ok must be false, not merely list==nil,
	// or a transient failure reads identically to a trustworthy empty and a
	// caller has no way to refuse posting it.
	t.Run("unreadable env file yields ok=false, not an empty-but-trustworthy list", func(t *testing.T) {
		t.Setenv(settings.EnvProjectsFile, "/does/not/exist.json")
		remote := &settings.Remote{Projects: &[]settings.RemoteProject{{ID: "proj_remote"}}}
		got := resolveProjects(remote)
		if got.ok {
			t.Fatalf("got %+v, want ok=false — a read error is not a trustworthy answer", got)
		}
		if got.list != nil {
			t.Fatalf("got %+v, want a nil list alongside ok=false", got)
		}
	})
}

func TestProjectsChanged(t *testing.T) {
	a := []settings.RemoteProject{{ID: "p1"}}
	b := []settings.RemoteProject{{ID: "p1"}}
	c := []settings.RemoteProject{{ID: "p2"}}
	if projectsChanged(a, b) {
		t.Fatal("identical lists must not read as changed")
	}
	if !projectsChanged(a, c) {
		t.Fatal("different lists must read as changed")
	}
	if !projectsChanged(nil, a) {
		t.Fatal("nil -> non-nil must read as changed")
	}
	if projectsChanged(nil, nil) {
		t.Fatal("nil -> nil must not read as changed")
	}
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

// C4: lastProjects moved from a bare var to projectsState because it is now
// written by TWO goroutines — the initial startup POST (moved off the
// synchronous path) and onRemote on the poll goroutine. This pins its two
// contracts directly: concurrent access must not race, and a FAILED post
// must leave the held value stale (never latch a failure as success).
func TestProjectsStateIsConcurrencySafe(t *testing.T) {
	ps := &projectsState{}
	done := make(chan struct{})
	go func() {
		for i := 0; i < 200; i++ {
			ps.set([]settings.RemoteProject{{ID: "from-goroutine-a"}})
		}
		close(done)
	}()
	for i := 0; i < 200; i++ {
		ps.changed([]settings.RemoteProject{{ID: "from-main"}})
	}
	<-done
}

func TestProjectsStateSetOnlyOnSuccess(t *testing.T) {
	ps := &projectsState{}
	p := []settings.RemoteProject{{ID: "proj_a"}}
	if !ps.changed(p) {
		t.Fatal("an empty state must read a non-nil list as changed")
	}
	// Simulate a failed post: never call set. The value must still read as
	// changed on the next attempt, not silently accepted as already-current.
	if !ps.changed(p) {
		t.Fatal("a failed post must not be remembered as success — the same list must still read as changed")
	}
	ps.set(p)
	if ps.changed(p) {
		t.Fatal("after a successful set, the identical list must read as unchanged")
	}
}

// NB1 (round 2 review): the startup POST must NEVER send an empty resolved
// list — that emptiness is precisely what could clobber a real list the
// concurrent settings poll already told the sidecar about.
func TestMaybePostProjectsAtStartupSkipsAnEmptyList(t *testing.T) {
	ps := &projectsState{}
	var calls int
	post := func(p []settings.RemoteProject) error {
		calls++
		return nil
	}
	maybePostProjectsAtStartup(post, ps, projectsResolution{ok: true})
	maybePostProjectsAtStartup(post, ps, projectsResolution{list: []settings.RemoteProject{}, ok: true})
	if calls != 0 {
		t.Fatalf("post called %d times for an empty/nil list, want 0", calls)
	}
}

// Finding 1 (round 3 review): an UNTRUSTWORTHY resolution (ok=false — a
// transient KELD_PROJECTS_FILE read error) must never be posted either, even
// though a resolution can name a non-empty list — ok is checked before
// content, not instead of the empty-list guard.
func TestMaybePostProjectsAtStartupSkipsAReadError(t *testing.T) {
	ps := &projectsState{}
	var calls int
	post := func(p []settings.RemoteProject) error {
		calls++
		return nil
	}
	// ok=false with a non-nil list would be a malformed resolution in
	// practice (resolveProjects never constructs one), but the gate must not
	// rely on that — it checks ok first, unconditionally.
	maybePostProjectsAtStartup(post, ps, projectsResolution{list: []settings.RemoteProject{{ID: "should-never-post"}}, ok: false})
	if calls != 0 {
		t.Fatalf("post called %d times for an untrustworthy resolution, want 0", calls)
	}
}

// NB1: when the settings poll has already told the sidecar about a list
// (matching what the startup goroutine itself resolved), the startup
// goroutine's own POST must be skipped as redundant rather than re-sending
// it — the re-check-before-POST guard.
func TestMaybePostProjectsAtStartupSkipsWhenAlreadyPosted(t *testing.T) {
	ps := &projectsState{}
	p := []settings.RemoteProject{{ID: "proj_env"}}
	ps.set(p) // simulate: the poll goroutine already posted this exact list
	var calls int
	post := func(got []settings.RemoteProject) error {
		calls++
		return nil
	}
	maybePostProjectsAtStartup(post, ps, projectsResolution{list: p, ok: true})
	if calls != 0 {
		t.Fatalf("post called %d times for an already-current list, want 0 (redundant POST)", calls)
	}
}

func TestMaybePostProjectsAtStartupPostsAndRecordsOnSuccess(t *testing.T) {
	ps := &projectsState{}
	p := []settings.RemoteProject{{ID: "proj_env"}}
	var got []settings.RemoteProject
	post := func(v []settings.RemoteProject) error {
		got = v
		return nil
	}
	maybePostProjectsAtStartup(post, ps, projectsResolution{list: p, ok: true})
	if len(got) != 1 || got[0].ID != "proj_env" {
		t.Fatalf("post received %+v, want %+v", got, p)
	}
	if ps.changed(p) {
		t.Fatal("state must be recorded as current after a successful post")
	}
}

func TestMaybePostProjectsAtStartupDoesNotRecordOnFailure(t *testing.T) {
	ps := &projectsState{}
	p := []settings.RemoteProject{{ID: "proj_env"}}
	post := func(v []settings.RemoteProject) error { return errors.New("sidecar unreachable") }
	maybePostProjectsAtStartup(post, ps, projectsResolution{list: p, ok: true})
	if !ps.changed(p) {
		t.Fatal("a failed post must not be recorded as current — a later attempt must still see it as changed")
	}
}

// NB1's regression guard: run the startup helper and the poll helper
// CONCURRENTLY, as real goroutines, with the startup side resolving an EMPTY
// list (resolveProjects(nil) — what every machine without KELD_PROJECTS_FILE
// resolves to today, since Atlas does not yet serve `projects`) and the poll
// side resolving a REAL one. Regardless of goroutine scheduling, the sidecar
// (the fake `post` sink here) must never end up holding the empty list.
//
// ⚠️ CORRECTED (Finding 2, round 3): this comment used to claim the race is
// "structurally impossible to lose" — that overclaims what THIS test proves.
// For THIS specific construction (one side always empty, the other always
// real), guard 1 alone (never post an empty list — pinned deterministically,
// on its own, by TestMaybePostProjectsAtStartupSkipsAnEmptyList) is what
// makes the outcome deterministic: the startup goroutine never calls `post`
// at all, so there is nothing left to race. Guard 2 (the changed()
// re-check immediately before posting) is a separate, PROBABILISTIC
// defense-in-depth for a narrower scenario this construction does not
// exercise — two goroutines both resolving genuinely different NON-EMPTY
// values (not reachable today, since resolveProjects's env-file-wins
// precedence makes the two call sites agree whenever KELD_PROJECTS_FILE is
// set — see postProjectsIfKnownNonEmpty's doc comment). Running many
// concurrent iterations here is a real (not sleep-based) exercise of
// goroutine scheduling and is worth keeping, but it does not itself prove
// guard 2 is race-free; it corroborates guard 1's determinism repeatedly
// rather than adding a second deterministic proof.
func TestNB1StartupNeverClobbersAConcurrentPollWithAnEmptyList(t *testing.T) {
	t.Setenv(settings.EnvProjectsFile, "") // resolveProjects(nil) must resolve empty, not to a leftover env file
	for iter := 0; iter < 50; iter++ {
		state := &projectsState{}
		var mu sync.Mutex
		var posted []settings.RemoteProject
		post := func(p []settings.RemoteProject) error {
			mu.Lock()
			posted = p
			mu.Unlock()
			return nil
		}
		real := []settings.RemoteProject{{ID: "proj_real"}}
		remote := &settings.Remote{Projects: &real}

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			maybePostProjectsAtStartup(post, state, resolveProjects(nil)) // empty: no KELD_PROJECTS_FILE
		}()
		go func() {
			defer wg.Done()
			postProjectsOnChange(post, state, remote)
		}()
		wg.Wait()

		mu.Lock()
		got := posted
		mu.Unlock()
		if len(got) == 0 {
			t.Fatalf("iter %d: the sidecar ended up with no projects even though a real list was available", iter)
		}
		if got[0].ID != "proj_real" {
			t.Fatalf("iter %d: posted = %+v, want the real list", iter, got)
		}
	}
}

// Finding 1 (round 3 review): the poll path had no empty-list guard at all
// before this fix — byte-identical to the pre-NB1 inline code, which is
// exactly why the reviewer asked for it anyway even though it isn't new
// breakage. Mirrors TestMaybePostProjectsAtStartupSkipsAnEmptyList for the
// poll half.
func TestPostProjectsOnChangeSkipsAnEmptyList(t *testing.T) {
	t.Setenv(settings.EnvProjectsFile, "")
	ps := &projectsState{}
	var calls int
	post := func(p []settings.RemoteProject) error {
		calls++
		return nil
	}
	postProjectsOnChange(post, ps, nil)
	postProjectsOnChange(post, ps, &settings.Remote{})
	if calls != 0 {
		t.Fatalf("post called %d times for an empty/nil resolution, want 0", calls)
	}
}

// Finding 1's named covering test: a TRANSIENT KELD_PROJECTS_FILE read
// failure at POLL time must NOT clear a previously-known-good list — that is
// precisely NB1's permanent-mis-attribution outcome, arriving through the
// poll door instead of the startup door. Before projectsResolution.ok
// existed, a read error and "the file legitimately declares zero projects"
// were the same value (nil) and this test would have failed: the read error
// would have posted an empty list right over the good one.
func TestPostProjectsOnChangeDoesNotClearAGoodListOnAReadError(t *testing.T) {
	ps := &projectsState{}
	good := []settings.RemoteProject{{ID: "proj_good"}}
	ps.set(good) // simulate: a prior successful post already told the sidecar about a real list

	t.Setenv(settings.EnvProjectsFile, "/does/not/exist.json") // set but unreadable: a transient failure
	var calls int
	post := func(p []settings.RemoteProject) error {
		calls++
		return nil
	}
	postProjectsOnChange(post, ps, &settings.Remote{Projects: &[]settings.RemoteProject{{ID: "proj_would_be_wrong_anyway"}}})

	if calls != 0 {
		t.Fatalf("post called %d times on a read error, want 0 — a transient failure must never reach the sidecar", calls)
	}
	// state must still read as "changed" against the good list — i.e. it was
	// never touched — so a later successful poll still tries to reconcile,
	// rather than the read-error silently being treated as "nothing to do".
	if ps.changed(good) {
		t.Fatal("projectsState must be untouched by a read error — it still holds the previously-known-good list")
	}
}
