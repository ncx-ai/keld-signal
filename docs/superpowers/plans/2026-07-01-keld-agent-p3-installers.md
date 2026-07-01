# keld-agent P3 — GUI Installers (unsigned-first) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship keld-agent as native GUI installers (macOS `.pkg`, Windows Inno `.exe`, Linux `curl|sh`) bundling the frozen GLiNER2 sidecar, buildable unsigned in CI with signing/notarization gated behind maintainer secrets.

**Architecture:** One locally-testable Go change (`sidecarBinPath()` per-OS resolution) + authored, CI-verified packaging: a PyInstaller freeze of `sidecar/` into `keld-agent-sidecar`, three installer definitions, extended `install.sh`, and a GitHub Actions workflow (freeze matrix → package → gated sign → attach to the GoReleaser release). The installers are thin: place binaries + register the per-user service; keld-agent's own first-run flow does login/config/model.

**Tech Stack:** Go (module `github.com/ncx-ai/keld-cli`), PyInstaller, pkgbuild/productbuild + notarytool (macOS), Inno Setup (Windows), GoReleaser, GitHub Actions.

## Global Constraints

- **Only Task 1 (`sidecarBinPath`) is locally testable** in this Linux dev env. Tasks 2–6 (freeze, `.pkg`, Inno, CI) are **authored here and verified by a CI run** on the maintainer's macOS/Windows/Linux runners — do NOT claim they were built/tested locally; validate syntax where a linter exists (`sh -n`, `actionlint`/`yamllint` if present) and stop there.
- **Unsigned-first:** every packaging step must produce an artifact with NO signing secrets present; signing/notarization runs only when the secrets exist and must be skipped cleanly when absent.
- **Sidecar binary name is `keld-agent-sidecar`** everywhere (fixes the current `keld-sidecar` mismatch in `daemon.go`).
- **The frozen sidecar is a separate release asset** (built by PyInstaller in CI, NOT GoReleaser). GUI installers (`.pkg`/Inno) **bundle** it, and `curl|sh` **installs it by default too** (the full ML experience out of the box). It is hundreds of MB, so `curl|sh` provides a `KELD_NO_SIDECAR=1` **opt-out** for a lean deterministic-only install; the deterministic backend works without it.
- **Runtime unchanged:** no edits to the P1/P2 enrichment runtime beyond `sidecarBinPath()`. Deterministic stays the default; ML dormant until the sidecar binary is installed.
- **No custom GUI app** (native installer wizards suffice); **nfpm `.deb`/`.rpm` deferred**; the installer never performs login.

## File Structure

- `internal/agent/daemon/daemon.go` (MOD) — `sidecarBinPath()` per-OS resolution.
- `internal/agent/daemon/sidecarpath_test.go` (NEW) — Go test for it.
- `sidecar/keld-agent-sidecar.spec` (NEW) — PyInstaller spec.
- `sidecar/build-freeze.sh` (NEW) — freeze build helper.
- `installers/macos/build-pkg.sh` + `installers/macos/scripts/postinstall` + `installers/macos/distribution.xml` (NEW).
- `installers/windows/keld-agent.iss` (NEW).
- `scripts/install.sh` (MOD) — opt-in sidecar fetch.
- `.github/workflows/installers.yml` (NEW) — freeze/package/sign/release.

---

### Task 1: `sidecarBinPath()` per-OS install-location resolution (LOCAL, TDD)

**Files:** Modify `internal/agent/daemon/daemon.go`; Test `internal/agent/daemon/sidecarpath_test.go`.

**Interfaces:** `sidecarBinPath() (string, bool)` — resolution order: (1) `KELD_SIDECAR_BIN` env (if it exists); (2) `keld-agent-sidecar[.exe]` next to the running executable (via `os.Executable()`); (3) a per-OS well-known location. Returns `("", false)` if none exist.

- [ ] **Step 1: Write the failing test**

