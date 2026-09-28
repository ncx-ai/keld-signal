# Keld client — Agent & Contributor Guide

This repo is the **Keld client**: everything Keld runs on an engineer's own
machine. It has two jobs, and the second is the core of the project:

1. **Telemetry** — the `keld` CLI configures local AI coding tools (Claude Code,
   Codex, Gemini CLI) to emit usage telemetry to Keld Atlas.
2. **On-device enrichment** — the `keld-agent` daemon (+ its GLiNER2 sidecar)
   classifies each prompt **locally**, masks anything sensitive, and publishes to
   Atlas only the derived, masked signal. **Raw prompt text never leaves the
   machine.** This is the privacy-preserving intelligence the CLI installs.

⚠️ **Two qualifications, and this said "One" until the second arrived.**

1. **Schema v18.** `inventory.named_terms` publishes proper nouns lifted from
   message TEXT, and real person names have been observed in it. It is still not
   raw text, a span, or an offset: it is a term and a count. See the
   `named_terms` note in the workstreams bullet under *The enrichment agent* for
   the decision and the alternative that was not taken.
2. **`KELD_TEXTEMBED`, off by default.** Message text is encoded ON DEVICE and a
   256-d vector is published — MRL-truncated, then multiplied by a fixed
   orthogonal projection that preserves cosine and inner products exactly, so
   training is unaffected while off-the-shelf inversion tooling needs a matrix
   the client did not choose. See *Text vectors* in
   `docs/superpowers/specs/2026-08-26-signal-embeddings-design.md`.

So **"nothing derived from the prompt's own words crosses" is not the invariant**
and has not been since v18; the honest statement is narrower and is the one at
the top of this file: **raw prompt text never leaves the machine.** Text, spans
and offsets do not cross. Things MEASURED from text — a count, a length, a
vector — may, each on its own argument and its own evidence, never by analogy to
one already here.

Go single static binaries (`keld`, `keld-agent`) + an optional Python ML sidecar.
No runtime dependencies for the CLI itself.

## Vocabulary — project and dimension (2026-09-23, amended 2026-09-25)

