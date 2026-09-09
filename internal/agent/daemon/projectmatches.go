package daemon

import (
	"path/filepath"
	"sort"
	"sync/atomic"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/projects"
	"github.com/ncx-ai/keld-signal/internal/agent/publish"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
	"github.com/ncx-ai/keld-signal/internal/paths"
)

// projectMatchesFor answers, for one block about to be published, which
// projects it lands in — each with its rules.
//
// ⚠️ **THIS IS THE LOCAL SIDE OF A COMPARISON ATLAS CANNOT MAKE ALONE.** Atlas
// knows its own projects' rules and nothing about the ones a person made here,
// so "a machine groups C with your A and B" is unanswerable from what it
// receives today. Putting both sides on the block makes it a set difference
// over rows it already stores: no route, no suggestion object, nothing to
// schedule or retract.
//
// ⚠️ **READ PER BLOCK, NOT CAPTURED ONCE.** The projects document changes while
// the daemon runs — someone makes a project, the settings poll reconciles one
// away — and a snapshot taken at wiring time would stamp every row for the rest
// of the process against a document that no longer exists. The file is small
// and the emitter sweeps every five minutes, so re-reading is not the expensive
// part of anything.
//
// A failure to read is not fatal and not logged per block: the row simply
// carries an empty list, which is the honest statement that this machine could
// not say. A block is still worth publishing without it.
// remoteProjects is the org's current values, installed once the projects store
// exists. A package-level atomic for the reason blockAdvance and devIngestHook
// are: this hook is handed to the emitter before that store is built, so it
// cannot be a parameter.
var remoteProjects atomic.Pointer[func() []settings.RemoteProject]

func setRemoteProjects(fn func() []settings.RemoteProject) {
	if fn == nil {
		remoteProjects.Store(nil)
		return
	}
	remoteProjects.Store(&fn)
}

func projectMatchesFor(b enrich.BlockCharacterisation) []publish.ProjectMatch {
	store := projects.NewStore(filepath.Join(paths.StateDir(), "projects.json"))
	d, err := store.Load()
	if err != nil {
		return nil
	}
	// The org's values as candidates too, so a block entering an ATLAS project
	// says so — Atlas can then see which of its own projects a machine agrees
	// with, not only which local ones it invented.
	candidates := d.Projects
	if fn := remoteProjects.Load(); fn != nil {
		if remote := projects.FromRemoteProjects((*fn)()); len(remote) > 0 {
			candidates = projects.MergeCandidates(candidates, remote)
		}
	}
	matches := projects.MatchesFor(b.Analysis.Workstreams, candidates,
		projects.WorkstreamOffFunc(settings.Load()))
	if len(matches) == 0 {
		return nil
	}
	out := make([]publish.ProjectMatch, 0, len(matches))
	for _, e := range matches {
		out = append(out, publish.ProjectMatch{
			ID: e.ID, Origin: e.Origin, Repos: e.Repos, TicketKey: e.TicketKey,
		})
	}
	// Sorted so the row is a function of WHAT was matched rather than of the
	// order the document happened to hold — two machines with the same
	// projects produce the same row, which is what lets Atlas group them.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Origin != out[j].Origin {
			return out[i].Origin < out[j].Origin
		}
		return out[i].ID < out[j].ID
	})
	return out
}
