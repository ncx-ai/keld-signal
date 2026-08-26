# Signal embeddings — the training corpus for future-work prediction

**Status:** design. Nothing below is built.

Companion research: `2026-08-26-joint-embeddings-design.md`, which compared three approaches and
recommended the structured-only one (A). This spec is that approach, extended with a client-side
text encoder after review, and scoped to what actually ships.

## What this is for

Keld trains models, centrally, to predict what a piece of work will do next. This spec defines the
`X` those models are trained on: a vector computed **on the engineer's own machine**, published to
Atlas, and paired with a `y` derived from what the same machine records later.

    on device:  transcript -> reference series -> feature vector + text vectors -> Atlas
    at Keld:    Atlas -> (X at time t, y at time t+h) -> a model

**Training never runs on a client device.** The client computes and publishes; it does not learn.

⚠️ **Raw prompt text never leaves the machine.** Unchanged. The encoder runs on device and only its
output vector is published. This spec adds no path by which a byte of a transcript reaches Atlas.

## Scope: `ml_backend:"deterministic"` ONLY

The whole subsystem registers only under the deterministic backend, which is v2's shipping default.
GLiNER2 is not part of the initial v2 release and nothing here loads it, waits on it, or degrades
without it. Under `ml_backend:"auto"` the subsystem is **absent** — never registered, so it appears
in neither `facets_skipped` nor `extractor_versions`, which is the existing distinction between a
pass that was skipped and one that was never wired (see AGENTS.md, `WithWorkstreams`). Lifting that
restriction later is a registration condition, not a redesign.

One consequence worth stating: with no GLiNER2 worker resident, the ~2.4 GB it holds is free, and
the parent-reserve contention documented at length in AGENTS.md does not apply to this path.

## The prior that shapes the whole design

⚠️ **Do NOT serialise the deterministic digest to text and embed it.** Measured
(`2026-08-22-prose-probe-results.md`): a bi-encoder fed digest text answered `record` on **36 of 36**
inputs at confidence 0.82–0.999, reproducing the majority baseline exactly. Stripping the harness
vocabulary moved the constant rather than recovering signal. **The digest is a uniform genre** —
every digest is the same kind of writing, so an encoder scores its register, which is constant by
construction. Not fixable by rewording.

The same study measured transcript **text** at 79.2% against a 62.5% majority, outside the
pre-registered ±8 band. So both halves earn their place, and the rule is:

> The deterministic half enters as NUMBERS. The text half enters as a text embedding. Neither is
> ever serialised into the other's modality.

## Anchors — where a row is snapshotted

Three row kinds, all published. **They carry different things** — a message row is not a small
`bin` row, and stating that here is what stops the three from being conflated into one wide type:

| kind | cadence | carries |
|---|---|---|
| `message` | one per user/assistant message | **the text vector only** (one stream, tagged), plus its timestamp and role. No shell ladder — a single message has no lookback. This is the only kind that preserves ORDER, which any sequence model needs and which pooling destroys. |
| `bin` | one per non-empty 5-minute bin | **the full `S(t)` shell ladder** (1,414 dims) + the per-shell pooled text scalars (`dispersion`, `drift`, `novelty`). No centroids — they are re-poolable at Keld from the message rows. |
| `block` | one per closed block | the same as `bin`, at the block's own end instant, plus `start_reason` / `end_reason`. Published because Atlas already renders blocks, not because it is the right sampling grid. |

⚠️ **Centroids are deliberately NOT published on `bin`/`block` rows.** They are an exact pooling of
message vectors Atlas already holds, so publishing them would duplicate ~3× the message payload to
say nothing new, and would freeze one pooling function (mean) into the wire when the pooling is
precisely the thing we expect to change. The three *scalars* publish because they are cheap and
because `drift` in particular needs the previous shell's centroid, which the client has and a
per-row consumer would have to reconstruct.

