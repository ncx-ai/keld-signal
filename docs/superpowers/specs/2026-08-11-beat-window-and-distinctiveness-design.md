# Beat windows and what counts as a subject

Two changes, specified together because they are the same question asked twice: **what does a
beat see, and which of the words in it name the work?**

Neither is scheduled yet. Both wait on the in-flight prose-untruncation measurement, because
changing beat inputs mid-flight would confound its before/after sweep.

> **Part 1 is BUILT AND MEASURED** (`66042cc`, then `ad174a6` / `bbe4ff7` / `5b184b9`). One of its
> requirements — 100% turn coverage — was measured to be **unachievable at `ctx` 8192** and is
> refuted below rather than met. Part 2 is separate work. Read *Part 1, measured* before quoting
> anything from *The change*.

## Part 1 — beats need their own window geometry

### What is wrong now

`Mine` emits one window per user prompt, each holding the last `K = 12` **turns** — where a
turn is a user message, an assistant message, or a tool invocation. Beats fire every 5 user
prompts. Five user prompts typically span 25–50 turns, so:

- **Consecutive beat windows are disjoint.** No shared ground, so no continuity between
  consecutive beats, which is why the series reads as periodic snapshots rather than an
  account.
- **Most of the transcript is never read by any beat.** With ~30 turns between beats and 12
  turns per window, roughly 18 of every 30 turns are invisible. The timeline has holes, and
  nothing reports them.
- **`ChangedSubject` cannot work.** It is asked whether the subject changed, while being shown
  a window that shares nothing with the previous one. Everything looks new. Measured at 100%
  before its last fix and 56% after — and the residual under-reporting is likely this, not the
  comparison rule.

The cause is inheritance, not a bug: `K = 12` was chosen for *classification*, where a window
exists to judge one prompt in context. A narrative has a different requirement, and the
geometry was never revisited when windows were reused for beats.

### The change

Give beats a window defined by their own stride:

- **Contiguous coverage.** A beat's window is every turn since the previous beat's window
  ended. Nothing in the transcript is skipped by construction, which is a stronger property
  than the current design has at any `K`.
- **A deliberate stride overlap** — the last ~25–30% of the previous window carried into the
  next. Consecutive beats then share ground, so a change of subject is distinguishable from
  merely being shown different material.
- **Bounded by characters, not turn count.** Turn sizes vary by two orders of magnitude here —
  a one-line tool invocation versus a long analytical message with tables — so a fixed turn
  count produces wildly uneven windows. The existing `PerTurnChars` / `WindowChars` bounds are
  the right unit; `K` is not.
- **When coverage exceeds the character bound**, drop from the *oldest* end and say so in the
  window, the way `omittedNotice` already does. A beat that silently skipped material is the
  defect this change exists to remove.

**This does not weaken the no-chain invariant.** The overlap is *transcript*, not model output.
Two beats sharing source turns cannot compound drift the way re-reading a previous summary
does — which is precisely why the previous report-paraphrase design was removed and this is
safe.

### What to measure

- Turn coverage: the fraction of the session's turns that appear in at least one beat window.
  Must be 100%; it is currently far below that and unmeasured.
- Overlap between consecutive windows, as a percentage — currently ~0%.
- `ChangedSubject` rate before and after, with the beat-1 → beat-2 case from this session's
  series as a named check.
- Prompt size: a wider window costs budget. The backstop must still report zero panics on the
  real-corpus probe.

### Part 1, measured

`TestCorpusBeatWindowGeometry`, offline over the pinned snapshot
(`/home/dg/keld/study-corpus-snapshot-2026-08-11T2130/projects`), 20 whole sessions, 137 beat
windows, both geometries computed on the same walk. Counts lead; every denominator here moves
between the geometries.

