# Window analysis: workstreams, dynamics, the session prior, blocks and the tick

> **Provenance — split out of `AGENTS.md` on 2026-09-17** (at `67be5f5`), verbatim.
> The material below was last substantively changed in `AGENTS.md` on **2026-08-27**.
> Every measurement here is stated as it was when written; the split re-verified
> **none** of them. Treat an undated figure as "true when measured, unknown now",
> and re-measure before acting on one.

AGENTS.md → the `workstreams` bullet, *The dynamics block*, *The session prior*,
*The block cutter* and *Tick-driven window characterisation* state what publishes
and what must not move.
This file carries the studies that fixed every threshold in it.

- **`workstreams`** (`enrich/workstreams.go`) is the one pass that runs **no
  inference**: it asks the sidecar's `/analyze` for the deterministic dimensions
  of the hour of work ending at this prompt — the seven ALLOCATION dimensions
  (project, branch, model, output_type, language, skill, tooling) plus the
  published INVENTORY ones — counted from tool-call metadata.
  It takes COORDINATES (transcript path + prompt id), never text, and publishes
  as `workstreams` — a map of dimension → `Labeled` (`share` becomes the
  confidence). It declares `ModelFree`+`AlwaysRun`, and is registered only when
  the daemon has an analysis backend (`enrich.WithWorkstreams`) **and** the
  source is one the analysis can read — `claude_code`/`cowork` only
  (`enrich.WorkstreamsEligible`): the analysis resolves a prompt by Claude-Code
  JSONL shape, so Codex/Gemini prompt ids 404, and registering it there would
  downgrade every one of their jobs to `"partial"` for a facet that was never
  obtainable. Callers with no backend (eval, localagent) are unchanged. A failed analysis
  fails the pass (`pipeline_status:"partial"`) rather than publishing an empty
  set. Attribution needs **two** things, not one: the winning value's share is
  ≥ 0.50 **and** the window holds ≥ `window.MIN_EVIDENCE` (5) observations at
  that level. The second exists because a share is a ratio — one tool call
  gives share 1.0 by construction. Five is derived, not chosen: under the 0.50
  floor read as a null hypothesis, `0.5**n` first falls below 5% at n=5, so
  below it no share distinguishes the window from a coin flip. Measured on the
  572-window reference sample: 347 of 2927 attributed dimension slots become
  unattributed, 330 of which were publishing at share 1.0 and 129 off a single
  observation.
  ⚠️ **EVERY dimension now publishes, and the floor is a LABEL rather than a
  publish gate** (schema v21 / sidecar SCHEMA 16). This reverses the rule that a
  sub-floor dimension is simply absent, and it reverses the second half of
  MIN_EVIDENCE's own justification, which used to read "`evidence` is dropped on
  the way to the published enrichment, so nothing downstream can tell one
  observation from five hundred". It isn't dropped any more: `Labeled` carries
  `evidence` (the count) and `status` (`attributed`/`thin`/`tie`/`no_majority`/
  `absent` — `enrich.WorkstreamStatuses`, the sidecar's `window.REASONS`), both
  `omitempty` so the ML facets that share the type are byte-unchanged. Deleting
  the dimension cost 924 of 12,016 measured dimension-slots (7.7%) that held
  **real evidence and published nothing** — 198 of them one observation short —
  with `toolchain` discarding more slots (172) than it published (138).
  **The floor did NOT move and nothing was promoted:** `attributed` still means
  exactly what it meant, the two conditions above are unchanged, and removing
  them would take P(false attribution) from 0.031 to 0.50. A consumer that
  renders `thin` identically to `attributed` is misreporting — the contract
  states that and cannot enforce it. A dimension is now
  present-with-a-stated-outcome, never silently missing; only a **pre-16
  sidecar**'s JSON null still drops (it sent no count and no status, so there is
  nothing to state), and an object with no `status` from that same sidecar reads
  as `attributed`, because that sidecar emitted an object only when it had
  attributed.
  **Nothing from `/analyze` reaches Atlas except those dimensions and the nine
  inventory ones.** ⚠️ **All nine inventories now publish, including
  `named_terms`** — a deliberate reversal (schema v18) of the rule that governed
  this file for most of its life. `inventory.named_terms` is proper nouns lifted
  from **message text**, matched against no declared vocabulary, and real person
  names have been observed in it ("Federico", "Daniel"). It used to be
  unmodelled on `sidecar.AnalyzeResult` precisely so a publish path had
  structurally nowhere to forward it; it is now modelled like its eight
  siblings, bounded by SHAPE alone (`sidecar.convertNamedTerms`).
  There is deliberately **no person-name filter**, and adding one would be worse
  than the absence: spaCy's person detection measured **~1% precision** on this
  corpus (998 of 1,090 spans with zero confirmed names), which is why presidio's
  `SpacyRecognizer` was removed from `sensitivity` outright. A filter at that
  precision does not remove names, it only removes the belief that names are
  present. The alternative that was NOT taken, and is still the safer shape if
  this is ever revisited, is `/match` + `publish.Custom`: an org declares its
  customers/suppliers/initiatives and only the **matched id** publishes — never
  a span, an offset, or the text. What still holds: `inventory` as a BLOCK
  remains unforwardable (a test pins it), so a tenth key the sidecar adds later
  cannot ride along; no raw prompt text, no spans and no offsets cross; masking
  is still enforced Go-side.
