# Non-CLI prompt capture via an on-device transcript watcher

**Status:** Design — approved approach, pending spec review
**Date:** 2026-07-21
**Author:** dg + Claude

## Goal

A person using **Claude Code** (any launch surface — terminal CLI, the Desktop
app, VS Code, JetBrains) or **Cowork** in a normal way should have their prompts
arrive in Atlas as enrichments, exactly like the terminal-CLI path does today —
**without** requiring the `keld __hook` command hook, which only some surfaces
fire.

## Background: how capture works today, and why it's insufficient

Signal captures prompts through **one** mechanism: a command hook. `keld setup`
writes `keld __hook --source <tool>` into a tool's config (e.g.
`~/.claude/settings.json`). On `UserPromptSubmit` the hook POSTs a **pointer**
(`transcript_path` + `prompt_id`) to the daemon's `/enrich`; the daemon reads the
prompt text locally from the transcript (`internal/agent/resolve/`), enriches it
on GLiNER2, masks sensitive spans, and publishes only the derived `Profile` to
Atlas. Raw prompt text never leaves the machine.

The hook is only a **trigger**. Everything downstream — resolve → enrich → mask →
publish — is transcript-driven and surface-agnostic. The problem is that the
trigger doesn't fire everywhere:

- **Cowork does not fire `~/.claude/settings.json` hooks** and won't — Anthropic
  issue [#63360](https://github.com/anthropics/claude-code/issues/63360) is closed
  *not-planned*. Cowork runs its agent in a Linux sandbox while the hook config
  lives on the host, so the hook model is structurally unavailable.
- **Claude Code inside the Desktop app** has reported cases (esp. Windows) where
  `settings.json` hooks silently don't fire, so relying on the hook alone is
  fragile even for Code.

### Ground-truth findings (verified on a real macOS machine, 2026-07-21)

- **Claude Code (all surfaces) writes JSONL transcripts to `~/.claude/projects/**/*.jsonl`.**
  The Desktop app bundles Claude Code (`…/Application Support/Claude/claude-code/<ver>/`)
  and writes to the *same* path. 425 transcript files were present, one updated
  live during this session.
- **Cowork is Claude Code in a sandbox and writes a standard Claude Code
  transcript to disk.** A live Cowork test prompt produced
  `…/local-agent-mode-sessions/<a>/<b>/local_<uuid>/.claude/projects/<encoded>/<sessionuuid>.jsonl`
  whose user-record schema is **byte-identical** to the canonical Claude Code
  transcript (`type:"user"`, `promptId`, `message.content`, `cwd`, `sessionId`,
  `gitBranch`, `entrypoint`, `origin`, `promptSource`). The existing
  `resolve.ClaudeReader` parses it verbatim.
- A sibling `local_<uuid>/audit.jsonl` also exists (plaintext; `.audit-key` is an
  **HMAC integrity key, not encryption**), but the nested `.claude/projects`
  transcript is the cleaner source and needs no new parser.
- **Plain Desktop chat** (not Code, not Cowork) has **no** reliable local
  transcript — conversations live in a server-synced IndexedDB LevelDB store. It
  is **out of scope** for on-device capture (a future Atlas-side / Anthropic-API
  or OTLP integration).
- Confirmation of approach: `codeburn` (getagentseal/codeburn) reads exactly these
  on-disk session files locally, no proxy — the community-standard method.

### Alternative considered and deferred: OTLP/OpenTelemetry

Cowork/Desktop *can* stream events — including full prompt text with
`OTEL_LOG_USER_PROMPTS=1` — to a custom **OTLP endpoint** (Organization settings
→ Cowork). Rejected as the primary path because it is Team/Enterprise-only,
admin-configured, adds a new OTLP-server subsystem, and *egresses raw prompt
text* to the collector (the privacy invariant only holds if the collector is
strictly local). Documented here as the future enterprise/centralized option; not
built now.

## Approach

Add a second capture **trigger** — an on-device **transcript watcher** in the
daemon — that tails the JSONL transcript roots, and for each new genuine
user-prompt record synthesizes the *same* `spool.Pointer` the hook produces and
offers it to the *existing* queue. Nothing downstream of the queue changes.

This preserves every invariant: text is read locally, enriched locally, masked,
and only the derived `Profile` is published. It covers Claude Code on every
surface and Cowork, on any plan, with no admin/enterprise setup.

### Design decisions (settled)

| Decision | Choice |
|---|---|
| Backfill on first start | **Forward-only** — cursors initialized at EOF for pre-existing transcripts; only new prompts are enriched. Bounded backfill is a possible later opt-in. |
| Cowork source label | **Distinct `cowork`** (Origin `watch`). Atlas distinguishes Code vs Cowork; Cowork skips the coding-only function rule. No dedup collision (Cowork has no hook). |
| Default state | **On by default** when the daemon runs (`ml_backend != off`); env/setting to disable. |
| Watch mechanism | **Periodic poll** (stat mtime/size, read from cursor) — not fsnotify. Simpler, rotation-robust; enrichment is async so sub-second latency is unnecessary. |
| Hook overlap | Watcher covers all roots (incl. canonical, for in-Desktop Code where the hook may not fire). Overlap closed by extending the queue dedup with a bounded recently-*completed* ring buffer — both paths share the daemon queue, so no cross-process ledger is needed. |

## Architecture

```
                      ┌───────────────────────── daemon ─────────────────────────┐
transcript .jsonl ──▶ │  watcher (poll loop)                                      │
  (append)            │    ├─ roots discovery (per-OS)                            │
                      │    ├─ per-file cursor (persisted ~/.keld/watch/)          │
                      │    ├─ parse new lines; filter genuine user prompts        │
                      │    └─ synthesize spool.Pointer ─▶ queue.Offer ──┐         │
   hook /enrich ──────┼──────────────────────────────────▶ queue.Offer ─┤         │
                      │                                                  ▼         │
                      │   [EXISTING] resolve → GLiNER2 enrich → mask → publish ────┼──▶ Atlas
                      └───────────────────────────────────────────────────────────┘
```

### File structure

- **Create `internal/agent/watch/watch.go`** — `Watcher` type: `New(...)`,
  `Run(ctx)` poll loop, `pollOnce()`. Discovers roots, iterates transcript files,
  reads new bytes from each file's cursor, parses lines, filters, synthesizes
  pointers, calls the injected `offer func(spool.Pointer)`.
- **Create `internal/agent/watch/roots.go`** — `Root{SourceID string; Dir string}`
  and `DiscoverRoots() []Root` with per-OS build tags or runtime `runtime.GOOS`
  switch:
  - macOS + Linux: `~/.claude/projects` → `claude_code`.
  - macOS only: each `~/Library/Application Support/Claude/local-agent-mode-sessions/*/*/local_*/.claude/projects` → `cowork` (globbed; new session dirs discovered each poll).
- **Create `internal/agent/watch/cursor.go`** — `CursorStore`: load/save a
  `map[filepath]offset` JSON under `paths.WatchDir()` (new,
  `~/.keld/watch/cursors.json`), atomic write. New files seen for the first time
  are recorded at their current EOF offset (forward-only) *unless* a backfill flag
  is set.
- **Create `internal/agent/watch/filter.go`** — `isUserPrompt(record) bool`:
  `type=="user"` AND `promptId` non-empty AND the message is a genuine human
  prompt (content is a string or contains a `text` block) AND not a tool-result
  record (no `tool_result` content block, `toolUseResult` absent). Extracts
  `promptId`, `cwd`, `sessionId`.
- **Modify `internal/agent/resolve/resolve.go`** — register `NewClaudeReader()`
  under `"cowork"` in addition to `"claude_code"` (identical format).
- **Modify `internal/agent/daemon/daemon.go`** — construct the `Watcher` with an
  `offer` closure that builds a `queue.Job` (reuse `ingress.JobFrom` /
  `pointerFromJob` semantics) and offers it to the queue; start `watcher.Run` as a
  panic-isolated goroutine alongside the spool drain; gate on `ml_backend != off`;
  stop on shutdown.
- **Modify `internal/agent/queue/queue.go`** — extend dedup to also drop keys in
  a bounded recently-*completed* ring buffer (default ~5000 `Source|Scheme|ID`
  keys), recorded when any job finishes. Closes the hook↔watcher overlap (see
  Dedup semantics).
- **Modify `internal/paths/paths.go`** (or equivalent) — add `WatchDir()`.
- **No change** to `internal/agent/enrich/context.go` `interactiveCodingTools`
  (Cowork intentionally excluded).

### Data model

The watcher builds the existing `spool.Pointer` (`internal/spool/spool.go`) — no
new wire type:

```go
spool.Pointer{
    Source:      spool.Source{ID: root.SourceID /* claude_code|cowork */, Origin: "watch", Version: <buildver>},
    Correlation: spool.Correlation{Scheme: "prompt_id", ID: promptID, SessionID: sessionID},
    Pointer:     &spool.PointerRef{TranscriptPath: file, PromptID: promptID, Cwd: cwd},
}
```

`Origin: "watch"` is a new value alongside the existing `"hook"`, letting Atlas
distinguish capture paths. `Source.ID` is the only place "which tool produced
this" is decided — and it's now derived reliably from *which root* the file lives
under, resolving the ambiguity the current hook-only model has.

### Dedup semantics

The canonical `~/.claude/projects` root is watched even when the CLI hook is
active (so in-Desktop Code, where the hook may silently not fire, is still
covered). A prompt caught by both paths shares `source=claude_code`,
`scheme=prompt_id`, `id=promptId`.

**The overlap is the common case, not a rare race, and must be closed — not
tolerated.** Timing: the hook fires on submit and its enrichment typically
completes in ~0.5–2s, while the watcher polls every ~5s. So by the time the
watcher first *sees* the transcript line, the hook's job has usually already
completed and left the in-flight set. An in-flight-only dedup would therefore
miss most overlaps and re-compute nearly every hooked prompt (Atlas upsert would
hide the duplicate *row*, but the wasted GLiNER2 compute — doubled on the most
common surface — is unacceptable).

**Fix — dedup against recent completions, not just in-flight.** Both the hook
and the watcher funnel through the *same daemon queue*, so no cross-process
ledger is needed. Extend the queue's dedup (`queue.go:22`) to also consult a
**bounded in-memory ring buffer of recently *completed* keys** (`Source|Scheme|ID`,
default last ~5000 — a few hundred KB of 36-char ids). A job is dropped if its
key is either in-flight *or* recently completed. The hook's completions populate
the same buffer because they pass through the daemon. This closes the window
regardless of hook-vs-watcher timing.

Persistence is unnecessary: the per-file cursor already prevents re-sighting old
transcript lines across a daemon restart, so the completed-set only has to cover
the live, seconds-scale race within a single daemon lifetime. (Cowork prompts
never collide — Cowork has no hook and a distinct `source`.)

### Error handling

Best-effort and non-fatal, mirroring the hook/spool ethos:
- Unreadable/rotated file → skip this poll, log at debug, retry next tick.
- Partial trailing line (write in progress) → do not advance cursor past it; wait.
- Missing/absent root (e.g. Cowork not installed) → skip silently.
- Malformed JSON line → skip that line, advance cursor past it.
- Watcher goroutine panic → recovered and isolated; the daemon and hook path
  survive.
- Gated entirely off when `ml_backend=off`.

### Configuration

- `KELD_WATCH` (default `on`) — master enable. `off` disables the watcher.
- `KELD_WATCH_POLL` (Go duration, default e.g. `5s`) — poll cadence.
- `KELD_WATCH_BACKFILL` (default `off`) — when `on`, initialize new-file cursors
  at offset 0 (enrich history) instead of EOF. Escape hatch for the deferred
  bounded-backfill feature; forward-only is the default.
- Future: promote the master enable to the org enrichment-settings control plane
  (a `capture_watch` key) — out of scope now (YAGNI).

## Testing

- **filter:** table test — genuine string prompt ✓, `text`-block prompt ✓,
  `tool_result` record ✗, `toolUseResult` record ✗, missing `promptId` ✗,
  non-`user` type ✗.
- **cursor:** write→read offset round-trip; atomic-write survives; unknown file
  initialized at EOF (forward-only) vs 0 (backfill on).
- **watch (integration, temp dirs):** drop a transcript with one user record →
  one pointer offered with correct `source`/`promptId`/`cwd`; append a second
  record → only the new one offered; simulate restart (reload cursor) → no
  reprocess; drop a Cowork-shaped nested path → offered with `source=cowork`.
- **resolve:** `cowork` source resolves text via `ClaudeReader` against a
  sanitized real Cowork transcript fixture.
- **dedup:** (a) hook-offered then watcher-offered *while in-flight* → dropped by
  in-flight set; (b) hook job **completed** (left the in-flight set) *then*
  watcher offers same key → dropped by the recently-completed ring buffer — the
  common timing, and the case an in-flight-only dedup would miss; (c) key evicted
  from the ring buffer → re-enrich is idempotent at Atlas (no duplicate row).
- **eval:** no enrichment-vocabulary change → no `SchemaVersion` bump and no eval
  gold changes required. Capture-only feature.

## Rollout

- No schema-version bump (no vocabulary change). New `Source.Origin="watch"` and
  `Source.ID="cowork"` values are additive; confirm Atlas accepts unknown
  source/origin strings gracefully (it already treats them as opaque).
- Docs: update README / AGENTS.md capture section and `docs/enrichment-settings.md`
  to describe the watcher as a second, hook-free capture trigger and list the new
  env vars.
- Target release: **v0.9.0** (new capture surface).

## Out of scope

- Windows paths (deferred; macOS + Linux first).
- Plain Desktop chat / claude.ai web capture (server-side; no local transcript).
- OTLP receiver (documented alternative; enterprise/central future work).
- Backfill of historical transcripts beyond the `KELD_WATCH_BACKFILL` escape hatch.
- Wiring agentic `Meta` fields from Cowork metadata (`local_<uuid>.json` has rich
  fields like `userSelectedFolders`, `model`, `systemPrompt`) — a later enrichment
  enhancement, not capture.