⚠️ **Blocks are NOT a sufficient sampling grid on their own.** Every block boundary is arithmetic —
a 20-minute cap or a 15-minute silence — and the ablation that removed the change detector
established that *no boundary is a claim about the work*. Anchoring the training set to arithmetic
cuts spends the sampling budget on non-events. Blocks are published because Atlas renders them, not
because they are the right grid.

Higher grains are derivable from lower ones by pooling; the reverse is not. That asymmetry is why
all three publish.

## Shells, not nested windows

The lookback ladder is **disjoint**: `[0,5m)`, `[5,20m)`, `[20,60m)`, `[60,240m)`,
`[240m, session start)`.

⚠️ Nested windows (`5m`, `20m`, `60m`, `240m` each measured from `t`) count the same first five
minutes four times. The four blocks are then near-collinear and a model has to undo the overlap
before it can use any of them. Shells are decorrelated, cost exactly the same (one `rollup_window`
per shell, ~2 ms each), and any nested window is recoverable by summing shells. A consumer that
wants "the last hour" adds three columns.

## `S(t)` — the structured vector

Computed from `store.rollup_window` + `turn_magnitude` + `dynamics` + `prior`, per shell.

⚠️ **The published block payload is the WRONG input and must not be reused.**
`workstreams.payload` emits, per ALLOCATION dimension, only the *dominant* value's
`value`/`share`/`evidence`/`status` — a presentation decision for a human reader. A feature vector
wants the whole distribution. `MIN_EVIDENCE` is a LABEL on a published attribution, not a filter on
evidence: a sub-floor rollup is perfectly good input to a model even where it is not honest to
publish as an attribution.

### Per shell (~265 dims)

| group | source | dims |
|---|---|---|
| `action` histogram | `vocab.ACTIONS` — genuinely closed | 22 + 1 other |
| `artifact` histogram | `vocab.ARTIFACT_EXT` keys + `code` + `chart` | 13 + 1 |
| `lang` histogram | distinct values of `vocab.EXT_LANG` | 37 + 1 |
| `toolchain` histogram | `vocab.TOOLCHAIN_EXE` keys | 8 + 1 |
| `tool` histogram | `vocab.TOOL_ACTION` keys + one `mcp:*` bucket | 21 + 2 |
| `vcs`, `model` family | git / reported-unverifiable / other; opus / sonnet / haiku / fable / synthetic / other | 3 + 6 |
| **shape stats, all 25 levels** | `log1p(evidence)`, `n_distinct`, `top1_share`, `top3_mass`, `norm_entropy` | 125 |
| effort + magnitudes | see below | 22 |

**The shape stats are how the OPEN levels contribute without publishing identity.** `file`, `dir`,
`component`, `exe`, `verb`, `term`, `skill`, `branch`, `workspace`, `service` have unbounded
vocabularies (measured on one live store: `term` 1,334 distinct, `verb` 490, `exe` 466, `file` 687).
One-hot is impossible, and hashing them would publish a fingerprint of the identity. Five shape
statistics say how concentrated and how varied the level was, which is what a model can use, and
carry no value string at all.

The effort group, with the newly-wired rows marked NEW:

    request_tokens (price-weighted), edit_bytes, authoring_turns
    NEW tok:   out, in_fresh, in_cached, cache_ratio = in_cached/(in_cached+in_fresh)
    NEW say:   user_chars, user_echo_chars, asst_chars, asst_think_chars, think_ratio
    NEW tool:  error_count, error_rate, result_bytes_total, result_bytes_p90
    tempo:     fast_share, n_gaps, gap_p50_s, gap_p90_s
    counts:    turns, human_prompts, tool_calls

### Once per row (89 dims)

| group | dims |
|---|---|
| `dynamics` — 4 kept dimensions × (`turnover`, `decay`, `concentration_shift`, `changed` as 2, `status` one-hot 6, `reading` one-hot 7) | 68 |
| `prior` — 4 dimensions × (`agrees`, `departure`, `novel`) | 12 |
| positional — hour sin/cos, weekday sin/cos, `log(session age)`, `log(gap since last turn)`, shell coverage, block position | 9 |

