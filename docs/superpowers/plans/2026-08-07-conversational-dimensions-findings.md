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

---

# Part 4 — Session digest: verification results

The digest is the second deliverable of this study: a standing report on a session's
work, refined as the session proceeds, for the person doing the work **and** for a
manager who was not present. Part 4 records what it measures at, and — more usefully —
what the act of measuring it revealed about the measurements themselves.

Model: Qwen3-4B-Instruct-2507 Q4_K_M, `ctx 5120`, constrained decoding, temperature 0.
Corpus: 14 real Claude Code sessions of >=16 mined windows each, four refinement steps
per session.

## Results

| Threshold | Result | Want |
|---|---:|---:|
| T1 usable digests | **100.0%** of 56 attempts | 100% |
| T2 unverified identifiers | **1.3%** of 790 | <=2% |
| T3 rubberstamped | **0.0%** of 18 correction-bearing | <=10% |
| T4 retention to final | **92.4%** of 79 | >=90% |
| T7 fabricated blockers | **2.6%** of 38 clean runs | <=10% |
| instruction leakage | **0** | — |

⚠️ **SUPERSEDED BY PART 7 (T4 only).** Every T4 figure in Parts 4–6 was measured with
the previous report embedded whole in the refinement prompt (`CarryForward`) under a
"must not become shorter or less specific" rule. Both are gone. Part 7 measures the
replacement at **50.0% / 56.2%**. The other rows in this table are unaffected by that
change and stand as measured.

**T4 is at its threshold, not above it.** Successive runs scored 89.9% and 92.4%, so
run-to-run movement is a couple of points and 90% is not comfortably cleared. Treat it
as "roughly meets" rather than "passes".

⚠️ "Roughly meets" was the right reading and it was not conservative enough. The
threshold was never comfortably held on this metric at any point in the study, and once
the embedded prose was removed it collapsed rather than drifted (Part 7). Read this
paragraph as the earliest signal of the branch's headline negative result, not as a
qualified pass.

**T5 (blind usefulness) is not measured here** and cannot be self-assessed: it needs a
reader who did not write the prompts. Everything above is a structural or
consistency property, not evidence that the reports are *useful*.

## Memory

`ctx` is the memory lever, and it was measured rather than assumed. CPU-only, after
plateau, `--no-repack -ctk q8_0 -ctv q8_0 --cache-ram 0`:

| ctx | baseline RSS | anonymous |
|---:|---:|---:|
| 4096 | 2855 MB | 371 MB |
| **5120** | **2932 MB** | 448 MB |
| 6144 | 3008 MB | 525 MB |
| 8192 | 3161 MB | 677 MB |

5120 is the largest window inside the 3 GB budget, which is why the prompt is bounded
in characters instead of by widening the context.

## Four metrics that were measuring the wrong thing

This is the part worth carrying forward. In each case a plausible-looking number was
reported for several runs before the flagged items were logged, and in each case the
number was mostly an artifact.

1. **Unverified identifiers measured English.** Any capitalised token absent from the
   source was flagged, so `Key`, `Initial`, `four-screen` and `e.g` all counted — 37
   of 37 flagged tokens were ordinary words behind a reported 22.6%. Requiring a
   strong signal (path, digit, internal caps, extension), or for a weak token
   mid-sentence position, took it to under 1%.

2. **Leak detection measured its own instruction.** Same defect at word grain: the
   vocabulary is the instruction prose, so a digest saying "changed" or "structure"
   was flagged whenever the transcript lacked that stem (~100/sweep). Moving to
   5-word phrases dropped it to 39 — **all 39 being `UnresolvedSentinel`, the string
   the model is told to emit verbatim.** Copying it is compliance. True leakage: 0.

3. **Pluralisation was scored as fabrication.** "KPI" in the transcript against "KPIs"
   in the digest counted as invented; 6 of 22 flagged tokens were that. Folded into
   the metric — but deliberately NOT into `VerifyTopics`, which is the publish gate
   and must stay strict.

4. **T1 hid a quarter of the work.** It counted valid/produced rather than
   succeeded/attempted, and reported **100% while 5 of 20 digests were being dropped**
   to truncated JSON. A dropped digest is worse than a malformed one, not exempt from
   the metric.

The pattern: a bare count gives no way to distinguish a defect from an artifact. Log
the flagged items, always.

## Three fixes that the measurement contradicted

- **`CapSections` was blamed twice for retention loss.** Instrumentation showed all 7
  lost facts disappeared while sections still had **room** under their caps, never at
  a cap. The model was recompressing (`done`: 860 -> 306 runes, taking two specifics
  with it). Handing back the prior digest's named specifics as an explicit retain-list
  fixed it — the same deterministic anchoring that made the counts authoritative.

  ⚠️ **SUPERSEDED BY PART 7.** "Handing back the named specifics fixed it" held while the
  retain-list *reinforced* the embedded prior prose. As the **only** channel carrying the
  previous report's specifics — which is what it became when `CarryForward` was deleted —
  it does not fix it: retention measures **50.0% / 56.2%** against ≥90%, with **161 of 240**
  specifics dropped by the model while explicitly listed under *"each must still appear"*.
  The first sentence (caps are not the cause) is *confirmed* by Part 7; the second (naming
  is the cure) is refuted.

- **Lowering the caps to buy prompt room made things worse.** 500/900 cut truncation
  from 5/20 to 2/20 but dropped retention to 83.3%, because a shorter cap clips
  exactly what the retain-list exists to preserve. Prompt room came instead from
  `CarryForward`, which drops the three sections a refinement rewrites wholesale
  (`current`, `why`, `next`) — ~675 tokens, no report content lost. Two unit tests
  caught a first version that over-trimmed: `unresolved` must carry because its update
  is a diff against the prior list, and `insights` must carry because the prompt
  forbids restating them while the code-side dedup is exact-match only.

  ⚠️ **The measurement stands; the explanation is REFUTED.** 500/900 did drop retention to
  83.3%. But *"because a shorter cap clips exactly what the retain-list exists to
  preserve"* is the pre-registered hypothesis Part 7 tested and refuted: neither retain-list
  cap ever bound on any refinement in either arm (largest list observed 24 of 60 entries /
  280 of 700 runes), and specifics were dropped anyway. Caps are not the mechanism that
  deletes named specifics. `CarryForward` is also gone (Part 7), so the second half of this
  bullet describes code that no longer exists.

- **Retrying does not fix a deterministic failure.** "next is empty" and one
  truncation both reproduced through all 5 attempts on identical input. Prevention
  replaced them: `minLength` in the schema so the grammar cannot emit an empty
  section, and a prompt character budget with the window clipped to its tail. Retries
  were kept for genuine sampling flukes, with validation moved inside the retry loop
  where it can act.

## Open

- **T5 needs a human reader.** Nothing above shows the reports are useful.
- **Sentinel adherence is 18/56 (32%).** Better than the 15% it started at, but the
  model still often writes prose where it should say "nothing is open".
- **The non-technical audience is still unmeasured.** Every session here is
  engineering work from `~/.claude/projects`. Cowork yields 0 readable transcripts on
  VM-backed builds, so the accountant/marketer case this must serve remains untested.
- **Residual T2 items are generic vocabulary** (`CSS`, `URL`, `11-task`) — the model
  adding domain words the transcript never used. Within threshold, but it is the same
  class of behaviour as a fabricated specific.

---

# Part 5 — Does it work for work that isn't code?

The requirement is that the digest serves accountants and marketers as well as
engineers. There was no evidence either way, and the corpus cannot supply it: all 14
project directories under `~/.claude/projects` are code repositories, and Cowork — the
surface non-engineers actually use — yields zero readable transcripts on VM-backed
builds. Observed data cannot answer this question at all.

So two sessions were hand-authored in the real transcript format: a **month-end close**
(bank reconciliation, AR provisions, accruals, depreciation, revenue cutoff; 20 user
turns) and a **product launch campaign** (positioning, channels, a five-email sequence,
measurement; 19 turns). Each contains a genuine reversal.

**What this can and cannot show.** It cannot measure accuracy — the transcripts and the
expectations are both mine, so agreement proves nothing about the real world. It can
find *structural* failure, which is the actual risk: a pipeline that silently presumes
code. `internal/agent/enrich/llmstudy/testdata/nontech/`.

## What held

- **`structure` described a PROCESS, not an architecture.** The finance digest laid out
  the eight-step close checklist and what each step validates; the marketing digest laid
  out the two-track messaging strategy and channel plan. The instruction's "for other
  work, the shape of the process" carried its weight.
- **No engineering vocabulary appeared** in either report — asserted in the test against
  a list (`codebase`, `refactor`, `deploy`, `pull request`, …), not eyeballed.
- **The finance reversal was reported, not smoothed over.** A specific provision was
  proposed against stated policy and corrected; `happened` says so.
- Domain specifics survived accurately: named counterparties, ageing buckets, the
  duplicate posting, newsletter reach and sponsorship costs.

## What broke — and both were the clipper, not the model

> **Followed to its conclusion in Part 8:** the clipper is now **gone**. Every prose cap named
> in this section was deleted, not resized. What this section found — that a constant, not the
> model, was deleting the most recent material from a cumulative section — is the argument for
> removing them; the sizing decision it describes (900 → 1400 for `happened`) is superseded by
> having no cap at all. Part 8 also measures what the removal did *not* buy.

**The cap was manufacturing rubberstamped reports.** `happened` is cumulative AND is
where difficulty belongs, but it shared the 900-rune prose cap and sat at it. Clipping
keeps the OLDEST text, so what got deleted was the most recent material — which is
exactly where a reversal appears. The marketing session's positioning reversal vanished
this way while its opening survived. Giving `happened` its own 1400-rune budget, as
`structure` already had, moved rubberstamp detection from 5.6% to **0.0% of 18**
correction-bearing digests on the real corpus. **The model was not smoothing over
difficulty; a constant was deleting it.**

⚠️ **The 0.0% is superseded; the finding is not.** T3 measures **16.7% of 12 / 8.3% of 12**
in Part 7, on a much smaller denominator and against a ≤10% threshold. The cap *was*
deleting difficulty and giving `happened` its own budget *did* remove that cause — but "T3
is clean" is not a standing result, and Part 7's denominator of 12 is too small to read the
difference between its arms.

**Clipping was not idempotent**, and real digests read `increasing from……`. `done` and
`happened` are carried into the next refinement, so the model reproduces the ellipsis
and the next clip appends another. The first fix was incomplete — stripping the trailing
marker missed one sitting mid-text, which becomes adjacent when the new cut lands just
after it. Both the whole string and the cut point are now cleaned, and clipped-ness
survives re-clipping, so a section that lost content stays marked rather than reading as
complete.

## Still open

- **`happened` is still capped**, so a long session's latest reversal can still fall
  outside it. 1400 moved the boundary; it did not remove it. A cumulative section with a
  fixed budget cannot hold an arbitrarily long session, and the honest fix is probably
  to summarise older difficulty rather than clip it.
- **`current` and `unresolved` went stale** on both synthetic sessions, retaining items
  the conversation had closed and, in one case, asserting no copy existed for a track
  whose landing page had been drafted. The refine prompt tells the model to drop what is
  closed; it does not reliably comply.
- **The synthetic corpus proves structure, not accuracy.** Real non-engineering
  transcripts remain the only way to measure whether these reports are *right*, and
  that needs a readable Cowork path or an in-VM emitter.

---

# Part 6 — Stale open items, and an ablation that refuted its own headline

Part 5 found `unresolved` retaining items the conversation had closed. T7 scored it as
passing, because T7 only catches a blocker with **no basis in the conversation at all**.
The harm is the same one the design already names — a reader acts on it — so two
thresholds were added:

