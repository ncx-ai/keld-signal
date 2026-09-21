# Signal integrations — wire contract

`GET /v1/integrations` and its two siblings. This file is the contract WS-A
(conformance harness) and WS-E (pane + Playwright) build against; the Go types
are `internal/agent/integrations/{types.go,vocabulary.go,catalogue.go}`.

Spec: `docs/superpowers/specs/2026-09-15-signal-integrations-discovery.html`
(**AC-1** the route shape, **AC-4** the refusals, **AC-7** the version rule,
**AC-8** one implementation, **AC-9** the Codex sentence, section 4 the decision
table). Plan: `docs/superpowers/plans/2026-09-15-signal-integrations-plan.md`.

**One rule, one place.** A tool's state is decided only in
`integrations.Compute`. The route, `keld signal doctor` and the client-events
emitter all call it. The pane renders `state` **verbatim** and maps nothing: a
value outside `vocabulary.states` renders as `unknown state: <value>`. A second
copy of the rule — in Go or in JavaScript — is a defect (AC-8).

**Privacy.** Nothing on these routes carries prompt text, a span or an offset.
Ids, instants, counters, versions, states and one file path (the adapter's
backup) — that is the whole of it.

---

## 1 · Routes

| Route | Body | Answers |
|---|---|---|
| `GET /v1/integrations` | — | the `Response` below |
| `POST /v1/integrations/{id}/setup` | — | `{"backup": "<path>", "restart_required": true}` — runs the tool's adapter with a backup |
| `POST /v1/integrations/{id}/report` | — | `{"report_path": "<path>", "event_queued": true}` — writes the redacted bundle and queues `integration.report` |

Loopback only, behind the same secret as `/v1/settings`. `{id}` is an `id` from
the catalogue below; an unknown id is a 404.

The pane polls `GET /v1/integrations` every 10 s, like the health dot. Doctor
calls `Compute` once. Neither may trigger a model load or a download.

## 2 · Response shape

```json
{
  "integrations": [
    {
      "id": "claude_code",
      "display_name": "Claude Code",
      "installed": true,
      "configured": true,
      "supported": true,
      "storage_class": "jsonl-tail",
      "state": "working",
      "tool_version": "2.1.267",
      "surfaces": [
        {
          "kind": "hook",
          "documented": true,
          "wired": true,
          "expected": true,
          "last_seen": "2026-09-15T09:12:44Z"
        },
        { "kind": "otel", "documented": true, "wired": true, "expected": true,
          "last_seen": "2026-09-15T09:12:40Z" },
        { "kind": "watcher", "documented": false, "wired": true, "expected": true,
          "last_seen": "2026-09-15T09:12:44Z" },
        { "kind": "reader", "documented": false, "wired": true, "expected": true,
          "last_seen": "2026-09-15T09:12:45Z" }
      ]
    },
    {
      "id": "codex",
      "display_name": "Codex",
      "installed": true,
      "configured": true,
      "supported": true,
      "storage_class": "jsonl-tail",
      "state": "approval_required",
      "tool_version": "0.153.4",
      "surfaces": [
        {
          "kind": "hook",
          "documented": true,
          "wired": true,
          "expected": true,
          "waiting_on": "approval",
          "instruction": "Open Codex, run /hooks, approve the two keld hooks. Signal confirms here within a minute."
        },
        { "kind": "otel", "documented": true, "wired": true, "expected": true,
          "last_seen": "2026-09-15T08:55:01Z" },
        { "kind": "watcher", "documented": false, "wired": true, "expected": false },
        {
          "kind": "reader",
          "documented": false,
          "wired": false,
          "expected": false,
          "waiting_on": "reader",
          "instruction": "Signal captures this tool but cannot read its transcripts yet, so its prompts are not classified — nothing for you to do."
        }
      ]
    },
    {
      "id": "cursor",
      "display_name": "Cursor",
      "installed": false,
      "configured": false,
      "supported": false,
      "storage_class": "db-poll",
      "state": "unsupported",
      "tool_version": "",
      "surfaces": []
    }
  ],
  "vocabulary": {
    "states": ["not_installed", "not_configured", "restart_required",
               "approval_required", "idle", "working", "broken", "unsupported"],
    "waiting_on": ["", "restart", "approval", "reader"]
  },
  "auto_setup": true,
  "computed_at": "2026-09-15T09:13:00Z"
}
```

