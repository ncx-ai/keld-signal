package daemon

import (
	"context"
	"log"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/ingress"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
	"github.com/ncx-ai/keld-signal/internal/atlas"
)

// ⚠️ **AN UNCONFIGURED DAEMON SERVED NOTHING, WHICH MADE PAIRING FROM THE APP
// IMPOSSIBLE.**
//
// `Run` waits on `awaitConfig` until `hook.json` carries an endpoint and a
// token, and every listener in this process was created AFTER that wait. So a
// freshly installed machine had no port, no `agent.json` and no page: the app
// read the missing discovery file and rendered its "not running" screen, which
// was CORRECT and useless — the daemon was running perfectly, idling exactly as
// designed. There was nowhere to paste a setup code, so `POST /v1/config`,
// which exists precisely to onboard a machine, could only ever be reached by a
// machine that had already been onboarded.
//
// That is also why `keld-agent install` had to take `--code`: configuring
// during install was not a convenience, it was the only door.
//
// The fix is NOT a second server on a second port. The port must be STABLE
// across onboarding, because the app resolves `agent.json` once and the page's
// own origin is that port — pair on one port and continue on another and the
// window can never come back. So the listener and `agent.json` are created
// FIRST, before the wait, and the handler behind them is swapped in place the
// moment configuration arrives. Nothing rebinds, and no restart is required for
// the page to keep working.
//
// What the pre-config handler serves is the subset that needs neither Atlas nor
// a token: the page, the ledger and projects reads (both local files), settings
// and `/v1/config` itself. `/enrich` is deliberately NOT among them — a pointer
// accepted before the daemon can publish is work with nowhere to go, and the
// hook already spools durably when the daemon is unreachable, so dropping it
// here costs nothing and inventing a queue to hold it would.

// swapHandler is one http.Handler whose delegate can be replaced while the
// server is running. Reads are lock-free because they happen on every request;
// writes happen exactly twice in a daemon's life.
type swapHandler struct{ h atomic.Pointer[http.Handler] }

func newSwapHandler(initial http.Handler) *swapHandler {
	s := &swapHandler{}
	s.Store(initial)
	return s
}

// Store replaces what the listener serves, for every request that begins after
// it returns. A request already in flight finishes on the old handler, which is
// correct: it was admitted under that handler's rules.
func (s *swapHandler) Store(h http.Handler) { s.h.Store(&h) }

func (s *swapHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h := s.h.Load()
	if h == nil || *h == nil {
		http.Error(w, "starting", http.StatusServiceUnavailable)
		return
	}
	(*h).ServeHTTP(w, r)
}

// loopbackServer is the daemon's single HTTP surface: one listener, one
// http.Server, and a handler that changes once.
//
// It exists because the server can no longer be created at the END of Run — it
// has to be listening while Run is still blocked in awaitConfig — but the
// things its shutdown must close (the job queue, the client-event emitter) are
// built much later. So stop-time work is registered through onStop rather than
// captured at construction.
type loopbackServer struct {
	ln     net.Listener
	srv    *http.Server
	swap   *swapHandler
	onStop atomic.Pointer[func()]
}

// newLoopbackServer binds nothing; it wraps a listener the caller has already
// bound and recorded in agent.json.
//
// The timeouts are the ones serve() carried, for the reason it documented:
// KELD_AGENT_BIND can make this listener reachable off loopback, and connection
// acceptance happens before ingress.go's constant-time secret check, so an
// unauthenticated caller can otherwise hold a goroutine open indefinitely.
func newLoopbackServer(ln net.Listener, initial http.Handler) *loopbackServer {
	sw := newSwapHandler(initial)
	return &loopbackServer{
		ln:   ln,
		swap: sw,
		srv: &http.Server{
			Handler:           sw,
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       30 * time.Second,
			IdleTimeout:       60 * time.Second,
		},
	}
}

// Serve starts accepting immediately and returns. Shutdown is driven by ctx.
func (s *loopbackServer) Serve(ctx context.Context) {
	go func() {
		if err := s.srv.Serve(s.ln); err != nil && err != http.ErrServerClosed {
			log.Printf("keld-agent: loopback server stopped: %v", err)
		}
	}()
	go func() {
		<-ctx.Done()
		if fn := s.onStop.Load(); fn != nil {
			(*fn)()
		}
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = s.srv.Shutdown(shutCtx)
	}()
}

// OnStop registers what to run when ctx is cancelled, before the server drains.
// Called once, by Run, after the queue and emitter exist.
func (s *loopbackServer) OnStop(fn func()) { s.onStop.Store(&fn) }

// Install swaps in the configured daemon's real handler.
func (s *loopbackServer) Install(h http.Handler) { s.swap.Store(h) }

// onboardingHandler is what the loopback listener serves while the daemon has
// no configuration.
//
// ⚠️ **It is built with `atlas.Off`, and that is not a stand-in for the real
// connector — there genuinely is no Atlas yet.** The endpoint and the token are
// the things being waited for. `newV3` already treats a disabled connector as
// "no org vocabulary", so the Projects pane says so out loud rather than
// showing an empty list that would read as "your org has declared nothing".
//
// The restart function is the real one. `POST /v1/config` answers
// `restart_required: true` and the page's restart bar resends through
// `PUT /v1/settings?restart=1`. A restart is not strictly necessary — the
// daemon's own awaitConfig poll picks the file up within KELD_CONFIG_POLL — but
// it is the fastest path and the one the page already promises: "Signal
// restarts and points there."
func onboardingHandler(set settings.Settings, secret string) http.Handler {
	sig := newV3(set, atlas.Off{})
	routes := append(sig.routes(),
		ingress.SettingsRoute(serviceRestarter{}.Restart),
		ingress.ConfigRoute(),
		// Generating work needs no Atlas and no token, and an unpaired machine
		// is exactly where someone wants to see a block appear before deciding
		// to pair at all. No drive hook: before configuration there is no block
		// emitter and no sidecar client, so the route says the block was not cut
		// rather than implying it was — and the transcript is on disk, so the
		// configured daemon cuts it on its first sweep.
		ingress.DevGenerateRoute(nil),
	)
	// DiscardHandler rather than Handler: there is no queue to offer to yet.
	return ingress.DiscardHandler(secret, routes...)
}

// awaitConfigNote is the one line an idling daemon logs. The old message named
// two CLI commands as the only remedy; pasting a setup code into the app is now
// the other, and on a machine installed without `--code` it is the expected
// one.
func awaitConfigNote(hookPath string) func() {
	return func() {
		log.Printf("keld-agent: not configured yet — the app is reachable and can pair from its "+
			"Settings pane, or run `keld login` + `keld signal setup`; waiting for %s "+
			"(no restart needed once it exists)", hookPath)
	}
}
