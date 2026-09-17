# The reference-series store: ingest, retention, capture and the analysis service

> **Provenance — split out of `AGENTS.md` on 2026-09-17** (at `67be5f5`), verbatim.
> The material below was last substantively changed in `AGENTS.md` on **2026-08-26**.
> Every measurement here is stated as it was when written; the split re-verified
> **none** of them. Treat an undated figure as "true when measured, unknown now",
> and re-measure before acting on one.

AGENTS.md → *The reference-series store* states the invariants. This file carries
the equivalence measurements, the retention policy's derivation and the
confused-deputy fix that confined `/analyze`.

- **The watcher signals ingest; the sidecar never polls.** `/analyze` answers out
  of a persistent reference-series store, and the parse that fills it is driven by
  the transcript watcher: a file that advanced in a poll is signalled once (per
  file, per poll) to the sidecar's **`POST /ingest`**, which parses only the
  appended tail from its own byte-offset checkpoint. Coordinates only — a path,
  never a line, never text. The seam is `watch.WithIngestSignal` (the coarse
  sibling of the per-line `observe` hook); the daemon-side policy is
  `daemon/ingestsignal.go`: **fire-and-forget** on a bounded, path-coalescing
  queue with one serial sender, so an unreachable sidecar can never block or slow
  the watcher's poll loop — the loop that carries every hook-free prompt. Signals
  are **dropped, not retried**, because ingest resumes from the stored offset: the
  next signal catches up, and `/analyze`'s own on-demand ingest is the backstop if
  none ever comes. Scoped to `enrich.WorkstreamsEligible` sources (the same
  predicate the pass is gated on — a Codex/Gemini window can never be served, so
  ingesting it is pure cost). Forward-only by default, matching
  `KELD_WATCH_BACKFILL`: a first sighting consumes nothing, so a daemon restart is
  not a herd of whole-file ingests. Why it matters: a first whole-file ingest
  measured **5.1s on a 90 MB transcript**, and inside an `/analyze` request that
  lands on an enrichment job's per-pass deadline.
- **Retention is bounded by TIME, and a pruned window REFUSES (410) rather than
  answering narrower.** The store's raw `event` rows expire at
  `KELD_REFSERIES_RETAIN_DAYS` (400) and the one text-derived level, `term`, at
  `KELD_REFSERIES_TERM_RETAIN_DAYS` (90); `KELD_REFSERIES_MAX_MB` (1024) is a size
  **backstop**, not the operating policy — measured, 1,552,800 rows (400 days at
  3,882/day) is 174 MB, so the cap is ~6-9 years and the horizon is what bites.
  ⚠️ **Pruning raw events does not degrade a window's edges — it breaks the digest
  outright.** `/analyze` serves every window with
  `exclude_slots=(RECONCILE_SLOT,)` (reconcile must be re-scoped per window), and
  `Store.window_rows` answers an excluded-slot query **entirely from `event`** —
  a `bin` row has no slot dimension to filter on — so for the digest path `bin`
  is not a degraded fallback for a pruned event, it is not read at all. Measured
  on the test fixture: prune the events, keep every bin, and the window returns
  **200** with `evidence` 179 → 36, `project`/`branch`/`model` silently `null`,
  and a confident 0.833 share off a fifth of the data, with `is_current()` still
  True so nothing objects. So the store keeps a monotonic **serving floor** and a
  window starting below it is refused: `WindowExpired` → **410** (`analyze_expired`),
  which the Go client treats as a genuine error rather than retrying — correct,
  since retrying can never restore a pruned row — so the workstreams facet
  publishes `partial`. Not 503 (the one status `post()` retries through; it would
  spin forever) and not 404 ("prompt not in this transcript", which would hide a
  horizon set shorter than the windows being asked for). `term` is the **only**
  level whose **bins** are pruned too: it is an INVENTORY level, so it is
  precomputed into `bin`, and under "rollups are never pruned" no event policy
  would bound the lifetime of a person's name at all. Never pruned: every other
  level's bins, and `prompt`/`parse_state`/`ingest` — the prompt index is what
  keeps 410 distinguishable from 404, and `parse_state` is what makes a tail parse
  equal a full parse. Pruning is **chunked** (5,000 rows, measured 14 ms, one short
  transaction each) so it cannot lock out the watcher-driven `/ingest`, rides
  `ingest_file` (both writers) with an hourly gate that being over-cap overrides,
  and is reported in `/metrics` under `store` — size (`live_mb` **and** `file_mb`,
  because **SQLite does not shrink the file on DELETE**: measured, 400,000 events
  took the file to 41.5 MB, and deleting half of them left the file at 41.5 MB
  while live pages halved to 21.1 MB. A cap read off the file size would delete
  every row the store had and *still* be over cap — it would prune the whole
  series to no effect — so it is enforced on `page_count - freelist_count`
  (`Store.live_mb`) and `/metrics` reports both), per-table row counts, oldest
  retained event, `serving_floor_ts` and what each policy removed.