### Field rules

| Field | Rule |
|---|---|
| `id` | the source id the daemon already uses everywhere else — the spool pointer's `Source.ID`, the watcher root's `SourceID`, the teleproxy's per-source key. Lane facts join on it. ⚠️ It is **not** always the tool adapter's name: Gemini's adapter is named `gemini` while its source id is `gemini_cli`. `Entry.AdapterName` carries the adapter name. |
| `installed` | the config dir exists on disk, stat'd this poll |
| `configured` | the manifest records the tool |
| `supported` | Signal claims to capture it; `false` rows are catalogue rows only |
| `storage_class` | `jsonl-tail` \| `db-poll` \| `rpc` — how the tool's work would be read. The whole of what an unsupported row says. |
| `state` | one of `vocabulary.states`, never anything else |
| `broken_lane` | the expected lane that went silent; present **only** under `broken` |
| `stale_session_id` | the tool session still running on the previous config; present **only** under `restart_required`, omitted when there is none. ⚠️ "Restart this tool" is not an instruction once two windows are open — measured 2026-09-18, two live Claude Code sessions with one restarted and one not. The rule already decides over EVERY live session, so it knows which; this is that answer reaching the reader. An IDENTIFIER, the class already published as `corr_id` — no text, span or offset is read to produce it. The pane prints the first 8 characters, the same prefix `keld signal doctor` prints for the same session; both resolve which sessions are live through `internal/agent/sessions`. |
| `tool_version` | read from the newest transcript — Claude Code `version`, Codex `session_meta.cli_version`, Pi header `version`. `""` when unknown, **never guessed** (AC-7). |
| `surfaces[]` | the tool's lanes, in catalogue order |
| `backup_path` | where the adapter put the previous config, when this run wrote one |
| `vocabulary` | the closed sets, so a consumer derives its cases from the server. The Playwright state suite reads `states` from the live daemon, so a new state without a fixture **fails the suite**. |
| `auto_setup` | `auto_setup_integrations` (`~/.keld/agent-config.json`, default `true`) |
| `computed_at` | when `Compute` ran |

### `surfaces[]`

| Field | Rule |
|---|---|
| `kind` | `hook` \| `otel` \| `watcher` \| `extension` \| `reader` |
| `documented` | the **TOOL** documents this lane. `false` is the lane a tool release breaks silently — tailing a transcript format nobody promised. |
| `wired` | the config **on disk** is what the adapter would write, read back now — never remembered (AC-1) |
| `expected` | this lane can feed at the tool's current support level. A lane that is not expected can never make the tool `broken`. ⚠️ The `otel` lane is `false` here unless `tool_otlp` is on, which it is not by default — the example above is a machine that turned it on. |
| `last_seen` | the last instant this lane carried something for this tool; **omitted** when never seen. It is an instant on disk, so a daemon restart does not erase it. |
| `waiting_on` | `""` \| `restart` \| `approval` \| `reader`; omitted when `""` |
| `instruction` | present whenever `waiting_on` is set; the sentences are fixed and quoted verbatim in §4 |

The lanes, and where each fact comes from:

- **hook** — the tool runs `keld __hook`, which posts a prompt pointer. Last
  pointer with `Origin: spool.OriginHook` for this source.
- **otel** — the tool posts OTLP to the loopback telemetry proxy. Teleproxy's
  **per-source** last forward (`teleproxy.LastForwardForSource`).
  ⚠️ **OPT-IN AND OFF BY DEFAULT SINCE 2026-09-18** (`tool_otlp` in
  `~/.keld/agent-config.json`, `KELD_TOOL_OTLP` in both directions; a Developer
  row on the Settings page). Signal reads a tool's usage from the tool's OWN
  TRANSCRIPT, so this lane carries nothing Atlas prices — and it is the only
  lane that requires a credential to live inside a tool's config file, which is
  why it is the one that keeps breaking: a tool reads that file once, at
  startup, so a rotation, a moved endpoint or a keld upgrade leaves a stale copy
  inside a process nothing on the machine can inspect. It stays in the product,
  behind the switch, so "nothing we need arrives only here" can be CHECKED
  before the lane is removed. With the switch off `keld signal setup` and the
  detector write no OTEL block and REMOVE one an earlier keld left, and the lane
  is `expected: false` — so by the rule above it can contribute neither half of
  `broken`. Local only: no remote override, the reasoning `attribution` and
  `dev_blocks` already carry. ⚠️ It used to
  be the machine-wide instant answered for every source, which supplied the
  ACTIVE half of `broken` out of a *different* tool's traffic: a tool nobody had
  used read `broken · watcher` off Claude Code's telemetry. The machine-wide
  instant survives only as the fallback while the per-source record is empty, or
  while any forward on it is unattributed (an unrecognised service name) — both
  err toward `idle`.
