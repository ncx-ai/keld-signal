# Can GLiNER2 tell what business domain a prompt serves?

**Date:** 2026-10-01
**Question:** given a user's prompt, can GLiNER2 say "this is likely marketing related"?
**Verdict:** **No, not reliably enough to publish.** It beats chance and beats a word list,
but at ~44-48% with a per-domain profile that is an artifact of label wording rather than of
the domains.

This is the question behind the `context` axis's deferred tier C
(`docs/superpowers/specs/2026-09-30-activity-context-axis-design.md` §6). That deferral was
made on inherited reasoning — no route could VALIDATE a text pass. This spike asks the
narrower, answerable question — can the model do it AT ALL — and answers it directly.

## Setup

- **126 synthetic prompts**, 9 domains, committed before any model ran (`scripts/domain-prompts.jsonl`).
  Each domain has 7 **obvious** prompts carrying its giveaway vocabulary and 7 **oblique**
  prompts describing the same work with that vocabulary removed.
- **300 real user prompts** sampled from two engineering corpora. Ground truth is
  `engineering` for all of them — one domain, but real text.
- **GLiNER2-large**, 1.9 GB, on an RTX 5060. 126 prompts in 4.5s (~36 ms each).
- Scorer and its bar committed before execution (`scripts/domain_gliner_spike.py`).

**Pre-registered bar:** GLiNER2 earns a place only if, on OBLIQUE prompts, it beats the
keyword baseline by ≥ 15 accuracy points.

## Results

| arm | overall | obvious | oblique |
|---|---|---|---|
| shuffled control | 6.3% | 7.9% | 4.8% |
| keyword baseline | 52.4% | **79.4%** | 25.4% |
| GLiNER2-large | 57.9% | 71.4% | **44.4%** |
| union (keyword, else GLiNER2) | **66.7%** | **88.9%** | 44.4% |

**The bar: PASS at +19.0 points on oblique.** The model reads something beyond vocabulary.

⚠️ **But it LOSES to the word list on obvious prompts** (71.4% against 79.4%). Where the
giveaway words exist, a deterministic bag of terms is better than a 1.9 GB model. The union
beats both, which is the only configuration here worth anything.

## The finding that decides it: the per-domain profile is an artifact of label wording

The repo already knew label wording is load-bearing (AGENTS.md: classifiers score against
readable DESCRIPTIONS, not bare ids). It is worse than load-bearing — it is dominant.

**On the 300 REAL prompts**, where ground truth is `engineering` for every one:

| label wording | accuracy | what it said instead |
|---|---|---|
| A "legal, contracts, compliance and agreements" … | **12.7%** | legal 60%, operations 15% |
| B bare ids ("legal", "finance", …) | 42.3% | marketing 34%, support 14% |
| C job-framed ("a software developer's work") | **48.3%** | scientific 38% |

A 4× swing from wording alone, on identical inputs. Wording A collapses because real
engineering prompts are full of rule and requirement language — "we must never", "the rule
is", "you need to make sure" — which reads as contracts and compliance.

**And on the synthetic set the aggregate is STABLE while the per-domain profile is not:**

| domain (oblique) | labels A | labels C |
|---|---|---|
| operations | 85.7% | **0.0%** |
| scientific | 0.0% | **71.4%** |
| financial | 85.7% | 100.0% |
| legal | 71.4% | 42.9% |
| marketing | 42.9% | 28.6% |
| **aggregate** | **44.4%** | **44.4%** |

Identical totals, reshuffled distribution. Changing the label text does not reduce the errors,
it **moves them to different domains**. So no per-domain claim from this model is trustworthy,
and tuning the labels is rearranging which domains are wrong rather than making it right.

## What this means for tier C

The deferral stands, now on measured grounds rather than inherited ones.

- At 44-48% a published `context` would be wrong more often than right.
- The failure is not fixable by better labels: wording changes which domains fail, not how
  many.
- The honest ceiling is lower still. The synthetic numbers were produced on text **I wrote**,
  so they carry my tells; the one real check available landed at 48.3% on a single domain,
  and a full real confusion matrix across nine domains is not measurable with the corpora
  this repo has.
- ⚠️ **The synthetic/real gap is the number to remember: 44.4% synthetic oblique against
  12.7% real on the configuration actually used in the main run.** Synthetic evaluation of
  this task overestimated by a wide margin, which is exactly what the original deferral
  warned about and the reason it was never a matter of trying harder.

## What would be worth trying, if this is revisited

1. **The union shape, not the model alone.** Keyword where it fires (59% of prompts), model
   only on the remainder. It was the best arm here by 9 points and costs almost nothing.
2. **Fewer, coarser domains.** Nine classes at 44% may hide two or three that are genuinely
   separable — `financial` scored 100% on oblique under labels C. A three-way split might be
   publishable where a nine-way one is not.
3. **A different model.** `gliner2.5-base-v1` (0.77 GB) and `convaiinnovations/laya` (421 MB,
   ModernBERT, a bounded-`choice` head) are both untried, and only the small ones could run
   under `ml_backend:"deterministic"` where the fleet actually is. GLiNER2-large needs
   `"auto"`, which installers do not write.
4. **Real labelled prompts from more than one domain.** Everything above is bounded by having
   exactly one real domain to check against. This is the same blocker as tier B's 0% coverage
   and the same corpus would unblock both.
