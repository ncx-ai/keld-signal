# The repo-history baseline — what is UNUSUAL about this hour

A window says `language: Python 93%`. That is a description, and every consumer already has it.
It does not say whether this hour was *unusual*, and for the routing goal — send this kind of work
to a cheap model, that kind to an expensive one — unusual is the entire signal. A description
tells you what happened; a comparison tells you whether it matters.

This specs the one comparison the client can make and does not: each window against **the history
of the repository it belongs to**.

## Why the session prior cannot do this job

The prior (`analysis/prior.py`) already compares a window to something, so the obvious question is
why a second comparison earns its place. Because the scope it uses was measured and found
degenerate for this purpose. From `scripts/refseries.py`'s `series` docstring:

> The baseline that `lift` divides by comes from `baseline_ev`, normally the whole history of the
> repositories the entity touches. Scoped to a session it has to: at 74 minutes old a
> transcript's own history IS the window, so **every lift collapsed to x1.0 and the column
> carried no information**, while the repository's history at the same moment put the branch at
> **x33.6** and the file at **x64.3**. "Unusual" is only meaningful against a yardstick longer
> than the thing being measured.

The prior is session-scoped by construction (`[session start, window start)`) — exactly the scope
already shown to carry nothing here. The two are complementary on four counts, not substitutes:

