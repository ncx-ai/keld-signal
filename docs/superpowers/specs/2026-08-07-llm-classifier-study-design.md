# Prompted local LLM vs GLiNER2 for conversational classification: study design

Status: **design**, approved 2026-08-07. An **offline, empirical** comparison of a
prompted local generative LLM against the production GLiNER2 backend on the
enrichment pipeline's classification facets, using a multi-turn conversational
window as input.

**This document designs an experiment, not a product change.** Nothing here
modifies the daemon, the sidecar, the wire format, or `SchemaVersion`. No
integration decision is made until the study reports.

## Problem

The classification facets that matter most are the ones performing worst.
Measured on the 87-row gold set with confounds (`docs/superpowers/plans/eval-baseline.txt`):

| Facet | Accuracy | Note |
|---|---:|---|
| `domain` | **0.422** | eval floor `minDomainAcc` is **0.40** — the weakness is institutionalized |
| `task_type` | **0.530** | 10-entry taxonomy; `leakage(task_type)=0.625` |
| `subcategory` | 0.500 (0.650 w/ context) | |
| `activity_type` | 0.750 | acceptable |
| `sensitivity` (class) | 0.812 | acceptable |
| `function_guess` | 0.706 (0.824 w/ context) | acceptable |
| `sensitivity` recall | 0.351 | out of scope here — see Non-goals |

Two mechanisms are suspected, and they compound:

1. **The classifier does lexical matching, not comprehension.** GLiNER2's
   classification head scores label *descriptions* against the text with a
   bi-encoder. `AGENTS.md` records that "the label wording is load-bearing" —
   an admission that the score depends on surface overlap. On a freeform
   conversational transcript, that is a weak signal.
2. **The input is too small to contain the answer.** A single user prompt
   frequently lacks the context needed to classify it (`do it`, `commit`,
   `push and open pr`). The information is in the surrounding conversation.

A prompted generative model addresses both: it reads rather than matches, and it
can be given the conversation.

## The goal is two-tier understanding

Stated by the project owner on 2026-08-07, and it shapes both the window and the
output:

> understand what the **session** is about, and then, at a finer grain closer to
> the more recent chat messages, what is **being discussed** within that context.

and, restating the system's purpose:

> The point of our system is to understand the conversation, what it's about, what
> the user is trying to do, and what they are talking about.

**This means the enum facets are a proxy for the goal, not the goal.** Three things
are wanted — subject ("what it's about"), intent ("what the user is trying to do"),
and topic ("what they are talking about") — and a 9-entry `domain` vocabulary
cannot express the third at any useful fidelity. The study is therefore designed to
measure the proxy *and* to test a representation that serves the actual goal.

So the target output has **two tiers**, not one:

- **Session tier** — what this session is about: subject matter, intent, the work
  being done. Coarse, stable across the session, changes slowly.
- **Prompt tier** — what the latest exchange is doing *within* that frame. Fine,
  volatile, changes every turn.

This is the capability GLiNER2 structurally cannot provide: it classifies a text,
it has no notion of a session. Which means the two tiers are evaluated
**differently**, and conflating them would make the study meaningless:

| Tier | Control exists? | How it is evaluated |
|---|---|---|
| Prompt | Yes — production GLiNER2 | head-to-head, blinded disagreement adjudication |
| Session | **No** | new capability: human quality review, no win/loss |

**A consequence that constrains the design.** A free-text "what is this session
about" is exactly the unmaskable prose ruled out under Non-goals — it can quote or
paraphrase the prompt and no deterministic gate can prove otherwise. Therefore:

- The **publishable** session signal must be **enum-shaped**, reusing the existing
  vocabularies at session scope (session `domain`, session `function_guess`, and a
  session `activity_type`), so masking stays structural and Atlas keeps aggregating.
- The study *also* collects a short free-text session summary as a **local-only
  diagnostic**, never published and never sent anywhere, purely to judge whether
  the model genuinely comprehends the session. It is a measurement instrument, not
  a proposed output. Any future attempt to publish it needs its own privacy design.

### Topic terms: bounded open vocabulary, deterministically gated

To serve "what they are talking about" at a fidelity no enum can reach, the model
also emits **short topic terms** — a handful of 1–4 word noun phrases naming what
the conversation is actually about (`retry logic`, `settings polling`,
`notarization queue`).

These are **verified, not trusted.** Each emitted term must be found in the source
transcript by literal substring match (case-insensitive); **any term that cannot be
located is dropped.** So the published set can only ever contain text that
demonstrably occurred in the conversation — the same deterministic gate proposed for
`sensitivity` spans, and the same guarantee `enrich.Mask` relies on. A hallucinated
or paraphrased term fails the check and never reaches an output.