**5 × 265 + 89 = 1,414 dims.** int8-quantised (nearly everything is a share in `[0,1]`) that is
1.4 KB per row.

### Three invariants of the spec itself

1. ⚠️ **Closed vocabularies are frozen from `vocab.py`'s tables, never from what a store happens
   to contain.** A machine that has never run `pandoc` must still emit a zero in the `document`
   toolchain slot. Otherwise two machines produce vectors of different meaning at the same index
   and the corpus is silently incoherent. The spec carries an ordered vocabulary manifest.
2. **The normalisation transform is part of the spec, not of the training script.** `log1p` versus
   raw is not recoverable after the fact.
3. **`feature_spec` version is part of the published row's identity**, so two spec versions can
   never be pooled by accident. Same argument as `ingest.terms_mode` fingerprinting the terms
   pipeline's identity into `parse_state`.

`ext` contributes shape statistics only and no histogram: it is the *input* to both `EXT_LANG` and
`ARTIFACT_EXT`, so a histogram over it is very nearly a linear function of two histograms already
present.

## Text vectors

**Client-side, always. The encoder runs on the device; only the vector is published.**

### The unit of encoding is the MESSAGE, not the shell

Three reasons, each independently sufficient:

1. A 240-minute shell holds hundreds of KB of text. No encoder context holds it.
2. The bulk of a transcript is `tool_result` lines, which `transcript.turns_in` **skips unparsed by
   design** — a raw substring check before `json.loads`, and it is what keeps a parse seconds-long
   rather than minutes-long. Embedding "the transcript" would mean decoding exactly the lines the
   parser is engineered to avoid.
3. Shells overlap across rows: a message in this row's `[0,5m)` is in the next row's `[5,20m)`.
   Encoding per message means **each message is encoded once, ever**, and every shell of every row
   containing it reuses that vector. Amortised cost is one forward pass per message: ~100/day.

### Streams, encoder, derived scalars

**Three streams, kept separate and never concatenated** — they are different registers, and mixing
them averages away the distinction:

- `user` — the human's own words. Smallest volume, highest density.
- `asst` — assistant prose, excluding thinking and excluding tool results.
- `think` — `think_blocks` content. Largest volume, and where the reasoning is.

**Encoder: Qwen3-Embedding-0.6B, 1024-d.** Chosen over EmbeddingGemma-300M now that RAM is not
binding: it is the stronger model, MRL-truncatable, and at a duty cycle below 0.3% the 2× latency
does not matter, while vector quality compounds across the whole corpus. EmbeddingGemma-300M @ 256-d
is the fallback if the measurement below comes back badly. **Not a generative LLM's hidden states** —
a decoder's last-token state is a poor sentence embedding without contrastive tuning and costs more;
the GGUF models already on hand would be a downgrade for this specific job.

⚠️ **Encode at 1024-d, publish MRL-truncated to 256-d.** These are different numbers and conflating
them is how the volume estimate goes wrong by 4×. Matryoshka truncation is a prefix slice, so the
256-d vector is a strict, free lossy compression of the 1024-d one — no second forward pass. The
default is 256 because it makes the message stream ~77 KB/user/active-day against ~307 KB at full
width, and because nothing yet measured says the extra 768 dimensions predict anything. **The
default is a measurement (below), not a preference**, and it is the one parameter here that cannot
be revised retroactively: a corpus collected at 256 cannot be widened without re-embedding every
machine's history.

⚠️ **Bounding a message is a repo convention, not a detail.** "Never cut text mid-sentence": a long
message is split at sentence boundaries and its chunk vectors mean-pooled, never truncated
mid-clause, and any drop is declared (`omittedNotice` precedent). The measured cost of getting this
wrong is on record — 46 of 47 generated beats came out mid-clause from a 200-rune cap.

Per shell, per stream, alongside the pooled centroid:

- `dispersion` — `1 − mean cos(message, centroid)`. How varied the talk was.
- `drift` — `1 − cos(this shell's centroid, the previous shell's)`. The text analogue of
  `dynamics.turnover`.
