# Text vectors (`KELD_TEXTEMBED`), the `/features` cursor route and the publish half

> **Provenance — split out of `AGENTS.md` on 2026-09-17** (at `67be5f5`), verbatim.
> The material below was last substantively changed in `AGENTS.md` on **2026-08-26**.
> Every measurement here is stated as it was when written; the split re-verified
> **none** of them. Treat an undated figure as "true when measured, unknown now",
> and re-measure before acting on one.

AGENTS.md → the `KELD_TEXTEMBED` bullet and *The signal-embeddings publish half*
state the privacy argument and the toggles. This file carries the encoder measurements, the frontier contract and
the cursor's refusals.

⚠️ **`KELD_TEXTEMBED` (default OFF) is the TEXT half of the same corpus, and it is the
first thing in this repo that reads message text in order to keep something derived
from it** (`analysis/textembed.py`). The deterministic half enters as numbers; this
half enters as a text embedding, and neither is ever serialised into the other's
modality — a bi-encoder fed digest PROSE answered `record` on 36 of 36 inputs, so
that direction is closed.
- **The unit is the MESSAGE, not the shell.** A 240-minute shell holds hundreds of KB;
  `tool_result` lines are the huge ones `turns_in` skips unparsed and must stay that
  way (this module reads only `text` and `thinking` content BLOCKS, so a `tool_result`
  riding a `tool_use` line is unreadable by construction, not by filter); and shells
  overlap across rows, so per-message encoding means each message is encoded ONCE EVER
  and every shell reuses the vector. Three streams — `user`, `asst`, `think` — kept
  separate and never concatenated. `think` is `skipped:empty` in practice: 9,144
  thinking blocks re-measured over the 40 largest local transcripts, **0 non-empty**.
- **Qwen3-Embedding-0.6B via `transformers.AutoModel`, encode 1024-d, publish MRL
  prefix-sliced to 256-d.** Nothing was added to `sidecar/requirements.txt` — gliner2
  already pulls torch and transformers. The 256 is the one parameter that cannot be
  revised retroactively: a corpus collected at 256 cannot be widened without
  re-embedding every machine's history.
