package daemon

import "github.com/ncx-ai/keld-signal/internal/agent/enrich"

// windowAnalyzer is the OPTIONAL capability a backend advertises when it can
// characterise the window of work around a prompt (the sidecar's /analyze:
// deterministic, no inference, coordinates in — never text). It is declared as
// an interface rather than a *sidecar.Client assertion so tests can wire a fake
// without a live sidecar, mirroring how enrich treats ContextModel/MultiLabelModel.
type windowAnalyzer interface {
	AnalyzeLabeled(path, promptID string, spanMinutes int) (map[string]enrich.Labeled, bool)
}

// analyzerFor returns m's window-analysis function, or nil when this run has
// none — which is the honest state in two cases: enrichment is running with no
// model backend at all (ml_backend "deterministic", where wireEnrichment never
// starts a sidecar, so there is no /analyze to call either), or the Model is a
// test/eval double. A nil result means the workstreams pass is not wired, so it
// does not run and cannot fail the profile.
//
// The sidecar client's per-job wrappers (withJobCtx, bindMaxLen) return
// *sidecar.Client copies, so the capability survives them and the analysis
// request is bound to the job context like every other sidecar call.
func analyzerFor(m enrich.Model) enrich.WorkstreamAnalyzer {
	a, ok := m.(windowAnalyzer)
	if !ok {
		return nil
	}
	return a.AnalyzeLabeled
}