A **project** (e.g. "SDKs") is Signal's only bucket, defined in Signal, in one flat list. The
repo/branch/model/… facets `/analyze` counts are **dimensions**. An **Atlas workstream** is
Atlas's value; Signal receives the org's list but does not match or show it. Until 2026-09-23
"workstream" meant three things here (a set of projects, the dimensions, and Atlas's value).
The 2026-09-23 rename briefly called the project a "workstream" and the set a "group"; on
2026-09-25 the project got its name back (Revision 3) and the set left the product (Revision 4).
`projects.Group` remains only as the stored container 3.0.6 needs. `scripts/check_vocabulary.sh`
fails CI on any retired name (`scripts/vocabulary-denylist.txt`).

**Signal labels on its own (since 2026-09-25).** The rule pass, the Projects pane, its totals
and `project_matches` see only the projects defined in Signal (`projects.Candidates`, the
local document). The org's workstreams still arrive on the settings poll and are held, unread on
those paths, and a poll no longer trims local rules an Atlas workstream covers. The model-based
attribution pass still scores against the org's list (decided the same day; its answers are
Atlas ids, and Signal's own ids are title-derived so cannot be published). Discovery:
`docs/superpowers/specs/2026-09-23-multi-group-attribution-discovery.html` → Revision 2.

**Kept on purpose — the keep list:** Atlas wire keys `projects` (settings and block row),
`projects_status`, `project_matches` and the facet key `workstreams`; the sidecar route
`POST /projects` and its `projects` body key (version skew); the status value
`skipped:no_projects`; the dimension named `project` (workspace basename); and anything about
Claude Code's `~/.claude/projects` directories or a `.keld.toml` project.
⚠️ **Stored names are exactly 3.0.6's, and must stay that way:** `state/projects.json`
(version 1, groups under `workstreams`, each project's group under `workstream`),
`workstreams_off` in agent-config.json and `KELD_PROJECTS_FILE`. Auto-update can roll a
machine back to 3.0.6, which reads only those; a renamed file meant the rolled-back daemon
found nothing, and whatever it saved was ignored after the next upgrade. Translate at
`projects.Load`/`Save`, never by moving the file. ⚠️ **3.0.6 renders a project only under a
group the file declares**, so `Save` writes every project under one (the person's existing
groups are kept; with none, one internal `projects` group is added) even though the page
has no groups. `workstreams_off` is never written any more: it is read once, on upgrade, to
hide the projects of a group that was switched off, and left in place for a rollback.

**Where the detail lives.** This file states the rules and the invariants. The
measurements, studies and incident post-mortems that produced them live in
`docs/architecture/`, one file per subsystem, each with a dated provenance header
— see *Design docs* at the end for the index. **Read the linked file before
changing one of those areas.** (Split out 2026-09-28, verbatim; nothing was
deleted.)

## Architecture

```mermaid
flowchart LR
  Tools["AI coding tools"]
  subgraph Client["Keld client (this repo)"]
    CLI["keld CLI"]
    Hook["keld hook"]
    Agent["keld-agent daemon"]
    Sidecar["analysis + enrichment sidecar<br/>(/analyze always; GLiNER2 lazily,<br/>no fallback for its facets)"]
  end
  Atlas["Keld Atlas"]
  CLI -->|configures| Tools
  Tools --> Hook
  Tools -->|OTLP| Agent
  Agent -->|forwarded OTLP| Atlas
  Hook -->|"/enrich pointer (never text)"| Agent
  Agent <-->|127.0.0.1| Sidecar
  Agent -->|masked enrichments| Atlas
```

**Two lanes, one privacy invariant.**
- **Telemetry (push):** AI tools POST OTLP to the daemon's loopback telemetry
  proxy (`internal/agent/teleproxy`, fixed port **14318**, `KELD_TELEMETRY_PORT`
  to move it), which forwards to Atlas with the daemon's own token.
  **No tool ever holds an Atlas credential.** `keld signal setup` writes the
  loopback address plus a **stable local secret**, generated once and never
  rotated on its own; the daemon attaches the org token itself, read **per
  request**, so a mid-flight rotation is picked up. A tool reads its config once
  at startup, which is why the credential must not live there — and why `keld
  signal setup` **says to restart the tools** and the `done` event carries
  `restart_required`.
  ⚠️ **The secret's source of truth is its own file, `~/.keld/telemetry-secret`
  (0600)**, resolved by `agentcfg.EnsureTelemetrySecrets` and mirrored
  write-through into `agent.json` for older binaries. Living only inside
  `agent.json` (rewritten by several writers) cost an outage on 2026-09-18.
  **Migration ADOPTS, never mints** — a fresh value 401s every configured tool at
  once. Not under `state/` (uninstall removes that wholesale). A deliberate
  rotation keeps the retired value valid for `KELD_TELEMETRY_SECRET_GRACE` (24h)
  in **all three** credential shapes; an empty previous authenticates nothing.
  ⚠️ **`keld signal setup` refuses rather than reporting success onto a broken
  machine:** it probes the running proxy with the credential it just wrote and a
  **401 restores the backups and exits non-zero** (no daemon listening is *not*
  a failure), and it refuses up front if a **newer** `keld` is on PATH.
  ⚠️ **The daemon repairs keld's OWN block** in a tool config when the values
  inside keld's markers drift (`integrations/drift.go`) — comparing VALUES, never
  a whole-file hash (the tools rewrite their own configs), never touching the
  person's own section, one attempt per tool per run through `ApplyEntry`, and
  **only if the running proxy confirms the credential it is about to write**
  (`telemetry.ProbeSecret`); the repair is SAID (`reason`, `repaired`).
  ⚠️ **The proxy accepts that secret in THREE shapes** — `x-keld-ingest-token`
  (Claude Code, Codex), a `/t/<token>` PATH SEGMENT (Gemini, whose OTLP SDK
  cannot send a custom header and breaks a `?token=` query — the query form is
  still ACCEPTED for configs written by older releases), and
  `x-keld-telemetry-secret`. Assuming one 401s all three tools while the Go suite
  stays green.
  ⚠️ **The proxy forwards only while the tool's `tool_otlp` switch is ON, read
  per request** (`Proxy.Forwarding`): a running tool keeps posting from memory
  after the switch goes off while the transcript mirror covers it, so forwarding
  both double-counted every running tool. Off, an authenticated export is
  answered 202, never forwarded, never recorded as a forward, and counted.
  ⚠️ **`teleproxy.textKey` is TWO-SIDED and both halves are load-bearing:** match
  a text word, then subtract the identifier/measurement suffixes (`.id`, `_id`,
  `_length`, `_tokens`, …). Drop the first and text leaks; drop the second and
  `prompt.id` is blanked, which silently kills every `Enrichment.corr_id` ↔
  `ToolEvent.prompt_id` join. An unanticipated shape fails **CLOSED**. Pinned by
  `striptext_identity_test.go` against a real captured payload — keep it real.
  **Doctor asks PER SESSION** (`localagent.SessionTelemetryState`), because the
  machine-wide check lets one healthy editor vouch for a broken one. Three
  refusals keep it honest: an empty record is *not tracked yet*, `agent-*.jsonl`
  subagent transcripts are excluded, and a session's start instant is read by
  DECODING lines for a top-level `timestamp`, never by taking the first one.
  ⚠️ `teleproxy`'s tests isolate `KELD_HOME` in a `TestMain` — `New()` resolves
  `StatePath()` at construction, so without it `go test ./...` overwrites the
  developer's real `~/.keld` telemetry record.
  Telemetry **depends on the daemon**; a bounded spool under `spool/telemetry`
  pays for that. Delivery is confirmed from the **RESPONSE**, never the status
  code (captive portals answer 200 with an HTML login page). A drain **stops on a
  REJECTION** (401/403), **ends the sweep on UNAVAILABLE** (net/5xx), and
  **continues past a REFUSED payload** (4xx).
  Why each of those exists, with the measurements: **`docs/architecture/telemetry-proxy-and-capture.md`**.
  Spec: `docs/superpowers/specs/2026-08-27-telemetry-loopback-proxy-design.md`.
- **Enrichment (local):** the hook fire-and-forgets a *pointer* (transcript path
  + prompt id — **never text**) to the daemon's loopback `/enrich`. The daemon's
  background worker resolves the text on-device, runs the enrichment pipeline,
  **masks**, and syncs to Atlas `/v1/enrichments`. The prompt text is read
  locally and never transmitted — masking is enforced Go-side before publish.

## The enrichment agent (`keld-agent`) — the core

`cmd/keld-agent` → `internal/agentcli` → `internal/agent/*`. Lifecycle:
`ingress` (loopback HTTP intake, per-user secret, bounded `queue`) → `resolve`
(read prompt text + tail recent prompts from the transcript) → `enrich`
(the pipeline) → `mask` → `publish` (Atlas). Panic-isolated per job; a readiness
gate holds work until the backend is up.

⚠️ **A DEDUP IS NOT BACKPRESSURE, and collapsing the two cost work twice.**
`queue.Offer` returns an `Outcome` — `Accepted` / `Duplicate` / `Full` / `Closed`
— because it used to return a bare `false` for all four and both callers read
that as overload. The ingress answered **429**, and since the hook treats any
`>=400` as failure and durably SPOOLS the pointer, a prompt the daemon had
already published came back as "try again later" and was written to disk;
`drainEnrichSpool` then kept that row and re-offered it on **every sweep
forever**, because a row is deleted only when its offer succeeds and a duplicate
never can — unbounded spool growth. Observed live: a POST for a prompt finished
one second earlier returned 429 while the 1024-slot queue held single digits.
`Outcome.TakenOn()` (Accepted or Duplicate) is the predicate both callers want —
the daemon has assumed responsibility, so a caller holding a durable copy may
drop it. Only `Full`/`Closed` are backpressure. The hook↔watcher overlap that
produces duplicates is **designed** (`queue.Complete` exists for it) and must
never read as overload. Delivery is durable: the hook writes a
prompt *pointer* (never text) to the on-disk `spool` when the daemon is
unreachable, and the daemon drains it on startup + a periodic sweep.

**An unconfigured agent does not fail, and an unpaired one COLLECTS.** A missing
`~/.keld/hook.json` is a normal startup state (the service is registered before
onboarding), so a clean exit must stay final: launchd's `KeepAlive` is the
`SuccessfulExit=false` dictionary (an unconditional `<true/>` cost **69 spawns in
12 minutes**), systemd's `Restart=on-failure` is the equivalent — don't add
`RestartSec`. **Collect always, pair to send** (`daemon/pairing.go`,
`daemon/senders.go`): every collector starts immediately, every sender resolves
its endpoint through `pairing` per request, and a sender handed `""` **HOLDS** —
spools, keeps its cursor or re-spools — never reports success and never drops
(`publish.ErrNotPaired`, `clientevents.ErrNotPaired`, `settings.ErrNotPaired`).
Enrichment holds each job in the enrich spool without consuming a retry, and the
spool drain is held with it; both are gated on Atlas being ON as well, so a
local-only machine keeps enriching. Status, doctor and the health strip say
**collecting, not paired** (`n/a`, reason `not_paired`) — never `failed`, never
silence. Detail: **`docs/architecture/pairing-and-collection.md`**.

**Capture triggers.** Two triggers feed the same queue: the **command hook**
(`keld __hook --source <tool>`, wired by `keld setup`), and an on-device
**transcript watcher** (`internal/agent/watch/`) that tails the JSONL transcripts
Claude Code (all surfaces incl. the Desktop app), **Cowork**, and **Gemini** write to disk —
the hook-free path. The watcher synthesizes the same `spool.Pointer` the hook does
(never text) keyed on `promptId`; the queue dedups hook↔watcher overlap via a
recently-completed set (`queue.Complete`, marked only on a real publish so retries
and watcher fallbacks stay re-offerable). Sources: `~/.claude/projects` →
`claude_code`, the Cowork `local-agent-mode-sessions/**/.claude/projects` trees →
`cowork` (macOS), `~/.gemini/tmp/*/chats` → `gemini` (all platforms). Env: `KELD_WATCH` (default on), `KELD_WATCH_POLL` (default 5s),
`KELD_WATCH_BACKFILL` (default off = forward-only), `KELD_WATCH_ROOTS` (comma-separated
`source:dir`, default empty). macOS + Linux; Windows deferred.

**Cowork went VM-backed and host-side capture cannot follow it** (as of
2026-08-06). Newer Claude desktop builds run Cowork inside a VM whose transcripts
live in its disk image; nothing on the host can read them, and because the
pre-VM session directories are never cleaned up a root is still discovered, just
permanently stale. `coworkHidden` (`internal/agent/watch/roots.go`) detects that
exact shape and logs one advisory line per daemon run — the failure is otherwise
completely silent. `KELD_WATCH_ROOTS=cowork:<dir>` points the watcher at a
readable path the day one exists.

**The transcript-first usage mirror (`internal/agent/promptlog`).** Started as
Cowork's workaround (its sandbox blocks egress to Atlas) and is now the general
mechanism: a transcript needs no config, no credential inside the tool and no
restart. **Never emits prompt/response text** (`privacy_test.go` canaries every
text-bearing field). Rules:
- **Each mirror speaks its tool's NATIVE OTLP shape**, so Atlas needs no new
  parser: `claude_code`/`cowork` → `user_prompt` + `api_request` (+ token
  metrics), `codex` → `codex.sse_event` `response.completed`, `gemini` →
  `gemini_cli.api_response`. Codex and Gemini emit no metrics.
- ⚠️ **ONE record per REQUEST, not per line** — Claude Code stamps a request's
  usage on every content-block line (1.79x measured); the mirror emits on a
  request's first line. Codex prices a record only where the cumulative total
  ADVANCED. Gemini's session is one document, so it arrives through
  `Telemetry.ObserveFile`, not the per-line hook.
- **Dedup contract:** every identifier on a priced record is read off the
  transcript, nothing from process state — **no `event.sequence`** on
  `api_request`, no wall clock, no counter. `assistant_response` is no longer
  mirrored. The remaining gap (Atlas preferring `request_id`) is Atlas-side.
- **Sources are a parameter** (`Telemetry.SetSources`, wired to the per-tool
  `tool_otlp` switch; `SourcesFromEnv` still defaults to `{cowork}`). Per source,
  mirroring is on or off — half a source double-counts. Widening the mechanism
  and flipping the policy stay separate changes.

**Gemini capture.** ⚠️ **Two chat shapes are in the wild** (a JSON document up to
0.37.1, JSONL from 0.60.0) and reading either alone leaves a population
uncaptured. **`internal/geminichat` is the ONE place that knows the shape** and
decides by CONTENT, never extension; watch, resolve and the conformance
checkpoint all read through it. A `$set` line is not a turn. The watcher's lane
is a DOCUMENT lane whose cursor counts prompts already offered, and it reads a
file from the start when its mtime is newer than the watcher's start (a one-shot
`gemini -p` would otherwise never be captured — its hook is inert). The
credential rides a `/t/<token>` path segment; `/v1/traces` is accepted and
discarded (counted). The reader is registered under BOTH `gemini` (hook) and
`gemini_cli` (watcher) — which name is right is an Atlas-side decision, not a
rename in passing. `mockllm` answers Gemini's router FROM THE REQUEST'S SCHEMA.
`traces: false` must not come back into `~/.gemini/settings.json` (the tool
rejects the key); `logPrompts: false` is the real control.

Detail: **`docs/architecture/telemetry-proxy-and-capture.md`** and
**`docs/architecture/gemini-capture.md`**.

**Enrichment pipeline (`internal/agent/enrich/`).** A staged registry of
extractors ("sweeps") run over a swappable `Model` backend, producing a `Profile`.
Single-flight (never fans out) so the shared model issues at most one inference at
a time. Two waves, up to 7 facets per prompt:
- **Wave 1** (independent, committed as a batch): `task_type` (the routing key for
  the Keld Inference Exchange — a 10-entry routing taxonomy: `summarization`,
  `translation`, `code_generation`, `information_extraction`, `classification`,
  `reasoning`, `question_answering`, `text_generation`, `rewriting`, `general`),
  `sensitivity` (+ masked entity spans; detects **concrete leaked data**, not
  topic — unions TWO independent evidence sources, **neither of them GLiNER2**,
  and rolls up to the highest-severity class: `ssn`⇒`phi`, `credit_card`⇒`pci`,
  `api_key`/`secret`⇒`secrets`, other personal id⇒`pii`. See
  **The sensitivity facet's two sources** below),
  `domain` (+ entities), `activity_type`, `personal`, `function_guess`
  (12 business functions).
- **Wave 2** (conditioned on Wave-1 `function_guess`): `subcategory`.

**`speech_act` was DROPPED at schema v9, and the gate is now model-free.** It
was an eighth facet (`command`/`question`/`statement`/`fragment`) and it also
supplied the enrichment gate's model half. A pre-registered study measured it
live over 2,015 inferences
(`docs/superpowers/specs/2026-08-24-facet-value-results.md`): accuracy **0.695
against a 0.713 majority baseline** — worth less than always answering
`command`. It predicted `statement` 22 times and was right **zero** times, at up
to full confidence. The other measured facets are fine (`task_type` 0.733 vs
0.143, `domain` 0.683 vs 0.261, `activity_type` 0.670 vs 0.243), so this is a
targeted removal, not a retreat from model-backed classification. Consequences,
each deliberate: `Profile.SpeechAct`/`SpeechActAlt` and
`Enrichment.speech_act`/`speech_act_alt` are gone from the published payload (a
published-vocabulary change, hence the v8→v9 bump; producer strings move `-v8`
→ `-v9`); `SpeechActDefs` is deleted; the gate keeps only its model-free
approval lexicon (`prefilterContentFree`), whose own validation measured it at
recall 100% on-corpus with the `fragment` branch a strict subset of it, so
0/24-dangerous survives; `sensitivity` is now the ONLY always-run pass. This
saves ~12% of enrichment CPU (one inference of eight) and **none** of the 1.8 GB
model — the remaining facets still need it. The **gold labels are kept** in
`eval/gold.jsonl`, unscored, as the evidence for judging a re-introduced
classifier: the study named the label *wording* as the suspect, so a
`SpeechActDefs` re-bakeoff is the live alternative to permanent removal.

**The sensitivity facet's two sources — NEITHER of them GLiNER2.** The facet does
not touch `ctx.Model` at all (a test asserts its output is identical with a Model
present and with none); nothing classifies, and the class is a rollup over which
entity labels were DETECTED (`sensitivityFromEntities`).
1. **`creddetect`** — vendored gitleaks credential rules, pure Go, no model, no
   network, over the FULL prompt text. Always available.
2. **The sidecar's `/pii`** (`app/pii.py`, presidio-analyzer) — region-scoped
   pattern recognizers, every one checksum- or algorithm-validated, needing no
   GLiNER2 and no spaCy model. It returns **offsets only, never the matched
   value**; the Go side resolves and masks from its own copy of the text.
   Configure with `KELD_PII_REGIONS` / `pii_regions`; `Remote.PIIRegions`
   overrides both and takes effect on the **next prompt**.

⚠️ **Region scoping is a PRECISION decision, not a cost one** — the full set
measured +0.5 ms/prompt. National-id shapes COLLIDE across countries (a valid
`us_npi` is exactly the `uk_nhs` shape, and `uk_nhs` rolls up to `phi`), so
enabling a region an org does not operate in manufactures severity. Collisions
are pinned in `sidecar/app/test_pii_regions.py`.

⚠️ **`person` and `address` are NOT DETECTED — the coverage is four types, not
six.** presidio's `SpacyRecognizer` measured **~1% precision** on 2,000 real
developer prompts and was removed rather than tuned. Free-form personal names in
prose are now undetectable; `SensitivityFromEntity` still knows both names so a
future detector needs no schema change.

⚠️ **The published-test-value gate lives in ONE place: `sidecar/app/wellknown.py`**,
applied **at source** inside `scan()`. Do not add a Go copy and do not add a
second detector that would need one. A companion numeric-fragment gate lives in
`pii.py` because it needs the surrounding text.

**`facets_degraded` turns on the SCAN, not the Model.** The scan is the sole
source for every personal-data type, so absent/failed/**truncated** ⇒ degraded
(unless the answer already reached `phi`, which no missing evidence could raise).
Never let a check that did not run publish a confident negative.

Measurements, region tables and the two argued severity assignments:
**`docs/architecture/sensitivity-and-pii.md`**.

**The deterministic passes, and the analysis service they call.** These need no
model and run under every backend that enriches at all:

- **`workstreams`** (`enrich/workstreams.go`) is the one pass that runs **no
  inference**: it asks the sidecar's `/analyze` for the deterministic dimensions
  of the hour ending at this prompt — seven ALLOCATION dimensions (project,
  branch, model, output_type, language, skill, tooling) plus nine published
  INVENTORY ones — counted from tool-call metadata. It takes COORDINATES
  (transcript path + prompt id), **never text**. `ModelFree`+`AlwaysRun`,
  registered only with an analysis backend (`enrich.WithDimensions`) **and** a
  source the analysis can read (`enrich.DimensionsEligible`: `claude_code` /
  `cowork` only — Codex/Gemini prompt ids 404). A failed analysis fails the pass
  (`pipeline_status:"partial"`); it never publishes an empty set.
  **Attribution needs TWO things:** the winning share ≥ 0.50 **and** ≥
  `window.MIN_EVIDENCE` (5) observations. A share is a ratio — one tool call
  gives 1.0 by construction. Five is derived: `0.5**n` first falls below 5% at
  n=5.
  **EVERY dimension publishes and the floor is a LABEL, not a publish gate**
  (schema v21 / sidecar SCHEMA 16): `Labeled` carries `evidence` and `status`
  (`attributed`/`thin`/`tie`/`no_majority`/`absent`). **The floor did not move
  and nothing was promoted** — removing the two conditions would take
  P(false attribution) from 0.031 to 0.50. A consumer rendering `thin` as
  `attributed` is misreporting.
  ⚠️ **All nine inventories publish, including `named_terms`** (schema v18) —
  proper nouns from message text, term + count, no span or offset, and real
  person names have been observed in it. There is deliberately **no person-name
  filter**: none measures better than ~1% precision, so one would remove the
  belief that names are present rather than the names. `inventory` as a BLOCK
  stays unforwardable (a test pins it), so a tenth key cannot ride along.
  **The PATH inventory levels** (`files`/`directories`/`components`) are already
  workspace-relative — `reconcile()` resolves against the workspace root, and it
  is gated TWICE (sidecar payload assert + `sidecar.notWorkspaceRelative` at the
  Go decode boundary). Do not add a producer that bypasses `reconcile()`.
  Per-level caps (**40 / 24 / 16**) sit just above each level's p90 and the cut
  is declared in `inventory_omitted`.
  ⚠️ **A prompt is named by `promptId`, never `uuid`.** `watch/filter.go` rejects
  a line without one, the spool pointer carries it, the queue dedups on it, and
  it publishes as `corr_id`. Holding only `uuid` in the sidecar's index silently
  404'd every lookup and made **8 of 8 prompts partial**. Pinned from both ends by
  `sidecar/app/test_prompt_id_seam.py` and `watch/filter_test.go` — **do not
  remove `promptId` from those fixtures**, and `analyze.py`'s oracle must change
  in the same commit as the index or the equality test proves nothing.

  Caps, studies and the full incident: **`docs/architecture/window-analysis.md`**.
- **The watcher signals ingest; the sidecar never polls.** A file that advanced
  in a poll is signalled once to `POST /ingest`, which parses only the appended
  tail from its byte-offset checkpoint. Coordinates only. Signals are
  **dropped, not retried** (the next one catches up); the queue is bounded and
  path-coalescing with one serial sender, so an unreachable sidecar can never
  slow the watcher's poll loop.
- **Retention is bounded by TIME, and a pruned window REFUSES (410) rather than
  answering narrower.** `KELD_REFSERIES_RETAIN_DAYS` (400), `term` at
  `KELD_REFSERIES_TERM_RETAIN_DAYS` (90); `KELD_REFSERIES_MAX_MB` (1024) is a
  size backstop enforced on **live pages**, not file size (SQLite does not shrink
  on DELETE). Pruning raw events does not degrade a window's edges — it breaks
  the digest outright, which is why the store keeps a monotonic serving floor and
  answers **410**, never 503 (retried forever) or 404 (hides a short horizon).
- ⚠️ **`/analyze` and `/ingest` are confined to `KELD_ANALYZE_ROOTS`.** The
  sidecar has **no auth**; these are the first routes that open an arbitrary
  filesystem path as the daemon's user, so unconfined they are a confused deputy
  on a multi-user host. `realpath` on both sides, **403** outside (not 404).
  Empty means **deny everything**; absent means the sidecar's own defaults.
- **This is the client-side analysis service, not a GLiNER2 wrapper.** It starts
  and serves with no model loaded. `/analyze`'s `named_terms` level is **on** by
  default (`KELD_TERMS=0` off) and loads spaCy — ~619 MB into the FastAPI parent,
  permanently, since the parent is never recycled. ⚠️ That default was justified
  by the level being unforwardable, and **since schema v18 it publishes**, so the
  default now rests on usefulness alone and was never re-argued on its own
  merits. `named_terms_status` distinguishes a level that never ran from a window
  that held no terms; `KELD_TERMS_MAX_LEN` (100k chars/message) restores spaCy's
  own per-document guard.
- **Classifiers score against readable label DESCRIPTIONS, not bare id strings**
  (the bi-encoder keys on token/semantic overlap — the label wording is
  load-bearing; e.g. `code_generation` scores against "software engineering").
- Label vocabularies live in `labels.go` (gated by `SchemaVersion`, currently **21**
  — bump it and re-run the eval when changing any vocab). Classify calls are
  prefixed with a context preamble (`Meta.PreambleCoding()`; `domain` uses the
  fuller `Meta.Preamble()`). **Facet-selective agentic augmentation:** agentic
  framework metadata helps `domain` but hurts `task_type`, so only `domain`
  augments with it — `task_type` and the others drop it.

**The reference-series store (`analysis/store.py`, `analysis/ingest.py`).**
`/analyze` used to re-parse the whole transcript per request — median **0.79s on
a 90 MB file**, the same hour parsed ~4x. It now answers from a persistent series
at `~/.keld/state/refseries.db` (native SQLite, `0600`, via `KELD_HOME`; the
package is deliberately pandas-free): **2.3ms on the same file**. Equality with a
parse is **asserted, not assumed** — `analyze_window_by_parse` is retained as the
ORACLE and never as a fallback (0 of 90 real prompts differ).
- **A window is a query over 5-minute bins and BOTH EDGES ARE READ EXACTLY**:
  interior from `bin`, the two partial edge bins from `event`. Snapping either
  way turns every digest into an approximation. The ordering rule is not
  reimplemented in SQL — `window.rollup` merges and applies its own tie-break.
- **`bin` is sparse by design and its absence must never read as "no evidence"**:
  a `bin_level` registry that `bin.level` REFERENCES (with `foreign_keys=ON`) plus
  `rollup_window` routing unbinned levels to `event`. `PRECOMPUTED_LEVELS` is
  DERIVED from `dimensions.ALLOCATION` + `INVENTORY`, never typed.
- Row timestamps are quantized to 0.1s (`levels.quantize`). WAL +
  `busy_timeout=5000` + `synchronous=NORMAL` — `NORMAL` is safe **only** because
  events, re-rolled bins and the byte-offset checkpoint commit in **ONE
  transaction**.
- ⚠️ **`KELD_CAPTURE` (default OFF) is fingerprinted into `parse_state`**, so
  flipping it forces one reparse and no transcript can hold rows from two
  settings — but that is a per-TRANSCRIPT guarantee, so a corpus builder can see
  two incomparable populations. Absent means NOT RECORDED, never zero. It keeps
  four numeric signals (per-role character counts, the token split, tool-call
  outcomes, and `bin_offset`). ⚠️ `capture.scan`'s anchoring timestamp must be
  the RECORD'S OWN: a bare first-`"timestamp"` regex took a nested one on 1.5% of
  lines and produced non-monotone byte offsets, so non-message lines are DECODED.
- ⚠️ **Thinking-block LENGTH is not in this data** — every block carries an empty
  `thinking` string. The COUNT is the signal. Don't wire a length consumer.

Derivations, measurements and the retention/`/metrics` detail:
**`docs/architecture/reference-series-store.md`**.

⚠️ **`KELD_TEXTEMBED` (default OFF) reads message text in order to keep something
derived from it** (`analysis/textembed.py`) — the second of the two qualifications
at the top of this file. What crosses is a 256-d vector: Qwen3-Embedding-0.6B
encodes 1024-d on device, MRL prefix-sliced to 256, then multiplied by a **fixed
orthogonal projection** (`KELD_TEXTEMBED_PROJECTION_SEED`) that preserves cosine
and inner products exactly — training unaffected, off-the-shelf inversion tooling
needs a matrix the client did not choose. ⚠️ The matrix is **Keld's, not the
client's**. No text, span or offset crosses.
- **The unit is the MESSAGE, not the shell**, so each message is encoded once
  ever and every overlapping shell reuses the vector. Three streams (`user`,
  `asst`, `think`) kept separate and never concatenated; `think` is
  `skipped:empty` in practice.
- **Its own child process, bf16** (measured 2026-08-26: 1313 MB cheaper than
  float32 and no slower). Sustained cost is **~1.1-1.6 s/message** and the peak
  **2.35-2.43 GB** — size any per-message work off ~1.6 s, not off the
  single-shot figures the design first carried.
- **Absent weights are a STATED status** (`degraded:weights_unavailable`), never a
  crash or a stall. ⚠️ **Never cut a message mid-sentence** — long messages split
  at sentence boundaries and mean-pool; an over-long single sentence is dropped
  WHOLE and declared in `dropped_chars`. Every scalar is `None` where it could not
  be computed, never 0.0.
- **`POST /features` is a CURSOR route** (schema 17), not an anchor-instant one:
  `{path, since_ts, now, max_rows, resolved}` → `{schema, rows, watermark}`, with
  `/features/probe` kept for studies. `since_ts` is `>` on a row's own instant so
  a batch is never cut inside one. `FEATURE_SPEC_VERSION` 2, `DIMS` **1534** — the
  106 text slots are present **whether or not the toggle is on**, because a width
  that depended on the environment is the incoherent-corpus failure the frozen
  manifest exists to prevent.
- ⚠️ **Encoding runs OFF the request** (the sidecar client's timeout is 5 s; one
  batch of 64 messages costs ~92 s). The instant of the first unencoded message is
  the **FRONTIER** and no row at or after it is emitted; `pending:encoding` is the
  stated status. A message the encoder ran on and produced nothing for is cached
  as such, or the frontier LATCHES and the cursor wedges forever. A **degraded**
  encoder drops the text half WHOLE and returns no frontier.

Full contract, the loadtest figures and the two defects they found:
**`docs/architecture/text-vectors-and-features.md`** and
`docs/superpowers/specs/2026-08-26-signal-embeddings-design.md`.

⚠️ **A tail parse is only equal to a full parse because it was MADE equal**, and
if it isn't the series is silently and permanently wrong — nothing downstream
re-derives these rows. `ingest_file` resumes from the `ingest` table's byte offset
(rotation caught by a `HEAD_BYTES = 4096` head fingerprint that includes the byte
count). Two retroactive sources, handled differently because the costs differ:
**`reconcile` is RECOMPUTED WHOLE each batch** (which is why `Store.replace_events`
DELETEs its slot first — a recomputed set must be able to RETRACT a row), and
**workspace evidence ACCUMULATES, with a change in the DERIVED ANSWER forcing a
reparse**. The parse state carries a **third** accumulator, `reqs`, because
`events_for_turns` deduped requests with a set local to one call — a request
straddling a batch boundary was costed twice.

**Equivalence at corpus scale: 284 real transcripts, each ingested in 40
successive chunks against one whole-file ingest — 0 files differed, 0 rows
differed** (measured 2026-08-26). The watermark must not retreat, and
`ingest.terms_mode` / `capture_mode` fingerprint the pipeline's identity into the
parse state; a changed fingerprint reparses. Bumping `ingest.STATE_VERSION` is
the REPAIR mechanism for rows already written wrong — **expect one reparse per
transcript on such an upgrade**.

Why each measurement was taken and what it cost to miss:
**`docs/architecture/reference-series-store.md`**.

**The dynamics block (`analysis/dynamics.py`) — what MOVED in the window.** The
same `/analyze` call answers it, so dynamics cost **no second round-trip and no
inference** and publish under `ml_backend:"deterministic"` too. The span is cut
into a recent **slice** and an abutting **baseline**. What crosses is the derived
half only: `status` (a closed six-value set, **always stated**, so absence is
readable — `tooling` is absent on 50.3% of 60-minute windows), `turnover` and
`decay` (two different facts), `concentration_shift`, `changed` (**three-state**:
`false` only for `both_absent`, **nil** wherever the comparison cannot support a
yes or a no), and `reading` (a closed 7-value vocabulary, in precedence order).
⚠️ **Stating the conclusion IS the feature** — raw window numbers scored
**-3.3/-20.0** on synthesis accuracy, worse than emitting nothing, against
**+36.7** for a labelled digest. Numbers ship **keyed** beside the stated reading;
the unlabelled remainder does not.
**That same cut is the privacy mechanism.** Every field that could hold a level's
own string lives in the per-side `slice`/`baseline` objects, which do not cross —
`term` has held real person names. The subtree's only strings are `status` and
`reading`, asserted by a reflect walk at the decode boundary and a marshal-level
wire test. Both vocabularies are mirrored Go-side (`enrich.DynamicStatuses` /
`DynamicReadings`) and pinned against `dynamics.py` by reading that file, because
the Go side **DROPS** an unrecognised value and the sidecar ships separately.
**`MIN_EVIDENCE` (5) is about SAMPLE SIZE, not minutes**, and `MATERIAL =
1/MIN_EVIDENCE = 0.2` is derived from it. Duration appears nowhere in the
derivation and `min_evidence_for()` deliberately takes no duration argument — a
duration-scaled floor would make a published attribution's significance a
function of slice length while `value` and `share` look identical either way.
**The FLOOR ITSELF must not move.**
**The slice is sized by an EWMA change detector** (`DEFAULT_SIZER`, fast 0.3 /
slow 0.02 / threshold 0.2, on a 60-second observation step), which beat
`FixedSizer(15)` by **+74.6 / +27.0 points** — and is believed because of the
**shuffled-truth control**: the EWMA collapses 86.4% → 24.1% on shuffled truth
while every fixed sizer barely moves. ⚠️ **`river` was measured and REJECTED; do
not add it hopefully.** Detection reads **`branch` only** — widening it is
unmeasured.
**Half the dimensions were dropped on their own distributions**
(`DROPPED_DIMENSIONS = ("project", "model", "tooling")`), against a bar written
down FIRST, re-measured 2026-08-25 on 500 transcripts / 2,555 windows. ⚠️ **The
CONSTANT band test alone MISCLASSIFIES A SPARSE SIGNAL** — never apply it without
the inside/outside contrast beside it. `DYNAMIC_DIMENSIONS` is derived from
`dimensions.ALLOCATION` minus the dropped set and `dynamics()` neither takes nor
forwards a `dimensions=` argument, so **the published vocabulary cannot be
widened by a caller**.

**The session prior (`analysis/prior.py`) — the session this window sits in,
reported BESIDE it.** Per dimension a `value`/`share`/`evidence`/`status` plus
three contrasts: `agrees`, `departure`, `novel`. Same `rollup_window`, wider
bounds, no second parse and no inference.
⚠️ **CONTRAST, NEVER FALLBACK — every other rule here is subordinate to this
one.** The prior never supplies a value the window lacked; with no window value
all three contrasts are `None` and `workstreams` keeps its honest blank. **45.1%
of windows have no prior at all**, and that number is the standing pressure to
soften this. Don't. The block is emitted anyway, saying `absent` out loud,
because a suppressed block reads as an oversight and an oversight is what someone
eventually "fixes".
⚠️ **The prior is cut at the window's START** — "the session so far" taken
literally puts the window inside its own prior, under which `novel` cannot fire
at all. Nothing is accumulated; it is **recomputed** per request.
**`ENABLED = ("branch", "language", "output_type", "skill")`**, decided over 1,022
windows. ⚠️ `output_type` was first excluded on agreement alone and **that was
wrong** — agreement is defined only where both sides attribute, so it is silent
about precisely the windows the dimension is for. `tooling` stays out, with the
bar for revisiting it written into `prior.py` and its test. `PRIOR_DIMENSIONS` is
**derived** from `dimensions.ALLOCATION`, so an INVENTORY level is structurally
not addable — which is what keeps `named_terms` out by construction rather than
by care.

**The block cutter (`analysis/blocks.py`) — where a piece of work ENDS.** Two
terminators and the set is closed: **idle** (`IDLE_BINS` 3 = 15 minutes of
silence) and **budget** (`MAX_BLOCK_MINUTES` 20). Reported separately
(`REASONS`), because a reader who cannot tell them apart cannot tell an
arithmetic boundary from a real pause. Both numbers are MEASURED, in a
pre-registered four-arm study over 496 sessions; retuning either re-opens it.
**Blocks tile the ACTIVE part of a session, not `[lo, hi)`** — the invariant is
*every active bin lies in exactly one block*, never "the blocks cover the span".
⚠️ **There is NO merge rule, and the absence is the design.** The obvious repair
was built and measured: it changes a published VALUE in **88.6%** of the merges
it performs, against a pre-registered 5% bar. A thin block publishes
UNATTRIBUTED and survives as its own block. Do not add a merge rule and do not
add a knob for one — a knob is a merge rule with the decision deferred.
⚠️ **A THIRD terminator was ABLATED and every measured number improved**
(attributable 95.29% → 96.21%; the detector was the ONLY source of empty blocks).
`EwmaSizer` was **not** removed — it keeps its separate, measured use sizing the
dynamics slice. `_form` keeps the `cuts` parameter it is now always handed `[]`
for, so the shipped arithmetic stays identical to the measured arm.
⚠️ **`cut()` requires BIN-ALIGNED bounds and fails SILENTLY without them** —
evidence lands in no block, nothing errors, and no block looks wrong. The caller
owns the alignment (`analyze._block_span`); it is deliberately not clamped inside
`cut()`, because the study oracle pins that function byte-identical to the
measured arm.
**`/analyze` reports the block BESIDE the window, never instead of it** — an
additive `block` key carrying the span and the two boundary reasons and nothing
else, because the two definitions of thinness in this codebase disagree (95.3%
per-level against 99.3% pooled). Whoever adds the first consumer picks the
per-level measure deliberately. Phases 2-5 of
`docs/superpowers/specs/2026-08-25-signal-block-pipeline-design.md` are **NOT
built**.

Every study, bar and distribution behind the numbers above:
**`docs/architecture/window-analysis.md`**.

**Model backends.** `ml_backend` (local, **startup-only**, `settings.Settings`)
selects one of three modes. The rule they share: **no facet is ever silently
swapped for a lower-fidelity substitute of itself.**
- **`"auto"`/`""` (compiled-in default)** — enrichment is **ML-only** for its full
  facet set. A sidecar that is reloading, evicted or not yet provisioned is
  **waited out**: jobs queue/spool until it is ready.
- **`"deterministic"`** — enrichment stays **on** with a `nil` `enrich.Model`. A
  *different* facet set that needs no model (credential detection, and the
  workstream dimensions `/analyze` derives from coordinates), **not** a fallback.
  **The analysis service still runs**; only the model is never loaded, and never
  even provisioned. When a sidecar is installed the readiness gate polls that
  service's **`/health`** (in the background, behind a cached atomic — an inline
  probe would cost thousands of loopback connects per deferred job); a
  present-but-unhealthy service keeps jobs queued. When **no sidecar is
  installed** (or its port cannot be allocated) the gate is **trivially true** and
  the analyzer nil — nothing can arrive this daemon lifetime, so waiting would
  wedge the mode forever; the workstreams pass simply never registers.
  ⚠️ **The supervisor never gives up**: after `maxRestarts` consecutive failed
  starts it RESTS (1 min doubling to 30) and retries; `RequestRestart` ends a
  rest, and a child answering `/health` resets the budget. The readiness deadline
  counts AWAKE time, and both sleep detectors compare wall-clock instants
  (`Round(0)`) — macOS's monotonic clock stops while asleep.
- **`"off"`** — enrichment is **disabled entirely**: no worker, `/enrich`
  accepts-and-discards (202). Telemetry and client-events are unaffected.

**A SKIP IS NOT A FAILURE.** `runStage` is tri-state
(`passOK`/`passFailed`/`passSkipped`): a deterministic run whose every executed
pass succeeded publishes `pipeline_status:"enriched"` and names what it dropped in
`facets_skipped`. `"partial"` keeps its one meaning — something that should have
worked did not. **A HALF-run pass is neither**: a pass declares reduced capability
per job via `degradedExtractor`, commits normally, and is named in the **sibling**
`facets_degraded`. Both lists are subsets of the `extractor_versions` keys; a pass
that was never *registered* is absent from both. Neither moves `pipeline_status`.

⚠️ **WHAT A FRESH INSTALL LANDS ON IS NOT THE COMPILED-IN DEFAULT.**
`keld-agent install` writes `{"ml_backend":"deterministic","blocks":true,
"attribution":false}` via `settings.WriteInstallDefaults`, which **MERGES** so an
operator's other keys survive. That is v2: the model-free facet set plus the block
emitter, and **no multi-gigabyte model download, ever**. ⚠️ `attribution` is
written OFF unconditionally — until 2026-09-09 it was written as a copy of
`blocks`, i.e. ON, which switched on a 1.2 GB download and on-device text reading
for someone who had chosen nothing. The config write happens **first** in
`runInstall`, before login, because `ml_backend` is read at startup and never
re-read; the restart inside `installService` is what makes it take effect. A test
pins that order.
The **compiled-in defaults are unchanged** (`auto`, `Blocks` false) so every
machine an installer never writes to — an in-place upgrade, `go run`, CI, the eval
harness — keeps the full ML facet set.
⚠️ **`ml_backend` has NO REMOTE OVERRIDE.** The installer is the only lever that
will ever exist, so a re-install FLIPS an existing `auto` machine and an existing
fleet's Atlas Context column empties machine-by-machine with **no server-side
brake**. `--backend auto|deterministic|off` is the manual path back.
⚠️ **`make install-linux` routes through `keld-agent install` too**, so a dev
machine converges on `deterministic` — pass `--backend auto` to keep exercising
GLiNER2. And ⚠️ **do not run `keld-agent install` to test the config write**:
`KELD_HOME` isolates `~/.keld` but NOT the service path.

⚠️ **FIRST SIGHT BACKFILLS** (`KELD_BLOCKS_BACKFILL`, default ON) — a reversal of
the forward-only default, which left every already-closed block permanently
unreachable. The analogy to `KELD_WATCH_BACKFILL` does not hold: a block backfill
is a query against a store that already holds the answer, bounded to
`maxPerSweep` (24) per transcript per sweep. Re-emission is free — a block's
identity is `(session, block.start)` and Atlas upserts. ⚠️ It needed two more
things, each silent without the next: first sight must **signal at all** (the
PROMPT path stays forward-only — offering every historical prompt is a real herd),
and those signals must be **PACED** (`firstSightPerPoll` 4; firing all of them at
once dropped ~2,088 of 2,152 permanently). A refused signal is **retried**, not
dropped.

⚠️ **`KELD_DEV_BLOCKS` (the `prompt`/`bin`/`minute` developer granularities) needs
both halves of the daemon to agree.** Their boundary reasons are the mode names,
so `enrich.DevBlockReasons` mirrors `devblocks.MODES` (pinned by reading that
Python file), stays DISJOINT from `enrich.BlockReasons`, and is admitted per
client (`Client.AdmitDevBlockReasons`) only by the caller that resolved the
granularity. The granularity is refused unless Atlas is a LOOPBACK mock, and
`sidecarEnv` assigns the RESOLVED value **always, empty included**, so a refused
mode overrides what the child would inherit; the same single resolution decides
admission. A refusal that discards work says so, once per run
(`BlocksAnswer.DroppedUnreadableReason`) — never a silent `continue`.

**Delivery reliability (never degrade, never wedge)** and **deadlines are PER
PASS, not per job** (`KELD_ENRICH_PASS_TIMEOUT`, 30s): a job issues 8-9
inferences, so a job-wide budget discarded every pass that had already succeeded
and re-spooled the whole job. Bounded per pass, a slow pass costs exactly one
facet and progress is monotonic. `KELD_ENRICH_JOB_TIMEOUT` (5m) is only a **wedge
backstop** and must stay above `passes × pass timeout` or it resurrects that
failure mode — a unit test pins the invariant. An exhausted job
(`KELD_ENRICH_MAX_ATTEMPTS`, 4) is `spool.Quarantine`'d, never retried forever;
Atlas dedups on `dedup_key`.

**What each lane buffers, replays and loses is written down per lane in
`docs/durability.md`**, with the spool path, cursor or ring size cited for every
claim — including the three places where something is genuinely lost (a
`promptlog` observation made while unpaired, client events emitted before the
reporter starts, and the rows a feature flush drops past its first failing
chunk). The page carries the one-sentence version beside the health strip
(`ui/app.js` · `durabilityNote`), pinned against that document from both sides.

Each mode's full behaviour, the installer ordering, the backfill and dev-block
incidents:
**`docs/architecture/model-backends-and-install-defaults.md`**.

**Tick-driven window characterisation (`daemon/tick.go`, sidecar `/tick`) — OFF
by default (`KELD_TICK`).** Enrichment fires per prompt and every window looks
**back** 60 minutes, so the work a prompt *causes* falls outside that prompt's own
window: measured, only **55-56%** of turns lie inside some prompt's look-back, and
it is worse the more autonomous the agent. A tick characterises the gaps (99.5%
after).
- **The frontier is the whole no-double-publish guarantee:** never emit above
  `min(watermark, now - span)`. Exact, not a margin. The price is latency only,
  and **nothing safety-relevant waits for a tick** — `sensitivity` and every text
  facet keep their per-prompt trigger; this path never reads prompt text.
- **A timer, not the ingest signal** (a gap becomes emittable a span after the
  work, by which time the machine is quiet). Idle emits nothing structurally.
- **The covered set comes from the DAEMON, not the store** — the store's `prompt`
  index holds assistant-shaped turns too, and planning against it emits nothing.
- ⚠️ **The client half ships INERT, which is why it is off.** A tick row publishes
  under its own `corr_scheme:"window"` (it could not ride a prompt's correlation —
  Atlas upserts over every column and would OVERWRITE the anchor prompt's
  enrichment), so it is stored and **joins to nothing** until Atlas learns a
  time+identity join. Flipping the default is a one-line change the day it does.

Coverage measurements and the state file's rules:
**`docs/architecture/window-analysis.md`**.

**Adaptive input truncation (`enrich/lenstat`).** gliner2's `max_len` defaults to
`None` — *no truncation* — so one long prompt could allocate a multi-GB spike. The
daemon tracks the streaming mean/variance of prompt lengths (Welford; **lengths
only, never text**) and truncates at **mu + 2*sigma**, clamped to
`[KELD_ENRICH_TOKEN_FLOOR (512), KELD_ENRICH_TOKEN_CEILING (768)]`, staying at the
ceiling until 200 observations make the estimate representative. The floor means
the adaptive cap can only ever *widen* the window; the ceiling is the memory
budget expressed in tokens and is a hard invariant. Ceiling values are
**measured** (the table in `lenstat.go`) — cost is superlinear in memory *and*
latency. Credential detection is unaffected; NER-derived PII sees only the window.
⚠️ **This is sized for chat-scale prompts and does NOT extend to agentic-workflow
payloads.** Three things break: mu+2*sigma is meaningless on a bimodal
population; no token cap both admits such a payload and fits the budget (>1
MB/token measured, so ~4000 tokens implies ~7 GB); and head-truncation discards
the work prompt when the system prompt leads. Read
`docs/superpowers/specs/2026-07-24-agentic-scale-input-bounding.md` before
extending enrichment to agentic sources.

**Control plane.** Enrichment is governed per-org from Atlas
(`settings/`, `agentcfg/`); the daemon polls `GET /v1/enrichment-settings`
(`KELD_SETTINGS_POLL`). Remote overrides local; non-fatal if Atlas is unreachable.
See `docs/enrichment-settings.md`.

⚠️ **The version source is ATLAS, NOT `releases/latest`.** A client that resolves
`latest` itself converges the entire fleet the moment a tag is pushed — the same
defect as `ml_backend`'s missing brake, with a faster fuse.
`settings.Remote.Release` (`agent_release`) carries `{enabled, version, base_url}`
and **an absent block means NO UPDATE**. `KELD_AUTOUPDATE=0` refuses locally;
**local refusal wins, local permission never does.**
⚠️ **`version` is a PIN, not a floor** — the daemon moves to it in EITHER
direction, because a control plane that can only move a fleet forward is not a
brake. Comparison is identity after normalizing one leading `v`, never semver
ordering.
⚠️ **A missing published SHA-256 is FATAL here** while `install.sh` warns and
continues — the installer has a human who can abort; an unattended swap does not.
⚠️ **The macOS `.pkg` cannot be updated in place and its symlinks are the trap:**
the daemon migrates to `~/.local/bin` and repoints the LaunchAgent via
`service.InstallAt` (not `Install`, which reads the stale `os.Executable()`);
`Swap.Replace` uses `os.Lstat`, never `os.Stat`; and `/usr/local/bin/keld` cannot
be rewritten at all, so `keld signal doctor` names it with the exact `ln -sf`.
⚠️ **Auto-rollback alone is unstable — `failed_versions` closes the loop.** Past
`KELD_UPDATE_CONFIRM_DEADLINE` (15m) the swap is undone whichever version is
running (a stale marker IS the crash report), and a rolled-back version is never
retried **until the pin moves**. A failed restart leaves the marker PENDING on
purpose; a rollback that cannot restore does **not** restart.
⚠️ **WHO SETS THE PIN IS AN OPEN ATLAS-SIDE QUESTION, AND `base_url` IS THE SHARP
EDGE. As of 2026-08-27** nothing serves `agent_release`, so the client is inert.
`checksums.txt` is fetched from the SAME `base_url`, so it proves the transfer was
not corrupted, never that the bytes came from Keld — write access to
`agent_release` is therefore equivalent to root on every machine in the org.
Three ways out are recorded in `docs/auto-update.md`; **none was implemented on
that date.** Choose before the first real rollout and record the choice WITH THE
DATE it was taken.

Full rationale and traps: **`docs/auto-update.md`**. Spec:
`docs/superpowers/specs/2026-08-27-signal-auto-update-design.md`.

**The signal-embeddings publish half (`internal/agent/features/`).** An emitter
with a per-transcript cursor, the sibling of `internal/agent/blocks/`. Rows ride
`publish.FeatureRow` under their **own `corr_scheme`**, never `Enrichment` or
`BlockEnrichment` — Atlas keys enrichments `UNIQUE(org_id, source_id, corr_scheme,
corr_id)` and upserts over every column, so sharing a scheme OVERWRITES rather
than dedups. The corr id is `session@feature@anchor@key`: four segments against a
block id's two and a prompt id's zero, so the id spaces are disjoint by SHAPE as
well as by scheme. Transport is `clientevents`' batch path with its **own** spool
dir — a shared one would cross-post bodies between routes.
⚠️ **The cursor advances on BUFFERING, not on delivery**, because a batching path
cannot observe delivery. Backpressure is what makes that safe: a sweep never takes
more rows than the buffer has room for, so a full buffer HOLDS the cursor.

**Four toggles, all OFF by default, and they are not interchangeable.**
`KELD_CAPTURE` (extra ingest rows; ⚠️ fingerprinted into `parse_state`, so
flipping it forces one reparse — which is why turning publishing off must never
cost a reparse to turn back on), `KELD_TEXTEMBED` (the encoder child),
`KELD_FEATURES` (compute and store locally), `KELD_FEATURES_PUBLISH` (send to
Atlas). The last two carry an Atlas per-org override riding the settings poll
(`Remote.Features`, `Remote.FeaturesPublish`); remote overrides local, and an
**omitted** key leaves the local base rather than defaulting on, so a silent
fleet-wide enable is not reachable from the server. The whole subsystem registers
only under `ml_backend:"deterministic"`; under `"auto"` it is **ABSENT** — in
neither `facets_skipped` nor `extractor_versions`.

**PROJECT ATTRIBUTION (`internal/agent/attrib/`, `daemon/attrib.go`,
`sidecar/app/analysis/attribution.py`, `sidecar/app/verifier.py`) — which declared
project a closed BLOCK belongs to, decided on device.** OFF by default
(`KELD_ATTRIBUTION`, or `attribution` in `~/.keld/agent-config.json`). An org
declares projects (`settings.RemoteProject`) via `KELD_PROJECTS_FILE` or the
settings poll; the daemon pushes them down with `POST /projects` and the block
emitter's `OnPublished` hook schedules a durable job per published block.
`POST /attribute` takes COORDINATES and the block's already-computed dims and
answers with project IDS, confidences, closed enums and integer timings — **no
text, no span, no offset, in either direction.**
- ⚠️ **TURNING ATTRIBUTION ON STARTS ENCODING MESSAGE TEXT ON DEVICE.**
  `daemon/sidecarenv.go` sets `KELD_TEXTEMBED=1` (set-if-absent) because
  `/attribute` needs the same encoder. Nothing derived from it is *published* by
  that, but the encoder does read message text locally, which is new behaviour on
  a machine that had the toggle off. `KELD_TEXTEMBED=0` explicitly still wins.
- **NOTHING is assigned without the encoder.** `BOOST_CAP` (0.35) sits below
  `THRESHOLD - BAND` (0.41) by construction, so a boost-only score cannot cross
  the bar; a machine with no weights answers `degraded:weights_unavailable` and
  re-attributes later. There is exactly one attribution path, the benchmarked
  one — `source` is `embedding|verifier` and "metadata" is **not producible**.
- **The decision is RELATIVE, not an absolute bar:** `cut = max(null, top -
  MARGIN)`. `NULL_DOC` is embedded beside the projects, so a project attributes
  only by BEATING "nothing" in the same ranking. `MARGIN` (0.08) answers shape;
  `VERIFY_HALO` (0.04) is where the verifier adjudicates.
- ⚠️ **WHAT IS SCORED CHANGED ON 2026-09-03: the WHOLE BLOCK, mean-pooled,
  centred.** User text alone put **28%** of 61 real labelled blocks on the right
  project; whole-block mean-pooled and centred put **92%** there. MEAN beats MAX
  (92% vs 82%), and **24 of 25 blocks with no user text at all** are agent
  continuations that only attribute this way. The feared failure — attributing to
  what the assistant happened to name — did occur, on 4 of 61. Only the USER's
  words still feed `concepts` and the verifier prompt. **Centring**
  (`attribution.Offsets`) is a running mean of SCALARS, gated all-or-nothing at
  `KELD_ATTRIBUTION_MIN_BACKGROUND` (50) messages. Rollback is one variable:
  `KELD_ATTRIBUTION_SCORING=user-max`.
- **⚠️ TWO MODEL CHILDREN AND A NEW NATIVE DEPENDENCY, ON THE SAME BUDGET.** The
  text encoder is the SAME child the signal-embeddings path uses (one provisioner,
  never two). The **verifier** (Gemma 4 E2B Q4_K_M GGUF, ~3 GB,
  `llama-cpp-python` — the repo's first compiled native dependency, pinned exactly
  for that reason) runs in its own recycled worker child and is
  **`KELD_ATTRIBUTION_VERIFIER=1` opt-IN, OFF by default since 2026-09-03**: the
  one real-data A/B went 1-for-3 for minutes of CPU per block, and every figure in
  `docs/notes/whats-next-attribution.md` §8 was measured without it. So
  `KELD_SIDECAR_MEM_BUDGET_MB` is spent by **three** children; the overrun is
  reported, not absorbed. ⚠️ **Both children must be polled by `lifespan`'s one
  poll loop** — `poll()` is the sole driver of the RSS ceiling, the recycle, the
  idle unload and the pressure eviction. A manager that is constructed is not a
  manager that is guarded.
- ⚠️ **`llama_cpp` must be in the PyInstaller spec, and its absence is
  invisible** — a ctypes binding imported inside `Verifier.__init__`, itself
  PyArmor-encrypted under `KELD_OBFUSCATE=1`. A spec missing it ships a binary
  that starts, is healthy, and fails EVERY verdict. `make freeze-check` /
  `make obfuscate-check` spawn the verifier child and demand a real verdict, and
  that arm FAILS rather than skips when no GGUF is present. ⚠️ **BUT IT IS
  DEVELOPER-MANUAL TODAY, NOT CI** — nothing under `.github/` invokes it, so
  **nothing automatically stops this defect returning.**
- **The two model downloads are gated on a KNOWN NON-EMPTY project list**, read
  live per published block. ⚠️ **`skipped:no_projects` is NON-TERMINAL while the
  daemon holds a list**, and the daemon re-posts after a sidecar respawn
  (`Supervisor.SetOnRespawn`): module state in the parent does not survive a
  crash-restart, and a change-gated POST concluded there was nothing new to say.
  Anything else the daemon pushes DOWN once belongs on that hook.
- **Version skew HOLDS rather than quarantines** (`AttributeResult.RouteUnsupported`
  on a 404); a genuine quarantine emits `attribution.job_quarantined`.
- ⚠️ **A block lands in EVERY project that matches it, and Signal has no groups**
  (2026-09-23 / 2026-09-25). Overlap is the model, not an error: the rule pass
  (`projects.Attribute`) never refuses and never produces `ReasonConflict` (the
  constant survives only so an old ledger row reads); there is no precedence.
  Totals are per project, each counting every block it holds in full, while
  `coverage` counts a shared block once; Signal sends every id it assigned and
  Atlas decides what overlap means. The sidecar runs ONE pooled competition and
  the daemon posts no `group` — a group survives only as 3.0.6's stored
  container (see *Vocabulary*).
- **Quality.** `sidecar/app/test_attribution_quality.py` (opt-in,
  `KELD_ATTRIBUTION_EVAL=1`) scores the *shipped* configuration and its floor is a
  regression tripwire under THAT measurement, not the design gate. MARGIN and
  VERIFY_HALO await calibration on LABELED REAL blocks — the correction flywheel,
  not another synthetic sweep.

Full scoring rationale, the evaluation and the packaging traps:
**`docs/architecture/project-attribution.md`**. Follow-ups:
`docs/notes/whats-next-attribution.md`. Runbook: `docs/attribution-smoke.md`.

**`keld signal doctor` / `status` report on-device model state**
(`internal/localagent/models.go`). ⚠️ **Presence is a filesystem stat, never a
daemon probe**, and that is what makes it correct rather than merely cheap: no
endpoint exposes GLiNER2 weight-presence at all, the encoder's `/metrics` field
only answers while the sidecar is up, and a CLI that cannot reach the daemon does
not thereby know a model is missing. Reading disk makes daemon reachability
irrelevant, so "unreachable" can never render as "absent" — the same
`thin`/`absent` discipline the rest of this codebase runs on. ⚠️ **A model that
is absent but NOT NEEDED is not a problem and must not be reported as one**, or
every v2 user is nagged forever about a 1.9 GB model they will never load:
GLiNER2 is needed only under `"auto"`, the encoder only when `KELD_TEXTEMBED` and
the local `features` toggle are both on. When one IS needed and absent, the line
states the reason and that the work defers rather than fails. Neither command may
trigger a download or a model load. Known limit: `Needed` resolves from
local-only config, so it is blind to an org remote override — the same limitation
`ml_backend` already has.

**Auth & self-heal.** Both client tokens are long-lived and revoke-only (no
TTL) — the CLI token (`~/.keld/auth.json`) and the org ingest token
(`~/.keld/hook.json`) — so normal background operation needs no re-auth. The
daemon reads the ingest token at startup but self-heals on a persistent
publish/settings `401`/`403`: it re-fetches the current ingest token via
`Onboarding` using the CLI token (`auth.Load`), live-swaps it into the shared
`creds.Token` (publish/settings/client-events all pick it up from there),
rewrites `hook.json`, and emits an `auth.refreshed` client-event.
Single-flight + cooldown (`KELD_REAUTH_COOLDOWN`, default 60s) turns a burst
of 401s into one re-onboard. Token-only: an endpoint change instead logs a
"restart to adopt" warning. If the CLI token itself is gone/revoked, the
daemon writes `~/.keld/reauth-required` and logs loudly; `keld signal
status`/`doctor` and `keld-agent status` surface it — recovery is `keld
login` then `keld-agent restart`. ⚠️ **Known limitation, and it is WIDER than this said.** It read "a job that
hits the 401 mid-rotation isn't itself re-spooled", which named the case it was
discovered in rather than the case the code has. `process` returns false on
**any** failed `pub.Send` — a timeout, a 5xx, an unreachable Atlas — and the
worker's re-spool/quarantine branch is reached only when a job missed its
DEADLINE (`finished == false`). So no failed publish is re-spooled, whatever
caused it, and the 401 is simply the one that was looked at. The prompt survives
only because `queue.Complete` is marked on a real publish and not before, which
leaves the watcher free to re-offer it.

The daemon still recovers forward for subsequent jobs, and lossless re-spool of
a failed publish remains the follow-up — now correctly scoped. Found while the
durability document was citing the code for each of its claims, which is the
argument for making a document cite its sources.

**Client-events telemetry (`internal/agent/clientevents/`).** Separately from
enrichment, the daemon emits structured **operational** events about itself —
job retries/quarantines, sidecar crashes/fallback, publish failures, resource
pressure, lifecycle — batched and POSTed to `POST /v1/signal/client-events`
(`x-keld-ingest-token`, same header convention as publish/settings). This is
the first route under the **`/v1/signal/*`** convention: the namespace for new
client↔Atlas protocol routes going forward (`/v1/enrichments` and
`/v1/enrichment-settings` predate it and are not renamed as part of this — a
later coordinated migration). Governed per-org via a `client_telemetry` block
riding the existing `/v1/enrichment-settings` poll (default ON, independent of
the enrichment toggle). Events carry only ids + structured primitive metadata;
a Go-side **privacy redaction gate** (`clientevents/redact.go`) strips
absolute paths, drops non-primitive field types, and reduces errors to a
class+summary before anything is buffered — never raw prompt text, matching
the same invariant enrichment upholds for masked spans. Durable like
enrichment: batched + periodically flushed, retried via `internal/retry`, and
spooled to `~/.keld/spool/clientevents/` (bounded, drop-oldest) when Atlas is
unreachable. Full wire contract (envelope, event/code catalog, settings
defaults, redaction guarantee): **`docs/signal-client-events.md`**.

**Resource safety (the sidecar is a good citizen).** Single-flight + bounded queue
(503 backpressure); a **rate governor** (CPU-EWMA min-interval pacing) and a **CPU
thread scaler** (capped to host load, default 50% of cores). Inference runs in a
separate **inference worker child**, not the long-lived FastAPI service, and is
**recycled** — killed and respawned, reclaiming its heap via process exit, the
only cross-platform memory reset — on an RSS ceiling, memory pressure, idle
(`KELD_SIDECAR_IDLE_UNLOAD_S`), a hung-job timeout, or a crash.

⚠️ **A SUPERVISOR KILL REAPS THE PROCESS GROUP, NOT THE PID IT CAN SEE.** Every
mechanism above assumes killing the sidecar reclaims what it held, and that was
false until `e40dd53`: SIGKILL to the pid alone left `lifespan`'s teardown unrun
and the `multiprocessing` children reparented to init, holding **2.9 GB** and
**0.55-1.9 GB**. The fix is `Setpgid` at spawn plus `stopChild`: **SIGTERM to the
sidecar ALONE** so the teardown can run, then **SIGKILL to the GROUP
unconditionally**. ⚠️ Two changes elsewhere are what make it work, and removing
either makes it silently inert while every test passes: `sidecarService` uses
`exec.Command`, not `CommandContext` (whose cancel hook SIGKILLs first), and `Run`
waits on `AwaitSidecarStop`. `KELD_SIDECAR_STOP_GRACE` (5s) is a BOUND, not a
budget. **Windows is PARTIAL** — `taskkill /T` reaps the tree but no SIGTERM is
reachable, so `lifespan` still does not run there.

⚠️ **The guard must not sample under the inference lock.** Sampling while holding
it could only ever measure the trough between jobs: RSS oscillated **2715 → 5692
MB against a 3409 MB ceiling with `recycles == 0`**. So `observe_rss()` samples
**lock-free**; the **RSS ceiling** is a baseline-drift guard decided only when the
lock is free, taken **non-blocking**; and a **hard limit** enforced on the
lock-free sample kills the worker even mid-job. ⚠️ **A lock-free sampler behind a
blocking caller is not a lock-free sampler** — the encoder child reintroduced this
exact shape and is now pinned by `app/test_guard_visibility.py`.

**The parent's share of the budget is MEASURED, not assumed** —
`parent_reserve_mb()` returns `max(constant, high-water parent RSS)`. **High-water,
not live**: a limit tracking a live sample would relax when the parent dipped,
with nothing about the risk having changed. **And the composition must be monotone
too**: `hard_limit_mb()` is `max(budget - reserve, ceiling + hard_margin)`, after a
step discontinuity where 2 MB of parent growth bought the worker 511 MB.

**The honest consequence: at the delivered defaults the budget cannot be met** —
parent 619.6 + ceiling 3409 + margin 512 = **4540.6 MB against a 4096 MB budget**.
The margin wins, and the overshoot is **reported, not absorbed**
(`budget_shortfall_mb()` in `/metrics`, plus one loud line per **worker
generation** — not per poll, which is the same as never warning). Which term gives
is an operator's decision the code must not make silently.

**`KELD_SIDECAR_MAX_CHARS` (24000) is a tokenizer-cost guard, not the memory
bound** — memory scales with *tokens*, and it must stay generous enough never to
pre-empt the token cap. **Footprint caps are set at spawn, parent-side**
(`MALLOC_ARENA_MAX=2` plus the `*_NUM_THREADS=2` family, all set-if-absent).
`MALLOC_ARENA_MAX` **must** be parent-set: glibc reads it when the child's
allocator initializes, before Python can set it for itself. Without it RSS
balloons to ~2x the model working set (measured 6.4 GB against ~2.6 GB).

The incidents, the metrics block and load-test validation:
**`docs/architecture/sidecar-resource-safety.md`** and
`sidecar/loadtest/README.md`.

## The CLI (`keld`)

`cmd/keld` → `internal/cli`. Browser-based device-authorization login
(`internal/auth`), tool detection + config editing with summary/diff + backups
(`internal/tools`, `internal/diffview`), hook install (`internal/hook`). Commands:
top-level `login`/`logout`/`whoami`; the `keld signal` group
(`setup`/`status`/`doctor`/`uninstall`) for telemetry onboarding. Config paths via
`internal/paths` (`KELD_HOME`).

**Machine interface (installer-/automation-driven onboarding).** `keld login` and
`keld signal setup` each take `--json`, emitting **NDJSON** on stdout (one event
object per line) instead of human text — the seam the native platform installers
drive to render the device code + setup progress in their own UI. `keld login
--json` emits `device_code` (immediately) then `authorized`/`error` (non-zero exit
on error); `--no-browser` suppresses the auto-open so the caller owns the link.
`keld signal setup --json` is non-interactive (implies `--yes`): a `tool` event per
tool (`configured`/`already_configured`/`skipped_conflict`) then `done`. Keep all
auth/setup logic Go-side behind the `onStart` (auth) and `SetupOpts.Emit` (setup)
seams — don't reimplement it in installer code; the human paths stay unchanged when
those seams are unset. **`keld-agent install` installs and nothing more**: it
registers the service and prints how to finish — the app's Settings pane, or
`keld login && keld signal setup`. Onboarding is OPT-IN, via `--code <CODE>`
(non-interactive, the installers' path) or `--login` (browser device flow, gated
on `term.IsTerminal` — `os.ModeCharDevice` is wrong because macOS launchd wires
stdin to `/dev/null`).
⚠️ **THAT DEFAULT WAS INVERTED UNTIL 2026-09-05, and the old one had EXPIRED
rather than been chosen.** `install` used to log in whenever stdout looked like a
terminal, with `--headless` to opt out — correct while the CLI was the only
onboarding surface, because an install that did not onboard left a daemon idling
with nowhere to be told about Atlas. `POST /v1/config` plus daemon/onboarding.go
(the daemon now serves the page and that route BEFORE it has any config) removed
the constraint, so the flag was protecting a dead end that no longer exists.
`--headless` is kept ACCEPTED AND INERT — it asks for what already happens —
because cobra fails hard on an unknown flag and scripts, runbooks and MDM
payloads outlive a release. Both `onboard.command` and `onboard.cmd` pass
`--login --yes` on their fallback path; they relied on the old default and would
otherwise have silently stopped onboarding anyone whose setup code failed.

## Repo layout

```
cmd/keld/            CLI entrypoint
cmd/keld-agent/      enrichment daemon entrypoint
internal/
  agentcli/          keld-agent cobra commands (run/install/uninstall/...)
  agent/
    ingress/         loopback /enrich intake (auth + bounded queue)
    queue/           bounded, key-deduping job queue (backpressure)
    resolve/         read prompt text + recent-prompt tail from transcripts
    enrich/          the pipeline: extractors, passes, labels, mask, meta
      sidecar/           HTTP client to the GLiNER2 sidecar (the only Model)
      lenstat/           adaptive input truncation (mu+2sigma prompt-length stats)
      creddetect/        deterministic credential detection (vendored gitleaks rules)
      eval/              enrichment quality eval harness
    provision/       model provisioning (weights → ~/.keld/models); GLiNER2 and
                     the Qwen3 text encoder, both on demand, neither at startup
    blocks/          the v2 block emitter + its per-transcript cursor
                     (KELD_BLOCKS enables it; KELD_BLOCKS_BACKFILL, default ON,
                      decides what FIRST SIGHT of a transcript does)
    features/        the signal-embeddings emitter + its cursor (KELD_FEATURES)
    projects/        projects: the local document (projects.json),
                     the rule pass (Attribute), suggestions, the page's edits
    attrib/          the semantic attribution job (POST /attribute) per closed block
    update/          auto-update: Atlas pins a release; fetch, verify, swap by
                     displacement, restart, confirm — or restore .prev and
                     never retry that version until the pin moves
    teleproxy/       loopback OTLP receiver: tools post here, the daemon forwards
                     with its own token so no tool ever holds an Atlas credential
    publish/         build + POST masked enrichments to Atlas; block, window and
                     feature rows each under their own corr_scheme
    settings/ agentcfg/  per-org control-plane polling
    service/         OS service install (darwin/linux/windows)
    daemon/          wires it all together; spawns/superwises the sidecar
                     procgroup_*.go: a kill reaps the GROUP, not the bare pid
  geminichat/        THE ONE place that knows Gemini's chat file shape — BOTH of
                     them (a JSON document and JSONL), decided by CONTENT rather
                     than by extension. watch, resolve and the conformance
                     checkpoint all read through it, so the predicate deciding
                     which messages are genuine prompts cannot drift.
  spool/             durable on-disk pointer queue (hook fallback + re-spool/quarantine)
  auth/ cli/ tools/ diffview/ hook/ paths/ telemetry/ config/ console/ ...
sidecar/
  serve.py           entrypoint the daemon spawns (uvicorn on 127.0.0.1)
  app/
    main.py          FastAPI app: /analyze /ingest /pii /match /vocabulary
                     /classify /extract /entities /health /metrics; lifespan
                     wires governor + scaler + runner + worker poll loop
    governor.py      CPU-EWMA rate pacing         runner.py       single-flight runner
    cpuscale.py      host-load → torch threads    worker.py       inference child (holds model)
    metrics.py       /metrics payload             worker_manager.py  spawn/recycle/dispatch
    adapter.py       normalize model output
    pii.py           presidio recognizers, region tiers, numeric-fragment gate
    wellknown.py     published-test-value gate (the ONE place; see Conventions)
    analysis/        the analysis service — no model, off the inference lock
      store.py         SQLite reference series (~/.keld/state/refseries.db):
                       events, 5-minute bins, bin_level registry, retention
      ingest.py        incremental tail parse from a byte offset (== a full parse)
      analyze.py       window digest; analyze_window_by_parse is the ORACLE
      dynamics.py      what MOVED in the window + the EWMA slice sizer
      blocks.py        the block cutter: 20m cap + 15m idle, no merge rule
      blockdigest.py   characterise ONE block -> the v2 payload (POST /blocks)
      capture.py       raw-line pass: tool outcomes + bin offsets, no json.loads
      features.py      S(t), the 1,534-dim shell ladder (POST /features)
      featuretext.py   the text half's cache, background pass and FRONTIER
      textembed.py     per-message text vectors, in their own encoder child
      window.py        rollup / attribution / dominant; MIN_EVIDENCE
      levels.py        level vocabulary + 0.1s timestamp quantization
      dimensions.py    ALLOCATION + INVENTORY payload (the published shape)
      transcript.py    JSONL line seams (turns_in / tool_use_in)
      workspace.py     whole-file workspace + remote resolution
      reconcile.py     prose paths against declared paths (re-scoped per window)
      match.py vocab.py shell.py terms.py text.py paths.py   supporting passes
  loadtest/          smoke + soak load-test harness (see its README)
  keld-agent-sidecar.spec / build-freeze.sh   PyInstaller packaging
docs/
  architecture/      the subsystem detail this file points at (see Design docs)
  notes/             working notes + live follow-ups
  superpowers/       {specs,plans}
                     enrichment-settings.md, auto-update.md, durability.md,
                     ONNX decision, ...
scripts/             install.sh / install.ps1, send-test-prompt.py, enrichments-sink.py
```

## Building & running

```bash
make build-binaries    # keld + keld-agent (Go)
make sidecar           # create the Python 3.12 sidecar venv (~/.keld/sidecar-venv) + wrapper
make install-linux     # build-binaries + sidecar + install the systemd --user service
make send-test-prompt  # push one test prompt to the running daemon
make uninstall-linux   # remove the service
```

## Testing

```bash
go test ./...          # Go unit tests (59 test files)

# Sidecar unit tests — standalone scripts (no pytest), via the sidecar venv:
cd sidecar
for f in app/test_*.py loadtest/test_*.py; do
  PYTHONPATH=. ~/.keld/sidecar-venv/bin/python "$f"; done

# Sidecar load tests (opt-in; load the real model, minutes-long):
cd sidecar
PYTHONPATH=. ~/.keld/sidecar-venv/bin/python -m loadtest smoke   # ~2-3 min
PYTHONPATH=. ~/.keld/sidecar-venv/bin/python -m loadtest soak --minutes 45 --live
```

## Conventions

- **Privacy is the invariant.** Raw prompt text is read on-device and must never
  be transmitted; the daemon publishes only masked labels + masked spans. Masking
  is enforced Go-side (`enrich/mask.go`) before publish; the sidecar returns raw
  spans and never publishes.
  ⚠️ **`named_terms` was the one exception** (schema v18): proper nouns lifted
  from message text, published as term + count, with no person-name filter
  because none measured reliable enough to be honest (~1% precision — see the
  workstreams bullet). Still no raw text, no spans, no offsets.
  ⚠️ **IT IS NO LONGER THE ONLY ONE, AND THIS BULLET USED TO SAY IT WAS** — "the
  ONLY published signal not derived from tool-call inputs; do not add a second
  one by analogy to it." The second arrived deliberately, not by analogy:
  `KELD_TEXTEMBED` (off by default) encodes message text ON DEVICE and publishes
  a 256-d vector, MRL-truncated and then multiplied by a fixed orthogonal
  projection — cosine and inner products preserved exactly, so training is
  unaffected, while off-the-shelf inversion tooling (vec2text, ALGEN) needs a
  matrix the client does not choose and an attacker does not have. The encoder
  never leaves the machine; only its output does.
  **So "derived from text" stopped being the test.** The test is whether TEXT, a
  SPAN, or an OFFSET crosses — and it never does. That is the stronger rule and
  it is the one to enforce. A sentence embedding is invertible in principle
  (measured elsewhere at up to 92% exact recovery on 32-token inputs), which is
  precisely why the projection exists, why the toggle ships off, and why a THIRD
  text-derived signal is a decision needing its own evidence rather than an
  analogy to these two.
- **Config via env (`KELD_*`)**, resolved through `internal/config` /
  `internal/paths`; credentials/tokens/hook/manifest under `~/.keld` with
  user-only permissions.
- **CLI = single static Go binary**, no runtime deps. `ml_backend:"auto"`
  (default) enrichment is **ML-only** for its facet set: the GLiNER2 sidecar is
  mandatory and there is no deterministic *substitute* for those facets — when
  it isn't ready, jobs queue/spool until it is (see Delivery reliability
  above). `ml_backend:"deterministic"` is not that substitute: it is a
  different facet set that needs no model at all — it still runs the analysis
  service and gates on it being healthy *when one is installed*, it just never
  asks it to load the model; with no sidecar present it drops the window-analysis
  facet and runs the rest rather than wedge (see Model backends above).
- **Sidecar single-flight** (one inference at a time) is load protection, not an
  accident — don't fan out inference. RAM is bounded by recycling the inference
  worker child process, CPU by the governor + thread scaler (see
  `sidecar/loadtest/README.md`).
- **Schema versioning:** changing any enrichment vocabulary is contract-affecting
  — bump `enrich.SchemaVersion` and re-run the eval (`enrich/eval/`). The
  `sensitivity_spans[].label` set counts: v7 added the 25 region-scoped entity
  names, which moved every producer string from `-v6` to `-v7`.
- **Dependency-pull retries:** outbound "fetch a required dependency" calls use
  `internal/retry` (`retry.Do` + the canonical `IsTransient` classifier; policy
  env-tunable via `KELD_RETRY_*`). Transient = net faults + HTTP 408/429/5xx;
  **unknown errors are permanent by design** (never hammer). The HF model download
  (`sidecar/hf.go`) uses it; settings-poll / publish / api adopt it when next
  touched — don't hand-roll new backoff loops.
- **Never cut text mid-sentence.** Any text read as language — a prompt, a
  generated report, a conversation window handed to a model, a span shown to a
  person — is bounded at a **logical delimiter**: a sentence end, a line break, a
  turn boundary, an entry boundary. Never at a rune count that lands mid-clause.
  An **identifier is never truncated at all**: a path or symbol cut short is a
  *false* identifier, so drop the whole term instead. And **dropping must be
  visible** — `omittedNotice` is the precedent; a silently shorter input is the
  same defect one level up. Measured cost of getting this wrong: beats were
  generated with a 200-rune cap against a "two or three sentences" instruction,
  and **46 of 47 came out mid-clause with a median of zero complete sentences**
  — unusable output from a correct model, caused entirely by the cut.
  This does **not** govern the ML token caps (`lenstat`'s `max_len`,
  `KELD_SIDECAR_MAX_CHARS`), where head-truncation is deliberate and the
  constraint is activation memory rather than legibility.

## Gotchas

- **The sidecar needs Python 3.12** (host default may be 3.14 without torch/gliner2
  wheels). Use the venv at `~/.keld/sidecar-venv` (`make sidecar`); run its tests
  with that interpreter, never the host python.
- **Sidecar tests are standalone scripts** (no pytest); each ends with a
  `__main__` runner that runs every `test_*` function.
- **Distribution packaging** freezes the sidecar with PyInstaller
  (`keld-agent-sidecar.spec`) into `keld-agent-sidecar`; the daemon resolves it
  beside `keld-agent` (flat or nested layout).
- **macOS signing needs TWO certs:** *Developer ID Application* on **every**
  Mach-O in the payload (notarization rejects a whole submission over one unsigned
  lib), *Developer ID Installer* on the pkg. CI derives the identity names from the
  imported keychain, and each p12 must bundle the **G2 intermediate**.
  ⚠️ **The pkg ships WITHOUT the sidecar** (notarizing its ~15k files queued a
  submission 4+ hours), and **the installer does not download it** — except
  `postinstall` on a SILENT/MDM install. The daemon fetches it automatically, once
  per run, **pinned to its own release** (`internal/sidecarinstall`;
  `GET /v1/engine` reads DISK, `POST /v1/engine/install` answers 202); the page
  reports and only a failure offers Try again. `plugin_test.sh` inverts the old
  assertions so a download put back into the pane fails there.
  ⚠️ **The two halves ship separately, so their versions are COMPARED:** the frozen
  tree carries a root-level `VERSION` stamp (not PyInstaller `datas`), the pkg
  replaces a sidecar on mismatch or when it has no stamp, and the daemon emits
  `sidecar.version_skew` (doctor says the same). `dev` on either half means
  *cannot tell*, never skew. A `/blocks` 404 is `BlocksAnswer.RouteUnsupported` and
  the emitter HOLDS its cursor — a presence-only check once cost ~3 weeks of blocks.
  ⚠️ **Notarization is a HARD GATE** (`KELD_NOTARY_REQUIRED=1`, relaxed only on the
  no-secrets path); `KELD_NOTARY_TIMEOUT` (15m) is stall tolerance, and the
  submission id is written first. Pinned by
  `installers/macos/build_pkg_notarization_test.sh`.
  Full entry: **`docs/architecture/packaging-and-installers.md`**.
- **Obfuscation (`KELD_OBFUSCATE=1`, CI-set, default off).** The installer/release
  freeze obfuscates the shipped sidecar — python-minifier **locals-only** rename
  (globals/Pydantic-fields/spawn-targets preserved; annotations kept so Pydantic
  v2 + FastAPI still work) → free-tier PyArmor bytecode encryption → PyInstaller.
  `build-freeze.sh` freezes from a **copy** (never clobbers the tree), hard-fails
  if the tools are missing, and the `.spec` names the obfuscated `app.*` +
  `pyarmor_runtime` as `hiddenimports` (their imports are encrypted, invisible to
  PyInstaller analysis). Go binaries are `-s -w`-stripped by GoReleaser. Dev/local
  builds stay plain/debuggable. It's **license-ready** (a paid PyArmor license
  unlocks RFT/BCC via the same flow) and protects **code logic only** — it does
  **not** hide the base model (GLiNER2 is discoverable via bundled deps + the
  on-disk weights; explicit non-goal).
- **Frozen worker spawn needs `freeze_support()`.** The inference worker uses
  `multiprocessing` spawn, which re-execs the frozen binary to bootstrap the
  child; `serve.py` calls `multiprocessing.freeze_support()` so the child doesn't
  fall through to argparse. This only manifests in the **frozen** binary — unit
  tests never freeze, so it can't be caught there. `make freeze-check` (plain) and
  `make obfuscate-check` (obfuscated) run the freeze + a real `/classify`
  worker-spawn gate locally (Linux); CI's installer smoke does the same for every
  shipped OS. Any change touching the worker/spawn/freeze path must keep those
  green.
- **macOS onboarding UI** happens inside the installer wizard
  (`installers/macos/plugin/`), before the Install step — ⚠️ a pane cannot follow
  it, so everything interactive is pre-install and `scripts/postinstall` does every
  destructive step afterwards. Two silent failures (a `SectionOrder` entry missing
  `.bundle`; a bundle signed before its executable was recompiled) are pinned by
  `installers/macos/plugin_test.sh`. `postinstall` opens `onboard.command` only
  when the pane never ran AND no `hook.json` exists — never on either alone. See
  `docs/macos-wizard-onboarding.md`.
- **Windows onboarding UI** is `installers/windows/onboard.cmd`, opened by the
  `[Run]` step with `postinstall shellexec skipifsilent`; it reports success from
  OBSERVED STATE (an `ingest_token` in `hook.json`), never an exit code. ⚠️ **Do
  not re-add `runhidden` to that `[Run]` line** — it idled every Windows machine
  forever. The Inno `[Code]` wizard page an older doc described **never existed**,
  and nothing here is verified on Windows.
  Full entries: **`docs/architecture/packaging-and-installers.md`**.
- **Windows code signing is THREE passes, and the order is load-bearing.**
  Windows 11 ships Smart App Control on by default and SAC evaluates a binary **as
  it LOADS**, so a signed `keld-setup.exe` installing an unsigned `keld.exe` buys
  nothing — the install succeeds and the product is refused the moment it starts.
  The loose payload is signed BEFORE `iscc`, the uninstaller DURING the compile,
  the installer AFTER. All three use Azure Artifact Signing as `CN=Keld Inc`;
  there is no PFX and never will be.
  ⚠️ The payload signer is handed a **catalog**, never a recursive folder sweep —
  78 of 188 PE binaries arrive signed by their own vendors, and re-signing
  replaces attestations we have no standing to make. Verification re-reads that
  catalog, **never a fresh scan**, which would find every file Valid and could
  never report a gap.
  ⚠️ The uninstaller can only be signed by Inno, which needs a **command line** —
  so that one path uses the signtool dlib rather than the signing Action, and
  `PrepareToInstall` deletes a stale `unins000.exe` because Inno otherwise keeps
  an existing one forever and an upgraded machine could never be uninstalled.
  Full entry: **`docs/windows-code-signing.md`**.
- **Managed tool settings** (e.g. Claude Code org/remote-managed `settings.json`)
  override user settings — if telemetry goes nowhere, check the managed OTLP
  endpoint.
- **Model provisioning is ON DEMAND, never at startup.** The ~1.9 GB fetch is
  triggered by `Worker`'s **warmup** — the one call that actually loads the
  model — via `daemon/model_on_demand.go`, not by a goroutine kicked off in
  `mlBackendWithOpts`. It used to be the latter, which charged the download to
  every machine running the default mode whether or not it ever enriched a
  prompt. Consequences worth knowing:
  - The download is **not awaited inside the warm budget.** `warmWait`
    (default 120s) is sized for a model *load*; awaiting a multi-gigabyte
    download inside it would cancel the fetch on expiry, and since
    `EnsureModel` stages into a temp dir it removes on failure, every job would
    restart from zero. So `ensure` runs the fetch on the **daemon's** context,
    waits only as long as the caller's ctx allows, and reports "not ready yet"
    otherwise. Jobs defer + re-spool (no retry budget consumed) and the next
    one finds the download further along. Never a lower-fidelity substitute.
  - Success **latches**; a failure does not. `EnsureModel` verifies by
    streaming a SHA-256 over ~1.9 GB, so re-asking it per job would re-hash the
    weights on every prompt.
  - **`ml_backend:"auto"` is unchanged in meaning and still needs the model.**
    The default pipeline issues **5 inferences per prompt** (4 classify + 1
    extract — `task_type`, `activity_type`, `personal`, `subcategory`, and
    `domain`'s extract; `function_guess` is structural on a coding-tool source
    and `sensitivity` consults no model — was 6 before schema v9 dropped
    `speech_act`), pinned by
    `enrich.TestBuiltInPipelineStillDemandsAModel`. On-demand provisioning
    therefore *defers* the download to the first prompt; it does not remove it.
    Claims that "in v2 nothing loads the model" do not hold for the Go
    pipeline — measure before acting on them.
  - `"deterministic"` and `"off"` get **no warmup at all**, so they can no
    longer fetch weights they never use.
- **`hf.go` filters the siblings manifest** (`nonModelFile`): docs, git
  metadata and images are skipped — `fastino/gliner2-large-v1` ships
  `README.md`, `.gitattributes` and `image/GitHub.png` (4.4 MB), all of which
  used to be installed next to the weights. It is a **denylist by shape, not an
  allowlist**: gliner2 opens `config.json`, `encoder_config/config.json`,
  `model.safetensors` (`pytorch_model.bin` fallback) and hands the whole dir to
  `AutoTokenizer`, so a missed tokenizer/config file is a runtime load failure
  no unit test catches. `.txt` is deliberately **not** denied — `vocab.txt` and
  `merges.txt` are real tokenizer files.

## Design docs

⚠️ **This file was split on 2026-09-28** (it had reached ~216k characters, which is
past what is safely loadable). The rules and invariants stayed here; the
measurements, studies and incident post-mortems behind them moved, **verbatim**,
into `docs/architecture/`. Each of those files carries a dated provenance header
saying when its material was last substantively changed and that the split
re-verified nothing. **Nothing was deleted.** When a change touches one of these
areas, read its file before editing — the rule in this file will tell you what you
must not break, and only the linked file will tell you what it cost to learn.

| Area | File |
|---|---|
| Telemetry proxy, the telemetry secret, capture triggers, the transcript-first usage mirror | `docs/architecture/telemetry-proxy-and-capture.md` |
| Gemini capture: two chat shapes, the credential path, the router, the tool name | `docs/architecture/gemini-capture.md` |
| Startup without a config; an unpaired daemon collects | `docs/architecture/pairing-and-collection.md` |
| `sensitivity`: two sources, PII regions, the test-value gate | `docs/architecture/sensitivity-and-pii.md` |
| The `workstreams` dimensions, dynamics, the session prior, blocks, the tick | `docs/architecture/window-analysis.md` |
| The reference-series store: ingest, retention, capture, `/analyze` confinement | `docs/architecture/reference-series-store.md` |
| Text vectors, `/features`, the publish half and the four toggles | `docs/architecture/text-vectors-and-features.md` |
| Model backends, install defaults, dev blocks, delivery reliability | `docs/architecture/model-backends-and-install-defaults.md` |
| Project attribution, and a block in every matching project | `docs/architecture/project-attribution.md` |
| Sidecar resource safety and input bounding | `docs/architecture/sidecar-resource-safety.md` |
| Packaging and installers: signing, notarization, sidecar version skew, onboarding UIs | `docs/architecture/packaging-and-installers.md` |
| Auto-update | `docs/auto-update.md` |
| What each lane buffers, replays and loses | `docs/durability.md` |

Specs in `docs/superpowers/specs/`, plans in `docs/superpowers/plans/`; control
plane in `docs/enrichment-settings.md`; sidecar resource safety + load testing in
`sidecar/loadtest/README.md` (including the `embed` arm, which is what found the
encoder's real peak and the two defects behind it); the signal-embeddings training
corpus — what `S(t)` holds, why the digest is never serialised into the encoder,
the cursor contract, and the corrections its own measurements forced — in
`docs/superpowers/specs/2026-08-26-signal-embeddings-design.md`, with the
three-approach comparison behind it (and its three superseded conclusions marked
rather than deleted) in `2026-08-26-joint-embeddings-design.md`;
macOS Developer ID signing + the notarization stall
(**resolved** 2026-08-06 account-side after days of zero verdicts; verdicts now land
in ~25s, and what to check if it recurs) in `docs/macos-signing-and-notarization.md`.
Release asset completeness gate in
`docs/superpowers/specs/2026-08-10-release-asset-completeness-gate-design.md`.
