# The production beat, re-measured on a real-majority corpus

The first production sweep is **retracted**, not compared against. Its worked examples were
invented out of a session that was itself in the eval corpus, and half its corpus was
hand-authored, so neither its figures nor its zero-firings guard mean anything. This run replaces
it: the artifact at `docs/qwen-beat-inputs-and-outputs.md` and the dump at `beat-run.json` are
overwritten, not set beside it.

**Nothing here judges the output.** A blind round scores it against a metric this document does
not own. Every figure below is a count over the run's own observations, and each was read against
the items behind it before it was written down.

⚠️ **No latency or RAM figure appears anywhere.** The server ran `--threads 18` without
`--no-repack`, which is not the viability configuration, so any resource number taken from this
run would misrepresent the case. That question needs its own run at the viability flags.

## The worked examples, and how the hold-out was verified

All three are read from real transcripts that are **excluded from the corpus** (`beatHeldOutSessions`):

| Example | Shape it teaches | Read from |
|---|---|---|
| `the daily-jar rewrite of seat_capex_split` | work done, and a check that passed | `c2019c5e-…a3.jsonl`, window 4 |
| `batched enrichment writes on the ingest path` | **nothing was finished** — raised, argued, an approach picked, nothing built | `aa59ef4c-…ac.jsonl`, window 4 |
| `the demo seed and its signal clients` | work done beside something deliberately left alone | `51476fbe-…26.jsonl`, window 4 |

There is still **no forbidden-phrase list**; naming a phrasing summons it (measured, 2 → 4).

**Verified mechanically, not by intention** (`TestBeatExamplesAreHeldOut`). It assembles the
windows and records the sweep actually shows the model — all 14 sessions, 841,888 runes — and
fails if any example's subject line, or any strong identifier or capitalised term any example
uses, occurs in it as a case-insensitive substring. Ordinary English is deliberately out of scope:
requiring "written" or "picked" to be absent from a twelve-session corpus is impossible, and every
measure on this branch that reached for ordinary English ended up measuring English.

**Revert-and-fail, on real input:** restoring the previous examples makes it fail on
`fa-register.csv`, `Meridian`, `March`, `Atlas` and `CSV` — the actual contamination, named.

## Corpus composition

12 real transcripts across 3 projects (keld-atlas 7, keld-signal 4, keld-cli 1), 65 beat windows;
2 hand-authored sessions, 7 beat windows.

⚠️ **Two of the twelve are the same conversation.** `129e9a80` and `16c69396` are byte-identical
over all six of their beat windows and their measured records — a forked or resumed session that
got a second id, which `StratifiedTranscripts` does not detect. So the real corpus is **11
distinct conversations, 59 distinct beat windows**, and six beats are the same work counted twice.
Where that matters it is called out below rather than divided out silently.

| session | project | mined windows | beats asked | generated |
|---|---|---|---|---|
| `0ac739ad-…90.jsonl` | keld-signal | 34 | 6 | 6 |
| `02fd750d-…41.jsonl` | keld-atlas | 60 | 6 | 6 |
| `bf277ad6-…d8.jsonl` | keld-cli | 44 | 6 | 6 |
| `129e9a80-…39.jsonl` | keld-signal | 31 | 6 | 5 |
| `12c80ab4-…9b.jsonl` | keld-atlas | 25 | 5 | 5 |
| `16c69396-…1b.jsonl` | keld-signal | 39 | 6 | 5 |
| `13695eef-…0b.jsonl` | keld-atlas | 20 | 4 | 4 |
| `139be731-…bc.jsonl` | keld-atlas | 20 | 4 | 3 |
| `43492104-…67.jsonl` | keld-signal | 72 | 6 | 6 |
| `1a3aa6d2-…5c.jsonl` | keld-atlas | 39 | 6 | 6 |
| `2927f65b-…93.jsonl` | keld-atlas | 21 | 4 | 4 |
| `32b52295-…db.jsonl` | keld-atlas | 33 | 6 | 6 |
| **`finance-close`** | **SYNTHETIC** | 20 | 4 | 4 |
| **`marketing-launch`** | **SYNTHETIC** | 19 | 3 | 3 |

## What the two populations did, separately

| | real (12) | synthetic (2) | together |
|---|---|---|---|
| beats asked / generated / failed / panicked | 65 / 62 / 3 / 0 | 7 / 7 / 0 / 0 | 72 / 69 / 3 / 0 |
| attempts | **1×54, 2×3, 3×4, 5×4** | **1×7** | 1×61, 2×3, 3×4, 5×4 |
| entries offered / kept | 248 / 181 | 26 / 25 | 274 / 206 |
| dropped by the anchoring guard | **0 of 248** | **0 of 26** | 0 of 274 |
| dropped to fit the 512-rune cap | 67, across 46 of 62 beats | 1, across 1 of 7 | 68, across 47 of 69 |
| subject carrying no term from the evidence | 3 of 62 | 0 of 7 | 3 of 69 |
| anchored in the record but not the window (seam) | 0 of 181 | 0 of 25 | 0 of 206 |
| names taken from the prompt's own examples | 8, across 3 of 62 | 0 | 8, across 3 of 69 |
| turn coverage | 4,021 of 5,681 (70.8%) | 104 of 104 (100%) | 4,125 of 5,785 (71.3%) |
| largest assembled prompt | 18,188 of 24,000 | 4,261 | 18,188; **0 over budget, 0 panics** |

