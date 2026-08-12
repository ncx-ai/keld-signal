# The qualitative review harness — blind packets, a calibration set, and a scorer

Status: **built and cut, unreviewed.** Round `r1` exists; no verdict has been returned. Nothing
in this document reports a review result, because none has happened yet.

## Why this exists

Findings Part 9 (`docs/superpowers/plans/2026-08-07-conversational-dimensions-findings.md`,
"What in this harness measures a fact, and what encodes a judgement") splits this study's
metrics in two. The facts — digests produced, recovered panics, prompt-within-budget, beat and
retain-list counts, window coverage, document frequency, T4 verbatim retention — stay on the
string apparatus and stay green. The judgements do not: T3, T7, T8, T9, T10, T11, T12,
`ChangedSubject`, `SubjectShifted`, `beatsRestate` suppression and "is this token a specific" are
each a significant-word overlap or a stopword lookup standing in for a semantic relation, and
each has now failed in a way that cost more to diagnose than the thing it measured. Unverified
identifiers flagged `Key`, `Initial` and `e.g`. Leak detection flagged only the sentinel the
model is instructed to emit. Plain plurals scored as fabrication. T1 reported 100% while
silently dropping 5 of 20 digests.

The judged half therefore moves to a reader: a stronger model reviewing the output against its
source, as the manager of the person doing the work. A reader is an instrument like any other, so
it has to be calibrated — which is what the planted-defect and clean-duplicate items here are
for — and its criterion has to be written down and kept in the repository, which is what
`internal/agent/enrich/llmstudy/review/reviewer-dispatch.md` is.

**This harness does not perform reviews.** It emits blind packets and scores verdicts. Dispatch
is the coordinator's.

## What a packet is

One item presented for judgement, as markdown, containing exactly three things:

1. **the measured record** — the authoritative counted block, verbatim;
2. **the conversation window** — the slice of transcript the writer saw, verbatim;
3. **the statement under review.**

Nothing else. No arm, no commit, no session id, no generation order, no other item's output, no
indication whether the item is real. Two omissions are worth naming because the source document
displays them prominently: the `window 25 of 72` coordinates (they leak session length and
generation order) and the `marked SUBJECT CHANGED` annotation (it is one of the heuristic
verdicts under comparison, so showing it would ask the reviewer to agree with it).

Withholding is verified two ways, over the files on disk rather than the structs:

- **Structurally** — every emitted file must equal a render over its record, window and
  statement alone. This is the guarantee, and it holds for fields added later that no substring
  search would know to look for.
- **By grep** — every answer-key value that can be searched for is searched for in every packet.
  Because the structural check holds, a hit is either inside the evidence the writer legitimately
  saw (recorded as a *coincidence*: the word "genuine" occurs in seven of these transcripts) or
  it came from the packaging (a *hit*: fatal, and the round is not dispatched). Round r1: 60
  values grepped, **0 hits**, 7 coincidences, 0 structural mismatches. The values deliberately
  not grepped for — packet ids, the mutated span, its absent tokens and signature, verdict words
  under four runes — are listed with a reason each in `withheld/leak-check.json`, because a check
  that silently narrows what it looks for is the failure being replaced.

Packet ids are salted hashes of provenance, and files are written in id order, so the directory
listing carries no information about kind and the same corpus regenerates the same round byte for
byte.

## The calibration set

Planted items are **mutations of real outputs**, one defect each. A defect invented from scratch
reads as synthetic and gets caught for its register rather than its content, so `Apply`
(`review/mutate.go`) rewrites an exact span of a genuine statement and verifies every property
the class claims:

| class | what is planted | verified |
|---|---|---|
| `fabricated_identifier` | a real path/symbol swapped for a plausible absent one | the substitute is absent from window **and** record |
| `invented_blocker` | an obstacle the window does not support | the replacement reads as an obstacle (checked vocabulary) |
| `unobservable_completion` | progress or near-completion the window cannot show | the replacement asserts completion (checked vocabulary) |
| `subject_drift` | the work attributed to a subject from a **different** session | the subject is absent here and **present there** |
| `sourceless_specificity` | a number or proper noun from nowhere | absent from window and record |

