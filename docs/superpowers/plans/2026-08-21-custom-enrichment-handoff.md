# Custom enrichments on an LLM backend — where we are, and what has to be decided

A handoff. Everything below was measured on this branch; nothing is inferred from reasoning
about the material. Where a number is unreliable, it says so.

## What the work is for

AI usage telemetry, cost and attribution, bucketed by workstream, for financial reporting and
operational insight. An org admin defines "workstreams" from a template gallery
(`~/Downloads/classifier-gallery-templates.json`, 10 templates: `single_label`, `multi_label`,
`entity_extraction`, `structured_extraction`), and spend has to land in those buckets.

That purpose changes what "good" means, and it took us a while to notice: the metric is
**attribution coverage** — what share of spend lands in a real bucket — not per-field accuracy.
A classifier that abstains honestly scores well on accuracy and contributes nothing to a report.

## Where we came from

GLiNER2 classifies each user message as it arrives. Fast, but contextless: "sure, do that"
carries no subject matter, so the enrichment describes the message rather than the work.

We tested a prompted local LLM over a window of the transcript instead. The window is the unit,
not the message, and it closes on a debounce so it includes the assistant turns and tool calls
that FOLLOW the last user prompt — which is what makes the enrichment about the work.

## What is settled

**Model.** Granite 4.1 3B Q4_K_M: 2,858 MB RSS / 786 MB anonymous, against Qwen3-4B's
3,836/1,387, ~20% faster, Apache-2.0, US origin. Granite 4.1 8B is out — 6,640 MB and 250 s per
window. RSS and anonymous are both quoted deliberately: they differ by ~2 GB and quoting one for
the other is how two "RAM usage" figures for the same model disagree.

**Cadence.** One pass per ~5 user prompts, triggered on: ≥5 prompts, ≥20-30 s quiet (the
debounce), or a 10-15 min ceiling. Measured cost on CPU at 18 threads: 50 s for a 1.6k-char
window, 106 s at 10k, 145 s at 16k. A minimal per-message pass (2 booleans) is ~4 s, so
sensitivity can stay per-message on the same loaded model — queued through single-flight, not
concurrent. There is no memory to run GLiNER2 and an LLM together, and no need to.

**Passes over one window are nearly free.** A second pass on a cached prefix costs 1.1 s against
6.8 s. 20 classifiers one at a time cost 121 s total, not 20x73 s. REQUIREMENT: per-pass content
must come LAST, or the prefix cache misses and every pass pays full price.

**Deferred passes need window COORDINATES, not text.** `(sessionId, startTurn, endTurn,
promptIds)` — re-render from the transcript. Same rule as `spool.Pointer`: never store prompt
text. This is what lets a low-priority classifier answer tomorrow against identical evidence.

**Ask the facts first, the model last.** For any dimension where the pipeline already holds the
fact, do not ask. `daemon/context.go` builds `Meta{Repo: j.Cwd, GitBranch, Project}` from the
hook, and keld-cli sends repo context as synchronous telemetry. Languages come from tool-call
file extensions. Team comes from Atlas.

**Windows: tools stripped by default, engineer turns pinned, holes marked and charged.**
Tool lines are noise for tone and were eating the character budget; their COUNTS stay
(`tools in this stretch:`), and `files touched:` carries the language and component signal at
half the size of code excerpts (header 758 -> 395 chars). Command echoes (`<command-name>`,
`local-command-stdout`) and injected skill documents are filtered: they are machine text in a
`user` envelope, and a window of five `/login` echoes scored frustration 4.

## The measured numbers

Attribution coverage over 130 classifier-windows — 10 real engineering windows from 5 keld-atlas
and keld-signal sessions, plus 3 from a colleague's Cowork product-research session:

| source | share |
|---|---|
| known (cwd, git branch, file extensions, team) | 36.9% |
| inferred (model) | 0.0% |
| extracted (model) | 0.0% |
| unrepresentable (label set cannot express a known fact) | 2.3% |
| unattributed | 60.8% |

**The model's measured contribution to cost attribution today is zero.** `work_function` looked
like a 10-for-10 win until an A/B varying only the team hint showed it follows the hint whenever
content is not distinctive — so applying the ask-facts-first rule absorbed it into `known`.
Extraction contributed 0 in 130 attempts.

61% unattributable is a property of THIS corpus: six of ten dimensions (campaign, account,
lifecycle stage, publish destination, sprint, ticket) do not appear in engineering or
product-research transcripts at all. An org whose transcripts discuss accounts and tickets would
look different, and we have no such transcripts.

## What is refuted, with the evidence

**Atlas's classifier descriptions are written for GLiNER and actively mislead an LLM.** They are
"what it is + 2-3 concrete examples" — the gallery's own notes say so — and a generative model
reads the examples as evidence: naming `Northwind` and `Sprint 24` produced `Northwind` and
`Sprint 24`. Migration means REWRITING descriptions, not reusing them.

**Abstention cannot be bought with a label. Seven attempts:** `__none__` in the enum (ignored),
a readable abstention label (ignored), `noise` (1 use in 400), `Backlog` as documented catch-all
(never chosen, 5 windows), `Other` added to every option set (chosen 0 times in 130). What DID
work, every time: removing the question — deleting the tone question when no engineer spoke,
gating on a presupposed entity ("this customer" cannot be asked before a customer is found), and
gating on an unrepresentable fact.