- **Its own child process, and bf16 is MEASURED on both axes.** Not the FastAPI parent:
  `parent_reserve_mb()` is a high-water latch, so anything resident there permanently
  shrinks the inference worker's hard limit. Measured on 200 real messages, 2 threads:
  float32 **3113 MB / 804.0 ms per message**, bfloat16 **1673 MB (1813 peak) /
  766.2 ms** — bf16 is 1313 MB cheaper and no slower, so it is not a latency trade.
  Idle-unloaded (`KELD_TEXTEMBED_IDLE_UNLOAD_S`), never spawned when the toggle is off.
  ⚠️ **Two of those bf16 figures were SINGLE-SHOT and did not survive the sustained
  arm** (`loadtest embed`, 104 messages / 23 batches / 181 s on a real 14.4 MB
  transcript, same host): resident replicates (1673 → **1700 MB**), but the peak is
  **2345-2432 MB, not 1813** — a one-shot script cannot see an in-flight transient, and the peak is
  not a stable number (2072/2345/2414/2432/2389 across five runs) — and the
  cost is **1119-1635 ms/message, not 766.2** (message LENGTH is the variable, and ~1.1-1.6 s
  is what `featuretext`'s independent ~1.44 s/message already said). The dtype comparison
  is unaffected — both arms ran the same inputs — and bf16 stands on the 1313 MB, which
  replicated. Size any per-message or per-block cost off **~1.6 s**.
- **Absent weights are a STATED status, never a crash or a stall.** They are provisioned
  on demand into `~/.keld/models` and handed over as `KELD_TEXTEMBED_DIR`, the sibling of
  `KELD_GLINER2_DIR`; nothing downloads at import. `degraded:weights_unavailable`, an
  empty vector list, and a retry cooldown — not a latch, because provisioning is
  asynchronous, and not per call, because a failed spawn costs seconds.
- **A fixed ORTHOGONAL projection is applied before publish**, generated deterministically
  from `KELD_TEXTEMBED_PROJECTION_SEED`. It preserves cosine and inner products exactly,
  so training is unaffected, and it withholds the embedding space from off-the-shelf
  inversion tooling. ⚠️ The matrix is **Keld's, not the client's** — issued to the fleet,
  so the client multiplies by a constant it did not choose.
- ⚠️ **Never cut a message mid-sentence.** Long messages are split at sentence boundaries
  and the chunk vectors mean-pooled; a single sentence over the cap is dropped WHOLE and
  the drop is declared as `dropped_chars`. Every scalar (`dispersion`/`drift`/`novelty`)
  is `None` where it could not be computed, never 0.0 — an absent comparison and a
  comparison that found no movement are different facts.

⚠️ **`POST /features` is a CURSOR route, not an anchor-instant one, and the sidecar chooses the
anchors** (schema **17**; `analysis/features.py`'s `feature_rows`, `analysis/featuretext.py`).
`{path, since_ts, now, max_rows, resolved}` → `{schema, rows, watermark}` — `POST /blocks`' shape,
because only this process owns the store and can therefore see where the non-empty 5-minute bins
and the closed blocks are; a daemon supplying a grid would have to guess it. The anchor-instant form
is kept, unchanged, as **`POST /features/probe`** for studies, which want raw floats in `manifest()`
order rather than the transport. Three anchor kinds ride one **globally chronological** stream
(`message` / `bin` / `block`) and `since_ts` is `>` on a row's own instant, so a batch is cut at an
instant boundary and **never inside one** — two rows can share a 0.1 s tick, and emitting half of
them would advance the caller's cursor past the other half forever. `anchor_id` is REQUIRED on a
`message` row (the turn's uuid) for the same reason. Rows carry the vector int8-quantised as
`{dims, scale, q}`, `q` base64 of two's-complement bytes, `dims` declared so the Go side compares
rather than trusts. Measured end to end on a real 26 MB transcript: **1.6 s for 96 rows** structured
only, 604 rows across the whole session replayed in 90 cursor calls with **0 lost and 0 repeated**,
and **0 of 604 rows dropped** by `sidecar.FeatureRowsFor`'s six refusals.
- **`FEATURE_SPEC_VERSION` is 2 and `DIMS` is 1534, not the spec's 1,414.** `S(t)` gained the
  per-shell, per-stream text scalars (`<shell>.text.<stream>.{n,dispersion,drift,novelty}` plus a
  `_known` flag per scalar) and `row.meta.text_recorded`. ⚠️ Those 106 slots are present **whether
  or not `KELD_TEXTEMBED` is on**: a width that depended on a machine's environment is exactly the
  incoherent-corpus failure the frozen manifest exists to prevent, so the flag beside them is what
  says they may be read — `capture_recorded`'s idiom one group along.
- ⚠️ **A `message` row exists ONLY where the text half ran, and that is ABSENT rather than empty.**
  A message has no lookback, so there is no structured vector to compute; with the toggle off the
  kind simply does not appear. `bin`/`block` rows never carry a `text` block at all — the centroid
  is not published — so the text half reaches them as those scalars.
- ⚠️ **ENCODING RUNS OFF THE REQUEST, and that is forced by a measurement, not tidiness.** The
  daemon's sidecar client has a **5-second** timeout and one batch of 64 real messages costs
  **~92 s** (~1.44 s/message with the real weights, 2 threads — and **1119-1635 ms/message** re-measured
  under `loadtest embed`'s sustained arm, which is the figure to size with) plus the child's first
  load, measured at **2.8 s warm / ~20 s cold** (this line said ~90 s, one cold contended reading;
  the argument never turned on it, since even 2.8 s plus any encoding is past the 5 s budget);
  a whole 1,646-message session is ~40 minutes. A synchronous encode could not land at any
  useful batch size, and a timed-out POST is classed as *retryable*, so the failure mode is an
  unbounded retry loop rather than one slow response. `featuretext.TextSource` therefore serves what
  its cache holds (measured **0.12-0.44 s** per call, real weights) and hands the remainder to one
  background pass. The instant of the first message with no vector is the **FRONTIER**, and NO row
  at or after it is emitted — including `bin`/`block` rows — so every published row's text half is
  measured over a **complete** message history up to its own instant, and the cursor never runs past
  an unencoded message. `pending:encoding` is the stated status meanwhile.
- ⚠️ **A message the encoder RAN on and produced nothing for is cached as such, or the frontier
  LATCHES AND THE CURSOR WEDGES FOREVER.** A message whose every sentence exceeds the chunk cap is
  dropped whole and will never have a vector; left as a cache miss it would pin the frontier at its
  own instant permanently. That is kept distinct from a **degraded** encoder, which must be retried —
  and a degraded encoder drops the text half WHOLE and returns no frontier, because publishing rows
  over a prefix of the history while the rest is unreachable is the confident-number-over-a-fraction
  failure the frontier exists to prevent.
- The encoder child is idle-unloaded from the same 1 s poll loop that recycles the inference worker
  (measured **1.70 GB resident / 2.35-2.43 GB peak**, bf16, real weights — the "~1.9 GB" this line used
  to carry sat between the two and named neither) — nothing else would ever release it, because
  `/features` only ever spawns. ⚠️ **That unload is now MEASURED end to end rather than asserted:**
  `python -m loadtest embed` is the encoder's arm of the load-test harness (`sidecar/loadtest/`,
  opt-in, never part of `smoke`) and it drives a sustained encode off a real transcript to establish
  no leak (**+32 MB** over 180 s), a bounded peak, that idle-unload actually returns **1711 MB** to
  the OS and the next request respawns the child, that `/analyze`, `/blocks` and `/features` keep
  answering *during* a pass (p50 **17→20 / 117→165 / 54→77 ms**), and the per-message cost above.
  ⚠️ Its first run found `embed.peak_rss_mb` pinned to the TROUGH — 1717 MB reported against a live
  2072 MB — because `Encoder.maybe_unload` took the encode lock BLOCKING and stalled the very poll
  loop whose lock-free `observe_rss` ran one line earlier. That is the RSS-oscillation incident's
  shape one child over; fixed, and pinned by `app/test_guard_visibility.py`'s encoder block. **A
  lock-free sampler behind a blocking caller is not a lock-free sampler.**


**The signal-embeddings publish half (`internal/agent/features/`).** The daemon
side of `POST /features`: an emitter with a per-transcript cursor, the sibling of
`internal/agent/blocks/` and built the same way — it asks the sidecar which rows
exist past its cursor and publishes them. Rows ride `publish.FeatureRow` under
their **own `corr_scheme`**, never `Enrichment` or `BlockEnrichment`, because
Atlas keys enrichments `UNIQUE(org_id, source_id, corr_scheme, corr_id)` and
upserts `ON CONFLICT DO UPDATE` over every column — sharing a scheme OVERWRITES
rather than dedups, the same trap `publish/window.go` documents at length. The
corr id is `session@feature@anchor@key`: four segments against a block id's two
and a prompt id's zero, so the id spaces are disjoint by SHAPE as well as by
scheme. Transport is `clientevents`' batch path (extracted in `a00a1e1` so a
second route could reuse it), with its own spool dir — a shared one would
cross-post bodies between routes.
⚠️ **The cursor advances on BUFFERING, not on delivery**, because a batching path
cannot observe delivery. That is made safe by backpressure rather than by hope: a
sweep never takes more rows than the buffer has room for, so a full buffer HOLDS
the cursor instead of dropping rows the sidecar would never re-offer.

**Four toggles, all OFF by default, and they are not interchangeable.**
`KELD_CAPTURE` (the extra ingest rows + `bin_offset`) ⚠️ is fingerprinted into
`parse_state`, so flipping it forces one reparse — that is why it is separate,
and why turning publishing off must never cost a reparse to turn back on.
`KELD_TEXTEMBED` gates the encoder child. `KELD_FEATURES` computes and stores
rows locally; `KELD_FEATURES_PUBLISH` sends them to Atlas. The last two carry an
Atlas per-org override riding the existing settings poll (`Remote.Features`,
`Remote.FeaturesPublish`) — the `client_telemetry` precedent, remote overrides
local, and an OMITTED key leaves the local base rather than defaulting on, so a
silent fleet-wide enable is not reachable from the server.
The whole subsystem registers only under `ml_backend:"deterministic"`. Under
`"auto"` it is ABSENT — never registered, so it appears in neither
`facets_skipped` nor `extractor_versions`, which is this codebase's existing
distinction between a pass that was skipped and one that was never wired.