- `novelty` — `1 − max cos(message, any earlier message in the session)`.

### The published-vector treatment

A fixed **orthogonal projection** is applied on device before publish. It preserves cosine
similarity and inner products **exactly**, so nothing about training changes, costs one matmul, and
makes off-the-shelf inversion tooling (vec2text, ALGEN) useless without the matrix — those attacks
need the embedding space to be known or alignable. The matrix is generated once per deployment and
held by Keld, not by the client.

This is a hardening measure, not a claim of impossibility. It is recorded here so the reason it
exists survives someone later asking why the client multiplies by a constant.

## What is wired through at ingest, and what it costs

All four are numbers. None is text, a span, or an offset into text.

1. **`say`** — per-turn character counts by role (`user`, `user_echo`, `asst`, `asst_think`).
   Already computed by `levels.events_for_turns` and **dropped by `store.upsert_events`**. Zero
   extra parse cost; one table write.

   ⚠️ **This section claimed `asst_think` "already carries a real count from `think_blocks()`",
   and that the `# 0 = not persisted by this store` comment in `levels.py` meant the row is
   discarded downstream "not that the number is zero". BOTH HALVES WERE FALSE.** The number IS
   zero: `think_blocks`' own docstring records that all 9,148 blocks in platform-written Claude
   Code transcripts carry a signature and an EMPTY `thinking` string, and the final review
   re-measured 7,648 blocks across the local corpus with **0 of nonzero length**. The only corpus
   that ever held real thinking text was a manual claude.ai export, which this system does not
   read. `_aggregate_mag` drops zeros, so `say_asst_think` is emitted and never stored.

   What IS available is what `text.py` names as the designed-for signal: block **incidence**.
   The COUNT of thinking blocks on a turn is captured as its own kind,
   `magnitude.SAY_THINK_BLOCKS` (`say_asst_think_blocks`) — the fact the zero-drop was
   destroying. `SAY_THINK` is kept, because the drop is the ONLY thing suppressing it and a
   producer that ever writes thinking text would populate it with no code change; nothing
   downstream may depend on it, and step 2 must not build a length feature on it.
2. **`tok`** — raw token counts (`out`, `in_fresh`, `in_cached`). Also computed and dropped, because
   the price-weighted `mag` magnitude superseded it **as a measure of cost**. The split is a
   different fact: `in_cached / (in_cached + in_fresh)` measures conversation reuse and context
   growth, which no cost figure expresses. Zero extra parse cost.
3. **Tool outcome** — `is_error` and result size. ⚠️ This is the one NEW parse surface. It is
   obtained by a raw substring check for `"is_error":true` plus `len(line)`, applied **before any
   `json.loads`** — the identical technique `turns_in` already uses — so the huge lines are never
   decoded and the parse stays fast. Whether a tool call failed is plausibly the strongest single
   predictor of the next five minutes. **Its cost must be measured on the 90 MB transcript before it
   ships**, against the current parse time, and the measurement recorded.
4. **Per-turn reconstruction** — `event` already carries `source_line`, so grouping by
   `(session, source_line)` should suffice; whatever is missing for a per-message row is added.

   ⚠️ **IT DOES NOT SUFFICE, and step 2 must not build on this sentence.** `source_line` is the
   BATCH ordinal — the absolute line the batch was read through — so it is IDENTICAL for every
   row an ingest wrote, and a whole-file ingest gives every row in the file the same value. The
   only per-turn key these rows carry is `ts`, quantized to 0.1 s (`levels.quantize`), and
   `store.py` already notes that two turns can collide on one tick. Recovering a genuine
   per-message row therefore needs a key that does not exist yet; designing it is step 2's
   first task, not an assumption it inherits. No code change was made for this in step 1.

### `bin_offset` — the index that makes the bounded read possible

⚠️ **`transcript.turns_between` is O(FILE), not O(span).** It does `sorted(iter_turns(path))` — a
whole-file parse, 0.79 s on a 90 MB transcript. That is precisely the cost the reference-series store
exists to eliminate, and reintroducing it once per block would undo it.

