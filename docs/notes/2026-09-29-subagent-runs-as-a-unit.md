# Subagent runs: the structure, and the experiments it makes possible

**Date:** 2026-09-29. **Status:** structure MEASURED on two corpora; experiments PROPOSED,
none run. Nothing here is a result about characterisation quality.

## Terminology, from the data rather than invented

Claude Code writes each subagent dispatch to **its own transcript file**, `agent-<agentId>.jsonl`.
Every record in it carries `isSidechain: true`, its own `agentId` (matching the filename), and
the **parent `sessionId`**. Call the unit a **subagent run**, identified by `agentId`.

⚠️ `sourceToolAssistantUUID` looks like a link to the dispatching call and **is not** — it
resolves inside the run's OWN file, 120/120 times, and never in the parent. The parent linkage
is `sessionId` plus time ordering.

## The structure, measured on both corpora

| | CORPUS A | CORPUS B |
|---|---|---|
| session files | 218 | 59 |
| **subagent runs** | **187** (46% of files) | **290** (83% of files) |
| first record is `type: user` | 187/187 | 282/290 |
| brief length, median | 2,990 chars | 4,670 chars |
| duration, median | 2.6 min | 7.2 min |
| **runs fitting inside one 20-min block** | **97%** | **80%** |
| requests/run, median (p90) | 13 (41) | 29 (118) |
| briefs opening `"You are …"` | 160 (86%) | 221 (76%) |

**Zero sidechain records appear inside session files** — subagent work is entirely separate,
which is why it is invisible to anything that reads a session transcript alone.

## Why this matters: a run is a semantically delimited unit and a block is not

A 20-minute block is an arbitrary time cut. A subagent run has:

- **a stated GOAL** — the first record is always the brief, and 76–86% of briefs open with an
  explicit `"You are …"` role sentence;
- **a bounded EXECUTION** — median 13 / 29 inference requests;
- **an observable DELIVERABLE** — the final assistant message handed back.

⚠️ **This is the exact deficiency that killed block-level classification.** User text is 7.0%
of a block and the rest is narration, which is why the user-only arm collapsed to 0.140 and
why GLiNER2 hit a +0.117 ceiling on block prose. **A brief is ~100% intent**, authored
deliberately, several thousand characters long. For calibration, Keld's shipped `task_type`
facet measures **0.733 on prompts** — and a brief is a prompt, a long and unusually
well-formed one. The unit that failed had no intent statement in it; this one leads with one.

⚠️ **And the block cutter SPLITS runs**: 20% of corpus B's runs exceed 20 minutes, so their
work is divided across two arbitrary time buckets with no marker saying it was one task.

## Proposed experiments, in dependency order

**E1 — Can a run be characterised from its BRIEF alone?**
Blind-label a stratified sample of briefs; score a classifier against them. This is the
well-posed version of the question that failed at block level, and it is cheap: the brief is
short, self-contained, and states the task. ⚠️ Pre-register the vocabulary and commit labels
before any arm runs, as with every prior study here.

**E2 — Does the brief PREDICT the run's actual activity mix?** *(the valuable one)*
We already have the deterministic per-request classifier, so each run's true activity
distribution is computable with no labelling at all. The question is whether the brief
predicts it. If it does, work can be characterised **at dispatch time, before it runs** —
which is a routing capability that does not exist today, and a far cheaper place to route
than per request. The gold needs no human: it is the run's own measured distribution.

**E3 — Does a SESSION have a coherent characterisation, or is it inherently a mixture?**
Worth knowing before any session-level surface is designed. The honest prior from this
project is that it is a mixture — which would make the session a container to be reported as
a distribution, never a labelled thing.

**E4 — Is the run a better published unit than the block for agentic work?**
Runs are 46% / 83% of transcript files and carry 41.5% / 70.6% of all inference requests.
A unit with a goal and a deliverable beats an arbitrary time cut, and E2 would supply the
evidence either way.

## Caveats to carry in

- **Both corpora use subagent-driven workflows heavily.** The `"You are …"` brief shape is
  partly a skill convention; a team that dispatches subagents ad hoc would have terser briefs.
  A third corpus is owed here as much as it is for the request classifier.
- **8 of corpus B's 290 runs do not start with a `user` record.** Small, but it means "the
  first record is the brief" is a strong regularity, not an invariant, and code must not
  assume it.