This is a **new capability with no control**, evaluated on: verification pass rate
(what fraction of emitted terms were real), and human quality review of whether the
surviving terms actually name the conversation's subject. It is measured here, not
shipped here — publishing topic terms would need a privacy review of its own, since
a verified substring is still transcript-derived text. Note the existing precedent
and its posture: `publish.Build` already carries `domain_entities` surface text
behind `includeEntityText`, **gated off by default**.

### Two-part window

The two tiers want different views of the transcript, so the window has two parts:

- **Session digest** — broad coverage of the whole session, aggressively
  compressed: the opening user prompt (which usually states the goal) plus
  compressed later turns sampled across the session.
- **Recent window** — the last K turns in detail, target prompt last. This is the
  fine-grain view.

Both are assembled from the same parsed transcript in one pass, and both are
rendered into the single prompt so one inference produces both tiers.

## Scope

**In scope.** Round 1 scores, at the **prompt tier**, `domain`, `task_type`,
`subcategory` (the weak facets), with `function_guess` and `activity_type` as
secondary readouts. At the **session tier** it produces session `domain`,
`function_guess`, `activity_type` plus the local-only diagnostic summary,
reviewed for quality rather than scored against a control.

**Non-goals — explicitly excluded from this study:**

- **`sensitivity`, `sensitivity_spans`, `domain_entities`, `creddetect`.** Span
  extraction requires exact character offsets, which generative models produce
  unreliably, and `enrich.Mask` assumes a span is a real substring of the prompt.
  The mitigation (emit verbatim text, locate it with `strings.Index`, drop any
  span that cannot be found — so only verified substrings are ever masked) is
  sound but is a separate round with its own risks.
- **Any integration into the daemon or sidecar.** Not in this study.
- **Changing label vocabularies or `SchemaVersion`.** The study deliberately
  reuses the current vocabulary verbatim; see "Prompt contract".
- **Publishing free-form model output.** `enrich/mask.go` is 21 lines because
  every published field is a closed-vocabulary `Labeled` or a maskable span.
  Free-form prose has no deterministic redaction gate. The study's output stays
  enum-shaped for exactly this reason.

## Relationship to `feat/multiturn-context`

An unmerged branch `feat/multiturn-context` is **under active development by
another agent**. This study treats it as **read-only prior art** and must not
modify it.

Its relevant content — `internal/agent/enrich/eval/mine/` (conversational window
extraction), `internal/agent/enrich/contextpack/` (budget allocator), and
`internal/agent/enrich/eval/multiturn_*.go` (metrics + sweep harness) — **does not
exist on `main`.** This study therefore implements its own window miner rather
than depending on files it cannot rely on. Reconciliation, if that branch lands,
is future work.

**Two substantive divergences, recorded deliberately:**

1. **Tool uses are included here; that branch drops them.** Its miner states
   "Non-text blocks (tool_use, tool_result, thinking, image) contribute nothing."
   Which tools were invoked is signal about the work being done, so this study
   keeps a compact form of them. Consequently that branch's measured
   "conversation is 1.79% of raw transcript bytes" does **not** transfer to this
   window shape, and byte budgets must be re-measured.
2. **The compression machinery is not needed here.** `contextpack` (slot budgets,
   headtail/extractive policies, budget redistribution) exists to fit multi-turn
   context into GLiNER2's window — 768 word tokens, superlinear in RAM and
   latency, where by that design's own measurement "two assistant turns saturate
   today's entire window." A local LLM with a 4–8k context and roughly linear
   prefill largely dissolves that constraint. This study passes the window
   substantially uncompressed, which is the point.

## Constraints

**The experiment host is not the production budget.** This machine has 20 cores
and 30.8 GB RAM. Production is bounded by `KELD_SIDECAR_MEM_BUDGET_MB` (4,096) and
a 30 s `DefaultPassTimeout`. The study runs unconstrained *and records what an arm
would cost*, so integration feasibility is measured rather than assumed.

**Latency is the likely binding constraint, not quality.** CPU prefill over a
multi-thousand-token window is the cost centre; published CPU figures for a 3B
model at 4,096 context are single-digit tokens/second. An arm that cannot classify
a window in under ~30 s is not integrable regardless of how accurate it is.

## Architecture

Offline CLI, in the `feat/llm-classify-study` worktree. Four components, each
independently testable:

