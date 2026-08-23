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

// piiDetector is the same kind of optional capability for the sidecar's /pii:
// presidio patterns plus a spaCy NER pass, no GLiNER2, off the inference
// single-flight. The sensitivity facet detects with it.
type piiDetector interface {
	DetectPII(text string) (enrich.PIIResult, bool)
}

// serviceFacets are the enrichment capabilities that belong to the analysis
// SERVICE rather than to the Model. Both are non-inference routes on the same
// sidecar, both must work with GLiNER2 absent entirely, and both are therefore
// wired in ml_backend "deterministic" as well as "auto" — where the Model is
// nil and rederiving them from it would silently drop them.
//
// They travel as one value so the next non-model route does not add another
// parameter to Worker, process and wireEnrichment. A zero value is the honest
// "this run has no analysis service": the workstreams pass then never
// registers, and sensitivity reports itself degraded (see
// enrich.WithPIIScanner).
type serviceFacets struct {
	Analyze enrich.WorkstreamAnalyzer
	ScanPII enrich.PIIScanner
}

// facetsFor returns the service facets of the client it is handed, leaving any
// the client cannot provide nil — a test/eval double, or the nil Model left
// behind when no analysis service could be started this run (no sidecar binary,
// or its port could not be allocated).
//
// It is deliberately NOT "the Model's facets": ml_backend "deterministic" runs
// the analysis service with no Model at all, and derives these from the service
// client here just as "auto" derives them from the sidecar Model. That is why
// wireEnrichment returns them as their own value and threads them to process,
// rather than letting process rederive them from the Model (which would be nil,
// and would silently drop every workstream and every PII finding).
//
// The sidecar client's per-job wrappers (withJobCtx, bindMaxLen) return
// *sidecar.Client copies, so the capabilities survive them and the requests are
// bound to the job context like every other sidecar call.
func facetsFor(m enrich.Model) serviceFacets {
	var f serviceFacets
	if a, ok := m.(windowAnalyzer); ok {
		f.Analyze = a.AnalyzeLabeled
	}
	if p, ok := m.(piiDetector); ok {
		f.ScanPII = p.DetectPII
	}
	return f
}