- **The PATH inventory dimensions (`files`, `directories`, `components`) publish
  a frequency distribution, and their caps are per-level.** The `file`/`dir`/
  `component` levels were extracted and stored long before they were published;
  they answer "which paths were hot this hour", which is a distribution, not a
  single owner — so they are INVENTORY, never ALLOCATION. ⚠️ **The blanket
  open-vocabulary cap of 12 is wrong for them**, and silently: measured over 165
  one-hour windows, distinct-per-window runs p50 8 / p90 32 / max 54 for `file`,
  p50 5 / p90 14 / max 27 for `dir`, p50 3 / p90 7 / max 17 for `component`, so
  a cap of 12 truncates **33% of windows** on `file` alone. Truncation is top-N
  by count, so a hotspot can never be the thing cut — but the tail is what
  separates "this hour touched three files" from "this hour touched forty", and
  losing it silently makes a scattered window read as a focused one. Caps sit
  just above each level's own p90 (**40 / 24 / 16**) and the cut is **declared**
  in the sibling `inventory_omitted`, which is what stops a truncated inventory
  from being indistinguishable from a short one — the `omittedNotice` rule
  (Conventions → never cut text mid-sentence) applied one level up. That block
  covers ALL nine inventory dimensions, since the other six were silently
  truncating at 12 already.
  ⚠️ **What makes these safe to publish is that they are ALREADY
  workspace-relative** — `reconcile()` resolves every path against the resolved
  workspace root. Verified over the full 500-transcript corpus plus a Cowork
  session: **zero** absolute paths, zero `~`/`/Users`/`/home`, zero `../`
  escapes, zero URLs, zero Windows drive paths, at all three levels. It is
  gated TWICE, not merely tested: the sidecar payload asserts the shape, and
  `sidecar.notWorkspaceRelative` re-checks it per entry at the Go decode
  boundary, dropping a bad value without losing the rest of the list. Two gates
  because the vocabulary is OPEN — `physical_acts` can lean on a closed table,
  and these cannot. The residual exposure is repo *structure* (`services/api/app/billing`)
  and any customer name inside a filename — the same class `branch` already
  crosses. (That comparison used to end "not the class `named_terms` does";
  since v18 `named_terms` crosses too, so paths are no longer the more exposed
  of the two.) Do not add a producer for these
  levels that bypasses `reconcile()`. Note they are coding-heavy: a
  non-engineering session yields **3 distinct paths in total**, so an empty list
  here is a real answer, not a gap.
⚠️ **THE PROMPT INDEX HOLDS BOTH IDS, AND HOLDING ONLY `uuid` SILENTLY EMPTIED
EVERY ENRICHMENT.** A Claude Code user line carries two: `uuid`, unique per line,
and `promptId`, the identity of the human TURN — shared by every follow-on line of
it (measured on a real transcript: one `promptId` spanned **7 user lines across 8
minutes**). The daemon names a prompt by `promptId` and only by it:
`watch/filter.go` REJECTS a line without one, the spool pointer carries it, the
queue dedups on it, and it is published as `corr_id`, which Atlas joins against
`ToolEvent.prompt_id`. So `promptId` is the id `/analyze` and `/tick` are ASKED
about. While the index held only `uuid`, every lookup 404'd, the workstreams pass
**failed** (not skipped — a failed pass is what sets `partial`), and every prompt
published `pipeline_status:"partial"` with no workstreams, no dynamics and no
prior. Under `ml_backend:"deterministic"` that is the whole payload. Measured on a
live v2 machine: **8 of 8 prompts partial, and 0 of 1,627 stored enrichments had
ever carried a workstream.**
**Why no test caught it, which is the part to keep:** both halves of the sidecar
agreed on `uuid` — the index AND `analyze.py`'s oracle scan — so
`analyze_window_by_parse`, the equality test that guards the entire store,
compared two identical wrong answers; and every sidecar fixture built a user turn
as `{"type":"user","uuid":…}` with **no `promptId` at all**, so the sidecar's own
corpus did not look like a real transcript. The Go tests use fakes and never
crossed the seam either. An oracle that shares the bug proves nothing, and a
fixture that does not resemble production is why it could.
Both ids are now indexed, in FILE order under `upsert_prompts`' existing
`ON CONFLICT DO NOTHING`, so a shared `promptId` resolves to the FIRST line
carrying it — the human prompt's own instant, never a continuation's, which would
run every window minutes long. `analyze.py`'s oracle matches either id and **must
change in the same commit as the index**, or the equality test goes back to
proving nothing. `resolve/claude.go` already accepted either id when reading
prompt TEXT; this made the sidecar consistent with a rule the Go side already had.
Pinned from both ends by `sidecar/app/test_prompt_id_seam.py` and
`watch/filter_test.go`'s `TestHumanPromptIDIsThePromptIdFieldNotTheUUID`, which
name each other — **do not remove `promptId` from those fixtures.**
`ingest.STATE_VERSION` 4 → 5 is the repair: existing stores hold uuid-only
indexes and nothing recomputes them, so **expect one reparse per transcript on
upgrade.**


