# v2 Installers Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make a fresh install land on v2 — `ml_backend:"deterministic"` with the block emitter on — and fix the Windows path that never onboards at all.

**Architecture:** All four installer paths call `keld-agent install`, so the config write goes in Go once (`runInstall`) rather than three times in sh/PowerShell/Pascal. `KELD_BLOCKS` gains an `agent-config.json` key first, because no service definition on any OS carries an environment block and an env-only toggle is otherwise unreachable from an installer. The remaining work is messaging (the sidecar is the analysis service, not GLiNER2) and giving Windows a visible onboarding console like macOS already has.

**Tech Stack:** Go 1.x (host toolchain), POSIX sh, PowerShell 5.1, Inno Setup 6 (Pascal), Make.

**Spec:** `docs/superpowers/specs/2026-08-27-v2-installer-design.md`

## Global Constraints

- **The compiled-in defaults do NOT change.** `Settings.MLBackend` zero value stays `""` (= auto); `Settings.Blocks` zero value stays `false`. Only the installer writes non-defaults. `TestBuiltInPipelineStillDemandsAModel` must keep passing untouched.
- **`ml_backend` has no remote override** and is startup-only. Do not add one.
- **`blocks` gets no remote override** in this work. `Remote.Features` is not a template to copy here.
- **The config write MERGES**, never overwrites: an operator's `pii_regions`, `include_entity_text`, `features`, `features_publish` must survive an installer run.
- **Atomic write at 0600**, temp-file + rename in `paths.KeldHome()` — copy `internal/agent/agentcfg/agentcfg.go:48-68` exactly.
- **Env wins over config, in BOTH directions.** `KELD_BLOCKS=0` must be able to switch off what the installer switched on. Reuse the `envBool` vocabulary: `1/true/on/yes` and `0/false/off/no`.
- **Two keys, verbatim:** `{"ml_backend": "deterministic", "blocks": true}`.
- **Run `go test ./...` (host toolchain) before every commit.** No sidecar Python changes in this plan, so the sidecar venv is not needed.
- **Never claim a step passed without pasting the output.**

---

### Task 1: `blocks` becomes reachable from `agent-config.json`

`blocks.Enabled()` reads `KELD_BLOCKS` and nothing else. `LaunchAgentPlist` and `SystemdUnit` (`internal/agent/service/service.go`) carry no environment block and the Windows task is a bare `/TR "<exe>" run`, so there is nowhere for an installer to put that variable. Give it a config key with the same **env > config > off** precedence `features/toggle.go` already uses.

**Files:**
- Modify: `internal/agent/settings/settings.go` (add `Blocks` field beside `Features`/`FeaturesPublish`, ~line 74)
- Modify: `internal/agent/blocks/emitter.go:425` (`Enabled` takes the config value)
- Modify: `internal/agent/daemon/blocks.go:49-51` (`startBlockEmitter` takes it and forwards)
- Modify: `internal/agent/daemon/daemon.go:821` (call site passes `set.Blocks`)
- Test: `internal/agent/blocks/emitter_test.go` (add; the file exists — append)

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `settings.Settings.Blocks bool` (json tag `blocks`), and `blocks.Enabled(fromConfig bool) bool`. Task 2 writes the `blocks` key; Task 3's test asserts the written file contains it.

- [ ] **Step 1: Write the failing test**

Append to `internal/agent/blocks/emitter_test.go`:

```go
func TestEnabledEnvOverridesConfigBothWays(t *testing.T) {
	// Env unset: the agent-config value decides. This is the path an installer
	// takes — it writes the file and sets no variable anywhere.
	t.Setenv(EnvEnabled, "")
	if !Enabled(true) {
		t.Error("config true, env unset: want enabled")
	}
	if Enabled(false) {
		t.Error("config false, env unset: want disabled")
	}

	// Env on wins over a config that says off.
	for _, v := range []string{"1", "true", "on", "yes"} {
		t.Setenv(EnvEnabled, v)
		if !Enabled(false) {
			t.Errorf("KELD_BLOCKS=%q over config false: want enabled", v)
		}
	}

	// Env off wins over a config that says on. This direction is what lets an
	// operator disable a machine the installer switched on, without editing
	// JSON — and it is the half a one-directional toggle would silently lose.
	for _, v := range []string{"0", "false", "off", "no"} {
		t.Setenv(EnvEnabled, v)
		if Enabled(true) {
			t.Errorf("KELD_BLOCKS=%q over config true: want disabled", v)
		}
	}

	// An unrecognised value is not an opinion: fall through to the config.
	t.Setenv(EnvEnabled, "maybe")
	if !Enabled(true) {
		t.Error("unrecognised env value should fall through to config true")
	}
}
```

- [ ] **Step 2: Run it and confirm it fails**

Run: `go test ./internal/agent/blocks/ -run TestEnabledEnvOverridesConfigBothWays -v`
Expected: FAIL to compile — `too many arguments in call to Enabled` (it currently takes none).

- [ ] **Step 3: Change `Enabled` to take the config value**

In `internal/agent/blocks/emitter.go`, replace the `Enabled` function (currently at line 425) with:

```go
// Enabled reports the LOCAL value of the `blocks` toggle: KELD_BLOCKS, else the
// agent-config value the caller resolved, else off.
//
// It takes the config value rather than reading agent-config.json itself for
// the reason features.Enabled does: the precedence is stated in one place, and
// this package does not import settings.
//
// ⚠️ The config key exists because an env-only toggle is UNREACHABLE FROM AN
// INSTALLER. No service definition on any OS carries an environment block —
// LaunchAgentPlist and SystemdUnit have none, and the Windows scheduled task is
// a bare /TR "<exe>" run — so there is nowhere an installer could put
// KELD_BLOCKS that the daemon would ever see. The v2 installer writes
// `"blocks": true` into agent-config.json instead.
//
// The env variable still WINS, in both directions, so KELD_BLOCKS=0 switches
// off a machine the installer switched on without editing JSON.
func Enabled(fromConfig bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvEnabled))) {
	case "1", "true", "on", "yes":
		return true
	case "0", "false", "off", "no":
		return false
	}
	return fromConfig
}
```

Also update `EnvEnabled`'s doc comment (line ~390) — it currently ends "Flipping the default is a one-line change the day Atlas reads them." Append:

```go
// ⚠️ The COMPILED-IN default is still off and this work did not change it. What
// changed is that a v2 installer now writes `"blocks": true` per machine (see
// docs/superpowers/specs/2026-08-27-v2-installer-design.md), so an installed
// machine emits blocks while a `go run` / CI / eval machine does not.
```

- [ ] **Step 4: Update the two call sites**

`internal/agent/daemon/blocks.go` — change the signature at line 49 and the guard at line 51:

```go
func startBlockEmitter(ctx context.Context, dig blocks.Digester, ingestEndpoint string,
	token func() string, actor string, emitter *clientevents.Emitter, blocksConfigured bool) func(source, path string) {
	if !blocks.Enabled(blocksConfigured) || dig == nil || token == nil {
		return nil
	}
```

`internal/agent/daemon/daemon.go:821` — pass the loaded setting (`set` is in scope; declared at line 659):

```go
		setBlockAdvance(startBlockEmitter(ctx, svc.Blocks, cfg.Endpoint, tok.Get, actor, emitter, set.Blocks))
```

- [ ] **Step 5: Add the settings field**

In `internal/agent/settings/settings.go`, immediately after the `Features`/`FeaturesPublish` pair (~line 74), add:

```go
	// Blocks is THE V2 PATH's toggle: whether the block emitter runs at all.
	// Local, read at startup, default OFF — the zero value is the default, same
	// as Features.
	//
	// ⚠️ It exists because KELD_BLOCKS alone is unreachable from an installer:
	// no service definition on any OS carries an environment block. A v2
	// install writes this key; KELD_BLOCKS still overrides it either way. See
	// blocks.Enabled.
	//
	// NO REMOTE OVERRIDE, deliberately. Remote.Features governs the
	// signal-embeddings toggles; blocks has no equivalent and adding one is a
	// separate decision with Atlas-side work. The asymmetry is real and worth
	// knowing: an org can turn feature rows off fleet-wide and cannot turn
	// blocks off.
	Blocks bool `json:"blocks"`
```

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/agent/blocks/ ./internal/agent/settings/ ./internal/agent/daemon/`
Expected: PASS (all three packages).

- [ ] **Step 7: Full suite**

Run: `go test ./...`
Expected: PASS. Paste the output.

- [ ] **Step 8: Commit**

```bash
git add internal/agent/settings/settings.go internal/agent/blocks/emitter.go \
        internal/agent/blocks/emitter_test.go internal/agent/daemon/blocks.go \
        internal/agent/daemon/daemon.go
git commit -m "feat(blocks): an agent-config key, because an env-only toggle is unreachable from an installer"
```

---

### Task 2: `settings.WriteInstallDefaults` — a merging, atomic writer

Nothing in this repo has ever written `agent-config.json`; it is read-only from the daemon's side. The installer needs to write exactly two keys **without destroying the rest of the file**.

**Files:**
- Create: `internal/agent/settings/install.go`
- Create: `internal/agent/settings/install_test.go`

**Interfaces:**
- Consumes: `settings.Settings.Blocks` from Task 1 (for the round-trip assertion).
- Produces: `settings.WriteInstallDefaults(backend string, blocks bool) error`. Task 3 calls it from `runInstall`.

- [ ] **Step 1: Write the failing tests**

Create `internal/agent/settings/install_test.go`:

```go
package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/paths"
)

// readConfig decodes the written file into a generic map, so the test sees
// exactly the keys on disk rather than only the ones Settings models.
func readConfig(t *testing.T) map[string]any {
	t.Helper()
	data, err := os.ReadFile(paths.AgentConfigPath())
	if err != nil {
		t.Fatalf("read agent-config.json: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal agent-config.json: %v\n%s", err, data)
	}
	return m
}

func TestWriteInstallDefaultsCreatesBothKeys(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())

	if err := WriteInstallDefaults("deterministic", true); err != nil {
		t.Fatalf("WriteInstallDefaults: %v", err)
	}
	m := readConfig(t)
	if m["ml_backend"] != "deterministic" {
		t.Errorf("ml_backend = %v, want deterministic", m["ml_backend"])
	}
	if m["blocks"] != true {
		t.Errorf("blocks = %v, want true", m["blocks"])
	}

	// And it round-trips through the real loader, which is what the daemon uses.
	s := Load()
	if s.MLBackend != "deterministic" {
		t.Errorf("Load().MLBackend = %q, want deterministic", s.MLBackend)
	}
	if !s.Blocks {
		t.Error("Load().Blocks = false, want true")
	}
	if s.MLEnabled() {
		t.Error("MLEnabled() = true; deterministic must not enable the model")
	}
}

