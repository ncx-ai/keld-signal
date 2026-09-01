package daemon

import (
	"encoding/json"
	"log"
	"os"
	"sync"

	"github.com/ncx-ai/keld-signal/internal/agent/settings"
)

// projectsResolution is resolveProjects's answer: the resolved list PLUS
// whether the resolution itself is TRUSTWORTHY.
//
// ⚠️ ROUND 3 FIX (Finding 1): a bare []RemoteProject collapses two different
// facts into one nil — "the file/remote key genuinely declares zero
// projects" and "I could not tell, because KELD_PROJECTS_FILE could not be
// read" — and only the first of those may ever reach a PostProjects call.
// Before this type existed, a TRANSIENT failure to read the env file at poll
// time (a momentary permission glitch, the file mid-rewrite, a flaky mount —
// anything that clears on the next poll) resolved to nil exactly like an
// honest "nothing declared", and posting that nil over a sidecar that
// already held a real list produced the identical permanent-mis-attribution
// outcome NB1 fixed on the startup side: the sidecar ends up with no
// projects, every /attribute answers skipped:no_projects, and the
// attributor's own (correct) switch treats that as TERMINAL — publish and
// delete. A read error must instead leave whatever the daemon already
// believes in place.
type projectsResolution struct {
	list []settings.RemoteProject
	// ok is false ONLY when the source that would have answered
	// authoritatively (KELD_PROJECTS_FILE, when the env var is set) could not
	// be read. It is true for every other case, including "nothing is
	// declared anywhere" — that is an honest, actionable "empty" the daemon
	// may safely act on (though see the empty-skip guard in
	// postProjectsIfKnownNonEmpty for why even a trustworthy empty is never
	// itself POSTed).
	ok bool
}

// resolveProjects is the daemon's project-definition precedence:
// KELD_PROJECTS_FILE wins if set (the mock path for tests/smoke, reproducible
// regardless of org state), else the remote settings doc's `projects` key,
// else none. remote may be nil (startup, before the first settings poll
// lands) — that is exactly "not known yet", the same reading a nil
// Remote.Projects gets once polling has started.
//
// A KELD_PROJECTS_FILE that fails to load returns ok=false — see
// projectsResolution's doc comment for why that must NOT collapse into the
// same answer as "the file says there are no projects".
func resolveProjects(remote *settings.Remote) projectsResolution {
	if p := os.Getenv(settings.EnvProjectsFile); p != "" {
		list, err := settings.LoadProjectsFile(p)
		if err != nil {
			log.Printf("keld-agent: %s=%s could not be read: %v — leaving the previously known project list in place",
				settings.EnvProjectsFile, p, err)
			return projectsResolution{ok: false}
		}
		return projectsResolution{list: list, ok: true}
	}
	if remote != nil && remote.Projects != nil {
		return projectsResolution{list: *remote.Projects, ok: true}
	}
	return projectsResolution{ok: true} // genuinely nothing declared anywhere — a trustworthy empty
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
	// declared is the last TRUSTWORTHY NON-EMPTY resolution this daemon saw,
	// whether or not the POST that followed it succeeded — i.e. what the
	// daemon BELIEVES is declared, as opposed to `value`, what it believes it
	// successfully told the sidecar.
	//
	// ⚠️ THE TWO ARE DIFFERENT AND CONFLATING THEM IS C4. `value` answers "do
	// I need to send anything?"; `declared` answers "should the sidecar have
	// projects at all?". The second question is the one the attributor asks
	// when /attribute comes back `skipped:no_projects`, and it has to be
	// answerable BEFORE the first POST has landed — otherwise the startup race
	// (I8) reads a not-yet-posted list as "this machine genuinely declares
	// nothing" and every block drained in that window is published attributed
	// to nothing and deleted.
	declared []settings.RemoteProject
}

// changed reports whether next differs from the currently held value.
func (p *projectsState) changed(next []settings.RemoteProject) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return projectsChanged(p.value, next)
}