```go
package daemon

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSidecarBinPathEnvOverride(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "custom-sidecar")
	if err := os.WriteFile(p, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KELD_SIDECAR_BIN", p)
	got, ok := sidecarBinPath()
	if !ok || got != p {
		t.Fatalf("env override: got %q,%v want %q,true", got, ok, p)
	}
}

func TestSidecarBinPathEnvMissingFileIgnored(t *testing.T) {
	t.Setenv("KELD_SIDECAR_BIN", filepath.Join(t.TempDir(), "nope"))
	// No sibling binary in the test's exec dir, so expect not-found.
	if _, ok := sidecarBinPath(); ok {
		t.Fatal("nonexistent env path should not resolve")
	}
}

func TestSidecarBinPathBesideExecutable(t *testing.T) {
	os.Unsetenv("KELD_SIDECAR_BIN")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	name := "keld-agent-sidecar"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	sib := filepath.Join(filepath.Dir(exe), name)
	if _, err := os.Stat(sib); err == nil {
		t.Skip("a real sidecar sits beside the test binary; skip synthetic check")
	}
	// Create it beside the test executable, assert resolution, then clean up.
	if err := os.WriteFile(sib, []byte("x"), 0o755); err != nil {
		t.Skipf("cannot write beside test exe (%v); environment-limited", err)
	}
	defer os.Remove(sib)
	got, ok := sidecarBinPath()
	if !ok || got != sib {
		t.Fatalf("beside-exe: got %q,%v want %q,true", got, ok, sib)
	}
}
```

- [ ] **Step 2: Run to verify it fails** — `cd ~/keld/keld-cli && go test ./internal/agent/daemon/ -run SidecarBinPath -v` → FAIL (current impl looks for `/usr/local/bin/keld-sidecar`, wrong name; beside-exe not implemented).

- [ ] **Step 3: Implement** — replace `sidecarBinPath()`:

```go
func sidecarBinPath() (string, bool) {
	if p := os.Getenv("KELD_SIDECAR_BIN"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p, true
		}
	}
	name := "keld-agent-sidecar"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	// (2) beside the running keld-agent executable (how the installers lay it out).
	if exe, err := os.Executable(); err == nil {
		if p := filepath.Join(filepath.Dir(exe), name); statExists(p) {
			return p, true
		}
	}
	// (3) per-OS well-known fallback.
	for _, p := range wellKnownSidecarPaths(name) {
		if statExists(p) {
			return p, true
		}
	}
	return "", false
}

func statExists(p string) bool { _, err := os.Stat(p); return err == nil }

func wellKnownSidecarPaths(name string) []string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		return []string{"/usr/local/bin/" + name, filepath.Join(home, ".local/bin", name)}
	case "windows":
		if la := os.Getenv("LOCALAPPDATA"); la != "" {
			return []string{filepath.Join(la, "Programs", "keld", name)}
		}
		return nil
	default: // linux
		return []string{filepath.Join(home, ".local/bin", name), "/usr/local/bin/" + name}
	}
}
```
Add `"path/filepath"` and `"runtime"` to the imports if not already present.

- [ ] **Step 4: Run to verify pass** — `go test ./internal/agent/daemon/ -run SidecarBinPath -v` → PASS. Then `go build ./... && go vet ./... && go test ./...` green.

- [ ] **Step 5: Commit** — `git add internal/agent/daemon/ && git commit -m "feat(daemon): resolve keld-agent-sidecar beside exe + per-OS paths"`

---

### Task 2: PyInstaller freeze of the sidecar (AUTHORED, CI-verified)

**Files:** Create `sidecar/keld-agent-sidecar.spec`, `sidecar/build-freeze.sh`. (Not runnable in this Linux env for the macOS/Windows targets; the Linux freeze *could* run in CI. Author + rely on CI.)

**Interfaces:** Produces a `keld-agent-sidecar` (one-dir) tree whose entry binary the installers place so `sidecarBinPath()` (Task 1) finds it. Entry point is `sidecar/serve.py` (`--port`).

- [ ] **Step 1: Write `sidecar/keld-agent-sidecar.spec`** (one-dir; collect torch/gliner2 data + hidden imports)

