# task_type routing-aligned taxonomy redesign

**Date:** 2026-07-18
**Status:** Approved (brainstorm) — ready for implementation planning
**Scope:** Go (enrich) + eval data + schema. Redesign the `task_type` vocabulary so
it labels prompts by **empirical real-world categories of async AI inference jobs**,
the routing key for Keld Inference Exchange order books (see memory
`tasktype-routing-purpose`). Text prompts only. Grounded by an adversarially-verified
research pass (`docs/superpowers/plans/2026-07-18-tasktype-taxonomy-research.md`).

## Motivation

`task_type`'s primary purpose is **routing** a prompt to an inference order book
(per-task-type, e.g. a "summarization" book). So the taxonomy is a **design
variable** jointly optimizing (a) routability to real order books and (b)
classifiability by the on-device GLiNER2 bi-encoder — not accuracy against a frozen
vocab.

The current vocab (ported from inference-enrichment) has two members that are weak
on *both* axes, confirmed by the task_type diagnostic (gold acc 0.696):
- **`agentic_tool_use`** causes ~half the errors (10/21 misses, high-confidence). It
  is a *workflow shape*, not an inference workload — "deploy the branch", "book a
  meeting and email the agenda", "charge this card" decompose into
  code_generation/text_generation/… inference. It is neither classifiable as one job
  nor a real order book.
- **`other`** is a classifier magnet (absorbs 7+ misses) AND a routing dead-end
  (no "other" order book).

Research validated our redesign: 6 categories are canonical across HF/Super-Natural-
Instructions/BIG-bench/HELM; two-axis separation (workflow/modality off task_type)
matches HELM + providers; a GLiNER bi-encoder handles 8–10 labels cheaply
(separability, not count, is the constraint); and the biggest missing high-volume
job is **rewriting/editing** (~2/3 of OpenAI's "Writing" traffic modifies user text).

## The taxonomy (v1 — text jobs)

Nine task categories + a `general` fallback (10 vocab entries):

| id | the inference job / order book |
|---|---|
| `summarization` | condense existing content into a shorter form |
| `translation` | convert text between languages |
| `code_generation` | write, modify, debug, or configure software |
| `information_extraction` | pull structured fields/entities/data from text |
| `classification` | label, categorize, or score inputs |
| `reasoning` | multi-step analysis, math, planning, decisions |
| `question_answering` | answer questions from documents/knowledge |
| `text_generation` | draft new prose/creative/marketing/email from scratch |
| `rewriting` | edit, rewrite, paraphrase, or improve user-supplied text |
| `general` | fallback: genuinely general/mixed/unclear prompts |

**Naming** follows HF/academic conventions (the de-facto zero-shot label strings a
bi-encoder matches best): `code_generation`, `information_extraction`,
`question_answering`, `text_generation`.

### Changes from the current vocab
- **DROP `agentic_tool_use`** — agentic-ness lives on the workflow axis (`speech_act`
  already carries the command/action signal); route agentic prompts by their
  underlying job.
- **ADD `text_generation`** (was hidden inside `other`) and **`rewriting`** (the
  highest-volume missing job; was forced into `summarization`/`other`).
- **RENAME** `codegen`→`code_generation`, `extraction`→`information_extraction`,
  `rag_qa`→`question_answering`.
- **REPLACE `other`→`general`** (a named fallback, not a vague magnet).
- **KEEP** `summarization`, `translation`, `classification`, `reasoning`.

### `general` semantics
`general` is a real vocab member the classifier may pick for prompts that fit no
specific job. Separately, keld emits `task_type` **confidence**; downstream routing
MAY additionally send low-confidence predictions to a general order book — but that
routing logic is NOT built here (keld's job is to classify into the vocab + emit
confidence). Note the calibration finding: task_type errors are *high-confidence*, so
`general`'s value is giving general prompts a home, not an abstain mechanism.

## Design

- **Vocab** (`labels.go`): replace `TaskTypes` + `TaskTypeDefs` with the 10-entry set
  above. `TaskTypeDefs` label **text** (what GLiNER scores against) is
  **bakeoff-selected** per the A6 method — short, positive, discriminative, HF-aligned
  descriptions. The load-bearing wordings target the research's predicted conflation
  pairs (below).
- **SchemaVersion 5 → 6** — vocab change is a contract-affecting event; signals the new
  task_type taxonomy to Atlas.
- **A6 path unchanged** — task_type still classifies via `classifyPass`(TaskTypeDefs)
  over the context preamble; only the vocab/descriptions change. `code_generation`
  keeps A6's winning "software engineering" framing.
- **Gold relabel (eval data)** — remap every `task_type` gold value in `gold.jsonl` +
  `confound.jsonl` to the new vocab, and add rows covering `text_generation`,
  `rewriting`, and `general` (currently absent/thin). Mapping rules:
  - `codegen`→`code_generation`; `extraction`→`information_extraction`;
    `rag_qa`→`question_answering` (mechanical).
  - `agentic_tool_use` (11 rows) → relabel each to its underlying job
    (deploy/migrate→`code_generation`; draft-email/book-and-email→`text_generation`;
    push-with-token/configure→`code_generation` or `general`; per-row judgment).
  - `other` (15 rows) → split into `text_generation` / `rewriting` / `general`.
  - Review `summarization` rows: any that *modify user-supplied text* → `rewriting`.
  - **REVIEW GATE:** the relabel + new rows are the crux (a mislabel makes the bakeoff
    optimize against wrong targets); the drafted mapping is reviewed before it's locked.
- **Conflation pairs the bakeoff must watch** (from research): reasoning vs
  classification/question_answering; information_extraction vs classification;
  information_extraction vs summarization; text_generation vs summarization/translation.
  `reasoning` is the weakest member — expect it to need the most wording care.

## Measurement (measure-first, same loop as A6)

- **Bakeoff** candidate `TaskTypeDefs` wordings against the relabeled gold set on the
  live sidecar; score per-category accuracy + the 4 conflation pairs; pick the set that
  maximizes accuracy without collapsing any category. (This is the *classifiability*
  test of the taxonomy.)
- **Gate:** keep only if task_type gold accuracy ↑ vs the 0.696 baseline (translated to
  the new labels), leakage stays ≤ baseline, and **every other facet flat**
  (`function_guess`/`speech_act`/etc. unchanged; task_type is Wave1-independent).
- Re-check calibration (`--calibration`) on the new vocab — did dropping
  `agentic_tool_use`/`other` reduce the high-confidence error mass?

## Non-goals

- **Modality axis** (image/video/audio/transcription jobs) — a *separate future axis*
  (output_modality), designed when the workload carries multimedia prompts and a gold
  set exists. `general` catches stray multimedia prompts safely meanwhile.
- **`data_analysis`** as a distinct category — secondary research candidate; hold for v2
  (fold into `reasoning`/`classification` for now).
- **The order-book mapping + admin filtering** (quality-score ranges, targeting) —
  downstream exchange concerns, not keld enrichment.
- **Abstain/low-confidence routing mechanism** — noted, not built (Lever F; and
  calibration showed it can't fix task_type accuracy anyway).

## Success criteria

- New 10-entry `task_type` vocab live, schema v6, bakeoff-selected descriptions.
- task_type gold accuracy ↑ vs 0.696; the `agentic_tool_use`/`other` error mass gone.
- No regression on any other facet; leakage flat-or-down.
- Gold set relabeled + reviewed; `text_generation`/`rewriting`/`general` covered.