**Open-ended beats an option list for honesty, not for coverage.** With no list and an explicit
`unknown`, 83% answered `unknown` in the model's own words — the first abstention that ever
stuck. But 0% correct: the 13% grounded were category errors (`ticket_id: 'Financial tab
redesign'`, `publish_destination: 'PowerPoint deck'`). Grounding proves PRESENCE, not relevance.

**Field count is not the variable; neighbours are.** 10-16 fields per pass moved the answers of
the first ten. Abstention collapsed above 16 in one run and IMPROVED above 12 in another. A
classifier's answer depends on which others share its pass, so one request per classifier removes
the variable rather than tuning it.

**Carrying answers forward does not work.** Zero coverage gain, and it propagated a wrong first
answer (`keld-signal`) across a whole keld-atlas session. Asking "is it still the same?" returned
`same: false` on all 10 cases, 3 of them where the fresh answer was identical.

**Deriving label sets by open-ended generation does not work.** 13 distinct answers over 13
windows, biased toward subject matter (`Cost-Allocation Dashboard` for engineering work) with
outright fabrication on thin material (`liquidity-routing-engine` for a PowerPoint deck).

## Method notes that cost us time

**GPU is a smoke test, never evidence.** Backends diverge on marginal decisions (3 demand markers
vs 1; complexity 8 vs 5); `-fa off` plus matched batch sizes did NOT reconcile them. They agreed
exactly on one task (blind attribution, 16 s vs 386 s), so agreement is a property of the task —
confirm each metric separately. CPU is deterministic run-to-run, and thread count does not change
the answer (18 threads = 4 threads, 130 s vs 195 s), so iterate at 18.

**Test blind.** The record's `projects:` line silently supplied answers: a window naming no
repository returned `keld-atlas` with the record and fabricated `keld-cli` without it. Strip it
in any control, or the record answers for the model.

**Watch what the harness adds to the window.** `tools in this stretch:` was quoted back as
content twice, and `programming_language` returned `Bash x354...` from our own header.

## Decisions that stand ahead

1. **Does the model stay in the classification path at all?** Measured contribution is zero
   today. Keeping it rests on two unmeasured bets: the `NEW` case (text naming something the
   label set lacks — the discovery signal, and the one thing GLiNER cannot express), and the
   cross-function override (an engineer's spend belonging to Finance, which the A/B showed the
   model getting right on distinctive content). Both are testable; neither is tested.

2. **What fills 61%?** The six absent dimensions are in other systems — sprint in the tracker,
   account in the CRM, campaign in the marketing tool. Either integrate those, drop the
   dimensions, or accept a report that is mostly unattributed. This is a product decision, not a
   modelling one.

3. **Unattributed and unrepresentable must be first-class rows.** A bucket nobody filled and a
   label set that cannot express the truth are different facts, and both differ from `Other`.
   Reports need all three, plus coverage as a headline number.

4. **Provenance per attribution.** `keld-atlas` from the cwd and `keld-signal` from five
   mentions in the text are different epistemic objects. Financial numbers get audited; the span
   is the audit trail. Decide the schema now.

5. **Multi-label breaks cost allocation.** `work_function: [Engineering, Operations]` double-
   counts. "Every label that fits" is right for observation and wrong for money — any dimension
   used as a cost bucket needs a single value or an explicit split rule.

6. **Reproducibility.** Reports must not move when nothing changed. Record model version, prompt
   version and label-set version with every enrichment, and pin a reporting period — the shape
   `SchemaVersion` already has.

7. **Per-session vs per-window for stable dimensions.** `programming_language: Markdown` for a
   docs commit in a Go repo is right about the bytes and wrong about the work. Dominant language
   per session probably beats per window.

8. **Gallery fixes, independent of any of the above.** `Work Function` has no catch-all;
   `Customer Lifecycle Stage` has none and asks the transcript for a CRM fact (8 formulations,
   always confident, always wrong — recommend not shipping it); `Publish Destination` has 8
   labels, no catch-all, and selected all 8 under several configurations.

9. **Cowork is not captured.** The colleague's session is VM-backed; `coworkHidden` detects
   exactly this and logs one advisory line. Non-engineering transcripts are the material this
   whole design most needs, and today we cannot see them.

## Where the code is

    scripts/coverage.py             attribution coverage by source — the headline measurement
    scripts/classify_experiment.py  ask each classifier its question; --device gpu|cpu
    scripts/classifier_schema.py    schema, abstention, and shape derivation from the label set
    scripts/qwen_windows.py         transcript -> windows (tools stripped, files/languages, holes)
    scripts/qwen_test.py            one prompt file against one window; --cpu for quotable timing
    scripts/prompt-v8-routing.md    routing prompt (tone/demand decomposition, work block)

`classifier_shape()` derives extractive vs inferential from the label set — name-like labels
(`acme/api`, `Northwind`, `sprint_24`) extract; concept-like labels (`engineering`, `prospect`)
must be inferred, and asking them to extract returned nothing on 8 of 10 fields. 9 of 10 match a
hand split.