| | OLD (`K = 12`) | NEW (contiguous, 16,000 runes) |
|---|---:|---:|
| turns read by some beat window, of 14,154 spanned | **1,567** (11.1%) | **7,926** (56.0%) |
| turns read by NO window | 12,587 | 6,228 |
| windows carrying a marked hole | n/a — holes were unreported | 75 of 137, **75 of 75 marked** |
| overlap with the previous beat's window | **2,960** of 379,587 runes (0.8%) | **381,272** of 1,870,860 runes |
| — as a share of what the previous beat READ | 0.8% | **23.7%** (spec asks 25-30%) |
| largest assembled beat prompt, budget 24,000 | 7,695 | **18,047** (5,953 headroom) |
| budget backstop fired | — | **0 times in 137 assemblies** |

The old numbers are computed on the same corpus, not remembered, and by a method that can only
**overstate** them: a stride turn counts as read if it matches, as a multiset, any turn of any
mined beat window of that session.

⚠️ **"Coverage must be 100%" is refuted, and not narrowly.** The largest stride is 52,148 runes;
20,000 runes of real transcript measures 5,433 tokens (`llama-server /tokenize`, worst of four
chunks), so a whole-stride window needs ~14,200 tokens against a context of 8,192 that must also
hold the record, the instructions and the generation. 100% coverage and a bounded window are
mutually exclusive here. **The price is measured rather than argued:** firing an extra beat
whenever the uncovered stride reaches the bound reaches 100% at **285 beats instead of 137 — 2.1x
the inference on the tier that already dominates sweep cost.** That is the trade to decide, not a
defect to fix silently. Until it is decided, the shortfall is 6,228 turns and every one of them is
inside a window that says so.

Two things the measurement corrected in the implementation, both of the class this branch keeps
paying for:

- **The hole marker was in the window and charged to nobody**, so a stride that filled the bound
  produced a window over it. Reverting the fix puts five real windows over the bound (keld-signal
  i19 +10, i24 +24 and +84, i29 +79, keld-cli i9 +38). The existing drop test *did* assert the
  bound — its fixture just left hundreds of runes of slack, so it passed for a reason unrelated to
  the arithmetic. A test asserting a limit the code does not apply is exactly what put five wrong
  worst-case numbers into this branch's record.
- **The overlap was reported against the previous STRIDE while its doc claimed the previous
  WINDOW**: 16.9% versus 23.7% on the same 117 pairs. The design was inside the spec's band and
  reporting itself outside it.

**What this does NOT measure is the series-level effect, and the reason is worth recording.** The
blind series round (`.superpowers/sdd/2026-08-12-series-review`) failed `continuous` on 6 of 6
clean-series reviews and 6 of 6 clean duplicates — every timeline, both reviewers. Those timelines
were generated **on the old geometry**: the artifact's own numbers say so (30 windows, min 70 /
median 2,227 / max 6,734 runes, zero hole markers, beats at windows 0/5/10/15), where the
contiguous geometry means 13,655 runes and 75 of 137 windows carrying a marker. `66042cc` migrated
the sweep, the report dump and the arms dump; it missed `beat_dump_test.go` and
`digest_dump_test.go`, and the review corpus came from the former. `bbe4ff7` retires `K` there
too. **So the 6-of-6 continuity failure is not evidence about this geometry — it is evidence about
the geometry this change replaced, and re-measuring it needs a fresh timeline round.**

## Part 2 — a real distinctiveness rule

### What is wrong now

`distinctiveToken` accepts a token if it is a strong identifier **or is at least 7 characters
long**. That second clause admits ordinary English, and it is the root cause of four separate
defects already recorded on this branch:

- `SessionRecord.Subjects` — labelled *authoritative* and fed to the model with the instruction
  "use the record to place the work" — contains `control`, `question`, `exactly`, `failure`,
  `1.00` and a full absolute path. Measured: 8 of 12 terms are not subjects.
- **T12 (beat-vs-record consistency) is unusable.** Its flags are accurate beats; the
  populations never intersect because one side is noise.