- ⚠️ **`/analyze` and `/ingest` are confined to `KELD_ANALYZE_ROOTS`.** The sidecar has **no
  auth** — `serve.py` binds 127.0.0.1 and that is the whole of it — which was
  adequate while every endpoint only processed text the caller already held.
  `/analyze` is the first that opens an **arbitrary filesystem path as the
  daemon's user** and returns content derived from it, so unconfined it is a
  confused deputy: on a multi-user host any other local user can POST a path
  under someone else's `~/.claude/projects` and read back their workspaces,
  branches and named terms. The path is therefore checked against an allowlist
  before the open — `os.path.realpath` on both sides, so neither `../` nor a
  symlink escapes — and anything outside answers **403** (not 404: a rejected
  path and an unresolvable one must stay distinguishable), counted as
  `analyze_rejected`/`ingest_rejected` in `/metrics`. `/ingest` shares the
  allowlist because it is the same read with a persistence side effect. The daemon sets the variable at spawn from
  `watch.AnalyzeRoots()` (`daemon/sidecarenv.go`), which is the **stable
  ancestors** of each layout plus `KELD_WATCH_ROOTS`, not `DiscoverRoots()`'s
  globbed leaves: session directories appear after the sidecar is spawned.
  Empty means **deny everything**; absent means the sidecar's own per-user
  defaults.
- **This is the client-side analysis and enrichment service, not a GLiNER2
  wrapper.** GLiNER2 was the first use case, not a precondition: the service
  starts and serves with no model loaded, and `/analyze` answers with the
  inference worker still `down` (pinned in `test_main.py` against the real
  `lifespan`). `/analyze`'s `named_terms` level is **on** by default
  (`KELD_TERMS=0` switches it off) and loads spaCy — ~619 MB, into the FastAPI
  parent, permanently, since the parent is never recycled.
  ⚠️ **That default used to be justified by the level being unforwardable, and
  that argument no longer exists** — `named_terms` publishes as of schema v18
  (see `docs/architecture/window-analysis.md`). The default is unchanged, but it is now an
  open decision resting on the level's usefulness rather than on its output
  being confined to the device, and it costs 619 MB in a parent that is never
  recycled, inside a budget already documented as oversubscribed in
  `docs/architecture/sidecar-resource-safety.md`. Anyone
  revisiting `KELD_TERMS`' default should know it was never re-argued on its own
  merits. That coexists with
  GLiNER2 fine (~60 + ~619 + ~2740 MB against a 4096 MB budget); what did not
  was the **accounting** — see the parent-reserve bullet under Resource safety.
  The response carries `named_terms_status` (`ok` / `skipped:disabled` /
  `degraded:spacy_unavailable`), because an empty `named_terms` is otherwise
  not self-describing: a window that held no terms and a level that never ran
  look identical. `KELD_TERMS_MAX_LEN` (default 100k chars/message) restores
  spaCy's own per-document guard, which had been set to 20,000,000 — i.e.
  disabled; over-length messages are skipped by the NER pass, never cut, and
  the regex shapes still read them in full.

