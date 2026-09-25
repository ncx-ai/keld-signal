package daemon

import (
	"log"
	"sync/atomic"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/ingress"
	"github.com/ncx-ai/keld-signal/internal/agent/ledger"
	"github.com/ncx-ai/keld-signal/internal/agent/projects"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
	"github.com/ncx-ai/keld-signal/internal/agent/ui"
	"github.com/ncx-ai/keld-signal/internal/atlas"
)

// v3 is everything the Keld Signal page needs, assembled once at startup: the
// delivery ledger, the projects document, and the loopback routes that serve
// them (docs/v3/contracts.md).
//
// ⚠️ **Every part of this is optional at runtime and none of it may break
// collection.** A ledger that cannot be opened, a projects file that cannot be
// parsed, a page that fails to embed — each degrades to a page that says so,
// never to a daemon that stops cutting blocks or publishing them. That is why
// nothing here returns an error to Run: the collector is the product, and the
// window onto it is not allowed to take it down.
type v3 struct {
	ledger   *ledger.Store
	projects *projects.Store
	// remote is the last org settings seen on the poll. An atomic pointer
	// rather than a mutex because it is written from the single poll goroutine
	// and read from every HTTP request; and a POINTER rather than a value so
	// "never polled" stays distinct from "polled and empty" — the projects
	// store treats the first as unknown and the second as an org that has
	// declared nothing, and those are different answers on the page.
	remote atomic.Pointer[settings.Remote]
	// atlasOn is fixed at startup, like the setting it reflects. The ledger
	// needs it to record sent/received as NOT APPLICABLE rather than as a
	// success on a machine that deliberately publishes nothing.
	atlasOn bool
	// atlas is the connector itself, held so the health strip can read
	// LastResponse() when Atlas is on — "reachable" and "never tried" are
	// different facts, and only the connector that actually made the calls
	// knows which one is true. nil-checked at every read (Off still answers
	// LastResponse(), but a future caller with a bare zero value must not
	// panic).
	atlas atlas.Client
}

func newV3(set settings.Settings, cl atlas.Client) *v3 {
	l := ledger.New()
	p := projects.NewStore(projects.DefaultPath())

	// The projects document needs two things this package owns: the blocks
	// this machine has closed, and the org's vocabulary. Both are injected as
	// functions so internal/agent/projects depends on neither the ledger nor
	// the Atlas connector — it is a pure decision layer and must stay one.
	p.Blocks = ledgerBlocks{l}
	hideProjectsInOffGroups(p, set)
	v := &v3{ledger: l, projects: p, atlasOn: cl.Enabled(), atlas: cl}
	p.RemoteProjects = func() []settings.RemoteProject {
		r := v.remote.Load()
		if r == nil || r.Projects == nil {
			return nil
		}
		return *r.Projects
	}
	if !cl.Enabled() {
		// With Atlas off there is no org vocabulary at all, and saying so is
		// better than an empty list that reads as "your org has declared
		// nothing". The projects store already treats nil as unknown.
		p.RemoteProjects = nil
	}
	return v
}

// hideProjectsInOffGroups turns every project whose group a person switched
// off into a HIDDEN project, once, on daemon start (R4-AC-4).
//
// ⚠️ **GROUPS LEFT SIGNAL IN REVISION 4 (2026-09-25), AND THEIR SWITCH WITH
// THEM — BUT WHAT IT EXCLUDED MUST STAY EXCLUDED.** "Counts for my work"
// switched off meant a group's projects attributed nothing. Dropping the
// switch and letting them count again would change numbers a person chose to
// exclude, silently. So each such project is marked hidden instead: it keeps
// attributing nothing, exactly as before, the page shows it hidden and the
// person can unhide it — per project now — and 3.0.6 honours hidden too.
//
// A project is in an off group when `workstreams_off` names its stored group's // vocab:keep
// KEY or that group's NAME, or its Team — the last because the old predicate
// (projectGroupOff) checked Team as well, for an Atlas value whose bucket rode
// there, and "exactly as before" means the same projects.
//
// Three properties, each deliberate:
//   - `workstreams_off` is LEFT in agent-config.json: a machine rolled back to // vocab:keep
//     3.0.6 still sees those groups off. This reads it and never writes it.
//   - IDEMPOTENT, and a no-op writes nothing: the document is only saved when
//     at least one visible project needs hiding, so a second start (or a
//     machine that never switched a group off) never rewrites projects.json.
//   - It cannot take the daemon down: an unreadable document is logged and
//     left alone — the page reports it as unreadable, as it would anyway.
func hideProjectsInOffGroups(s *projects.Store, set settings.Settings) {
	if s == nil || len(set.GroupsOff) == 0 {
		return
	}
	inOffGroup := func(d projects.Document, p projects.Project) bool {
		if p.Hidden {
			return false
		}
		if p.Group != "" && set.GroupOff(p.Group) {
			return true
		}
		for _, g := range d.Groups {
			if g.Key == p.Group && g.Name != "" && set.GroupOff(g.Name) {
				return true
			}
		}
		return p.Team != "" && set.GroupOff(p.Team)
	}
	d, err := s.Load()
	if err != nil {
		log.Printf("keld-agent: could not read projects to hide switched-off groups' projects: %v", err)
		return
	}
	need := false
	for _, p := range d.Projects {
		if inOffGroup(d, p) {
			need = true
			break
		}
	}
	if !need {
		return
	}
	hidden := 0
	if _, err := s.Update(func(d projects.Document) (projects.Document, error) {
		next := d
		next.Projects = append([]projects.Project(nil), d.Projects...)
		for i, p := range next.Projects {
			if inOffGroup(d, p) {
				next.Projects[i].Hidden = true
				hidden++
			}
		}
		return next, nil
	}); err != nil {
		log.Printf("keld-agent: could not hide switched-off groups' projects: %v", err)
		return
	}
	log.Printf("keld-agent: %d project(s) in a switched-off group are now hidden "+
		"(groups left Signal; unhide one on the Projects page — workstreams_off is kept for a rollback)", hidden) // vocab:keep
}