- **T11 (synopsis lag) is near-tautological.** An unrelated synopsis scores a match on
  "remains" and "whether", and the check only fires at zero matches.
- **`SubjectShifted`** fired on 41 of 42 refinements.

A stopword list does not fix it. `failure`, `control`, `question` are content words, not
function words — they are *generic*, not *stop*. The discriminator needed is specificity, and
specificity is not a property of a word in isolation.

### The change: document frequency over the local corpus

A term that appears in many different sessions is generic; a term concentrated in few sessions
names that session's subject. That is inverse document frequency, it is the standard approach
to exactly this problem, and it is computable on device from transcripts already on disk.

- Compute document frequency per term across the local transcript corpus — how many *sessions*
  contain it, not how often it occurs. Counts only; no text leaves the machine and nothing is
  stored but a term→count table.
- A term is distinctive when its DF is below a threshold. `GLiNER2`, `Meridian`,
  `depreciation` appear in few sessions; `control`, `question`, `failure`, `remains` appear in
  nearly all.
- Keep `strongIdentifier` as an independent sufficient condition — a path or a dotted name is
  distinctive regardless of frequency.
- **Retire the ≥7-character clause entirely.** It is the defect.

**Why this beats a hand-tuned word list:** it adapts to the user's actual domain. For an
accountant `depreciation` and `accruals` are subjects and would survive, while a hand-built
engineering stoplist would never have contained them. This is the same reason the design
insists label vocabularies be readable descriptions rather than bare ids — the discriminator
has to fit the material.

### The cold-start problem, and the precedent for handling it

A new machine has one session, so DF is meaningless. `lenstat` already solves this shape: stay
at a liberal default until `KELD_ENRICH_LEN_MIN_SAMPLE` observations make the estimate
representative. Do the same — until enough sessions exist, fall back to identifier-shape alone
(precise but narrow) rather than to the ≥7-character rule (broad and wrong). Prefer admitting
too few subjects over admitting noise, because noise is what is currently poisoning a block
labelled authoritative.

Optionally ship a small precomputed DF table for common English and common technical vocabulary
so a first session is not defenceless. Measure whether it is needed before adding it.

### What to measure

- On the three corpus sessions: how many extracted subjects a human would call subjects.
  Currently 4 of 12 on this session. This is the headline number.
- **T11 and T12 re-measured.** Both are currently unusable *because of this rule*; if the fix
  is real, both become meaningful for the first time, and if they do not move, that is a
  finding about the checks rather than the tokeniser.
- `SubjectShifted` rate, which was 41 of 42.
- Whether the accounting session's subjects survive — `depreciation`, `accruals`, `Meridian`,
  `Larkin` — since a rule that only works for code would fail the audience requirement.

## Why these two are one piece of work

`ChangedSubject`, `SubjectShifted`, T11 and T12 all ask "did the subject change", and all four
are degenerate today. Two independent causes: the windows share no ground to compare, and the
notion of "subject" admits ordinary English. Fixing either alone leaves the signals broken, and
the measurement would not show which cause was which. Doing both together, with the
before/after on the same corpus, is the only way to attribute the result.

## Risks

**DF needs a corpus and the corpus is the user's own sessions.** On this machine there are 537
transcripts across 14 projects; on a new install there is one. The cold-start fallback must be
the narrow rule, not the broad one, or a first session gets the current behaviour and the defect
ships to exactly the users least able to notice it.

**A wider beat window costs prompt budget**, and the budget currently has +0 runes of slack at
realistic input scale. The backstop converts an overrun into a loud failure rather than silent
truncation, so this cannot corrupt output — but it can abort a run, and the real-corpus probe
must be re-run rather than assumed.

**Overlap is not free.** Carrying 30% of the previous window means the same turns are read by
two beats, which may make consecutive beats restate each other — the suppression path
(`beatsRestate`) exists for that and is currently inert, having discarded 0 of 70. If overlap
starts producing restatements, that is the mechanism working, not a regression, and the
discard count is the measure.