// observe records a trustworthy, non-empty resolution as what the daemon
// believes is declared — independent of whether the sidecar has been told yet.
// A resolution that is untrustworthy (a KELD_PROJECTS_FILE read error) or
// empty leaves the previous belief alone, for exactly the reasons
// projectsResolution and postProjectsIfKnownNonEmpty already give.
func (p *projectsState) observe(r projectsResolution) {
	if !r.ok || len(r.list) == 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.declared = r.list
}

// knownNonEmpty reports whether the daemon believes projects are declared on
// this machine. This is the predicate that makes `skipped:no_projects`
// non-terminal: if the daemon holds a list, an answer of "no projects" is a
// statement about the SIDECAR's state, not about the org's.
func (p *projectsState) knownNonEmpty() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.declared) > 0
}

// forget clears the record of what the sidecar has been told — never what the
// daemon believes is declared. Called when the sidecar is known to have
// restarted: `attribution._projects` is module state in the sidecar PARENT, so
// a respawn takes the list with it, and without this the daemon's
// `!state.changed(next)` guard concludes there is nothing to say and the
// sidecar answers `skipped:no_projects` forever.
func (p *projectsState) forget() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.value = nil
}

// declaredList returns a copy of what the daemon believes is declared.
func (p *projectsState) declaredList() []settings.RemoteProject {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.declared) == 0 {
		return nil
	}
	out := make([]settings.RemoteProject, len(p.declared))
	copy(out, p.declared)
	return out
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

// postProjectsIfKnownNonEmpty is the ONE gate both maybePostProjectsAtStartup
// and postProjectsOnChange apply — extracted in round 3 (Finding 1) so the
// startup half's hardening and the poll half's are structurally the SAME
// code rather than two hand-kept-in-sync copies. A resolution may only ever
// reach `post` when it is:
//
//  1. TRUSTWORTHY (r.ok). An untrustworthy resolution (a transient
//     KELD_PROJECTS_FILE read error) must leave state — and so the
//     sidecar — exactly as it was; see projectsResolution's doc comment.
//  2. NON-EMPTY. An empty resolution is never itself posted, from EITHER
//     call site — the sidecar's own never-been-told-anything state already
//     reads as "no projects" (skipped:no_projects either way), so skipping
//     costs nothing when there is genuinely nothing to say, and it
//     structurally cannot be the write that clobbers a real list, because it
//     is never sent. (This means an operator cannot use an explicit `[]` in
//     KELD_PROJECTS_FILE to CLEAR a previously-declared list via this path —
//     an accepted, deliberate limitation of the simplest fix for the
//     permanent-mis-attribution failure mode; see the NB1/round-2 report.)
//  3. CHANGED. Re-checked immediately before posting, so a resolution that a
//     concurrent update already made redundant is skipped rather than
//     re-sent.
func postProjectsIfKnownNonEmpty(post func([]settings.RemoteProject) error, state *projectsState, r projectsResolution, failLogFmt string) {
	if !r.ok {
		return // Finding 1: never let a read error clobber a known-good list
	}
	if len(r.list) == 0 {
		return // NB1 guard: never let an empty resolution race a real list
	}
	// Record the BELIEF before the changed-check can short-circuit: whether
	// the sidecar has to be told is a different question from whether this
	// machine declares projects at all, and only the second one makes
	// `skipped:no_projects` non-terminal. See projectsState.declared.
	state.observe(r)
	if !state.changed(r.list) {
		return // a concurrent update already landed
	}
	if err := post(r.list); err != nil {
		log.Printf(failLogFmt, err)
		return
	}
	state.set(r.list)
}