So ingest — which already walks the file with a byte offset in hand — records the offset of the
first line of each 5-minute bin:

    CREATE TABLE IF NOT EXISTS bin_offset (
      session  TEXT    NOT NULL,
      bin_ts   INTEGER NOT NULL,
      "offset" INTEGER NOT NULL,
      PRIMARY KEY (session, bin_ts)
    ) WITHOUT ROWID;

~12 rows per active hour. A block is bin-aligned by construction (`analyze._block_span` floors and
ceils), so a block span maps to a byte range: one seek, one bounded scan. Go's `resolve.scanFrom`
already uses exactly this technique; this gives the sidecar the same capability without moving text
across a process boundary.

## Publishing

Per-message rows, per-bin rows and per-block rows, batched and spooled — the `clientevents`
mechanism, not a new one: bounded buffer, periodic flush, `internal/retry`, drop-oldest spool under
`~/.keld/spool/`.

Volume, int8-quantised, per user per active day:

| | rows | bytes |
|---|---|---|
| message vectors (3 streams, MRL-truncated to 256-d) | ~100 | ~77 KB |
| bin rows (1,414 dims) | ~72 | ~101 KB |
| block rows | ~18 | ~25 KB |
| | | **~200 KB/day, ~50 MB/user/year** |

Nothing here rides `Enrichment` or `BlockEnrichment`. It is its own row type under its own
correlation scheme, for the same reason the block row needed one: Atlas keys enrichments
`UNIQUE(org_id, source_id, corr_scheme, corr_id)` and upserts `ON CONFLICT DO UPDATE` over every
column, so sharing a scheme overwrites rather than dedups.

## Toggles

Three, each a `KELD_*` env / `agent-config.json` value with an Atlas per-org override riding the
existing `/v1/enrichment-settings` poll — the `client_telemetry` precedent, remote overrides local,
non-fatal if Atlas is unreachable.

| toggle | governs | flipping it |
|---|---|---|
| `capture` | the extra ingest rows (`say`, `tok`, tool outcome, `bin_offset`) | ⚠️ **fingerprinted into `parse_state`; a change forces a reparse.** Without this a store silently holds two incomparable populations — the exact trap `ingest.terms_mode` exists for. |
| `features` | computing and storing the vectors locally | free either way |
| `publish` | sending them to Atlas | free either way |

Separating `capture` is what makes the other two free: turning publishing off must not cost a full
reparse to turn back on.

⚠️ **The fingerprint's guarantee is per TRANSCRIPT, not per STORE, and the table above overstated
it.** `ingest.capture_mode` fingerprints the setting into `parse_state`, so a change forces a
reparse and no single transcript can hold rows from both settings — that much holds. But only the
sessions that see another append ever reparse: flip `KELD_CAPTURE=1` and a dormant session keeps
no capture rows for as long as it stays dormant. The STORE is the query unit for a corpus builder,
and it therefore CAN hold the two incomparable populations this row claims the fingerprint
prevents.

No schema change was made for this in step 1, deliberately. `Store.bin_offset` already models the
honest reading — a missing row means **not recorded**, which is not the same as zero — and whether
a per-session capture marker is worth DDL is a step-2 design decision, to be taken when the corpus
builder exists and can say what it needs. It is recorded in `store.py`'s header and in
`turn_magnitude`'s own comment so it cannot be discovered late.

## Performance budget

The constraint is explicit: **data collection must not affect the end user's experience.**

