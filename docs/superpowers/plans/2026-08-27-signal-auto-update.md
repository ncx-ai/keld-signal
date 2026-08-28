# Signal Auto-Update Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The `keld-agent` daemon moves itself, the `keld` CLI and the frozen analysis sidecar to the release Atlas names — fetching, verifying, swapping, restarting, and rolling back if the new version does not come up.

**Architecture:** A new `internal/agent/update` package holds every decision and every filesystem move, with the network, the clock, the process restarter and the exec probe all injected so unhappy paths are testable on Linux CI. The daemon wires it at exactly two points: the settings poll's existing `onRemote` hook (trigger) and daemon startup (confirm/rollback). No new clock, no new poll.

**Tech Stack:** Go 1.26, `internal/retry`, `internal/agent/settings`, `internal/agent/service`, `internal/agent/clientevents`, `internal/paths`, `net/http/httptest`.

**Spec:** `docs/superpowers/specs/2026-08-27-signal-auto-update-design.md`

## Global Constraints

- **Absent Atlas key ⇒ no update.** Never "default on". Pointer fields, `Features` precedent.
- **`Version` is a PIN, not a floor.** Downgrade is first-class; comparison is string identity after normalizing a leading `v`.
- **A missing published SHA-256 is FATAL**, unlike `install.sh` which warns. No human is reading the output.
- **Staging directory lives INSIDE the destination directory** so the commit is a same-filesystem rename. Never fall back to `/tmp`.
- **`version.CLI == "dev"` never updates.**
- **Local refusal wins:** `KELD_AUTOUPDATE=0` blocks updates whatever Atlas says. Local permission never enables.
- **Displace, never delete:** every replaced file/tree moves to `<name>.prev` first; a failed swap restores from it.
- **A rolled-back version is never retried** until the Atlas pin moves (`failed_versions`).
- Env defaults: `KELD_UPDATE_MIN_INTERVAL=1h`, `KELD_UPDATE_CONFIRM_DEADLINE=15m`.
- Go tests: `go test ./...` from the repo root. No new third-party dependencies.

---

### Task 1: The Atlas `Release` block

**Files:**
- Create: `internal/agent/settings/release.go`
- Modify: `internal/agent/settings/remote.go` (add the `Release *Release` field)
- Test: `internal/agent/settings/release_test.go`

**Interfaces:**
- Produces: `settings.Release{Enabled *bool; Version *string; BaseURL *string}`; `Remote.Release *Release`; `func (r *Release) Target() (version, baseURL string, enabled bool)` — nil-receiver safe, returning `("", "", false)`.

- [ ] **Step 1: Write the failing tests**

```go
func TestAbsentReleaseBlockMeansNoUpdate(t *testing.T) {
	var r settings.Remote
	if err := json.Unmarshal([]byte(`{"include_entity_text":true}`), &r); err != nil { t.Fatal(err) }
	if r.Release != nil { t.Fatalf("absent block decoded non-nil") }
	_, _, enabled := r.Release.Target()
	if enabled { t.Fatal("a nil Release must not enable updates") }
}

func TestReleaseTargetReadsVersionAndBase(t *testing.T) {
	var r settings.Remote
	body := `{"agent_release":{"enabled":true,"version":"v0.4.2","base_url":"https://mirror/x"}}`
	if err := json.Unmarshal([]byte(body), &r); err != nil { t.Fatal(err) }
	v, base, enabled := r.Release.Target()
	if v != "v0.4.2" || base != "https://mirror/x" || !enabled {
		t.Fatalf("got %q %q %v", v, base, enabled)
	}
}

func TestReleaseDisabledExplicitly(t *testing.T) {
	var r settings.Remote
	if err := json.Unmarshal([]byte(`{"agent_release":{"enabled":false,"version":"v9"}}`), &r); err != nil { t.Fatal(err) }
	if _, _, enabled := r.Release.Target(); enabled { t.Fatal("enabled:false must disable") }
}
```

- [ ] **Step 2: Run to verify failure** — `go test ./internal/agent/settings/ -run Release -v`; expect compile failure (`Release` undefined).
- [ ] **Step 3: Implement** `release.go` with the struct, the `agent_release` JSON tag on `Remote`, and a nil-safe `Target()`.
- [ ] **Step 4: Run** — `go test ./internal/agent/settings/ -v`; expect PASS.
- [ ] **Step 5: Commit** — `feat(settings): model the org's target release`

---

### Task 2: Update state (`state.json`)

**Files:**
- Create: `internal/agent/update/state.go`
- Modify: `internal/paths/paths.go` (add `UpdateDir()` and `UpdateStatePath()`)
- Test: `internal/agent/update/state_test.go`

