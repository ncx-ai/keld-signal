# Does the model stay in the classification path? — the test design

Handoff decision 1 (`docs/superpowers/plans/2026-08-21-custom-enrichment-handoff.md`). The
model's measured contribution to attribution is **zero** over 130 classifier-windows. Two
untested bets are the only remaining reason to keep it:

* **NEW** — the text names something the label set lacks. The discovery signal, and the one
  thing GLiNER structurally cannot express (it scores against a fixed label set).
* **Cross-function override** — an engineer's spend belonging to Finance. Everything else in
  the pipeline says Engineering, because Atlas says the person is on the Engineering team.

Both are measured here, blind, on the same 13 real windows the coverage number came from.

## What the corpus can and cannot support

Searched every transcript on this machine (`~/.claude/projects` 39+11 sessions,
`~/.codex/sessions` 14, no Gemini) for work that genuinely belongs to another business
function. **There is none.** Every finance/legal/marketing hit is engineering work whose
*subject* is another function: LiteLLM pricing data, opex/capex split rules, a budget box, UX
copy for a finance admin. Four of the thirteen windows are saturated with finance vocabulary
(90, 100, 81, 63 finance terms) and all four are Engineering work.

So the corpus gives the override's **negative** half in abundance and its positive half not at
all. The design below gets the positive half by **inverting the hint** instead of inventing a
transcript: told the person is on the *Finance* team over unmistakably-engineering content, a
model that answers `Engineering` has demonstrated exactly the capability in question — content
contradicting the recorded team. Ground truth is sound and nothing is fabricated.

## Arm 1 — NEW, by label ablation

Ground truth comes from a fact we hold independently of the text: the cwd repo. Remove the true
label from the label set and withhold the fact, and the only correct answer is a name quoted
from the window — the NEW case, with checkable truth.

| sub-arm | dimension | label set | correct answer | n |
|---|---|---|---|---|
| recall | `repository` | true label removed | the repo name, in the model's words | 13 |
| recall | `project` | true label removed | `Atlas` / `Signal` | 10 (john excluded: no defensible truth) |
| control | `repository`, `project` | full | the true label | 23 |
| silence | `customer_account`, `marketing_campaign`, `sprint_iteration`, `ticket_id` | as shipped | **empty** — none of these exist in this corpus | 52 |

The silence sub-arm is the precision denominator. A NEW channel that finds the repo but also
invents a customer has not earned its place; these four dimensions are where a free-text answer
is always wrong, so they price the channel.

Answers are bucketed: `truth` / `wrong_label` (nearest-match fabrication from the ablated set) /
`other_in_text` (grounded but irrelevant — grounding proves presence, not relevance) /
`not_in_text` (fabrication) / `empty`.

## Arm 2 — cross-function override, two framings

**classify** — "Which business function does this work serve?", team hint in the tail, hint
varied over {Engineering (true), Finance, Marketing, Legal, Customer Support, none}: 78 asks.
Measures **hint-following**. If the answer is a function of the hint, the override channel
carries no information: it can never contradict the team, it can only echo whatever Atlas said.

**detect** — the production shape. "Our records say this person is on the *H* team. Does this
work serve *H*, or a different function?" over H ∈ {Engineering (true), Finance, Marketing}: 39
asks. Abstention here is *structural* — "no override" is silence, the only abstention shape that
has ever worked in this study. Precision = silence on the true hint (a spurious override
misroutes money on the exact dimension the report is for). Recall = naming `Engineering` under a
false hint.

## Pre-registered decision rule

Written before the run so the outcome cannot be rationalised afterwards.

* **NEW earns its place** iff recall > 0.5 on the ablated sub-arm **and** fabrication ≤ 0.1 on
  the silence sub-arm. Zero recall means there is nothing to keep; high fabrication means the
  channel costs more than it returns.
* **Override earns its place** iff `detect` recall ≥ 0.8 under a false hint **and** spurious
  override ≤ 0.1 under the true hint, *including the four finance-saturated windows*. Failing
  the second is worse than failing the first: a false override is a wrong number in a financial
  report, where a missed one is only a gap.
* If neither passes, the model leaves the classification path and GLiNER2 keeps it.

## Method, per the handoff's working rules

Granite 4.1 3B Q4_K_M, temperature 0, free-string answer schema (an off-list answer stays
visible instead of being grammar-forced into the enum). Blind: the record's `projects:` line is
stripped — with it, a window naming no repository returned `keld-atlas`; without it, `keld-cli`.
One neutral prefix per window (record + window) so all 221 asks reuse one prefill, with every
varying part — question, options, team hint — in the tail. GPU is a smoke test; every number
quoted is re-measured on CPU at 18 threads.

Script: `scripts/model_bets.py`.

---

## Results — 13 windows, 215 asks, Granite 4.1 3B, temperature 0

**GPU run. Per the handoff's rule this is a smoke test, not evidence** — but the decisive cells
are 0/23 and 0/20, not the marginal one-token decisions where the two backends were measured to
diverge. A CPU-at-18-threads confirmation is still owed before any of this is quoted.

| what the model was asked to do | asks | right | verdict |
|---|---|---|---|
| **NEW** — name the repo/project when the label set cannot express it | 23 | **0** | dead |
| pick the true label when it *is* offered (control) | 23 | 2 | barely alive |
| stay silent on four dimensions this corpus has no instance of | 52 | **52** | works |
| **override** — flag the function when told the wrong team (`detect`) | 20 | **0** | dead |
| not flag a false override under the true team (`detect`) | 10 | **10** | works, vacuously |
| contradict a false team hint (`classify`) | 40 | 1 | dead |
| judge the function from content alone, no hint | 10 | 5 | coin flip |

**What it does instead of discovering.** Under ablation it went silent 18 times in 20 — and the
only two times it spoke it named *the wrong repository*: `keld-signal` for a keld-atlas window
(which mentions keld-signal 9 times) and `keld-atlas` for a keld-signal window. So the NEW
channel returned nothing correct and everything it did return was a misattribution.

**The hint is the answer.** Told the person was on Marketing over TypeScript frontend work,
10 of 10 windows answered `Marketing`. Legal 6/10, Customer Support 8/10, Finance 7/10.
Content-anchored under a false hint: 1 in 40. The hint sat in the *tail*, nearest the answer,
where recency should have made it easiest to resist.

**Silence is unconditional, so its precision is worthless.** `detect` abstained on 29 of 30
asks — 10/10 correct under the true hint and 0/20 under a false one. A channel whose only
output is silence cannot be scored: it earns perfect precision by never answering.

**The confound is live.** With no hint at all, three of the four finance-saturated windows
(100, 79, 68 finance terms) answered `Finance` for work that is building a finance feature.
That is the expensive direction of error — Engineering spend booked to Finance in a report whose
entire purpose is that split.

## Verdict against the pre-registered rule

* NEW needed recall > 0.5: got **0.0**. Fabrication was 0.0 as required, but there is nothing
  to buy with that precision.
* Override needed `detect` recall ≥ 0.8: got **0.0**. Precision passed only because the channel
  never fires.

**Both bets fail. The model leaves the classification path.** The two things it does well —
silence, and not inventing — are both *not answering*, which GLiNER2 also does for free and
without 2.8 GB of RSS. Everything that would put a number in a report measured zero.

What would reopen the question, in order of cost: transcripts of genuinely non-engineering work
(`coworkHidden` is the blocker), a bigger model (Granite 8B was rejected on 6,640 MB and 250 s
per window, not on quality), and label sets whose descriptions were rewritten for a generative
reader rather than inherited from GLiNER.