```python
# PyInstaller spec — build with: pyinstaller sidecar/keld-agent-sidecar.spec
# One-dir keeps torch's shared libs + data files intact (one-file unpacks slowly
# and is fragile with torch). Produces dist/keld-agent-sidecar/keld-agent-sidecar.
from PyInstaller.utils.hooks import collect_all, collect_submodules

datas, binaries, hiddenimports = [], [], []
for pkg in ("torch", "gliner2", "transformers", "tokenizers", "safetensors", "huggingface_hub"):
    d, b, h = collect_all(pkg)
    datas += d; binaries += b; hiddenimports += h
hiddenimports += collect_submodules("uvicorn")

a = Analysis(["sidecar/serve.py"], pathex=["sidecar"], datas=datas,
             binaries=binaries, hiddenimports=hiddenimports, noarchive=False)
pyz = PYZ(a.pure)
exe = EXE(pyz, a.scripts, [], exclude_binaries=True, name="keld-agent-sidecar",
          console=True)
coll = COLLECT(exe, a.binaries, a.datas, name="keld-agent-sidecar")
```

- [ ] **Step 2: Write `sidecar/build-freeze.sh`** (installs deps + runs PyInstaller; used by CI per-OS)

```bash
#!/usr/bin/env bash
# Freeze the sidecar into dist/keld-agent-sidecar/. Run per-OS in CI (needs the
# target OS's Python 3.12). NOT runnable for other OSes from this machine.
set -euo pipefail
cd "$(dirname "$0")/.."
python -m pip install --upgrade pip pyinstaller
python -m pip install -r sidecar/requirements.txt
pyinstaller --clean --noconfirm sidecar/keld-agent-sidecar.spec
echo "frozen -> dist/keld-agent-sidecar/"
```

- [ ] **Step 3: Verify (authored-only)** — `bash -n sidecar/build-freeze.sh` (syntax); confirm `sidecar/serve.py` still exists and its `--port` entry is intact. Do NOT attempt to run PyInstaller here — record that the freeze is CI-verified (Task 6 adds the smoke).

- [ ] **Step 4: Commit** — `git add sidecar/ && git commit -m "build(sidecar): PyInstaller freeze spec + build script (T11)"`

---

### Task 3: macOS `.pkg` (AUTHORED, CI-verified)

**Files:** Create `installers/macos/build-pkg.sh`, `installers/macos/scripts/postinstall`, `installers/macos/distribution.xml`.

**Interfaces:** Consumes the GoReleaser `keld`/`keld-agent` binaries + the frozen `keld-agent-sidecar` (Task 2). Produces `keld-<version>.pkg`. Signing gated on `APPLE_DEVELOPER_ID_APP`/`_INSTALLER` + notarytool secrets.

- [ ] **Step 1: `installers/macos/scripts/postinstall`** — place binaries on PATH, register the per-user LaunchAgent as the console user (mirrors `service_darwin.go`).

```bash
#!/bin/bash
set -e
# Payload staged to /usr/local/keld; symlink the CLIs onto PATH, keep the sidecar
# beside keld-agent so sidecarBinPath() finds it.
PREFIX="/usr/local/keld"
ln -sf "$PREFIX/keld" /usr/local/bin/keld
ln -sf "$PREFIX/keld-agent" /usr/local/bin/keld-agent
# Register the LaunchAgent in the logged-in user's GUI session (postinstall runs as root).
uid=$(stat -f %u /dev/console); user=$(id -un "$uid")
launchctl asuser "$uid" sudo -u "$user" "$PREFIX/keld-agent" install || true
exit 0
```
(Sidecar lives at `$PREFIX/keld-agent-sidecar`; `keld-agent` is at `$PREFIX/keld-agent`, so beside-exe resolution finds it. The symlinks on `/usr/local/bin` are the PATH entries; the daemon resolves the sidecar via its real path since `os.Executable()` follows the symlink target.)

- [ ] **Step 2: `installers/macos/distribution.xml`** — productbuild distribution (title, welcome, per-user note). Minimal:

```xml
<?xml version="1.0" encoding="utf-8"?>
<installer-gui-script minSpecVersion="2">
  <title>Keld</title>
  <options customize="never" require-scripts="true"/>
  <choices-outline><line choice="default"/></choices-outline>
  <choice id="default"><pkg-ref id="co.keld.agent"/></choice>
  <pkg-ref id="co.keld.agent" version="0">keld-component.pkg</pkg-ref>
</installer-gui-script>
```

