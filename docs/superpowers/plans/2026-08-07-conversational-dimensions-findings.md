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
