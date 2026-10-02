# Document-type recognition — study design

**Date:** 2026-10-01
**Branch:** `feat/activity-class-and-subagent-marker`
**Status:** design approved conversationally. **Phase 1 is a study and touches no production
code.** Phase 2 is gated on Phase 1's numbers and is not authorised by this document.
**Revert:** `rm scripts/doctype_*`. No schema bump, no golden, no Atlas coordination.

## 1. What this answers, and why it replaces the domain question

The standing goal is to tell what KIND of work a session is doing — "this is marketing
related" — not just that someone is summarising. Four routes were measured and rejected on
2026-09-30 and 2026-10-01:

| route | result |
|---|---|
| GLiNER2 on prompts | 44.4% synthetic, **12.7% real**; per-domain profile is an artifact of label wording |
| GLiNER2 on paths | **70.7% real**, but one domain only and needs `ml_backend:"auto"` |
| keyword lists | 52% on text I wrote, **17% on real paths** — memorised the author's tells |
| agent self-report via hook | `SubagentStop` blocks **do not reach the model**; 4 firings, 0 actions |

**The reframe that makes this tractable: recognise the DOCUMENT, not the domain.** "Is this
marketing work" is a judgement about purpose — two people disagree, and every label derivable
from our corpora is circular with a classifier that reads those corpora. "Is this a letter of
intent" is a **fact about an artifact**. You can look and tell.

Three consequences follow, and they are the whole argument:

1. **Ground truth becomes obtainable.** Inter-rater agreement on document type is high where
   it is poor on domain, so hand labels are close to fact rather than opinion.
2. **The signatures are externally authored.** An ADR's `## Context` / `## Decision` /
   `## Consequences` is the ADR convention; `MUST`/`SHOULD` is RFC 2119; `### Added` /
   `### Fixed` is Keep a Changelog. ⚠️ This is the one thing that distinguishes it from the
   keyword lists that scored **17% on real paths** — those I invented, so they encoded my own
   tells and transferred to nothing.
3. **Domain becomes a lookup, not an inference.** `letter_of_intent → legal`,
   `soap_note → medical`, `10k → financial`. The inference step is replaced by a declarative
   map — the same shape as the vendor table, which is the one approach that has worked.

## 2. Scope — what Phase 1 does and does not do

**Phase 1 (this spec): a study under `scripts/`.** It reads transcripts, extracts authored
documents, runs a recogniser, and scores it against hand labels committed beforehand. It
produces a number and a recommendation.

**Phase 2 (NOT authorised here):** a `sidecar/app/analysis/doctype.py`, a published level, a
schema bump, the content-invariant widening in §4. None of it happens unless Phase 1's numbers
justify it.

**Not in scope at all:** the business-document vocabulary (`letter_of_intent`, `10k`,
`soap_note`). Those are Phase 2's declarative extension and **zero of them appear in either
corpus**, so nothing about them is measurable here. Phase 1 proves the MECHANISM on types we
have; the vocabulary extension rides on the mechanism working.

## 3. Evidence: authored documents only

⚠️ **THE READER CANNOT SEE WHAT THE AGENT READ, ONLY WHAT IT WROTE**, and this is a load-bearing
limit rather than an implementation gap. `capture.py` states that the `tool_result` skip
happens by substring check BEFORE any JSON decoding, and that it "is what keeps a parse
seconds-long rather than minutes-long" on a 90 MB file. Document bodies arriving as `Read`
results are therefore not in the pipeline at all, and recovering them "would undo the one
decision that makes the parse affordable."

What is reachable is `Write` / `Edit` / `NotebookEdit` INPUTS — `content`, `new_string` —
plus filenames and paths.

**So the feature recognises documents the agent AUTHORED or EDITED, never ones it merely
read.** "The agent wrote an LOI" is knowable; "the agent read an LOI" is not. Stated here
because a reader will otherwise assume the broader capability.

## 4. The content invariant, and how Phase 2 would widen it without eroding it

Phase 1 does not touch this. Recorded now because it is the main cost of Phase 2 and should be
visible when the gate is decided.

`test_magnitude.py` asserts today that `magnitude.edit_bytes` is the ONLY function in the
analysis package reading `old_string`/`new_string`/`content`, and it proves the second half
**by reading `levels.py` itself**. `edit_bytes` returns an `int` and has no variant that
returns text.

Phase 2 would widen that to exactly two named functions:

| function | may read | may return |
|---|---|---|
| `magnitude.edit_bytes` | those keys | an `int`, never text |
| `doctype.recognise(name, inp)` | those keys | a label from a CLOSED set, never text |

⚠️ The test changes from "one function" to "these two, named" — so a THIRD one added later
still fails the guard. That is a deliberate widening with a named cost, not an erosion.
`recognise` returns no span, no offset and no excerpt, exactly as `edit_bytes` returns no
string.

## 5. The recogniser

