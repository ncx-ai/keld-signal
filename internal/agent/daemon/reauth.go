package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/clientevents"
	"github.com/ncx-ai/keld-signal/internal/agent/creds"
	"github.com/ncx-ai/keld-signal/internal/api"
	"github.com/ncx-ai/keld-signal/internal/auth"
	"github.com/ncx-ai/keld-signal/internal/config"
	"github.com/ncx-ai/keld-signal/internal/paths"
	"github.com/ncx-ai/keld-signal/internal/retry"
)

// defaultReauthCooldown is the minimum time between refresh attempts, so a
// burst of 401s (e.g. publish, settings-poll, and client-events all hitting a
// just-rotated token around the same time) triggers one re-onboard, not a
// storm of Onboarding calls.
const defaultReauthCooldown = 60 * time.Second

// reauther re-fetches the org ingest token using the still-valid CLI token
// and live-swaps it into tok — so publish/settings/the client-events reporter
// (which all read the token through tok.Get) observe the new value with no
// daemon restart. If the CLI token itself is gone or revoked, it records a
// terminal "re-authentication required" state instead of retrying forever.
//
// Every dependency is an injectable func field so refresh is fully unit
// testable with no network or filesystem access beyond a temp KELD_HOME
// (for the marker file). newReauther wires production defaults; tests
// override fields directly.
type reauther struct {
	loadAuth func() (*auth.AuthData, error)
	onboard  func(apiURL, cliToken string) (*api.Onboarding, error)
	save     func(endpoint, token string) error
	now      func() time.Time
	cooldown time.Duration

	tok     *creds.Token
	emitter *clientevents.Emitter

	mu             sync.Mutex
	lastAttempt    time.Time
	inFlight       bool
	reauthRequired atomic.Bool
}

// newReauther builds a reauther with production seams: auth.Load, a real
// Onboarding call via api.NewClient, config.SaveHookConfig, time.Now, and the
// KELD_REAUTH_COOLDOWN-tunable default cooldown.
func newReauther(tok *creds.Token, emitter *clientevents.Emitter) *reauther {
	return &reauther{
		loadAuth: auth.Load,
		onboard: func(apiURL, cliToken string) (*api.Onboarding, error) {
			return api.NewClient(apiURL, cliToken).Onboarding()
		},
		save:     config.SaveHookConfig,
		now:      time.Now,
		cooldown: reauthCooldown(),
		tok:      tok,
		emitter:  emitter,
	}
}

// reauthCooldown resolves the default cooldown from KELD_REAUTH_COOLDOWN: a
// Go duration string (e.g. "30s") or a bare integer read as seconds. Falls
// back to defaultReauthCooldown when unset or unparsable.
func reauthCooldown() time.Duration {
	if v := os.Getenv("KELD_REAUTH_COOLDOWN"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
		if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
			return time.Duration(secs) * time.Second
		}
	}
	return defaultReauthCooldown
}

// refresh re-fetches the ingest token using the stored CLI credential and, on
// success, live-swaps it into tok. Single-flight + cooldown: a call that
// arrives while a refresh is already running, or within cooldown of the last
// attempt (successful or not), is a silent no-op (returns nil) — the next
// trigger (a later 401, or the next poll tick) retries once the window
// passes.
//
// Terminal outcomes (no stored CLI credential, or Onboarding 401/403 — the
// CLI token itself is gone/revoked) mark the daemon "re-authentication
// required" (see markTerminal) and return an error. A transient/network
// error from Onboarding also returns an error but is NOT terminal — the
// caller keeps using the last-known ingest token and the next call after
// cooldown tries again.
func (r *reauther) refresh(ctx context.Context) error {
	r.mu.Lock()
	if r.inFlight || r.now().Sub(r.lastAttempt) < r.cooldown {
		r.mu.Unlock()
		return nil
	}
	r.inFlight = true
	r.lastAttempt = r.now()
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.inFlight = false
		r.mu.Unlock()
	}()

	a, err := r.loadAuth()
	if err != nil {
		r.markTerminal("no stored credentials")
		return fmt.Errorf("reauth: load CLI credentials: %w", err)
	}
	if a == nil || a.AccessToken == "" {
		r.markTerminal("no stored credentials")
		return errors.New("reauth: no stored CLI credentials")
	}

	ob, err := r.onboard(a.APIURL, a.AccessToken)
	if err != nil {
		var se *retry.StatusError
		if errors.As(err, &se) && (se.Code == http.StatusUnauthorized || se.Code == http.StatusForbidden) {
			r.markTerminal("CLI token rejected")
			return fmt.Errorf("reauth: CLI token rejected: %w", err)
		}
		// Transient/network failure: NOT terminal — return as-is so the caller
		// keeps the last-known token and a later trigger retries after cooldown.
		return fmt.Errorf("reauth: onboarding fetch failed: %w", err)
	}

	if err := r.save(ob.Endpoint, ob.IngestToken); err != nil {
		return fmt.Errorf("reauth: save hook config: %w", err)
	}
	r.tok.Set(ob.IngestToken)
	r.clearTerminal()
	if r.emitter != nil {
		r.emitter.Emit("auth.refreshed", clientevents.SevInfo, map[string]any{})
	}
	log.Printf("keld-agent: ingest token refreshed")
	// Endpoint rotation is out of scope (v1: token-only refresh — see spec
	// §3): the three consumers' base URLs are fixed at daemon startup, so a
	// changed endpoint here can't be adopted without a restart. We don't have
	// the startup endpoint threaded into reauther to compare against, so this
	// is a deliberate no-op rather than a half-correct comparison.
	return nil
}

// markTerminal records a terminal "re-authentication required" state: sets
// the in-daemon flag and writes the local marker file (paths.ReauthMarkerPath)
// — the only visibility channel left once Atlas can no longer be reached
// with the stored credentials. Writing the marker is best-effort: a failure
// is logged but never changes the flag (the daemon still stops hammering).
func (r *reauther) markTerminal(reason string) {
	r.reauthRequired.Store(true)
	msg := fmt.Sprintf("re-authentication required (%s) — run 'keld login' then 'keld-agent restart'\n%s",
		reason, r.now().UTC().Format(time.RFC3339))
	if err := os.WriteFile(paths.ReauthMarkerPath(), []byte(msg+"\n"), 0o600); err != nil {
		log.Printf("keld-agent: failed to write re-auth marker: %v", err)
	}
	log.Printf("keld-agent: %s", msg)
}

// clearTerminal removes the marker file and, only once it's confirmed gone,
// clears the in-process terminal flag. Fail safe toward still-required
// (mirroring markTerminal's caution): if os.Remove fails for a reason other
// than the marker already being absent, the flag is left set so
// TerminalRequired() doesn't report false while a stale marker remains on
// disk.
func (r *reauther) clearTerminal() {
	if err := os.Remove(paths.ReauthMarkerPath()); err != nil && !os.IsNotExist(err) {
		log.Printf("keld-agent: failed to remove re-auth marker: %v", err)
		return
	}
	r.reauthRequired.Store(false)
}

// TerminalRequired reports whether the daemon is currently in the terminal
// re-authentication-required state. This is an in-process convenience for
// tests/callers that already hold the reauther; the status CLI (a later
// task) reads the marker file directly since it runs in a different process.
func (r *reauther) TerminalRequired() bool { return r.reauthRequired.Load() }
