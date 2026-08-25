# Work-shift detection, and the signals it needs published

Two halves of one problem. Phase 0a established that the shipped detector finds **branch** change,
not **work** change — 7.1% recall against any-dimension truth, below a fixed constant's 17.3%. This
specs (A) publishing the signals that are computed and withheld today, and (B) a detector built on
them, admitted only on held-out evidence.

Nothing here ships on an argument. Part B has a pre-registration and a held-out split, and the null
result is a permitted outcome.

## Part A — the signals already computed and not published

| signal | where | published today |
|---|---|---|
| `EDIT_BYTES` per turn | `analysis/magnitude.py`, `turn_magnitude` table | yes, summed, as `effort.authored_bytes` |
| **`REQUEST_TOKENS`** (price-weighted, per request — the SPEND series) | same | **no** |
| `TOKENS` (per-line rollup weight — **never sum it**) | same | no, and correctly so |
| inter-turn gaps | `analysis/latency.py` | only `gaps` (a count) and `fast_share` |
| **the gap distribution** | derivable from the same times | **no** |
| inventory membership | `files`, `components`, `programs`, … | the sets, yes — **their movement, no** |

⚠️ **Publishing the token weight contradicts nothing, and the reason needs stating** because a study
appears to have rejected it. `548e469` measured the price-weighted token count as an **attribution
weight** — does weighting turns by cost change which value wins a dimension? — and the answer was
no: the dominant value changed in **51 of 5,752 slots, 0.89%**. That is a correct rejection of a
specific use. It says nothing about the number's value as a published measure of what an hour cost,
or as an input to change detection, neither of which was tested.

⚠️ **CORRECTION, 2026-08-25 — the field this spec originally named `weighted_tokens` WAS THE TRAP
THE CODEBASE ALREADY GUARDS AGAINST, and it is removed.** The first draft asked for
"price-weighted input-token equivalents **over the block**", i.e. a sum of `TOKENS`.
`magnitude.py`'s own docstring forbids exactly that:

> `REQUEST_TOKENS` — the request's cost, ONCE per `requestId`. This is the SPEND SERIES: it is what
> sums to what a window actually cost. `tokens` does not — summing it multiplies a request by its
> line count (median 2, up to 12 measured) — and a number that looks like a spend total while
> over-counting by 2x is exactly the plausible-wrong-number failure this codebase keeps paying for.
> Hence two names, one per question, instead of one number and a caveat.

`TOKENS` is a PER-LINE weight, existing so a weighted rollup reduces to an event-weighted one when
every request costs the same. It is not a window total and must never be summed into one.

And the field was redundant as well as wrong: `levels.py:242` computes
`w = magnitude.token_weight(u)` — the price-weighted value — and emits it as `REQUEST_TOKENS` once
per request. **`request_tokens` summed IS the price-weighted window cost.** One field, correctly
named, already carrying the number the draft asked for under a name that invites summing it wrongly.

⚠️ **`request_tokens` partly duplicates telemetry, and the wire must say so.** Atlas already
receives `input_tokens`/`output_tokens`/`cache_read_tokens` per `ToolEvent`. This figure differs in
two ways: it is **window-scoped** (summed over the block, not the call) and **price-weighted** into
input-token equivalents — `input + 1.25*write_5m + 2.0*write_1h + 0.1*read + 5.0*output`, ratios
rather than dollars, so it needs no price table. Publishing it without saying so invites a consumer
to double-count. It belongs in `effort` beside `authored_bytes`, not in a new block.

What to add to `effort` — THREE fields, not four:

    request_tokens       int    price-weighted spend over the block, once per request
    gap_p50_s            float  median inter-turn gap
    gap_p90_s            float  90th percentile — the tail is where "stopped to think" lives
    Each omitted when its evidence is absent, never defaulted to 0.

`gap_p50`/`gap_p90` are the addition with the clearest independent value: `fast_share` collapses the
whole distribution to one side of a 5-second threshold, so a block of steady 30-second turns and a
block of alternating 2s/5m turns are currently indistinguishable.

## Part B — why single-level detection has a ceiling

Every candidate Phase 0a tested reads ONE categorical level and encodes it as a per-bucket novelty
share. That has a structural limit, and the measured 7.1% recall is what the limit looks like:

**a work shift usually changes several things weakly rather than one thing decisively.** Moving from
writing code to writing docs shifts `output_type`, `language`, the file set and the act mix — each
too little to cross a per-level threshold, while the joint movement is unmistakable. A per-level
detector cannot see a conjunction. Seven independent threshold tests is not the same instrument as
one test on a vector.