**The dynamics block (`analysis/dynamics.py`) — what MOVED in the window.** The same
`/analyze` call that characterises the window also answers what changed inside it:
`WorkstreamAnalyzer` returns one `WindowAnalysis`, so the dynamics cost **no second
round-trip and no inference at all** (two `rollup_window` calls, ~2ms each) and they
publish under `ml_backend:"deterministic"` too. The span is cut into a recent
**slice** and an abutting **baseline**, and each dimension is compared across the
cut. What crosses to Atlas is the derived half only:
- `status` — the comparison's own outcome, from a closed six-value set (`compared`,
  `both_absent`, `slice_absent`, `baseline_absent`, `slice_thin`, `baseline_thin`).
  **Always stated**, so a missing metric is readable rather than merely absent:
  `tooling` is *absent* on 50.3% of 60-minute windows, and a reader who cannot tell
  absence from stability reads near-constant churn off a dimension that has no data
  at all. **Metrics are reported only under `compared`.**
- `turnover` / `decay` — the share of slice evidence in values absent from the
  baseline, and its mirror. **Two different facts**: a slice can take on a new value
  without dropping an old one. Both are shares, so they are invariant to how busy the
  window was.
- `concentration_shift` — the slice dominant's share of the slice minus that same
  value's share of the baseline: is the thing that owns the window holding more of it
  than it used to. Withheld when the slice has no dominant value, rather than computed
  against an arbitrary pick.
- `changed` — did the dominant value change? **Three-state**: `false` for
  `both_absent` (a level that never fired did not change), and **nil** wherever the
  comparison cannot support a yes or a no. A plain `bool`/`float64` would render all
  of that as `false`/`0.0`, i.e. "we checked, nothing moved" — the single misreading
  the evidence-floor work exists to prevent.
- `reading` — the conclusion, **stated**, from a closed 7-value vocabulary in
  precedence order: `switched` / `narrowing` / `broadening` / `churning` / `widening`
  / `shedding` / `steady`. Computed entirely from the four fields above: no new
  inference, no second query. Unstated (empty) outside `compared`, never defaulted to
  `steady`.

⚠️ **Stating the conclusion IS the feature — emitting the numbers alone measured
worse than emitting nothing.** Three arms scored on the same windows: a 16 KB
characterisation of raw window numbers came in at **-3.3/-20.0 on synthesis
accuracy**, worse than emitting nothing, against **+36.7** for a digest of the same
facts. The digest was not number-free; it *labelled* each number and stated the
conclusion, and all 14 full-document failures were the one question where the reader
got `engineer_messages: 5` / `assistant_messages: 84` and had to divide. A bare
number also invites a **wrong** reading: asked "which ticket?", a model answered
**2659** — the window's own `reference_events` count — and labelling it moved correct
declines from **76% to 100%**. So the numbers ship **keyed** (in JSON the key IS the
label) beside the stated reading; the unlabelled remainder, which is what made the
losing arm 16 KB, does not — no per-side value/share/evidence/reason, no timestamps,
no sizer detail.

