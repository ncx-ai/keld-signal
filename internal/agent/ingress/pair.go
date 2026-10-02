package ingress

import (
	"errors"
	"net/http"
	"strings"
	"sync/atomic"

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
// already use — auth.ParsePairingCode, auth.LoginWithCode (via pair(), which the
// browser sign-in's GET /auth/callback shares), and the
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
	a, err := pair(base, code, "")
	if err != nil {
		var pe *pairError
		errors.As(err, &pe)
		switch pe.stage {
		case pairLogin:
			writeError(w, http.StatusBadRequest, "login_failed")
		case pairOnboarding:
			writeError(w, http.StatusBadGateway, "onboarding_failed")
		default:
			writeError(w, http.StatusInternalServerError, "hook_write_failed")
		}
		return
	}

	writeJSON(w, http.StatusOK, configResponse{Host: a.APIURL, RestartRequired: true})
}

// pairStage names which step of pair() failed, so each caller can say so in
// its own vocabulary (a JSON error code for /v1/config, a fixed page for the
// browser sign-in's return).
type pairStage int

const (
	pairLogin      pairStage = iota // Atlas refused or never answered the redeem
	pairOnboarding                  // the redeem worked; fetching the ingest token did not
	pairHookWrite                   // hook.json could not be written
)

// String is the stage's name in a log line.
func (s pairStage) String() string {
	switch s {
	case pairLogin:
		return "login"
	case pairOnboarding:
		return "onboarding"
	default:
		return "hook_write"
	}
}

type pairError struct {
	stage pairStage
	err   error
	// secrets are what this pairing handled — the code, the verifier, the
	// tokens Atlas handed back — for redacted() to scrub.
	secrets []string
}

func (e *pairError) Error() string { return e.err.Error() }
func (e *pairError) Unwrap() error { return e.err }

// redacted is the error as ONE log line with every secret pair() handled, and
// any the caller adds, replaced. An Atlas error body is quoted into the error
// (api.checkStatus keeps 200 bytes of it), and Atlas may echo what it was sent.
func (e *pairError) redacted(more ...string) string {
	msg := strings.ReplaceAll(e.err.Error(), "\n", "; ")
	for _, sec := range append(append([]string(nil), e.secrets...), more...) {
		if sec != "" {
			msg = strings.ReplaceAll(msg, sec, "[redacted]")
		}
	}
	return msg
}

// pair is THE ONE WAY this daemon pairs itself with an Atlas: redeem the code
// at base (with the PKCE verifier when the code came from a browser sign-in),
// fetch the onboarding hand-off, and write auth.json + hook.json. Both
// POST /v1/config and GET /auth/callback call it, so there is exactly one
// definition of what a pairing writes. Every error is a *pairError.
func pair(base, code, verifier string) (*auth.AuthData, error) {
	a, err := auth.LoginWithCodeVerifier(api.NewClient(base, ""), code, verifier)
	if err != nil {
		return nil, &pairError{stage: pairLogin, err: err, secrets: []string{code, verifier}}
	}
	ob, err := api.NewClient(a.APIURL, a.AccessToken).Onboarding()
	if err != nil {
		return nil, &pairError{stage: pairOnboarding, err: err, secrets: []string{code, verifier, a.AccessToken}}
	}
	if err := config.SaveHookConfig(ob.Endpoint, ob.IngestToken); err != nil {
		return nil, &pairError{stage: pairHookWrite, err: err, secrets: []string{code, verifier, a.AccessToken, ob.IngestToken}}
	}
	if f := onPaired.Load(); f != nil {
		(*f)()
	}
	return a, nil
}

// onPaired is told each time pair() has written hook.json, so the daemon's
// pairing watcher adopts it now rather than on its next poll — the page reads
// the health strip right after "Signed in", and a watcher still asleep left it
// saying "Atlas not paired". It must not block: pair() runs inside a request.
var onPaired atomic.Pointer[func()]

// SetOnPaired installs the pairing announcement (nil removes it).
func SetOnPaired(f func()) {
	if f == nil {
		onPaired.Store(nil)
		return
	}
	onPaired.Store(&f)
}
