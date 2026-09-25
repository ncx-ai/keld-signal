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
prose, measured at **accuracy 0.700 against a 0.370 constant (lift +0.330, coverage 1.00)** —
above the facets Keld already publishes (`activity_type` 0.670, `domain` 0.683).

⚠️ **That number is a claim about ENGINEERING work and nothing else.** On the only
non-engineering gold that exists (7 windows of product/marketing), the same arm scores
**3/7 primary, 0.548 recall**. The label distribution a headline was measured on is part of
the headline. See §10.

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
| **prose -> 17 verbs (factored, `docs`)** | **0.700** | **+0.330** | 0.644 |
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
| rich, with positive + negative cues | 0.660 | 0.600 |
| **`docs` — text verbs named as documents, decks, pages** | **0.700** | **0.644** |

**Spread 0.160 overall, 0.378 on `code.*` — larger than every arm gap in the study.**

⚠️ **One word cost the whole non-engineering population.** `rich` described `text.transform` as
"reformatting **prose**", which excludes a slide deck. Across 37 clean calls on document work
the model proposed a `text.*` verb **ZERO times**. Widening to "document, deck, page, report,
spec" (`docs`) fixed it AND improved engineering 0.660 -> 0.700 — a strict improvement, not a
trade. ⚠️ The change was made on PRINCIPLE and regression-checked on the other corpus; tuning
descriptions against a 7-window set is fitting noise.
Therefore: descriptions are versioned with the vocabulary, a change to one is
contract-affecting and takes a schema bump, and the eval is re-run on any edit. This is the
same rule AGENTS.md already states for classifier labels ("the label wording is load-bearing").

⚠️ **Richer is not uniformly better — it trades errors.** The `rich` set introduced
`research` -> `extract` x6, caused by the cue "pulling structured facts out of content" against
a corpus full of "investigate and report back concrete facts". Tune with the eval, never by eye.

## 4b. INPUT HYGIENE IS PART OF THE CONTRACT

⚠️ **Measured 2026-09-25: input hygiene moved this facet further than model choice did.** The
same arm scored 0/7 on John's session and then 3/7 after two input fixes — while GLiNER2.5,
a whole model generation, moved it 14 points the wrong way. Hygiene is not tuning here.

**The sub-window handed to the model is bounded at a LOGICAL delimiter — a turn boundary
first, a real sentence end only if one turn overflows.** This is AGENTS.md's standing
convention ("a conversation window handed to a model"), and it was violated three ways in the
first implementation:
1. **Human-readable renderings were fed as model input**, so `[... N chars omitted]` markers
   appeared in **36% of sub-windows**. One began `"172 chars omitted] apply all three point 2:"`.
2. **`(?<=[.!?])\s+` treats list enumerators `1.` `2.` as sentence ends**, shredding a
   numbered procedure into fragments. The splitter must ignore enumerators and initials:
   `(?<![0-9A-Z])[.!?]+\s+(?=[A-Z"'(\[])`.
3. **Role markers were stripped**, running user prompts into assistant prose with no boundary.

**A truncation marker must never reach the model.** If text was cut for a human, that rendering
is not the model's input.

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
- **No team branch on the verb axis.** Measured 2026-09-25: stating the team is inert
  (+0.010, noise) and BRANCHING the label set by team is harmful (-0.030). It is also unsound
  in principle — John, who is not an engineer, wrote 23 `.js` files and a `.py` to build his
  deck, so a team-keyed exclusion makes the correct label UNREACHABLE. **Team informs the
  CONTEXT axis, never the verb.**
- **No free-form extraction as a REPLACEMENT for the label set.** Measured 2026-09-25: it has
  the best recall of anything (0.714 vs 0.548) and cannot rank (top-1 0.000 vs 0.429). It is a
  candidate GENERATOR, not a ranker — see §11.
