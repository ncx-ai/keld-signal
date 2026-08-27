# Joint embeddings over messages, windows and blocks — three candidate designs

⚠️ **SUPERSEDED IN THREE PLACES by `2026-08-26-signal-embeddings-design.md`.** Kept because the
comparison of the three approaches, and the measured priors behind it, are still the argument for
what shipped. But three of its conclusions were wrong and are recorded here rather than quietly
edited out:

1. **"Centroids are local-only; a text embedding cannot cross the wire."** Wrong about the
   requirement. Keld trains centrally; the client computes and publishes and never learns, so text
   vectors DO go to Atlas. The invariant is that **raw prompt text** never leaves the machine, which
   a vector computed on-device satisfies. The residual inversion risk is handled by a fixed
   orthogonal projection applied before publish (exact cosine preservation, no training cost), not
   by withholding the vector.
2. **"The fleet corpus is structured-only by construction."** Follows from (1) and falls with it.
   The fleet corpus is the full corpus. Approach C's cross-modal distillation is therefore a
   modelling option rather than the only route to a text-informed signal.
3. **Approach A3 — "materialise at block close, recompute the dense series locally at export."**
   Wrong once Atlas is the corpus: Atlas holds no event series, so nothing unpublished can be
   recomputed there. **Publish granularity IS training granularity**, and all three grains
   (message, 5-minute bin, block) publish.

A fourth, smaller error: this document said a block's messages could be re-read cheaply via
`transcript.turns_between`. That function is O(FILE) — `sorted(iter_turns(path))`, 0.79 s on a
90 MB transcript. The shipped design adds a `bin_offset` index instead.

**Status:** research. Nothing below is built. Written against v2 as it stands on 2026-08-26
(`docs/superpowers/specs/2026-08-25-v2-block-path-design.md`,
`2026-08-25-block-model-contract-design.md`), which a parallel session is actively shipping.

The question: how do we compute vectors — from individual user messages, and from moving windows
of several lookback lengths — that can be JOINED with the deterministic statistics we already
compute, such that the joint vector is an `X` we can later train on to predict future transcript
data and future analysis (`y`) over some horizon.

---

## 0. What v2 is, in one paragraph, because it constrains everything here

    a transcript -> blocks of work -> one characterisation per block

A block is ≤ 20 minutes of one session's ACTIVE time (`MAX_BLOCK_MINUTES`), ended by 15 minutes of
silence (`IDLE_BINS = 3`) or by the cap, and by nothing else — the change detector was ablated and
every measured number improved. Blocks tile active time; silence belongs to no block; there is no
merge rule and thin blocks publish unattributed. The characterisation is a rollup over a span out
of the reference-series store (`refseries.db`), composed in `blockdigest.py` from
`rollup_window` + `workstreams.payload` + `dynamics` + `prior` + `effort`. **v2 ships
`ml_backend:"deterministic"`: GLiNER2 is never loaded and no classifier runs on any message.**
Per-message, only `sensitivity` survives.

Two properties of that make this work tractable:

- **Arbitrary spans are cheap.** `rollup_window` answers a 60-minute window in ~2.3 ms out of the
  store (vs. 0.79 s re-parsing a 90 MB transcript). A ladder of five lookbacks costs ~10 ms. A
  multi-scale window embedding is essentially free.
- **The store is already the label source.** Everything we would want to predict — the next
  block's dimensions, its effort, its dynamics, whether it is attributable at all, whether the
  session goes idle — is computed by the same functions from the same rows. `X` and `y` come out
  of one system, which is the only way the labels stay consistent over a 400-day horizon.

---

## 1. The finding that dictates the fusion strategy

⚠️ **Do NOT serialise the deterministic digest to text and embed it.** This is measured, in
`docs/superpowers/specs/2026-08-22-prose-probe-results.md`:

| arm | accuracy (n=24) | distinct predictions |
|---|---|---|
| `majority` | 62.5% | — |
| `G-digest` (digest text → bi-encoder) | 62.5% | **1** |
| `G-text` (transcript text → bi-encoder) | **79.2%** | 2 |
| `G-both` | 66.7% | 3 |

`G-digest` answered `record` on **36 of 36** inputs at confidence 0.82–0.999. Stripping the
harness vocabulary did not recover signal — it moved the constant. **The digest is a uniform
genre**: every digest is the same kind of writing, a deterministic statistical description of a
span, so an encoder asked what kind of text this is scores the digest's own register, which is
constant by construction. This is not fixable by rewording.