So the mechanism changes, and this is why the contract says do not widen the level set:

    current:  did the dominant value of ONE level flip?
    proposed: how far did this window move from the previous one, across ALL of them?

One distance, one threshold, computed over a vector that mixes categorical and continuous parts.

### The vector

- **Categorical levels** (`branch`, `output_type`, `language`, `skill`, `component`, `action`) —
  each contributes the distance between consecutive windows' *distributions*, not their dominant
  values. Total-variation or Jensen-Shannon over the rollup, so a level that shifts from 55/45 to
  45/55 registers movement while its dominant value does not flip at all. This alone may explain
  much of the missing recall.
- **Inventory sets** (`files`, `components`, `programs`) — Jaccard distance between consecutive
  windows' member sets. **Nothing computes this today**, and a changed *set* is a different fact
  from a changed dominant value. The `turnover`/`decay` pair already published for dynamics is
  exactly this idea applied per-dimension; here it feeds detection instead.
- **Continuous signals** (`request_tokens`, `authored_bytes`, `gap_p50`, `fast_share`) — normalised
  per session, then contributing standardised deltas. A shift from reading to authoring is a tempo
  and volume change with **no categorical change at all**, which is a whole class of boundary the
  current detector structurally cannot see.

Weighting across parts is the thing most at risk of overfitting, so it is not chosen here — it is a
Phase-1 output with the split below as its guard.

### ⚠️ The overfitting risk, and the split that answers it

A distance over ~12 inputs with per-part weights has many more knobs than a two-EWMA threshold, and
the qualifying population is **32 sessions**. Fitting and evaluating on the same sessions would
produce a number that means nothing.

**The held-out set is a disjoint set of TRANSCRIPTS, never a disjoint set of windows.** Windows
inside one transcript overlap ~12x and share their session's entire character; splitting on windows
leaks the answer wholesale. Split the 32 qualifying transcripts 20 / 12 by a fixed seed, tune every
parameter on the 20, and **report only the 12.** The tuning-set score is reported too, and a large
gap between them is itself the finding.

### Pre-registered bars — to be written to
`~/keld/refseries-context/blocks/SHIFT-DETECTION-PREREGISTRATION.md` before any tuning

1. **BEATS BOTH INCUMBENTS ON HELD-OUT DATA**, on wide ground truth: precision and recall both above
   `branch`'s 49.5 / 7.1 **and** above `FixedSizer(15)`'s 27.6 / 17.3. Beating one incumbent on one
   metric is an operating point, not a result.
2. **MARGIN.** >= 10 points on at least one metric with no regression on the other, held-out. Below
   that it does not justify a vector, a weighting and a second code path.
3. **SURVIVES SHUFFLING.** Held-out precision must drop >= 20 points when transitions are relocated
   to random non-empty bins of the same session. This is the control that collapsed the EWMA 86.4%
   -> 24.1% and exposed `action` at **-3.5** — better against random truth than real truth.
4. **NOT DEGENERATE.** Fires on 2-50% of windows.
5. **GENERALISATION GAP REPORTED.** Tuning-set and held-out scores both stated. A held-out score
   more than 15 points below the tuning score means the weighting is fitted to 20 transcripts and
   is reported as such, whatever the absolute numbers say.
6. **ABLATION.** Score the three parts separately — categorical distributions alone, inventory
   Jaccard alone, continuous alone — and the full vector. If one part carries the result, ship that
   part and not the vector. The simplest thing that clears the bar wins.
7. **A NULL IS A RESULT.** If the vector does not beat `branch` held-out, `branch` stays and the
   contract's `budget`-is-common framing stands. Written down, not worked around.

## Sequencing

Part A is independent and can ship first: it is additive fields on `effort` plus a bump, and the
signals are already in the store. Part B needs Part A only for `request_tokens` and the gap
percentiles as detector inputs — the categorical and Jaccard parts need nothing new.

Part B does **not** block Phase 1. The contract already states `branch` as the detection level and
`budget` as the common boundary; a better detector improves block edges later without changing the
block model, the wire shape, or `covers`.

## Out of scope

- Changing what a block IS. This is about where its edges fall.
- `river` or any new dependency. It was measured and rejected; if a distance needs a library, that
  is a finding to report, not a shortcut to take.
- Routing. A better boundary makes a better block; what to do with one is its own decision.
