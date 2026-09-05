package ingress

import (
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/attrib"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
)

// restartDelay gives the response time to reach the client's TCP buffer
// before the process that would otherwise restart it dies. It is a courtesy,
// not a guarantee — the same shape `daemon`'s update path already accepts
// (Quiesce is a bounded wait, not a condition for updating).
const restartDelay = 150 * time.Millisecond

// SettingsRoute registers GET|PUT /v1/settings (docs/v3/contracts.md).
//
// restart, when non-nil, is how the daemon restarts its OWN OS service — the
// SAME mechanism auto-update already uses (internal/agent/update.Updater's
// Restarter, which wraps service.Restart(): launchctl bootout+bootstrap /
// systemctl --user restart / schtasks /End+/Run). It is invoked ONLY when the
// caller passes ?restart=1 AND the write actually requires one, and only
// AFTER the response has been written — a restart kills this process, so
// answering first is what lets the caller ever see {"restart_required":true}
// rather than a dropped connection. A nil restart (every test here, or a
// build with no resolvable service) just skips that step; the response
// already told the caller a restart is needed, which is the documented
// fallback ("exit(0) after responding is acceptable because the service
// supervisor restarts it") for a build where wiring the real Restarter is not
// worth doing — except here we DO wire the real one in daemon.go, because
// service.Restart already exists, already does exactly this, and reusing it
// costs one line at the call site.
func SettingsRoute(restart func() error) Route {
	return Route(func(mux *http.ServeMux, auth func(http.Handler) http.Handler) {
		mux.Handle("GET /v1/settings", auth(http.HandlerFunc(handleGetSettings)))
		mux.Handle("PUT /v1/settings", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handlePutSettings(w, r, restart)
		})))
	})
}

// settingsView is GET /v1/settings' body: the EFFECTIVE values (file merged
// with env), plus which keys an env var currently pins so the page can grey
// them out rather than let a person "change" a setting that cannot move.
type settingsView struct {
	SendToAtlas    bool     `json:"send_to_atlas"`
	DevBlocks      string   `json:"dev_blocks"`
	ShowBreaks     bool     `json:"show_breaks"`
	WorkstreamsOff []string `json:"workstreams_off"`
	Attribution    bool     `json:"attribution"`
	Readonly       []string `json:"readonly"`
	DevGenerate    bool     `json:"dev_generate"`
	DevRepos       []string `json:"dev_repos"`
}

func handleGetSettings(w http.ResponseWriter, r *http.Request) {
	set := settings.Load()
	devBlocks, _ := set.DevBlocksMode() // GET reports the effective value, not a refusal
	off := set.WorkstreamsOff
	if off == nil {
		off = []string{}
	}
	devRepos := set.DevRepos
	if devRepos == nil {
		devRepos = []string{}
	}
	writeJSON(w, http.StatusOK, settingsView{
		DevGenerate:    set.DevGenerate,
		DevRepos:       devRepos,
		SendToAtlas:    set.AtlasEnabled(),
		DevBlocks:      devBlocks,
		ShowBreaks:     set.ShowBreaks,
		WorkstreamsOff: off,
		Attribution:    attrib.Enabled(set.Attribution),
		Readonly:       readonlySettingsKeys(),
	})
}

// readonlySettingsKeys names every v3 key whose value is currently PINNED by
// an environment variable — so a write to the file would have no observable
// effect. Each check mirrors the exact recognized-value vocabulary the real
// resolver uses (Settings.AtlasEnabled, Settings.DevBlocksMode, attrib.Enabled)
// rather than "is the env var set at all", because an env var set to
// something neither resolver recognizes falls through to the file and is
// therefore NOT actually pinning anything.
func readonlySettingsKeys() []string {
	var out []string
	if atlasEnvPins() {
		out = append(out, "send_to_atlas")
	}
	if devBlocksEnvPins() {
		out = append(out, "dev_blocks")
	}
	if attributionEnvPins() {
		out = append(out, "attribution")
	}
	return out
}

// atlasEnvPins mirrors Settings.AtlasEnabled's switch exactly (case-sensitive,
// no "yes"/"no"): only these six literal values ever override the file.
func atlasEnvPins() bool {
	switch strings.TrimSpace(os.Getenv(settings.AtlasEnv)) {
	case "0", "false", "off", "1", "true", "on":
		return true
	}
	return false
}

// devBlocksEnvPins mirrors Settings.DevBlocksMode: ANY non-empty
// KELD_DEV_BLOCKS wins over the file, even an unrecognised one (which then
// resolves to "" rather than falling back to the file's value) — so presence
// alone is what pins it.
func devBlocksEnvPins() bool {
	return strings.TrimSpace(os.Getenv(settings.DevBlocksEnv)) != ""
}

