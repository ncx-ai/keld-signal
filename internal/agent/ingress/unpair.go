package ingress

import (
	"errors"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ncx-ai/keld-signal/internal/auth"
	"github.com/ncx-ai/keld-signal/internal/paths"
)

// UnpairRoute serves POST /v1/auth/unpair: the Settings page's Unpair button.
// It removes this machine's pairing (hook.json, and the CLI login in
// auth.json) and then restarts Signal, the same way Settings' Restart does.
//
// Why a restart rather than dropping the pairing in place: the daemon is built
// to pair once without a restart (collect always, pair to send) and was never
// built to drop a pairing mid-run and pair again — the wait that adopts a
// pairing has returned by then. Restarting comes up "collecting, not paired",
// exactly a fresh machine's state, from which Sign in works as it always does.
// Nothing collected is lost: every lane holds while unpaired and delivers once
// paired again.
func UnpairRoute(restart func() error) Route { return unpairRoute(signIns, restart) }

func unpairRoute(s *signInStore, restart func() error) Route {
	return Route(func(mux *http.ServeMux, requireSecret func(http.Handler) http.Handler) {
		mux.Handle("POST /v1/auth/unpair", requireSecret(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handleUnpair(w, s, restart)
		})))
	})
}

func handleUnpair(w http.ResponseWriter, s *signInStore, restart func() error) {
	// A pairing named by KELD_CTX_ENDPOINT / KELD_CTX_TOKEN is not the page's
	// to remove: deleting hook.json would change nothing and the restart would
	// come back paired, so say why instead.
	if strings.TrimSpace(os.Getenv("KELD_CTX_ENDPOINT")) != "" || strings.TrimSpace(os.Getenv("KELD_CTX_TOKEN")) != "" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "pairing_set_by_env"})
		return
	}
	// A restart now would strand the browser's return on a dead port, the
	// reason Settings holds its own Restart while a sign-in is in flight.
	if s.pending() {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "signin_in_progress"})
		return
	}
	if !isPaired() {
		writeJSON(w, http.StatusOK, map[string]bool{"unpaired": false, "restarting": false})
		return
	}
	if err := os.Remove(paths.HookConfigPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("keld-agent: unpair could not remove hook.json: %v", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "unpair_failed"})
		return
	}
	if _, err := auth.Clear(); err != nil {
		// hook.json is already gone, so this machine no longer sends; the
		// leftover login only matters to the CLI. Say so, and still restart.
		log.Printf("keld-agent: unpaired, but auth.json could not be removed: %v", err)
	}
	log.Printf("keld-agent: unpaired from the page; restarting to stop sending")
	writeJSON(w, http.StatusOK, map[string]bool{"unpaired": true, "restarting": restart != nil})
	if restart != nil {
		go func() {
			time.Sleep(restartDelay)
			if err := restart(); err != nil {
				log.Printf("keld-agent: unpair-triggered restart failed: %v", err)
			}
		}()
	}
}