**Interfaces:**
- Produces: `type State struct{ From, To, InstallDir string; Prev []string; PendingConfirm bool; AttemptedAt time.Time; FailedVersions []string; LastAttempt time.Time; LastOutcome, LastError string }`; `func LoadState(path string) (State, error)`; `func SaveState(path string, s State) error`; `func (s State) HasFailed(v string) bool`; `func (s *State) MarkFailed(v string)`.
- `LoadState` on a missing file returns a zero `State` and a nil error. On a **corrupt** file it returns a zero `State` and a nil error too, having renamed the bad file aside — a corrupt marker must not wedge the daemon, and a zero state is the safe reading ("no update in flight").

- [ ] **Step 1: Write the failing tests** — missing file ⇒ zero state, nil error; round-trip; corrupt JSON ⇒ zero state, nil error, `state.json.bad` exists; `MarkFailed` dedups; file mode is 0600; save is atomic (temp + rename).
- [ ] **Step 2: Run** — expect FAIL (package does not exist).
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** — `go test ./internal/agent/update/ -v`; PASS.
- [ ] **Step 5: Commit** — `feat(update): persist update state, and never let a corrupt marker wedge the daemon`

---

### Task 3: The decision

**Files:**
- Create: `internal/agent/update/decide.go`
- Test: `internal/agent/update/decide_test.go`

**Interfaces:**
- Produces: `type Target struct{ Version, BaseURL string; Enabled bool }`; `type Decision struct{ Act bool; Version string; Reason string }`; `func Decide(cur string, t Target, s State, now time.Time, envDisabled bool, minInterval time.Duration) Decision`.
- Reason codes, exact strings: `dev_build`, `disabled`, `disabled_local`, `no_target`, `up_to_date`, `failed_previously`, `too_soon`, `ok`.
- `func NormalizeVersion(v string) string` — strips one leading `v`, trims space.

- [ ] **Step 1: Write the failing tests** — one per reason code, plus: a downgrade pin returns `Act=true` (a pin is not a floor); `v0.4.2` and `0.4.2` compare equal; `too_soon` is measured from `LastAttempt`; a successful *confirm* does not count as an attempt for the floor.
- [ ] **Step 2: Run** — FAIL.
- [ ] **Step 3: Implement** as a pure function — no clock, no env reads inside; both are parameters.
- [ ] **Step 4: Run** — PASS.
- [ ] **Step 5: Commit** — `feat(update): decide, as a pure function, whether to move`

---

### Task 4: Fetch and verify

**Files:**
- Create: `internal/agent/update/fetch.go`
- Test: `internal/agent/update/fetch_test.go`

**Interfaces:**
- Produces: `type Fetcher struct{ HTTP *http.Client; BaseURL string }`; `func (f *Fetcher) Fetch(ctx context.Context, tag, asset, dest string) error`; `func ParseChecksums(r io.Reader) map[string]string`.
- `Fetch` downloads `<BaseURL>/<tag>/<asset>` to `dest`, resolves the expected hash from `<BaseURL>/<tag>/checksums.txt` then `<BaseURL>/<tag>/<asset>.sha256`, and returns an error if **no** hash was published or the hash does not match. Retries via `retry.Do` with `retry.HTTPStatus(code)` for non-2xx so `IsTransient` classifies.

- [ ] **Step 1: Write the failing tests** against `httptest.NewServer`: happy path · checksum mismatch (error mentions both hashes, dest removed) · truncated body (Content-Length longer than body) · no hash published anywhere ⇒ error · `checksums.txt` 404 but `.sha256` present ⇒ success · malformed `checksums.txt` lines ignored · asset 404 ⇒ error, no retry storm · 500 twice then 200 ⇒ success · 500 always ⇒ error · server hangs, ctx deadline ⇒ error · `*` prefix on a checksums filename is stripped.
- [ ] **Step 2: Run** — FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** — PASS.
- [ ] **Step 5: Commit** — `feat(update): fetch release assets, refusing an unhashed one`

---

### Task 5: Destinations, writability, staging

**Files:**
- Create: `internal/agent/update/dest.go`
- Test: `internal/agent/update/dest_test.go`

**Interfaces:**
- Produces: `type Dest struct{ BinDir, SidecarDir string; SidecarNested bool; Writable bool }`; `func Writable(dir string) bool`; `func StageDir(dir string) (string, error)`; `func DetectSidecarLayout(dir string) (nested bool, ok bool)`.
- `StageDir` creates `<dir>/.keld-update.XXXXXX` and returns it; it never falls back to another filesystem.