```
transcripts (JSONL, on-device)
        │
        ├──────────────────────────────┐
        ▼                              ▼
  ┌───────────┐              ┌──────────────────┐
  │  miner    │              │ production input │  raw target prompt +
  │  K=8      │              │ (as shipped)     │  Meta preamble (3 prompts)
  └───────────┘              └──────────────────┘
        │                         │          │
   window: last K turns,          │          │
   oldest-first, target LAST      │          │
        │                         │          │
        ├───────────┐             │          │
        ▼           ▼             ▼          ▼
  ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌────────────┐
  │ arm A:   │ │ arm B:   │ │ control: │ │ arm C:     │
  │ Qwen3-4B │ │ Qwen3    │ │ GLiNER2  │ │ guard-omni │
  │ llama.cpp│ │ -1.7B    │ │ sidecar  │ │ (sidecar)  │
  └──────────┘ └──────────┘ └──────────┘ └────────────┘
        │           │             │            │
        └─────┬─────┴─────────────┴────────────┘
                                     ▼
                             ┌───────────────┐
                             │ differ        │  agreements discarded
                             └───────────────┘
                                     ▼
                             ┌───────────────┐
                             │ adjudication  │  blinded, provenance hidden
                             │ set (human)   │
                             └───────────────┘
                                     ▼
                          win/loss/tie + binomial CI
                          p95 latency, peak RSS, JSON validity
```

### Component 1 — window miner

Reads Claude Code transcript JSONL and emits a window of the last K turns,
**oldest-first so the prompt under classification is last**.

Rendering rules:

- **User turns**: text verbatim.
- **Assistant turns**: prose verbatim; consecutive records belonging to one reply
  are merged.
- **Tool uses**: one compact line — tool name plus a short identifying argument
  (`tool: Edit settings.go`). **Tool *results* are dropped entirely, and argument
  bodies are never rendered** — a `Read` of a 2,000-line file must not become
  2,000 lines of window.
- **Fenced code**: replaced with `[code block, N lines]`. That code was written is
  signal; the code is bulk.
- **Thinking blocks, images, sidechains**: dropped.
- **Per-turn character cap** so one pathological turn cannot dominate the window,
  and a whole-window cap as a backstop.

**K is fixed at 8 turns** for the head-to-head. It is a parameter of the miner,
but sweeping it is **explicitly follow-up, not round 1** — a sweep multiplies the
adjudication load by the number of settings, and the question round 1 answers is
"does a prompted LLM read a conversation better than GLiNER2 matches labels," not
"what is the optimal K."

Deterministic and pure: given a transcript and K, the window is reproducible.
This is what makes the arms comparable and the tests meaningful.

### Component 2 — arms

| Arm | Model | Runtime | Rationale |
|---|---|---|---|
| Control | `fastino/gliner2-large-v1`, **production input** | existing sidecar | the baseline being challenged |
| A | `Qwen3-4B-Instruct`, Q4_K_M | `llama.cpp` | front-runner on published evidence |
| B | `Qwen3-1.7B-Instruct`, Q4_K_M | `llama.cpp` | does parameter count buy accuracy here? |
| C | `hivetrace/gliner-guard-omni`, **production input** | existing sidecar | separates "LLM" from "better encoder" |