| risk | control |
|---|---|
| ingest hot path | untouched. Embedding runs at block close, in the sidecar. The watcher poll loop — which carries every hook-free prompt — never waits on it. |
| file re-reads | `bin_offset`. O(block), not O(file). |
| CPU | rides the **existing** `governor.py` CPU-EWMA pacing and `cpuscale.py` thread cap (~50% of cores under host load). No second load-protection mechanism is built. |
| concurrency | the existing single-flight runner. Never fan out. |
| duty cycle | a block closes ~every 20 min; ~30 messages batched at 20–50 ms ≈ 0.6–1.5 s per block. **Below 0.3%**, and paced. |
| RAM | the encoder runs in **its own worker child**, not the FastAPI parent — recyclable, idle-unloadable, and never loaded at all when `features` is off. |
| backlog | a machine with many closed blocks caps per-sweep work and drains. Bounded and coalescing, the shape `daemon/ingestsignal.go` already uses. Never a herd. |
| proof | not asserted. The embedder joins `sidecar/loadtest/`'s smoke arm; `/metrics` gains an `embed` block (p50/p90 per block, RSS, queue depth) beside `worker` and `store`. |

## The label side (`y`)

Not built by this spec, but it determines what must be published, so it is fixed here.

**Horizons:** next block; +1 h; +4 h; end of session.

**Targets, all derivable at Keld from the published rows:**

- *categorical* — the next block's dominant `branch` / `lang` / `artifact` / `skill` / `toolchain` /
  `model` / `project`, **and each one's `status`**, so "will the next block be attributable at all"
  is itself a target. Measured distribution over 12,016 dimension-slots: `attributed` 53.2%,
  `absent` 38.7%, `thin` 7.7%, `tie`/`no_majority` 0.4%.
- *multi-label* — which actions, tools and components occur next.
- *regression* — next-block `request_tokens`, `authored_bytes`, `fast_share`, `gap_p50_s`.
- *event* — time to idle (survival), block `end_reason` (`budget` 48.5% / `session_end` 33.0% /
  `idle` 18.5%), does the session continue.
- *change* — will the dominant value CHANGE. This is `dynamics.changed` computed FORWARD, and it is
  the routing-relevant one: routing needs to know a shift is coming, not that the last hour
  resembled the one before.

### Leakage traps, named because each is silent

1. ⚠️ **Workspace resolution is a whole-file pre-pass.** `workspace.scan_workspace` re-resolves the
   09:00 turns from a `CLAUDE.md` read at 17:00 — `ingest.is_current`'s docstring says so
   explicitly. Any feature derived from workspace / `repo` / `root_dir` resolution is therefore not
   a strictly causal prefix of what the daemon knew at time `t`. **Measured bound: retroactive
   reparses hit 0.7% of appends (53/7,571)**, so the effect is small — but the affected feature
   groups carry a declared causality flag so a training run can drop them, rather than a reader
   rediscovering this.
2. `reconcile` is causally clean **only** through `_rollup_at`, which re-scopes it with
   `pending_in(store, path, lo, hi)` — verified bounded by `start <= b[0] < end`. The *stored*
   reconcile slot is whole-file and must never be read directly.
3. `prior` is already half-open on the right (`[session start, block start)`) — causal by
   construction, deliberately, and reusable unchanged.
4. **Retention silently changes the feature set across a long span.** `event` is bounded at 400
   days, `term` at 90. A training set spanning more than 90 days has a `term` level that vanishes
   partway through with nothing saying so. Published rows are immutable at Atlas and unaffected;
   this bites only re-derivation from a client store.

## Module layout

Everything new lives in its own module. v2's rule — *a path, not a parameter* — applies.

    sidecar/app/analysis/features.py    NEW. S(t) — the shell ladder and the vocabulary manifest
    sidecar/app/analysis/textembed.py   NEW. per-message encoding, pooling, the derived scalars
    sidecar/app/analysis/store.py       + bin_offset table, + say/tok/tool-outcome routing
    sidecar/app/analysis/levels.py      + tool-outcome extraction (substring pre-check)
    sidecar/app/main.py                 + POST /features  (the only edit to an existing endpoint file)
    internal/agent/features/            NEW. the emitter's feature half + its cursor state
    internal/agent/publish/             + a feature row type; existing types untouched

## Delivery order

Four deliverables, each shippable and testable on its own. The order is a dependency order, not a
preference — and the first is worth landing alone, because it is the only part whose absence cannot
be repaired later without a reparse of every machine.