- [ ] **Step 1: Write the failing tests** — `Writable` true on `t.TempDir()`, false on a `0555` dir (skip if running as root: `os.Geteuid() == 0`) · `StageDir` creates inside the destination and errors on an unwritable one · `DetectSidecarLayout` recognizes both flat and nested trees.
- [ ] **Step 2: Run** — FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** — PASS.
- [ ] **Step 5: Commit** — `feat(update): resolve destinations and stage inside them`

---

### Task 6: The swap, with restore

**Files:**
- Create: `internal/agent/update/swap.go`
- Test: `internal/agent/update/swap_test.go`

**Interfaces:**
- Produces: `type Swap struct{ moves []move; prev []string }`; `func NewSwap() *Swap`; `func (s *Swap) Replace(target, staged string) error`; `func (s *Swap) Prev() []string`; `func (s *Swap) Rollback() error`; `func (s *Swap) Commit()`.
- `Replace` renames an existing `target` to `target.prev` (removing a stale `.prev` first), then renames `staged` into `target`. `Rollback` undoes every completed move in reverse. `Commit` deletes the `.prev` copies.
- `func ExtractTarGz(archive, destDir string) error` — extracts with path traversal refused (`..` or absolute entries error out).

- [ ] **Step 1: Write the failing tests** — replace a regular file · replace a directory tree · replace a **symlink** target with a real file (the macOS shape) · target absent ⇒ no `.prev`, still installs · rename into a read-only dir fails and `Rollback` restores every prior move · `Rollback` when a `.prev` is missing reports an error rather than silently succeeding · `Commit` removes `.prev` · `ExtractTarGz` refuses `../escape` and absolute entries · extraction of a truncated archive errors.
- [ ] **Step 2: Run** — FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** — PASS.
- [ ] **Step 5: Commit** — `feat(update): swap by displacement, restoring on any failure`

---

### Task 7: macOS migration and stale-symlink reporting

**Files:**
- Create: `internal/agent/update/migrate.go`
- Test: `internal/agent/update/migrate_test.go`

**Interfaces:**
- Produces: `func MigrationTarget(home string) string` — `<home>/.local/bin`; `type StaleLink struct{ Path, Points, Want string }`; `func StaleLinks(roots []string, want string) []StaleLink` — reports any `keld`/`keld-agent` on a well-known PATH dir that is a symlink pointing somewhere other than `want`, so `doctor` can print an exact `ln -sf` fix.
- Produces: `type PlistRewriter func(execPath string) error` — injected; the darwin implementation calls `service.Install()` after `os.Executable()` has been superseded, the test implementation records the path.

- [ ] **Step 1: Write the failing tests** — a root-owned-shaped symlink at `<home>/.local/bin/keld` is reported as stale when it points elsewhere · a real file is never reported · a correct symlink is not reported · `StaleLinks` on a missing dir returns nothing rather than erroring.
- [ ] **Step 2: Run** — FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** — PASS.
- [ ] **Step 5: Commit** — `feat(update): migrate off an unwritable install, and name the link we cannot fix`

---

### Task 8: The updater orchestrator

**Files:**
- Create: `internal/agent/update/update.go`
- Test: `internal/agent/update/update_test.go`

**Interfaces:**
- Produces:

```go
type Restarter interface{ Restart() error }
type Emitter interface{ Emit(code string, sev string, fields map[string]any) }

type Updater struct {
    Current   string
    StatePath string
    Dest      Dest
    Fetch     *Fetcher
    Restart   Restarter
    Probe     func(bin string) error   // runs <bin> --version
    Now       func() time.Time
    Emit      func(code, sev string, fields map[string]any)
    Quiesce   func(ctx context.Context) // waits for the queue to drain
    MinInterval time.Duration
    OS, Arch  string
}

func (u *Updater) Maybe(ctx context.Context, t Target)  // single-flight, non-blocking decision
func (u *Updater) apply(ctx context.Context, version string) error
```

- `Maybe` is safe to call from the settings poll on every tick: it takes a `sync.Mutex`-guarded "in flight" flag and returns immediately if one is running.

- [ ] **Step 1: Write the failing tests** — end-to-end against `httptest` with synthesized tarballs: happy apply writes `state.json` with `PendingConfirm` and calls `Restart` once · a failing pre-flight probe aborts **before** any swap (destination unchanged) · a fetch failure leaves the destination untouched and records `LastOutcome="failed"` · two concurrent `Maybe` calls run one apply · a skip emits `update.skipped` with the reason and does not touch disk · `Restart` returning an error is recorded and the state still says pending (so the next start rolls back) · `Quiesce` is called before `Restart`, never after.
- [ ] **Step 2: Run** — FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** — PASS.
- [ ] **Step 5: Commit** — `feat(update): the orchestrator — probe, swap, restart, record`

