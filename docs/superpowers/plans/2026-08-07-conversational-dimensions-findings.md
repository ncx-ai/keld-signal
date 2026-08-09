# What can be reliably captured from conversational data

Measured 2026-08-07 on 200 windows from 464 real Claude Code transcripts, scored by
three independent systems: GLiNER2 large-v1 (the shipped encoder), Qwen3-4B-Instruct,
and Qwen3-0.6B.

This answers a design question, not a model-selection one: **which dimensions are
reliably measurable from interactive conversational data at all?** It is separate
from the quality comparison, which still needs human adjudication.

## The question, and why it came up

The shipped taxonomy assumes it is classifying **discrete, task-like prompts** — the
shape of a completion API call. Interactive sessions are not that: a turn is a move
in a conversation, not a self-contained job. Asking "what task is `do it`?" has no
answer, so a facet built on that assumption cannot be measured reliably no matter
which model runs it.

## Method

Reliability here means **cross-system reproducibility**: if three independent systems
looking at the same conversation produce the same answer, the dimension is measurable;
if they don't, it isn't — regardless of which one is "right".

Two levels were compared:

- **Per turn** — exact-match agreement on each window's label.
- **Session focus** — each arm's per-turn labels folded into an EWMA-decayed
  distribution per session (`Focus`, alpha 0.3), compared two ways: argmax match, and
  **cosine similarity of the full score vectors** (so a dimension that legitimately
  varies within a session can still be reproducible as a *mix*).

Restricted to the 21 sessions with >= 5 observations, so a focus is meaningful.

## Result 1: aggregation splits the facets cleanly

| facet | per-turn agreement | session-focus argmax | delta |
|---|---:|---:|---:|
| `domain` | 86% | **100%** (21/21) | **+14** |
| `function_guess` | 83% | **100%** (21/21) | **+17** |
| `subcategory` | 45% | **71%** | **+26** |
| `activity_type` | 31% | 24% | −8 |
| `task_type` | 35% | **14%** | **−20** |

Aggregation makes `domain` and `function_guess` **perfectly reproducible** across two
independent models. It makes `task_type` substantially **worse**.

## Result 2: it is the dimension, not the model

Cosine similarity of session-focus distributions, all three pairs (median over sessions):

| facet | 4B ~ 0.6B | 4B ~ GLiNER2 | 0.6B ~ GLiNER2 | verdict |
|---|---:|---:|---:|---|
| `function_guess` | 0.99 | **1.00** | **1.00** | **reliable** |
| `domain` | **1.00** | 0.78 | 0.87 | **reliable** |
| `subcategory` | 0.78 | 0.65 | 0.49 | marginal |
| `task_type` | 0.51 | 0.66 | 0.42 | **not reliable** |
| `activity_type` | 0.41 | 0.56 | 0.40 | **not reliable** |

No pair on `task_type` or `activity_type` exceeds 0.66, and most sit at 0.40-0.56.
Three systems of very different architectures and sizes — a 4B decoder, a 0.6B
decoder, and a 435M-class bi-encoder — all disagree with each other. A dimension that
no two independent measurers reproduce is not a property of the data being measured.

**A hypothesis this refutes.** When aggregation hurt `task_type`, the natural
explanation was that it "genuinely varies turn to turn, so the argmax is unstable but
the mix is real." That predicts high distribution cosine with low argmax agreement.
Measured cosine is 0.42-0.66. The mix does not reproduce either, so the explanation
was wrong: this is not stable variation, it is disagreement.

## Conclusions

**Reliable at session scope — publish with confidence:**
- **`function_guess`** — 0.99-1.00 across all three pairs. The single most reproducible
  dimension measured, and reproducible even between architectures.
- **`domain`** — 1.00 between the two LLMs; 0.78-0.87 against the encoder. Note the
  encoder is the outlier here, consistent with its measured 0.422 gold-set accuracy.

**Marginal — usable with a concentration threshold:**
- **`subcategory`** — 0.49-0.78. Worth keeping only where the focus is concentrated.

**Not reliably measurable from interactive conversation, at any scope, by any model
tested:**
- **`task_type`**, **`activity_type`**.

For `task_type` this is a **scoping** result, not a condemnation: it is the routing key
for discrete completion-API jobs, where a prompt *is* a task and the question is
well-posed. It should simply not be emitted for interactive sessions. `activity_type`
has no such home and needs either a genuinely different vocabulary or retirement.

