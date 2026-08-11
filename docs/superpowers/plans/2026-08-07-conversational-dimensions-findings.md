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
4. **Re-run the ablation** on the de-confounded harness before quoting any anchor magnitude.
5. **`maxSubjectTermLen`** drops every `docs/` path (longest tracked path 83 runes vs a 64-rune
   cap) — a second mechanism deleting named specifics. Raising it is not free: measured at 96,
   the create-path worst case breaches the 1,600-rune window floor. It needs budget headroom
   the corpus probe says does not exist.