- **watcher** — the daemon tails the transcripts the tool writes. Last pointer
  with `Origin: spool.OriginWatch`. ⚠️ Recorded where the watcher OFFERS the
  pointer, not where a worker picks the job up: on a machine whose hook is wired
  the hook wins and the watcher's offer is a `Duplicate` that no worker ever
  sees, so recording downstream left this lane silent forever on exactly the
  machines where it worked. The origin constants live in `spool` for the same
  reason — this record expected `"watcher"` while the watcher wrote `"watch"`,
  and every fact was dropped as unrecognised.
- **extension** — the tool loads a keld extension (Pi's shape; none shipped).
- **reader** — the sidecar parses this tool's transcripts into store rows. Rows
  exist for the recent pointers of this source.

## 3 · States

One sentence each. The set is closed and ordered; `vocabulary.states` publishes
it in this order.

| State | Meaning |
|---|---|
| `not_installed` | The tool's config dir is not on this machine. |
| `not_configured` | Installed, but the manifest does not record it — nothing has written keld's blocks into its config; the pane shows **Set up**. |
| `restart_required` | Configured, but a live tool session started before **keld** wrote that config, so that session is still using what it launched with. ⚠️ **"When keld wrote it" is READ FROM KELD'S OWN RECORD, not from the tool config's mtime.** The tools rewrite those files themselves — measured on the maintainer's machine 2026-09-18, Codex wrote `hooks.state` trust entries into its own `config.toml` at session start (20:56) and Claude Code rewrote `settings.json` unprompted (21:21) — so the mtime moved on the tool's schedule and a tool nobody had touched asked for a restart, repeatedly and unclearably. The instant now resolves, in order: per-tool `configured_at` in `~/.keld/manifest.json` (written by `keld signal setup` and the detector, through the one `ApplyEntry` path) → the manifest file's own mtime (keld is its only writer; coarse but still a fact about keld) → the tool config's mtime (what a machine written by an older keld still has). **Never `broken`** (AC-4). ⚠️ Cleared by EVIDENCE, not only by a newer start instant: a session that has forwarded telemetry through the loopback proxy since the config was written has demonstrably read it. Without that, a **resumed** session — which keeps its transcript, and therefore its start instant — could never clear the state however many times the tool was restarted, and the instruction beside it asked for a repair that does not work. Subagent (`agent-*.jsonl`) transcripts are excluded from both halves: they share the parent's session id and their first line is when the SUBAGENT started. ⚠️ Asked over EVERY session the tool still has open (written inside 30 minutes), not the newest transcript: with two live windows — one restarted, one carried over from yesterday — recency made the row alternate between `working` and `restart_required` every few seconds with nothing about the machine having changed. A live session still on the old config wins the report; once it goes quiet for 30 minutes the row clears on its own. |
| `approval_required` | Configured, and the tool itself is holding the wiring back pending a human approval — Codex's hook trust is the one case. **Never `broken`**, and the hook lane never reads `working` before approval (AC-9). |
| `idle` | Configured and restarted, and nothing arrived on any lane inside the window. A quiet user is not a bug (AC-4). |
| `working` | Every expected lane saw the tool inside the window. |
| `broken` | One expected lane saw the tool inside the window **and** another expected lane did not; `broken_lane` names the silent one. Both halves are required. |
| `unsupported` | A catalogue row only: Signal names the tool and its storage class and does not claim to capture it. |

**The two refusals matter more than the happy path.** Silence is `idle`, not
`broken`. A session older than its config is a restart notice, not a fault. A
lane a tool cannot feed is not expected, so it can contribute neither half of
`broken`.

The window is **24 h** to begin with — a starting guess, tuned once fleet events
say how quiet real machines get, not a measurement.

### Decision table (spec §4)

| # | configured | session newer than config | pointers · 24 h | telemetry · 24 h | rows for pointers | state |
|---|---|---|---|---|---|---|
| 1 | no dir | — | — | — | — | `not_installed` |
| 2 | no | — | — | — | — | `not_configured`, or auto-setup → `restart_required` |
| 3 | yes | no | any | any | any | `restart_required` |
| 3b | yes, hook untrusted (Codex) | — | 0 via hook | any | — | `approval_required` |
| 4 | yes | yes | 0 | 0 | — | `idle` |
| 5 | yes | yes | 0 | >0 | — | `broken` · hook |
| 6 | yes | yes | >0 | 0 | yes | `broken` · otel |
| 7 | yes | yes | >0 | >0 | no, reader expected | `broken` · reader |
| 7b | yes | yes | >0 | >0 | no, reader **not** expected (Codex today) | `working`, reader shown as not yet supported |
| 8 | yes | yes | >0 | >0 | yes | `working` |
| 9 | unsupported entry | — | — | — | — | `unsupported`, storage class shown |

⚠️ **Rows 5 and 6 turn on the `otel` lane, so with `tool_otlp` off they are not
reachable at all.** The telemetry column then reads "—": the lane is not
expected, so neither its silence nor its activity is evidence about the tool.
Row 6 (`broken · otel`) is unproducible; row 5's silent hook is still reported
as `broken · hook` when some OTHER expected lane is active.

## 4 · `waiting_on` and its instructions

Four values; `""` is a member and means *waiting on nothing*, stated rather than
implied by an absent key (the key is omitted on the wire when empty; the
vocabulary still lists it). Every non-empty value carries exactly one sentence.
These are the strings, byte for byte — they live once, in
`integrations.Instructions`, and the pane prints what the server sent.

| `waiting_on` | Instruction |
|---|---|
| `""` | *(none)* |
| `restart` | `Restart this tool to finish — a session that started before its config was written keeps using the settings it launched with.` |
| `approval` | `Open Codex, run /hooks, approve the two keld hooks. Signal confirms here within a minute.` |
| `reader` | `Signal captures this tool but cannot read its transcripts yet, so its prompts are not classified — nothing for you to do.` |

The `approval` sentence is quoted **verbatim in AC-9**. Codex records trust
against the hook's *hash*, so a keld release that edits the hook command returns
the row to `approval_required` and this sentence is what the person reads. Do
not reword it without moving AC-9.

## 5 · Catalogue

Seven entries, in this order. `config dir` resolves through `HOME` at call time,
so a test or the conformance harness isolates it by setting `HOME`.

| id | display name | adapter | config dir | storage class | supported | reader | expected lanes |
|---|---|---|---|---|---|---|---|
| `claude_code` | Claude Code | `claude_code` | `~/.claude` | jsonl-tail | yes | yes | hook, watcher, reader (+ `otel` *) |
| `codex` | Codex | `codex` | `~/.codex` | jsonl-tail | yes | **no** | hook (+ `otel` *) |
| `gemini_cli` | Gemini CLI | **`gemini`** | `~/.gemini` | jsonl-tail | yes | no | watcher (+ `otel` *) |
| `cowork` | Cowork | — | `~/Library/Application Support/Claude/local-agent-mode-sessions` | jsonl-tail | yes | yes | watcher |
| `pi` | Pi | — | `~/.pi/agent` | jsonl-tail | **no** | no | *(none)* |
| `antigravity` | Antigravity | — | `~/.antigravity` | rpc | **no** | no | *(none)* |
| `cursor` | Cursor | — | `~/.cursor` | db-poll | **no** | no | *(none)* |

\* **`otel` is expected only while `tool_otlp` is on, and it is OFF by
default.** The lane is opt-in behind a Developer row on the Settings page while
its removal is being evaluated — see its note in §2 for why. On a default
machine it is listed, unwired and `expected: false`, which is the same shape
Codex's `reader` lane had before WS-D: shown so that a lane with no traffic and
a lane nothing asked to speak do not look alike.

Why each expected set is what it is:

- **Claude Code** feeds all four lanes today — three of them by default, since
  the tool's own OTLP export is opt-in.
- **Codex** has no sidecar reader yet (WS-D). Its `reader` lane — and its
  `watcher` lane, whose only consumer is that reader — are listed and **not
  expected**, because expecting them would read `broken · reader` by
  construction on every Codex machine from the day the pane ships. Both flip
  when `Entry.ReaderAvailable` flips, together with the row in `types_test.go`
  that pins it.
- **Gemini CLI** has no hook lane at all: it runs no command hook for us, so
  there is nothing to be silent. Its reader is not expected either.
- **Cowork** runs in a VM. No hook can reach the host daemon from inside it, and
  its egress to Atlas is blocked by design, so the `otel` lane is shown and
  **never** expected; the watcher — the daemon reading the transcripts
  host-side — is the only lane that can ever feed.
- **Pi** is `supported: false` with a documented `extension` surface (its
  `before_agent_start` extension fires with the session file path). Shown
  because it is the shape a supported Pi would use; expected by nothing until a
  reader exists.
- **Antigravity** and **Cursor** are rows with a storage class and no surfaces.

`ExpectedLanes` is the one derivation of "expected"
(`Entry.ExpectedLanes(level)`), evaluated against a `SupportLevel`
(`{Supported, ReaderAvailable, ToolOTLP}`) rather than read off the entry inside
the rule, so a test can ask what an entry *would* expect at a level it is not at
yet. ⚠️ `tool_otlp` reaches the rule ONLY through that level — `Options.ToolOTLP`
→ `SupportLevel.ToolOTLP` → `ExpectedLanes` — and `Compute` tests it nowhere
else. A second check for it would be the second copy of the rule AC-8 exists to
prevent.

## 6 · What this package does NOT hold

No state logic. `Compute`, the wiring and lane facts, the version reader, the
detector and the routes are WS-C1's files (`compute.go`, `facts.go`,
`version.go`, `state_file.go`, `detector.go`, and `daemon/integrations*.go`).
The client-event codes and the report bundle are WS-C2's
(`docs/signal-client-events.md`). This file freezes the shape all of them
publish.

