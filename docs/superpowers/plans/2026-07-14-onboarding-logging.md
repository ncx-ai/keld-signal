# Onboarding logging cleanup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:test-driven-development. Single task.

**Goal:** Unify the `curl … | sh` install / onboarding console output into one consistent, phased, `✓`-item format (user-approved mockup below). Human output only — the `--json` NDJSON contract is unchanged.

**Approved target output** (the whole `curl … /signal/install.sh | sh` run):

```
Keld · installing  (linux/amd64, v0.3.4)

Downloading…
  ✓ keld + keld-agent          → ~/.local/bin
  ✓ ML sidecar (GLiNER2)       → ~/.local/bin/keld-agent-sidecar

Signing in…
  Approve this device in your browser (code pre-filled):
    http://localhost:3000/cli/signal?code=6V6P-BTMK
  ✓ admin@acme.test · org Acme

Configuring your AI tools…
  ✓ Claude Code                already configured
  ✓ Codex                      already configured
  ✓ Gemini CLI                 already configured
  ✓ Hook                       ~/.keld/hook.json

Starting the agent…
  ✓ keld-agent running — enrichment stays on-device; only masked signal is sent

Done — Keld is set up and running.
```

## Global Constraints
- **`--json` / NDJSON output MUST NOT change.** Only edit the human branches (`opts.Emit == nil`, `jsonOut == false`, the `quiet == false` paths). The `emitEvent(...)` / `SetupOpts.Emit` / `deviceCodeEvent`/`authorizedEvent`/`toolEvent`/`doneEvent` payloads stay byte-identical. Installer UIs depend on them.
- Format rules: phase headers are lowercase gerunds ending `…`, no trailing blank noise; detail lines indent 2 spaces and lead with `✓` (done) / `⚠` (non-fatal) / `✗` (failed, + a "fix:" line); no box-drawing rules (`console.Rule`) anywhere in this flow; tool/label column left-aligned (pad to a fixed width, e.g. 26 cols); conditionals — PATH guidance only when `DEST` not on `PATH`, Gatekeeper note only when `os = darwin`.
- ML is mandatory: never print a "deterministic" or "skip sidecar" line (already removed from install.sh).
- Commit trailer: `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- Test: `go test ./...` ; `go build ./...` ; `gofmt -l` the touched files.

## Ownership (which file prints which lines)
Each command self-headers so both standalone and orchestrated (`keld-agent install`) flows read cleanly.

1. **`scripts/install.sh`** — banner + Downloading + final:
   - Replace `Installing keld ${tag} (${os}/${arch})...` + the `  Source:`/`  Destination:` lines with a single banner: `Keld · installing  (${os}/${arch}, ${tag})` then a blank line.
   - After the keld/keld-agent extract: `Downloading…` (once) then `  ✓ keld + keld-agent          → ${DEST}`.
   - After the sidecar extract succeeds: `  ✓ ML sidecar (GLiNER2)       → ${DEST}/keld-agent-sidecar` (drop the old `keld-agent-sidecar installed to …` line).
   - Drop the final duplicate `keld ${tag} installed to …` / `keld-agent installed to …` block.
   - Keep PATH guidance but only emit it when `${DEST}` is not already on `$PATH` (`case ":$PATH:" in *":${DEST}:"*) ;; * ) …print… ;; esac`).
   - Gatekeeper note only when `[ "$os" = "darwin" ]`.
   - Last line: `Done — Keld is set up and running.`
2. **`internal/cli/login.go`** — human sign-in (both the `--code` branch ~line 44 and the interactive branch ~line 77):
   - Before the auth call, print header `Signing in…` (human mode only, not under `jsonOut`).
   - Replace `Logged in as %s (org: %s)` → `  ✓ %s · org %s` (principal, org). Both branches.
3. **`internal/auth/device.go`** — the interactive device-start human print (~line 98) and `(Opening your browser…)` (~line 30):
   - Replace the `To authorize this device, open:\n  %s\nThe code %s is already filled in — confirm it matches, then approve.` block with:
     `  Approve this device in your browser (code pre-filled):\n    %s` (URL only; drop the separate "code is filled in" sentence — it's implied by "code pre-filled").
   - Change `(Opening your browser…)` → `  Opening your browser…` (2-space indent to match). Keep it human-only.
4. **`internal/cli/setup.go`** (`runSetup`, human `say`/`console.Rule` paths only):
   - Remove the `console.Rule(fmt.Sprintf("%s · %s", adapter.DisplayName(), path))` per-tool box rule.
   - Print header `Configuring your AI tools…` once at the start (human mode).
   - Per tool, a single left-aligned line: configured → `  ✓ %-26s configured` (+ ` (backed up %s)` when a backup was made); unchanged → `  ✓ %-26s already configured`; skipped conflict → `  ⚠ %-26s skipped (conflict)`. Use the adapter DisplayName in the column.
   - Hook: `  ✓ %-26s %s` with label `Hook` and the `~/.keld/hook.json` path (shown once, after tools).
   - Drop `Nothing to apply.` and `Setup complete. Restart any running sessions…` and the `Hook · keld __hook (writes …)` line — folded into the `✓ Hook` line. (When zero tools changed but all already-configured, the per-tool `already configured` lines already convey state; no separate summary.)
5. **`internal/agentcli/agentcli.go`** (`runInstall`, the code + TTY branches — human path):
   - After `service.Install()` succeeds, print `Starting the agent…` **before** calling it, then on success `  ✓ keld-agent running — enrichment stays on-device; only masked signal is sent`. (Print the header before `installService()` so ordering matches; on error, existing behavior.)
   - Do NOT print phase headers for login/setup here — those commands self-header (items 2 & 4). The subprocess stdio is inherited, so their output appears inline.

## Task: implement the unified format

- [ ] **Step 1 (tests first):** update the assertions that pin the OLD human strings to the NEW format, and add/adjust a couple that lock the new lines. Grep first: `grep -rn "Logged in as\|To authorize\|already configured\|Nothing to apply\|Opening your browser\|Setup complete" internal/ ` — update matching `*_test.go` (`login_test.go`, `setup_test.go`, `device_test.go`, `agentcli_test.go`). Do NOT touch tests asserting `--json`/event payloads. Assert: login prints `✓ <principal> · org <org>`; setup prints `✓` per tool + `✓ Hook`; setup no longer prints a box rule or `Nothing to apply.`; agentcli prints `Starting the agent…`.
- [ ] **Step 2:** run the updated tests → they FAIL (old strings still emitted).
- [ ] **Step 3:** implement items 1–5 above.
- [ ] **Step 4:** `go test ./...` green; `go build ./...`; `gofmt -l internal/ | grep -v vendor` clean for touched files. Then manually eyeball `keld signal setup --dry-run` output (human) to confirm the new tool lines render (no box, aligned ✓).
- [ ] **Step 5:** commit `feat(onboarding): unified, phased install/onboarding console output`.

## Self-Review
- Coverage: banner+download+PATH/Gatekeeper (install.sh) ✓; Signing in + ✓ principal·org (login.go/device.go) ✓; Configuring + per-tool ✓ + Hook (setup.go) ✓; Starting the agent + ✓ running (agentcli) ✓; Done (install.sh) ✓.
- `--json` untouched: only human branches edited; event payload structs + `emitEvent`/`Emit` calls unchanged.
- No box rules; conditionals (PATH/Gatekeeper) honored; ML-only wording.