**That same cut is the privacy mechanism.** Every dynamics field that could hold a
reference level's own string lives in the per-side `slice`/`baseline` objects, and
`term` — the one level read from message text — has held real person names. So no
field that crosses the wire can hold a level value at all: the subtree's only strings
are `status` and `reading`, asserted exhaustively by a reflect walk at the decode
boundary and by a marshal-level wire test. Both vocabularies are mirrored Go-side as
`enrich.DynamicStatuses`/`DynamicReadings` and pinned against `dynamics.py` by
reading that file, because the Go side **DROPS** an unrecognised value — the sidecar
is frozen and shipped separately, so version skew is real, and a drift would silently
stop publishing a dimension instead of failing.

**`MIN_EVIDENCE` (5) is about SAMPLE SIZE, not minutes — and `MATERIAL =
1/MIN_EVIDENCE = 0.2` is derived from it.** Duration appears nowhere in the
derivation: it asks whether unanimity could have come from a coin, which depends only
on how many times the coin was flipped (`0.5**n` first falls below 5% at n=5, and
`min_evidence_for(floor, alpha)` deliberately takes **no duration argument**, so a
duration-scaled floor cannot be written by accident). A duration-scaled floor is not a
generalisation of that argument but a worse one: it would make the significance of a
published attribution a function of slice length while `value` and `share` look
identical either way. (`evidence` used to be **dropped before publish** too, so no reader
could tell a 3%-confident claim from a 50%-confident one; since v21 the count and the
status DO publish — see the workstreams bullet — which is what turned the floor into a
label. The duration argument is unaffected: a duration-scaled floor would still make
significance a function of slice length, and it is the FLOOR ITSELF that must not move.) Measured over 20,000 windows
(4,000 seeded anchors x 5 slice lengths, 55 transcripts / 542 MB): median `workspace`
evidence falls **130 → 20** from 60 to 5 minutes — a sixth, as the plan predicted —
but a sixth of 130 is still four times the floor. Buying back the 424 of 3,902
`project` slots the floor costs at 5 minutes gains 13.5 pooled points and takes
P(false attribution) from **0.031 to 0.50**. `MATERIAL` follows the same argument one
level down: at the floor a share is measured over 5 observations, so 0.2 is the finest
difference one observation can produce.

**The slice is sized by an EWMA change detector, and that beat a constant by
measurement.** `DEFAULT_SIZER = EwmaSizer()` (fast 0.3, slow 0.02, threshold 0.2)
encodes the `branch` series as a per-bucket novelty share and cuts at the LAST rising
edge of `fast - slow`, on a **60-second** observation step — deliberately finer than
the 5-minute bin, giving 60 observations inside the span budget instead of 12. Over 25
sessions / 111 transitions / 1,966 windows: **86.4% precision / 54.8% recall against
`FixedSizer(15)`'s 11.8% / 27.8% — +74.6 and +27.0 points**, firing on 27.0% of
windows, median detection **2.0 min** from the nearest real transition against fixed's
10.0.
- **The shuffled-truth control is why that is believed.** Relocate every transition to
  a random non-empty bin of the same session and the EWMA collapses **86.4% → 24.1%**,
  while every fixed sizer **barely moves (11.9% → 10.9%)** — because a constant offset
  carries no information about the work and **was never a detector**. The fixed sweep
  is flat at chance across 5-30 min.
- ⚠️ **`river` was measured and REJECTED; do not add it hopefully.** Its best detector
  (PageHinkley, 55.5% / 34.3%) clears the pre-registered rules but is **dominated on
  both metrics** by an idiom already in this repo (the sidecar's own CPU-EWMA rate
  governor); ADWIN and KSWIN lose outright, and ADWIN scores *better* on shuffled
  truth than on real truth. Their defaults are silent inside a **60-observation
  budget** and their detection lag (6-9 buckets) exceeds the 5-minute hit tolerance,
  so those two structurally cannot hit. `sidecar/requirements.txt` is untouched.
- `FixedSizer` stays as the **no-detection fallback**, which is 73% of windows, and
  `SLICE_MINUTES` stays **15**. 10 minutes was the measured optimum for a constant
  standing ALONE; behind a detector the constant only ever runs on stationary work,
  where localisation is irrelevant by definition and attribution rate is the metric
  instead (`language` 68.0% vs 63.0% at 15 vs 10). Both numbers are measured on their
  own population. Detection reads **`branch` only**, because that is what could be
  measured — `workspace` has **ZERO** transitions in 51 sessions, a transcript being
  scoped to one project dir. Widening the level is unmeasured.