## 7 · The analysis engine (`GET /v1/engine`, `POST /v1/engine/install`)

Not an integration — it lives here because it is served by the same route list
and gated by the same mounted-routes check.

⚠️ **THIS MOVED OUT OF THE macOS INSTALLER ON 2026-09-21.** The wizard pane used
to download the ~300 MB engine and hold Continue until the fetch settled. That
put a large download in front of somebody who had not finished installing, on a
release host measured answering **504 on three of four full pulls** with a
30-minute client timeout per attempt; and rendering its progress from the
XPC-hosted pane drove a layout pass that pegged the plugin's main thread —
sampled on a real stuck install, **302 of 553 samples** in
`updateNextEnabled → KeldPaneView layout → heightFor:width:`, with the download
already finished and staged on disk. The daemon is the right owner: it knows
whether an engine is needed, which version is on disk, and can fetch it with
nothing blocked meanwhile.

`GET /v1/engine` → `200`:

```json
{ "needed": true, "installed": true, "version": "v3.0.4", "expected": "3.0.5",
  "outdated": true, "status": "idle", "received": 0, "total": 0, "error": "" }
```

- `needed` is false only under `ml_backend: "off"`. Both `"auto"` and
  `"deterministic"` run the analysis service, so both need one.
- `installed`/`version` are read from disk (`sidecarBinPath` + the tree's
  `VERSION`), never by probing the running service — an engine that is present
  but not yet up must not read as absent. A tree with no `VERSION` predates the
  stamp: `installed` true, `version` empty.
- `outdated` compares against this binary via `version.Skew`, so **`dev` on
  either half answers false** — cannot-tell never renders as a problem.
- `status` is `idle` | `running` | `done` | `failed`; `error` carries the
  reason verbatim on `failed` (`http status 504` and `no space left on device`
  are different actions).

`POST /v1/engine/install` → `202` with the same body, **immediately**: it starts
one install and the page polls. `409 not_needed` when `ml_backend` is `"off"` —
that is a choice, and this must not be the one way to undo it. `409
already_running` when one is in flight, so a second click cannot start a second
300 MB fetch.

The page renders this through `engineNotice()` (`ui/app.js`), which returns
**null** for a healthy machine: no card, no button nobody needs to press.