## What replaces them: deterministic structural signals

The dimensions models cannot agree on are also the ones we most want for routing
("how hard is this work"). Structural signals sidestep the problem entirely: they are
computed from the transcript with no model, so they are **reproducible by
construction** — cross-system agreement is 1.00 trivially — and they are **counts
only**, hence publishable without a masking gate.

Measured spread over 52 sessions:

| signal | p10 | p50 | p90 | p99 | nonzero |
|---|---:|---:|---:|---:|---|
| `turns` | 17 | 383 | 1,170 | 2,636 | 100% |
| `user_turns` | 3 | 22 | 59 | 91 | 100% |
| `tool_calls` | 16 | 279 | 722 | 1,821 | 94% |
| `tool_variety` | 4 | 9 | 12 | 14 | 94% |
| `corrections` | 0 | 1 | 5 | 13 | 62% |
| `code_blocks` | 0 | 2 | 12 | 37 | 71% |

⚠️ **Aperture matters more than the signals.** At the K=8 classification window these
same signals are nearly constant (`turns` p10=7, p50=9, **max=9**; `tool_calls` 3-7;
`corrections` nonzero only 10%). Complexity is a property of a *span of work*, so it
must be measured at session or episode scope. Computing it on the classification
window would produce a feature with no variance.

## Proposed dimension set for interactive sessions

| scope | dimension | mechanism | evidence |
|---|---|---|---|
| session | subject (`domain`) | LLM + EWMA focus | cosine 1.00 (LLM pair) |
| session | function (`function_guess`) | LLM + EWMA focus | cosine 0.99-1.00 (all pairs) |
| session | workstream / themes | verified topic terms + EWMA | substring-gated; pass rate not yet measured |
| session | complexity | deterministic structural signals | 2+ orders of magnitude spread |
| episode | work mode | **untested** | — |
| turn | dialogue act (`speech_act`) | existing facet, right shape | not measured here |
| turn | entities + sensitivity | unchanged, per-prompt | out of scope for this study |

**Not proposed:** `task_type` and `activity_type` for interactive sessions.

## Caveats

- One engineer's corpus. `domain` is 198/200 `software`, so its high agreement is
  partly agreement on an easy majority class. `function_guess` spans 6 values and is
  the stronger result.
- 21 sessions is a small denominator for the argmax percentages; the cosine medians
  are the more robust statistic.
- Only 52 of 464 transcripts yielded a session-scope window. Unexplained, possibly a
  miner bug at unbounded K, and it bounds confidence in the structural-signal spread.
- **Reproducibility is necessary, not sufficient.** Three systems agreeing could in
  principle agree on something wrong. `function_guess`'s 1.00 says it is *measurable*,
  not that the taxonomy is *useful*. Only adjudication speaks to correctness.
- `Focus` alpha = 0.3 was not tuned. Half-life ~2 observations; a different alpha
  would change the argmax percentages, though not the cosine ordering.

---

# Part 2: per-turn capability estimation

Added 2026-08-07. Measured over **1,357 user turns** across 464 transcripts.

## Why this one is on firmer ground than the taxonomy facets

"How much intelligence does this turn need" looks like another classification
problem needing a vocabulary — and the vocabulary-shaped facets above turned out not
to be reliably measurable at all. But effort is not a judgement call: **it is
recorded**. Every user turn is followed by an observable amount of work, and by a
human reaction that reveals whether that work landed.

So this is a **supervised problem with free labels**, validated by held-out
prediction rather than by cross-model agreement. That is a fundamentally better
epistemic position: a wrong answer here is detectable without human adjudication.

`Outcome` (`outcome.go`) reads forward from each user turn to the next and records
`assistant_turns`, `tool_calls`, `code_lines`, `corrected` (the human's next message
pushes back) and `terminal`. Counts and one boolean; no text retained.

## Result 1: the labels are strong

| outcome | p50 | p90 | p99 | max |
|---|---:|---:|---:|---:|
| `assistant_turns` | 4 | 24 | 76 | 155 |
| `tool_calls` | 5 | 38 | 129 | 240 |
| `code_lines` | 0 | 1 | 11 | 23 |

Effort per turn varies by 1-2 orders of magnitude — there is a great deal to predict.
Base rates: **`corrected` 6.9%** (94/1,357), `terminal` 3.8%.

## Result 2: the proposed pre-turn features do NOT predict it

Each pre-turn feature split at its median, outcomes compared across the split:

| pre-turn feature | low: corrected | high: corrected | low: tools/turn | high: tools/turn |
|---|---:|---:|---:|---:|
| `target_chars` | 6.2% | 7.7% | 12.9 | 15.0 |
| session `tool_calls` | 6.8% | 7.1% | 13.4 | 15.0 |
| prior `corrections` | 6.8% | 8.4% | 14.1 | 12.8 |
| session `assistant_chars` | 8.5% | **5.3%** | 13.1 | 14.8 |

Every split is inside noise, and `assistant_chars` runs the wrong way. **No
structural pre-turn feature separates hard turns from easy ones.**

With a 6.9% base rate, "always predict success" scores 93.1%. That is the bar any
predictor must clear, and none of these come close.

### The error this corrects

Part 1 reported that structural signals span 2+ orders of magnitude at session scope
and concluded they were therefore a good routing substrate. **That inference was
invalid.** Spread and predictive power are different properties, and only spread had
been measured. A feature can vary enormously and carry no information about the
outcome — which is exactly what these do.

**Recommendation withdrawn:** do not build per-turn routing on structural signals.
They remain useful for *describing* a session's shape; they do not *predict* per-turn
difficulty.

## Result 3: the deterministic directive detector is too narrow

`hasActionMarker` — imperative verb openings plus explicit go-aheads — fires on only
**12.9%** of user turns. Real prompts in this corpus look like "the CLI CTA that is",
"wire the sidecar into the mirror too", "now do the same for publish": directive in
force, unrecognisable to a lexicon. Detecting whether a turn asks for work needs a
model, or at minimum a far better feature than prefix matching.

## Revised plan

1. **Ship outcome harvesting now.** Deterministic, counts-only, and it accumulates a
   labeled dataset from real usage *before* anyone knows which features work. That
   dataset is the asset; the features are replaceable.
2. **Test semantic features against those labels.** Have the model judge per-turn
   properties plausibly linked to difficulty — is it a directive, how underspecified
   is it, how wide is its scope — then check whether they beat 93.1%. Untested, and
   given `task_type`'s failure the prior should be low, but it is now *falsifiable*.
3. **Gate any router on beating base rate** on held-out turns. Not on plausibility.

## Caveats

- `corrected` has only 94 positives: enough to detect a strong effect, thin for
  training, and it undercounts (a user often abandons rather than pushes back).
- Effort reflects the model that actually did the work (Claude) and this user's
  style, so labels are **model-specific** and would need recalibration per target
  model. For model *recommendation* that is close to the quantity of interest, but it
  is not intrinsic difficulty.
- Median splits detect monotone effects only. A genuinely interacting feature set
  could still work; this rules out these features as single predictors, not all
  feature engineering.
- One engineer's corpus.

## Result 4: semantic judgement — effort is predictable, failure is not

Per-turn `Judgement` (`capability.go`): `directive`, `specificity`, `scope`, `novelty`,
`difficulty`, schema-constrained. Deliberately phrased about the REQUEST, with a test
asserting no task-taxonomy vocabulary leaks into the prompt.

Stratified design: `corrected` fires on 6.9% of turns, so a random sample yields too
few positives. Took 60 corrected + 60 clean turns and compared judgement
distributions — separation is measurable that way; accuracy on a distorted base rate
would not be.

### The 0.6B mode-collapses; the 4B does not

Share of turns taking each value (both arms, all 120 turns):

| field | Qwen3-0.6B | Qwen3-4B |
|---|---|---|
| `difficulty` (trivial/moderate/hard) | 0 / **100** / 0 | 28 / 70 / 2 |
| `specificity` (under/adequate/precise) | 77 / 15 / 8 | 7 / 62 / 32 |
| `scope` (single/multi/open) | **100** / 0 / 0 | 77 / 22 / 2 |
| `novelty` (cont/ext/new) | 7 / 92 / 2 | 23 / 63 / 13 |

100% on one value is mode collapse, not measurement. The 0.6B cannot make graded
per-turn judgements; the 4B uses the range.

### Failure is not predictable (4B)

Corrected vs clean deltas: `difficulty` +0/−2/+2, `specificity` −3/+2/+2, `scope`
+5/+3/−8, `novelty` −2/+2/+0. All inside noise. Only `directive` separates, at
**+12 pts** (80% corrected vs 68% clean), and weakly.

### Effort IS predictable (4B)

Observed tool calls, grouped by the model's PRE-HOC difficulty judgement:

| judged difficulty | n | median tool_calls | mean |
|---|---:|---:|---:|
| `trivial` | 34 | **2** | 8.6 |
| `moderate` | 85 | **7** | 14.5 |
| `hard` | 1 | 15 | 15.0 |

Monotone, with a **3.5x median separation** between trivial and moderate. Because
`difficulty` shows no correlation with `corrected`, the 50/50 stratification is
unlikely to have induced this — the relationship is probably genuine.

### Consequences

1. **Route on predicted effort, not predicted failure.** Effort separates; failure
   does not. Effort is also the more useful routing target.
2. **Capability estimation and session classification have DIFFERENT model
   requirements.** The 0.6B matches the 4B at 1.00/0.99 on `domain`/`function` but
   collapses entirely here. "The cheap model is enough" is true per-dimension, not
   globally. Options: pay for a larger model; run judgement only on directive turns;
   or run it only when a routing decision is actually pending.
3. **The 3-tier difficulty scale is effectively 2 tiers** — `hard` drew n=1. Either
   recalibrate the vocabulary or accept that this corpus lacks hard turns.

### Not established

Descriptive correlation on 120 turns, **not a validated predictor**: no held-out
split, and n = 34/85/1 is thin. The shipping gate stands — beat the 93.1% base rate
on held-out turns before any router goes live.

---

# Part 3: extraction against a hand-authored gold set

Added 2026-08-09. **First measurement in this study with real ground truth** rather
than cross-model agreement — so it can say whether an answer is right, not merely
whether two models concur.

## Why a gold set

The mined corpus is one engineer's coding transcripts: it contains almost no vendor,
billing, campaign or customer-account material, so the business half of the Atlas
gallery cannot be evaluated on it at all. 54 rows were hand-authored against the
real templates in `keld-atlas/services/web/lib/classification-templates.ts`
(mirrored in `gallery.go`), covering `entity`, `structure`, `single_label` and
`multi_label` kinds.

Design choices that make it a test rather than a demo:
- **Negatives on every template where "nothing" is expressible.** Precision matters
  as much as recall; an eval with only positives cannot detect over-firing.
- **Decoys**, e.g. a placeholder `YOUR_KEY_HERE` (must NOT be a credential), a
  version `2.4.1` (must NOT be a ticket id), `HTTP-429` (matches the ticket regex,
  is a status code), `AWS` (a platform we use, not an external customer).
- **The gold set validates itself** — spans must be verbatim substrings, template
  and type names must exist, every entity type must be stated explicitly so
  negatives are deliberate. This caught two authoring bugs of mine before scoring.

## Results

Budget stated by the project owner: **resting <= 3 GB, peak may add ~1 GB.**

| model | F1 | P | R | exact | halluc | resting RSS | peak | verdict |
|---|---:|---:|---:|---:|---:|---:|---:|---|
| Qwen3-0.6B | 0.49 | — | — | 17/54 | 11 | 1,463 MB | 1,520 MB | too weak |
| **Qwen3-1.7B** | **0.84** | 0.83 | 0.86 | **40/54** | **2** | **2,547 MB** | **2,563 MB** | **fits budget** |
| Qwen3-4B | **0.93** | 0.94 | 0.92 | **46/54** | **0** | 5,192 MB | 5,309 MB | over budget (+73%) |

⚠️ The 4B was first scored at F1 0.92 / 45-54 **before** two scoring bugs were fixed
(a case-sensitive list sort and unnormalised separators). Re-run on the corrected
scorer it is **0.93 / 46-54**, so the figures above are same-scorer comparable. The
0.6B row predates the fixes and is mildly understated; it is far enough from the
budget-and-quality frontier that re-running it would not change any conclusion.

### 1.7B vs 4B, per template (same scorer)

| template | 1.7B | 4B | gap |
|---|---:|---:|---|
| `models_mentioned` | 0.75 | **1.00** | **+0.25** — largest |
| `product_area` | 0.67 | **0.83** | +0.16 |
| `deployment` | 0.88 | **1.00** | +0.12 |
| `sensitive_data` | 0.89 | **1.00** | +0.11 |
| `support_topics` | 0.84 | **0.94** | +0.10 |
| `external_vendors` | 0.78 | **0.88** | +0.10 |
| `campaign_brief` | 0.94 | **1.00** | +0.06 |
| `ticket_ids` | 0.86 | 0.89 | +0.03 |
| `external_orgs` | 1.00 | 1.00 | tie |
| `technologies_mentioned` | 0.88 | 0.88 | tie (1.7B better on exact rows, 3/4 vs 2/4) |
| `billable_or_internal` | 0.75 | 0.75 | tie |