The consequence for every design below: **the deterministic half enters the joint vector as
NUMBERS, and the text half as a text embedding. They are never mixed by serialising one into the
other's modality.** The prose probe also establishes the other half of the prior — transcript
text does carry real signal (+16.7 points, outside the pre-registered ±8 band) — so the text tower
is worth its cost, on a semantic label at least.

---

## 2. The shared substrate: `S(t, L)`, the structured feature vector

All three designs need this. It is the whole rollup, not the published payload.

⚠️ **The published payload is the wrong input.** `workstreams.payload` emits, per ALLOCATION
dimension, the *dominant* `value`/`share`/`evidence`/`status` — a publishing decision made for a
human reader. An embedding wants the **whole distribution**: the shares of every value, the
entropy, the distinct count. `MIN_EVIDENCE`'s floor is a LABEL, not a filter on features; a
sub-floor rollup is perfectly good evidence for a model even where it is not honest to publish as
an attribution.

Composition of `S(t, L)` for the span `[t − L, t)`:

| group | content | approx dims |
|---|---|---|
| closed-vocabulary histograms | normalised counts over `action` (22), `tool` (21), `ext` (16), `skill` (9), `artifact` (7), `lang` (6), `model` (4), `toolchain` (3), `vcs` (2) | ~90 |
| per-level shape | for every level: `log1p(evidence)`, `n_distinct`, top-1 share, top-3 mass, normalised entropy | ~125 (25 levels × 5) |
| open-vocabulary identity | hashing trick, 64 buckets each, for `exe` (466 distinct), `verb` (490), `file` (687), `dir` (133), `component` (65), `term` (1,334) — **or omit entirely and keep only the shape row above** | 0 or 384 |
| effort | `request_tokens`, `authored_bytes`, `authoring_turns`, `fast_share`, `n_gaps`, `gap_p50_s`, `gap_p90_s`, turn count, prompt count — all `log1p` where unbounded | ~10 |
| dynamics | per kept dimension (`branch`, `output_type`, `language`, `skill`): `turnover`, `decay`, `concentration_shift`, `changed` as 2 dims (3-state), `status` one-hot (6), `reading` one-hot (7) | ~68 |
| prior contrast | per `PRIOR_DIMENSIONS`: `agrees`, `departure`, `novel` | ~12 |
| positional | hour-of-day sin/cos, day-of-week sin/cos, `log(minutes since session start)`, time since last turn, span length | ~8 |

**≈ 315 dims without open-vocabulary identity, ≈ 700 with it.** Small, dense, and computable in
the deterministic backend with no model at all.

**Multi-scale is the answer to "moving windows of different lookback lengths".** Compute `S` at
`L ∈ {5m, 20m, 60m, 240m, session-to-date}` and concatenate. Five rollups ≈ 10 ms. The ladder is
what lets a model see "this 5 minutes is unlike this session's last 4 hours" — which is precisely
the discriminative, routing-relevant signal `why-we-characterise-windows` names as the goal most
often forgotten, and the same argument `lift` makes: *unusual is only meaningful against a
yardstick longer than the thing being measured.*

**Per-message structured row `m(i)`** (no text): role, tool-call count, tool kinds one-hot, the
turn's own `action` mix, `magnitude.REQUEST_TOKENS` and `EDIT_BYTES` for the turn, gap since the
previous turn, distinct paths touched, position in block. ~50 dims. Already in `event` and
`turn_magnitude`; nothing new is parsed.

---

## 3. Approach A — structured-only, multi-scale (no text encoder)

**Message vector:** `m(i)` above.
**Window vector:** `[S(t,5m); S(t,20m); S(t,60m); S(t,240m); S(t,session)]` ≈ 1,575 dims.
**Joint:** trivially, the structured half alone. Optionally compressed to 128 d by a
self-supervised encoder trained on the corpus — a GRU/temporal-CNN over the 5-minute bin sequence
with a CoLES-style objective (disjoint sub-spans of the same session are positives, spans from
other sessions negatives), or straight next-bin prediction.

**Cost:** zero new model, zero new RAM, runs in `ml_backend:"deterministic"` which is v2's
shipping default. ~10 ms per window.
**Privacy:** no new surface. Excluding `term` from the hashed identity block makes it strictly
tool-call-derived, i.e. the pre-v18 invariant.
**Trains today** on the rebuilt 500-transcript / 496-session / 681,857-event frozen corpus.

