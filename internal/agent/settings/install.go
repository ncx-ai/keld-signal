package settings

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/ncx-ai/keld-signal/internal/paths"
)

// KnownBackends is the closed set ml_backend accepts. Validated at the WRITE
// rather than at the read because Load() must never reject an operator's file —
// but an installer writing a typo would hand every machine it touched a value
// MLEnabled() silently reads as "auto", and ml_backend has no remote override to
// fix it with.
var KnownBackends = []string{"auto", "deterministic", "off"}

// WriteInstallDefaults writes the three settings a v2 install lands on —
// ml_backend, blocks and attribution — into ~/.keld/agent-config.json.
//
// ⚠️ IT MERGES. The file belongs to the operator, not to this struct: it may
// hold pii_regions, include_entity_text, feature toggles, or a key no version of
// Settings models at all, and an installer run must leave every one of them
// intact. So the existing file is decoded into map[string]json.RawMessage — NOT
// into Settings, which would drop unmodelled keys on the way back out and
// re-serialise every modelled one at its zero value.
//
// ⚠️ IT MUST BE FOLLOWED BY A DAEMON RESTART, which is the caller's job and
// happens to already be true: ml_backend is read at startup and never re-read,
// and runInstall ends with service.Install(), which restarts on all three OSes
// (launchctl bootout+bootstrap, systemctl restart, schtasks /End+/Run). The
// macOS pkg is the proof that this matters — its postinstall kickstarts the
// agent BEFORE opening onboard.command, so a daemon is already running on the
// old settings by the time this is called.
//
// An absent or unparseable file starts from empty rather than failing: that
// mirrors Load(), which keeps zero-value defaults on invalid JSON, and an
// install must not be abortable by a corrupt config.
// ⚠️ ATTRIBUTION IS DERIVED FROM blocks, NOT WRITTEN AS A LITERAL, and never
// given a parameter of its own. The attribution loop is driven off the block
// emitter's OnPublished hook (see daemon/workstreams.go), so `attribution` with
// `blocks` off is a key that can never do anything — a config an operator would
// read as "on" while nothing was ever attributed. There is no blocks=false,
// attribution=true install, so the invariant is expressed here rather than left
// to a caller to honour.
//
// ⚠️ IT IS WRITTEN AT ALL because KELD_ATTRIBUTION is unreachable from an
// installed service — the same reason `blocks` has a config key. No service
// definition on any OS carries an environment block (LaunchAgentPlist and
// SystemdUnit have none, the Windows task is a bare /TR "<exe>" run), so before
// this key an installed daemon could not be told to attribute at all, and every
// machine that did was one someone had started by hand with the env var set.
// KELD_ATTRIBUTION still wins in both directions — see attrib.Enabled.
func WriteInstallDefaults(backend string, blocks bool) error {
	if !validBackend(backend) {
		return fmt.Errorf("unknown ml_backend %q (want one of %v)", backend, KnownBackends)
	}

	cfg := map[string]json.RawMessage{}
	if data, err := os.ReadFile(paths.AgentConfigPath()); err == nil {
		// A decode failure is deliberately ignored: see the doc comment.
		_ = json.Unmarshal(data, &cfg)
	}

	backendJSON, err := json.Marshal(backend)
	if err != nil {
		return err
	}
	blocksJSON, err := json.Marshal(blocks)
	if err != nil {
		return err
	}
	attributionJSON, err := json.Marshal(blocks)
	if err != nil {
		return err
	}
	cfg["ml_backend"] = backendJSON
	cfg["blocks"] = blocksJSON
	cfg["attribution"] = attributionJSON

	// Indented + newline-terminated: this file is read and edited by humans, and
	// MarshalIndent sorts map keys, which is what makes a re-install
	// byte-identical rather than merely equivalent.
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')

	return writeConfigAtomic(out)
}

func validBackend(b string) bool {
	for _, k := range KnownBackends {
		if b == k {
			return true
		}
	}
	return false
}

// writeConfigAtomic writes agent-config.json via temp file + rename at 0600, so
// a concurrent reader never observes a torn file. Same shape as
// agentcfg.writeAgentInfo — the daemon may be reading this path while an
// installer rewrites it.
func writeConfigAtomic(data []byte) error {
	if err := os.MkdirAll(paths.KeldHome(), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(paths.KeldHome(), ".agent-config-*.json.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once the rename succeeds
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, paths.AgentConfigPath())
}