// attributionEnvPins mirrors attrib.Enabled's switch exactly (lower-cased,
// with "yes"/"no" as well as the usual six).
func attributionEnvPins() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(attrib.EnvEnabled))) {
	case "1", "true", "on", "yes", "0", "false", "off", "no":
		return true
	}
	return false
}

// settingsPatch is PUT /v1/settings' body: any subset of the four v3-owned
// keys plus attribution (docs/v3/contracts.md). Pointer fields so an absent
// key is distinguishable from an explicit zero value — decodeJSONBody feeds
// this struct directly, so a key the JSON omits leaves its pointer nil.
type settingsPatch struct {
	SendToAtlas    *bool     `json:"send_to_atlas"`
	DevBlocks      *string   `json:"dev_blocks"`
	ShowBreaks     *bool     `json:"show_breaks"`
	WorkstreamsOff *[]string `json:"workstreams_off"`
	Attribution    *bool     `json:"attribution"`
	DevGenerate    *bool     `json:"dev_generate"`
	DevRepos       *[]string `json:"dev_repos"`
}

func handlePutSettings(w http.ResponseWriter, r *http.Request, restart func() error) {
	var patch settingsPatch
	if !decodeJSONBody(w, r, &patch) {
		return
	}
	if patch.DevBlocks != nil && !validDevBlocksValue(*patch.DevBlocks) {
		writeError(w, http.StatusBadRequest, "invalid_dev_blocks")
		return
	}

	// The refusal is evaluated on the MERGED effective view (this patch atop
	// the file), because a PUT that turns Atlas on cannot be judged safe by
	// looking at the file alone if the same PUT is also the one asking for a
	// dev granularity. Only evaluated when the patch actually touches one of
	// the two relevant keys — an unrelated PUT (e.g. show_breaks alone) must
	// not be blocked by a pre-existing state it did not create and was not
	// asked to change.
	if patch.SendToAtlas != nil || patch.DevBlocks != nil {
		merged := settings.Load()
		if patch.SendToAtlas != nil {
			merged.SendToAtlas = patch.SendToAtlas
		}
		if patch.DevBlocks != nil {
			merged.DevBlocks = *patch.DevBlocks
		}
		if _, refused := merged.DevBlocksMode(); refused {
			writeError(w, http.StatusConflict, "turn_off_send_to_atlas_first")
			return
		}
	}

	err := settings.WriteV3Settings(settings.V3Patch{
		SendToAtlas:    patch.SendToAtlas,
		DevBlocks:      patch.DevBlocks,
		ShowBreaks:     patch.ShowBreaks,
		WorkstreamsOff: patch.WorkstreamsOff,
		Attribution:    patch.Attribution,
		DevGenerate:    patch.DevGenerate,
		DevRepos:       patch.DevRepos,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "settings_write_failed")
		return
	}

	// docs/v3/contracts.md: send_to_atlas, dev_blocks and attribution require
	// a restart.
	//
	// ⚠️ **`attribution` WAS MISSING HERE AND THE TOGGLE WAS A SILENT NO-OP.**
	// The contract originally enumerated two keys, `attribution` was added to
	// the patch afterwards, and this line kept reporting the original two on
	// the reasoning that widening it was "a contract change, not a bug fix".
	// It is both, and the contract was the half that was wrong: the daemon
	// reads the gate exactly once, at `daemon.go`'s `startAttributor` call,
	// and never again. So turning Attribution on in the page wrote the file,
	// answered `restart_required: false`, raised no restart bar, and changed
	// nothing at all until the daemon happened to restart for another reason
	// — a control that reports success and does nothing, which is the failure
	// this codebase refuses at every other layer. A key that is read at
	// startup belongs in this list; the test to write when adding the next one
	// is "does anything re-read it while the daemon runs".
	// dev_generate and dev_repos are deliberately absent: handleDevGenerate
	// calls settings.Load() per request, so both take effect immediately. The
	// rule is "a key nothing re-reads while the daemon runs", not "a key in the
	// developer box" — adding them by symmetry would make the page demand a
	// restart it does not need.
	restartRequired := patch.SendToAtlas != nil || patch.DevBlocks != nil || patch.Attribution != nil
	if restartRequired && restart != nil && r.URL.Query().Get("restart") == "1" {
		go func() {
			time.Sleep(restartDelay)
			if err := restart(); err != nil {
				log.Printf("keld-agent: settings-triggered restart failed: %v", err)
			}
		}()
	}
	writeJSON(w, http.StatusOK, map[string]any{"restart_required": restartRequired})
}

func validDevBlocksValue(v string) bool {
	for _, k := range settings.DevBlocksModes {
		if k == v {
			return true
		}
	}
	return false
}
