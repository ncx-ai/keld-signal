# Pre-registration — mapping blocks to `atv1` activity types

**Committed BEFORE any label is written and before any arm is scored.** That ordering
is provable from git and is the only thing that makes these numbers falsifiable. It
follows the protocol of `~/keld/refseries-context/facets/ACTIVITY-RERUN-PREREGISTRATION.md`,
whose result this work is downstream of.

**Date:** 2026-09-24 · **Vocabulary:** `keld-activity-types-v1.csv` (`atv1`), 68 ids,
17 verbs x 11 contexts, 7 families. **The vocabulary is FIXED.** No id is added,
renamed or removed by this work. An earlier draft of this design proposed seven new
`code.*` verbs; that is withdrawn.

## What is already closed, and is not re-opened here

`sidecar/app/analysis/activity.py` measured `action`-level -> 6-value `Activities`
FOUR times and refuted it each time (best: coverage 0.827, accuracy 0.323 against a
0.492 constant, **lift -0.169**). Conceding the one arguable labelling call still
leaves +0.013 over the constant. Its rule 6 closed the question.

**This is not a fifth attempt at that question.** The 6-value vocabulary divides on
INTENT and `action` records physical ACTS; that mismatch is structural. What is asked
here is narrower and was never tested, because the old vocabulary had no code verbs:

> Does the act->intent refutation transfer to `atv1`'s `code.write` / `code.edit`,
> which are themselves closer to physical acts?

If it transfers, the deterministic path is dead for every verb and GLiNER2 on prose
carries both axes. If `code.*` escapes, code blocks get a free correct verb.

Also already measured and NOT re-opened: **proportions over act VOLUME are refuted**
(Amendment 1 — rolling up tool events and taking the dominant class produced
`researching 827 / editing 20 / conversing 20` over 1,014 windows, a 95.4% majority
class, because an hour of authoring issues ~50 Reads and ~5 Edits). Any proportion
this work publishes must use a denominator that is not act volume, and must be shown
not to reproduce that distribution.

## Frame

`~/keld/refseries-context/facets/activity-rerun-sample.ndjson` — the SAME 150 windows
attempt four used, with `prompts`, `prose`, `actions`, `levels`, `tools`, `volume`.
Reused deliberately: a fresh sample would make these results incomparable with the
refutation they are testing.

⚠️ **This corpus is 100% software engineering.** Every `context` label will be
`none` or `general`. **No claim about the context axis may be made from it.** Context
is Study 4's job, on external corpora, and its limits are stated there.

## Labelling protocol (blind, rubric first)

Labels are assigned from **window TEXT ONLY** — user prompts + assistant prose, fenced
code elided, every `tool_use` / `tool_result` / `thinking` block dropped. No tool name,
program, action, count or volume reaches the labeller. Blindness is enforced by the
view generator, not promised.

Each window gets a primary `activity_type_id` from the 68, and may carry a secondary
where the window genuinely contains two. `other.general` is the abstention.

Labels are committed before any arm is run.

## Arms

| arm | verb from | context from |
|---|---|---|
| **D** deterministic | `Acts` -> verb mapping | n/a |
| **P** prose, flat | GLiNER2, 17-way | GLiNER2, 11-way + NONE |
| **H** prose, hierarchical | GLiNER2: 7 families -> verb in family | as P, only if verb takes one |
| **F** flat 68 | GLiNER2, one 68-way call | (same call) |

## Bars, fixed now

1. **Any arm must beat the majority constant.** Publishing a facet that scores below a
   constant is strictly worse than publishing nothing — this project's standing rule.
2. **Study 3 (does the refutation transfer):** arm D's accuracy **restricted to windows
   whose gold verb is `code.write` or `code.edit`** must reach **>= 0.70** for the
   deterministic path to survive for code verbs. Below that, D is dead everywhere.
3. **Study 1 (pass structure):** H must beat P by **>= 5 F1** to justify 2-3x the
   calls. P must beat F at all to justify factoring. Otherwise take the cheaper arm.
4. **Study 2 (wording):** three description styles (bare id / one-line gloss / rich
   definition with positive and negative cues) on ONE arm. Report the spread. **If the
   spread exceeds the largest gap between arms in Study 1, Study 1's conclusion is void**
   until wording is fixed, and is re-run.
5. **Study 4 (context):** per-context P/R on external corpora, WITH a shuffled-label
   control. **If performance does not collapse on shuffled labels the number is void**,
   however good it looks — it is measuring register, not domain.
6. **Proportions:** the published window-proportion distribution must not reproduce the
   Amendment-1 signature (one class >= 90% of windows across the corpus). If it does,
   the denominator is wrong and no proportion publishes.

## Rule 7 — stopping

If every arm fails bar 1, the answer is that `atv1` cannot be attributed to blocks by
these means, and that is the finding. It is recorded, not retried with a sixth arm.

## Stated limits, before any number exists

- The corpus is one developer's engineering work. Verb results do not generalise to
  non-engineering populations and the context axis is untestable here.
- Gold labels come from an agent reading blind views, not a domain expert.
- Study 4's external corpora are not transcripts. They measure whether the
  discrimination exists, never whether it survives work conversation. Where a context
  appears in both dialogue and document form, the drop between them is reported as a
  correction factor for the document-only contexts.