**Signals, not keywords.** Each type declares independent SIGNAL KINDS:

- `filename` — a pattern over the path (`README`, `*/adr/*`, `CHANGELOG`)
- `headings` — required section headings
- `phrases` — canonical phrases the format mandates
- `shape` — structural facts (a date-ordered list, a numbered decision record)

**The score is the count of DISTINCT KINDS matched, not total matches.** Repeating one phrase
five times is one signal. A document scoring on filename *and* headings scores 2.

⚠️ **TWO KINDS MINIMUM OR IT DOES NOT COUNT.** One signal is a coincidence: a file named
`plan.md` is not a plan, and a document containing the word "Decision" is not an ADR. This is
the `MIN_EVIDENCE` discipline the window rollup already runs on, applied to signal DIVERSITY
rather than observation count.

**Output is a distribution**, normalised scores across types, with the evidence count carried
beside it — never a bare winner. A document that is half design-spec and half runbook splits
its evidence, which is the honest answer rather than a forced choice.

**Phase 1 vocabulary — eight types the corpora actually contain:**

`readme` · `design_spec` · `adr` · `runbook` · `postmortem` · `meeting_notes` ·
`changelog` · `plan`

## 6. Validation, and the trap it exists to avoid

⚠️ **THE AUTHOR OF THE SIGNATURES MUST NOT BE FREE TO WRITE THE LABELS AFTERWARDS.** That is
the circular control, and this session has produced it three times: tier A's `kind()` agreed
97.2% with the rule it was checking; the keyword baseline was scored on text containing its own
keywords; the agent self-report test was run on an agent that knew about the test. Each time it
flattered the thing being measured.

Two mechanisms, and the first is the one that matters:

1. **ORDER, PROVABLE FROM GIT.** Claude assigns the labels BLIND — shown filename plus the
   first ~40 lines of each document, nothing else, and in particular not the signature table,
   which does not exist yet — and they are **committed before that table is written**. Same
   discipline as the tier A bar. The labeller and the signature author are the same party,
   which is why mechanism 2 exists and why the ordering is the only thing preventing the
   signatures from being fitted to the answers.
2. **A human spot-check.** The repo owner reviews ~15 of the labels. "Close to fact" is not
   "fact", and if the labels are systematically wrong everything downstream inherits it. This
   is the only external check available and costs about five minutes.

**Pre-registered bar, fixed before the signature table exists:**

- **The floor is the MAJORITY-CLASS baseline, not chance.** If most authored documents are
  `design_spec`, the recogniser must beat "always say design_spec". Chance is the wrong floor
  and would flatter the result.
- **Precision is the headline, not recall** — the same asymmetry tier A has. A wrong type is a
  false claim about someone's document; a missing type publishes nothing, which is the honest
  default.
- **PASS** requires BOTH, and ⚠️ the two are measured on DIFFERENT denominators, which the
  first draft of this spec left ambiguous:
  - **precision ≥ 85%**, over the documents the recogniser ANSWERS on (its abstentions are
    excluded — an abstention is not a wrong answer);
  - **accuracy over ALL labelled documents ≥ majority-class + 20 points**, where an abstention
    COUNTS AS WRONG. This is the comparable number, because the majority-class baseline
    answers on everything and never abstains. Comparing its accuracy against the recogniser's
    precision-on-answered would flatter the recogniser by exactly the abstention rate.
- **Abstention is a reported result, not a failure.** The share of authored documents matching
  fewer than two signal kinds is part of the finding. A recogniser that answers on 30% of
  documents at 95% precision is a different and possibly better outcome than one answering on
  all of them at 70%.

## 7. Files

```
scripts/doctype-labels.txt      hand labels, committed FIRST, blind
scripts/doctype_frame.py        extract authored documents from both corpora
scripts/doctype_study.py        the signature table, the recogniser, the scorer, the bar
docs/notes/2026-10-01-doctype-study-results.md   the numbers
```

Nothing else. Revert is deleting them.

## 8. The gate

- **PASS** → Phase 2 is worth specifying: the production module, the content-invariant
  widening in §4, the published level, and the declarative extension to business document
  types.
- **FAIL** → stop, and record it beside the four routes in §1. A fifth measured negative on
  this question is worth more than a fifth untested idea.
- **Partial** (high precision, low coverage) → a real outcome and probably the most likely one.
  It would argue for publishing a document type only when confident, with abstention as the
  norm — which is what `system_categories` already does with `unrecognized`.

## 9. Open questions

- **Business document types are unmeasurable here** and will stay that way until a corpus with
  non-engineering work exists. The same single corpus unblocks tier B's 0% coverage and the
  path classifier's one-domain limit. Three open questions, one input.
- **The unit is not settled.** Phase 1 scores individual documents. Whether the published
  thing is per-document, per-block or per-session is a Phase 2 decision and depends on what
  the coverage number turns out to be.