**Why it is not merely a baseline.** The prose probe's frame rule lost badly (41.7% vs 79.2%) —
but on a *semantic* label (rhetorical function of writing), and `observable.py` already records
the structural diagnosis: **the levels record what was TOUCHED, not what it MEANT**, which is why
`activity_type` was refuted (0.218 against a 0.538 constant, `transform` predicted 36 times and
right zero). The targets here are *not* semantic — they are the next block's own structure. Using
structure to predict structure is the case where that diagnosis does not bite. Nobody has measured
it. **This is the cheapest and highest-information first experiment and it should be run before
any encoder is chosen.**

**Risk:** if the answer is "the last block's dominant value predicts the next block's dominant
value at 90%", we will have learned that the targets are trivially persistent and that the
interesting `y` is the CHANGE, not the value — which is a finding, and it reshapes the label set.

---

## 4. Approach B — frozen text encoder + structured, late fusion

**Message vector:** a small on-device sentence encoder over each human prompt (and optionally the
first N sentences of each assistant turn).

| candidate | params | dim | context | notes |
|---|---|---|---|---|
| **EmbeddingGemma-300M** | 300 M | 768, Matryoshka → 512/256/128 | 2,048 tok | built for on-device; <200 MB RAM quantised; requires task prefixes (`task: clustering \| query: …`) |
| **Qwen3-Embedding-0.6B** | 600 M | 1024 | 32,768 tok | strongest sub-1 GB open model; instruction-aware; the long context matters for whole-block text |
| reuse GLiNER2's DeBERTa-v3-large | 435 M | 1024 | 512 | **already resident** in `auto` mode — but NOT loaded in `deterministic`, which is v2's default, and it lives in the recycled worker child. Reusing it re-couples v2 to the model v2 exists to avoid. |

Recommendation: **EmbeddingGemma-300M at Matryoshka 256** for the first build — the 2,048-token
context is enough for a single prompt, and 256 d keeps storage at 512 B/message in fp16.
Qwen3-Embedding-0.6B is the upgrade if whole-block text (not per-message) turns out to be the
better unit, because 32k context means a block's text needs no chunking.

⚠️ **Where it runs.** Not in the GLiNER2 inference worker — that worker does not exist in
deterministic mode. Either a second lightweight worker child, or llama.cpp/ONNX in the FastAPI
parent. Note the parent-reserve arithmetic: `parent_reserve_mb()` is a **high-water latch** and
directly reduces the worker's hard limit, and the budget is already documented as oversubscribed
(`619.6 + 3409 + 512 = 4540.6` against 4096). In deterministic mode the 2.4 GB worker is absent
and a 200–400 MB embedder fits comfortably; in `auto` mode it makes the declared shortfall worse
and must be reported through `budget_shortfall_mb()` like everything else.

⚠️ **Bounding the input is a repo convention, not a detail.** "Never cut text mid-sentence" —
bound a message at a sentence/turn boundary, never at a token count that lands mid-clause, and
declare the drop (`omittedNotice` precedent). The measured cost of getting this wrong is on the
record: 46 of 47 generated beats came out mid-clause from a 200-rune cap.

**Window vector:** pool the message vectors inside the lookback. Three poolings worth measuring
against each other:
1. mean,
2. recency-weighted exponential mean,
3. attention pooling with a learned query.

Plus one derived scalar that is genuinely new information and slots straight into the existing
dynamics block: **text churn** `1 − cos(mean(slice), mean(baseline))`, the text analogue of
`dynamics.turnover`, computed across the same EWMA-sized cut `dynamics.py` already makes.

**Joint:** `z = [normalise(S(t,L)) ; E_text(t,L)]`, concatenated. First measurement with a plain
concat into gradient-boosted trees or a linear probe — that is the honest way to read the
*marginal* value of the text half against Approach A. A learned projection `W·z` comes after, only
if the concat shows lift.

**Storage:** 256-d fp16 = 512 B/message. At ~50 prompts/day × 400 days ≈ 10 MB/user, against a
live store currently at 25 MB. Negligible.