**The reference-series store (`analysis/store.py`, `analysis/ingest.py`).**
`/analyze` used to re-parse the whole transcript on every request: median **0.79s
on a 90 MB file**, and since a 60-minute window holds a **mean of 3.8 user prompts
(max 20, over 370 windows)** that is the same hour parsed **~4x**, up to 20x in a
burst. It now answers from a persistent series at `~/.keld/state/refseries.db`
(native SQLite, created `0600`, path resolved through `KELD_HOME`; never pickled
pandas — the package is deliberately pandas-free): **2.3ms on the same file, 340x.**
Nothing else persisted before this, so no dynamics were computable at all. Equality
with a parse is **asserted, not assumed**: `analyze_window_by_parse` is retained as
the ORACLE and never as a fallback, and **0 of 90 real prompts across 30 transcripts
differ from it**.
- **A window is a query over 5-minute bins (`BIN_SECONDS = 300`) and the two edges
  are read EXACTLY.** A 60-minute window ending at a prompt's own instant
  essentially never lands on a bin boundary, so both edge bins are almost always
  partial; snapping outward over-counts, snapping inward drops, and either turns
  every digest into an approximation. `window_rows` partitions instead — the
  fully-covered interior from `bin` (11 of 13 queries for a typical hour), the two
  edges from `event` — and a window shorter than one bin has no interior and is
  answered from events alone. The ordering rule is **not** reimplemented in SQL:
  SQLite computes the per-`(level, ref)` sums and `window.rollup` merges and applies
  its own alphabetical tie-break, so `rollup_window` returns exactly what
  `window.rollup` returns over the same rows and `workstreams.payload` consumes it
  unchanged.
- **`bin` is sparse by design, and its absence must never read as "no evidence".**
  Two things make that unmisreadable rather than merely documented: a **`bin_level`
  registry that `bin.level` REFERENCES** (with `foreign_keys=ON`, so the table
  physically cannot hold an unregistered level — asserted with a direct INSERT), and
  `rollup_window` routing unbinned levels to `event`, so the sparseness never reaches
  a caller. `PRECOMPUTED_LEVELS` is **derived** from `workstreams.ALLOCATION` +
  `INVENTORY` (16 levels) rather than typed, against the 19 `events_for_turns` emits;
  registering a new level backfills its bins from the retained events, with no
  transcript re-read.
- **Row timestamps are quantized to the series' own 0.1s resolution**
  (`levels.quantize`), so a window edge finer than that is not representable and is
  evaluated at that resolution. Measured against the old exact-timestamp turn
  selection: **6 of 90 real prompts move one turn across an edge, changing an
  evidence count by 1; 0 change any published VALUE.**
- WAL + `busy_timeout=5000` + `synchronous=NORMAL`; one writer, readers on executor
  threads — WAL is what lets a digest be served *during* an ingest. `NORMAL` is safe
  **only** because events, re-rolled bins and the byte-offset checkpoint commit in
  **ONE transaction**: a dropped trailing commit loses the offset too, so the next
  ingest re-reads the same tail. A half-applied batch is the state nothing downstream
  would notice, which is why `transaction()` exists. Non-ref rows are dropped on the
  way in — a `say` row carries `len(body)`, a measure of message text, and a `tok`
  row carries token counts; neither is a reference event. ⚠️ **That drop is now
  conditional** — see `KELD_CAPTURE` below — but the default is still drop, and
  nothing routes those rows to `event` either way.