**Half the dimensions were dropped on their own distributions.** Cheap was not an
argument for emitting one, so every dynamic was measured over EVERY window `/analyze`
could answer — **51 sessions, 2,702 windows**, the quiet ones included, sized by the
shipped `DEFAULT_SIZER` — against a bar written down FIRST: disqualified if 90% of
readings fall inside one 0.05-wide band (CONSTANT), if `compared` on under 10% of
windows (RARE), or if the yes/no a reader acts on is yes on ≥90% of windows
(ALWAYS-YES).
⚠️ **RE-MEASURED 2026-08-25 on 500 transcripts / 2,555 windows, because the original ran on
55.** The store every pre-`bbb74b4` study used held 55 of 500 transcripts — a unique-session-key
filter dropped all 445 `agent-*.jsonl` subagent transcripts, whose names collide in 8 characters.
On the rebuilt corpus `project` (100% zero), `model` (98.6%) and `tooling` (comparable on 3.2%)
all REPLICATE, so the drops stand on 9x the evidence. Full results:
`~/keld/refseries-context/blocks/DYNAMICS-REMEASURED.md`.
⚠️ **And the CONSTANT band test alone MISCLASSIFIES A SPARSE SIGNAL — never apply it without the
inside/outside contrast beside it.** Re-measured, `branch` sits at 90.2% inside one band and so
"fails" CONSTANT by 0.2 points, while its turnover is **0.346 inside a transition window against
0.003 outside** — a 115x separation, and the actual reason it is kept. A metric that is zero on
90% of windows and large on the rest is concentrated AND informative; those are not opposites and
the band test cannot distinguish them. Applied alone it recommends removing exactly the signals
that fire rarely and mean the most. `skill` likewise re-measures at 9.0% comparable against the
10% RARE bar, down from 12.6%, and is KEPT: the 445 newly-included subagent transcripts are short
and skill-free, so they enlarge the denominator without adding comparable windows — a population
effect, not a weakening signal.
`DROPPED_DIMENSIONS = ("project", "model", "tooling")`:
- `project` — turnover, decay and shift **identically 0.000 on all 2,180 compared
  windows**, `changed` never True, reading `steady` 100.0%. Constant **BY
  CONSTRUCTION**: a transcript is scoped to one project directory, the same fact
  `DETECT_LEVEL` is pinned on.
- `model` — turnover exactly zero on 98.5% of 2,126 windows, lift against ground truth
  **+0.000**, and `changed` **True 0 times in 2,702 windows**.
- `tooling` — `compared` on **3.9%** (106 of 2,702), and where it IS comparable it
  points the **WRONG WAY**: mean turnover 0.010 inside a transition window against
  0.070 outside.

KEPT: `branch` — mean turnover **0.346 INSIDE** a transition window against **0.003
outside**, which is what a change-of-work metric looks like — plus `output_type`,
`language` and `skill`; the last is 2.6 points above the RARE bar and that is
stated at the constant rather than smoothed. Inventory levels are excluded
structurally and the exclusion was confirmed by distribution rather than by argument
(`integrations` `compared` on **0** of 2,702 windows; `named_terms` non-zero on
**98.3%** — no window in which it says no, a disqualifier needing no ground truth).
`DYNAMIC_DIMENSIONS` is derived from `workstreams.ALLOCATION` minus the dropped set,
and `dynamics()` neither takes nor forwards a `dimensions=` argument, so **the
published vocabulary cannot be widened by a caller** — the parameter exists only to
reproduce that measurement. The dropped three are still reported as allocation
workstreams by the digest; only their *dynamics* are gone.