- **T8** counts open items the report itself contradicts: listed as open while `done`
  claims them in place. Checkable without comprehension, which is what makes it usable.
- **T9** counts `current` describing a finished action rather than one underway.

## Result

| | result | want |
|---|---:|---:|
| T1 usable digests | 100.0% of 56 | 100% |
| T2 unverified identifiers | 0.6% of 711 | ≤2% |
| T3 rubberstamped | 0.0% of 18 | ≤10% |
| T4 retention | 94.9% of 79 | ≥90% |
| T7 fabricated blockers | 0.0% of 38 | ≤10% |
| T8 stale open items | 0.0% of 61 | ≤2% |
| T9 current-is-completed | 3.6% of 56 | ≤5% |
| instruction leakage | 0 | — |

⚠️ **Three rows here are superseded, for different reasons.**

- **T4 94.9%** is the last measurement taken under the embedded-prose scheme, and it is the
  *before* half of this branch's headline result: **94.9% → 50.0%** (Part 7). It is not a
  current figure.
- **T3 0.0% of 18** does not hold either: Part 7 measures 16.7% / 8.3% of 12, a *failure* in
  the anchor-on arm. See the note in Part 5.
- **T8 0.0% of 61** and every T8/T10 figure reported before the beat work was measured with
  a defect in `significantWords` (a phantom empty token that under-matched asymmetric
  possessives — 0.714 vs 0.833 on a real pair). Per the implementation ledger these numbers
  **must not be carried forward**; T8 was re-measured in Part 7 (0.0% of 75 / 0.0% of 66) on
  corrected code. The conclusion below that "the prompt change is what fixed staleness"
  rests on the superseded pair and is re-stated there.

On the finance fixture that exposed the defect, `current` now reads "nothing in progress"
and `unresolved` holds the sentinel — correct, since that session completed all eight
checklist steps.

## Enforcement as validation was wrong, and measuring it said so

The first implementation rejected any refinement whose open-item accounting was
incomplete. It produced **T8 0.0% and T1 76.8%**: the model could not satisfy the rule, so
10 of 56 refinements burned all five retries and were discarded. That is the same error as
retrying a deterministic failure, already paid for earlier in this branch. **A dropped
digest is worse than a stale open item.**

Staleness is therefore repaired in code — declared closures applied, self-contradicted
items removed — needing no model cooperation, exactly as insight merging already works.
Validation moved onto the **repaired** digest, because validating the raw response rejected
a legitimate answer that moved every open item into `closed` and then spent five retries on
"unresolved is empty", a state the repair completes by supplying the sentinel.

## The ablation refuted the claim it was built to support

`KELD_DIGEST_NO_CLOSURE=1` was added to measure the repair's effect rather than assert it.
It showed **T8 is 0.0% with the repair disabled as well** (0 of 51). There is no gap, and
the hatch never isolated what it was supposed to: the prompt's accounting block and the
`closed` field remain active when it is set. **The prompt change is what fixed staleness.**
The repair's measurable contribution is delivery instead — **T1 82.1% disabled versus
100% enabled** — because introducing `closed` lets the model legitimately empty
`unresolved`, and without the sentinel substitution those digests are rejected and lost.

Worth stating plainly: an ablation designed to confirm a mechanism instead attributed the
effect elsewhere. Both changes are needed; the reasons are not the ones assumed.

⚠️ **Both T8 numbers in this section (0.0% of 61 and 0 of 51) predate the
`significantWords` fix and must not be carried forward** — see the note under the table.
The comparison is 0.0% against 0.0%, so a systematic under-match affects both arms in the
same direction and the *no-gap* observation survives; what does not survive is treating
either figure as a measured T8 rate. Part 7 re-measured T8 at 0.0% of 75 / 0.0% of 66 on
corrected code, so the conclusion holds — but it holds on Part 7's evidence, not this
section's. The **T1 82.1% vs 100%** figures are unaffected: they count delivery, not
staleness.

⚠️ There is also a second, structural caveat that applies to every ablation in this study,
including this one and the anchor ON/OFF arms in Part 7: an environment flag that gates
more than one thing measures the bundle, not the mechanism. This section caught that case
(the hatch left the prompt block active). Part 7's anchor ablation did **not** — see its
own caveat.

## The recurring error, four times in one session

T9's first detector over-counted by half. Two of four flagged cases were genuine live
states whose completion clause was subordinate — "the running API service is currently
blocked from loading" and "a pull request is open for review". This is the same failure as
unverified identifiers reading 22.6%, leakage reading ~100 per sweep, and plurals counting
as fabrications: **a phrase's presence is not a statement's meaning.** Every one of these
was found by logging the flagged items; none was visible in the count.

---

# Part 7 — Session story rollup: the beats work, fact retention does not

The design in `docs/superpowers/specs/2026-08-10-session-story-rollup-design.md` replaced the
refine loop's embedded prior report with three inputs split by truth-status: a **measured
session record**, a **beat series** written every few turns from the transcript, and the
**recent window**. `CarryForward` is gone, so no model output is ever input to a later
generation of the same kind. The no-shrink rule went with it.

This is the measurement of that design running end to end.

## The headline, in one line

The design removed the *"must not become shorter or less specific"* rule on the theory that
cheap transcript-derived beats plus a deterministic retain-list would substitute for embedding
the previous report's prose. **Measured: they do not.**

> **Fact retention: 94.9% → 50.0%.**
> (94.9% of 79, Part 6, embedded prose + no-shrink rule → 50.0% of 80 with the recency anchor
> on, 56.2% off, against a ≥90% threshold. Split by kind it is worse on the population that
> matters: real identifiers 38.6%, bare capitalised words 63.9%.)

> ⚠️ **AMENDED by Part 8 — the magnitude, not the failure.** Re-measured on the current tree
> with everything about the caps left in place, T4 is **58.2% (ON) / 62.0% (OFF) of 79**. The
> difference is the beat work that landed after this sweep (cadence 3 → 5 user turns,
> `ChangedSubject` grounded, the progress and forbidden-opener rules), not anything about
> retention machinery. So the headline is **94.9% → 58.2%** on the code as it now stands; it
> fails ≥90% either way and every conclusion below is unaffected. Part 8 also **refutes a second
> candidate mechanism**: prose clipping. It is now removed, and retention did not move by one
> item in either arm.

**The deliverable of this branch is a negative result.** The substitute for the no-shrink rule
does not work as built, and it fails for a reason the plan explicitly did not predict — not
the retain-list's caps (neither ever bound) but the model dropping specifics it was handed
under an instruction not to, compounded by a retain-list derived from the last report only, so
every drop is permanent. It should merge **as** that result: the measurement is clean
(temperature 0, both arms run twice, every figure reproduced exactly), the prompt-budget work
underneath it is sound and tightly pinned, and the next experiment it points at — a cumulative
retain-list — is a design change that was deliberately not landed unmeasured.

Three thresholds fail. A fourth (T11) reports a passing number that is **not a measurement**;
see its section.

## Method

14 stratified sessions, this branch's own development transcript first. Four reports per
session at windows 4/8/12/15 — the same spacing every earlier configuration used, so only the
*inputs* changed. A beat every 3 user turns. Qwen3-4B-Instruct-2507 Q4_K_M, `ctx` 8192,
temperature 0, prompt budget 14,000 runes.

Two arms, because the design makes a prediction about the recency anchor:

- **anchor ON** — the `SubjectShifted` stand-in decides. It fired on 41 of 42 refinements, so
  this arm is *anchor-always*, not the gated design. That remains unmeasurable without the
  EWMA focus the digest path does not compute.
- **anchor OFF** — no anchor on any refinement. This is the arm the prediction is about.

The two arms were each run twice. Every figure below reproduced **exactly** across the
independent pairs, so nothing here is sampling noise.

⚠️ **The ON/OFF ablation is CONFOUNDED, and no magnitude below may be attributed to the
anchor.** The harness derived one `reason` value and gave it to two consumers: the anchor
(`RefineInput.Why`) and the measured record's turning-point list
(`SessionRecord.NoteTurningPoint`). `NoteTurningPoint` keeps only `focus_shift`/`friction`, so
because the arm switch gated that single value, the **OFF arm ran with an empty
`TurningPoints` list for its entire duration** while the ON arm fired on ~41 of 42 steps — a
~220-rune difference in the `SESSION RECORD (measured — authoritative)` block on *every step*.
The confound is **aligned** with the anchor, so it inflates the anchor's apparent cost.

What survives: the **direction** (every difference favoured OFF, and the anchor's own
pre-rollup measurement — 97.4% → 88.3% retention, 4.1% → 10.2% fabricated blockers — carried
no such confound), and therefore the conclusion that the anchor stays gated. What does not
survive: *"the anchor costs 6 points of T4"*, and any other per-metric attribution across the
arms. The harness is fixed (`reason` is now computed independently of the arm and only
`RefineInput.Why` is gated, so both arms see identical records); **the corrected comparison is
UNMEASURED** and the sweep was deliberately not re-run for a comment-and-doc fix wave.

Note the aggregate figures are unaffected by this: T4 at 50.0% and 56.2% both fail ≥90%, and
the T4 diagnosis below (161 of 240 named-and-dropped, 0 cap evictions) is identical in the two
arms.

## Results

| | anchor ON | anchor OFF | want |
|---|---:|---:|---:|
| T1 usable digests | 100.0% of 56 | 100.0% of 56 | 100% |
| T2 unverified identifiers | 1.0% of 719 | 0.6% of 779 | ≤2% |
| T3 rubberstamped | **16.7% of 12** | 8.3% of 12 | ≤10% |
| **T4 retention to final** | **50.0% of 80** | **56.2% of 80** | **≥90%** |
| — identifier-shaped specifics | **38.6% of 44** | **47.7% of 44** | — |
| — bare capitalised words | 63.9% of 36 | 66.7% of 36 | — |
| T7 fabricated blockers | 4.5% of 44 | 2.3% of 44 | ≤10% |
| T8 stale open items | 0.0% of 75 | 0.0% of 66 | ≤2% |
| T9 current-is-completed | 0.0% of 56 | 1.8% of 56 | ≤5% |
| T10 synopsis restates | 0.0% of 56 | 0.0% of 56 | ≤5% |
| T11 synopsis lags | 0.0% of 25 judged † | 0.0% of 29 judged † | ≤10% |
| T12 beat-vs-record | **15.7% of 70 checked** | **15.7% of 70 checked** | ≤5% |
| T13 fabricated `next` | 1.4% of 73 | 0.0% of 73 | ≤5% |
| instruction leakage | 0 | 0 | — |
| recovered panics | **0** | **0** | 0 |

† **T11's 0.0% is NOT a pass.** The check cannot detect the failure it scores — two ordinary
English words shared between a synopsis and an unrelated window certify the synopsis current.
Read it as *not established*, alongside T12, which shares its root cause. See its section.

Three thresholds fail: T4 badly and in both arms, T12 for a reason that turns out not to be
about beats at all, and T3 on a denominator of 12 where a single item moves the rate 8 points.
T11 is a fourth failure of a different kind — the instrument, not the model.

## T4 is the design's failure, and it is not the retain-list cap

> **Nor the prose clip, measured in Part 8.** `CapSections`' character-level truncation of
> sections is now gone entirely, and T4 is unchanged **to the item** in both arms — the same
> lost-fact lists, not merely the same rate. Both storage-side explanations for the retention
> failure are therefore refuted by measurement, and what this section says about the mechanism
> is what is left. The rates quoted below are this sweep's; see the amendment under the headline
> for the current-tree figures.