1. **Capture.** `say` / `tok` / tool-outcome / `bin_offset` into the store, behind the `capture`
   toggle with its `parse_state` fingerprint. No vectors, no publishing, no encoder. Ships the
   moment the tool-outcome parse-cost measurement passes.
2. **`S(t)`.** `features.py`, the vocabulary manifest, `POST /features`, and the local rows. Still
   nothing published and no encoder loaded. Measurement 3's arms (i) and (ii) can run at the end
   of this step, which is the cheapest point at which we learn whether any of this predicts
   anything.
3. **Publish.** The Go emitter half, the wire type, the batch/spool path, the `publish` toggle, and
   the Atlas schema. Structured rows only.
4. **Text.** `textembed.py`, the encoder child, the orthogonal projection, the message rows. Last
   because it is the only part that adds a model to the client, and because steps 1–3 give it a
   measured baseline to beat.

⚠️ **Step 2 is a real decision point, not a milestone.** If `S(t)` does not beat the persistence
baseline, steps 3 and 4 are building a pipeline for a signal that is not there, and the right
response is to stop and re-open what `y` should be — not to add the encoder and hope.

## Out of scope

- **The learned compressor.** Phase 1 publishes a hand-built vector. A self-supervised encoder
  (CoLES-style contrastive, or a GRU next-bin predictor) compressing it to ~128-d is Phase 2 and is
  gated on this collection having run — there is nothing to learn a compression from yet.
- **The two-tower predictive model** (JEPA future-latent + cross-modal alignment). That is the
  destination described in the companion research doc; it needs the corpus this spec collects.
- **Any routing decision.** The vector is an input to one; naming a model per prediction is its own
  spec.
- **`ml_backend:"auto"`.** See Scope.

## Measurements to run before this is believed

Pre-registered, bars written first, in the style of `BLOCK-BOUND-2` and the facet-value study.

1. **Tool-outcome parse cost** on the 90 MB transcript, against the current parse time. Gate: the
   substring pre-check must not measurably move it.
2. **Encoder choice** — Qwen3-Embedding-0.6B vs EmbeddingGemma-300M: per-message latency, RSS, and
   downstream predictive lift on the same held-out split.
2b. **Published width** — 1024 vs 512 vs 256 vs 128, MRL-truncated, scored on the same split. ⚠️
   Run this one FIRST among the encoder measurements: it is the only parameter that cannot be
   revised without re-embedding every machine's history.
3. **The arm comparison**, on the rebuilt 500-transcript / 496-session corpus, horizon = next block,
   predicting the allocation dominants + their `status` + `end_reason` + time-to-idle:
   (i) a **persistence baseline** — the last block's value, which is the bar that matters, not a
   class prior; (ii) `S(t)` alone; (iii) `S(t)` ⧺ text scalars; (iv) `S(t)` ⧺ full centroids.
   ⚠️ The prior to state in the pre-registration is the prose probe's inversion — digest inert, text
   live — **and the reason it may not carry**: that was measured on a SEMANTIC label, and these
   targets are STRUCTURAL. `observable.py`'s diagnosis is that the levels record what was TOUCHED,
   not what it MEANT; predicting structure from structure is the case where that does not bite.
4. **Volume in the field**, not from this document's arithmetic: bytes per user per active day at
   each of the three grains.

## Sources

- Prose probe — `docs/superpowers/specs/2026-08-22-prose-probe-results.md`
- Block bound + ablation — `~/keld/refseries-context/blocks/BLOCK-BOUND-2-{PREREGISTRATION,RESULTS,ABLATION}.md`
- Block contract — `docs/superpowers/specs/2026-08-25-block-model-contract-design.md`
- Companion research — `docs/superpowers/specs/2026-08-26-joint-embeddings-design.md`
- CoLES — https://arxiv.org/abs/2002.08232
- Qwen3 Embedding — https://arxiv.org/pdf/2506.05176
- EmbeddingGemma-300M — https://huggingface.co/google/embeddinggemma-300m
- Vec2Text — https://www.emergentmind.com/papers/2310.06816
- ALGEN — https://arxiv.org/pdf/2502.11308