**The session prior (`analysis/prior.py`) — the session this window sits in, reported
BESIDE it.** A window is characterised in isolation, so a value sitting just over the
attribution floor is indistinguishable from one that is the whole story. The session is a
cheap, stable frame of reference that makes the difference visible: per dimension a
`value`/`share`/`evidence`/`status` for the session, plus three contrast measures —
`agrees`, `departure` (the window's share minus that value's share of the prior) and
`novel` (the window's value never occurred before it). Same `rollup_window`, wider bounds,
no second parse and no inference.

⚠️ **CONTRAST, NEVER FALLBACK — every other rule here is subordinate to this one.** The
prior never supplies a value the window lacked: with no window value all three contrasts
are `None` and `workstreams` keeps its honest blank. A thin window inheriting the
session's value buys coverage by laundering "we do not know" into something confident,
which is the exact defect `MIN_EVIDENCE` exists to prevent and which this project has
already paid for twice (`activity_type`'s `transform` predicted 36 times, right zero;
`speech_act`'s `statement` 22 times, right zero). **45.1% of windows have no prior at
all** — 461 of 1,022 are a session's first — and that number is the standing pressure to
soften this. Don't. The block is emitted anyway, saying `absent` out loud, because a
suppressed block reads as an oversight and an oversight is what someone eventually
"fixes".

⚠️ **The prior is cut at the window's START, which is a deliberate correction to its own
spec.** "The session so far" taken literally puts the window INSIDE its own prior, and
that reading is degenerate rather than merely weak: `novel` cannot fire — 0 of 1,022
windows on all seven dimensions, structurally — a session's first window IS its own prior
(agreement 100%, departure 0), and every departure shrinks toward zero monotonically with
how much of the session the window is (`language` agreement 70.6% → 89.9%, `skill` 25.8%
→ 83.8%, purely from the overlap). So it covers `[session start, window start)`: still
causal, a strict subset of what the daemon knew, and the only reading under which all
three measures are non-degenerate. Nothing is accumulated — the prior is **recomputed**
per request from stored events, because an incrementally-updated one would drift from
those events with no way to check it.

**`ENABLED = ("branch", "language", "output_type", "skill")`**, decided over 1,022 windows
(`docs/superpowers/specs/2026-08-24-session-prior-results.md`): `skill` 25.8% agreement /
44.0% novelty — the signal, being the phase transitions of the process — `language` 70.6%
/ 2.3%, `branch` 76.1% / 6.1%, `output_type` 86.7% / 1.1%. `project` and `model` agree
**100.0% with zero disagreements**, so a contrast there would publish a constant.
⚠️ **`output_type` was excluded on that 86.7% and the exclusion was WRONG** — not because
the number was wrong but because of what agreement can say: it is defined only where BOTH
sides are attributed, so it is silent about precisely the windows the dimension is for.
On John's Cowork session the prior carried `output_type` in **6 of 7** windows where the
window could not attribute at all (the deck is built in hour one; every hour after reads
`absent` while the session reads `presentation`) against `tooling` 4/7 and every other
dimension 0/7. That session's SHAPE outweighs its size: it is skill-free, as is 61.6% of
the corpus, and without `output_type` the block would rarely say anything for the majority
case. `tooling` stays out, with the bar for revisiting it written into `prior.py` and its
test rather than remembered: agreement ≤ 0.90 **or** prior-attributed coverage ≥ 0.70,
against its current 98.5% / 24.3%.

`PRIOR_DIMENSIONS` is **derived** from `workstreams.ALLOCATION` rather than restated, so
the two cannot drift and **an INVENTORY level is structurally not addable** — which is
what keeps `named_terms` (the one level read from message text, and which has held real
person names) out of this block by construction rather than by care. `status` is named
`status`, not `reason`: `reason` is on publish's `forbiddenWireKeys` as the dynamics
per-side key, and a second meaning on the wire is a reader's error waiting to happen. A
prior that is itself `no_majority` is **informative** — the window's ambiguity is the
session's — and is never collapsed into "no prior". Cost is 1.6 µs per dimension; the
block's ~16.7 ms is its two rollups, paid per call regardless of how many dimensions ride
it.

**The block cutter (`analysis/blocks.py`) — where a piece of work ENDS.** A window is an
arbitrary hour; a **block** is a contiguous span of one session's ACTIVE time, and where one
ends is the only question this module answers. Two terminators, and the set is closed:
**idle** — `IDLE_BINS` (3) consecutive empty 5-minute bins, i.e. 15 minutes of silence, which
is not a claim about the work but a claim there wasn't any — and **budget**,
`MAX_BLOCK_MINUTES` (20) elapsed, which is only "we had to cut somewhere". They are reported
separately (`REASONS = session_start / idle / budget / session_end`) because a reader who
cannot tell them apart cannot tell an arithmetic boundary from a real pause. Both numbers are
MEASURED, in a pre-registered four-arm study over 496 sessions
(`~/keld/refseries-context/blocks/BLOCK-BOUND-2-{PREREGISTRATION,RESULTS}.md`, harness
`scripts/block_sizing_eval.py`): a plain time cap (A′), an evidence-gated cap that defers
until the block can attribute (B′), a turn count (C′), and no bound at all (D′). **A′ won, at
20 minutes** — and on the CONSTRAINTS rather than on the metric: with dead air excluded every
arm attributes within a point of every other (**95.2-96.2%**), so what separated them is that
A′'s maximum block EQUALS its cap by construction (**0.33h**), while B′ is **bit-identical**
to A′ at every cap ≥ 30 min (the deferral gate never fires once idle is handled) and D′
produces **7.5-hour** blocks with **53.0%** of them spanning a whole session. `IDLE_BINS` was
fixed in the pre-registration and then swept (2/3/6 bins = 10/15/30 min) rather than left
asserted: at the shipped cap, **94.9%** attributable at 10 min, **95.3% at 15**, **93.4%** at
30. Retuning either re-opens the four-arm comparison.

**Blocks tile the ACTIVE part of a session, not `[lo, hi)`.** Idle splits the span into active
segments first and the cap runs WITHIN each; the dead air between them belongs to NO block. So
the invariant is **every active bin lies in exactly one block**, and never "the blocks cover
the span" — which is what round 1 of the study measured by accident, tiling silence with empty
20-minute blocks, and it cost arm A its attribution outright (**29.8% → 95.3%** once
corrected). A reader tempted to make blocks abut across a gap is reintroducing exactly that.

⚠️ **There is NO merge rule, and the absence is the design.** The 95% attribution bar was
DEFINED, in the pre-registration and before the run, as the point at which a merge rule becomes
unnecessary; A′@20 clears it (**95.3%**, **96.21%** after the ablation below). The obvious
repair — fold a thin block forward into its neighbour — was built and measured
(`BLOCK-SIZING-RESULTS.md`): it changes a published VALUE in **88.6%** of the merges it
performs, against a pre-registered **5%** bar, and merges chain (**94.6%** of the 7,391
absorbed blocks were followed by a block that was itself thin). Merging does not recover a thin
block's answer; it overwrites it with the neighbour's. So a thin block publishes UNATTRIBUTED
and survives as its own block — an honest blank, the same call `window.MIN_EVIDENCE` and
`prior.py`'s CONTRAST-NEVER-FALLBACK make one level down. Do not add a merge rule and do not
add a knob for one: a knob is a merge rule with the decision deferred.

⚠️ **A THIRD terminator was ABLATED, and every measured number improved.** The change detector
(`EwmaSizer` over the `branch` series) was the third terminator in the arm that won the
pre-registered comparison. A post-hoc ablation over the same 496-session corpus at the shipped
cap (`BLOCK-BOUND-2-ABLATION.md`) emptied its cut list: attributable **95.29% → 96.21%**,
blocks holding any evidence **99.3% → 100.0%** — the detector was the **ONLY** source of empty
blocks, which is the precise failure the idle terminator was introduced to eliminate — merge
rate **1.36% → 0.67%**, longest block unchanged at 20m. The mechanism is not subtle: a detected
cut ends a block EARLY, so it holds less evidence and is likelier to fall under `MIN_EVIDENCE`;
the detector was buying its **4.3%** of boundaries by thinning the blocks around them. What is
given up is the claim those cuts sat in more MEANINGFUL places — which is exactly the claim
**Phase 0a could not establish**: `branch` recalled **7.1%** of real work shifts against a
fixed-interval control's **17.3%**, and every alternative detection level failed its bar, with
`action` scoring **−3.5** — better on shuffled truth than on real truth. Two consequences worth
knowing: the bound is now **fully domain-agnostic** (detection was its only branch-dependent
part, so a session with no repository behaves identically — nothing missing, nothing degraded),
and `blocks.py` no longer imports `dynamics` at all. ⚠️ **`EwmaSizer` was NOT removed** — it
keeps its separate, measured, shipped use sizing the dynamics SLICE (above), which this
ablation does not touch. `_form` likewise keeps the `cuts` parameter it is now always handed
`[]` for, so the shipped arithmetic stays identical to the measured arm evaluated with an empty
cut list rather than being a second code path that would have to be shown equal to it;
`detected` is therefore deliberately absent from `REASONS`, which is what `cut` can emit.

⚠️ **`cut()` requires BIN-ALIGNED bounds and fails SILENTLY without them.** `active_segments`
filters on bin STARTS, so a bin straddling a non-aligned `from_ts` is dropped from every
segment while `rollup_window`, which takes exact instants, still counts the events inside it:
evidence lands in no block, nothing errors, and no block looks wrong. The caller owns the
alignment (`analyze._block_span` floors and ceils). It is deliberately not clamped inside
`cut()`, because the study oracle pins that function byte-identical to the measured arm, whose
harness always passed bin-aligned session bounds — a clamp would mean the shipped cutter is no
longer the arm that was measured.

**`/analyze` reports the block BESIDE the window, never instead of it.** An additive `block`
key (opt-in) carrying the span and the two boundary reasons, and nothing else — no evidence or
attributability field, because the two definitions of thinness in this codebase disagree:
per-level attribution reads 95.3% against 99.3% for holding any pooled evidence, and a block
holding one unit at each of eight allocation levels clears a pooled floor while every level in
it reads `thin`. Whoever adds the first consumer picks the per-level measure deliberately
rather than reaching for the shorter pooled sum. Sidecar `SCHEMA` 14 → 15; every other field is
still computed over the hour, and narrowing the window to the block is a later phase with its
own eval re-run. **Phases 2-5 of
`docs/superpowers/specs/2026-08-25-signal-block-pipeline-design.md` are NOT built** — `covers`
(the prompt-id → block-span episode mapping, `complete: false` where an episode runs past the
block), the Go wire (the deterministic facets move onto `publish.WindowEnrichment` and
`publish.Enrichment` shrinks to `sensitivity` plus correlation), the tick becoming the primary
trigger rather than a gap-filler, and the `ml_backend:"deterministic"` default flip. The last
two are gated on Atlas, for the same reason the tick ships inert (below): flipping the default
before Atlas renders block facets **blanks its Context column**, since `function_guess`,
`subcategory` and `activity_type` are all in the deterministic skipped set.


**Tick-driven window characterisation (`daemon/tick.go`, sidecar
`analysis/coverage.py` + `analysis/tick.py`, `POST /tick`) — OFF by default
(`KELD_TICK`).** Enrichment fires **per prompt** and every window looks **back**
60 minutes, so the work a prompt *causes* falls outside that prompt's own
window; when the next prompt is more than an hour later, nothing characterises
it. Measured (`scripts/tick_coverage.py`, frozen corpus): **56.4%** of john's
reference events and **55.0%** of the 496-transcript Claude Code corpus's turns
lie inside some prompt's look-back — a third to a half of all work is invisible
to enrichment, and it is worse the more autonomous the agent. A tick
characterises the gaps: **99.7% / 99.5%** after.

- **The frontier is the whole no-double-publish guarantee.** Never emit above
  `min(watermark, now - span)`. Time `t` below that can only be covered by a
  prompt in `(t, t+span]`, all of which have already arrived, so the covered set
  there is FINAL and an emitted window can never later overlap a prompt's. Not a
  margin — exact, and replayed against randomised incremental prompt streams.
  The price is latency only: a window facet lands up to `span + interval` after
  the work. **Nothing safety-relevant waits for a tick** — `sensitivity` and
  every text facet keep their per-prompt trigger, and this path never reads
  prompt text at all.
- **A timer, not the ingest signal.** A gap becomes emittable a whole span after
  the work, by which time the machine is usually quiet and no ingest signal is
  coming. Measured, the share of recovered work emitted only after the
  transcript's last turn: **5.8%** (john) / **79.5%** (Claude Code corpus) — an
  ingest-driven tick would drop it, in exactly the burst-then-silence shape the
  tick exists for. **Idle emits nothing** structurally instead: a silent
  interval's windows hold no evidence and are dropped sidecar-side.
- **The interval is latency, not coverage.** 99.5% at 5/10/20/60 minutes alike.
  Default 10m (`KELD_TICK_INTERVAL`).
- **The covered set comes from the DAEMON, not the store.** The store's `prompt`
  index holds every user- *and* assistant-shaped turn (~260 rows for john's 14
  human prompts); planning against it swallows the session and emits nothing.
  The daemon names the prompt ids (it owns `watch/filter.go`'s human-prompt
  filter) and the store times them; state in `~/.keld/state/tick.json`
  (per-transcript monotonic cursor + bounded prompt memory, forward-only on
  first sight).
- **Watermark and retention are honoured through `analyze_window`'s own
  `StoreBehind`/`WindowExpired`, never re-derived.** Behind ⇒ the cursor stops
  and the tick retries. Expired ⇒ the window is dropped, counted, and the cursor
  **advances** — stopping on a permanent refusal would wedge a daemon that had
  been down longer than the retention horizon.
- ⚠️ **The client half ships INERT, and that is why it is off by default.** A
  tick row publishes under `corr_scheme:"window"` with a deterministic
  `<session>@<window_end>` id, in its own wire type (`publish.WindowEnrichment`)
  carrying no text facets at all. It could **not** ride a prompt's correlation:
  Atlas keys enrichments `UNIQUE(org_id, source_id, corr_scheme, corr_id)` and
  inserts `ON CONFLICT DO UPDATE` over every column, so the design spec's
  recommended option (a) would **overwrite** the anchor prompt's enrichment
  rather than dedup against it. Under its own scheme it cannot collide — but
  every Atlas consumer joins `Enrichment.corr_id == ToolEvent.prompt_id`, so a
  window row is accepted and stored (including in `enrichments.raw`) and
  **joins to nothing** until Atlas learns a time+identity join. Switching
  `KELD_TICK` on logs that and emits a `window.tick_enabled` client-event
  saying so. Flipping the default is a one-line change the day Atlas catches up.