- [ ] **Step 3: `installers/macos/build-pkg.sh`** — pkgbuild component + productbuild, with gated signing.

```bash
#!/usr/bin/env bash
# Build keld-<version>.pkg from a staged payload dir. Signs + notarizes ONLY when
# the Developer ID / notarytool secrets are present; otherwise emits an unsigned pkg.
set -euo pipefail
VERSION="${1:?version}"; STAGE="${2:?payload dir (contains keld, keld-agent, keld-agent-sidecar)}"
OUT="keld-${VERSION}.pkg"; ROOT="$(cd "$(dirname "$0")" && pwd)"

# Optional codesign of the Mach-O binaries (hardened runtime) when signing identity present.
if [ -n "${APPLE_DEVELOPER_ID_APP:-}" ]; then
  for b in keld keld-agent keld-agent-sidecar; do
    codesign --force --options runtime --timestamp --sign "$APPLE_DEVELOPER_ID_APP" "$STAGE/$b" || true
  done
fi

pkgbuild --root "$STAGE" --install-location /usr/local/keld \
  --scripts "$ROOT/scripts" --identifier co.keld.agent --version "$VERSION" keld-component.pkg

PB=(productbuild --distribution "$ROOT/distribution.xml" --package-path . "$OUT")
if [ -n "${APPLE_DEVELOPER_ID_INSTALLER:-}" ]; then PB+=(--sign "$APPLE_DEVELOPER_ID_INSTALLER"); fi
"${PB[@]}"

# Notarize + staple when notarytool creds are present.
if [ -n "${APPLE_NOTARY_KEY:-}" ] && [ -n "${APPLE_NOTARY_KEY_ID:-}" ] && [ -n "${APPLE_NOTARY_ISSUER:-}" ]; then
  xcrun notarytool submit "$OUT" --key "$APPLE_NOTARY_KEY" --key-id "$APPLE_NOTARY_KEY_ID" \
    --issuer "$APPLE_NOTARY_ISSUER" --wait
  xcrun stapler staple "$OUT"
fi
echo "built $OUT"
```

- [ ] **Step 4: Verify (authored-only)** — `bash -n installers/macos/build-pkg.sh installers/macos/scripts/postinstall`; sanity-check the XML is well-formed (`xmllint --noout` if available). Record: real `.pkg` build + notarization are CI-only (macOS runner).

- [ ] **Step 5: Commit** — `git add installers/macos && git commit -m "build(macos): notarizable .pkg (unsigned-first) + LaunchAgent postinstall"`

---

### Task 4: Windows Inno Setup installer (AUTHORED, CI-verified)

**Files:** Create `installers/windows/keld-agent.iss`.

**Interfaces:** Per-user install to `{localappdata}\Programs\keld`; adds it to the user PATH; runs `keld-agent install` (P1 registers the logon task via schtasks). Authenticode signing gated (CI passes `/sign` when `WINDOWS_CERT_PFX` present).

- [ ] **Step 1: Write `installers/windows/keld-agent.iss`**

