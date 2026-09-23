package daemon

import (
	"encoding/json"
	"net/http"

	"github.com/ncx-ai/keld-signal/internal/agent/ingress"
	"github.com/ncx-ai/keld-signal/internal/agent/integrations"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
	"github.com/ncx-ai/keld-signal/internal/config"
	"github.com/ncx-ai/keld-signal/internal/tools"
)

// integrationsSnapshot is the seam the route reads states through. A function
// value rather than a direct call so the httptest suite drives the route with
// a fixed Response — the route's job is the HTTP shape, and the rule is
// exercised where it lives, in Compute.
type integrationsSnapshot func() integrations.Response

// integrationsApply is the seam the setup route writes through.
type integrationsApply func(id string) (integrations.SetupResult, error)

// IntegrationsRoute registers the two integrations surfaces on the daemon's
// loopback mux, behind the same secret as /v1/settings.
//
//	GET  /v1/integrations             → integrations.Response   (AC-1)
//	POST /v1/integrations/{id}/setup  → integrations.SetupResult (AC-2, server half)
//
// ⚠️ NEITHER MAY TRIGGER A MODEL LOAD OR A DOWNLOAD. The pane polls the first
// every 10 s; a route that warmed anything would turn an open browser tab into
// a standing cost. Everything both routes read is a filesystem stat, a config
// file, a transcript head and one JSON state file.
func IntegrationsRoute(snapshot integrationsSnapshot, apply integrationsApply) ingress.Route {
	if snapshot == nil {
		snapshot = liveIntegrationsSnapshot
	}
	if apply == nil {
		apply = liveIntegrationsApply
	}
	return ingress.Route(func(mux *http.ServeMux, auth func(http.Handler) http.Handler) {
		mux.Handle("GET /v1/integrations", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			writeIntegrationsJSON(w, http.StatusOK, snapshot())
		})))
		mux.Handle("POST /v1/integrations/{id}/setup", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.PathValue("id")
			if _, ok := integrations.Get(id); !ok {
				// An id outside the catalogue is a 404, never a 400: the
				// catalogue is the set of ids that exist, and "unknown tool" is
				// a fact about the URL, not about the request body.
				writeIntegrationsJSON(w, http.StatusNotFound, map[string]string{"error": "unknown_integration"})
				return
			}
			res, err := apply(id)
			if err != nil {
				writeIntegrationsJSON(w, http.StatusConflict, map[string]string{"error": "setup_failed"})
				return
			}
			writeIntegrationsJSON(w, http.StatusOK, res)
		})))
	})
}

func writeIntegrationsJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// liveIntegrationsSnapshot reads the machine. The lane record is the daemon's
// own, so the route reports the instants the worker has been writing rather
// than re-reading the file per request.
func liveIntegrationsSnapshot() integrations.Response {
	set := settings.Load()
	return integrations.Snapshot(
		integrations.Deps{Lanes: currentIntegrationLanes()},
		integrations.Options{AutoSetup: set.AutoSetupEnabled(), ToolOTLP: set.ToolOTLPEnabled()},
	)
}

// liveIntegrationsApply runs the tool's adapter through integrations.ApplyEntry
// — the SAME call the detector's automatic poll makes, so a machine configured
// by a click and one configured by a poll are configured identically.
func liveIntegrationsApply(id string) (integrations.SetupResult, error) {
	e, ok := integrations.Get(id)
	if !ok {
		return integrations.SetupResult{}, errUnknownIntegration
	}
	m, err := config.LoadManifest()
	if err != nil {
		return integrations.SetupResult{}, err
	}
	return integrations.ApplyEntry(e, tools.Get, integrationsSetupParams, m)
}

var errUnknownIntegration = errUnknown("unknown_integration")

type errUnknown string

func (e errUnknown) Error() string { return string(e) }
