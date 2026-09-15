package daemon

import (
	"net/http"
	"path/filepath"
	"runtime"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/clientevents"
	"github.com/ncx-ai/keld-signal/internal/agent/ingress"
	"github.com/ncx-ai/keld-signal/internal/agent/integrations"
	"github.com/ncx-ai/keld-signal/internal/agent/teleproxy"
	"github.com/ncx-ai/keld-signal/internal/paths"
	"github.com/ncx-ai/keld-signal/internal/version"
)

// reportDeps is everything POST /v1/integrations/{id}/report needs, as seams.
//
// Describe is the state rule (integrations.Compute's answer for one row) and is
// allowed to be nil: a daemon built before it is wired still has to be able to
// take a report, because "I pressed the button and nothing happened" is the
// failure this route exists to end. What it costs is a report whose State is
// empty and which says so in its findings — never a guessed state.
type reportDeps struct {
	// ReportsDir defaults to ~/.keld/reports.
	ReportsDir string
	// Log returns the daemon's recent log lines, oldest first.
	Log func() []string
	// Describe answers the row's state, its tool version and doctor's finding
	// IDS for one source.
	Describe func(id string) (state, toolVersion string, findings []string, ok bool)
	// Counters answers the per-source lane counters.
	Counters func(id string) map[string]int
	// Sink publishes the queued event.
	Sink integrations.Sink

	AgentVersion   string
	SidecarVersion string
	InstallID      string
	Now            func() time.Time
}

// defaultReportLogLines is how much of the daemon's log the route reads before
// the bundle's own cap trims it. Twice the cap, because the scrubber DROPS
// credential lines outright: reading exactly 200 would hand a bundle fewer than
// 200 lines on precisely the machine whose log is most interesting.
const defaultReportLogLines = 400

// reportDefaults fills the seams a running daemon can always answer for itself.
func (d reportDeps) withDefaults() reportDeps {
	if d.ReportsDir == "" {
		d.ReportsDir = filepath.Join(paths.KeldHome(), "reports")
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.AgentVersion == "" {
		d.AgentVersion = version.CLI
	}
	if d.Log == nil {
		d.Log = func() []string {
			// stderr first, then stdout: the service managers send the daemon's
			// log to stderr and its rarer stdout writes to the other file, and
			// a bundle that read only one of them would be empty on half the
			// platforms.
			out := clientevents.TailLines(paths.AgentStderrLog(), defaultReportLogLines)
			return append(out, clientevents.TailLines(paths.AgentStdoutLog(), defaultReportLogLines)...)
		}
	}
	if d.Counters == nil {
		d.Counters = defaultReportCounters
	}
	return d
}

// defaultReportCounters is what this daemon can answer with no state rule
// wired: how long ago this tool's telemetry last reached Atlas.
//
// ⚠️ AN AGE, NOT A COUNT, AND IT SAYS SO IN ITS KEY. The per-source record
// (teleproxy.LastForwardForSource) holds an instant; the lane COUNTS belong to
// the state rule's own facts and arrive with it. Publishing an instant under a
// count's name would be the more comfortable lie.
func defaultReportCounters(id string) map[string]int {
	out := map[string]int{}
	if at, ok := teleproxy.LastForwardForSource(id); ok && !at.IsZero() {
		out[id+".otel_age_s"] = int(time.Since(at).Seconds())
	}
	return out
}

// integrationsReportRoute serves POST /v1/integrations/{id}/report (AC-6).
//
// It does two things and reports both: writes the bundle to
// ~/.keld/reports/<ts>-<id>.json, and queues one integration.report event
// carrying the bundle's SUMMARY — never its log lines, which cannot ride a
// client event at all (see Bundle.EventFields).
func integrationsReportRoute(deps reportDeps) ingress.Route {
	d := deps.withDefaults()
	return func(mux *http.ServeMux, auth func(http.Handler) http.Handler) {
		mux.Handle("POST /v1/integrations/{id}/report", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.PathValue("id")
			entry, known := integrations.Get(id)
			if !known {
				// 404 rather than accepting: a report filed against a tool this
				// client has never heard of would arrive at Atlas unjoinable to
				// anything, which is worse than a refusal a person can read.
				writeJSON(w, http.StatusNotFound, map[string]any{
					"error":  "unknown_integration",
					"detail": "no tool with that id is in this client's catalogue",
				})
				return
			}

			state, toolVersion, findings := "", "", []string{}
			if d.Describe != nil {
				if s, v, f, ok := d.Describe(id); ok {
					state, toolVersion, findings = s, v, f
				}
			} else {
				// Stated, not guessed: an empty state with a named reason is
				// readable; an empty state alone reads as a bug in the report.
				findings = append(findings, "state_rule_unavailable")
			}

			b := clientevents.BuildBundle(d.Now(), clientevents.BundleInput{
				Source:         entry.ID,
				State:          state,
				ToolVersion:    toolVersion,
				AgentVersion:   d.AgentVersion,
				SidecarVersion: d.SidecarVersion,
				OS:             runtime.GOOS,
				Arch:           runtime.GOARCH,
				InstallID:      d.InstallID,
				DoctorFindings: findings,
				Counters:       d.Counters(id),
				Log:            d.Log(),
			})

			path, err := clientevents.WriteBundle(d.ReportsDir, b)
			if err != nil {
				// The event still goes: a person who pressed the button gets
				// their problem reported even when this machine cannot write to
				// its own home, which is itself worth knowing.
				integrations.Emit(d.Sink, []integrations.Emission{
					integrations.ReportEmission(entry.ID, b.EventFields()),
				})
				writeJSON(w, http.StatusInternalServerError, map[string]any{
					"error":        "bundle_not_written",
					"event_queued": true,
				})
				return
			}

			integrations.Emit(d.Sink, []integrations.Emission{
				integrations.ReportEmission(entry.ID, b.EventFields()),
			})
			writeJSON(w, http.StatusOK, map[string]any{
				"report_path":  path,
				"event_queued": true,
				"log_kept":     b.LogKept,
				"log_redacted": b.LogRedacted,
				"log_dropped":  b.LogDropped,
			})
		})))
	}
}

// emitterSink adapts the client-events Emitter to integrations.Sink.
//
// ⚠️ The adapter lives HERE, in the daemon, on purpose: internal/agent/
// integrations must not import clientevents (the report bundle lives there and
// needs the integrations ids, so the import would be a cycle), and the daemon
// is the one layer that already knows both.
type emitterSink struct{ e *clientevents.Emitter }

func (s emitterSink) Emit(code, severity string, fields map[string]any) {
	if s.e == nil {
		return
	}
	s.e.Emit(code, clientevents.Severity(severity), fields)
}

func (s emitterSink) EmitExempt(code, severity string, fields map[string]any) {
	if s.e == nil {
		return
	}
	s.e.EmitExempt(code, clientevents.Severity(severity), fields)
}