// The whole reason this is a merge: an operator's settings must outlive an
// installer run. ml_backend has no remote override, so a clobbered pii_regions
// could not be restored from the server either.
func TestWriteInstallDefaultsPreservesOtherKeys(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)

	existing := `{
  "pii_regions": ["us", "uk"],
  "include_entity_text": true,
  "features": true,
  "unknown_future_key": {"nested": 1}
}`
	if err := os.WriteFile(filepath.Join(home, "agent-config.json"), []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := WriteInstallDefaults("deterministic", true); err != nil {
		t.Fatalf("WriteInstallDefaults: %v", err)
	}

	m := readConfig(t)
	if m["include_entity_text"] != true {
		t.Error("include_entity_text was lost")
	}
	if m["features"] != true {
		t.Error("features was lost")
	}
	// A key no version of Settings models must survive too — the file is the
	// operator's, not this struct's.
	if m["unknown_future_key"] == nil {
		t.Error("unknown_future_key was lost")
	}
	regions, ok := m["pii_regions"].([]any)
	if !ok || len(regions) != 2 || regions[0] != "us" || regions[1] != "uk" {
		t.Errorf("pii_regions = %v, want [us uk]", m["pii_regions"])
	}
	if m["ml_backend"] != "deterministic" || m["blocks"] != true {
		t.Errorf("the two new keys did not land: %v", m)
	}
}

// A re-install must be a no-op on content, not an accumulating rewrite.
func TestWriteInstallDefaultsIsIdempotent(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())

	if err := WriteInstallDefaults("deterministic", true); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(paths.AgentConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteInstallDefaults("deterministic", true); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(paths.AgentConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Errorf("not idempotent:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

// An unreadable/corrupt file must not abort an install, and must not silently
// keep its garbage either: Load() already treats invalid JSON as "defaults", so
// the writer starts from empty rather than failing.
func TestWriteInstallDefaultsOverCorruptFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "agent-config.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteInstallDefaults("deterministic", true); err != nil {
		t.Fatalf("WriteInstallDefaults over corrupt file: %v", err)
	}
	m := readConfig(t)
	if m["ml_backend"] != "deterministic" || m["blocks"] != true {
		t.Errorf("keys did not land over a corrupt file: %v", m)
	}
}

func TestWriteInstallDefaultsRejectsUnknownBackend(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	if err := WriteInstallDefaults("turbo", true); err == nil {
		t.Fatal("want an error for an unknown backend, got nil")
	}
	// And nothing was written: a rejected value must not leave a half-config.
	if _, err := os.Stat(paths.AgentConfigPath()); !os.IsNotExist(err) {
		t.Errorf("a rejected backend wrote a file anyway (stat err = %v)", err)
	}
}

func TestWriteInstallDefaultsFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix file modes")
	}
	t.Setenv("KELD_HOME", t.TempDir())
	if err := WriteInstallDefaults("deterministic", true); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(paths.AgentConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", st.Mode().Perm())
	}
}
```

- [ ] **Step 2: Run them and confirm they fail**

Run: `go test ./internal/agent/settings/ -run TestWriteInstallDefaults -v`
Expected: FAIL to compile — `undefined: WriteInstallDefaults`.

- [ ] **Step 3: Implement it**

Create `internal/agent/settings/install.go`:

```go
package settings

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/ncx-ai/keld-signal/internal/paths"
)

// KnownBackends is the closed set ml_backend accepts. Validated at the write
// rather than at the read because Load() must never reject an operator's file —
// but an INSTALLER writing a typo would hand every machine it touched a value
// MLEnabled() silently reads as "auto", and ml_backend has no remote override to
// fix it with.
var KnownBackends = []string{"auto", "deterministic", "off"}

// WriteInstallDefaults writes the two settings a v2 install lands on —
// ml_backend and blocks — into ~/.keld/agent-config.json.
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
	cfg["ml_backend"] = backendJSON
	cfg["blocks"] = blocksJSON

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
```

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/agent/settings/ -run TestWriteInstallDefaults -v`
Expected: PASS, all six.

- [ ] **Step 5: Full suite**

Run: `go test ./...`
Expected: PASS. Paste the output.

- [ ] **Step 6: Commit**

```bash
git add internal/agent/settings/install.go internal/agent/settings/install_test.go
git commit -m "feat(settings): WriteInstallDefaults — a merging, atomic agent-config writer"
```

---

### Task 3: `keld-agent install` writes the v2 config

**Files:**
- Modify: `internal/agentcli/agentcli.go` (the `installConfig` struct ~line 100, `runInstall` ~line 110, the `installCmd` wiring ~line 195)
- Test: `internal/agentcli/agentcli_test.go` (append; update the existing `runInstall` call sites for the new parameter)

**Interfaces:**
- Consumes: `settings.WriteInstallDefaults(backend string, blocks bool) error` from Task 2.
- Produces: `runInstall(cfg installConfig, isTTY func() bool, resolveKeld func() (string, error), run stepRunner, writeConfig func(string, bool) error, installService func() error) error` — note `writeConfig` is the **fifth** parameter, before `installService`. `installConfig` gains `backend string`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/agentcli/agentcli_test.go`:

```go
// configRecorder records what runInstall asked to be written.
type configRecorder struct {
	calls   int
	backend string
	blocks  bool
	err     error
}

func (c *configRecorder) write(backend string, blocks bool) error {
	c.calls++
	c.backend = backend
	c.blocks = blocks
	return c.err
}

func TestRunInstallWritesV2Config(t *testing.T) {
	_, run := recorder()
	var cfgw configRecorder
	err := runInstall(installConfig{backend: "deterministic"}, func() bool { return true },
		func() (string, error) { return "/fake/keld", nil }, run, cfgw.write, func() error { return nil })
	if err != nil {
		t.Fatalf("runInstall: %v", err)
	}
	if cfgw.calls != 1 {
		t.Fatalf("writeConfig called %d times, want 1", cfgw.calls)
	}
	if cfgw.backend != "deterministic" || !cfgw.blocks {
		t.Errorf("wrote backend=%q blocks=%v, want deterministic/true", cfgw.backend, cfgw.blocks)
	}
}