Qwen3 is chosen on evidence rather than familiarity: Qwen3-4B reaches **0.65 F1**
on zero-shot text classification where Llama-3.2-3B-Instruct and Phi-4-mini-instruct
sit in the **mid-0.40s** ([BTZSC, arXiv 2603.11991](https://arxiv.org/html/2603.11991)),
and on log-severity classification Qwen3-4B scored **27.12%** against Gemma3-4B's
**4.79%** ([arXiv 2601.07790](https://arxiv.org/pdf/2601.07790)). At this scale
instruction-tuning quality dominates parameter count, so the second arm is spent
on a size ablation within Qwen3 rather than on another vendor.

**The control must receive its production input, not our window.** This is a
methodological requirement, not a detail. In production GLiNER2 sees the raw
target prompt plus the existing `Meta` preamble (last 3 user prompts, capped);
that is the thing being challenged, so that is what the control runs. Feeding
GLiNER2 our multi-turn window instead would be **invalid** for two compounding
reasons: it is out of distribution for that backend, and gliner2 truncates
**head-keeping** (`text_tokens[:max_len]`) while our window places the target
prompt **last** — so a window exceeding the 768-token cap would silently discard
the very prompt being classified, sandbagging the control into an artificial loss.
Arms A/B receive the window; the control **and arm C** receive production input —
arm C is an encoder subject to the identical truncation behaviour, and it only
answers "is this a better encoder" if its input matches the control's. The study
therefore measures **"is a prompted LLM on a conversation better than what we
ship,"** which is the decision-relevant question. It deliberately does *not*
isolate model change from input change — that decomposition needs a GLiNER2 arm
fed a *compressed* window, which is what `contextpack` on the other branch exists
to build, and is out of scope here.

Arm C is nearly free — a fine-tune of `fastino/gliner2-multi-v1`, loaded by the
same `gliner2` library through the existing sidecar, no new runtime. Its card
reports 307M params under Apache-2.0 while its paper reports 209M on mmBERT-small
under CC BY 4.0; **this discrepancy must be resolved before any integration
decision**, since a CC BY 4.0 weight licence is a distribution question. It does
not affect the study's validity.

**Runtime**: `llama.cpp` (`llama-server`) with **GBNF / JSON-schema constrained
decoding**, so output is structurally guaranteed to be valid JSON drawn from the
declared enums — no parse failures, no invented labels. Neither `llama.cpp` nor
`ollama` is installed on this host; setup is a task. Everything runs locally.

### Component 3 — prompt contract

One inference per window, returning all in-scope facets as a single JSON object.

**Enums and their descriptions are read directly from `enrich/labels.go`.** Not
transcribed, not paraphrased — read from the source of truth, so the study cannot
silently drift into evaluating a different taxonomy. This is what makes the
comparison fair, and it means a positive result transfers to the existing wire
format with no schema change.

The prompt states the window's turn structure, names the target prompt as the
classification subject, and instructs that context is for interpretation only —
the label describes the *target prompt*, not the whole session.

### Component 4 — differ and blinded adjudication

1. Run every arm over N = **200** mined windows.
2. Per facet, diff each arm against control. **Agreements are discarded** — they
   carry no information about which model is better.
3. Emit an adjudication set: the window, plus the competing answers **shuffled
   with provenance hidden**, so the adjudicator cannot tell which model produced
   which answer.
4. Human adjudication picks the better label (or "tie" / "both wrong").
5. Report per-facet **win/loss/tie with a binomial confidence interval**, so a
   12-of-20 result is not mistaken for a finding.

Expected hand-labelling load: **40–80 rows**, not 200 — the reason approach (C)
was chosen over building a gold set.

## Metrics

Per arm, per facet: win/loss/tie vs control, binomial CI, agreement rate with
control.

Per arm, operationally: **p95 and max wall-clock per window, peak RSS, JSON
validity rate, window token count distribution.**

## Kill criteria — pre-registered

Stated in advance so the study can genuinely fail:

- **No significant win.** If adjudication shows no per-facet improvement whose CI
  excludes parity, that is the finding. A null result is reported, not retried
  with a better prompt until it passes.
- **Latency.** An arm whose p95 exceeds ~30 s per window on 20 cores is recorded
  as not integrable and dropped from consideration, whatever its accuracy.
- **Adjudication ambiguity.** If a facet's disagreements are frequently
  "both wrong" or judged arbitrary, that facet's *label vocabulary* is the
  problem, not the model. Report it as such rather than crowning a winner.

## Testing

- **Miner**: table-driven tests over fixture transcripts — tool-use rendering,
  tool-result exclusion, code elision, turn merging, per-turn and window caps,
  target-prompt-last ordering, determinism.
- **Prompt builder**: asserts enums match `enrich/labels.go` exactly, so a
  vocabulary change breaks the test rather than silently skewing the study.
- **Differ**: agreements dropped, disagreements retained, provenance absent from
  adjudication output, shuffling deterministic under a seed.
- **Live arms**: build-tagged like `sidecar_eval_test.go`, skipped when the
  backend is unreachable, so `go test ./...` stays green without a model present.

## Risks

| Risk | Mitigation |
|---|---|
| Latency makes any win academic | measured and pre-registered as a kill criterion |
| Adjudicator bias toward fluent output | provenance hidden and answers shuffled |
| N = 200 too small for per-facet significance | CIs reported; N is extensible if a facet is borderline |
| Windows are one machine's transcripts, not representative | acknowledged; this is a directional study, not a shipping gate |
| Overlap with `feat/multiturn-context` | separate worktree; that branch read-only; divergences recorded above |

## Outcome

A results document recording, per facet and per arm: win/loss/tie with CIs, p95
latency, peak RSS, and a plain recommendation — pursue integration, pursue with a
different arm, or stop. If the recommendation is to pursue, the integration
design (including the span-verification gate for `sensitivity`) is a separate
spec.
