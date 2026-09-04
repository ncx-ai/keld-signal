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
}

func newV3(set settings.Settings, cl atlas.Client) *v3 {
	l := ledger.New()
	p := projects.NewStore(filepath.Join(paths.StateDir(), "projects.json"))

	// The projects document needs two things this package owns: the blocks
	// this machine has closed, and the org's vocabulary. Both are injected as
	// functions so internal/agent/projects depends on neither the ledger nor
	// the Atlas connector — it is a pure decision layer and must stay one.
	p.Blocks = ledgerBlocks{l}
	v := &v3{ledger: l, projects: p}
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
