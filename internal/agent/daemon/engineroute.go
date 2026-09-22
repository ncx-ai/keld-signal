package daemon

import (
	"net/http"
	"path/filepath"
	"sync"

	"github.com/ncx-ai/keld-signal/internal/agent/ingress"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
	"github.com/ncx-ai/keld-signal/internal/sidecarinstall"
	"github.com/ncx-ai/keld-signal/internal/version"
)

// The analysis engine's install, driven from the page.
//
// ⚠️ IT USED TO RUN INSIDE THE macOS INSTALLER, AND THAT IS WHERE IT WEDGED.
// The wizard pane spawned `keld signal install-sidecar` and held Continue until
// the fetch settled — a 315 MB download in front of somebody who had not
// finished installing yet, with no way to skip it, on a release host that
// answers 504 often enough to matter (measured 2026-09-21: three of four full
// pulls, and each attempt carries a 30-minute client timeout). Worse, the pane
// then had to render progress from an XPC-hosted view, and the layout pass that
// drove wedged the plugin's main thread with the download already finished and
// staged on disk.
//
// The page is the right place for all of it: the daemon already knows whether
// an engine is present, which version it is and whether this machine even needs
// one, nothing is blocked while it downloads, and a failure is a line of text
// beside a button rather than a stuck installer.
//
// engineState is what GET /v1/engine answers. Every field is a fact the daemon
// can establish on its own — no probe of the running service, so an engine that
// is present but not yet up never reads as absent.
type engineState struct {
	// Needed is false only when this machine will never load one: `ml_backend`
	// "off" disables enrichment outright. Under "auto" and "deterministic" the
	// analysis service is what cuts blocks, so it is needed in both.
	Needed bool `json:"needed"`
	// Installed and Version describe what is on disk. Version is "" when a tree
	// exists with no VERSION file, which predates the stamp and is therefore
	// stale by definition — the same reading onboard.command makes.
	Installed bool   `json:"installed"`
	Version   string `json:"version,omitempty"`
	// Expected is this binary's own version, so the page can say "out of date"
	// rather than just "present". "dev" on either side means CANNOT TELL, and
	// Outdated stays false — the refusal version.Skew already makes.
	Expected string `json:"expected,omitempty"`
	Outdated bool   `json:"outdated"`
	// Status is the install's own state: idle, running, done, failed.
	Status   string `json:"status"`
	Received int64  `json:"received,omitempty"`
	Total    int64  `json:"total,omitempty"`
	Error    string `json:"error,omitempty"`
}

// engineManager owns the one install this daemon will run at a time.
type engineManager struct {
	mu       sync.Mutex
	status   string
	received int64
	total    int64
	errMsg   string
	// autoFor is the INSTALLED VERSION the automatic path last acted on ("" for
	// an absent engine). It is the loop guard, and it is keyed on the mismatch
	// rather than on "once per run".
	//
	// ⚠️ "ONCE PER RUN" LEFT A MACHINE STUCK IN A STATE IT COULD NOT LEAVE.
	// Measured 2026-09-22: the daemon updated the engine to rc.6 correctly at
	// 16:30, a bare `keld signal install-sidecar` put v3.0.4 over the top at
	// 16:37, and nothing re-fixed it — the automatic attempt was spent, and the
	// page had no button because it reports rather than asks. A red "out of
	// date" with no way to act, until somebody restarted the daemon.
	//
	// Keyed on the version, both properties hold: a FAILED fetch leaves the same
	// installed version, so it does not retry and a flaky host cannot become a
	// download loop; a mismatch that is genuinely NEW — a downgrade, a hand
	// install, a tree replaced underneath us — is acted on, because it is not
	// the one already handled.
	autoFor string
	// autoDone says autoFor is meaningful. Without it an absent engine ("")
	// would read as "already handled" before anything ran.
	autoDone bool
	// install is the seam tests replace. Nil means the real one.
	install func(sidecarinstall.Opts) (sidecarinstall.Result, error)
	// restart is the seam tests replace; nil means the sidecar-child restart.
	restart func() error
	// locate and mode are the two facts about this machine, as seams for the
	// same reason: a test must not depend on what is installed where it runs.
	locate func() (string, bool)
	mode   func() string
}