// The headless branch registers the service without onboarding — it still needs
// the right config, or a GUI-installer machine runs v1 behaviour forever.
func TestRunInstallWritesConfigInHeadlessBranch(t *testing.T) {
	_, run := recorder()
	var cfgw configRecorder
	err := runInstall(installConfig{backend: "deterministic"}, func() bool { return false },
		func() (string, error) { return "/fake/keld", nil }, run, cfgw.write, func() error { return nil })
	if err != nil {
		t.Fatalf("runInstall: %v", err)
	}
	if cfgw.calls != 1 {
		t.Errorf("writeConfig called %d times in the headless branch, want 1", cfgw.calls)
	}
}

// ml_backend is startup-only, so the write is worthless unless the service
// restart that follows it actually happens after it. Pin the ORDER.
func TestRunInstallWritesConfigBeforeInstallingService(t *testing.T) {
	_, run := recorder()
	var order []string
	writeConfig := func(string, bool) error { order = append(order, "config"); return nil }
	installService := func() error { order = append(order, "service"); return nil }

	err := runInstall(installConfig{backend: "deterministic"}, func() bool { return true },
		func() (string, error) { return "/fake/keld", nil }, run, writeConfig, installService)
	if err != nil {
		t.Fatalf("runInstall: %v", err)
	}
	want := []string{"config", "service"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v — ml_backend is read at startup, so the "+
			"restart in installService must come after the write", order, want)
	}
}

// A config write that fails must abort before anything is registered or started.
func TestRunInstallConfigFailureAbortsBeforeService(t *testing.T) {
	_, run := recorder()
	cfgw := configRecorder{err: errors.New("disk full")}
	installed := false
	err := runInstall(installConfig{backend: "deterministic"}, func() bool { return true },
		func() (string, error) { return "/fake/keld", nil }, run, cfgw.write,
		func() error { installed = true; return nil })
	if err == nil {
		t.Fatal("want an error when the config write fails, got nil")
	}
	if installed {
		t.Error("service was installed despite a failed config write")
	}
}

func TestRunInstallHonoursBackendOverride(t *testing.T) {
	_, run := recorder()
	var cfgw configRecorder
	err := runInstall(installConfig{backend: "auto"}, func() bool { return true },
		func() (string, error) { return "/fake/keld", nil }, run, cfgw.write, func() error { return nil })
	if err != nil {
		t.Fatalf("runInstall: %v", err)
	}
	if cfgw.backend != "auto" {
		t.Errorf("backend = %q, want auto — --backend must be able to put a "+
			"machine back on the model without hand-editing JSON", cfgw.backend)
	}
}
```

- [ ] **Step 2: Run and confirm it fails**

Run: `go test ./internal/agentcli/ -run TestRunInstall -v`
Expected: FAIL to compile — `unknown field backend in struct literal` and `too many arguments in call to runInstall`.

- [ ] **Step 3: Add the field and the parameter**

In `internal/agentcli/agentcli.go`, add to `installConfig` (after `jsonOut`, ~line 103):

```go
	// backend is the ml_backend a v2 install lands on. Default "deterministic"
	// (see newInstallCmd); "auto" restores the pre-v2 ML pipeline and "off"
	// disables enrichment. It is written to agent-config.json because
	// ml_backend has NO REMOTE OVERRIDE — the installer is the only lever that
	// will ever exist for it.
	backend string
