package ingress

import (
	"net/http"

	"github.com/ncx-ai/keld-signal/internal/agent/settings"
	"github.com/ncx-ai/keld-signal/internal/api"
	"github.com/ncx-ai/keld-signal/internal/auth"
	"github.com/ncx-ai/keld-signal/internal/config"
	"github.com/ncx-ai/keld-signal/internal/paths"
)

// ConfigRoute registers POST /v1/config (docs/v3/contracts.md): redeem a
// one-time setup code from the loopback page and pair this machine with the
// Atlas host that minted it.
//
// ⚠️ IT CALLS THE SAME FUNCTIONS `keld login --code` and `keld signal setup`
// already use — auth.ParsePairingCode, auth.LoginWithCode, and the
// Onboarding + config.SaveHookConfig pair that writes hook.json — never a
// reimplementation of that flow. It deliberately does NOT run the tool-adapter
// half of `keld signal setup` (detecting and rewriting Claude Code/Codex/
// Gemini config files): that is a decision about which AI tools this MACHINE
// runs, unrelated to which Atlas org this machine reports to, and re-running
// it from a page action would silently touch files the page never asked
// about. What this route writes is exactly auth.json (via LoginWithCode) and
// hook.json (via SaveHookConfig) — the two files the contract names.
//
// ⚠️ Refused with 409 while Send to Atlas is off: pairing writes the ingest
// credential Atlas publishing uses, and a machine that has deliberately asked
// for local-only should not silently gain one from a page action. Malformed
// input is refused with 400 BEFORE that check even runs, and before anything
// is written — auth.ParsePairingCode holds no file handle and touches no
// path, so a structurally bad code costs nothing to reject early.
func ConfigRoute() Route {
	return Route(func(mux *http.ServeMux, auth func(http.Handler) http.Handler) {
		mux.Handle("POST /v1/config", auth(http.HandlerFunc(handleConfig)))
	})
}

type configRequest struct {
	Code string `json:"code"`
}

type configResponse struct {
	Host            string `json:"host"`
	RestartRequired bool   `json:"restart_required"`
}

func handleConfig(w http.ResponseWriter, r *http.Request) {
	var body configRequest
	if !decodeJSONBody(w, r, &body) {
		return
	}

	host, code, err := auth.ParsePairingCode(body.Code)
	if err != nil {
		writeError(w, http.StatusBadRequest, "malformed_code")
		return
	}

	if !settings.Load().AtlasEnabled() {
		writeError(w, http.StatusConflict, "send_to_atlas_is_off")
		return
	}

	base := host
	if base == "" {
		base = paths.APIBase()
	}
	a, err := auth.LoginWithCode(api.NewClient(base, ""), code)
	if err != nil {
		writeError(w, http.StatusBadRequest, "login_failed")
		return
	}

	ob, err := api.NewClient(a.APIURL, a.AccessToken).Onboarding()
	if err != nil {
		writeError(w, http.StatusBadGateway, "onboarding_failed")
		return
	}
	if err := config.SaveHookConfig(ob.Endpoint, ob.IngestToken); err != nil {
		writeError(w, http.StatusInternalServerError, "hook_write_failed")
		return
	}

	writeJSON(w, http.StatusOK, configResponse{Host: a.APIURL, RestartRequired: true})
}
