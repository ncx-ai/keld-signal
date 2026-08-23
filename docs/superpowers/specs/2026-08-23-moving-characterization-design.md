# A moving characterization of the work being done

**Status:** design, awaiting review. Consumer side of
`2026-08-23-incremental-reference-series-store-design.md`, which specifies the store this reads
and deliberately left "what triggers a digest" unanswered.

## What is wrong today

Enrichment is triggered by a prompt. Each prompt recomputes a 60-minute window ending at itself,
and the result is stapled onto that prompt. Three consequences, all measured:

- **Waste.** A 60-minute window contains a mean of **3.8 prompts** (max 20 over 370 windows), so
  one hour of work is characterised ~4x on average and up to 20x, each time from a full transcript
  parse (0.8-1.0s on a 64 MB file).
- **No dynamics.** Every window is computed in isolation; nothing is stored, so nothing can be
  compared. There is no turnover, no drift, no rate of change — the handoff lists exactly these as
  unmeasured.
- **The trigger is the wrong shape.** Adjacent windows minutes apart share >90% of their mass, so
  the published series is a smear: differencing it measures noise at the edges, not change.

## The model

**A tick, not a prompt.** At a regular cadence the daemon characterises a recent slice of activity
**relative to a longer baseline**, and applies that characterisation to the activity in the slice.
The slice says what is happening now; the baseline says whether that is a change. "80% of the
recent slice is in `services/web`, against 30% across the baseline" is a shift. Neither number
alone is.

**This is a query, not a parse.** Against the incremental store a tick reads a handful of bins. No
transcript is opened.

### How the slice and baseline are sized is NOT decided here

A 10-minute slice against a 60-minute baseline was an illustration, not a design, and specifying it
would be hand-rolling a constant where validated precedent exists. The sizing method is a **seam**,
and which method fills it is a measurement, not a preference.

**Use a library, not a hand-rolled rule.** `river` (0.26.1) is the maintained streaming/online
-learning library and ships the canonical algorithms; it pulls only 3 packages (`river`, `scipy`,
`narwhals`). Candidates, all already implemented there:

    fixed slice/baseline      the baseline to beat — a constant, no dependency
    EWMA fast/slow            two decay rates compared; in-repo precedent already
                              (the sidecar rate governor runs on a CPU-EWMA)
    ADWIN                     adaptive windowing (Bifet & Gavalda): the window grows while the
                              stream is stationary and shrinks on change, with guarantees.
                              The direct precedent for "size the lookback adaptively".
    PageHinkley / KSWIN       sequential change detection over a metric stream
    DDM / EDDM / HDDM         binary-signal drift detectors

### The bar, and why it is set here

**This project has already refuted adaptive windowing once.** The handoff records that episode
detection via change-point boundaries was mutation-tested and reached only PARITY with a fixed
hourly window on label purity, at much higher complexity — "one non-dividing constant beat all of
it." That measured a different question (where to PLACE a reporting window, not when a metric
stream has drifted), so it does not settle this one. It does set the bar:

**A fixed slice/baseline is the default and stays the default until an adaptive method beats it on
this corpus under a pre-registered rule.** Adopting ADWIN because it is principled, without
measuring, would repeat the exact error the episode-detection experiment already paid for.

What to measure, and against what: the point of a moving characterisation is that a reader can
tell when the work changed. So the comparison is on agreement with observable transitions in the
corpus — the same ground the episode experiment used — not on the detector's own internal
statistics. Pre-register the rule before running, as every experiment on this branch has.

## Facets split by what they actually depend on

Today the window dimensions ride on every prompt because a prompt is the only trigger that exists.
They differ from text facets in what they read, and the tick makes that separation honest:

| | trigger | reads | latency |
|---|---|---|---|
| `sensitivity` (and later `task_type`, `speech_act`) | **per prompt, immediately** | the prompt TEXT | as now |
| workstream dimensions + dynamics | **per tick** | tool-call metadata over a span | up to `N` |

**Rule, stated rather than left to fall out: nothing safety-relevant may wait for a tick.** A PII
detection must not sit in a buffer for ten minutes. Text facets do not need a window and do not
get one.

## How the characterisation reaches Atlas

Two shapes. They are not equivalent and the choice should be deliberate.

**(a) Fan-out — the wire contract does not change.** Compute once per tick, attach the same
dimensions to each enrichment row for prompts in the slice. Atlas continues to receive dimensions
attached to activity and needs no change. This is the cheap path and it captures the whole saving:
one computation instead of ~4.

**(b) A window row Atlas joins.** Publish one characterisation per slice; Atlas joins activity to
it by time and identity. Less duplication, and it is the only shape that can carry a dynamic that
belongs to the WINDOW rather than to any prompt in it (turnover has no per-prompt meaning). Needs
an Atlas-side change and a new key.

**Recommendation: (a) first, (b) as the endpoint.** (a) is client-only and testable now; the
dynamics in the next section are what eventually force (b), because they are properties of the
slice, not of a prompt.

## What "moving" adds — the measures that need a baseline

Composition is what today's digest reports. These are the ones that require the slice/baseline pair
and are currently impossible:

- **Turnover** — how much of the slice's evidence is in values absent from the baseline. High
  turnover is a context switch.
- **Concentration shift** — the dominant value's share in the slice minus its share in the baseline.
- **Emergence / decay** — values entering or leaving.

None of these should ship on the grounds that they are now computable. Each needs the same
treatment the digest got: does it change a reader's answer? The store makes them cheap; that is not
the same as making them worth publishing.

## Rules that prevent the obvious failures

- **Idle ticks emit nothing.** A quiet machine must not publish empty characterisations forever.
- **Never characterise past the watermark.** The store's per-transcript `watermark_ts` bounds what
  is fully ingested; a tick whose slice extends beyond it waits rather than reporting a slice
  missing its last minutes.
- **A missing bin is not a zero.** `bin` is sparse and only default levels are precomputed
  (a level's absence means "not precomputed", not "no evidence").
- **Evidence floors must follow the slice.** `MIN_EVIDENCE = 5` was DERIVED for a 60-minute
  window (the first n at which `0.5**n` clears 5%). Any shorter slice carries proportionally less
  evidence, so a fixed floor will mark nearly every tick unattributed. The floor has to be a
  function of the slice, re-derived — not a constant carried over. **This is the most likely way to
  get a plausible wrong number out of this design.** It applies doubly to an ADAPTIVE slice, whose
  length varies per tick.

## Open questions

- **Which sizing method fills the seam.** Fixed is the default until measured otherwise; see the
  bar above. The earlier "stride should not divide span" finding concerned where a reporting window
  is PLACED relative to real transitions; here every event is characterised by its containing
  slice, so edge alignment matters less — but that is untested.
- **Which dynamics earn a place on the wire.** See above; answer with a measurement.
- **Interaction with the existing per-prompt `dedup_key`.** Under (a) the key is unchanged; under
  (b) a window needs its own identity.

## Out of scope

- The incremental store itself (its own spec).
- Re-introducing GLiNER2 in any form.
- Backfilling historical transcripts into a characterisation series.
