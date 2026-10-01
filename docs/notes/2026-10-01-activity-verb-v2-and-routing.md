# The activity verb axis: what shipped, and the routing purpose it is aimed at

**Date:** 2026-10-01
**Status:** shipped as a **DESCRIPTIVE** signal. ⚠️ **Not approved for routing** — see §4 and §5.
**Measured on:** 18,451 inference requests from corpus B, with corpus A (194 requests) as a
second-person check. Reproduce with the snippets in §6.

## 1. What `activity_verb` is, relative to `activity_class`

**The verb is DERIVED from the class. It is not a second opinion about the same request.**
`verbs.verb_for(cls, tools)` takes the class as its first argument and refines it.

| `activity_class` | share of requests | what the verb adds |
|---|---|---|
| `retrieve` | 30.1% | nothing — abstains |
| `synthesize` | 18.6% | nothing — abstains |
| `acknowledge` | 13.2% | nothing — abstains |
| `operate` | 7.9% | nothing — excluded as not-work |
| `unclassified` | 1.4% | nothing — abstains |
| `verify` | 7.2% | a pure rename to `review` |
| `delegate` | 1.9% | a pure rename to `plan` |
| **`author_code`** | **15.7%** | **splits** into `code.write` / `code.edit` / neither |
| **`author_prose`** | **4.0%** | **splits** into `text.create` / `text.transform` / neither |

So the verb **restates the class on 9.1% of requests, refines it on 19.7%, and is silent on 71.2%.**
⚠️ **The create-vs-edit split is the only new information in the level.** A reader who treats
`activity_verbs` as an independent classification of the work will double-count it against
`activity_classes`.

### 1a. The mapping, exactly

`verbs.verb_for(cls, tools)` — all nine classes, with the condition each one turns on.
`None` means **no row is emitted at all**, not an empty value.

| `activity_class` | condition on the request's tools | → `activity_verb` |
|---|---|---|
| `verify` | — (unconditional) | `review` |
| `delegate` | — (unconditional) | `plan` |
| `author_code` | a `Write` is present | `code.write` |
| `author_code` | an `Edit` / `MultiEdit` / `NotebookEdit`, and no `Write` | `code.edit` |
| `author_code` | neither in evidence | **`None`** |
| `author_prose` | a `Write` is present | `text.create` |
| `author_prose` | an `Edit` / `MultiEdit` / `NotebookEdit`, and no `Write` | `text.transform` |
| `author_prose` | neither in evidence | **`None`** |
| `retrieve` | — | **`None`** (pending split, §2) |
| `synthesize` | — | **`None`** (pending split, §2) |
| `operate` | — | **`None`** (excluded: not work) |
| `acknowledge` | — | **`None`** (excluded: not work) |
| `unclassified` | — | **`None`** (an honest abstention, kept as one) |

Four things in that table are decisions rather than mechanics, and each is load-bearing:

- **Tool names are matched after stripping any MCP prefix** (`name.split("__")[-1]`), matching
  `reqclass`. So `mcp__abc__Write` resolves. ⚠️ A reviewer mutated this line and found the suite
  stayed green, so it is now pinned by its own test.
- ⚠️ **`author_code` / `author_prose` with NEITHER tool returns `None` and does not guess.**
  `classify_bash` and `CODE_TOOLS` both reach `author_code` with no authoring tool in evidence at
  all (`python3 -c '...'`, `javascript_tool`), and `PROSE_TOOLS` reaches `author_prose` the same
  way. Picking a side there would publish a false claim about someone's work from evidence that
  does not contain the answer. It is 1.5% and 2.7% of requests respectively — not a rounding
  error, and the reason the verb's coverage is 24.7% rather than 28.8%.
- **A request carrying BOTH a create and an edit resolves to create.** Creating a new thing is
  the larger claim about the work. The alternative was tool-list order deciding it, which made
  the answer depend on an ordering nothing controls; a wrong-but-stable rule beats that. Pinned
  from both orders by a test.
- **A class that is not in the table returns `None`.** A tenth class added to `reqclass` would
  therefore abstain silently, which is indistinguishable from a deliberate exclusion — so
  `HANDLED == set(reqclass.CLASSES)` is asserted by a test and a new class fails loudly instead.

⚠️ **The verb distribution's total is NOT the block's request count.** It covers 24.7% of
requests. `activity_classes` is the complete denominator; normalising against the verb
distribution reports shares of a subset as shares of the work.

## 2. Why the create/edit split is worth having, and the rest is not

Median output tokens, measured:

| | p50 | p90 |
|---|---|---|
| `code.write` | **1,509** | 5,079 |
| `code.edit` | 526 | 2,016 |
| `text.create` | **3,512** | **14,522** |
| `text.transform` | 596 | 2,804 |

**2.9x and 5.9x.** `author_code` alone reports 583 and hides both; `author_prose` reports 626
and hides a 6x spread whose p90 is 14,522 tokens.

The two splits that were *proposed* and measured on 2026-10-01 — `retrieve` → `research`/`extract`
and `synthesize` → `text.summarize`/`research` — separate nothing:

| proposed distinction | measured separation |
|---|---|
| whole-file Read (125) vs bounded Read (166) | **1.3x** |
| unstructured prose (1,022) vs structured (1,027) | **1.0x** |

They also failed their own blind-label study
(`docs/notes/2026-10-01-verb-split-results.md`, 138 of 200 requests `unclear`). Two independent
reasons to leave `retrieve` and `synthesize` abstaining, and they agree.

## 3. "v2" needs no client change — it is a join Atlas already has the inputs for

The composite that measured best (§4) is `activity_class` refined by create-vs-edit. **The pair
`(activity_class, activity_verb)` already determines it exactly** — verified over 18,451
requests: 13 distinct pairs, 13 distinct values, no ambiguity.

