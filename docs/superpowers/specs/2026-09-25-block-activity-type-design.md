# Block activity types — Design

**Status:** Draft, awaiting sign-off · not implemented
**Author:** Claude Opus 5 (1M context)
**Date:** 2026-09-25
**Measurements:** `docs/notes/2026-09-24-activity-atv1-results.md`
**Pre-registration:** `docs/superpowers/specs/2026-09-24-activity-atv1-preregistration.md` (`fde3297`)

Every number in this document was measured before it was written, against 100 blind hand
labels committed before any arm ran (`ea4c6a5`). No figure here is an estimate.

## 1. What this adds, in one paragraph

A closed block gets a **ranked list of `atv1` activity types**, so Atlas can say what a piece
of work WAS — `code.edit`, `review`, `research`, `plan` — rather than only what it touched.
The deterministic pass already answers "which files, which acts, which branch". It cannot
answer "what kind of work was this", and four prior attempts to make it
(`sidecar/app/analysis/activity.py`) were refuted. This pass answers it with GLiNER2 over
prose, measured at **accuracy 0.660 against a 0.370 constant (lift +0.290, coverage 1.00)** —
inside the band of facets Keld already publishes (`activity_type` 0.670, `domain` 0.683).

**The `atv1` vocabulary is FIXED**: 68 ids, 17 verbs x 11 contexts, from
`keld-activity-types-v1.csv`. This design adds no id and renames none.

## 2. The two lanes, and why they do not mix

```
BLOCK CLOSES
   |
   +-- DETERMINISTIC PASS (exists, unchanged)
   |     acts | file types | languages | workstream dims | tempo | tokens
   |     -> publishes as today, 0 inference
   |
   +-- ACTIVITY PASS (new) -- GLiNER2, ONE model, ONE call per prose window
         verb: 17 labels + rich descriptions
         -> snap to a legal atv1 id -> rank by window share
```

⚠️ **The deterministic lane does NOT feed the model, and that is measured, not stylistic.**
Stating raw act counts as a prompt hint LOWERED accuracy 0.660 -> 0.640 and `code.*`
0.600 -> 0.511, moving 8 windows into `research` that prose alone got right. That is
Amendment 1's read-swamping (an hour of authoring issues ~50 Reads and ~5 Edits) re-entering
through the PROMPT instead of the rollup. It also matches AGENTS.md's standing rule that
augmentation is facet-selective.
⚠️ **This refutes RAW act counts only.** Discriminative acts (`test`/`commit`/`build`), ratios,
or file-type evidence are DIFFERENT experiments and are not refuted. Do not read this as
"deterministic evidence cannot help the model".

## 3. Verb from prose. Never from acts.

| route | accuracy | lift | `code.*` |
|---|---|---|---|
| deterministic acts -> verb | 0.452 | +0.036 | 0.628 |
| **prose -> 17 verbs (factored)** | **0.660** | **+0.290** | 0.600 |
| prose -> 7 families -> verb | 0.350 | **-0.020** | 0.556 |
| prose -> all 68 ids at once | 0.430 | +0.060 | 0.822 (artifact) |

- **Factored beats flat-68 by 0.230**, which exceeds the 0.160 wording spread, so the claim is
  safe. At `gloss` wording the gap was exactly the spread and was withheld.
- **The hierarchical cascade is refuted** — below the constant. A wrong family pick is
  UNRECOVERABLE because the verb pass can only choose inside it. ⚠️ Do not re-propose it on
  the strength of the CSV's `family` column or the pipeline's Wave-1 -> Wave-2 precedent.
- **The deterministic route is refuted for the FIFTH time.** `activity.py` refuted it four
  times on the 6-value vocabulary; this is the first test on a vocabulary with explicit code
  verbs, and `code.*` reaches 0.628 against a pre-registered 0.70 bar. Its dominant error is
  the prior study's exact one, `review` -> `research` x16.
- ⚠️ **`F`'s 0.822 on `code.*` is an ARTIFACT** of calling nearly everything `code.edit`.
  Never read the `code.*` column without the accuracy column beside it.

## 4. Label descriptions are SCHEMA, not a detail

| wording | accuracy | `code.*` |
|---|---|---|
| bare id | 0.500 | 0.222 |
| one-line gloss | 0.640 | 0.467 |
| rich, with positive + negative cues | **0.660** | **0.600** |

**Spread 0.160 overall, 0.378 on `code.*` — larger than every arm gap in the study.**
Therefore: descriptions are versioned with the vocabulary, a change to one is
contract-affecting and takes a schema bump, and the eval is re-run on any edit. This is the
same rule AGENTS.md already states for classifier labels ("the label wording is load-bearing").

⚠️ **Richer is not uniformly better — it trades errors.** The `rich` set introduced
`research` -> `extract` x6, caused by the cue "pulling structured facts out of content" against
a corpus full of "investigate and report back concrete facts". Tune with the eval, never by eye.

## 5. What publishes

Additive to the block digest. **Ranked, never a single label** — the repo already measured
single-label attribution of activity to an hour at coverage 0.185 against a 0.70 bar, because
work is PLURAL (p50 7 distinct acts per window).

**Schema version:** this adds a published vocabulary (the 68 `atv1` ids, plus the closed
`context_status` set), which is contract-affecting under AGENTS.md's rule. **Bump
`enrich.SchemaVersion` 23 -> 24 and re-run the eval**; producer strings move `-v23` -> `-v24`.
A consumer must also learn `atv1` itself, which is why `vocabulary_version` is on the wire
rather than implied.

```
activity: {
  ranked: [ {activity_type_id, share, confidence}, ... ],
  windows: <int>,                  # the denominator, stated
  context_status: "not_evaluated", # stage 1 -- see section 6
  vocabulary_version: "atv1"
}
```

