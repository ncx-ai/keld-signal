package daemon

import (
	"context"
	"log"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/clientevents"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/features"
	"github.com/ncx-ai/keld-signal/internal/paths"
)

// THE SIGNAL-EMBEDDINGS PATH's daemon wiring. The emitter and its reporter are
// internal/agent/features; this file is only the seams they need — the analysis
// source, the transcript's repository facts, the transport, the two toggles,
// and the watcher's advance signal.
//
// ⚠️ IT IS WIRED UNDER ml_backend "deterministic" ONLY, and that is a scope
// decision from the design spec rather than a limitation of the code. Under
// "auto" the subsystem is ABSENT — never registered, so it appears in neither
// facets_skipped nor extractor_versions, which is this codebase's existing
// distinction between a pass that was SKIPPED and one that was NEVER WIRED
// (WithWorkstreams is the precedent). Lifting the restriction later is a
// registration condition, not a redesign: move featureSourceFor's call from
// deterministicBackend into facetsFor and nothing else changes.
//
// One consequence worth stating: with no GLiNER2 worker resident the ~2.4 GB it
// holds is free, so the parent-reserve contention AGENTS.md documents at length
// does not apply to this path.

// signalFeaturesEndpoint derives the feature ingest URL from the configured
// ingest endpoint by swapping the trailing path segment for
// /v1/signal/features — the same derivation every other route uses, and under
// the /v1/signal/* namespace new client<->Atlas routes go in (see
// docs/signal-client-events.md).
func signalFeaturesEndpoint(ingest string) string {
	if i := strings.Index(ingest, "/v1/"); i >= 0 {
		return ingest[:i] + "/v1/signal/features"
	}
	return strings.TrimRight(ingest, "/") + "/v1/signal/features"
}

// featureSourceCap is the capability behind THE SIGNAL-EMBEDDINGS PATH (the
// sidecar's POST /features): "which feature rows has this transcript produced
// since the cursor". Same shape as windowAnalyzer / windowTickerCap /
// blockDigesterCap — a service route, no inference, works with GLiNER2 absent —
// which is what makes it resolvable from the deterministic backend's service
// client at all.
type featureSourceCap interface {
	FeatureRowsFor(path, source, sessionID string,
		since *float64, now time.Time, maxRows int,
		resolved enrich.ResolvedFacts) ([]enrich.FeatureVector, *float64, bool)
}

// featureSourceFor probes a service client for the feature capability. It is
// deliberately NOT part of facetsFor: facetsFor runs in both ml_backend modes
// that have a service, and this path is scoped to "deterministic" only. Keeping
// the probe here means the scope is one call site rather than a condition
// buried inside a shared helper.
func featureSourceFor(v any) features.Source {
	if s, ok := v.(featureSourceCap); ok {
		return s
	}
	return nil
}

