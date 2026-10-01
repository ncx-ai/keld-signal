# Document-type recognition — PARKED before scoring, and why

**Date:** 2026-10-01
**Status:** parked at Task 2 of 5. Tasks 3-5 (signature table, scorer, results) were never run.
**Spec:** `docs/superpowers/specs/2026-10-01-document-type-recognition-design.md`
**Plan:** `docs/superpowers/plans/2026-10-01-document-type-recognition.md`

## What it was testing

Whether externally-authored structural signatures — an ADR's `Context`/`Decision`/
`Consequences`, Keep a Changelog's `### Added`, RFC 2119's `MUST` — can recognise what KIND of
document was written. The reframe's appeal was that document type is a FACT about an artifact
rather than a judgement about purpose, so hand labels would be close to ground truth where
every domain label we tried was circular.

## Why it stopped: the corpus cannot test it

Task 1 extracted 372 documents the agent authored. Task 2 labelled 60 of them blind. The
labels are committed beside this note as the evidence.

**1. Half the frame is the agent writing about its own work.** 177 of 372 (48%) are process
artifacts — 132 `task-N-report.md`, plus briefs, progress files and fix reports from earlier
subagent-driven runs. In the 60-document sample that is 44 `other` (73.3%), which puts the
majority-class baseline at 73.3% and the pre-registered bar at ≥93.3% accuracy. Effectively
unreachable, and it would have been measuring the wrong thing anyway.

**2. Six of the eight types are absent.** Searching all 203 non-process documents for both a
filename hint and a heading hint:

| type | documents with both |
|---|---|
| plan | 38 |
| design_spec | 20 |
| readme · adr · changelog · runbook · postmortem · meeting_notes | **0** |

Re-sampling cannot conjure types the corpus does not contain.

**3. ⚠️ And the two that remain are the wrong two.** The plans and design specs here were
written by Claude following this repo's own superpowers skills — `## Goals`, `## Non-Goals`,
`### Task N`, `- [ ]` checkboxes. Those conventions are OURS. A signature table for them would
be transcribing our own tooling's formats, which is exactly the contamination the spec said
separated this approach from the keyword lists that scored 52% on text their author wrote and
**17% on real paths**.

So the study would have produced a number, and the number would have meant nothing.

## What was kept

- `scripts/doctype_frame.py` — the authored-document extractor. Two review rounds found a
  frame that looked reproducible but was not (selection followed `os.walk` order, affecting
  19 of 372 documents) and input loss that left no trace. Both fixed; it is sound and reusable.
- `scripts/doctype_sample.py`, `scripts/doctype-labels.txt` — 60 blind labels. Committed as the
  evidence for the finding above, not as a usable answer key.

## What replaced it

`docs/superpowers/specs/2026-10-01-conversation-domain-design.md` — the same question
(what KIND of work is this?) asked of the thing the repo owner actually wanted classified: the
USER AND AGENT CONVERSATION, against WildChat, which has 1,137 conversations carrying a
professional-domain signal across six domains.

⚠️ **That corpus also corrects a claim this session repeated several times.** "Public chat data
does not contain the work — 28 candidates in 59,857, of which ~1 was genuine" was measured with
a filter looking for work matching the atv1 taxonomy specifically. Asked the broader question —
what is this conversation about — the same shard yields **1,137 conversations, 40x more**:
marketing 350, medical 281, legal 179, hr 179, sales 145, financial 62. The original figure was
not wrong, it answered a narrower question than the one that mattered.
