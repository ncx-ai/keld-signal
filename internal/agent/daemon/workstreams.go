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

// analyzerFor returns the window-analysis function of the service client it is
// handed, or nil when that client cannot analyse — a test/eval double, or the
// nil Model left behind when no analysis service could be started this run
// (no sidecar binary, or its port could not be allocated). A nil result means
// the workstreams pass is not wired, so it does not run and cannot fail the
// profile.
//
// It is deliberately NOT "the Model's analyzer": ml_backend "deterministic"
// runs the analysis service with no Model at all, and derives its analyzer
// from the service client here just as "auto" derives it from the sidecar
// Model. That is why wireEnrichment returns the analyzer as its own value and
// threads it to process, rather than letting process rederive it from the
// Model (which would be nil, and would silently drop every workstream).
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