func newEngineManager() *engineManager {
	return &engineManager{status: "idle"}
}

func (m *engineManager) installer() func(sidecarinstall.Opts) (sidecarinstall.Result, error) {
	if m.install != nil {
		return m.install
	}
	return sidecarinstall.Install
}

func (m *engineManager) locator() func() (string, bool) {
	if m.locate != nil {
		return m.locate
	}
	return sidecarBinPath
}

// restarter is how this daemon picks up a newly installed engine: it restarts
// the sidecar CHILD it supervises, never the service it is itself running as.
// See the note at the call site.
func (m *engineManager) restarter() func() error {
	if m.restart != nil {
		return m.restart
	}
	return func() error { return currentServiceHealth.Load().RestartSidecar() }
}

func (m *engineManager) backend() string {
	if m.mode != nil {
		return m.mode()
	}
	return settings.Load().MLBackend
}

// state reads the machine and the in-flight install together, under one lock,
// so the page can never see a "done" with the old version beside it.
func (m *engineManager) state() engineState {
	m.mu.Lock()
	st := engineState{Status: m.status, Received: m.received, Total: m.total, Error: m.errMsg}
	m.mu.Unlock()

	st.Needed = m.backend() != "off"
	if p, ok := m.locator()(); ok {
		st.Installed = true
		st.Version = sidecarinstall.ReadVersion(sidecarTreeOf(p))
	}
	st.Expected = version.CLI
	if st.Installed {
		if skewed, known := version.Skew(version.CLI, st.Version); known && skewed {
			st.Outdated = true
		}
	}
	return st
}

// start begins an install unless one is already running. It returns false when
// one is, so the route can answer 409 rather than queueing a second 315 MB
// fetch behind the first.
func (m *engineManager) start() bool {
	m.mu.Lock()
	if m.status == "running" {
		m.mu.Unlock()
		return false
	}
	m.status, m.received, m.total, m.errMsg = "running", 0, 0, ""
	m.mu.Unlock()

	go func() {
		// Throttled to whole percent, the same as the CLI's NDJSON path: the
		// page polls, so a finer signal than it can render is pure lock churn.
		progress := sidecarinstall.NewProgressThrottle(func(received, total int64) {
			m.mu.Lock()
			m.received, m.total = received, total
			m.mu.Unlock()
		})
		_, err := m.installer()(sidecarinstall.Opts{
			Progress: progress,
			// ⚠️ THE SIDECAR, NOT THE SERVICE. sidecarinstall's default restarts
			// the whole local service after a swap, which is right for the CLI
			// — a short-lived process installing for somebody else — and fatal
			// here, because this process IS that service. Measured on a real
			// machine 2026-09-22: the engine landed correctly, the daemon then
			// bounced itself mid-commit, did not come back, and the page sat
			// polling a dead port frozen on "Updating… 100%". The daemon
			// supervises the sidecar child directly and only ever needed that
			// restarted, which is the same call the page's own Restart button
			// makes.
			Restart: m.restarter(),
		})
		m.mu.Lock()
		if err != nil {
			m.status, m.errMsg = "failed", err.Error()
		} else {
			m.status, m.errMsg = "done", ""
		}
		m.mu.Unlock()
	}()
	return true
}