**Stage 1 publishes LEGAL atv1 ids with the context pinned.** Context-free verbs publish bare
(`code.write`, `code.edit`, `plan`, `embed`); context-taking verbs publish `.general`
(`research.general`, `review.general`). Every value is one of the 68.

⚠️ **`context_status` is REQUIRED and is the whole honesty of stage 1.** A consumer must be
able to tell "`general` because we determined it is general" from "`general` because the
context axis was never evaluated". Without it this facet publishes a confident negative from a
check that did not run — the failure this codebase refuses everywhere else.

⚠️ **The proportion denominator is PROSE WINDOWS, and it is UNTESTED.** Act volume is
refuted: Amendment 1 measured `researching 827 / editing 20 / conversing 20` over 1,014
windows, a 95.4% majority class, because reads swamp the rollup. **Before `share` publishes,
the window-share distribution must be shown not to reproduce that signature** (no class >= 90%
across the corpus). If it does, the denominator is wrong and `ranked` publishes without
`share`.

## 6. The context axis is DEFERRED, and the gate is written down

**It is not built in stage 1.** The corpus that validated the verb axis is 100% software
engineering: all 100 gold labels carry context `general` or `none`, so 10 of 11 contexts have
zero support and no number about them is obtainable from it.

The one figure that exists is a warning: the flat-68 arm assigned **`review.legal` to 11 of
100 windows** on a corpus with zero legal work, and the rate ROSE to 11 from 5 as descriptions
got richer. **The context axis over-fires when offered.**

**Gate for stage 2**, all three required:
1. Per-context precision/recall on external domain-labelled corpora.
2. ⚠️ **A shuffled-label control.** A legal corpus is legal in REGISTER as well as topic, so a
   classifier can score well by keying on style. **If performance does not collapse on
   shuffled labels the number is void**, however good it looks.
3. A measured false-domain rate on engineering-only input, against the 11/100 baseline above.

**First thing to test in stage 2:** the Atlas **team prior**. Stating "this person is on the
Engineering team" cut false-domain attribution **11 -> 4 (-64%)** — the single most effective
intervention measured. ⚠️ It TRADES an error (`review`->`code.edit` 16 -> 22) and was measured
only in its false-positive half; whether it helps FIND true domains needs multi-team data.

## 7. Where it runs

**Block scope, on the block emitter's `OnPublished` hook** — the `/attribute` precedent, not
the per-prompt enrichment pipeline. It inherits the durable per-block job, the version-skew
HOLD (`RouteUnsupported` on 404), and the quarantine path.

**A new `ml_backend` mode: `"activity"`.** `deterministic`-only was provisional until a model
could be reintroduced; this is that reintroduction. `"activity"` runs the full deterministic
pass PLUS this block facet, and **not** the 5-inference-per-prompt enrichment pipeline.
`auto`, `deterministic` and `off` keep their current meanings exactly, so no existing machine
changes behaviour.

`keld-agent install` writes `ml_backend:"activity"` via `settings.WriteInstallDefaults`, which
MERGES, so an operator's other keys survive. ⚠️ **`ml_backend` is startup-only and has NO
REMOTE OVERRIDE** — the installer is the only lever that will ever exist, so a re-install flips
an existing machine with no server-side brake. `--backend` remains the manual path back.

⚠️ **The mode set is now FOUR and a reader must be able to tell them apart:**

| mode | deterministic passes | block activity facet | per-prompt ML pipeline |
|---|---|---|---|
| `off` | no enrichment at all | no | no |
| `deterministic` | yes | no | no |
| **`activity`** (new install default) | **yes** | **yes** | **no** |
| `auto` | yes | yes | yes |

**Cost, measured on 258 real blocks:** p50 **17 prose windows -> ~4.5 s**, p90 57 -> 15 s,
max 140 -> 37 s, at GLiNER2's 264 ms/call. Against a >=20-minute block cadence that is a
~0.4% duty cycle, off the request path. **One model, ~1.5 GB, sequential with the existing
worker — no second resident model.**

⚠️ **One 1.9 GB download arrives on machines that previously downloaded nothing.** That is the
deliberate consequence of reintroducing a model and must be stated in the installer, not
discovered.

## 8. What this design does NOT do

- **No new activity types.** An earlier draft proposed seven `code.*` verbs; withdrawn.
- **No contexts in stage 1** (section 6).
- **No deterministic verb mapping** (section 3) — and `activity.py` stays retained-not-deleted
  so the next person reads the measurement instead of rebuilding the mapping.
- **No act-count augmentation** (section 2).
- **No hierarchical pass** (section 3).
- **No GLiNER2.5.** Measured 2026-09-25: `GLiNER2.5-Decide` 0.520 and `gliner2.5-base-v1`
  0.340 against the current model's 0.660, under the same library so the version is not a
  confound. ⚠️ `base`/`small` are a different architecture (`boundary`, not `span`) and load
  only via `AutoExtractor`; forcing `max_width` into their config builds the WRONG model class.

## 9. Honest limits, stated before implementation

- **One developer's engineering corpus.** 7 of 17 verbs have any support; 10 of 11 contexts
  have none. Nothing here generalises to a non-engineering population without re-measurement.
- **`code.*` never cleared its 0.70 bar** (0.600 best genuine). On an engineering fleet that is
  the MAJORITY of blocks, so the 0.660 headline is carried by the verbs such a fleet exercises
  LEAST. ⚠️ There is a live temptation to tune descriptions against this 100-window sample
  until they fit it; the eval must move to held-out data before any further wording work.
- **Gold labels are an agent's, blind, not a domain expert's.**
- **Frame is 100 windows** (Amendment 1, recorded before labelling).
