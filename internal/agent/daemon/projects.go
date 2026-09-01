package daemon

import (
	"encoding/json"
	"log"
	"os"

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