⚠️ **`KELD_CAPTURE` (default OFF) keeps four signals the store used to compute and
discard, and flipping it costs a reparse.** They are the training corpus step 2 of
`docs/superpowers/specs/2026-08-26-signal-embeddings-design.md` needs, and all four
are numbers: per-role message CHARACTER COUNTS (`say`), the raw token split (`tok`),
tool-call OUTCOMES (`is_error` + result size), and `bin_offset` — the byte position
where each 5-minute bin's first line starts. The first three ride `turn_magnitude`'s
existing `kind` dimension (a new magnitude is data, not DDL); only `bin_offset` adds
a table. `Store.has_magnitudes` stays scoped to the COST kinds so a published field
cannot move because a character count arrived.
- **Why a byte index at all:** `transcript.turns_between` is O(FILE) — a whole-file
  parse, 0.79 s on the 90 MB transcript — so re-reading one block through it puts
  back the exact cost this store exists to remove. A block is bin-aligned by
  construction, so a block span maps to a byte range: one seek, one bounded scan.
- ⚠️ **The anchoring timestamp must be the RECORD'S OWN, and a bare regex does not
  give that.** `capture.scan` reads the instant off the raw line without decoding it,
  and taking the first `"timestamp"` anywhere in the line took a NESTED one:
  `file-history-snapshot` records have no top-level timestamp at all, and measured
  over 73,449 lines of the 40 largest real transcripts 1,135 of them (1.5%) match. The
  result was not merely imprecise but NON-MONOTONE — 31 of those 40 transcripts held a
  bin whose offset disagreed with `json.loads`, one anchoring at byte 13,931 of a 24 MB
  file whose preceding bin anchored at 9,426,720, i.e. a negative-length byte range —
  and these rows are written once at ingest and never re-derived. The line is therefore
  routed: a message-shaped line (`"type":"user"`/`"assistant"`, the shape `turns_in`
  gates on) keeps the regex, measured exact on 45,587 lines and 293.7 MB of the 321.3 MB
  corpus; anything else is DECODED, exact by construction and so robust to a record type
  Claude Code has not invented yet, and affordable because the bookkeeping records are
  the small ones — 8.3 ms to decode every one of them on the 90 MB transcript.
- ⚠️ **`KELD_CAPTURE` is fingerprinted into `parse_state`** (`ingest.capture_mode`, the
  sibling of `terms_mode`), so a change forces one reparse and no single transcript can
  hold rows from two settings. That is a per-TRANSCRIPT guarantee and the store is not
  one transcript: flip it on and only sessions that see another append reparse, so a
  dormant session keeps no capture rows. A corpus builder querying the store CAN
  therefore see two incomparable populations; whether a per-session marker is needed is
  a step-2 decision, deliberately not answered. Absent means NOT RECORDED, never zero.
- ⚠️ **Thinking-block LENGTH is not in this data and no toggle changes that.** Every
  block a platform writes carries a signature and an EMPTY `thinking` string (9,148
  measured in `text.think_blocks`, re-measured 7,648 with 0 of nonzero length), so
  `say_asst_think` is emitted and, being zero, never stored. The COUNT is the signal and
  is captured as `say_asst_think_blocks`. Don't wire a length consumer.
- `bin_offset` and `turn_magnitude` are both swept to the retention SERVING FLOOR
  rather than carrying horizons of their own: below it, one is a number nothing can be
  joined to and the other a seek into a window `/analyze` refuses (410). `/metrics`
  reports both row counts under `store.rows`.


