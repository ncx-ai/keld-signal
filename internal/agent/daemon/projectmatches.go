package daemon

import (
	"sort"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/projects"
	"github.com/ncx-ai/keld-signal/internal/agent/publish"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
)

// projectMatchesFor answers, for one block about to be published, which
// projects it lands in — each with its rules.
//
// ⚠️ **THIS IS THE LOCAL SIDE OF A COMPARISON ATLAS CANNOT MAKE ALONE.** Atlas
// knows its own projects' rules and nothing about the ones a person made here,
// so "a machine groups C with your A and B" is unanswerable from what it
// receives today. Putting this machine's side on the block makes it a set
// difference over rows Atlas already stores, against its own side: no route, no
// suggestion object, nothing to schedule or retract.
//
// ⚠️ **READ PER BLOCK, NOT CAPTURED ONCE.** The projects document changes while
// the daemon runs — someone makes, edits or maps one on the page — and a snapshot taken at wiring time would stamp every row for the rest
// of the process against a document that no longer exists. The file is small
// and the emitter sweeps every five minutes, so re-reading is not the expensive
// part of anything.
//
// A failure to read is not fatal and not logged per block: the row simply
// carries an empty list, which is the honest statement that this machine could
// not say. A block is still worth publishing without it.
//
// ⚠️ **SIGNAL PROJECTS ONLY, SINCE REVISION 2 (2026-09-25).** The org's
// Atlas workstreams used to be merged in as candidates here, through a
// package-level atomic installed by newV3, so a block entering an Atlas
// project said so. It no longer does: the row names only what the person
// defined in Signal, with its rules — which is still exactly the comparison
// above, because Atlas holds its own side and can take the difference itself.
// An overlay made with "Same as" before this revision is a local entry, so it
// is a candidate, and it keeps its Atlas id on the wire (MatchesFor emits an id
// only for an entry whose origin is atlas); every other Signal id is
// title-derived and never sent. projects.Candidates is the one candidate
// rule, shared with the recorded pass and the page.
func projectMatchesFor(b enrich.BlockCharacterisation) []publish.ProjectMatch {
	store := projects.NewStore(projects.DefaultPath())
	d, err := store.Load()
	if err != nil {
		return nil
	}
	matches := projects.MatchesFor(b.Analysis.Dimensions, projects.Candidates(d),
		projects.GroupOffFunc(settings.Load()))
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