**94.9% → 50.0%.** Half the named specifics injected at the first report are gone from the
final one, against 94.9% of 79 under the embedded-prose scheme this design replaced (Part 6).
The plan's own pre-registered explanation was that the retain-list, no longer reinforced by
embedded prose, needed a bigger cap. **That is refuted by measurement.** Over 240
(refinement × fact) pairs:

| | ON | OFF |
|---|---:|---:|
| fact was named in the retain-list the prompt carried | 161 | 161 |
| fact was dropped by `boundRetainList`'s cap | **0** | **0** |
| fact had already fallen out of the prior report, so no channel could carry it | 79 | 79 |
| largest retain-list observed | 24 entries / 280 runes | 32 / 336 |
| caps | 60 entries / 700 runes | 60 / 700 |

Neither cap ever bound, on any refinement, in either arm. Raising `retainListMaxCount` or
`retainListMaxTotal` cannot move T4 by one point.

The mechanism is instead a **one-way cascade**. The retain-list is re-derived from the
*previous report* (`Identifiers(prev)`), so the moment the model drops a specific, the next
refinement's retain-list can no longer name it, and the loss is permanent. Session 6 shows the
shape exactly: `agentcfg.Info`, `SetSidecarPort`, `daemon.go` and `metrics.go` were all named
in the retain-list at the first refinement, dropped anyway, and then reported as
`RETAIN-ALREADY-GONE` at both later steps. 161 of 240 pairs had the fact explicitly named
under the instruction *"each must still appear, unless the new part shows it was wrong"* — so
the primary failure is the model disobeying an explicit, deterministic instruction, and the
retain-list's derivation from the last report rather than from the union of all reports is what
makes each disobedience irreversible.

**The split makes it worse, not better.** Because `Identifiers()` is position-aware over prose,
its output legitimately mixes real specifics with bare capitalised English. Splitting T4 by the
code's own `strongIdentifier` rule was expected to show the aggregate overstating the loss.
It shows the opposite: identifier-shaped specifics survive at **38.6%** while bare capitalised
words survive at **63.9%**. The 50% aggregate *understates* the failure on exactly the
population T4 exists to protect. The expectation was recorded before the split was measured and
it was wrong in the flattering direction — one more instance of the pattern this study keeps
hitting, where the composition of a rate is only visible in its items.

## T11: NOT ESTABLISHED — the check cannot detect what it scores

An earlier version of this section was headed *"the prediction held"* and drew a live action
item from T11's 0.0%. **That is withdrawn.** T11 is moved into the same *not established*
status as T12, because it has the **same root cause in the same function**, and Part 7
diagnosed that function for T12 while leaving the reassuring small number unaudited. That is
the study's own recurring error — a rate is only as trustworthy as its items — appearing one
more time, in the flattering direction, in a document that names the error four times.

**The defect.** `SynopsisLag` compares `distinctiveTerms(synopsis)` against
`distinctiveTerms(recent window)` and reports lag only when the intersection is **empty**. Its
docstring claimed comparison is *"on DISTINCTIVE terms only, never bare word overlap"*. It is
not: `distinctiveToken`'s unconditional 7-character rule admits ordinary English. Reproduced
directly — a synopsis entirely about a ledger reconciliation, measured against a recent window
entirely about dropdown opacity, returns `recentHits=2`, on the words **"remains"** and
**"whether"**. Two ordinary English words shared between any two passages certify a synopsis as
current. So **0.0% is close to a tautology of the tokeniser, not a floor.**

**What is therefore unmeasured.** The design's prediction — that lag improves *without* the
anchor because framing is no longer pinned by verbatim prose — is neither confirmed nor
refuted, and it cannot be until the tokeniser is fixed. Nor is the *opposite* excluded: a run
producing lagging synopses in both arms would report the same 0.0%.

**The action item survives, on different evidence.** *Leave the anchor gated; there is no case
for enabling it.* That now rests on T4 (worse with the anchor in both the pre-rollup
measurement, 97.4% → 88.3%, and here), T3 and T7 (both higher with it here) — **not** on T11,
whose justification is withdrawn. Note the ON/OFF magnitudes here are confounded (see Method),
so the argument leans on the direction and on the unconfounded pre-rollup anchor experiment.

**Two denominator corrections, both real.**

- `SynopsisLag` had the same abstention defect the T12 denominator was corrected for: it returns
  `lag=false` both when it judges a synopsis current and when it *abstains* for want of opening
  evidence (`earlyHits < minLagEvidence`), so a rate over all refinements counts every
  abstention as a pass. Corrected here: **17 of 42 abstained (ON), 13 of 42 (OFF)** — 40% and
  31% of the sample carries no verdict. The rate is 0.0% of the 25 and 29 actually judged.
  Every previously published lag figure in this study shares the uncorrected denominator.
- The old **14.3%** is not a baseline: it predates the trimming fix and was measured while the
  untrimmed-token bug made `SynopsisLag` abstain on synopses that *were* lagging.

**Partially mitigated, and explicitly not fixed.** `digestStopWords` is capitalised-keyed (it
was built for `Identifiers`, which only offers capitalised tokens) and `distinctiveToken` looked
it up case-sensitively, so roughly half the list was dead on the one path that admits lowercase
words: `distinctiveToken("Currently")` was false while `("currently")` was true, and likewise
for however/although/completed/reconciled/because/without/several. That is now
case-insensitive — the mechanical root of both this and T12 — and it changed **no** committed
test's term set, so nothing was silently re-baselined. It does **not** close the hole: neither
"remains" nor "whether" is in any stopword list at any casing, and a test asserts that so the
mitigation cannot be mistaken for a fix. A real distinctiveness rule is a design change that
would re-open T11 and T12 for measurement.

## The beats are the design's success