- **No GLiNER2.5.** Measured 2026-09-25: `GLiNER2.5-Decide` 0.520 and `gliner2.5-base-v1`
  0.340 against the current model's 0.660, under the same library so the version is not a
  confound. ⚠️ `base`/`small` are a different architecture (`boundary`, not `span`) and load
  only via `AutoExtractor`; forcing `max_width` into their config builds the WRONG model class.

## 9. Honest limits, stated before implementation

- **Corpus composition and the `code.*` bar: see §10**, which supersedes what this section
  used to say. (It carried 0.600/0.660; the shipped wording measures 0.644/0.700.)
- **Two corpora, both small.** 100 engineering windows (Amendment 1, recorded before
  labelling) and 7 product/marketing windows. The second is a CASE, not a corpus — no rate
  from it is stable, and the durable observation there was "zero `text.*` proposals across 37
  calls", not the 0/7 itself.
- **Gold labels are an agent's, blind, not a domain expert's.** The engineering set is
  single-label; John's is multi-label with prominence order. **They cannot be pooled until the
  engineering set is re-labelled multi-label**, which is owed work, not an oversight.
- **Nothing here is validated on held-out data.** Every number is in-sample on one of those two
  sets.


## 10. What the headline is a claim about

⚠️ **0.700 is measured on 100 windows of one developer's SOFTWARE ENGINEERING work** — 37%
`code.edit`, 28% `review`, and **zero** `text.*` gold. The only non-engineering gold in
existence (7 windows of product/marketing, `scripts/activity-atv1-john-labels.txt`) scores
**3/7 primary, 0.548 recall** on the same arm.

**A headline accuracy is a claim about the label distribution it was measured on**, and this
design states both numbers rather than the flattering one. The gap was found only because a
second, differently-shaped corpus existed; before it did, the failure was invisible.

⚠️ **`code.*` never cleared its 0.70 bar** (0.644 best genuine). On an engineering fleet that
is the MAJORITY of blocks, so the headline is carried by the verbs such a fleet exercises
LEAST. There is a live temptation to tune descriptions against these two small samples until
they fit; the eval must move to held-out data before further wording work.

## 11. The composition principle, and what it licenses

**Evidence must inform the axis it is actually about.** Measured:

| evidence | its axis | effect |
|---|---|---|
| file extensions | **modality** | recall 0.476 -> 0.548, top-1 unchanged |
| prose, closed labels | **the verb** | **the only thing that RANKS** (0.700 / 0.429) |
| team membership | **context** | false domains 11 -> 4; inert-to-harmful on the verb |
| act counts | operation | refuted 5x |
| free-form + embedding | proposal | recall -> **0.714**, top-1 -> 0.000 |

**PROPOSE BROADLY, RANK NARROWLY.** Deterministic and open-vocabulary sources ADD candidates;
only the closed-label prose pass orders them. A proposer cannot be wrong the way a gate can —
only silent.

⚠️ **`/attribute`'s NULL_DOC discipline DOES NOT TRANSFER to short spans.** 515 of 515
extracted spans beat the null document, so nothing was ever rejected. A two-word noun is closer
to some `atv1` description than to "no particular activity" every time. **A guard that never
fires is not a guard** — anything carrying that pattern to a new matcher must first measure
that it rejects something.

## 12. Two `atv1` and Signal defects found while measuring

⚠️ **`text.create` has a `sales` context; `text.transform` does NOT** (financial, general,
legal, marketing, media, medical, scientific only). A customer deck is therefore labelable
`sales` when created and only `general` when revised — identical work, different label,
decided by the vocabulary rather than the work. This makes context scoring on revision windows
**unresolvable**, and no context number may be quoted from them.

⚠️ **`paths.PATH_INPUTS` is `("file_path","notebook_path","path")` and does NOT cover
`SendUserFile.files`.** On John's session the delivered `keld-acme-routing-scenarios.pptx` —
the entire point of the work — is invisible to every path and extension level Signal computes,
which sees only the `.js` scaffolding. **Non-code deliverables are structurally invisible to
the deterministic layer.** This is a Signal fix, not a study artifact.