⚠️ **The parse state carries a THIRD accumulator, and adding it forced a one-off reparse of
every existing store.** `pending` (reconcile) and `cwds` (workspace) were the two; `reqs` is the
third — the set of `requestId`s already costed. It exists because `events_for_turns` deduped
requests with a set **local to one call** while incremental ingest calls it once per batch, and
`turn_magnitude`'s primary key includes `source_line` (the batch ordinal). So a request whose
assistant lines straddled a batch boundary was written twice and `turn_magnitudes` summed both:
measured **2x on a three-line request cut after line 1, and 3x ingested a line at a time** —
exactly lines-per-request. Nothing caught it because the oracle test ingests in ONE batch, the
fixture used one `requestId` per line (making the dedup a structural no-op), and `test_ingest`'s
chunked-equivalence comparator did not look at `turn_magnitude` at all. It does now.
`ingest.STATE_VERSION` 3 → 4 is the REPAIR rather than mere bookkeeping: existing stores already
hold the duplicate rows and nothing recomputes them, so the version mismatch forces one reparse and
`clear_session` drops them. **Expect every store to reparse once on upgrade.** The set costs
1,875 ids / ~59 KB of JSON on a 90 MB transcript — the same order as `pending` — and a truncated
hash was deliberately rejected, because a collision would silently DROP a request's spend rather
than double it.

⚠️ **A tail parse is only equal to a full parse because it was MADE equal.**
`ingest_file` parses just the bytes a transcript grew by, resuming from the `ingest`
table's byte offset (rotation/truncation caught by a `HEAD_BYTES = 4096` head
fingerprint that includes the byte *count*, since a growing file changes how much
there is to hash). The non-obvious part is that this is **not** automatically equal
to parsing the whole file, and if it isn't, the series is silently and permanently
wrong — nothing downstream re-derives these rows. **Measured first:** naive
per-chunk ingest of real transcripts differed from a single pass by up to **4,179
`repo_mentioned` rows** and **1,276 `workspace_evidence` rows** on single files.
Both retroactive sources are real, and they are handled by two different means
because the costs differ:
- **`reconcile` is RECOMPUTED WHOLE each batch.** It resolves prose paths against
  every DECLARED path, so a tail declaration reattributes a head mention and no
  incremental form is correct. `pending` is persisted in `parse_state`, the tail is
  appended, and the result REPLACES the previous one — exact by construction, no
  detection needed. Affordable because `pending` is tiny: **104-859 entries for
  2-47 MB transcripts, and reconciling the whole of one costs 0-3ms.** This is why
  `Store.replace_events` exists and DELETEs its slot first: `upsert_events` can only
  add or raise a count, but a recomputed set must be able to **RETRACT** a row, or a
  reattributed file stays counted under both names — reconcile's own split-share
  defect reintroduced by the storage layer.
- **Workspace evidence ACCUMULATES, and a change in the DERIVED ANSWER forces a
  reparse.** `scan_workspace` is a whole-file pre-pass: a `CLAUDE.md` read at 17:00
  re-resolves the 09:00 turns from "cwd as given" to "repo-level marker", and with it
  the `root_dir` every path is relative to. Accumulation is exactly equal to one pass
  but cannot be retroactive, so the distinct cwds seen so far (1-8 per transcript) are
  carried and their resolution + remote selection recomputed after each batch. Keying
  on the **answer** rather than the raw evidence means a new `cd` target that resolves
  to the same workspace costs nothing. Reparse over re-derivation is deliberate:
  re-deriving means a second, partial copy of `events_for_turns`.

**Equivalence at corpus scale: 284 real transcripts (0-47 MB), each ingested in 40
successive chunks against one whole-file ingest — 0 files differed, 0 rows
differed.** Retroactive reparses hit **0.7% of appends (53/7,571)**. The 47 MB file
costs **1.82s for its entire 41-chunk lifetime against 0.76s for one full parse**
(~44ms per ingest, against the 0.8-1.0s per PROMPT the parse path used to spend),
and chunk count barely moves the total — which is the O(tail) claim holding. Two
further silent-wrongness traps are pinned rather than hoped for: the **watermark**
must not retreat (taking the batch's last turn regresses on the 9-in-9,937 real
turns whose timestamp precedes the previous line), and `ingest.terms_mode`
fingerprints the terms pipeline's **identity** into the parse state — `term` is the
one level never re-derived, so a store ingested under `KELD_TERMS=0` would otherwise
report no `named_terms` forever. A changed fingerprint reparses.