// autoStart installs or updates the engine WITHOUT being asked, once per daemon
// run.
//
// ⚠️ THERE IS NO DECISION HERE TO GIVE SOMEBODY. The daemon knows which version
// it needs — its own — and an engine that does not match it is not a preference,
// it is the version-skew failure this project has already paid for twice (a
// 2.3.0 daemon against an Aug-11 sidecar: /blocks 404s, zero blocks published,
// doctor reporting no problems for three weeks). A button asking permission to
// fix that is friction in front of a question with one answer, and every hour
// it goes unclicked is an hour of work that cuts no blocks.
//
// Once per daemon run, not on a timer: a fetch that failed will fail the same
// way in thirty seconds, and a page that retried on its own schedule would turn
// a flaky release host into a download loop. The state stays `failed` with its
// reason, the page offers a Try again, and the next daemon start tries once
// more. That is the same shape KELD_ENRICH_MAX_ATTEMPTS and the update loop's
// failed_versions already take: bounded, stated, and recoverable by hand.
func (m *engineManager) autoStart() {
	st := m.state()
	if !st.Needed {
		return
	}
	// Already correct: nothing to do, and nothing to remember.
	if st.Installed && !st.Outdated {
		return
	}
	m.mu.Lock()
	if m.autoDone && m.autoFor == st.Version {
		// The same mismatch we already acted on — a fetch that failed will fail
		// the same way now. See autoFor.
		m.mu.Unlock()
		return
	}
	m.autoFor, m.autoDone = st.Version, true
	m.mu.Unlock()
	m.start()
}

// engineRoute serves the page's two calls. GET is safe to poll; POST starts one
// install and answers immediately, because a request that waited out a 315 MB
// download is the wizard's mistake in a different process.
func engineRoute(m *engineManager) ingress.Route {
	if m == nil {
		m = newEngineManager()
	}
	return func(mux *http.ServeMux, auth func(http.Handler) http.Handler) {
		mux.Handle("/v1/engine", auth(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.Method != http.MethodGet {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			// ⚠️ THE READ RE-ARMS THE FIX. Without this the only automatic
			// attempt happened at daemon start, so anything that broke the
			// engine afterwards stayed broken until a restart — which is
			// exactly what happened on 2026-09-22 (see autoFor). autoStart is
			// guarded by the mismatch it already handled, so polling cannot
			// turn into a download loop; it simply means a machine never sits
			// in a state nothing is re-checking.
			go m.autoStart()
			writeJSON(w, http.StatusOK, m.state())
		})))
		mux.Handle("/v1/engine/install", auth(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			// Refusing when it is not needed keeps the button from being the
			// one way to put an engine on a machine that has said it wants
			// none — `ml_backend: "off"` is a choice, not an oversight.
			if st := m.state(); !st.Needed {
				writeJSON(w, http.StatusConflict, map[string]any{"error": "not_needed"})
				return
			}
			if !m.start() {
				writeJSON(w, http.StatusConflict, map[string]any{"error": "already_running"})
				return
			}
			writeJSON(w, http.StatusAccepted, m.state())
		})))
	}
}

// sidecarTreeOf maps the sidecar BINARY back to the directory its VERSION file
// sits in. resolveSidecar accepts two layouts — flat (the binary directly in a
// directory) and one-dir (dir/keld-agent-sidecar/keld-agent-sidecar) — and
// build-freeze.sh writes VERSION at the tree ROOT in both, so the answer is
// simply the binary's own directory. Named rather than inlined because reading
// the version off the wrong directory is the failure that makes every
// comparison silently say "no version" (see the stamp note in AGENTS.md).
func sidecarTreeOf(bin string) string { return filepath.Dir(bin) }

// currentEngineManager is the one manager this process uses, so the page's GET
// and POST see the same install. A package-level value rather than a field for
// the same reason integrationLanes is one: the route is constructed in a list
// that has no daemon state in scope, and a second manager would report "idle"
// beside a running download.
var engineManagerOnce sync.Once
var engineManagerShared *engineManager

func currentEngineManager() *engineManager {
	engineManagerOnce.Do(func() { engineManagerShared = newEngineManager() })
	return engineManagerShared
}
