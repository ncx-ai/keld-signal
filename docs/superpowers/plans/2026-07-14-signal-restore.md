# `keld signal restore` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`).

**Goal:** `keld signal restore [tool...]` rolls a tool's config back to keld's pristine pre-keld backup (or, if keld created the file fresh and there's no backup, surgically strips keld's hooks + OTEL endpoint) so the tool stops sending telemetry to keld — for all keld-touched tools, or named ones.

**Design (approved):**
- Distinct from `uninstall` (surgical strip, keeps the file/later edits): `restore` with a backup does a **full rollback** to the pristine copy `config.BackupConfig` recorded (in `manifest.Tools[tool].BackupPath`). `BackupConfig` preserves the FIRST (pre-keld) backup and never re-backs-up, so a recorded backup is guaranteed keld-free.
- Per tool:
  - `BackupPath != nil` and that file exists → copy backup → `ConfigPath` (overwrite). This inherently removes keld's OTEL endpoint (pre-keld state).
  - else (keld created the file fresh, no backup) → apply `adapter.Remove(current, Managed)` and write/delete exactly like `uninstall`'s created/non-created handling (strips keld's env vars incl. `OTEL_EXPORTER_OTLP_ENDPOINT` + hooks, deleting the file if keld created it and it's now empty).
  - **Remove the tool from the manifest** (both cases) and save it — so `status`/`doctor` show keld as no-longer-configured there.
- Destructive (overwrites the current config): confirm by default; `--yes` skips; `--dry-run` previews (prints per-tool the action — "restore from <backup>" / "strip keld config (no backup)" — writing nothing).

## Global Constraints
- Reuse `uninstall`'s per-tool `Remove` logic for the no-backup branch — factor a shared helper, don't duplicate. (`internal/cli/uninstall.go` `runUninstall`'s loop body.)
- Reuse `config.BackupConfig`/`paths.BackupsDir`/`config.LoadManifest`/`Manifest.Save`; add a `config.RestoreBackup(backupPath, configPath) error` (atomic copy) if none exists.
- No `--json`/schema changes. Commit trailer: `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`. Test: `go build/test/vet ./...`; `gofmt -l`.

## Task 1: shared removal helper + `keld signal restore`
**Files:** create `internal/cli/restore.go` (+ `restore_test.go`); modify `internal/cli/uninstall.go` (extract the shared helper), `internal/cli/root.go` (register), `internal/config/writer.go` (add `RestoreBackup` if needed).

- [ ] **Step 1 (test first):** in `restore_test.go`, with `t.Setenv("KELD_HOME", t.TempDir())`: (a) a tool with a backup → `runRestore` copies the backup content over the config file and removes the tool from the manifest; (b) a tool with NO backup (BackupPath nil) whose config keld created → `runRestore` strips keld's block via the adapter (config no longer contains the keld OTLP endpoint / hook) and removes it from the manifest; (c) `--dry-run` writes nothing and leaves the manifest unchanged; (d) named-tool filtering restores only the named tool. Use a real adapter (e.g. `claude_code`) with a crafted managed manifest + config file, mirroring `uninstall_test.go`'s setup.
- [ ] **Step 2: run → fail** (`runRestore`/`newRestoreCmd` absent).
- [ ] **Step 3: implement.**
  - Extract from `runUninstall` a helper `stripToolConfig(m *config.Manifest, name string, tm config.ToolManifest, adapter tools.Adapter) error` that does the current per-tool Remove/write/delete-if-created + `.keld.bak` cleanup + `delete(m.Tools, name)`. Have `runUninstall` call it (behavior unchanged — verify `uninstall_test.go` still passes).
  - `config.RestoreBackup(backupPath, configPath string) error`: atomically copy `backupPath` → `configPath` (reuse `copyFile`/`WriteAtomic`); error if the backup is missing.
  - `func runRestore(m *config.Manifest, names []string, yes, dryRun bool, confirm func(string) bool) error`: collect targets (all `m.Tools`, filtered by `names`); if none → "Nothing to restore."; confirm (unless `yes`/`dryRun`) with a destructive-sounding prompt listing targets. Per target: resolve adapter (`tools.Get`; unknown → still process the backup path / drop from manifest). If `tm.BackupPath != nil` and the file exists: dry-run → log "would restore <tool> from <backup>"; else `config.RestoreBackup(*tm.BackupPath, tm.ConfigPath)` + `delete(m.Tools, name)` + log `✓ <Display> restored from backup`. Else (no backup): dry-run → log "would strip keld config from <tool> (no backup)"; else `stripToolConfig(...)` + log `✓ <Display> keld config removed (no backup to restore)`. After the loop (not dry-run): `m.Save()`.
  - `newRestoreCmd`: `Use: "restore [tool...]"`, `Short: "Restore tool configs from keld's pre-setup backups (or strip keld's config where none exists)."`, flags `--yes`/`-y` and `--dry-run`; `RunE` loads the manifest, calls `runRestore(m, args, yes, dryRun, stdinConfirm)`.
  - Register in `root.go`: `signal.AddCommand(newRestoreCmd())`.
- [ ] **Step 4: run → pass** + `go build/vet ./...` + `go test ./...` (incl. `uninstall_test.go` still green) + `gofmt -l` clean.
- [ ] **Step 5: commit** `feat(cli): keld signal restore — roll tool configs back to pre-keld backups`.

## Self-Review
- Backup case = full pristine rollback (keld endpoint gone); no-backup case = adapter.Remove (endpoint+hooks stripped) — both satisfy "stop sending to keld" + update the manifest.
- Shared `stripToolConfig` reused by uninstall (no duplication; uninstall behavior unchanged).
- `--dry-run` writes nothing; confirm guards the destructive overwrite.