| | prior (shipped) | this spec |
|---|---|---|
| baseline | this session | the repo's whole history |
| direction | did the dominant value change? | what is unusually present, **and what is usually here and missing** |
| vocabulary | 4 allocation dims | every published level, inventories included |
| coverage | absent on **45.1%** of windows (a session's first) | works on a session's first window |

The second row is the one nothing else on the wire can express. There is today **no way to publish
a negative** — "Go is normally half this repo's work and did not appear this hour" is not
representable in any field. That is the gap.

## What it produces

Two readings, from one ratio.

- **`prominent`** — a value whose share of this window far exceeds its share of the repo's history.
  Real example from the digest: `file installers/macos/build-pkg.sh x34.7`.
- **`absent`** — a value with a substantial baseline share that this window does not contain at
  all. Real example: `lang Go (usually 50%)`.

## Constraint 1 — the baseline CANNOT be computed the way windows are

`/analyze` serves every window with `exclude_slots=(RECONCILE_SLOT,)`, and `Store.window_rows`
answers an excluded-slot query **entirely from `event`**, because a `bin` row has no slot dimension
to filter on. Two consequences:

- **Events are pruned at `KELD_REFSERIES_RETAIN_DAYS` (400).** A baseline defined over "all
  history" would silently mean "all history that survived retention", and a window whose baseline
  straddles the serving floor would be comparing against a truncated denominator without saying so.
- **Reading 400 days of events per prompt is the cost the store exists to remove.** The parse it
  replaced was 0.79s median; a long-horizon event scan re-introduces that class of cost on the
  enrichment hot path.

`bin` rows are ~3 orders of magnitude cheaper and, apart from `term`, **never pruned**.

⚠️ **So the baseline reads `bin` — and therefore INCLUDES reconcile rows, while the window
EXCLUDES them.** A ratio whose numerator and denominator are differently-scoped populations is not
a lift, it is an artefact. This is the same defect class as the digest's two tempo implementations
and reconcile's split-share bug: two things that look like one number.

**Resolution: the lift is computed from a MATCHED PAIR, both sides from `bin`.** The window's own
published `share`/`evidence` stay event-based and exact — they are a different question and must
not change. The lift carries its own bin-derived numerator, used for nothing else.

- `salience.basis` states which population the pair came from, so a reader can never assume it
  matches `workstreams`.
- A test asserts the numerator is the bin-derived value and NOT `workstreams[dim].share`. Getting
  this wrong produces a plausible number, which is the failure mode that survives review.

## Constraint 2 — cross-session rollup is new store surface

`rollup_window(session, start, end)` is per-transcript. "This repo's history" spans sessions, so:

1. Resolve the window's repo: the `repo` level's dominant value (added in `edb09cd`, resolved by
   the daemon from `.git/config` and sent in).
2. Find sessions carrying it: `SELECT DISTINCT session FROM bin WHERE level='repo' AND ref=?`.
3. Roll up those sessions' bins over `[epoch, window_start)`.

Step 3 is a new method, `Store.rollup_across(sessions, start, end, levels)`. It is NOT a parameter
on `rollup_window`: that function's contract is one session and `window.rollup`'s tie-break, and
widening it would put a cross-session concern inside the function the digest depends on.

⚠️ **When the window has no `repo`, there is no baseline and the block is ABSENT.** A directory
that was never `git init`ed is normal, not an error (measured: 1 of 50 transcripts, plus 4 whose
project dir no longer exists). Do not fall back to the workspace basename — two machines' `notes`
directories are not one history, and pooling them would manufacture a baseline out of a collision.

## Causality — the baseline ends at the window's START

The prior's spec had to be corrected for exactly this, and the correction transfers verbatim: a
baseline that includes its own window is degenerate, not merely weak. Measured there: `novel` could
never fire (0 of 1,022 windows, structurally), and every departure shrank monotonically with how
much of the session the window was.

So the range is `[epoch, window_start)`. Still causal, a strict subset of what the daemon knew.

## Decision 1 — which levels get a baseline

**Only levels that already publish.** That is now all nine inventories plus the allocation set.

Excluded, and why: `repo_mentioned`, `repo_from_text`, `vcs`, `workspace_evidence` and `ext`-only
internals are extracted but deliberately unpublished. A lift over them would put values on the wire
that no other field does — the block would become a side channel for exactly the levels a publish
decision has already been made about. `salience` must never be the first thing to publish a level.

A test pins the vocabulary as a subset of the published set, derived rather than restated, so the
two cannot drift — the same mechanism `PRIOR_DIMENSIONS` uses.

## Decision 2 — what crosses the wire

The stated conclusion, bounded. Two lists, no baseline tables:

    salience:
      basis: "bin"                       # which population the ratio was computed over
      horizon_days: 287                  # how much history actually backed it
      prominent: [{level, value, lift}]  # top N by lift
      absent:    [{level, value, usual_share}]
      status:    compared | no_repo | baseline_thin | no_history

`status` is always stated, from a closed set, for the reason the dynamics block states its own:
`tooling` is *absent* on 50.3% of 60-minute windows, and a reader who cannot tell absence from
stability reads churn off a dimension with no data. **Metrics only under `compared`.**

What does NOT cross: per-level baseline shares, counts, the session list, or timestamps. That is
the 16 KB arm — a characterisation of raw window numbers scored **-3.3/-20.0 on synthesis
accuracy, worse than supplying no context at all**, against **+36.7** for a digest of the same
facts that labelled each number and stated the conclusion. Here the *value* IS the conclusion
(`Go, usually 50%, absent`), so it ships; the tables it was derived from do not.

`horizon_days` is stated because a lift against 3 days of history and against 300 are not the same
claim, and no other field would reveal which one a reader is holding.

## Decision 3 — horizon and refresh

All history to `window_start`, with `horizon_days` reported. **Measure before caching.** A
bin-based cross-session rollup is expected to be cheap (the store's own numbers: 1,552,800 event
rows for 400 days is 174 MB, and bins are three orders below that), so a cache may be pure
complexity. The threshold for adding one is stated in Phase 1 below rather than assumed.

## Floors — two of them, and they are different questions

1. **Baseline sufficiency.** A lift computed against a value seen twice in the whole history is
   noise. The floor is on the VALUE's own baseline count, not the total.
2. **Window sufficiency.** `window.MIN_EVIDENCE = 5` already applies to the numerator, and its
   derivation carries over unchanged: under the null, unanimity over *n* observations has
   probability `0.5**n`, first clearing 5% at n=5.

Neither floor's value is chosen here. Both are Phase 1 outputs, derived the way `MIN_EVIDENCE` was
— from a stated null hypothesis, not from a round number.

## Phase 1 — MEASURE IT, and the bars are written down FIRST

This block is not built until it clears bars committed before the numbers exist. That discipline is
what killed `speech_act` (0.695 against a 0.713 majority baseline) and dropped three of seven
dynamics dimensions; it is the house rule.

Population: every window `/analyze` can answer over the frozen corpus, quiet ones included, sized
by the shipped `DEFAULT_SIZER`. Disqualifiers, any one of which drops a reading:

- **CONSTANT** — 90% of readings fall inside one 0.05-wide band.
- **RARE** — `compared` on under 10% of windows.
- **ALWAYS-YES** — the yes/no a reader acts on is yes on >= 90% of windows. (`named_terms` was
  excluded from dynamics on exactly this: non-zero on 98.3%, no window in which it says no.)
- **SIZE-DEPENDENT** — |correlation with log window volume| >= 0.5. A "salience" that mostly
  reports how busy the hour was is a volume proxy wearing a hat.

⚠️ **And a SHUFFLED-BASELINE CONTROL, which is what makes a positive believable.** Recompute every
window's lift against the history of a *different, randomly chosen* repo. If the readings barely
move, the signal was never about this repo's history and the whole block is a volume statistic.
This is the control that collapsed the EWMA sizer's precision 86.4% -> 24.1% and exposed every
fixed sizer as carrying no information at all (11.9% -> 10.9%, flat at chance). Pre-register it:
**a drop of under 20 points on the shuffled control disqualifies the reading.**

Thresholds to derive, not choose: the lift cut for `prominent` (the study used >= 3, unvalidated),
the baseline-share floor for `absent`, both floors above, and N for each list.

Report null results. If `absent` clears and `prominent` does not, ship one.

## Phase 2 — build, only what Phase 1 cleared

1. `Store.rollup_across(sessions, start, end, levels)` + the repo->sessions lookup.
2. `analysis/salience.py` — pure functions over rolled-up counts, no I/O, mirroring
   `dynamics.py`/`prior.py`. `SALIENCE_LEVELS` derived from the published set.
3. `analyze_window(..., salience=False)` opt-in, then default once measured.
4. Go: `sidecar.SalienceBlock` modelled SELECTIVELY — fields for `status`, `basis`,
   `horizon_days`, and the two lists, and **nothing else**, so a per-level baseline table has
   structurally nowhere to land. `enrich.SalienceStatuses` mirrored and pinned against the Python
   by reading that file, because the Go side DROPS an unrecognised value and the sidecar ships
   frozen and separately — version skew is real, and drift would silently stop publishing.
5. `publish.Enrichment.salience` + `WindowEnrichment.salience`; the exhaustive wire-shape
   allowlist and `sampleWindow()` fixture both updated, or the check passes vacuously.
6. `SchemaVersion` bump; sidecar `SCHEMA` bump.

## Out of scope

- **Cross-machine or org-wide baselines.** The store is one machine's. An org baseline is an Atlas
  concern and a different design.
- **Retiring `unusually_prominent`/`absent_but_usual` from `scripts/refseries.py`.** The study
  renderer keeps them under `vs_repo_history`, clearly labelled frame-derived, until this ships.
- **Making `salience` a routing decision.** It is an input to one. Naming a model per reading is a
  separate spec and needs its own evidence.

## The honest risk

`prominent` and `absent` are ratios over an open vocabulary, and an open vocabulary's tail is
mostly noise: a file touched once in 400 days has an enormous lift and means nothing. The floors
exist for that, and Phase 1 exists to find out whether anything survives them. The plausible null
result is that only the low-cardinality allocation levels (`language`, `output_type`, `branch`)
produce a stable reading and the inventories are unusable — in which case `absent` on those three
is still worth shipping, because a stated negative is a thing the payload cannot express today at
all.