⚠️ **Privacy is the real cost, and it is a hard line.** A sentence embedding is invertible.
Published results: vec2text recovers up to 92% exact text for 32-token inputs and 89–95% of names
in clinical data; ALGEN shows embedding spaces are near-isomorphic at sentence level, so a
one-step linear alignment from a *single* leaked embedding–text pair suffices; noise and
quantisation defences trade utility roughly one-for-one. **A prompt embedding is therefore closer
to raw text than to `named_terms`, and it cannot cross the wire under the current invariant.**
Two honest paths, and they should be named explicitly rather than left to be discovered:
  (i) vectors stay on-device; training is local or federated;
  (ii) an explicit opt-in dogfood corpus for training only.
Approach C offers a third.

---

## 5. Approach C — jointly trained predictive two-tower (JEPA + cross-modal contrastive)

Two towers over the same span:

- `f_S` — structured tower: a small transformer or GRU over the *sequence* of 5-minute bins
  (each bin a level-histogram + magnitude vector), rather than over the pooled `S`. → `z_S ∈ R^d`
- `f_T` — text tower: the frozen encoder of Approach B plus a trainable pooling/adapter over the
  message vectors. → `z_T ∈ R^d`

Three self-supervised objectives, **no human labels**, all computable from the corpus:

1. **Predictive (JEPA).** A predictor `g` maps the joint latent of `[t−L, t)` to the latent of the
   *future* span `[t, t+h)` produced by an EMA target encoder. The loss is in latent space, not on
   reconstructed values. This makes the stated end goal — an embedding that predicts the future —
   the *training* objective rather than something a downstream head has to discover. Latent-space
   prediction is the right choice here for the same reason it is in V-JEPA: most of the future's
   detail (which exact file, which exact term) is unpredictable noise, and forcing reconstruction
   spends capacity on it.
2. **Cross-modal contrastive (CLIP-style).** `z_S(b) ↔ z_T(b)` for the same block, against other
   blocks. Aligns the two towers into one space.
3. **Sequence contrastive (CoLES-style).** Two disjoint sub-spans of the same session are
   positives, spans from other sessions negatives. CoLES is the established result for exactly
   this data shape (discrete event sequences per user) and reports embeddings on par with or
   better than hand-engineered features on downstream tasks.

**Why objective 2 is the strategic move, and not just a regulariser.** Once the towers are
aligned, **the structured tower alone can stand in for the text tower.** Text becomes a
training-time teacher that never has to leave the device or cross the wire — a distillation route
to a text-informed embedding that is publishable, because what ships is a function of tool-call
metadata only. And it is *measurable*: the experiment is "how much of the text tower's predictive
lift survives in the aligned stats tower", which is a number, not a hope. If the answer is "most
of it", the privacy problem in §4 dissolves. If it is "none", we know the text signal is
irreducibly textual and the on-device-only constraint is real.

**Cost:** torch 2.12 is already in the sidecar venv, so the training harness has no new
dependency. Inference is a <20 M-parameter tower, ~50 MB. Training runs off the hot path on the
frozen corpus or a dev machine.

⚠️ **Gated on data, and the gate is real.** The corpus is 500 transcripts / 496 sessions /
681,857 events → on the shipped cutter, **1,502 blocks**. That is enough for a linear or GBM probe
on frozen features (A and B) and for small adapters. It is **not** enough to train a joint
transformer from scratch. C is the destination; A and B are how we get the data to reach it. This
is the honest reason to build A and B *now*.

---

## 6. The label side (`y`), because it determines what to store

Specify this before collecting, or the collection is the wrong shape.

**Horizons:** next block; +1 h; +4 h; end-of-session.

**Targets, all free from the store:**
- *categorical* — next block's dominant `branch` / `lang` / `artifact` / `skill` / `toolchain` /
  `model` / `project`, **and their `status`**, so "will the next block even be attributable" is
  itself a target (7.7% of dimension-slots are `thin`, 38.7% `absent` — that distribution is
  information).
- *multi-label* — which `action`s / `tool`s / `file`s / `component`s occur next.
- *regression* — next-block `request_tokens`, `authored_bytes`, `fast_share`, `gap_p50_s`.
- *event* — time-to-idle (survival), block `end_reason` (`budget` 48.5% / `session_end` 33.0% /
  `idle` 18.5%), does the session continue.
- *change* — will the dominant value CHANGE. This is `dynamics.changed` computed FORWARD, and it
  is the routing-relevant target: routing needs to know a shift is coming, not that the last hour
  looked like the one before.