// maybePostProjectsAtStartup is the startup half of the C4 async-POST fix,
// closing the race that fix itself reopened (NB1, round 2) via
// postProjectsIfKnownNonEmpty's three guards above.
//
// ⚠️ THE RACE: pollSettings runs its first poll (and so onRemote's own
// PostProjects call) essentially immediately, concurrently with THIS
// goroutine — both retrying against the same cold, just-spawned sidecar with
// independent backoff. Before the guards existed, the startup call posted
// resolveProjects(nil) unconditionally, which is an EMPTY list on any
// machine without KELD_PROJECTS_FILE (today, that is every machine — Atlas
// does not serve `projects` yet). If the startup call's older,
// longer-backing-off attempt happened to land AFTER onRemote's had already
// told the sidecar about a real list, the sidecar would end up holding NO
// projects — every subsequent /attribute answers skipped:no_projects, which
// the attributor's switch treats as TERMINAL: publish and delete. Those
// blocks are permanently attributed to nothing, and by the time the next
// poll (5 minutes later) could correct it, the blocks are already gone from
// the store.
func maybePostProjectsAtStartup(post func([]settings.RemoteProject) error, state *projectsState, r projectsResolution) {
	postProjectsIfKnownNonEmpty(post, state, r, "keld-agent: initial /projects post failed: %v")
}

// postProjectsOnChange is onRemote's half of the same gate
// maybePostProjectsAtStartup implements for the startup half: resolve r's
// project list and post it only if postProjectsIfKnownNonEmpty's three
// guards all pass.
//
// ⚠️ ROUND 3 (Finding 1): before this shared gate existed, this half had NO
// empty-list guard of its own — round 2 hardened only the startup path, and
// this poll path posted resolveProjects's result unconditionally, byte-
// identical to the pre-NB1 inline code. That reopened the SAME
// permanent-mis-attribution failure mode through a different door: a
// transient KELD_PROJECTS_FILE read error at POLL time (not just at
// startup) resolved to nil exactly like an honest "nothing declared", and
// nothing stopped that nil from being posted over a sidecar that already
// held a real list. projectsResolution.ok is what closes that specific hole
// (a read error is no longer indistinguishable from a real empty), and the
// shared empty-skip guard closes the rest, exactly as it does for the
// startup half.
func postProjectsOnChange(post func([]settings.RemoteProject) error, state *projectsState, r *settings.Remote) {
	postProjectsIfKnownNonEmpty(post, state, resolveProjects(r), "keld-agent: /projects update failed: %v")
}

// repostProjectsAfterRespawn re-tells a freshly-restarted sidecar the project
// list, unconditionally with respect to `changed`.
//
// ⚠️ C4, THE HALF THE DAEMON OWNS. `attribution._projects` is module state in
// the sidecar PARENT process, and the supervisor restarts that process on
// crash — so a respawned sidecar holds NO projects while the daemon's
// projectsState still records that it was told. Both PostProjects call sites
// route through postProjectsIfKnownNonEmpty, which returns early when
// `!state.changed(next)`, so nothing would ever say it again: every
// /attribute answers `skipped:no_projects`, and (before the attributor's own
// half of this fix) that was TERMINAL — publish and delete. Every block after
// the crash was permanently attributed to nothing until the DAEMON itself
// restarted.
//
// forget() first, then the ordinary gate, rather than a second POST path: the
// gate's other two refusals (trustworthy, non-empty) still apply and stay in
// one place; only the "we already said this" memory is what a respawn
// invalidates, and it is invalidated by clearing it rather than by adding a
// force flag every future caller could pass by mistake.
func repostProjectsAfterRespawn(post func([]settings.RemoteProject) error, state *projectsState) {
	if post == nil {
		return // no /projects channel this run (no sidecar, or a test double)
	}
	list := state.declaredList()
	if len(list) == 0 {
		return // nothing was ever declared: a respawned sidecar holding nothing is correct
	}
	state.forget()
	log.Printf("keld-agent: sidecar restarted — re-posting %d project definition(s) it lost with the process", len(list))
	postProjectsIfKnownNonEmpty(post, state, projectsResolution{list: list, ok: true},
		"keld-agent: /projects re-post after a sidecar restart failed: %v")
}
