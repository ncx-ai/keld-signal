# keld-agent P3 — GUI installers (unsigned-first, signing gated) — design

**Date:** 2026-07-01
**Status:** Design approved (brainstorm), pre-spec-review
**Branch:** `feat/keld-agent-p3-installers` (off `main`)
**Parent design:** `docs/superpowers/specs/2026-06-30-keld-agent-enrichment-daemon-design.md` (§9 Distribution, §12 P3)
**Builds on:** P1 (service install, `scripts/install.{sh,ps1}`, GoReleaser), P2 (sidecar backend + `sidecarBinPath()` seam).

## 1. Summary

P3 ships keld-agent as native GUI installers — the standard, likely-enforced
install path. **Sequencing: unsigned-first, signing gated** — author the per-OS
sidecar freeze + installers + CI so everything builds **unsigned** (testable
without secrets), then layer Developer-ID/notarization (macOS) and Authenticode
(Windows) as a final step gated on credentials the maintainer provides.

**Reconciliation with P2:** the parent spec §9 payload is stale — it predates the
2b spike decision (in-process ONNX → **sidecar**). There is **no `libonnxruntime`
and no bundled model**. The installed payload is `keld` + `keld-agent` +
`keld-agent-sidecar` (frozen) + the service definition; the ~1.9 GB model is
fetched on **first run** (P2b provisioning), not bundled.

## 2. Payload (per OS)

- `keld` (Go CLI, on PATH) — already built by GoReleaser.
- `keld-agent` (Go daemon, on PATH) — already built by GoReleaser.
- `keld-agent-sidecar` (frozen Python+torch+gliner2, from T11) — new, per-OS.
- Per-user service definition (LaunchAgent / logon task / systemd --user) —
  already implemented in P1 (`internal/agent/service`).

The model is **not** in the payload (first-run download with progress, P2b).

## 3. Sidecar freeze (T11)

- **PyInstaller** builds `keld-agent-sidecar` from `sidecar/serve.py` per OS
  (bundles CPython + torch + gliner2 + FastAPI/uvicorn). One-file or one-dir;
  one-dir is safer for torch's data files (decide during implementation).
- Built in a **GitHub Actions matrix** (macOS arm64 + x64, Windows x64, Linux
  x64) — cannot be produced/tested in the Linux dev env; authored here, run in CI.
- Expected size: hundreds of MB per OS (torch dominates) — documented; this is
  the accepted cost of the sidecar decision (P2 decision doc).
- The daemon resolves the installed binary via `sidecarBinPath()` (P2b): P3
  makes it resolve the per-OS installed location (next to `keld-agent`), keeping
  the `KELD_SIDECAR_BIN` env override for dev.

## 4. macOS — notarized `.pkg`

- `pkgbuild` (component) + `productbuild` (distribution) producing a `.pkg` with
  a native GUI. Payload placed on PATH (e.g. `/usr/local/bin` for `keld`,
  `keld-agent`; sidecar in a support dir resolved by `sidecarBinPath()`).
- **postinstall**: register the LaunchAgent via `launchctl bootstrap gui/$UID`
  (per-user), consistent with P1's darwin service. Does **not** perform login
  (interactive) — keld-agent's own first-run flow handles login/config/model.
- **Signing (gated)**: `codesign` the binaries (Developer ID Application,
  hardened runtime), `productsign` the `.pkg` (Developer ID Installer),
  `notarytool` submit + `stapler staple`. Required to clear Gatekeeper.

## 5. Windows — Inno Setup `.exe`

- Inno Setup script (`.iss`) producing a per-user installer (install to
  `%LOCALAPPDATA%\Programs\keld`), a clean GUI wizard, PATH update.
- Registers the **logon task** (consistent with P1's `schtasks` windows
  service). No login in the installer.
- **Signing (gated)**: Authenticode-sign `keld-agent.exe`,
  `keld-agent-sidecar.exe`, and the installer `.exe` to clear SmartScreen.

## 6. Linux — `curl | sh`

- Extend the existing `scripts/install.sh` to also fetch + place
  `keld-agent-sidecar` beside `keld`/`keld-agent` **by default** (matching the
  GUI installers), then `systemctl --user enable --now keld-agent` (P1 already
  does the service). `KELD_NO_SIDECAR=1` opts out (lean deterministic-only).
  `curl | sh` remains the power-user path (no GUI).
- `.deb` / `.rpm` via **nfpm**: deferred (parent spec §9 "later").

## 7. Build / CI

- GoReleaser continues to build the Go binaries + archives (unchanged).
- A **GitHub Actions workflow** orchestrates the rest:
  1. **freeze** (matrix, per OS): PyInstaller → `keld-agent-sidecar`.
  2. **package** (per OS): assemble payload → `.pkg` (macOS) / Inno `.exe`
     (Windows) / tarball + `install.sh` (Linux).
  3. **sign/notarize** (gated on secrets): skipped when secrets absent →
     unsigned artifacts; runs when the maintainer provides certs.
  4. **release**: attach installers to the GoReleaser GitHub release.
- Secrets enumerated (set in CI, not committed): `APPLE_DEVELOPER_ID_APP`,
  `APPLE_DEVELOPER_ID_INSTALLER`, `APPLE_NOTARY_KEY`/`_ISSUER`/`_KEY_ID`,
  `WINDOWS_CERT_PFX` + password. Absent → unsigned build (the default path now).

## 8. Authored-here vs CI-only

- **Authored + committed here:** PyInstaller spec, `.pkg` scripts
  (pkgbuild/productbuild + postinstall), Inno `.iss`, `install.sh` changes, the
  GHA workflow, `sidecarBinPath()` per-OS resolution + a Go test for it, docs.
- **Runs only in CI (not this Linux env):** the actual per-OS freezes, `.pkg`
  and Inno builds, and all signing/notarization. This spec does not claim those
  are verified locally — they are verified by a green CI run on the maintainer's
  runners.

## 9. Scope / non-goals

- **In scope:** sidecar freeze spec, three installer definitions (unsigned-first),
  GHA workflow with gated signing, `install.sh` sidecar wiring,
  `sidecarBinPath()` install-location resolution.
- **Non-goals:** custom GUI application (native installer wizards suffice);
  auto-update; `.deb`/`.rpm` (nfpm, later); bundling the model; performing login
  from the installer; any change to the enrichment runtime (P1/P2 frozen).

## 10. Open risks

1. **PyInstaller + torch is large and per-OS fragile** (hidden imports, data
   files). Mitigation: one-dir mode; a CI smoke step that runs the frozen
   `keld-agent-sidecar --port N` and hits `/health` before packaging.
2. **Cannot test installers in the dev env** — all multi-OS/signing validation is
   CI-only. Mitigation: keep installer logic thin; put behavior in the tested Go
   daemon; a post-install CI smoke (install → service up → `/health`).
3. **Notarization latency/flakiness** — gated + ret/staple with retry.
4. **Signing credentials are the maintainer's** — unsigned-first keeps the whole
   pipeline unblocked until they are provided.