```

Change `runInstall`'s signature and its opening, and its doc comment:

```go
// runInstall writes the v2 settings, sets the user up, then registers the service.
//
// ORDER MATTERS TWICE. The daemon refuses to run until signal setup has written
// ~/.keld/hook.json and installService starts it immediately, so service install
// runs LAST. And ml_backend is read at daemon STARTUP and never re-read, so the
// config write runs FIRST — the restart inside installService (launchctl
// bootout+bootstrap / systemctl restart / schtasks /End+/Run) is what makes the
// new mode take effect in this same run. On macOS that is not academic: the pkg's
// postinstall kickstarts the agent BEFORE opening onboard.command, so a daemon is
// already running on the old settings when this is called.
//
// The config write happens in EVERY branch, the headless one included: a machine
// that only registers the service still has to be configured, or a GUI-installer
// install runs v1 behaviour until someone notices.
func runInstall(cfg installConfig, isTTY func() bool, resolveKeld func() (string, error),
	run stepRunner, writeConfig func(backend string, blocks bool) error, installService func() error) error {
	backend := cfg.backend
	if backend == "" {
		backend = "deterministic"
	}
	// blocks is always true: it is what v2 IS. An operator who wants it off sets
	// KELD_BLOCKS=0, which wins over the config file in both directions.
	if err := writeConfig(backend, true); err != nil {
		return fmt.Errorf("write agent-config.json: %w", err)
	}

	login := []string{"login"}
	...unchanged from here...
```

- [ ] **Step 4: Wire the flag and the real writer**

In the `installCmd` `RunE` (~line 198), read the flag and pass the real function:

```go
			jsonOut, _ := cmd.Flags().GetBool("json")
			backend, _ := cmd.Flags().GetString("backend")
			cfg := installConfig{code: code, apiURL: apiURL, yes: yes, jsonOut: jsonOut, backend: backend}
			return runInstall(cfg, stdoutIsTTY, resolveKeld, runStep,
				settings.WriteInstallDefaults, service.Install)
```

And register the flag beside the others (~line 210):

```go
	installCmd.Flags().String("backend", "deterministic",
		"Enrichment backend to configure: deterministic (v2 default — no model download), auto (the GLiNER2 pipeline), or off.")
```

Add the import: `"github.com/ncx-ai/keld-signal/internal/agent/settings"`.

- [ ] **Step 5: Update the existing `runInstall` call sites in the test file**

Every existing `runInstall(...)` call in `internal/agentcli/agentcli_test.go` gains a no-op writer as its fifth argument. There are ten (`TestRunInstallSequence`, `TestRunInstallWithCodeIsHeadlessCapable`, `TestRunInstallCodeAbortsBeforeService`, `TestRunInstallTTYLoginFailureAbortsBeforeService`, `TestRunInstallApiURLAndJSONPassthrough`, `TestRunInstallYesInTTY`, `TestRunInstallPrintsStartingAgentHeaderHuman`, `TestRunInstallSuppressesHumanLinesInJSONMode`, `TestRunInstallAbortsWhenKeldMissing`, `TestRunInstallNoTTYSkipsLoginAndSetup`). Add this helper near `recorder()` and use it:

```go
// noopConfig is the config writer for tests that are not about the config.
func noopConfig(string, bool) error { return nil }
```

So e.g. `TestRunInstallSequence` becomes:

```go
	err := runInstall(installConfig{}, func() bool { return true },
		func() (string, error) { return "/fake/keld", nil }, run, noopConfig,
		func() error { installed = true; return nil })
```

⚠️ **`TestRunInstallAbortsWhenKeldMissing` needs a second look.** It asserts `runInstall` fails when `resolveKeld` errors. The config write now happens BEFORE `resolveKeld`, so it will have been called — that is correct behaviour (the config is valid regardless of whether a `keld` binary was found) and the test's assertion about the error is unaffected. Do not "fix" it by moving the write later.

- [ ] **Step 6: Run the package tests**

Run: `go test ./internal/agentcli/ -v -run TestRunInstall`
Expected: PASS, including the five new tests.

- [ ] **Step 7: Full suite**

Run: `go test ./...`
Expected: PASS. Paste the output.

- [ ] **Step 8: Commit**

```bash
git add internal/agentcli/agentcli.go internal/agentcli/agentcli_test.go
git commit -m "feat(install): write the v2 config (ml_backend=deterministic, blocks=true) before starting the service"
```

---

### Task 4: The sidecar is the analysis service, not "the ML sidecar"

Every installer describes the sidecar as GLiNER2 and justifies its necessity with "on-device ML has no deterministic fallback". Under v2 the model is never loaded and that justification is backwards — but the *conclusion* (abort) is more true than before, because without the sidecar there is no `/analyze`, no `/ingest` and no `/blocks`, and a v2 install produces nothing but credential detection.

**Files:**
- Modify: `scripts/install.sh:198-203` (the comment + `sc_fail` text) and `:232` (the progress line)
- Modify: `installers/macos/onboard.command:12-20` (the comment block) and its `fetch_sidecar` messages
- Modify: `Makefile:73-76` (the `install-linux` summary)
- Modify: `scripts/install.ps1` (final notes)
- Test: `scripts/test-install-sh.sh` (existing harness — confirm it still passes)

**Interfaces:**
- Consumes: nothing. Pure text.
- Produces: nothing consumed by later tasks.

- [ ] **Step 1: Check what the install.sh test harness covers**

Run: `bash scripts/test-install-sh.sh` and read `scripts/test-install.sh`
Expected: a baseline PASS to compare against after editing. Paste it.

- [ ] **Step 2: Rewrite `scripts/install.sh`'s sidecar block**

Replace the comment at lines 198-203 with:

```sh
# Fetch the frozen analysis sidecar — REQUIRED on Linux AND macOS.
#
# This is not "the ML sidecar" any more and the distinction now matters. It is the
# client-side ANALYSIS service: /analyze, /ingest, /blocks and /pii, which is what
# turns transcripts into workstreams, dynamics and v2 blocks. GLiNER2 is one
# capability it loads lazily, and a v2 install (ml_backend:"deterministic") never
# asks for it — so no multi-gigabyte model is ever downloaded.
#
# The abort below is therefore MORE justified than when it was written for ML, not
# less: without this binary a Keld install collects credential detection and
# nothing else. The macOS .pkg does not bundle it either (Apple notarization scans
# all ~15k of its files) — onboard.command fetches it exactly as this does.
# Published per-OS/arch as keld-agent-sidecar_<os>_<arch>.tar.gz (macOS:
# darwin/arm64, Apple Silicon only).
```

Replace the `sc_fail` body:

```sh
  sc_fail() {
    echo "keld: analysis sidecar install failed — without it Keld can derive nothing from" >&2
    echo "  your transcripts (no workstreams, no blocks, no PII scan). Aborting." >&2
    echo "  URL: ${sc_url}" >&2
    exit 1
  }
```

Replace line 232's progress line:

```sh
  echo "  ✓ $(printf '%-26s' 'analysis sidecar') → ${DEST}/keld-agent-sidecar"
```

- [ ] **Step 3: Add the good news to install.sh's summary**

In the `onboarded = 1` branch of the summary (near the end), change:

```sh
  echo "Done — Keld is set up and running."
```

to:

```sh
  echo "Done — Keld is set up and running."
  echo "  Enrichment runs on-device with no model download — nothing multi-gigabyte"
  echo "  is fetched, now or later."
```

- [ ] **Step 4: Same treatment for `installers/macos/onboard.command`**

Replace the `# ── ML sidecar ──` header and its comment block (lines ~12-20) with:

```sh
# ── Analysis sidecar ──────────────────────────────────────────────────────────
# The client-side analysis service: /analyze, /ingest, /blocks, /pii — what turns
# transcripts into workstreams, dynamics and v2 blocks. NOT "the ML sidecar": a v2
# install runs ml_backend:"deterministic" and never loads GLiNER2, so no
# multi-gigabyte model is downloaded at any point.
#
# The pkg deliberately ships without it (~15k files / ~190MB of torch): Apple's
# notary service scans every file, which put release builds hours into an unbounded
# queue. Fetch it here instead.
```

and in `fetch_sidecar`, change the three user-facing strings:

```sh
    echo "  ✓ analysis sidecar already present → ${dest}/keld-agent-sidecar"
```
```sh
    *) echo "  ! Keld's analysis sidecar ships for Apple Silicon only (this Mac reports ${arch})." >&2
```
```sh
  echo "  … downloading analysis sidecar (${tag}, ~190MB) → ${dest}"
```
```sh
  echo "  ✓ analysis sidecar → ${dest}/keld-agent-sidecar"
```

and the download-failure message, which must match `install.sh`'s verdict in wording even though it cannot abort a completed pkg install:

```sh
    echo "  ! analysis sidecar download failed ($url)" >&2
    echo "    Keld will still collect telemetry, but it can derive nothing from your" >&2
    echo "    transcripts (no workstreams, no blocks, no PII scan) until this is present." >&2
    echo "    Re-run this script to retry." >&2
```

- [ ] **Step 5: Fix the Makefile summary**

`Makefile:72-76` currently claims the first ML enrichment provisions a ~1.9GB model. Replace with:

```make
	@echo ""
	@echo "keld-agent installed WITH the analysis sidecar."
	@echo "NOTE: 'keld-agent install' configured this machine for v2:"
	@echo "  ml_backend=deterministic, blocks=true  (~/.keld/agent-config.json)"
	@echo "  No model is downloaded. For the GLiNER2 pipeline instead:"
	@echo "    keld-agent install --backend auto"
	@echo "If not yet configured, run:  keld login && keld signal setup"
	@echo "Visualize enrichments:      make enrichments-sink"
```

- [ ] **Step 6: Add the same note to `scripts/install.ps1`**

After the existing `"keld $tag installed to ..."` line, add:

```powershell
Write-Host "This machine is configured for v2: ml_backend=deterministic, blocks=true."
Write-Host "  No model is downloaded. For the GLiNER2 pipeline: keld-agent install --backend auto"
```

- [ ] **Step 7: Verify**

Run: `bash -n scripts/install.sh && bash -n installers/macos/onboard.command && bash scripts/test-install-sh.sh`
Expected: no syntax errors, and the harness output matches Step 1's baseline. Paste it.

Run: `pwsh -NoProfile -Command "[void][System.Management.Automation.Language.Parser]::ParseFile('scripts/install.ps1',[ref]$null,[ref]$null); 'ps1 parses'"` if `pwsh` exists; otherwise state that it was not checked, rather than claiming it was.

- [ ] **Step 8: Commit**

```bash
git add scripts/install.sh scripts/install.ps1 installers/macos/onboard.command Makefile
git commit -m "docs(installers): the sidecar is the analysis service, and v2 downloads no model"
```

---

### Task 5: Windows finally onboards

`installers/windows/keld-agent.iss:31-32` runs `keld-agent install` with `runhidden nowait`. Whichever way `stdoutIsTTY()` resolves inside a hidden console, no human can complete a login in a window they cannot see, and `nowait` means Inno cannot report the outcome either. The daemon idles on `awaitConfig` forever, collecting nothing and saying nothing.

The fix is parity with macOS: a visible console running an onboarding script, reusing the Go seam that already works on two platforms.

**Files:**
- Create: `installers/windows/onboard.cmd`
- Modify: `installers/windows/keld-agent.iss` (`[Files]` stages it, `[Run]` opens it visibly)
- Modify: `.github/workflows/installers.yml` (the Windows packaging step copies it — it already copies `keld.exe`/`keld-agent.exe`, and `onboard.cmd` lives beside the `.iss` so no copy is needed; **verify** rather than assume)

**Interfaces:**
- Consumes: `keld-agent install --code <CODE>` and `--backend`, both from Task 3.
- Produces: nothing consumed by later tasks.

- [ ] **Step 1: Write `installers/windows/onboard.cmd`**

Create it as the Windows sibling of `onboard.command`. Note `%KELD_HOME%` fallback and the observed-state success check, both matching its macOS and shell siblings:

```bat
@echo off
setlocal enabledelayedexpansion
rem Keld setup - runs after install. Redeems your one-time setup code (from the Keld
rem download page) for a non-interactive login, configures your AI tools, then starts
rem the background agent. Safe to re-run.
rem
rem This is the Windows sibling of installers/macos/onboard.command. It exists because
rem the Inno [Run] step used to launch `keld-agent install` with `runhidden`, so the
rem interactive login ran in a window nobody could see and no machine was ever
rem onboarded.
set "AGENT=%~dp0keld-agent.exe"
if not exist "%AGENT%" set "AGENT=%LOCALAPPDATA%\Programs\keld\keld-agent.exe"

echo.
echo ==== Set up Keld ====
echo.
set /p CODE="Paste your setup code from the Keld download page (or press Enter to log in with a browser): "

if defined CODE (
  "%AGENT%" install --code "%CODE%"
  if errorlevel 1 (
    echo Setup code didn't work; falling back to browser login...
    "%AGENT%" install --yes
  )
) else (
  "%AGENT%" install --yes
)

rem Claim success only if it is true: setup is done when an ingest token exists in
rem hook.json, the same file the daemon reads. `keld-agent install` can exit 0 after
rem merely registering the scheduled task.
set "KELD_HOME_DIR=%KELD_HOME%"
if not defined KELD_HOME_DIR set "KELD_HOME_DIR=%USERPROFILE%\.keld"

echo.
findstr /r /c:"\"ingest_token\"[ ]*:[ ]*\"[^\"]" "%KELD_HOME_DIR%\hook.json" >nul 2>&1
if errorlevel 1 (
  echo Keld is installed, but NOT set up yet ^(nothing is being collected^).
  echo Run:  keld login  then  keld signal setup
  echo The agent is already running and picks the configuration up on its own.
) else (
  echo Keld is set up and running. You can close this window.
)

echo.
echo ^(Re-run anytime: "%~f0"^)
echo.
pause
```

- [ ] **Step 2: Stage it and make the post-install step visible**

In `installers/windows/keld-agent.iss`, add to `[Files]`:

```
Source: "onboard.cmd";          DestDir: "{app}"; Flags: ignoreversion
```

and REPLACE the `[Run]` section entirely:

```
[Run]
; Onboarding runs in a VISIBLE console in the user's session and registers the
; scheduled task itself (keld-agent install does login -> signal setup -> service,
; in that order). It used to be `keld-agent.exe install` with `runhidden nowait`,
; which meant the interactive login ran where nobody could see or complete it and
; the daemon idled on awaitConfig forever. Do not re-add runhidden here.
Filename: "{app}\onboard.cmd"; Description: "Set up Keld"; \
  Flags: postinstall shellexec skipifsilent
```

⚠️ `skipifsilent` matters: a `/SILENT` install (an MDM push) must not block on a console waiting for a human to paste a code. Such a machine registers nothing and is finished by `keld-agent install --code` from the management tool.

- [ ] **Step 3: Verify the workflow stages the new file**

Read `.github/workflows/installers.yml`'s "Package Windows installer" step. It copies `stage\keld.exe, stage\keld-agent.exe` into `installers\windows\`; `onboard.cmd` is already committed there, so `iscc` finds it with no workflow change.

Run: `grep -n "Copy-Item" .github/workflows/installers.yml`
Expected: confirms only the exes and the sidecar dir are copied, and that `onboard.cmd` needs no new copy step. **If the `.iss` build turns out to run from a different directory, add the copy — do not assume.**

- [ ] **Step 4: Update the `.iss` header comment**

The header lists what CI stages beside the script. Add `onboard.cmd`:

```
;   keld.exe, keld-agent.exe, keld-agent-sidecar\  (frozen one-dir)
;   onboard.cmd is committed beside this script, not staged by CI.
```

- [ ] **Step 5: Verify what can be verified here**

Run: `go test ./...`
Expected: PASS (nothing Go changed, but the tree must stay green).

⚠️ **State plainly in the commit and to the user that the `.cmd` and the Inno page were NOT executed on Windows.** No CI check can confirm a console appeared and a human pasted a code. What CI does cover: `iscc` compiles the `.iss` (an unstaged `Source:` file is a compile error, so Step 2's `[Files]` line is machine-checked). The rest needs one human run of the built `keld-setup.exe`.

- [ ] **Step 6: Commit**

```bash
git add installers/windows/onboard.cmd installers/windows/keld-agent.iss
git commit -m "fix(windows): onboard in a visible console — runhidden meant no machine ever logged in"
```

---

### Task 6: AGENTS.md and CLAUDE.md corrections

**Files:**
- Modify: `AGENTS.md` (the Windows onboarding gotcha; the sidecar's installer role; a new v2-install note near *Model backends*)
- Modify: `CLAUDE.md` only if a bullet there repeats a corrected claim — check, do not assume.

**Interfaces:**
- Consumes: everything above.
- Produces: nothing.

- [ ] **Step 1: Find the stale claims**

Run: `grep -n "Windows onboarding UI\|\[Code\]\|WinAPI timer\|NDJSON temp-file" AGENTS.md CLAUDE.md`
Expected: locates the "Windows onboarding UI" gotcha bullet. Paste the line numbers.

- [ ] **Step 2: Replace the Windows onboarding gotcha**

Replace that bullet with:

```markdown
- **Windows onboarding UI:** `installers/windows/onboard.cmd`, a plain console script
  staged into the payload by the `.iss` and opened by the post-install `[Run]` step
  with `postinstall shellexec skipifsilent`. It is the sibling of macOS's
  `onboard.command` and does the same three things: prompt for the one-time setup
  code, run `keld-agent install --code "$CODE"` (falling back to `--yes` browser
  login), and report success from OBSERVED STATE — an `ingest_token` in `hook.json`
  — never from an exit code.
  ⚠️ **This bullet used to describe an Inno `[Code]` wizard page driving `keld --json`
  with a WinAPI timer and async NDJSON polling. That page NEVER EXISTED** — `git log`
  on the `.iss` shows two commits and neither added it. What was actually there was
  `[Run] keld-agent.exe install` with `runhidden nowait`, i.e. an interactive login
  in a window nobody could see, on a step Inno did not wait for and could not report.
  Every Windows machine registered its logon task and then idled on `awaitConfig`
  forever, collecting nothing and saying nothing. The doc describing unbuilt code as
  built is what kept that invisible. **Do not re-add `runhidden` to that `[Run]`
  line.** The wizard page remains a nicer UX and is a legitimate future change; it is
  an aspiration, not a description.
```

- [ ] **Step 3: Add the v2-install note**

Immediately after the **Model backends** bullet's `"off"` entry in `AGENTS.md`, add:

```markdown
**⚠️ WHAT A FRESH INSTALL LANDS ON, AND WHY IT IS NOT THE COMPILED-IN DEFAULT.**
`keld-agent install` writes two keys into `~/.keld/agent-config.json` —
`{"ml_backend": "deterministic", "blocks": true}` — via
`settings.WriteInstallDefaults`, which MERGES so an operator's `pii_regions` and
feature toggles survive. That is v2: the model-free facet set plus the block
emitter, and **no multi-gigabyte model download, ever**.

The **compiled-in defaults are unchanged** (`ml_backend` zero value = `"auto"`,
`Blocks` = false) and that is deliberate, not a hedge. Every machine an installer
never writes to — a binary upgraded in place, `go run`, CI, the eval harness —
keeps the full ML facet set, `TestBuiltInPipelineStillDemandsAModel` keeps passing
untouched, and Atlas's Context column keeps rendering `function_guess` /
`subcategory` / `activity_type` for that population. Phase 5 of
`docs/superpowers/specs/2026-08-25-signal-block-pipeline-design.md` (the default
flip) is still unstarted and still gated on Atlas.

⚠️ **`ml_backend` has NO REMOTE OVERRIDE.** It is local and startup-only and
nothing in `agentcfg/` touches it, so Atlas can neither move a machine between
modes nor roll one back. **The installer is the only lever that will ever exist**,
which has two consequences worth stating: a re-install FLIPS an existing `auto`
machine (deliberate — every re-install converges), and an existing fleet's Atlas
Context column therefore empties machine-by-machine at whatever pace people
upgrade, with no server-side brake. If that pace ever needs controlling, the
control is a staged rollout of the installer itself.

`--backend auto|deterministic|off` on `keld-agent install` is the manual path back.
`blocks` likewise has no remote override — an asymmetry with `Remote.Features`,
which CAN turn feature rows off fleet-wide — and `KELD_BLOCKS=0` is what switches
a single machine off without editing JSON. ⚠️ **`make install-linux` routes through
`keld-agent install` too** (`Makefile:67`), so a dev machine converges on
`deterministic` like everyone else; pass `--backend auto` to keep exercising
GLiNER2 locally.
```

- [ ] **Step 4: Correct the sidecar's installer role**

In the **Repo layout** / **Gotchas** area, wherever an installer bullet calls the fetched tarball "the ML sidecar" or "the frozen GLiNER2 sidecar" in the context of what an installer downloads, change it to "the analysis sidecar" and add once:

```markdown
  ⚠️ **What the installers fetch is the ANALYSIS service, not "the ML sidecar".**
  `/analyze`, `/ingest`, `/blocks` and `/pii` are what turn transcripts into
  workstreams, dynamics and blocks; GLiNER2 is one capability the same binary
  loads lazily, and a v2 install never asks for it. So `install.sh` still ABORTS
  when the fetch fails — the conclusion was always right, only its stated reason
  ("on-device ML has no deterministic fallback") was wrong. Without this binary a
  Keld install derives nothing from a transcript and publishes credential
  detection alone.
```

- [ ] **Step 5: Check CLAUDE.md**

Run: `grep -n "sidecar\|installer\|Windows" CLAUDE.md`
Expected: CLAUDE.md's bullets are about the privacy invariant and dev workflow, not installers. **If nothing there repeats a corrected claim, change nothing** — do not add installer prose to CLAUDE.md for symmetry.

- [ ] **Step 6: Verify the tree is green**

Run: `go test ./...`
Expected: PASS. Paste the output.

- [ ] **Step 7: Commit**

```bash
git add AGENTS.md
git commit -m "docs: correct the Windows onboarding claim, and state what a v2 install lands on"
```

---

## Final verification

- [ ] **Run the whole Go suite and paste it**

Run: `go test ./...`
Expected: PASS.

- [ ] **Prove the two keys actually land, end to end**

⚠️ **DO NOT run `keld-agent install` on a development machine to check this.**
`KELD_HOME` isolates `~/.keld` but NOT the service path: `service.Install` resolves
the systemd unit / launchd plist from `os.UserHomeDir()`, so it rewrites the
developer's REAL unit to point at the `go run` temp binary and restarts it. Done
once during this plan's own execution, it left `keld-agent.service` pointing at
`/tmp/go-build.../exe/keld-agent` in state `failed`, and the unit had to be
hand-restored. `--backend` does not change that; nothing about the flag is at
fault. Fixing this properly means teaching `service.Install` to honour a home
override, which is out of scope here.

Verify the composition without running the installer:

```bash
go test ./internal/agent/settings/ -run TestWriteInstallDefaults -v
go test ./internal/agentcli/ -run 'TestRunInstall(WritesV2Config|DefaultsToDeterministic|WritesConfigBeforeInstallingService)' -v
```

The first proves the file content and mode; the second proves `runInstall` passes
`deterministic`/`true` and does it before `installService`. Together they cover the
end-to-end claim, and the flag-to-`installConfig` wiring is one line visible in
`installCmd`'s `RunE`. Paste both outputs.

- [ ] **Confirm the shell installers still parse**

Run: `bash -n scripts/install.sh && bash -n installers/macos/onboard.command && echo "shell OK"`

- [ ] **State the verification gap out loud**

The Windows console flow (`onboard.cmd` + the Inno `[Run]` page) was NOT executed on Windows. `iscc` compiling the `.iss` in CI proves the file is staged and the script syntax is accepted; it proves nothing about whether a human can paste a code into it. Say so in the summary rather than implying coverage.
