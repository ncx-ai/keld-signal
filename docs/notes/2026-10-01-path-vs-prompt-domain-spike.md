# Telling WHAT KIND of text: paths beat prompts

**Date:** 2026-10-01
**Question:** we can tell someone is summarizing. Can we tell what KIND of document?
**Verdict:** **Paths are the better evidence, by a wide margin, and the model is what reads
them.** 70.7% on real data against 48.3% for prompts. Not shippable yet; the first route
in this investigation that looks like it could be.

Follow-on to `2026-10-01-gliner2-domain-discernment-spike.md`, which measured prompts.

## Why paths were tried

`activity_class` reaches F1 0.92 because it reads tool names and shell commands —
structured metadata. "What kind of text" had only prose, which is where the prompt spike
measured 44% synthetic / 12.7% real. So the move was to look for the OBJECT in metadata too.

A filename is a human-authored label for exactly the thing we want to name, and it is closer
to metadata than to prose: no rhetorical framing, no instructions, no politeness. The specific
failure the prompt spike hit cannot occur there — engineering PROMPTS read as `legal` because
they are full of "we must never" and "the rule is"; `auth/middleware.go` has no such surface.

`files`, `directories` and `components` are already published as workspace-relative paths.

## Results

**Synthetic (126 paths, 9 domains, obvious = a folder names the domain, oblique = folders
name a project/client and only the filename carries the work):**

| arm | overall | obvious | oblique |
|---|---|---|---|
| shuffled control | 9.5% | 9.5% | 9.5% |
| keyword on path | 53.2% | 69.8% | 36.5% |
| GLiNER2 on path | 61.9% | 88.9% | 34.9% |
| union | **74.6%** | 90.5% | **58.7%** |

**Combining path and prompt signals (ids are parallel, four weak signals per item):**

| arm | overall | obvious | oblique |
|---|---|---|---|
| best single signal (prompt GLiNER2) | 57.9% | 71.4% | 44.4% |
| combined, precedence | 77.0% | 93.7% | 60.3% |
| combined, majority vote | **78.6%** | **96.8%** | **60.3%** |
| deterministic only, NO MODEL | 68.3% | — | 47.6% |

## ⚠️ And then real data reversed the ranking

300 real file paths from the two engineering corpora, ground truth `engineering`:

| arm | correct | what it said instead |
|---|---|---|
| keyword on path | **17.0%** | no answer at all on 65% |
| **GLiNER2 on path** | **70.7%** | sales 11%, support 7% |
| union | 58.7% | scientific 14%, sales 11% |

**The keyword arm won on synthetic paths because I wrote synthetic paths containing my own
keyword list.** Real paths are `internal/agent/service/service_linux.go`, and "service" and
"agent" are not in the engineering word list. The model generalises; the word list memorised
the author's tells. On synthetic the union beat the model; on real the model beats the union
by 12 points, because the keyword arm is confidently wrong whenever it fires.

That is the second time in two spikes that a synthetic result pointed the wrong way, and both
times in the direction that flattered the simpler arm.

## What this changes

1. **Paths are better evidence than prompts for document kind** — 70.7% against 48.3% on the
   only real data available, and they are already on the wire.
2. **The deterministic arm does NOT transfer.** A hand-written word list scores 53% on text
   I wrote and 17% on real paths. Any keyword approach must be validated on real paths before
   it is believed, and the synthetic number for it should be discarded entirely.
3. **The failure mode is better-shaped.** The prompt spike's `legal 60%` was a single dominant
   attractor that made every answer suspect. The path confusions are diffuse — sales 11%,
   support 7% — which is what a weak-but-real signal looks like rather than a broken one.

## What is still not known, and why this is not shippable

- **One real domain.** Everything real here is engineering. 70.7% could partly be a prior
  toward developer-looking labels rather than discrimination, and no confusion matrix across
  nine domains is computable from the corpora this repo has. Same blocker as tier B's 0%
  coverage; the same single corpus would resolve both.
- **GLiNER2-large needs `ml_backend:"auto"`**, which installers do not write. A path
  classifier that only runs on `auto` reaches dev machines and essentially no users. The
  small models — `gliner2.5-base-v1` (0.77 GB), `convaiinnovations/laya` (421 MB) — are
  untested and are the only ones that could run where the fleet is.
- **70.7% is still not a publishable label.** It is the first number in this investigation
  that would be worth improving rather than abandoning.
