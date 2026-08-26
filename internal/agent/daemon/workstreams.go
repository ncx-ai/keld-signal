package daemon

import (
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/blocks"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

// windowAnalyzer is the OPTIONAL capability a backend advertises when it can
// characterise the window of work around a prompt (the sidecar's /analyze:
// deterministic, no inference, coordinates in — never text). It is declared as
// an interface rather than a *sidecar.Client assertion so tests can wire a fake
// without a live sidecar, mirroring how enrich treats ContextModel/MultiLabelModel.
type windowAnalyzer interface {
	AnalyzeLabeled(path, promptID string, spanMinutes int,
		resolved enrich.ResolvedFacts) (enrich.WindowAnalysis, bool)
}

// piiDetector is the same kind of optional capability for the sidecar's /pii:
// presidio patterns, no GLiNER2, off the inference single-flight. The
// sensitivity facet detects with it.
//
// Declared with the REGION-TAKING form. Which country tiers of checksum
// recognizers run is org policy (settings.Settings.PIIRegions), and it rides
// each request rather than the sidecar's startup environment, so a capability
// that could not carry it would silently pin every deployment to the sidecar's
// own default.
type piiDetector interface {
	DetectPIIIn(text string, regions []string) (enrich.PIIResult, bool)
}

// windowTickerCap is the capability behind the tick (the sidecar's /tick): "which
// slices of this transcript has no prompt's look-back reached, and what were
// they". Same shape as the two above — a service route, no inference, works with
// GLiNER2 absent — so it is resolved the same way and travels in serviceFacets.
type windowTickerCap interface {
	TickCharacterised(path, source, sessionID string, promptIDs []string, cursor *float64,
		now time.Time, spanMinutes float64, maxWindows int,
		resolved enrich.ResolvedFacts) ([]enrich.WindowCharacterisation, float64, bool)
}

// blockDigesterCap is the capability behind THE V2 BLOCK PATH (the sidecar's
// POST /blocks): "which CLOSED blocks of work does this transcript have, and
// what was each one". Same shape as the three around it — a service route, no
// inference, works with GLiNER2 absent — so it is resolved the same way and
// travels in serviceFacets.
//
// It is declared here, beside windowTickerCap, and is emphatically not built on
// it: the tick patches the holes a prompt-anchored window leaves, and a block
// path has no holes to patch. The two are wired independently so v2 can be
// promoted, or deleted, without unpicking v1.
type blockDigesterCap interface {
	BlocksCharacterised(path, source, sessionID string, promptIDs []string,
		since *float64, now time.Time, maxBlocks int,
		resolved enrich.ResolvedFacts) ([]enrich.BlockCharacterisation, *float64, bool)
}

// transcriptIngester is the capability behind the watcher's ingest signal (the
// sidecar's /ingest): "this transcript advanced, bring the reference series up to
// date". Coordinates only, no inference, and no answer anyone waits for — see
// sidecar.Client.SignalIngest on why it is one attempt and never a retry.
//
// It belongs with the other two rather than on the Model for the same reason
// they do: it is a service route, it must work with GLiNER2 absent, and it is the
// producer side of the very store /analyze reads.
type transcriptIngester interface {
	SignalIngest(path string, resolved enrich.ResolvedFacts) bool
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
	// SignalIngest is not consumed by a job at all — it is handed to the
	// transcript watcher (see ingestSignalHook), which is what makes /analyze's
	// answer cheap. It travels here because it is the same service, resolved from
	// the same client, in both ml_backend modes that have one.
	SignalIngest func(path string, resolved enrich.ResolvedFacts) bool
	// Tick is not consumed by a job either — it is driven by the daemon's own
	// timer (see tick.go), which is what lets it characterise a burst of
	// autonomous work AFTER the machine has gone quiet. Nil when the service
	// cannot provide it, which switches the ticker off rather than degrading it.
	Tick windowTicker
	// Blocks is THE V2 PATH's producer, and like Tick it is consumed by no job:
	// it is driven by the block emitter's own timer off the watcher's advance
	// signal (see blocks.go). Nil when the service cannot provide it, which
	// switches the emitter off rather than degrading it.
	Blocks blocks.Digester
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
// `regions` is resolved PER CALL, not captured once. wireEnrichment runs at
// startup and the first settings poll lands after it, so binding the region list
// at wiring time would ignore the org until the daemon restarted — the one thing
// the local-then-remote shaping exists to avoid. A nil provider (tests, the eval
// harness) sends no opinion and lets the sidecar apply its own default.
func facetsFor(m enrich.Model, regions func() []string) serviceFacets {
	var f serviceFacets
	if a, ok := m.(windowAnalyzer); ok {
		f.Analyze = a.AnalyzeLabeled
	}
	if p, ok := m.(piiDetector); ok {
		f.ScanPII = func(text string) (enrich.PIIResult, bool) {
			if regions == nil {
				return p.DetectPIIIn(text, nil)
			}
			return p.DetectPIIIn(text, regions())
		}
	}
	if in, ok := m.(transcriptIngester); ok {
		f.SignalIngest = in.SignalIngest
	}
	if t, ok := m.(windowTickerCap); ok {
		f.Tick = t
	}
	if b, ok := m.(blockDigesterCap); ok {
		f.Blocks = b
	}
	return f
}