```
author_code  + code.write     -> code.write      verify       + review -> verify
author_code  + code.edit      -> code.edit       delegate     + plan   -> delegate
author_code  + (none)         -> code.author     retrieve     + (none) -> retrieve
author_prose + text.create    -> text.create     synthesize   + (none) -> synthesize
author_prose + text.transform -> text.edit       operate      + (none) -> operate
author_prose + (none)         -> text.author     acknowledge  + (none) -> acknowledge
                                                 unclassified + (none) -> unclassified
```

⚠️ **Publishing this as a third 13-value level was considered and rejected.** Seven of its
thirteen values would duplicate `activity_classes` verbatim, for a schema bump, a fixture
rebaseline and an Atlas column that can disagree with the one beside it. The join costs nothing
and cannot drift.

## 4. The routing purpose, and the evidence as it stands

**The intended future use of this axis is routing** — choosing a cheaper or more capable model
per request or per block. This section records how far the evidence actually goes, so that
intent does not get mistaken for a mandate.

Output tokens is the cost proxy throughout. Held-out tests, with a noise control:

**Predicting cost** (MAE on log₁₀ output tokens; gain = share of error the label removes):

| arm | corpus B held-out | corpus A |
|---|---|---|
| random 13 labels (noise floor) | −0.0% | 0.1% |
| `activity_class` (9) | 16.8% | 11.8% |
| class + create/edit (13) | **18.0%** | **12.4%** |

**Picking the right cost band** (3 bands fitted on train: cheap <239 tok, moderate 239–712,
expensive >712):

| arm | corpus B held-out | corpus A |
|---|---|---|
| always-biggest-band (floor) | 34.4% | 41.2% |
| random 13 (noise) | 33.2% | 29.9% |
| `activity_class` (9) | 54.5% | 45.4% |
| class + create/edit (13) | **56.3%** | **47.9%** |

Both taxonomies beat noise decisively, and the noise control behaving correctly is what makes
the rest believable. But **the label removes only 12–18% of cost error**, and at 56% on a
three-way band choice **44% of requests would be misrouted.**

## 5. ⚠️ Why this is NOT approved for routing

**The cross-corpus gap is real and unexplained.** +21.9 points over floor on held-out
same-corpus data; **+6.7 on corpus A.**

**And the confound is fatal to interpreting it: corpus A is a SINGLE SESSION of 194 requests,
entirely on `claude-fable-5`, while corpus B is a mix of opus-4-8, sonnet-5, opus-5 and haiku.**
"Different person" is completely confounded with "different model" and cannot be separated with
this data.

What the diagnosis does show:
- **The label cost-ORDER transfers** — Spearman ρ = **0.714** across the two corpora. `retrieve`
  and `operate` stay cheap; `synthesize` and `code.author` stay expensive.
- **The distribution shifts** — corpus A's p33 is 395 tokens against corpus B's 242.
- **One label moves hard** — `code.edit` is **3.94x** more expensive in corpus A (527 → 2,079),
  while everything else sits in 0.78–1.42x. That is a model-behaviour difference, and no
  relabelling fixes it: the same act costs four times as much there.

⚠️ **AN ATTEMPTED FIX WAS MEASURED AND FAILED, and the failure is worth more than the fix would
have been.** Per-session relative bands — calibrating thresholds to each session instead of
using absolute token cuts — appeared to recover +4.7 points on corpus A (47.9% → 52.6%). That
number was **LOOKAHEAD LEAKAGE**: it computed thresholds from the very requests it was scoring.
Done causally, calibrating on a session's first 20 requests and scoring only later ones, corpus
A drops to **44.3% — worse than doing nothing.** A router sees a session's past, never its
future. Corpus B is unaffected either way (56.3% → 56.8%) because it has many sessions, so a
20-request warmup is representative there; corpus A is one session, where it is not.

**So the obvious normalisation does not work, and the gap stands.**

**What would resolve it** is narrower than "another corpus": **the same person's work across two
models, or two people on the same model.** Either breaks the confound. Until then this axis is
descriptive.

## 6. Reproducing these numbers

Corpus paths come from the environment and are never hardcoded (`KELD_CORPUS_A`,
`KELD_CORPUS_B`). The per-label medians, the held-out MAE and band tests, the Spearman figure
and the causal-vs-leaky comparison were all computed with ad-hoc snippets over
`transcript.iter_turns` + `reqclass.route_class` + `verbs.verb_for`; none is committed, because
each is a handful of lines and committing them would imply a maintained harness that does not
exist. The study harness that IS committed is `scripts/verbsplit_*.py`, which covers §2's two
rejected splits only.

## 7. Open, dated

- **The `synthesize` / `acknowledge` boundary is a 400-token cut on identical behaviour.**
  Measured: **84%** of `synthesize` requests qualify on length alone, with no structure at all;
  only 16% trip `is_report`. Both are "the model produced prose and called no tool". The
  docstring defends the threshold (three genuine completion reports sat at 247/374/376 tokens,
  which is why structure was added as a second route), but the stability of that boundary has
  not been measured. **Re-check before anyone routes on the distinction** — as of 2026-10-01 no
  consumer does.
- **Reasoning demand is unmeasurable here.** `think_blocks` is empty on essentially every
  request (0.0% non-empty across all nine classes), exactly as AGENTS.md warns. If routing
  should key on "did this need deep reasoning", this data cannot answer it.
- **Cost here is the generation half only.** Output is 10–14% of modelled cost, the rest cache
  reads, and fresh input is 2 tokens median across every class — so it discriminates nothing.
  These figures measure the part that varies, not total spend.
