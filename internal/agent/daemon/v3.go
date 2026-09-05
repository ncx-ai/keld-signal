package daemon

import (
	"log"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/ingress"
	"github.com/ncx-ai/keld-signal/internal/agent/ledger"
	"github.com/ncx-ai/keld-signal/internal/agent/projects"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
	"github.com/ncx-ai/keld-signal/internal/agent/ui"
	"github.com/ncx-ai/keld-signal/internal/atlas"
	"github.com/ncx-ai/keld-signal/internal/paths"
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
	p := projects.NewStore(filepath.Join(paths.StateDir(), "projects.json"))

	// The projects document needs two things this package owns: the blocks
	// this machine has closed, and the org's vocabulary. Both are injected as
	// functions so internal/agent/projects depends on neither the ledger nor
	// the Atlas connector — it is a pure decision layer and must stay one.
	p.Blocks = ledgerBlocks{l}
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

// observeRemote is called from Run's onRemote on every successful settings
// poll, so the Projects pane reflects the org's current vocabulary without a
// restart — the same live-update property the PII region list already has.
//
// ⚠️ **IT ALSO RUNS STAGE 5 OF THE PROJECT LIFECYCLE, ON THIS SAME CALL.** When
// an admin approves a suggestion into an org project, the definition arrives
// here — and from that instant the org project and whatever local project was
// standing in for it BOTH claim the same repository. Two visible projects
// sharing a repository rule is `ReasonConflict`: the matcher reports every
// matching id and refuses to pick one, so until the local project is folded
// away NEITHER attributes and the work falls out of both.
//
// So the fold is not scheduled, queued or deferred to the next sweep. It
// happens on the same call that installs the definition, and the interval in
// which the two could coexist does not exist.
//
// Folding is restricted to FULL CONTAINMENT (projects.Inherit): it is the one
// step in the lifecycle that changes a decision a person already made without
// asking them, and it is only safe when the org's project covers every rule the
// local one had — because then the two are the same thing.
func (v *v3) observeRemote(r *settings.Remote) {
	if v == nil || r == nil {
		return
	}
	cp := *r
	v.remote.Store(&cp)
	v.inheritFromRemote()
}

// inheritFromRemote folds local projects the org has now defined.
//
// Errors are logged and dropped rather than retried: the next poll is five
// minutes away and carries the same definitions, so a failed write costs one
// interval. What it must never do is leave the document half-folded, and it
// cannot — projects.Store.Update applies the whole transformation under one
// lock or none of it.
func (v *v3) inheritFromRemote() {
	if v.projects == nil || v.projects.RemoteProjects == nil {
		return
	}
	remote := projects.FromRemoteProjects(v.projects.RemoteProjects())
	if len(remote) == 0 {
		return
	}
	var moved []projects.Inherited
	if _, err := v.projects.Update(func(d projects.Document) (projects.Document, error) {
		next, m := projects.Inherit(d, remote, // Read fresh, not captured: the exclusion list is a local setting a
			// person can change between polls.
			projects.WorkstreamOffFunc(settings.Load()))
		moved = m
		return next, nil
	}); err != nil {
		log.Printf("keld-agent: could not fold local projects onto the org's: %v", err)
		return
	}
	for _, m := range moved {
		// Said out loud, once per fold. A person made that local project on
		// purpose; it disappearing without a word is the kind of silent change
		// that makes people distrust the pane.
		log.Printf("keld-agent: %q is now the org's %q — %d rule(s) moved, nothing was unattributed",
			m.LocalTitle, m.OrgTitle, len(m.Repos))
	}
}

// routes are the v3 loopback surfaces, in the order they are mounted. The page
// itself is last so its catch-all "/" cannot shadow a /v1 route.
func (v *v3) routes() []ingress.Route {
	return []ingress.Route{
		ingress.LedgerRoute(v.ledger),
		ingress.ProjectsRoute(v.projects),
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
		dims := map[string]enrich.Labeled{}
		// Only a non-empty dim is offered, and it is offered as `attributed`:
		// the ledger stores a dimension value only when the sidecar had one,
		// so an absent dim here means the block genuinely had none rather than
		// that it was thin. Writing a thin status we do not have would make the
		// projects layer refuse evidence that is real.
		if r.Repo != "" {
			dims[projects.DimRepo] = enrich.Labeled{Value: r.Repo, Status: enrich.WorkstreamAttributed}
		}
		if r.Branch != "" {
			dims[projects.DimBranch] = enrich.Labeled{Value: r.Branch, Status: enrich.WorkstreamAttributed}
		}
		if r.Workspace != "" {
			dims[projects.DimWorkspace] = enrich.Labeled{Value: r.Workspace, Status: enrich.WorkstreamAttributed}
		}
		out = append(out, projects.BlockSummary{
			SessionID: r.Session,
			Start:     r.Start,
			Dims:      dims,
			Minutes:   r.Minutes,
			Tokens:    r.Tokens,
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

// announce logs where the page is, once, at startup. A loopback URL nobody
// knows about is the same as no page at all — and this is the line a colleague
// on Windows will be told to look for.
func announcePage(port int) {
	log.Printf("keld-agent: Keld Signal page at http://127.0.0.1:%d/ — open it with `keld signal open`", port)
}