// observeRemote is called from Run's onRemote on every successful settings
// poll and stores what the org sent, so the org's list is HELD — current, live,
// no restart needed — behind Store.RemoteProjects and v.remote. What still
// reads it belongs to the semantic pass, which stays on the Atlas list by
// decision Q1 of the Revision 2 discovery (resolveProjects; since Revision 4
// nothing labels its outcome with a group). The future "use Atlas workstreams
// again" work is the other intended reader. Nothing on the rule pass, the page or
// project_matches reads it.
//
// ⚠️ **IT NO LONGER RECONCILES, AND UNTIL REVISION 2 (2026-09-25) IT DID.** On
// this same call a poll used to run projects.Reconcile, which deleted a local
// project every one of whose rules an Atlas workstream covered and trimmed the
// covered rules off the rest — sound while the Atlas list was a candidate set,
// because two projects claiming one repo was then a conflict and Atlas's
// rules took precedence. Signal now attributes only to projects defined in
// Signal, so the Atlas list is neither matched nor shown, and a reconcile kept
// running here would SILENTLY DELETE PEOPLE'S OWN RULES in favour of
// projects they can no longer see: the block would fall out of every
// project on the page with nothing said. So a poll writes nothing to the
// local document, ever. projects.Reconcile itself is kept for the separate
// Atlas-import work; nothing calls it.
func (v *v3) observeRemote(r *settings.Remote) {
	if v == nil || r == nil {
		return
	}
	cp := *r
	v.remote.Store(&cp)
}

// routes are the v3 loopback surfaces, in the order they are mounted. The page
// itself is last so its catch-all "/" cannot shadow a /v1 route.
func (v *v3) routes() []ingress.Route {
	return []ingress.Route{
		// ledgerRoute rather than ingress.LedgerRoute: the page's payload gains
		// a `service` block beside `health`, and internal/agent/ledger has no
		// seam for a key it does not own. See servicehealth_route.go.
		// v.ledgerReader(), not v.ledger: the block rows' `attributed` cell is
		// recomputed live from the current rules, so the Today rows and the
		// Projects pane cannot answer differently about the same block (see
		// liveAttribution in v3blocks.go). Nothing stored is rewritten.
		ledgerRoute(v.ledgerReader(), func() serviceWire { return currentServiceHealth.Load().Snapshot() }),
		// The restart control the page offers. It reads the health owner live,
		// so an unconfigured machine (the onboarding handler mounts these too)
		// answers 409 not_applicable rather than pretending to restart nothing.
		serviceRestartRoute(func() error { return currentServiceHealth.Load().RestartSidecar() }, nil),
		ingress.ProjectsRoute(v.projects),
		// The Integrations pane's two routes. nil seams ⇒ the live readers:
		// integrations.Snapshot off disk, and integrations.ApplyEntry through
		// the same adapters and the same write path `keld signal setup` uses.
		IntegrationsRoute(nil, nil),
		// ⚠️ MOUNTED SINCE 2026-09-15, having been written, tested and left out
		// of this list. The handler, the bundle writer, the redaction gate and
		// both planted-string tests were all green while nothing could reach
		// it: the pane POSTed here and the wire contract documented it as live.
		// `routes_mounted_test.go` now fails on any documented path that 404s,
		// because a route nobody mounts is a route that does not exist.
		integrationsReportRoute(reportDeps{
			Describe: describeIntegrationForReport,
			Sink:     currentIntegrationSink(),
		}),
		ui.Route(),
	}
}

// ledgerBlocks adapts the ledger's block feed to what the projects store needs.
//
// ⚠️ **The window is the last seven days, not "everything".** Suggestions are
// about work you might still place; a repository you touched once in March is
// noise on that pane, and the coverage figure the page leads with is a weekly
// one. A wider window would also make the pane slower the longer Signal has
// been installed, which is the wrong direction for a page people open daily.
type ledgerBlocks struct{ l *ledger.Store }

func (b ledgerBlocks) SinceWeekStart() ([]projects.BlockSummary, error) {
	recs, err := b.l.BlocksSince(time.Now().AddDate(0, 0, -7), 5000)
	if err != nil {
		return nil, err
	}
	out := make([]projects.BlockSummary, 0, len(recs))
	for _, r := range recs {
		out = append(out, projects.BlockSummary{
			SessionID: r.Session,
			Start:     r.Start,
			// dimsOfRecord, not a second copy of the same conversion: the
			// Today rows' live pass reads the same rows through it, and two
			// copies would be a way for the two surfaces to disagree again.
			Dims:    dimsOfRecord(r),
			Minutes: r.Minutes,
			Tokens:  r.Tokens,
			USD:     r.EstimateUSD,
		})
	}
	return out, nil
}

// noteHealth records the machine-level facts the page's health strip shows.
// Called at startup and whenever one of them changes; each is a fact the daemon
// already knew and previously kept to itself.
func (v *v3) noteHealth(key ledger.HealthKey, status ledger.Status, detail string) {
	if v == nil || v.ledger == nil {
		return
	}
	v.ledger.SetHealth(ledger.Health{Key: key, Status: status, Detail: detail, At: time.Now().UTC()})
}