```ini
; Inno Setup script — build in CI: iscc /DMyVersion=%VERSION% installers\windows\keld-agent.iss
; Per-user install (no admin). Files staged next to this script by CI:
;   keld.exe, keld-agent.exe, keld-agent-sidecar\  (frozen one-dir)
#define MyVersion GetEnv("KELD_VERSION")
[Setup]
AppName=Keld
AppVersion={#MyVersion}
DefaultDirName={localappdata}\Programs\keld
PrivilegesRequired=lowest
DisableProgramGroupPage=yes
OutputBaseFilename=keld-setup
ChangesEnvironment=yes
[Files]
Source: "keld.exe";            DestDir: "{app}"; Flags: ignoreversion
Source: "keld-agent.exe";      DestDir: "{app}"; Flags: ignoreversion
Source: "keld-agent-sidecar\*"; DestDir: "{app}"; Flags: ignoreversion recursesubdirs createallsubdirs
[Tasks]
Name: "addtopath"; Description: "Add Keld to my PATH"; Flags: checkedonce
[Registry]
Root: HKCU; Subkey: "Environment"; ValueType: expandsz; ValueName: "Path"; \
  ValueData: "{olddata};{app}"; Tasks: addtopath; Check: NeedsAddPath('{app}')
[Run]
; Register the per-user logon task (keld-agent install uses schtasks on Windows).
Filename: "{app}\keld-agent.exe"; Parameters: "install"; Flags: runhidden nowait postinstall
[Code]
function NeedsAddPath(P: string): Boolean;
var O: string;
begin
  if not RegQueryStringValue(HKCU, 'Environment', 'Path', O) then O := '';
  Result := Pos(';' + P + ';', ';' + O + ';') = 0;
end;
```
(The frozen one-dir `keld-agent-sidecar\` is copied into `{app}`, so `keld-agent-sidecar.exe` sits beside `keld-agent.exe` → `sidecarBinPath()` beside-exe resolution finds it.)

- [ ] **Step 2: Verify (authored-only)** — visual review; Inno's `iscc` is Windows-only, so compilation is CI-verified. Confirm file names match Task 2's freeze output (`keld-agent-sidecar.exe` inside the one-dir).

- [ ] **Step 3: Commit** — `git add installers/windows && git commit -m "build(windows): Inno Setup per-user installer (unsigned-first) + logon task"`

---

### Task 5: Linux `install.sh` — opt-in sidecar (LOCAL: `sh -n`)

**Files:** Modify `scripts/install.sh`.

**Interfaces:** By **default** fetches the `keld-agent-sidecar_${os}_${arch}.tar.gz` release asset and extracts it beside `keld-agent` in `$DEST`. `KELD_NO_SIDECAR=1` **opts out** (lean deterministic-only install). A failed fetch is non-fatal (continues on the deterministic backend).

- [ ] **Step 1: Add the sidecar fetch** after the existing `keld-agent` chmod/service block:

```sh
# Fetch the frozen GLiNER2 sidecar (large, ~hundreds of MB) BY DEFAULT — this is
# the full ML experience, matching the GUI installers. Set KELD_NO_SIDECAR=1 for
# a lean, deterministic-only install (the deterministic backend needs no sidecar).
if [ "${KELD_NO_SIDECAR:-0}" != "1" ]; then
  sc_archive="keld-agent-sidecar_${os}_${arch}.tar.gz"
  sc_url="https://github.com/${REPO}/releases/download/${tag}/${sc_archive}"
  echo "Fetching keld-agent-sidecar (large; set KELD_NO_SIDECAR=1 to skip)..."
  if curl -fsSL "$sc_url" | tar -xz -C "$DEST"; then
    chmod +x "${DEST}/keld-agent-sidecar" 2>/dev/null || true
    echo "keld-agent-sidecar installed to ${DEST}/keld-agent-sidecar"
  else
    echo "keld: sidecar download failed; continuing with the deterministic backend." >&2
  fi
fi
```

- [ ] **Step 2: Verify** — `sh -n scripts/install.sh` (POSIX syntax OK). Confirm `KELD_NO_SIDECAR=1` skips the block and the default path attempts the fetch.

- [ ] **Step 3: Commit** — `git add scripts/install.sh && git commit -m "build(linux): opt-in keld-agent-sidecar fetch in install.sh"`

---

### Task 6: GitHub Actions workflow (AUTHORED, CI-verified)

**Files:** Create `.github/workflows/installers.yml`.

**Interfaces:** On tag push / release: freeze the sidecar per-OS (matrix) → smoke it (`--port` + `/health`) → package (`.pkg`/Inno/tar) → gated sign → upload assets to the release. Consumes Tasks 2–5 scripts. Secrets: `APPLE_DEVELOPER_ID_APP`, `APPLE_DEVELOPER_ID_INSTALLER`, `APPLE_NOTARY_KEY`/`_KEY_ID`/`_ISSUER`, `WINDOWS_CERT_PFX`(+password). Absent → unsigned.

- [ ] **Step 1: Write `.github/workflows/installers.yml`** (concrete skeleton — matrix + smoke + gated sign + upload)

```yaml
name: installers
on:
  release:
    types: [published]
jobs:
  build:
    strategy:
      matrix:
        include:
          - os: macos-14   # arm64
          - os: macos-13   # x64
          - os: windows-latest
          - os: ubuntu-latest
    runs-on: ${{ matrix.os }}
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-python@v5
        with: { python-version: "3.12" }
      - name: Freeze sidecar
        shell: bash
        run: bash sidecar/build-freeze.sh
      - name: Smoke the frozen sidecar
        shell: bash
        run: |
          BIN=dist/keld-agent-sidecar/keld-agent-sidecar
          [ "${{ runner.os }}" = "Windows" ] && BIN="dist/keld-agent-sidecar/keld-agent-sidecar.exe"
          "$BIN" --port 8399 --host 127.0.0.1 &
          for i in $(seq 1 120); do
            curl -sf http://127.0.0.1:8399/health && break || sleep 2
          done
          curl -sf http://127.0.0.1:8399/health | grep -q '"ok":true'
      # download the GoReleaser keld/keld-agent binaries for this OS/arch into a stage dir,
      # add the frozen sidecar, then package:
      - name: Package (macOS)
        if: startsWith(matrix.os, 'macos')
        env:
          APPLE_DEVELOPER_ID_APP: ${{ secrets.APPLE_DEVELOPER_ID_APP }}
          APPLE_DEVELOPER_ID_INSTALLER: ${{ secrets.APPLE_DEVELOPER_ID_INSTALLER }}
          APPLE_NOTARY_KEY: ${{ secrets.APPLE_NOTARY_KEY }}
          APPLE_NOTARY_KEY_ID: ${{ secrets.APPLE_NOTARY_KEY_ID }}
          APPLE_NOTARY_ISSUER: ${{ secrets.APPLE_NOTARY_ISSUER }}
        run: bash installers/macos/build-pkg.sh "${{ github.event.release.tag_name }}" "$STAGE"
      - name: Package (Windows)
        if: matrix.os == 'windows-latest'
        shell: pwsh
        env:
          KELD_VERSION: ${{ github.event.release.tag_name }}
          WINDOWS_CERT_PFX: ${{ secrets.WINDOWS_CERT_PFX }}
        run: |
          iscc installers\windows\keld-agent.iss
          if ($env:WINDOWS_CERT_PFX) { <# signtool sign ... #> }
      - name: Upload assets
        uses: softprops/action-gh-release@v2
        with:
          files: |
            keld-*.pkg
            Output/keld-setup.exe
            keld-agent-sidecar_*.tar.gz
```

> IMPLEMENTER NOTE: the STAGE assembly (download GoReleaser binaries for the matrix OS/arch + copy the frozen sidecar in) and the Linux `keld-agent-sidecar_*.tar.gz` tarball step are the two glue pieces to finalize; keep the gated-signing `if` guards exactly (unsigned when secrets absent). This workflow is validated by `actionlint` (if available) here and a real CI run on the maintainer's account — not runnable in this env.

- [ ] **Step 2: Verify (authored-only)** — `actionlint .github/workflows/installers.yml` if available, else a careful read; confirm secret names match the spec. No local run.

- [ ] **Step 3: Commit** — `git add .github/workflows/installers.yml && git commit -m "ci: installers workflow (freeze/package/gated-sign/release)"`

---

## Notes for the executor
- **Task 1 is the only locally-testable task** — do it via TDD and `-race`/full-suite as usual.
- **Tasks 2–6 are authored + CI-verified.** Validate with the available linters (`bash -n`, `xmllint`, `actionlint`) and STOP; do not claim a macOS/Windows build succeeded from this Linux env. The real gate is a green `installers.yml` run on the maintainer's CI.
- **Keep signing gated** — every sign/notarize step must no-op cleanly when its secret is unset (unsigned-first).
- **Names must line up** across tasks: the freeze output dir/binary (`keld-agent-sidecar[.exe]`), the installer file placement (beside `keld-agent`), and `sidecarBinPath()` resolution (Task 1).
- After merge, P3's remaining real-world step is the maintainer supplying signing secrets + running CI; that is out of scope for this plan's local work.