**Leakage traps specific to this codebase.** Name them now; each is silent.
1. ⚠️ **Workspace resolution is a whole-file pre-pass.** `workspace.scan_workspace` re-resolves
   09:00 turns from a `CLAUDE.md` read at 17:00, and `ingest.is_current`'s docstring says so
   explicitly. Any feature derived from workspace/`repo`/`root_dir` resolution is therefore **not
   a strictly causal prefix** of what the daemon knew at time `t`. For a *forecasting* study that
   is future information in `X`.
2. `reconcile` is clean **if** taken through `_rollup_at`, which re-scopes it with
   `pending_in(store, path, lo, hi)` — verified bounded by `start <= b[0] < end`. The *stored*
   reconcile slot is whole-file and must not be read directly.
3. `prior` is already half-open on the right (`[session start, block start)`) — causal by
   construction, deliberately, and reusable as-is.
4. **Retention silently changes the feature set over a long span:** `event` 400 days, `term` 90
   days. A training set spanning more than 90 days has a `term` level that vanishes partway
   through with nothing saying so.

---

## 7. Where the vectors live

Additive table in `refseries.db` — no migration, the `turn_magnitude` precedent
(`CREATE TABLE IF NOT EXISTS` upgrades in place; a new *column* would not):

    CREATE TABLE IF NOT EXISTS embedding (
      session TEXT NOT NULL,
      ts      REAL NOT NULL,
      kind    TEXT NOT NULL,        -- message | window:<L> | block
      model   TEXT NOT NULL,        -- encoder identity + dim + pooling
      dim     INTEGER NOT NULL,
      vec     BLOB NOT NULL,        -- fp16
      PRIMARY KEY (session, ts, kind, model)
    ) WITHOUT ROWID;

- `model` is **in the primary key** so re-embedding under a new encoder cannot silently mix two
  spaces. Same argument as `ingest.terms_mode` fingerprinting the terms pipeline's identity: a
  changed fingerprint must be visible, not absorbed.
- A `bin_level`-style registry (`embedding_kind`, REFERENCEd) so "which kinds are stored" is data
  in the database rather than a constant in one process's memory.
- **Retention must be declared, not inherited by accident.** A text-derived vector should take
  `term`'s 90-day policy (`KELD_REFSERIES_TERM_RETAIN_DAYS`), not the event horizon — it is the
  same class of signal. A structured-only vector can take the 400-day event horizon. Report both
  in `/metrics` under `store`, like every other retention policy.
- Store **message** vectors only; compute **window** vectors on demand by pooling. Otherwise a
  5-lookback ladder writes 5 rows per tick forever, and the pooling function is exactly the thing
  we want to keep changing.

---

## 8. Recommendation

**Build A now. Build B behind a flag. Treat C as the destination, gated on the data A and B
collect.**

First pre-registered study, on the rebuilt 500-transcript frozen corpus, horizon = next block:
predict the 8 allocation dominants + their `status` + `end_reason` + time-to-idle from

  (i) a majority / persistence baseline (last block's value — this is the bar that matters, not a
      class prior),
  (ii) `S(t, L)` multi-scale — Approach A,
  (iii) A ⧺ text embedding — Approach B.

Pre-register the bars before the run, as `BLOCK-BOUND-2` and the facet-value study did. The prior
to state in the pre-registration is the prose probe's inversion — digest inert, text live — and
the specific reason it may not carry: it was measured on a **semantic** label, and these targets
are **structural**. If A alone closes most of the gap to A+B, the privacy problem in §4 never has
to be solved, and v2 gets a predictive embedding that runs in `ml_backend:"deterministic"` with no
model at all.

## Sources

- Prose probe: `docs/superpowers/specs/2026-08-22-prose-probe-results.md`
- Block bound study: `~/keld/refseries-context/blocks/BLOCK-BOUND-2-{PREREGISTRATION,RESULTS,ABLATION}.md`
- CoLES — https://arxiv.org/abs/2002.08232
- LaT-PFN (JEPA for time series) — https://arxiv.org/pdf/2405.10093
- Qwen3 Embedding — https://arxiv.org/pdf/2506.05176
- EmbeddingGemma-300M — https://huggingface.co/google/embeddinggemma-300m
- Vec2Text — https://www.emergentmind.com/papers/2310.06816
- ALGEN few-shot embedding inversion — https://arxiv.org/pdf/2502.11308