**The split is the finding.** `19 of 19 on the first attempt` did not survive the rebalance:
on real transcripts 8 of 62 beats needed more than one attempt and 3 were lost outright, while the
synthetic pair generated 7 of 7 first time with one entry dropped between them. The two
populations are not the same measurement, and averaging them describes neither.

**All three failures are the 200-rune entry cap**, on real transcripts, after five ladder
attempts each: a 235-rune entry twice — the same window in the duplicate pair, so **2 distinct
failing windows** — (`"The staleness defect in the digest system was identified and fixed by
introducing prompt-level accounting rules …"`) and a 219-rune one. Each is a single
long sentence, not a paragraph — the shape the cap was raised from 130 to 200 to admit. Raising it
again would only move the loss to the beat cap.

## The anchoring guard fired zero times, and that is what it is

**0 of 274 entries, 0 of 69 beats, on messier real material than it has ever seen.** That is not
evidence of grounding and it is not presented as any: substring presence over a 16,000-rune window
is easy to satisfy, and the guard's sensitivity outside its unit tests remains **unmeasured** for
the second run running. There are no dropped bullets to show, because there are none.

What the run does show is a case it demonstrably does **not** catch. Three beats — **2 distinct windows**, one of them the
duplicated pair, all on material whose subject matter is itself prompt and digest work — are
near-verbatim copies of the prompt's worked examples:

```
the daily-jar rewrite of seat_capex_split
- the phase-1 plan was rewritten around the daily-jar model after the explainer document landed
- seat_capex_split was rewritten to settle each closed day, and the collateral suites passed unchanged
- batching the enrichment writes was raised, and the stream-and-consumer approach was picked out of three, gated on KELD_ENRICH_INGEST_MODE
- signal_clients.py was written, and four seeded members were left without a device
```

Every entry anchored — on `plan`, `each`, `enrichment`, `written`. Anchoring is an OR over an
entry's terms, so an entry that copies an instruction still passes on the ordinary English it
carries. **This is only visible because the examples are held out**: an example drawn from the
corpus supplies names the window also has, so against the previous set a copied beat would have
read as a correctly anchored one.

One measure did catch them, exactly: `SubjectAnchored`, recorded and never enforced, is false on
those 3 beats and on no others — 3 of 3. Enforcing it (fail the beat, re-request at a wider
temperature) is the obvious next move and is deliberately not taken here, because it would change
what this run measured.

## Entries lost to the cap: measured, and left alone

**68 of 274 offered entries (25%) were dropped across 47 of 69 beats** — on real transcripts
alone, 67 of 248 across 46 of 62. This is not incidental: four entries at their 200-rune cap plus
an 80-rune subject is 880 runes against a 512-rune `BeatCap`, so the schema admits an answer that
cannot be stored whole. To keep every dropped entry, a beat would need up to **774 runes** (median
604 over the 47 beats that overflowed).

**The premise for leaving it was wrong, and the correction is the measurement.** Raising `BeatCap`
was believed to panic the prompt backstop, on the report tier's 4 runes of headroom. It does not:
setting it to 640 leaves the whole package green, because `fitDiscretionary` shrinks the beat
*selection* until the refine prompt fits. The cost lands somewhere quieter — the report reads
fewer beats. Measured at the realistic worst-case refine input the budget tests already use
(`TestBeatCapTradesBeatsInTheReportRatherThanTrippingTheBackstop`):

| beat size | beats reaching the refine prompt |
|---|---|
| 512 (`BeatCap`) | 4 of 12 |
| 640 | 3 of 12 |
| 774 (lossless) | 2 of 12 |

So the trade is **entries lost per beat against beats lost per report**, and buying back a quarter
of the entries costs half the beats a report can read under pressure. It is left at 512, for two
reasons: the report tier's own quality was measured at this value and moving it invalidates that
round without a fresh one, and the drop here is at whole-entry granularity and marked in the beat,
while the loss at the report tier is a whole beat vanishing from a timeline. That is a real cost
either way and it is recorded, not resolved.

## Geometry

4,125 of 5,785 spanned turns read by a window (71.3%); 1,660 read by none; 27 of 72 windows carry
a hole marker. Largest assembled prompt 18,188 runes of the 24,000 budget, **0 over budget, 0
panics, 0 recovered panics**. Two real sessions reached 100% coverage, one 47%.

## Concerns

1. **Instruction copying is real and unhandled** — 3 of 62 real beats (2 distinct windows),
   concentrated on material about prompt work. Held-out examples make it *visible*; nothing yet makes it *fail*.
2. **The anchoring guard has still never fired on real data.** Two sweeps, 0 of 274 here. Either
   it is measuring something that does not happen, or it is too lenient to measure it; the run
   above shows one shape it cannot catch by construction.
3. **The examples are all engineering now**, because the pinned snapshot holds nothing else. The
   prompt no longer steers a non-technical answer at all, so the synthetic pair carries the whole
   domain-neutrality question on 7 beats.
4. **Two of the twelve real sessions are the same conversation** (`129e9a80` and `16c69396`,
   byte-identical over all six windows), so the real corpus is 11 distinct conversations and 59
   distinct windows. Corpus selection has no fork/resume detection; a later sweep should dedupe
   on window content rather than on session id.
