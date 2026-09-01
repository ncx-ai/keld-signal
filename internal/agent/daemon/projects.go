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
