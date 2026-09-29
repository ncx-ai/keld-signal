# Activity types: what the evidence supports building

**Date:** 2026-09-29. **Status:** agreed with the repo owner, nothing built yet.
**Evidence:** `docs/notes/2026-09-24-activity-atv1-results.md` — read its header first; this
file is the product reading of those measurements, not a restatement of them.

This exists so the line between *measured* and *assumed* survives contact with a roadmap.
Every tier below names what it rests on, and Tier 3 names what would have to be measured
before it could move up.

## The feasibility fact that makes this near-term

`sidecar/app/analysis/levels.py` **already holds `call.name` and `call.input`** for every
tool call and already reads `inp.get("command")` / `file_path` / `skill`; `requestId` is
already tracked in the parse state as the `reqs` accumulator. The classifier is therefore a
pure function of data the sidecar ingests **today** — no new plumbing, no new field, and
nothing new crossing the privacy boundary (a class is a label; no text, span or offset).

## Tier 1 — the evidence supports building this now

**1. Activity class per inference request.** Deterministic, no model, so it runs under
`ml_backend:"deterministic"` — which is what `keld-agent install` writes by default, so it
reaches the v2 machines that will never load GLiNER2.
Evidence: κ 0.885 inter-labeller; 0.917 where two labellers agree; macro F1 0.920 against the
author's labels and 0.815 against an independent labeller's; residual 1.7% / 1.6% on two
different people's corpora under identical rules; tool-free negative control passes.

**2. A per-block activity DISTRIBUTION, never a single label.** ⚠️ **It needs TWO
denominators, because they disagree by ~3x:**

    author_prose    9.4% of calls  but  25.8% of output tokens
    retrieve       18.9% of calls  but   5.5% of output tokens

A surface showing call-share alone misrepresents where the work is. Show tokens by default.

**3. Cost by activity class — the strongest immediate product value.** `retrieve` is
**32.4% (corpus A) / 42.0% (corpus B)** of modelled cost. Computed from `usage` already in
the transcript, no model. It is the one output that changes a purchasing conversation rather
than merely describing activity.

## Tier 2 — buildable, with the caveat stated IN THE UI

**4. Abstention is rendered, never folded.** `unclassified` (~1.6–2.8%) shows as itself. This
project has now been bitten twice by a default class silently absorbing a coverage gap —
`atv1`'s `other` at 38.8%, then `operate`'s fallthrough at 57.5% — and both times the number
looked healthy until someone measured the composition.

**5. Routing RECOMMENDATIONS, explicitly not routing.** "31% of your requests are
`retrieve`-class and are candidates for a cheaper model" is supported. "This will save you
X" is not.

## Tier 3 — NOT yet, and worth resisting pressure on

**6. Automatic model substitution.** We have proven we can CLASSIFY a request. We have not
measured whether a cheaper model SUCCEEDS on one. ⚠️ The cost asymmetry is why this matters:
a failed substitution costs a whole additional request at a 113k–200k token input, while
over-provisioning costs only a price delta on one. Shipping routing on classification
evidence alone would be inferring the conclusion from the premise.

**7. Domain / context** ("is this marketing or engineering work"). Refuted on every available
route: synthesis failed its generator-agreement control, and public chat data held ~1 genuine
professional-work conversation in 59,857 candidates.

**8. Which code model** (the complexity axis). Scoped 2026-09-28 as separate, finer-grain work
layered on the class — deliberately not folded into the class boundary.

## The one measurement that unlocks Tier 3

**Replay.** Take N real `retrieve` requests, reconstruct the conversation prefix from the
transcript, run them against a candidate cheaper model, and compare the tool call it produces
against the one that actually happened. That yields a substitution success rate, which is the
number that converts a recommendation into a routing rule.

It is tractable: the prefix is in the transcript, the expected output is observable, and a
request that FAILED and was retried is visible — a natural gold signal that needs no labels.

## ⚠️ One schema decision to take BEFORE the first consumer exists

`activity_class` belongs in **INVENTORY** (a distribution of values with counts), **not
ALLOCATION** (one winner at share ≥ 0.50 with ≥ `MIN_EVIDENCE` observations).

ALLOCATION's floor exists to answer "which project was this?" — a question with one right
answer, where a wrong attribution is a false statement. "What kind of work was this?" has
several right answers at once. Forcing it through the winner-take-all floor would publish
`no_majority` on most blocks while discarding the distribution that IS the product.

Cheaper to settle now than after a consumer depends on the shape.

## Open before any of this

- A **third** person's corpus. Corpus B's 1.6% residual is fitted to corpus B's residual
  exactly as corpus A's 2.8% was to corpus A's.
- **Session- and subagent-level characterisation**, which is the next investigation and may
  change what the right unit is: 41.5% (corpus A) and **70.6%** (corpus B) of all inference
  requests are subagent sidechain, and nothing measured so far treats a subagent run as a
  unit of its own.