---

### Task 9: Confirm and roll back at startup

**Files:**
- Create: `internal/agent/update/confirm.go`
- Test: `internal/agent/update/confirm_test.go`

**Interfaces:**
- Produces: `func Confirm(statePath, current string, now time.Time, deadline time.Duration, restart Restarter, emit func(code, sev string, fields map[string]any)) error`.
- Behaviour: no pending marker ⇒ no-op. Pending and `current == To` ⇒ clear marker, delete `.prev`, emit `update.applied`. Pending and `current == From` ⇒ restore `.prev`, `MarkFailed(To)`, emit `update.rolled_back`, `Restart()`. Pending and `now - AttemptedAt > deadline` ⇒ same rollback path regardless of which version is running.

- [ ] **Step 1: Write the failing tests** — happy confirm removes `.prev` and clears the marker · wrong-version rollback restores the previous bytes and records the failed version · stale-marker rollback fires even when `current == To` (the new binary came up but never got healthy) · a rollback whose `.prev` is missing does **not** restart and reports loudly · `failed_versions` prevents a re-apply of that version (assert via `Decide`) · a corrupt `state.json` is a no-op.
- [ ] **Step 2: Run** — FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** — PASS.
- [ ] **Step 5: Commit** — `feat(update): confirm the new version came up, or put the old one back`

---

### Task 10: Daemon wiring

**Files:**
- Create: `internal/agent/daemon/update.go`
- Modify: `internal/agent/daemon/daemon.go` (call `update.Confirm` early in `Run`; build the updater; call `u.Maybe` from the settings poll's `onRemote`)
- Test: `internal/agent/daemon/update_test.go`

**Interfaces:**
- Produces: `func newUpdater(emit func(string, string, map[string]any)) (*update.Updater, bool)`; `func updateTargetFrom(r settings.Remote) update.Target`.

- [ ] **Step 1: Write the failing tests** — `updateTargetFrom` maps the remote block and yields `Enabled=false` for a nil block · `KELD_AUTOUPDATE=0` yields a non-acting decision · the confirm pass runs against `KELD_HOME` and is a no-op with no marker. Use `t.Setenv("KELD_HOME", t.TempDir())` — no test may touch the developer's real `~/.keld` (the `teleproxy` lesson).
- [ ] **Step 2: Run** — FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** — `go test ./internal/agent/daemon/ -v`; PASS.
- [ ] **Step 5: Commit** — `feat(daemon): wire auto-update to the settings poll and to startup`

---

### Task 11: Reporting and docs

**Files:**
- Create: `internal/localagent/update.go`
- Modify: `docs/signal-client-events.md`, `AGENTS.md`, `CLAUDE.md` if a rule changes
- Test: `internal/localagent/update_test.go`

**Interfaces:**
- Produces: `type UpdateState struct{ Current, Target, LastOutcome, LastError string; Pending bool; Stale []update.StaleLink }`; `func ReadUpdateState() UpdateState`; `func (u UpdateState) StatusLine() string`; `func (u UpdateState) ProblemLine() string` (empty when there is no problem).
- `ReadUpdateState` reads `state.json` from disk only. It never contacts the daemon and never triggers an update — the rule `localagent/models.go` already follows.

- [ ] **Step 1: Write the failing tests** — a clean state produces no problem line · a `rolled_back` outcome produces a problem line naming the version · a stale symlink produces a problem line containing the exact `ln -sf` command · a missing state file is not a problem.
- [ ] **Step 2: Run** — FAIL.
- [ ] **Step 3: Implement**, and wire the lines into `keld signal status` / `doctor`.
- [ ] **Step 4: Run** — `go test ./...`; PASS.
- [ ] **Step 5: Commit** — `feat(cli): report update state from disk, never by probing`

---

## Self-Review

**Spec coverage:** §1 trigger → Task 10. §2 Atlas contract → Task 1. §3 decision → Task 3. §4 fetch/verify → Task 4. §5 swap and destinations → Tasks 5, 6. §5b macOS → Task 7. §6 confirm/rollback → Tasks 2, 8, 9. §7 reporting → Tasks 8, 11. §8 testing → distributed across every task's Step 1.

**Placeholders:** none — every task names exact files, exact identifiers and exact reason-code strings.

**Type consistency:** `Target`, `Decision`, `State`, `Dest`, `Swap`, `Updater`, `StaleLink` are each defined in exactly one task and referenced by the same name and field set afterwards.