> ⚠️ **This table is superseded by the beat work that landed after this sweep** (cadence 3 → 5
> user turns, and the three beat fixes). Re-measured in Part 8's runs, identically in all four
> arms: **42 asked / 40 generated / 40 kept**, 2 generation failures ("holds no complete sentence
> within 512 runes", both after 5 retries), **0 discarded**, **36 of 40 marked subject-changed
> (90.0%)**, consecutive-beat overlap mean 0.134 / max 0.268 over 26 pairs, and refinements
> carrying **1.9** beats on average. The two conclusions below survive: nothing is discarded and
> the comparator is nowhere near firing, and `ChangedSubject` is still degenerate at 90%. Two
> beats are now *lost* per run, which the 3-turn cadence did not produce.

| | ON | OFF |
|---|---:|---:|
| beats asked / generated / kept | 70 / 70 / 70 | 70 / 70 / 70 |
| discarded as restatement | **0 (0.0%)** | **0 (0.0%)** |
| marked "subject changed" | **70 (100%)** | **70 (100%)** |
| consecutive-beat overlap ratio, mean / max | 0.256 / 0.500 | 0.250 / 0.500 |
| `beatsRestate` discard threshold | 0.80 | 0.80 |

Nothing was discarded, and the cadence is **not** the reason. The largest overlap any real
consecutive pair produced was 0.500 against a 0.800 threshold — the comparator was never close
to firing. At a 3-user-turn cadence on real sessions, consecutive beats genuinely differ, so the
"beats that say nothing are not stored" mechanism is **inert on this corpus** rather than
mis-tuned. It cost 70 generations to discard nothing; that is cheap, but it is also unexercised
machinery, and its behaviour on a run of acknowledgement turns is **not established** by this
run.

The series itself reads as the design claimed. Session 1, verbatim:

```
[1] evaluating and improving the classification of multi-turn conversation transcripts…
[2] designing a study to evaluate whether a prompted LLM performs better than production…
[3] comparing memory usage and latency of different LLM configurations…
[4] evaluating the feasibility of running smaller, CPU-only LLMs within a strict RAM budget…
[5] determining the memory and performance footprint of a 0.6B Qwen3 model CPU-only…
```

That is an accurate trajectory of a session the author lived. Whether it is a *better* answer to
"what happened over the session" than a single report is the design's claim, and it remains a
judgement — the same unmeasured usefulness question every part of this study has left open. What
is measured is that the series is accurate, cheap, and free of any chain: each beat was written
from its own window and no beat read another.

**But `ChangedSubject` is degenerate, and demonstrably wrong.** Marked on 100% of beats, it
carries no information: `SelectBeats`' preference for subject-changing beats degenerates to
"prefer everything". Beats [4] and [5] above are plainly the same subject; the comparator that
cannot see a restatement also cannot see continuity, because both use the same 0.8 test. This is
the same failure as `SubjectShifted` firing on 41 of 42 refinements, now confirmed on a second,
independent mechanism: **thin token overlap does not distinguish a change of subject from a
rewording of one.** The design's claim that beats "unblock" the turning-point signal is
**not established** — the signal exists but is stuck at 1.

Selection was never exercised either. With a 16-window prefix and a 3-turn cadence only 5 beats
accumulate against a cap of 12, so the sampling rule (first + subject changes + recent + even
spacing) never had to choose. Refinements carried 4.0 beats on average, all of them, and
`fitDiscretionary` never shrank a selection.

## T12 became measurable, and immediately measured the wrong thing

T12 (`BeatContradictsRecord`) printed `UNMEASURED` before this task because nothing built a
record or generated beats. It now runs: 70 beats checked, 0 abstentions, **15.7% flagged**
against a ≤5% threshold.

All 11 flagged beats are **accurate**. Two, in full:

- *"…ensuring visual consistency in UI components by aligning the opacity and background
  styling of floating dropdown menus with established design patterns…"* — flagged against a
  record whose subjects were `README.md, service.name, confirm, acme.test, actor_name_short`.
- *"…finalizing and verifying the password reset flow in the authentication system…"* — flagged
  against `OAuth, /dev/null, buttons, confirm, provider, KELD_OAUTH_, TaskUpdate`.

Two independent defects produce this, neither of them about beats:

1. **`distinctiveTerms` admits ordinary English.** `distinctiveToken`'s unconditional 7-character
   rule passes `aligning`, `ensuring`, `designing`, `specifically`, `consistent`,
   `correctly` — so a beat's "subject terms" are mostly gerunds and adverbs that no record
   could ever contain.
2. **`SessionRecord.Subjects` is dominated by tool names and workflow vocabulary.** Measured
   subject lists contain `WebSearch`, `WebFetch`, `TaskUpdate`, `SendMessage`, `DONE`,
   `Awaiting`, `dispatching`, `re-review`, `existing`, `confirm`, `running`, `because`,
   `changes`, `numbers`. `Observe` reads the whole delta including tool lines and assistant
   prose, then frequency-ranks, and agentic sessions are overwhelmingly tool traffic.

The two never intersect, so "no grounded term" fires on correct beats. **T12's 15.7% is a
measure of vocabulary mismatch between two extractors, not of beat accuracy**, and whether it
would catch a *genuine* contradiction is **unmeasured** — no genuine one occurred. The
threshold is not usable until the record holds subjects rather than tool names. That makes it the
fifth metric in this study to report a large number that was mostly ordinary English — after
unverified identifiers at 22.6%, leakage at ~100 per sweep, plurals counted as fabrications, and
T9's first detector.

**And T11 is the sixth, running the other way.** Defect (1) above is in
`distinctiveTerms`, which also backs `SynopsisLag` — so the same ordinary-English admission that
produced T12's large *wrong* number produced T11's small *reassuring* one, by making a match
easy to find rather than hard. Diagnosing it here and not there is the single most instructive
mistake in this document: the number that needed auditing was the one that looked fine.

## What did not come back down: the prompt budget or `ctx`

The design expected both to fall, since the carried report drops from ~4,700 runes to the
retain-list. Measured, they did not — and the budget went *up*, 11,000 → 14,000, during the
prompt-integrity work that had to land first.

| | measured |
|---|---:|
| largest assembled prompt, sweep | 13,997 of 14,000 (ON), 13,982 (OFF) |
| largest assembled prompt, full corpus probe | 14,000 of 14,000 |
| tightest window margin over the 1,600 floor, sweep | +3,019 (ON), +3,244 (OFF) |
| tightest window margin, full corpus probe | **+20**, over 1,087 steps / 2,174 prompts |
| refine instructional tail | 5,673 runes (41% of the prompt) |

The sweep alone would have licensed a smaller budget — its tightest step left the window 4,619
runes, so ~11,100 would have sufficed. The **corpus probe refutes that**: across 29 sessions the
tightest real margin is +20 runes at the full 14,000. Removing `CarryForward` did free
3,200–4,400 runes, but the instructional tail and the enforced 1,600-rune window floor consume
more than it returned. `ctx` 8192 has roughly 170 tokens of worst-case headroom once a
schema-legal nine-section report is allowed for, so it cannot come down either.

Both figures are **prompt-side**; no `n_predict` bound exists, and the output side is what
would need to move first.

## Two harness defects found while wiring this

**`Observe` cannot be fed `Mine`'s windows.** `SessionRecord.Observe` sums what it is given, and
`Mine` returns one window per user prompt carrying up to K=12 context turns, so consecutive
windows overlap by ~11. Folding them straight in produces counts wrong in *both* directions —
measured over a 16-window prefix, `user_turns` inflated 2.4× (38 vs 16) while `tool_calls`
*deflated* 2.6× (117 vs 299), because K=12 and the 12,000-rune window cap truncate any long
agentic gap between two user prompts. Those counts are labelled "measured — authoritative" in
the prompt and `digestRules` tells the model corrections are a fact it must be consistent with,
so the error is not cosmetic: it biases T3 in the flattering direction. The sweep now folds
disjoint per-user-prompt deltas (`sessionDeltas`), which reproduce `user_turns` exactly.

**`WithFocus` is deliberately never called.** The EWMA focus needs the classification pipeline
the digest path does not run, so writing a plausible focus into an authoritative block would be
the fabricated-record failure this plan was already corrected for once. `Populated()` reports
`[counts projects subjects turning_points]` and omits focus, which is what it exists to do — the
spine is honestly partial, and the design's own risk note about model-dependent fields is
realized rather than avoided.

## The pattern is systemic: a green test suite cannot detect this class of defect

Four sections of this document describe *a metric that measured ordinary English*. There is a
second, structurally identical pattern running through the branch, and it has now been found
**at least ten times across two independent audits** — more than the five the earlier text
implied. Anyone inheriting this suite needs to know it by name:

> **A test certifying a bound the code does not enforce.** The test passes. Its docstring
> states a measured before/after. And removing the mechanism it names changes nothing.

Confirmed instances, each verified by removing the mechanism and observing a green suite:

| # | site | how it was vacuous |
|---|---|---|
| 1–5 | the prompt-budget work (task 7b, rounds 1–4) | recorded in the implementation ledger |
| 6 | `TestCreatePathAccountsForItsOwnHeadersBeforeComputingRoom` | decalibrated by the 11,000→14,000 budget raise; reverting gave 4554 **both ways** |
| 7 | `frictionWords`' `"revers"` stem | the fixture's word "correction" matched the pre-existing `"correct"` entry and short-circuited |
| 8 | `clipProse`'s word-boundary branch | asserted the absence of a literal the naive cut never produces |
| 9 | the whole `THE LATEST TURNS ARE ABOUT:` block | the test passed `TriggerNone`, and its `Contains(p, "DigestSchema")` was satisfied by the raw window |
| 10 | `arms.go`'s latency assignment | the test asserted `< 0`, the exact trap a comment two lines above it names |
| 11 | `digest_consistency_test.go`'s "all three punctuation shapes" | `,` and `)` are `subjectTokens` separators; one of the three cases did all the work |
| 12 | `digeststore`'s `SetMaxOpenConns(1)` credit | `busy_timeout` alone is sufficient; the test pins the conjunction and can attribute nothing |

**Three lessons, all paid for.**

1. **A green `-count=1` run is not evidence.** Only *revert-and-fail* detects this class. Every
   repair in this wave was verified that way, and the verification is recorded in the docstring
   next to the number, not only in a commit message.
2. **Raising a global budget silently disarms every test calibrated against it.** Three of the
   instances above are that one cause. The fix is to size fixtures **from the live constants**
   and to assert the *regime* as a `t.Fatal` precondition, so a future change fails loudly
   instead of quietly widening the margin past where the bug lives.
3. **The reassuring number is the one nobody audits.** T12's 15.7% got a root-cause diagnosis;
   T11's 0.0%, produced by the same defect in the same function, got a section headed "the
   prediction held". Large surprising numbers attract scrutiny automatically. Small confirming
   ones need it scheduled.

## Not established

- **Usefulness.** Unchanged: every threshold here is structural or consistency-based.
- **The gated anchor.** `SubjectShifted` fired on 41 of 42 refinements. What was measured is
  anchor-always versus anchor-never.
- **The anchor's cost, per metric.** The ON/OFF ablation was confounded by
  `NoteTurningPoint` sharing the arm switch (see Method). Direction only; the corrected
  comparison is unmeasured.
- **Turning-point detection from beats.** `ChangedSubject` is 100% and wrong on at least one
  demonstrable pair.
- **Restatement suppression.** 0 discards, comparator never within 0.3 of firing.
- **Beat selection at the cap.** Only 5 beats per session against a cap of 12.
- **T11 as a currency check.** Its 0.0% is near-tautological: `distinctiveToken` admits ordinary
  English, so two shared English words certify a synopsis current.
- **T12 as a consistency check.** No genuine contradiction occurred; its 15.7% is extractor
  mismatch — the same root cause as T11.
- **`TriggerPolicy`, including the wall-clock floor and reason deferral.** No non-test caller
  exists anywhere in the repository; the sweep computes its reason from `SubjectShifted`
  directly and never calls `ShouldRefresh`. Unit-tested, not measured.
- **Non-engineering accuracy.** Cowork is still VM-backed and yields no readable transcripts.
- **Long sessions.** Every measurement is a 16-window prefix, 4 reports, ≤5 beats.

## What to do next, in order

1. **A cumulative retain-list** (a union over all prior reports, not just the last). It is the
   only change the T4 diagnosis actually points at: 161 of 240 drops were already named, and
   79 were unnameable *because* the list is re-derived from the last report. Not landed here,
   deliberately, because it is a design change that must be measured, not asserted.
2. **A real distinctiveness rule** for `distinctiveToken`, not a longer stopword list. It gates
   T11 and T12 both, and until it lands neither threshold means anything.
3. **`SessionRecord.Subjects` must hold subjects, not tool names.** `Observe` reads the whole
   delta including tool lines and frequency-ranks it, and agentic sessions are overwhelmingly
   tool traffic. This is the other half of T12.
4. ~~**Re-run the ablation** on the de-confounded harness before quoting any anchor magnitude.~~
   **DONE in Part 8** — all four of its runs use the de-confounded harness. T4 is 58.2% (ON) vs
   62.0% (OFF): the same direction as before, unconfounded, and a 3-fact difference on 79. ON is
   still *anchor-always*, so the gated design remains unmeasured.
5. **`maxSubjectTermLen`** drops every `docs/` path (longest tracked path 83 runes vs a 64-rune
   cap) — a second mechanism deleting named specifics. Raising it is not free: measured at 96,
   the create-path worst case breaches the 1,600-rune window floor. It needs budget headroom
   the corpus probe says does not exist — and Part 8 measures the real-corpus window margin at
   **+0**, down from +20, so there is now none at all.

---

# Part 8 — Untruncating the reports: the clipper was doing nothing to retention

`CapSections` clipped all seven prose sections of every refinement's output at rune counts and
appended an ellipsis (synopsis 650, `done`/`current`/`why`/`next` 900, `happened` 1400,
`structure` 1600). It is **removed** — enforcement and constants both. Moving the length
statement into the prompt as guidance was the intended companion change; it was measured on two
further pairs of sweeps and **reverted**, because it costs thresholds that were passing (see its
section below).

The instruction was a product judgement ("we shouldn't be truncating the reports… 2-3 sentences
is a guide, it's stupid to just lop off words at the end"). The measurement question was
different and specific: Part 7 left fact retention as the design's headline failure, and one
hypothesis was that the clip contributed — the retain-list is derived from `Identifiers(prev)`,
so a specific clipped off the end of a section was never in the retain-list and could never be
carried forward.

**Measured: it contributed nothing.** Retention is unchanged, to the item, in both arms.

## The caps were vestigial, and that was traced rather than assumed

`CapSections` was introduced when the previous report was embedded verbatim in the next prompt
(`CarryForward`, deleted in Part 7's redesign), so a section's length *was* prompt length. After
that redesign nothing embeds prior prose. Every consumer was checked:

- `CapSections` has **one** production caller — `RefineFrom`, on its return value.
- **No prompt builder reads a `Digest`'s prose.** `DigestUpdatePromptFrom` is the only prompt
  taking a `prev Digest` and it reads it through exactly two channels, each bounded by its own
  constants: `Identifiers(prev)` via `boundRetainList` (60 entries / 700 runes) and the
  open-item accounting via `priorOpenItems` (`DefaultListCap` 12 × `promptOpenItemCap` 80).
- `BeatPrompt` takes record and window strings; `beat.go`, `beat_series.go` and
  `session_record.go` do not reference `Digest` at all. The create path takes no digest.
- `DigestJSON`, whose doc still read *"renders a digest for embedding in a refine prompt"*, has
  **no callers**. It was the last trace of the embedding scheme.

Pinned, not argued: `TestRefinePromptIsInsensitiveToStoredProseLength` assembles the real refine
prompt from a prior report with 7,250 runes of identifier-dense prose and again with 72,500, and
gets the same prompt **to the rune** — 13,973 of 14,000, window content 1,788 over the
1,600 floor, retain-list 598 of 700 (from 25,219 and 97,719 runes offered). Ten times the prose,
zero difference, because the bound is on the channel and not on the section.

## Results: every threshold, before and after, both arms

Same 14 stratified sessions, this branch's own transcript first; reports at windows 4/8/12/15;
a beat every 5 user turns; Qwen3-4B-Instruct-2507 Q4_K_M, `ctx` 8192, temperature 0, budget
14,000 runes. The only difference between the *before* and *after* columns is the removal of
prose clipping — same harness binary shape, same corpus, same arm definitions.

| | ON before | ON after | OFF before | OFF after | want |
|---|---:|---:|---:|---:|---:|
| T1 usable digests | 100.0% of 56 | 100.0% of 56 | 100.0% of 56 | 100.0% of 56 | 100% |
| T2 unverified identifiers | 0.9% of 762 | 0.9% of 762 | 2.3% of 769 | 2.3% of 770 | ≤2% |
| T3 rubberstamped | 0.0% of 12 | 0.0% of 12 | **16.7% of 12** | **0.0% of 12** | ≤10% |
| **T4 retention to final** | **58.2% of 79** | **58.2% of 79** | **62.0% of 79** | **62.0% of 79** | **≥90%** |
| — identifier-shaped specifics | 47.6% of 42 | 47.6% of 42 | 50.0% of 42 | 50.0% of 42 | — |
| — bare capitalised words | 70.3% of 37 | 70.3% of 37 | 75.7% of 37 | 75.7% of 37 | — |
| T7 fabricated blockers | 4.5% of 44 | 4.5% of 44 | 4.5% of 44 | 4.5% of 44 | ≤10% |
| T8 stale open items | 0.0% of 74 | 0.0% of 74 | 0.0% of 74 | 0.0% of 74 | ≤2% |
| T9 current-is-completed | 1.8% of 56 | 1.8% of 56 | 1.8% of 56 | 1.8% of 56 | ≤5% |
| T10 synopsis restates | 0.0% of 56 | 0.0% of 56 | 0.0% of 56 | 0.0% of 56 | ≤5% |
| T11 synopsis lags † | 0.0% of 30 judged | 0.0% of 30 judged | 2.9% of 35 judged | **5.7% of 35 judged** | ≤10% |
| T12 beat-vs-record † | 25.0% of 40 | 25.0% of 40 | 25.0% of 40 | 25.0% of 40 | ≤5% |
| T13 fabricated `next` | 3.3% of 90 | 3.3% of 90 | 6.5% of 92 | 6.5% of 92 | ≤5% |
| instruction leakage | 0 | 0 | 0 | 0 | — |
| recovered panics | **0** | **0** | **0** | **0** | 0 |
| retain-list: named / cap-evicted / already gone | 164 / **0** / 73 | 164 / **0** / 73 | 168 / **0** / 69 | 167 / **0** / 70 | — |
| largest retain-list | 30 / 292 runes | 30 / 292 | 29 / 278 | 29 / 278 | 60 / 700 |
| largest prompt | 13,929 | 13,929 | 13,992 | 13,992 | ≤14,000 |

† T11 and T12 remain **not established** for the reasons Part 7 gives (`distinctiveToken` admits
ordinary English). Their numbers move here; that does not make them measurements.

**The ON arm is byte-identical apart from length.** Diffing the two ON logs line by line yields
**10 changed lines out of 217**: three `FINAL REPORT RUNES` lines, the summary length line, and
the wall-clock. Every beat, every flagged item, every rate is the same text. At temperature 0
with an identical prompt the generation is identical, and the prompt is identical because it
never contained the prose — so the clip's entire observable effect was on the stored text.

**T4's lost facts are the same items, not just the same rate.** ON, both before and after:

```
s1 [CPU-based GPU Claude JSONL]      s8  [Activity]
s2 [Downloads]                        s10 [telemetry.py KPI]
s3 [Overview Spend Observability]     s11 [custom.go enrichments.custom.product_name.entities]
s4 [PID signal-client-telemetry.md]   s12 [pass-test-drawer.tsx PassRow classifications-catalog-view.tsx]
s5 [CTAs Dimension]                   s13 [Atlas Classify Integrations]
s6 [agentcfg.Info SetSidecarPort daemon.go metrics.go]
s14 [organizations.slug slug.py ensure_unique_slug auth.py orgs.py Acme]
```

Not one of those specifics was lost to a clip. They were dropped from the middle of a report the
model rewrote, which is what Part 7's diagnosis already said (161 of 240 pairs named in the
retain-list and dropped anyway; 0 cap evictions, reproduced here as 0 again in all four arms).

## The one movement, and it is one session

The OFF arm diverged on **session 13 only**, and the divergence starts at step 2 — so an earlier
step's clipped output changed the next prompt (through the retain-list) and the generation
cascaded from there. Three metrics move, all on that one session:

- **T3 16.7% → 0.0% of 12.** The two flagged reports were both session 13 (steps 2 and 3), the
  same report re-flagged twice. Verbatim, the step-2 flag that disappeared: *"The initial focus
  was on aligning text in enrichment schema cards to the top, but the primary work shifted to
  improving the visual hierarchy and consistency of the lead-in text. A first attempt to fix the
  lead-in styling replaced an outdated `COLUMN_HEADER_CLA…"*. Session 13's `happened` grew from
  754 to 1,046 runes across the change. This is **consistent with** Part 6's finding that the
  clipper was deleting the account of difficulty, and it is **not established as that**: the
  whole step-2 report differs, not only a clipped tail, and `LooksRubberstamped` searches every
  prose field, so any friction word anywhere flips it. On a denominator of 12, one session's two
  flags are 16.7 points.
- **T11 2.9% → 5.7% of 35 judged.** One new flag, also session 13 step 3, and T11 is not a
  measurement (see Part 7).
- **T2 769 → 770 identifiers**, same 2.3%. One more identifier in the corpus, same flagged set.

Nothing else moved anywhere: T4, T7, T8, T9, T10, T12, T13, leakage, panics and every cap
statistic are identical in both arms.

## Do the reports become unreadably long? No — 3 sections in 98

The measurable risk of removing a cap is that four refinements under an *extend the picture*
instruction grow a section without limit. Largest value each section reached in the **final**
report of each session, in runes:

| section | ON before | ON after | OFF before | OFF after | former cap |
|---|---:|---:|---:|---:|---:|
| synopsis | 644 | **755** | 642 | 642 | 650 |
| done | 896 | **954** | 842 | **1,253** | 900 |
| happened | 962 | 962 | 916 | **1,046** | 1400 |
| structure | 840 | 840 | 932 | 932 | 1600 |
| current | 268 | 268 | 271 | 271 | 900 |
| why | 288 | 288 | 288 | 288 | 900 |
| next | 361 | 361 | 407 | 407 | 900 |
| insights (12 entries) | 2,190 | 2,190 | 2,248 | 2,248 | — (300/entry) |
| unresolved | 479 | 479 | 500 | 500 | — (300/entry) |

**3 of 98 final prose sections exceeded a former cap** (s1 synopsis 755, s3 `done` 954, s9
synopsis 688 in the ON arm). The largest section anywhere in the four runs is `done` at 1,253
runes — about 200 words. Nothing approaches "several thousand runes"; on this corpus the model
does not run on when nothing stops it, and the caps were binding on a handful of sections rather
than shaping the output.

Two things this does not establish. Every measurement here is a **16-window prefix with four
refinements** — the same coverage limit as Part 7 — so accumulation over a long session is
unmeasured, and `structure`, the section most exposed to it, never came close to its old cap.
And the *content* of the restored text is not scored: what is measured is that removing the clip
changed no threshold, not that the extra 105 or 353 runes are good writing.

## The prompt budget still holds

The real-corpus probe (`TestRealCorpusPromptsNeverTripTheBackstop`, 200 sessions requested,
this session's 11.4 MB transcript first, no model needed) after the change:

| | before removal | after removal (**ships**) | + guidance (since reverted) |
|---|---|---|---|
| sessions / steps / prompts | 29 / 1,100 / 2,200 | 29 / 1,101 / 2,202 | 29 / 1,100 / 2,200 |
| **panics** | **0** | **0** | **0** |
| tightest refine window margin | **+0** (s1 i8) | **+0** (s1 i8) | **+0** (s12 i8) |
| largest refine prompt | 14,000 | 14,000 | 14,000 |

(Step counts differ by one between probe runs because the corpus includes this session's own
transcript, which grows while the work is done.)

The margin is **+0**, and that is not caused by any change in this part: it is +0 before the
removal too. Part 7 reported +20 on the same probe; the corpus has since grown (1,100 steps
against 1,087, this session's transcript now 11.4 MB), and the last 20 runes went with it. Zero
is the exact reserve met — correct by construction, with nothing left over. **The budget cannot
absorb another instructional paragraph** without either raising it or taking the room from
somewhere else.

## Length as prompt guidance was measured too, and it does not ship

Removing the caps leaves length unstated, so the obvious companion change is to say it in the
prompt, where a length is a guide the writer can weigh rather than a cut applied afterwards.
That was written, measured on **both arms of two further sweeps**, and **reverted**: on this
model it costs thresholds that were passing.

Two wordings. The first put a paragraph after the section list — *"The lengths named above are
GUIDES, not limits. Nothing cuts your answer short, so write what a section needs and then stop…
Padding and repetition are the failure, not length"* — plus per-section sentence guides (`done`
"A few sentences", `current`/`why` "A sentence or two", and so on). The second moved that note
**ahead** of the list and deleted "and then stop … Padding and repetition are the failure".

| | baseline (caps removed) | wording 1 | wording 2 | want |
|---|---:|---:|---:|---:|
| T1 usable digests | 100.0% / 100.0% | **96.4% / 96.4%** | 100.0% / 100.0% | 100% |
| T2 unverified identifiers | 0.9% / 2.3% | 1.8% / **2.9%** | **2.2%** / 2.0% | ≤2% |
| T3 rubberstamped | 0.0% / 0.0% | 9.1% of 11 / 9.1% of 11 | 0.0% / 0.0% | ≤10% |
| T4 retention | 58.2% / 62.0% | 55.8% / 55.8% | 51.2% / 57.3% | ≥90% |
| T7 fabricated blockers | 4.5% / 4.5% | 2.3% / 2.3% | 6.8% / 9.1% | ≤10% |
| **T9 current-is-completed** | **1.8% / 1.8%** | **7.4% / 13.0%** | **10.7% / 16.1%** | **≤5%** |
| T13 fabricated `next` | 3.3% / 6.5% | 3.6% / **5.4%** | 3.6% / 2.6% | ≤5% |
| recovered panics | 0 / 0 | 0 / 0 | 0 / 0 | 0 |

(ON / OFF. T4's denominator moves between configurations — 79, 77, 82 — because the injected
facts are taken from the *first* report, which the prompt change alters, so T4 is rate-comparable
across these columns but not item-comparable.)

**Wording 1 lost two digests**, in both arms, deterministically: `session 7 step 2` and
`session 13 step 0` both failed `unresolved is empty — it must be addressed explicitly` through
all five retries. The note was the last thing read before a required list and "write what a
section needs and then stop" is evidently readable as licence to omit. By this study's own
standard a dropped digest is the worst outcome available, so that wording was not a candidate.

**Wording 2 fixed T1 and made T9 worse.** Moving the note and deleting the offending clause
returned T1 to 100%, and T9 went from 1 flagged report at baseline to **6 (ON) and 9 (OFF)**.
The flags are real, not the ordinary-English artifact this study keeps hitting: six reports whose
`current` reads *"is complete"*, *"has successfully signed"*, *"is complete. The next step is…"* —
a finished action reported as in progress, which is precisely what T9 exists to catch. The
plausible mechanism is the guide itself: `current`'s "A sentence or two" invites a sentence where
the section's own instruction says the answer may be "nothing in progress". T2's extra flags in
the same runs are the artifact class (`Extended Usage`, `Next.js`, `PostgreSQL`, `Conduct
Refactor`), so T2 is not what the decision rests on.

The decision rule was recorded before the second pair of runs finished — keep it only if T1
returns to 100% **and** nothing that passed now fails — and it says revert. Both commits are
reverted. Moving the note a third time would be tuning a prompt against a threshold panel until
it passes, which is the failure mode this study has documented under a different name five times.

**What that leaves.** Length is stated nowhere except the synopsis's original "Three or four
sentences", and on this corpus that is what the measurement supports: without any cap the largest
section is 1,253 runes, so there is nothing for guidance to fix and it is demonstrably not free
to add. **Not established:** that no wording can work. Two were tried on one model at one budget.
A third attempt should leave `current` alone — the section whose guide is the plausible driver of
the only decisive regression.

**A cost worth recording even though it is reverted.** `digestSections` sits inside both
instructional tails, so the guidance was 401 runes of prompt budget (301 after narrowing) and the
worst-case refine window margin fell from +188 to +4. It also **decalibrated a calibrated test**
twice — the fourth and fifth instances of this branch's signature defect, and the first caused by
a change made in the same commit as its own recalibration. `TestWindowKeepsItsFloorAtTheBoundary`
passed with the bug it guards reverted (window 1,613 both ways), and the `itemLen` scan had to be
re-run for each wording (9 → 12 → 21, divergence band 12-21 → 21-29).
`TestFloorIsReachableAtRealisticInputScale`'s revert changed *shape* twice as well. **Any change
to prompt text decalibrates these tests, and nothing detects it automatically.**

That last point produced one more finding, independent of this task's changes: the same test's
docstring was **already** stale before any of this. Its figures (reverted 1,589 / fixed 1,788)
were measured in task-7b round 4 and invalidated by the beat work raising `BeatCap` 200 → 512 —
the test's entire pressure is `MaxBeatSelection × BeatCap`. Re-scanned in the shipping state:
fixed never breaches (smallest window 1,604; 2,033 at `itemLen` 9), reverted breaches at 1-10 and
46-54 (1,501 at 9). It kept *detecting* the defect the whole time, which is why nothing caught the
drift: **a docstring can go stale while its assertion stays live**, and that is the softer half of
the signature defect rather than a separate one. Corrected, with the band rather than the point
documented.

## What this eliminates, and what it does not

**Eliminated as a cause of the T4 failure: character-level truncation of prose.** It was not
deleting the specifics that go missing, and removing it moves retention by zero in both arms.
Combined with Part 7's 0 cap evictions across all 240 pairs, both *storage-side* explanations
for the retention failure are now refuted by measurement. What remains is what Part 7 named: the
model drops specifics it was explicitly handed, and the retain-list's derivation from the last
report alone makes each drop permanent.

**Not established here**

- **That length guidance in the prompt can be made safe.** Two wordings measured, both regress
  a passing threshold; reverted. A third attempt should leave `current` alone.
- **That the removal improves the report.** It restores text a reader was losing (3 sections in
  98, plus whatever was clipped at intermediate steps, which this run does not count) and
  removes a defect that was visible only on reading — an ellipsis mid-clause. Neither is scored
  by any threshold in this study.
- **Accumulation over a long session.** 16-window prefix, four refinements, `structure` never
  near its old cap.
- **The T3 improvement.** One session, two flags, denominator 12, OFF arm only; mechanism
  consistent with Part 6 but not isolated.
- **Anything about the anchor.** The ON/OFF ablation is de-confounded in the harness now
  (Part 7's must-fix 3) and these four runs are the first to use it, but `SubjectShifted` still
  fires on nearly every refinement, so ON remains *anchor-always*. The ON/OFF gap in T4 here is
  58.2% vs 62.0% — the same direction as Part 7's, now unconfounded, and still a 3-fact
  difference on a denominator of 79.

> **⚠️ SUPERSEDED IN PART BY PART 9.** The two beats lost per run are diagnosed and fixed there
> (a byte-identical re-request at temperature 0); T12's 15.7% → 25.0% is explained there (the
> sample shrank with the cadence change, the flagged count fell 11 → 10); and the T4 figures below
> are the *baseline* Part 9 measures against, not the current tree's — Part 9's shipping tree
> measures 64.2% (ON) / 66.7% (OFF) of 81.

**A correction to Part 7's figures, for the same code path.** Part 7 reports T4 at 50.0% (ON) /
56.2% (OFF) of 80. Re-measured *with the clipping still in place* on the current tree, it is
**58.2% / 62.0% of 79** — the *before* column above. Nothing about the caps explains that gap:
between Part 7's sweep and this one the beat work landed (cadence 3 → 5 user turns,
`ChangedSubject` grounded in the prompting turn, the progress and forbidden-opener rules), which
changes what every refinement reads. Part 7's numbers stand for the code they were taken on;
these are the current ones, and the ≥90% threshold fails either way. The retention *failure* is
unchanged; its magnitude is 4-6 points less bad than published.

# Part 9 — Five gated changes, in counts: the reach-forward delimiter rule is the whole gain

Six sweeps × two arms, each measured against a committed baseline in both arms (14 stratified
sessions, this branch's own transcript first; Qwen3-4B-Instruct-2507 Q4_K_M, ctx 8192,
temperature 0, budget 14,000). Per-step detail, including the variant that was measured and
rejected: `.superpowers/sdd/2026-08-10-session-story-rollup/gate-and-subjects-report.md`.

**Counts lead here and rates follow, deliberately.** Every denominator in this round moves — the
delimiter rule carries slightly *more* text, so slightly more facts get injected — and the reason
last round produced five unreadable verdicts is that each one was a rate over a denominator that
had moved too. The rates are kept below for continuity with Parts 4-8, marked as secondary.

> **On the provenance of this Part.** Two agents wrote a Part 9 into this file within minutes of
> each other, working the same five steps from the same logs. This is the merge of both. Where they
> disagreed on a number, both numbers are shown with their method — see *Two hand audits that
> disagree*, which is a finding rather than a tidying-up problem.

---

## The causal attribution, which is the point of the section

**Net over the whole sequence: +6 facts retained (ON) and +5 (OFF), and essentially all of it is
one change.**

`3986c78` — the rule that a delimiter cut must reach FORWARD to the next boundary rather than
retreat to the previous one — is worth **+7 (ON) and +4 (OFF) against the baseline**, arriving at
53/53 from 46/49 (and +11/+10 against the retreating step it replaced). The three changes that
land after it (the beat retry, the beat window geometry, DF-based subjects) move retention by −1 to
+2 and finish within one fact of each other. There is no reading of these counts in which the later
three carry the gain.

**The FIRST form of the delimiter rule was harmful and is superseded, not shipped.** `556667b`
retreated to the last sentence end *inside* the budget, which threw away everything between that
boundary and the cut — 40.3% of the coarse session view (146,426 runes; 83 turns reduced to a bare
marker). Retention fell **46 → 42 (ON) and 49 → 43 (OFF) over FEWER facts injected** (79 → 78),
which is the plainest possible statement that it made things worse. It was measured, rejected, and
replaced by `3986c78`.

Reaching forward costs **+2.1%** on the coarse view (6 drops, not 83) and **+7.8%** on mined turns
(0 drops): the delimiter-respecting cut now carries slightly *more* text than the rune-count cut it
replaces, while landing on a full stop rather than mid-word. `TestCorpusDelimiterCutIsAffordable`
asserts the −5% bar on a real corpus rather than logging it.

⚠️ **What is NOT established is that any single later step caused its own T4 movement.** T4's
denominator derives from the first report, so any input change moves both the rate and the items,
and this round measured five configurations inside a ±5-point band. The attribution above rests on
the *magnitude* of the delimiter step (+4 to +7, in both arms, in the same direction, and with a
directly measured mechanism: 40.3% of the view restored) standing outside that band, not on the
band's internal ordering.

---

## Fact retention, in facts

| step | arm | injected | **retained** | lost | identifier-shaped retained/injected |
|---|---|---:|---:|---:|---:|
| `fdb1a23` baseline | ON | 79 | 46 | 33 | 20/42 |
| | OFF | 79 | 49 | 30 | 21/42 |
| `556667b` delimiter, **retreating** (superseded) | ON | 78 | 42 | 36 | 18/43 |
| | OFF | 78 | 43 | 35 | 19/43 |
| `3986c78` delimiter, **reaching forward** | ON | 81 | **53** | 28 | **26/45** |
| | OFF | 81 | **53** | 28 | **25/45** |
| `d4343df` beat retry at rising temperature | ON | 81 | 53 | 28 | 26/45 |
| | OFF | 81 | 53 | 28 | 25/45 |
| `66042cc` beat window geometry | ON | 81 | 54 | 27 | 24/45 |
| | OFF | 81 | 55 | 26 | 25/45 |
| `f62b80e` DF-based subjects (shipping tree) | ON | 81 | 52 | 29 | 24/45 |
| | OFF | 81 | 54 | 27 | 26/45 |

The identifier-shaped half — the population T4 exists to protect — moves 20 → 24 (ON) and
21 → 26 (OFF).

This is the first movement on this branch's headline failure that survives being counted rather
than rated. It is also nowhere near the ≥90% the design asks for: **29 named facts still vanish
between the report that held them and the final one.**

## Beats asked versus generated

| step | asked | generated | lost | which |
|---|---:|---:|---:|---|
| baseline | 42 | 40 | 2 | s6 i9, s10 i4 — no complete sentence |
| delimiter (retreat) | 42 | 40 | 2 | same two |
| delimiter (forward) | 42 | 39 | 3 | those two + s12 i14 |
| **beat retry** | 42 | **42** | **0** | — |
| beat windows | 42 | 41 | 1 | s3 i9 |
| DF subjects | 42 | 40 | 2 | s10 i4, s10 i9 |

Identical in both arms at every step.

At temperature 0 a rejected generation is re-requested **byte-identically**, so all five attempts
failed on the same string. `d4343df` samples 0 → 0.2 → 0.4 → 0.6 → 0.8 **at an explicit seed** —
seeded rather than merely warmed, because "both arms run twice with identical figures" is what
retired *is it variance?* for this study. `callValid` is unchanged for every other caller, and the
shape standard is unchanged; both asserted.

`d4343df` **held**: all three known failures recovered, nothing lost at the geometry it was
measured on. It is **not a guarantee** — one generation exhausted all five temperature-varied,
seeded attempts at the new beat-window geometry and two did at DF subjects. The honest statement is
that varying the sample recovers *some* rejected generations, not that a rejected generation is now
recoverable.

## Digests produced, and the one cost that was then fixed

56 of 56 at baseline, retreat, forward and beat-retry, in both arms. Then **55 of 56**: ON at
`66042cc` (s1 step 3), and both arms at `f62b80e` (ON s1 step 2, OFF s3 step 2). All three are the
same failure — five attempts exhausted on `unresolved is empty — it must be addressed explicitly`.

**Diagnosed and fixed after this round; see Part 10.** The root cause is that neither existing
repair fires when the model returns `unresolved` empty *and* `closed` empty, so the repaired digest
is the raw digest and `ValidateDigest` rejects it — and because the digest path samples greedily,
all five attempts are byte-identical.

## Two hand audits that disagree, and both belong in the record

`f62b80e` replaced "a subject is a strong identifier **or any token ≥7 characters**" with "a
subject is a term the corpus shows is rare" (document frequency ≤0.35 of sampled sessions, cold
start falling back to identifiers only under 12 sessions — `lenstat`'s precedent with the direction
reversed, because here the risk is noise in a block the prompt labels *authoritative* rather than a
memory spike).

The measure of whether that worked is a person reading the 14 sessions' 12-term `Subjects` blocks.
**Two agents did that independently, over the same 168 terms and the same logs, and got different
numbers:**

| audit | criterion | before | after | delta |
|---|---|---:|---:|---:|
| A | a **pre-registered stoplist** of terms that name nothing specific, written before the "after" lists were seen and applied identically to both; product and tool names (Cowork, Gemini, OAuth, GLiNER2) counted AS subjects | 88 of 168 (52.4%) | 145 of 168 (86.3%) | **+57** |
| B | a term counts if it **names something specific to THAT session** — identifier, path, file, symbol, flag, env var, product or technology name, or a domain term the session was about — and not if it is ordinary English or generic software vocabulary | 69 of 168 | 102 of 168 | **+33** |

**They agree on direction, on sign for every session, and on order of magnitude. They do not agree
on absolute value, and the whole of the gap is where the line sits for generic technical
vocabulary** — `latency`, `heap`, `arena`, `trim`, `keychain`, `secrets`, `windows`, `signing`.
Audit A's stoplist did not contain them, so they scored as subjects; audit B's rule calls them
generic software vocabulary, so they did not.

**A's criterion is not in the record and B's is.** A's lived in `scratchpad/score_subjects.py`,
which was never committed and no longer exists, so the exact stoplist cannot be reapplied — only
its description survives. B's rule is the prose above and can be reapplied by anyone.

⚠️ **This is a finding, not an embarrassment, and it is the reason it is written down.** Evaluation
on this study is moving *toward* qualitative review by a stronger model reading output against its
source, precisely because the string thresholds cannot make the judgements they stand in for. Two
careful readers differing by 19 percentage points on the same 168 terms **is the variance figure
for that method** — the first one this project has. A qualitative verdict quoted without its
criterion is worth about ±20 points, which means: state the criterion, keep it in the repository,
and do not compare two audits that used different ones. Averaging these two numbers, or picking the
flattering one, would have thrown the only measurement of the new method's reliability away.

Neither number is a measurement in the sense the rest of this document uses the word.

### What both audits agree on

- **The audience requirement holds**, verified on the corpus rather than quoted (34 sessions,
  22,610 distinct terms): `depreciation` .03, `accruals` .03, `larkin` .03, `meridian` .00, all far
  under the 0.35 cut. An accountant's vocabulary survives a rule tuned on nobody's stoplist.
  Asserted by `TestCorpusDocumentFrequencySeparatesSubjectsFromEnglish`.
- **A real cost**: `keld-signal` (.41) and `enrichment` (.56) are *excluded* though a reader would
  call them subjects — correct IDF behaviour on a corpus where every session is about them.
- **The DF table does NOT show two separated populations**, which is the honest version of the
  design's expectation: the generic and specific lists overlap between ~.12 and ~.56, so 0.35 is a
  cut through a contested band, not a valley.
- The clearest single case is the memory session, whose record is what the prompt calls
  authoritative:
  - before: `sidecar, home/dg/keld/keld-cli, metrics, command, keld-agent, process, package, service, running, inference, fragmentation, restart`
  - after: `home/dg/keld/keld-cli, daemon, trim, fragmentation, glibc, MALLOC_ARENA_MAX, malloc_trim, spawn, agent.json, daemon.go, arena, heap`
- `exactly`, `confirm`, `existing`, `changes`, `running`, `question`, `complete` — the words a
  "≥7 characters" rule cannot tell from a subject — are gone corpus-wide.
- Two defects the audits surfaced, neither fixed: the same term at two casings can occupy two of
  the twelve slots (`Cowork` and `cowork` in s9), and one session lost three genuine specifics the
  old rule held (`KELD_TEST_DATABASE_URL`, `asyncpg`, `postgresql`).

## Two errors of mine, recorded because both were quoted to you as measurements

1. **`beats LOST 2 → 3` was offered as evidence that `d4343df` was not holding. It cannot bear on
   that fix at all.** `3986c78` landed *before* `d4343df`, so the 2 → 3 move happened at the
   reach-forward delimiter step, one commit earlier than the fix it was cited against. What the
   sequence actually shows is in the beats table above: 0 of 42 lost at the geometry the fix was
   measured on, then 1 at the new beat windows and 2 at DF subjects, each a *new* generation that
   exhausted five varied, seeded attempts.
2. **"4 of 12 subjects at baseline on this session" was an eyeballed figure quoted as a
   measurement.** It is not reproducible under any stated criterion: audit B measures s1 at **7 of
   12 both before and after** (`control` and `changes` leave, `study` and `weights` arrive), and
   audit A at 8 of 12 before / 10 of 12 after. The spec's starting point is not the tree's starting
   point, and a number nobody can re-derive should not have been passed on as one.

## The gate, and its per-step verdicts

Four regressions on this branch were each found by the *next* review rather than blocked at the
change — the recency anchor, two length-guidance wordings, and the beat work, which gained ~8
points of retention while silently losing two beats per run. Nothing enforced "no net regression".
`internal/agent/enrich/llmstudy/gate/` parses the sweep's own log — one source of truth, and every
historical log in that directory becomes comparable without re-running it — and reports per
threshold and per arm: improved / unchanged / **REGRESSED**, regressions first.

Three properties those four failures needed and no artifact had:

- **Denominators travel with rates.** A rate that improves while its denominator collapses is
  marked *not attributable* rather than counted as a win.
- **Losing a verdict is a regression.** `NO VERDICT` parses as *absent*, not as 0.0%.
- **An incomplete log is not comparable.** A half-finished sweep would parse into zeroes, and
  zeroes read as a sweeping improvement on every lower-better metric.

Re-baselining is a separate test taking an explicit output path, so the person looking at a
regression is not one flag away from making it disappear. It is not wired to CI, by design: it has
to be run.

| step | gate verdict vs baseline |
|---|---|
| delimiter rule, retreating | **REGRESSED** — T4 in both arms. **Rejected**, superseded. |
| delimiter rule, reaching forward | REGRESSED — T9 1.8 → 3.6% (1 → 2 items), beats lost 2 → 3 |
| beat retry | REGRESSED — T9 only, **inherited** from the previous step; beats lost 2 → 0 |
| beat windows | REGRESSED — **T1 100% → 98.2%**, T3 OFF 8.3%, T2 ON 1.1% |
| DF distinctiveness | REGRESSED — T1 98.2%, **T3 16.7% (crosses ≤10%)**, T2 ON 1.5% |

**Nothing was reverted after the first step, and that was escalated rather than decided.** The
delimiter rule, the beat geometry and the distinctiveness rule are each your own convention or
spec, which is the brief's exception (b): *stop and ask*. The retreating variant, which no spec
required, **was** rejected and replaced.

### T12's 15.7% → 25.0%, settled

**The sample shrank; the behaviour did not worsen.** 15.7% of 70 is 11 flagged beats, 25.0% of 40
is 10. The denominator fell because the cadence went from every 3 user turns to every 5 — 42 beats
asked instead of 70 — and `t12Checked` tracks the cadence directly. One *fewer* flagged beat over a
43% smaller sample, reported as a 9-point rise. Neither earlier artifact recorded which number had
moved.

### `9b4f01e`, the gate's noise floor — kept

One piece of the gate work finished green before the redirection below arrived, and it stays: a rate
that moves while its flagged count stands still, across a moved denominator, is a **rate artifact**
rather than a regression, and the revert class contains behavioural moves only. It also parses
T11's and T12's abstention counts and attaches them to the rate they qualify. A less noisy gate is
still worth having for the metrics we keep — see the fact/judgement split below — even though most
of what it watches is being retired.

⚠️ Recorded as a process fault: this landed in the worktree *while the sweeps were running*, and
every gate verdict in the report was re-run against the post-change gate so the six sweeps stay
comparable to each other. A gate whose sensitivity is edited between measurements is the failure
mode the gate exists to prevent, even when the edit is right.

## The mechanism findings, briefly

**The delimiter rule's largest site was not cosmetic.** `toolLine`'s 80-rune clip **truncated
3,376 of 3,596 shell commands (93.9%) mid-token** — and window text is both T2's verification
reference and the source `SessionRecord.Observe` extracts verbatim-verified `Subjects` from, so
half a token could become an authoritative subject that never existed.

**Two findings that are not changes.** `fitTurns`' line trim cannot be made unconditionally
boundary-respecting at this budget — making it so is the amplifier that panicked 6 of 293 real
refine steps, and reserving a line's worth needs 1,200 runes that do not exist at +0 headroom. And
**`maxSubjectTermLen` already drops rather than truncates**: the brief's claim that it truncates a
term is wrong and its own doc says so. What was missing was a *test*.

**Beat windows: coverage 11.0% → 59.5%.** `K = 12` was inherited from classification, where a
window exists to judge ONE prompt in context. Beats fire every 5 user prompts, and a five-prompt
stride runs **median 13,956 / max 52,148 runes** against a `K=12` window's median 2,578 — so
consecutive beat windows were disjoint and most of every stride was read by nothing. Contiguous
spans with a reserved 28% stride overlap, bounded at 16,000 runes: coverage 59.5%, overlap 18.0% of
the previous window, `ChangedSubject` 90.0% → 82.9%, T12 25.0% → 9.8%. The old 11.0% was not merely
low, it was **unmeasured** — coverage did not exist as a quantity anywhere in the harness.

⚠️ **Two spec defects, recorded rather than worked around.** "Coverage must be 100%" is unreachable
at `ctx` 8192 and not narrowly: 20,000 runes of real transcript measures 5,433 tokens
(`/tokenize`, worst of four chunks), so the largest stride needs ~14,200 tokens against a context
that must also hold the record, the instructions and the generation. The shortfall is now marked
*inside* the window, between the overlap and the kept turns, where the hole actually is. And the
spec's named check does not exist to be checked: it asks for "the beat-1 → beat-2 case this
session's series currently misses", but every beat of session 1 is marked subject-changed on the
baseline and the corpus rate is 90% — `ChangedSubject` is *over*-reporting, the opposite of the
premise.

**T11 and T12 re-measured after the DF change, and the answer is a finding about the checks.**
T12 got *worse* against the immediately preceding step (9.8% → 17.5%), and the mechanism was
predicted from the DF table before the run: DF is measured over **transcripts**, and T12's problem
terms are **model-prose gerunds** that are genuinely rare there (`analyzing` .00, `ensuring` .03,
`finalizing` .18), so they pass a rarity test comfortably — while the record's side became narrower,
so fewer terms match. T12 compares a model-prose term set against a transcript-derived one, and
document frequency can only discipline the second. T11 did not move and lost half its power:
0.0% of 30 judged → 0.0% of 18, abstentions 12 → 23 of 41. It is near-tautological for a *different*
reason now — not two English words certifying a synopsis, but rarely rendering a verdict at all.

## The rates, secondary, for continuity with Parts 4-8

Every denominator here moves; read the counts above first.

| | base ON | final ON | base OFF | final OFF | want |
|---|---:|---:|---:|---:|---:|
| T1 usable digests | 100.0% of 56 | **98.2% of 56** | 100.0% of 56 | **98.2% of 56** | 100% |
| T2 unverified identifiers | 0.9% of 762 | **1.5% of 810** | 2.3% of 770 | 1.4% of 864 | ≤2% |
| T3 rubberstamped | 0.0% of 12 | **16.7% of 12** | 0.0% of 12 | **8.3% of 12** | ≤10% |
| **T4 retention to final** | 58.2% of 79 | **64.2% of 81** | 62.0% of 79 | **66.7% of 81** | ≥90% |
| — identifier-shaped | 47.6% of 42 | 53.3% of 45 | 50.0% of 42 | 57.8% of 45 | — |
| — bare capitalised | 70.3% of 37 | 77.8% of 36 | 75.7% of 37 | 77.8% of 36 | — |
| T7 fabricated blockers | 4.5% of 44 | **0.0% of 43** | 4.5% of 44 | **0.0% of 43** | ≤10% |
| T8 stale open items | 0.0% of 74 | 0.0% of 74 | 0.0% of 74 | 0.0% of 69 | ≤2% |
| T9 current-is-completed | 1.8% of 56 | 1.8% of 55 | 1.8% of 56 | **0.0% of 55** | ≤5% |
| T10 synopsis restates | 0.0% of 56 | 0.0% of 55 | 0.0% of 56 | 0.0% of 55 | ≤5% |
| T11 synopsis lags † | 0.0% of 30 (12 abst) | 0.0% of 18 (23 abst) | 5.7% of 35 (7 abst) | 0.0% of 18 (23 abst) | ≤10% |
| T12 beat-vs-record † | 25.0% of 40 | **17.5% of 40** | 25.0% of 40 | **17.5% of 40** | ≤5% |
| T13 fabricated `next` | 3.3% of 90 | 3.4% of 88 | 6.5% of 92 | **5.9% of 101** | ≤5% |
| instruction leakage | 0 | 0 | 0 | 0 | 0 |
| **recovered panics** | **0** | **0** | **0** | **0** | 0 |
| beat turn coverage | 11.0% (unmeasured) | **59.5%** | same | same | — |
| consecutive-window overlap | 0% | **18.0%** of prev | same | same | — |
| largest prompt | 13,929 | 13,968 | 13,992 | 13,933 | ≤14,000 |

† T11 and T12 are still **not established** as instruments (Part 7). Their numbers move; that does
not make them measurements.

**Real-corpus probe on the shipping tree**: 29 sessions / 1,102 steps / 2,204 prompts, **0 panics**,
tightest refine window margin **+0** (s5 i23) — unchanged from the baseline's +0. Budget and ctx
cannot come down.

## What in this harness measures a fact, and what encodes a judgement

This is the durable output of the round, and it is why the gate's per-threshold re-adjudication was
dropped mid-task on your instruction: **the string thresholds are not worth making more reliable**,
because most of them are heuristics standing in for semantic judgements, and every one has now
failed in a way that cost more to diagnose than the thing it measured.

**Facts — mechanically checkable, worth trusting.**

- Digests produced (T1): a schema-valid object exists or does not.
- Recovered panics.
- Prompt within budget, largest prompt, window margin over the floor: arithmetic on the assembled
  prompt.
- Beats asked / generated / kept / discarded, and the difference between the first two.
- Retain-list counts: offered / evicted by the cap / already gone from the prior report.
- Beat-window turn coverage, and consecutive-window overlap.
- Document frequency itself.
- **T4 retention is the one quality-flavoured metric that is a fact** — it asks whether a string
  present in report *N* is present in report *N+3*, verbatim — with the caveat that *which* strings
  enter the population is a judged extraction, so the denominator is judged even though the test is
  not.

**Judgements a string heuristic cannot make — do not trust the number.** T3 rubberstamping, T7
fabricated blockers, T8 stale open items, T9 current-is-completed, T10 synopsis restates, T11
synopsis lag, T12 beat-vs-record, `ChangedSubject`, `SubjectShifted`, `beatsRestate` suppression,
and "is this token a specific". Each is a significant-word overlap ratio or a stopword lookup
standing in for a semantic relation, and each has now been shown to fail: every one of T12's flags
was an accurate beat, T11's 0.0% was near-tautological *and* is now judged on 18 of 41 refinements
rather than 32, `SubjectShifted` fires on 41 of 42, `ChangedSubject` on ~90%.

**T2 and T13 are half-facts.** "This identifier appears nowhere in the source" is verbatim-checkable,
but whether the token is an identifier is the same judged extraction as T4's denominator, and naming
something from earlier context is not necessarily fabrication.

## Where evaluation goes next

**Evaluation of the judged half moves to qualitative review of the output against its source** by a
stronger model, with the criterion written down and kept in the repository — which is the lesson the
two hand audits above paid for. **The threshold apparatus is retained only for the facts**: the
gate, its noise floor and the fact-list above stay useful for T1, panics, prompt budget, beat and
retain-list counts, coverage and T4, and those are worth keeping green. The judged thresholds should
not be extended, re-tuned or cited in the meantime; they are kept running because a change that
moves one of them by a lot is still worth looking at, not because the number means what it says.

## Not established

- **That any of the later three changes caused its T4 movement.** T4's denominator derives from the
  first report; the gate cannot tell chaos from causation and neither can this Part. The delimiter
  attribution rests on magnitude plus a measured mechanism, not on the ordering.
- **That no beat is lost.** Two known failures recovered; new ones appeared at two later geometries.
- **T11 and T12 as instruments.** Still not established, now for new reasons.
- **`SubjectShifted`.** Still 41 of 42. It needs the production EWMA focus, not a better tokeniser.
- **`clipProse` has no production callers** and `DefaultListEntryCap` is now advisory for a single
  un-terminated sentence. Both recorded at their sites; neither cleaned up, because deleting them
  rewrites fixtures the floor tests are calibrated on.

## Amendments to earlier Parts

- **Part 7 and Part 8's T12 discussion**: the 15.7% → 25.0% move is explained — the sample shrank
  with the cadence change and the flagged count fell 11 → 10. Neither Part could say which.
- **Part 8's "two beats are lost per run" (concern 5)**: cause identified (a byte-identical
  re-request at temperature 0) and fixed, with the case the fix does not cover stated above.
- **Part 8's "+0 runes of headroom"**: still +0 on the shipping tree, and the delimiter rule's
  forward allowance did not consume it.

# Part 10 — The three lost digests: an empty open list is an answer, and it is now counted

Part 9 recorded three digests lost to `unresolved is empty — it must be addressed explicitly` with
all five attempts exhausted: ON at `66042cc` s1 step 3, and both arms at `f62b80e`, ON s1 step 2 and
OFF s3 step 2. This is the diagnosis, the fix, and one sweep of both arms.

## The diagnosis, and it was not what the retry story said

**Two independent causes, and the code says so.**

1. **Neither existing repair fires.** `applyClosures` and `dropStaleOpenItems` each substitute
   `UnresolvedSentinel` when *they* empty the open list — but each returns early when it has
   nothing to do (`len(closed) == 0`, `len(stale) == 0`). When the model answers with `unresolved`
   empty **and** `closed` empty there are no closures to apply and nothing stale to drop, so the
   list stays empty. Moving validation onto the *repaired* digest, which an earlier round did and
   documented, does not help here: on this input the repaired digest **is** the raw digest.
2. **The retry cannot differ.** The digest path calls `callValid`, i.e. `sample == nil`, so all five
   attempts are the byte-identical greedy request. `d4343df`'s temperature ladder (0 → 0.2 → 0.4 →
   0.6 → 0.8 at an explicit seed) is `GenerateBeat`'s alone and was never wired to the digest path.

⚠️ **Correction to the brief that commissioned this work**, which asserted the retries were already
temperature-varied and seeded and concluded that the model therefore "genuinely returns an empty
open list five times running". It returns it *once*, and is then asked four more identical
questions. Both halves matter, but they are different defects and only the first is fixed here.

## The fix, and the signal it must not erase

`ensureUnresolvedIsAddressed` is a third repair, last in the chain: if the repaired list is still
empty, the sentinel takes its place. If the model returns nothing open, its answer **is** "nothing
is open", and the sentinel is that answer's prescribed form — the same reasoning the other two
repairs already use for the lists they empty themselves.

**`ValidateDigest` rejects an empty list deliberately**, and its own comment says why: an empty list
is what a rubberstamping model produces. A silent substitution would have deleted that signal along
with the failure — the exact defect class this branch keeps finding, applied to the branch's own
fix. So the repair **reports whether it fired**, `Llama` counts it, and the sweep prints both an
attributable per-step line and a summary row:

```
EMPTY-UNRESOLVED s3 step2: the model returned no open list at all; code supplied the
  sentinel (this step would have been LOST before the substitution existed)
EMPTY-UNRESOLVED SUBSTITUTED 1 of 42 refinements
```

It fires **only** on the model-returned-nothing case. A list one of the other two repairs emptied
means the model did name open items and code resolved them, which is a derivation, not a silence,
and counting it would make the number as useless as hiding it.

Counted once per committed digest — not inside the validator, which runs per attempt, nor inside
the repair closure, which runs twice per call.

## What the sweep measured

Both arms, 14 sessions, 56 attempts each, on a **fresh pinned snapshot** (558 transcripts,
2026-08-11T21:26).

| | ON | OFF |
|---|---:|---:|
| **T1 usable digests** | **100.0% of 56** | **100.0% of 56** |
| digests produced / attempted | 56 / 56 | 56 / 56 |
| **EMPTY-UNRESOLVED substituted** | **0 of 42** | **1 of 42** (s3 step2) |
| recovered panics | 0 | 0 |
| instruction leakage | 0 | 0 |
| T4 retention (facts) | 49 of 71 | 50 of 71 |
| beats asked / generated / kept | 42 / 40 / 40 | 42 / 40 / 40 |
| largest prompt | 13,896 | 13,968 |
| tightest refine window margin | 2,088 | 2,623 |

**T1 is back to 56/56 in both arms**, from 55/56 in both arms at `f62b80e`.

**One of the three named cases reproduced and was recovered: `OFF s3 step 2`** — same arm, same
session, same step, and the substitution line names it. That is the mechanism observed on real
data rather than argued.

**The other two did not recur, and the sweep therefore cannot speak to them.** The ON arm
substituted **zero** times, so on this corpus the model never returned an empty open list in that
arm at all. `66042cc` s1 step 3 and `f62b80e` s1 step 2 are not reproduced; their recovery rests on
the unit-level revert-and-fail, which reverting the substitution reproduces exactly, error string
included: `gave up after 5 attempt(s): invalid digest: unresolved is empty`.

### The zero is also what makes "nothing else moved" checkable

With **0 substitutions in the ON arm, that arm ran the byte-identical pre-fix code path** —
`ensureUnresolvedIsAddressed` returns its input untouched when the list is non-empty. So every ON
movement in this sweep is attributable to the corpus, not to the change. That is a stronger
statement than a threshold comparison could make, and it is worth more than the arms being
identical would have been.

⚠️ **The corpus is NOT the one Part 9 measured on, and this could not be avoided.** That round's
pinned snapshot (567 transcripts, 2026-08-11T15:13) was not preserved and no longer exists on disk,
and neither were its two sweep logs — so a step-to-step comparison against `f62b80e` is impossible.
This sweep pins a fresh snapshot and both arms share it. Its T4 denominator is 71 against Part 9's
81, so **none of Part 9's fact counts may be compared with this Part's**.

**The snapshot is kept this time**, which is the whole lesson:
`/home/dg/keld/study-corpus-snapshot-2026-08-11T2130/projects` (558 transcripts, 783 MB, outside the
repo). Point a run at it with
`KELD_STUDY_CORPUS_ROOT=/home/dg/keld/study-corpus-snapshot-2026-08-11T2130/projects`, and anything
measured against Part 10 must use it or say that it did not.

While fixing this, a second reason the earlier pinning was weaker than believed came out:
`corpusRoot()`'s doc promised pinnability but only the document-frequency table read it —
`StratifiedTranscripts` and `ThisSessionTranscript` each built `$HOME/.claude/projects`
themselves, so `KELD_STUDY_CORPUS_ROOT` pinned the DF table and left **session selection on the
live, growing directory**, which contains this harness's own transcript. Both now read
`corpusRoot()`.

### Gate verdict: 3 rows, none of them this change

Against the committed `fdb1a23` baseline, all three in the ON arm — the arm with zero substitutions:

| row | baseline ON | now ON | reading |
|---|---:|---:|---|
| T2 unverified identifiers | 0.9% of 762 | 1.5% of 744 (count 7 → 11) | **inherited**: `f62b80e` already measured 1.5% of 810 |
| T3 rubberstamped | 0.0% of 12 | 6.7% of 15 (count 0 → 1) | **improved** against the step before it — `f62b80e` was 16.7% of 12, over its ≤10% threshold; this is back under |
| T13 fabricated `next` | 3.3% of 90 | 5.6% of 72 (count 3 → 4) | one item on a denominator that fell 20%; corpus, not this change |

Improved against the baseline in both arms: T4, T7 (to zero), T9, T12 (25.0% → 12.5%), and T2/T11
in the OFF arm.

**Nothing is reverted, and for once the reason is not a judgement call**: the arm carrying all three
flagged rows executed code that cannot differ from the pre-fix tree, so no gate row in this sweep
can be caused by the substitution.

## Known gap, recorded not fixed

`CreateDigestWithView` validates the raw response and has no repair chain, so a **create** answering
with an empty `unresolved` would lose the digest the same way. Never observed — all three measured
losses were step 2 or step 3, because a first report over a whole session prefix always has
something open — so extending the substitution there would change create-path output with no sweep
behind it. Named at its site.

Also unfixed, and the larger of the two causes above: **the digest path's five attempts are still
byte-identical.** The beat path proves a temperature-and-seed ladder is cheap and keeps a recovered
sample reproducible. Applying it to the digest path would change every retried digest and needs its
own sweep.
