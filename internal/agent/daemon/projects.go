package daemon

import (
	"encoding/json"
	"log"
	"os"
	"sync"

	"github.com/ncx-ai/keld-signal/internal/agent/settings"
)

// resolveProjects is the daemon's project-definition precedence:
// KELD_PROJECTS_FILE wins if set (the mock path for tests/smoke, reproducible
// regardless of org state), else the remote settings doc's `projects` key,
// else none. remote may be nil (startup, before the first settings poll
// lands) — that is exactly "not known yet", the same reading a nil
// Remote.Projects gets once polling has started.
//
// A KELD_PROJECTS_FILE that fails to load is reported and treated as "no
// projects" rather than crashing the daemon or silently falling through to
// the remote key — an operator who set the env var deliberately wants THAT
// file honoured, and a stale remote list is worse than an honest empty one.
func resolveProjects(remote *settings.Remote) []settings.RemoteProject {
	if p := os.Getenv(settings.EnvProjectsFile); p != "" {
		list, err := settings.LoadProjectsFile(p)
		if err != nil {
			log.Printf("keld-agent: %s=%s could not be read: %v — attribution runs with no declared projects",
				settings.EnvProjectsFile, p, err)
			return nil
		}
		return list
	}
	if remote != nil && remote.Projects != nil {
		return *remote.Projects
	}
	return nil
}

// projectsChanged reports whether two resolved project lists differ, so the
// daemon calls PostProjects only when something actually changed rather than
// on every settings poll (default every 5 minutes).
func projectsChanged(a, b []settings.RemoteProject) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) != string(bb)
}

// projectsState is a mutex-guarded box for the last project list this daemon
// successfully told the sidecar about.
//
// ⚠️ C4 FIX: it exists BECAUSE the initial POST moved off the synchronous
// startup path onto its own goroutine (see Run — a cold-starting sidecar is
// not listening yet, and a synchronous call there stalled the whole daemon
// behind postProjectsCallTimeout). That startup goroutine and onRemote's own
// goroutine (the settings poll) can therefore both attempt to update this
// value. The daemon used to rely on a bare `var lastProjects` plus "the
// startup write happens-before `go pollSettings` starts" — correct for
// exactly one writer, and it stopped being exactly one writer the moment the
// startup POST became asynchronous. A mutex is simpler to get right here than
// re-deriving a second happens-before argument for a second goroutine, and it
// is cheap: this is updated at most once per settings poll (default 5m) plus
// once at startup.
type projectsState struct {
	mu    sync.Mutex
	value []settings.RemoteProject
}

// changed reports whether next differs from the currently held value.
func (p *projectsState) changed(next []settings.RemoteProject) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return projectsChanged(p.value, next)
}

// set records v as the last list successfully POSTed. Called ONLY after
// svc.PostProjects itself succeeded, so a failed post leaves the held value
// stale on purpose — a later poll (or the failed call itself, retried) will
// see it as still "changed" and try again, rather than a failure being
// silently remembered as success.
func (p *projectsState) set(v []settings.RemoteProject) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.value = v
}

// maybePostProjectsAtStartup is the startup half of the C4 async-POST fix,
// and it exists to close the race that fix itself reopened (NB1, round-2
// review).
//
// ⚠️ THE RACE: pollSettings runs its first poll (and so onRemote's own
// PostProjects call) essentially immediately, concurrently with THIS
// goroutine — both retrying against the same cold, just-spawned sidecar with
// independent backoff. Before this fix, the startup call posted
// resolveProjects(nil), which is an EMPTY list on any machine without
// KELD_PROJECTS_FILE (today, that is every machine — Atlas does not serve
// `projects` yet, so remote.Projects is always nil and onRemote's own
// resolveProjects(r) is ALSO empty in practice; env-file-set machines resolve
// identically at both call sites since the env always wins regardless of
// r). If the startup call's older, longer-backing-off attempt happened to
// land AFTER onRemote's had already told the sidecar about a real list, the
// sidecar would end up holding NO projects — every subsequent /attribute
// answers skipped:no_projects, which the attributor's switch treats as
// TERMINAL: publish and delete. Those blocks are permanently attributed to
// nothing, and by the time the next poll (5 minutes later) could correct it,
// the blocks are already gone from the store.
//
// TWO GUARDS, matching the review's suggested shape:
//  1. An EMPTY resolved list is never posted at startup at all. The sidecar's
//     own un-POSTed-to state already reads as "no projects", so skipping costs
//     nothing when there is genuinely nothing to say yet, and it structurally
//     cannot be the empty list that clobbers a real one, because it is never
//     sent.
//  2. Immediately before posting a NON-empty list, state.changed(p) is
//     re-checked — if onRemote's own poll has already landed with the same
//     (or a different) resolved value, this call is now redundant and skips.
//     This does not fully serialize the two goroutines (a genuine race window
//     remains between the check and the network call completing), but
//     combined with guard 1 it closes every failure mode reachable under the
//     CURRENT Atlas contract, where the two call sites can only ever disagree
//     via a resolveProjects(nil) that resolves empty — which guard 1 already
//     refuses to send.
func maybePostProjectsAtStartup(post func([]settings.RemoteProject) error, state *projectsState, p []settings.RemoteProject) {
	if len(p) == 0 {
		return // NB1 guard 1: never let an empty startup resolution race a real list
	}
	if !state.changed(p) {
		return // NB1 guard 2: a concurrent update (the settings poll) already landed
	}
	if err := post(p); err != nil {
		log.Printf("keld-agent: initial /projects post failed: %v", err)
		return
	}
	state.set(p)
}

// postProjectsOnChange is onRemote's half of the same gate
// maybePostProjectsAtStartup implements for the startup half: resolve r's
// project list and post it only if it differs from state's currently held
// value. Extracted alongside maybePostProjectsAtStartup so both halves of the
// NB1 fix are exercised by the same kind of direct, deterministic unit test
// rather than only through daemon.Run (which nothing in this codebase
// exercises end to end).
func postProjectsOnChange(post func([]settings.RemoteProject) error, state *projectsState, r *settings.Remote) {
	p := resolveProjects(r)
	if !state.changed(p) {
		return
	}
	if err := post(p); err != nil {
		log.Printf("keld-agent: /projects update failed: %v", err)
		return
	}
	state.set(p)
}
