# Conversation-domain recognition — study design

**Date:** 2026-10-01
**Status:** design approved conversationally. **This is a study and touches no production code.**
**Revert:** `rm scripts/convdomain_* && rm -rf /tmp/claude-1000/wcvenv`.

## 1. The question, and why this corpus can answer it when nothing else could

Can we tell, from a user-and-agent conversation, **what kind of work is being done and what
domain it serves** — "this is marketing related"?

Five routes were measured and rejected on 2026-09-30/10-01:

| route | result |
|---|---|
| GLiNER2 on prompts | 44.4% synthetic, **12.7% real**; per-domain profile an artifact of label wording |
| GLiNER2 on paths | **70.7%** but ONE domain only, and needs `ml_backend:"auto"` |
| hand-written keyword lists | 52% on text their author wrote, **17% on real paths** |
| agent self-report via hook | `SubagentStop` blocks **do not reach the model**; 4 firings, 0 actions |
| document-type recognition | PARKED — corpus is 48% agent process artifacts, 6 of 8 types absent |

**Every one of those failed on the same thing: no real, multi-domain, independently-labelled
data.** Our two corpora are 100% engineering, so every label derivable from them is circular
with a classifier reading them.

WildChat ends that. `shard0.parquet` holds **59,857 real ChatGPT conversations**, and a broad
domain scan finds **1,137 carrying a professional-domain signal**:

| domain | conversations |
|---|---|
| marketing | 350 |
| medical | 281 |
| legal | 179 |
| hr | 179 |
| sales | 145 |
| financial | 62 |

A human reading *"write me terms & conditions and policies for my website"* says **legal**
without consulting any feature a classifier would use. That is genuine ground truth, and it is
the thing this question has lacked all along.

⚠️ **This corrects a claim this session repeated several times.** "Public chat data does not
contain the work — 28 candidates in 59,857, of which ~1 was genuine" was measured by a filter
searching for work matching the **atv1 taxonomy specifically**. Asked the broader question, the
same shard yields **40x more**. The old figure was not wrong; it answered a narrower question,
and it was then cited as though it answered the broad one.

## 2. Two axes, because topic is not work

Each conversation gets **two** labels:

- **domain** — `marketing`, `financial`, `sales`, `legal`, `medical`, `hr`, `engineering`,
  `none` (no professional domain), `other` (a domain outside this set).
  ⚠️ `engineering` is in the vocabulary but was NOT one of the six the §1 scan searched for, so
  the CANDIDATE stratum cannot contain it by selection. It will appear only via RANDOM, where
  hobby coding is common — which is the right place for it, since a labelled `engineering`
  found without a keyword net is the one case where CANDIDATE bias cannot be the explanation.
- **mode** — `doing` (the person is performing that work) or `asking` (learning about it,
  homework, curiosity).

A student asking *"what is an NDA?"* and a founder saying *"draft me an NDA for a vendor"* are
both legal by topic and are not the same event. Routing, costing and reporting all care about
the second one. Collapsing them would make a law firm and a law school look identical.

## 3. ⚠️ THE CIRCULARITY THIS DESIGN MUST AVOID, AND IT IS SUBTLE

**The 1,137 candidates were selected BY KEYWORD MATCHING.** Scoring a keyword classifier on a
keyword-selected pool measures the selector, not the classifier — it would score near 100% by
construction, and that is exactly how the earlier keyword baseline looked good at 52% before
collapsing to 17% on data it had not selected.

So the labelled set is **two strata, and both are labelled and scored**:

- **CANDIDATES** — drawn from the 1,137. Rich in domain work; biased toward keyword-visible
  phrasing.
- **RANDOM** — drawn from the full English pool with no filter at all. Mostly `none`/`asking`,
  and it is the only stratum where a false-positive rate is measurable.

**Every arm is scored on both strata and the two numbers are reported separately.** An arm that
scores well on CANDIDATES and badly on RANDOM is a keyword detector wearing a classifier's
clothes. Pooling them would hide exactly that.

## 4. Scope