Also enforced for every mutation: the span occurs **exactly once** in the source statement (or
"the exact span mutated" is not a fact, and the scorer's located-the-defect test keys on it); the
result stays within a length band of the genuine statement (35%, floor 60 runes) so it cannot be
spotted by shape; the result still ends at a sentence boundary, per the repository's delimiter
rule; and the replacement introduces at least one word of four runes or more that the genuine
statement did not use, so a reviewer has something to quote.

**Clean duplicates** are unmutated genuine items re-emitted under a fresh id, byte-identical.
Without them the round measures sensitivity and reports it as accuracy: a reviewer who flags
something in every item catches every plant and looks perfect. They also give the one strict
self-consistency test in the round — the same reviewer, the same statement, two ids.

Round r1 composition, from `withheld/answer-key.json`:

- **46 packets** = **30 genuine** + **10 planted** + **6 clean duplicates**
- planted, 2 per class: `fabricated_identifier` 2, `invented_blocker` 2,
  `unobservable_completion` 2, `subject_drift` 2, `sourceless_specificity` 2
- corpus: 3 sessions, 30 statements parsed, 1 skipped (the document's one GENERATION FAILED beat)
- three of the ten plants are the accounting session, which is 4 of the 30 genuine items —
  over-represented on purpose, because a reviewer who only reads code is the failure mode the
  audience requirement cares about and it is invisible in a corpus-weighted sample.

## The rubric

`internal/agent/enrich/llmstudy/review/reviewer-dispatch.md`, pasted verbatim with three
placeholders substituted. Five dimensions, each an explicit `pass`/`fail` and never a score:
`faithful`, `not_rubberstamping`, `legible_to_a_manager`,
`recognisable_to_the_practitioner`, `domain_neutral_specificity`. They are derived from what the
retired checks were reaching for; the fifth exists because the corpus includes non-engineering
sessions and a rubric that only works on code fails the audience requirement — for a month-end
close, "reconciling the March ledger for the Meridian entity" is fully specific, and demanding a
filename there is a category error.

**Every verdict must carry evidence**: either a `quote` copied verbatim from the packet's
evidence, or an `absent` list of strings claimed to appear nowhere in it. Both are checked
mechanically, and an unevidenced verdict is exactly the failure mode being designed out, so the
scorer detects and lists it. A quote is checked against the **evidence only** — quoting the
statement back is not evidence.

Two independent reviewers per packet, so disagreement is measurable: 46 packets → **92
dispatches**, listed row by row in `dispatch-plan.tsv` with the verdict path each writes to.

## The scorer

`REVIEW_SCORE_DIR=<round> go test ./internal/agent/enrich/llmstudy/review/ -run TestScoreRound -v`
writes `score.md` and `score.json` into the round. Every line is a count over its denominator;
there is no bare-rate type in the package, because T12 moved 15.7% → 25.0% while its denominator
moved 70 → 40 and no report could say which had changed. It reports:

- **Calibration** — per class: reviews that located the planted span, items caught by either
  reviewer and by both, defects claimed at all, and the class named correctly. A class no
  reviewer located is printed as a **BLIND SPOT**; a class with nothing planted is printed as
  **NOT PLANTED — untested, not clean**; a class whose items got no verdict is printed as
  **unmeasured**. Every missed item is listed with the span that was missed.
- **False positives** — defects claimed on genuine items and on clean duplicates, **separately**,
  plus the same-reviewer-same-statement contradiction count and per-dimension fail counts on
  clean items.
- **Inter-reviewer disagreement** — per dimension, over packets reviewed twice, plus the defect
  call itself as a sixth row.
- **Judge versus heuristic** — the judgement-class checks run **one more round** for this table
  (none of them is deleted or disabled; retiring them is a later decision this number informs).
  Per heuristic: a 2×2 of judge-flags against heuristic-flags with abstention as its own column,
  the disagreeing packets by id, and what each flag actually flagged.
- **Unevidenced verdicts**, counted and listed, split into `unevidenced`, `quote_not_found`,
  `absent_token_present`, `malformed_verdict` and `missing_dimension`.
- **Problems** — an unparseable verdict file, a verdict for a packet not in the round, a reviewer
  returning two verdicts. Nothing is dropped in silence; that is how T1 reported 100%.

## Where the round lives, and why it is not committed

    .superpowers/sdd/2026-08-11-qualitative-review/
      README.md              coordinator guide (deliberately carries no composition counts)
      packets/               PKT-*.md + manifest.json
      reviewer-dispatch.md   the prompt as it stood when the round was cut
      dispatch-plan.tsv      92 rows
      verdicts/              verdict JSON lands here
      withheld/              answer-key.json, leak-check.json, README.md — NOT for reviewers

That tree is under `.superpowers/sdd/`, which is gitignored. The packets quote
`docs/qwen-inputs-and-outputs.md`, which is the project owner's and untracked: committing the
packets would commit that document by another route. The code, the calibration set and the rubric
are committed; the cut round is reproducible from them with

    REVIEW_EMIT_DIR=<dir> REVIEW_ROUND=r1 \
      go test ./internal/agent/enrich/llmstudy/review/ -run TestEmitRound -v

The answer key is outside `packets/` and carries its own README saying no reviewer may read it. A
dispatched reviewer is given one packet path and the rubric, and nothing else.

## What the heuristics said about this round, before any review

Recorded now so the comparison has a baseline that predates the verdicts. Counts over the 46
packets, from the answer key:

- `beat_contradicts_record` (T12) — flagged **14 of 46**: **4 of 10** planted, **10 of 36**
  clean. It abstained on 22 of 46. Consistent with Part 9: it flags accurate statements, mostly
  on real identifiers the record simply does not list (`turn-row.tsx`, `localStorage`,
  `noreply.keld.co`).
- `unverified_specifics` (the "is this token a specific" gate, run over the statement as prose) —
  flagged **6 of 46**, and all six are planted: **6 of 10** planted, **0 of 36** clean. On this
  corpus it is the one judgement-class check that looks like an instrument.
- `subject_shifted` — flagged **0 of 46**. On adjacent-window beats it fires never, where the
  sweep's four-apart refinements had it firing 41 of 42. It will contribute nothing to this
  round's comparison, and that is a fact about the check, not about the reader.
- `beat_restates_previous` — flagged **0 of 46** (5 items have no prior statement).
- `changed_subject_recorded` — changed on 23 of 46, unchanged on 13, and 10 inherited from a
  source item. **Recorded, not recomputed**: it is decided on the mined `Window`'s grounded
  subject terms, which a rendered window cannot reconstruct, so re-deriving it would compare the
  reader against a lookalike. A planted item carries its source's annotation, labelled inherited,
  and is not evidence about the mutated statement.

## Limits, stated rather than discovered later

1. **Genuine items are not certified defect-free.** A defect claimed on a genuine item may be a
   real finding the harness never planted. Only the clean duplicates are controls in the strict
   sense, and what they measure is self-consistency. The scorer's column is therefore named
   "defects claimed on genuine items"; reading it as a pure false-positive rate would be reading
   more than it says.
2. **Two plants per class.** Enough to distinguish "never caught" from "one off item", not enough
   to separate a blind spot from a hard item. A class at 0 of 2 is suggestive; it needs a second
   round before it is established.
3. **Absence is verified as a string, case-insensitively.** An `absent` token is checked as a
   substring of window+record, not as a concept, so a paraphrase-supported claim could in
   principle pass the check. This is why `invented_blocker` and `unobservable_completion` — the
   two classes whose defining property is not lexical — are anchored by a checked vocabulary and
   an author's note instead, and it is the weakest joint in the calibration set.
4. **A very short fabricated identifier cannot be planted.** The located-the-defect signature
   keeps only words of four runes or more, so a three-rune substitute is rejected at emission.
   Pinned by a test, not left to be found.
5. **Locating is lexical.** A reviewer who describes the defect entirely in their own words
   without quoting any word the mutation introduced scores as a miss. The scorer looks in the
   defect quote, the reasons, the named unsupported claims and the absence lists to make that as
   unlikely as it can be, but it is a floor on the measurement, not on the reader.
6. **Clean duplicates are byte-identical**, so a reviewer with memory across dispatches would
   recognise the second copy. Each dispatch must be a fresh reviewer.
7. **The corpus is 30 statements over 3 sessions**, one of them hand-authored, two of them
   software. Domain neutrality is exercised, not established.
8. **The rubric's own yield is unmeasured.** No review has been run, so nothing here says the
   five dimensions discriminate or that two reviewers disagree at a usable rate. The first scored
   round is what answers that, and it is the point of the calibration set.
