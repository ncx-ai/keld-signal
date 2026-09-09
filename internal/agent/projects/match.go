package projects

import (
	"sort"
	"strings"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

// Match is one project a block matched, listed WITH THE RULES that matched it.
//
// ⚠️ **IT WAS CALLED `Entered` UNTIL 2026-09-09, AND THE WIRE KEY WAS
// `entered`.** The name came from the set model this is built on: a project is
// a set of rules and a block enters it by matching any member. That is exactly
// right about the mechanism and useless to a reader, who meets a field called
// "entered" on a block row and cannot tell what entered what, nor that the
// rules travel with it. Renamed while it was free: the key had shipped only in
// a pre-release and nothing in Atlas read it yet, because the set-difference
// work there is deliberately not built.
//
// ⚠️ **THE RULES ARE THE POINT, NOT THE ID.** Atlas already knows its own
// projects' rules; what it cannot see is the local ones. Putting both sides on
// the same row lets Atlas compute the set difference — "a machine groups C with
// your A and B" — from one block, with no join against a definition that may
// have changed since and no second object to keep in step.
//
// ⚠️ **A TITLE IS NEVER HERE.** A rule is a repository remote or a ticket key,
// and both already travel as block dimensions, so nothing new crosses. A local
// project's typed title stays on the machine — the decision taken when this was
// still going to be a suggestion feed, and unchanged by dropping the feed.
// `ID` is omitted for a local project for the same reason: it is derived from
// the title (`newProjectID`), so sending it would send the title in a thin
// disguise.
type Match struct {
	// ID is the Atlas value id, and EMPTY for a local project — see above.
	ID string `json:"id,omitempty"`
	// Origin is "atlas" or "user", so a reader can tell whose project this is
	// without inferring it from the presence of an id.
	Origin string `json:"origin"`
	// Repos and TicketKey are the rules. Sorted, so two machines that added the
	// same rules in different orders produce the same row and Atlas can group
	// them without normalising first.
	Repos     []string `json:"repos,omitempty"`
	TicketKey string   `json:"ticket_key,omitempty"`
}

// MatchesFor is every project these block dimensions land in, each with its
// rules.
//
// ⚠️ **IT IS "MATCHES", NOT "ATTRIBUTED", AND THE PLURAL IS THE POINT.**
// Attribute picks ONE project and refuses when two match; this lists
// everything the block matched, because a block that matched two projects is
// the case an admin most needs to see. Reconcile should have made that
// impossible for a local/org pair, and if it ever reappears this is what
// carries the evidence rather than swallowing it.
func MatchesFor(dims map[string]enrich.Labeled, candidates []Project,
	workstreamOff func(key string) bool) []Match {
	repo, hasRepo := attributedValue(dims, DimRepo)
	ticket, hasTicket := "", false
	if branch, ok := attributedValue(dims, DimBranch); ok {
		ticket, hasTicket = ticketKeyIn(branch)
	}
	if !hasRepo && !hasTicket {
		return nil
	}
	var out []Match
	for _, p := range candidates {
		if p.Hidden || projectWorkstreamOff(p, workstreamOff) {
			continue
		}
		matched := false
		if hasRepo {
			for _, r := range EffectiveRepos(p) {
				if strings.EqualFold(strings.TrimSpace(r), strings.TrimSpace(repo)) {
					matched = true
					break
				}
			}
		}
		if !matched && hasTicket && p.TicketKey != "" &&
			strings.EqualFold(strings.TrimSpace(p.TicketKey), strings.TrimSpace(ticket)) {
			matched = true
		}
		if !matched {
			continue
		}
		e := Match{Origin: p.Origin, TicketKey: p.TicketKey}
		if p.Origin == OriginAtlas {
			e.ID = p.ID
		}
		e.Repos = append([]string(nil), DeclaredRepos(p)...)
		sort.Strings(e.Repos)
		out = append(out, e)
	}
	return out
}