- **English only.** 29,634 of 59,857 (50%). Domain recognition across Chinese, Russian and
  Spanish is a different and harder problem, and mixing it in would confound a language effect
  with a domain effect.
- **The whole conversation**, user turns and assistant turns. Median 2 turns, max 55.
- **The unit is one conversation**, not a turn — matching what the repo owner asked to
  classify.

## 5. Arms

All scored on both strata, against two floors.

1. **majority-class** — the real floor. On RANDOM that is `none`, which will be high; an arm
   must beat it there, not just on CANDIDATES.
2. **shuffled** — the noise floor.
3. **keyword** — the same generous word lists the earlier study used. ⚠️ Expected to look
   strong on CANDIDATES and weak on RANDOM; that contrast is a finding, not a defect.
4. **GLiNER2** — job-framed labels (`"a marketing team's work"`), which measured 48.3% against
   12.7% for description-framed on real paths. Label wording is load-bearing and that specific
   wording is the one that survived.
5. **keyword ∪ GLiNER2** — the union that was best on both prior spikes (+9 points on prompts,
   +12 on paths) and is nearly free.

⚠️ **GLiNER2-large needs `ml_backend:"auto"`, which installers do not write.** Any arm using it
reaches dev machines and essentially no users. If GLiNER2 wins, the follow-up is whether
`gliner2.5-base-v1` (0.77 GB) or `convaiinnovations/laya` (421 MB) can do the same job where
the fleet actually is — both untested.

## 6. Labels, and the guard

**A subagent labels blind**, shown the conversation and nothing else — no arm, no keyword list,
no candidate/random marker. Labels are **committed before any arm is written**, provable from
git, and the arm author is a different agent that never reads the labels file. Under
subagent execution this is structural rather than a promise.

⚠️ **The stratum must be hidden from the labeller.** Knowing a conversation came from the
candidate pool would bias toward finding a domain in it. Sample ids are shuffled and the
stratum recorded separately.

**Sample:** 120 conversations — 60 CANDIDATES (10 per domain) and 60 RANDOM.

**The repo owner spot-checks 15.** Same reason as before: commit order stops the arms being
fitted to the labels; nothing stops the labels being systematically wrong. More than 3
corrections in 15 and the sample is re-labelled.

## 7. The bar, pre-registered

- **PRECISION ≥ 80%** on conversations where an arm answers, measured on CANDIDATES and RANDOM
  separately.
- **ACCURACY over ALL labelled conversations ≥ majority-class + 20 points**, abstentions
  counted WRONG, because the baseline never abstains. ⚠️ The two halves use different
  denominators on purpose — comparing precision-on-answered against baseline accuracy would
  flatter an arm by exactly its abstention rate.
- **The RANDOM stratum is the one that decides it.** An arm passing on CANDIDATES and failing
  on RANDOM has not learned domains; it has learned the selector.
- **`mode` is scored separately** and is allowed to be worse. Telling doing from asking is a
  second claim and should not be hidden inside the domain number.

## 8. What a result would and would not mean

**Would:** that a conversation's domain is recoverable from its text, by a named method, at a
measured rate, on real multi-domain user data with independent labels. That is more than any
route this week has produced.

**Would not:** that it transfers to Keld's data. ⚠️ **WildChat is ChatGPT chat; Keld's sources
are agentic transcripts with tool calls, long tool output, and far more assistant text than
user text.** A number here is evidence the method works on conversations, not evidence it works
on ours. The honest next step after a pass is re-measuring on agent transcripts, which needs a
non-engineering customer corpus — the same input tier B's 0% coverage and the path classifier's
one-domain limit are both waiting on.

## 9. Files

```
scripts/convdomain_frame.py     extract English conversations, both strata
scripts/convdomain-labels.txt   blind labels, committed FIRST
scripts/convdomain_study.py     the arms, the bar, the scorer
docs/notes/2026-10-01-conversation-domain-results.md
```

`pyarrow` lives in a throwaway venv at `/tmp/claude-1000/wcvenv`, never in
`~/.keld/sidecar-venv`, which is production tooling.