**The trade:** the 4B buys +0.09 F1 and 6 exact rows for **2.0x the RAM**, and breaks
the budget by 73%. Its baseline is 4,795 MB before serving a request and it plateaus
flat at 5,309 MB, so tuning cannot rescue it.

**The one gap worth mitigating rather than accepting** is `sensitive_data`
(0.89 vs 1.00) — the privacy-facing template, where a false positive is expensive.
But credentials do not depend on the model at all: `creddetect` already runs the
deterministic gitleaks layer Go-side over the full text. The union of gitleaks and a
1.7B is plausibly better than a 4B alone, and cheaper. Measure that before upsizing.
`models_mentioned` (+0.25) is the 1.7B's clearest real weakness and also the least
governance-critical template in the set.

All CPU-only (`--device none`), `ctx 4096`, `--cache-ram 512`, `parallel 1`,
`b256/ub64`, `enable_thinking:false`. Zero invalid answers on all three.

The 4B's **baseline is 4,795 MB before any request**, so no cache tuning brings it
under 3 GB. The 1.7B's peak is only **+16 MB** over resting, leaving the 1 GB
inference allowance almost untouched and ~450 MB of headroom.

### Qwen3-1.7B per template

| template | kind | P | R | F1 | exact |
|---|---|---:|---:|---:|---|
| `external_orgs` | entity | 1.00 | 1.00 | **1.00** | 5/5 |
| `campaign_brief` | structure | 0.89 | 1.00 | **0.94** | 3/4 |
| `sensitive_data` | entity | 0.80 | 1.00 | 0.89 | 4/5 |
| `deployment` | structure | 0.79 | 1.00 | 0.88 | 4/5 |
| `technologies_mentioned` | entity | 1.00 | 0.78 | 0.88 | 3/4 |
| `ticket_ids` | entity | 1.00 | 0.75 | 0.86 | 3/4 |
| `support_topics` | multi_label | 0.73 | 1.00 | 0.84 | 5/7 |
| `models_mentioned` | entity | 1.00 | 0.60 | 0.75 | 2/4 |
| `billable_or_internal` | single_label | 0.75 | 0.75 | 0.75 | 3/4 |
| `product_area` | single_label | 0.67 | 0.67 | 0.67 | 4/6 |

## Two findings that matter more than the aggregate

**1. The `structure` kind works, and Atlas currently cannot build it.** The gallery
marks `deployment` and `campaign_brief` coming-soon because *"`structure` has no
sidecar preview, so the Lab can't even test it"* — a GLiNER2 limitation, not a
product decision. Both score **0.88-0.94** on a 1.7B inside budget. This is a
capability unlock, not a like-for-like replacement.

**2. The verbatim gate is load-bearing, with receipts.** The 0.6B's 11 hallucinated
spans were the template's OWN EXAMPLES leaking from the task description into the
answer: it reported "Slack connector" for a **Notion** prompt, and "Northwind" and
"Acme Corp" for a **Globex** prompt. Those are exactly the plausible-but-wrong
governance answers that would be impossible to spot downstream. The substring gate
dropped every one. Any extraction backend must keep it.

**Corroboration worth noting:** the two weakest templates — `product_area` 0.67 and
`billable_or_internal` 0.75 — are precisely the two Atlas already flags as needing
**off-prompt context**. The model's weakness there supports the gallery's own
reasoning rather than contradicting it.

## Caveats

- 54 rows is small; per-template denominators are 4-7, so per-template F1 is
  indicative, not precise. The aggregate is the more robust figure.
- **The gold set is mine.** I authored both the examples and the expected answers,
  so it encodes my judgement about what "vendor" or "billable" means. Several 4B
  misses are arguably my gold being debatable (a Stripe webhook as `api` vs
  `billing`; `HTTP-429` as a ticket id). It should be reviewed by someone else
  before it gates a decision.
- Scoring is lenient on span boundaries and separators by design (`Postgres` vs
  `Postgres 16`, `billing-worker` vs `billing worker`), because boundary choice is a
  different problem from finding the right thing. Two scoring bugs were found and
  fixed during this run — a case-sensitive list sort and unnormalised separators.
- Measured on a 20-core desktop; a laptop will be slower but no hungrier.