// startFeatureEmitter wires and starts the signal-embeddings path, returning
// the advance observer the watcher's ingest signal feeds (nil when it is off,
// so the signal's call is a cheap nil check).
//
// BOTH TOGGLES DEFAULT OFF and both are read PER SWEEP / PER FLUSH rather than
// captured here: the org's override rides the /v1/enrichment-settings poll,
// whose first response lands after this runs, so binding either at startup
// would ignore the org until the daemon restarted. That is the same reason
// facetsFor resolves PII regions per call. The consequence is that the emitter
// goroutine is started whenever the deterministic backend has a service, and
// simply takes nothing while the gate is closed — a five-minute ticker over an
// empty active set, which is cheaper than a restart-to-adopt.
//
// The client-event on the first enabled sweep is not decoration. Atlas has no
// consumer for a feature row at all — POST /v1/signal/features is not built
// there yet — and an operator who switched this on deserves to see that stated
// once per run rather than discover it by finding rows nothing joins. Emitted
// exempt from the severity floor for the same reason the lifecycle events are:
// it describes what this run WILL do.
func startFeatureEmitter(ctx context.Context, src features.Source, ingestEndpoint string,
	token func() string, actor, installID string,
	enabled, publishing func() bool, emitter *clientevents.Emitter, enc *encoderProvisioner) func(source, path string) {
	if src == nil || token == nil || enabled == nil {
		return nil
	}
	facts := newFactsCache()
	em := features.New(src,
		func(path string) enrich.ResolvedFacts { return facts.forTranscript(path).resolved() },
		enabled, actor, features.StatePath())

	tr := features.NewTransport(signalFeaturesEndpoint(ingestEndpoint), token, paths.FeaturesSpoolDir())
	rep := features.NewReporter(tr, em.Drain, installID, publishing)

	interval := features.IntervalFromEnv()
	flush := featuresFlushFromEnv()
	if enabled() {
		log.Printf("keld-agent: signal-embeddings collection ON (sweeping every %s, flushing every %s). "+
			"Publishing is %s; Atlas has no consumer for a feature row yet.",
			interval, flush, onOff(publishing != nil && publishing()))
		if emitter != nil {
			emitter.EmitExempt("features.emitter_enabled", clientevents.SevWarn, map[string]any{
				"interval_s":    int(interval.Seconds()),
				"flush_s":       int(flush.Seconds()),
				"publishing":    publishing != nil && publishing(),
				"read_at_atlas": false,
			})
		}
	}
	go em.Run(ctx, interval)
	go rep.Run(ctx, flush)

	// THE TEXT ENCODER'S WEIGHTS ARE OWED TO THIS HOOK, among others — see
	// encoder_on_demand.go's package comment for the other caller
	// (attribution) that can also want it, and daemon.go's Run for why the
	// two share ONE provisioner instance rather than each fetching the same
	// ~1.2 GB independently. enc is built there, once, and handed in here
	// rather than constructed by this function: this function's OWN
	// existence condition (svc.Features non-nil, i.e. ml_backend
	// "deterministic" only) must not gate whether the encoder gets built at
	// all — attribution needs it under "auto" too.
	//
	// The emitter's own comment says Advance "is the emitter's ONLY trigger for
	// adding work", which makes it the precise analogue of Worker's warmup call
	// for GLiNER2: the one signal that means something actually wants what the
	// weights produce. So the fetch hangs off it here too. A machine where
	// nothing wants the encoder gets a nil enc and never fetches; a machine
	// whose org has the `features` toggle off (and attribution off) gets one
	// that declines at demand time and can still be switched on later in the
	// same run; a machine where no eligible transcript ever grows never
	// fetches either, which is correct — no message wanted a vector.
	//
	// ⚠️ Wrapping Advance rather than sweeping is deliberate: NOTHING in the
	// sweep, the flush or any sidecar request may wait on this download, and the
	// only way to be sure of that is for the trigger to be fire-and-forget on a
	// path that already forbids blocking. See encoder_on_demand.go.
	if enc != nil {
		return func(source, path string) {
			em.Advance(source, path)
			enc.demand()
		}
	}
	return em.Advance
}

// featuresFlushFromEnv is the reporter's flush cadence, features.DefaultFlush
// unless overridden.
func featuresFlushFromEnv() time.Duration {
	if v := os.Getenv(features.EnvFlush); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return features.DefaultFlush
}

func onOff(b bool) string {
	if b {
		return "ON"
	}
	return "OFF"
}

// featureAdvance is how the watcher's per-file advance signal reaches the
// feature emitter. A package-level atomic rather than another parameter
// threaded through the watcher wiring, mirroring tickObserver and blockAdvance
// exactly: the emitter is optional, off by default, and needs nothing from the
// signal but its two coordinates, so widening the wiring to pass a usually-nil
// hook would cost more than it buys.
//
// It rides ingestSignalHook rather than a third WithIngestSignal, because the
// watcher takes ONE hook — and because all three want the identical scoping. A
// transcript the analysis cannot serve is exactly a transcript no feature row
// can be computed from, so enrich.WorkstreamsEligible gates them together, in
// one place.
var featureAdvance atomic.Pointer[func(source, path string)]

func setFeatureAdvance(fn func(source, path string)) {
	if fn == nil {
		featureAdvance.Store(nil)
		return
	}
	featureAdvance.Store(&fn)
}

// noteFeatureAdvance tells the feature emitter a transcript grew. A no-op when
// the path is off. Must stay cheap and non-blocking: it is called from the
// watcher's poll loop, the path every hook-free prompt on the machine travels,
// and it is — a map write under a short mutex, no I/O, and nothing that waits
// on the sidecar.
func noteFeatureAdvance(source, path string) {
	if fn := featureAdvance.Load(); fn != nil {
		(*fn)(source, path)
	}
}
