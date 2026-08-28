# The production beat, re-measured after the cap, the guard and the subject rule

This **replaces** the sweep of the same name, which is retracted along with the one before it. It
measures three changes made because that run measured their absence:

1. **the beat cap now holds the answer the prompt asks for** (`BeatCap` 512 → 892) — the previous
   run discarded 68 of 274 offered entries (25%) across 47 of 69 beats;
2. **the anchoring guard reads specifics rather than any four-rune word** — the previous rule fired
   0 times across two sweeps and accepted `each` as an anchor;
3. **`SubjectAnchored` is enforced** by re-request rather than recorded — it flagged the three
   instruction-copying beats and nothing else.

The corpus is also deduplicated on window content, so 12 real sessions are 12 distinct
conversations rather than 11.

**Nothing here judges the output.** A blind round scores it against a metric this document does not
own. Every figure below is a count over the run's own observations, and each was read against the
items behind it before it was written down.

⚠️ **No latency or RAM figure appears anywhere.** The server ran `--threads 18` without
`--no-repack`, which is not the viability configuration, so any resource number taken from this run
would misrepresent the case. That question needs its own run at the viability flags.

## Corpus

12 real transcripts (keld-atlas 8, keld-signal 3, keld-cli 1), 65 beat windows; 2 hand-authored
sessions, 7 beat windows. The worked examples' three sessions remain held out, verified mechanically
against the corpus the sweep actually selects (`TestBeatExamplesAreHeldOut`, 14 sessions, 856,997
runes of window and record evidence).

**The fork/resume duplicate is gone.** `16c69396` is byte-identical to `129e9a80` over the walked
prefix and is now dropped at selection, with `36821116` admitted behind it. Fingerprinting the whole
session does **not** work — a fork shares its parent's history and then diverges, so that pair has
31 and 39 mined windows while the material the sweep reads is identical; the fingerprint is over the
walked prefix for that reason. The hold-out check runs the same selection function, so the newly
admitted session is checked for contamination rather than assumed clean.

## Every figure, three ways

| | real (12) | synthetic (2) | together |
|---|---|---|---|
| beats asked / generated / failed / panicked | 65 / 61 / 4 / 0 | 7 / 7 / 0 / 0 | 72 / 68 / 4 / 0 |
| attempts | **1×51, 2×3, 3×6, 5×5** | **1×7** | 1×58, 2×3, 3×6, 5×5 |
| entries offered / kept | 244 / 243 | 26 / 25 | 270 / 268 |
| **dropped to fit the beat cap** | **0** | **0** | **0 of 270** |
| dropped by the anchoring guard | 1 of 244, 1 of 61 beats | 1 of 26, 1 of 7 | 2 of 270 |
| kept entries naming no specific (unconstrained) | 65 of 243 | 11 of 25 | 76 of 268 |
| beats re-requested for an unanchored subject | 2 beats, 10 attempts | 0 | 2 beats, 10 attempts |
| subject still unanchored after the ladder | **2** | 0 | 2 |
| stored beats whose subject carries no evidence term | 0 of 61 | 0 of 7 | 0 of 68 |
| entries checked only against the record (seam) | 0 of 243 | 0 of 25 | 0 of 268 |
| names taken from the prompt's own examples | **0** | 0 | **0 of 68** |
| identifiers named that occur nowhere in the evidence | 8 across 7 of 61 | 0 | 8 across 7 of 68 |
| entries per stored beat | min 3 / med 4 / max 4 | 3 / 4 / 4 | 3 / 4 / 4 |
| stored beat runes | 324 / 582 / **774** | 322 / 410 / 459 | 322 / 578 / 774 |
| turn coverage | 4,386 of 6,181 (71.0%) | 104 of 104 (100%) | 4,490 of 6,285 (71.4%) |
| largest assembled prompt | 18,188 of 24,000 | 4,261 | 18,188; **0 over budget, 0 panics** |

## The cap: 68 entries dropped became 0

**0 of 270 offered entries were dropped for length, on either population.** The largest stored beat
is 774 runes against the 892-rune cap — exactly the figure the previous run named as the size a
lossless beat would have needed, now with 118 runes of headroom. The model still offers the full
four entries on nearly every beat (median 4, min 3), so the previous 25% loss was the schema's
arithmetic rather than the model's verbosity: four entries at the 200-rune entry cap beside an
80-rune subject renders to 892, and the cap was 512.

**What it cost, measured, not estimated:** at the realistic worst-case refine input, **2 of 12 beats
reach the report** where 4 did at 512
(`TestBeatCapTradesBeatsInTheReportRatherThanTrippingTheBackstop`). The trade is entries per beat
against beats per report and it was taken in favour of entries, because the timeline a person reads
back is the primary product and the report is derived from it. Nothing panics: `fitDiscretionary`
shrinks the beat selection until the refine prompt fits, which is why the earlier warning about the
report tier's 4 runes of headroom was wrong. `TestBeatCapHoldsTheAnswerTheSchemaAdmits` now pins the
three constants against each other, so the inconsistency cannot reappear silently.

## The guard: 2 drops of 270, both shown

The rule is now: every **specific** an entry names — an identifier-shaped token, a number or amount,
or a proper noun capitalised somewhere other than the start of its sentence — must occur in that
entry's own window or in the measured record; an entry naming no specific is unconstrained and
passes. Both drops, in full:

**real, `bf277ad6…d8.jsonl` window 14** — subject *"admin-web's PG query scalability under 100 keld
signal clients"*:

```
- the need for index optimization on the `install_id`, `severity`, and `timestamp` columns was
  discussed as a follow-up          → not in the evidence: install_id
```

The window carries the UI column **`Install ID`** and the route **`/signal/{installId}`**; it does
not carry `install_id`. The guard normalises case, simple plurals and possessives, but not separator
style, so a snake_case column name the conversation never writes is reported as named-from-nowhere.
That is the intended standard (verbatim occurrence) applied to a mild case, and it is shown rather
than argued.

**synthetic, `marketing-launch` window 14**:

```
- the landing page includes a one-line problem statement, a 15-second capture-to-sync
  demonstration, …                  → not in the evidence: 15-second
```

The window says *"a fifteen second capture-to-sync demonstration"*. The number is real and the
rendering is the model's. This is the one arguable drop of the two.

**Zero flags were ordinary English**, which was the risk: eight earlier measures on this branch
turned out to be measuring English. The rule was settled by replaying it over the previous sweep's
274 real entries before it was adopted — 12 flags there, 9 of them the instruction-copying entries
the old guard passed by construction — and two false shapes found in that replay were fixed:
plurals/possessives/case, and the `/`-joined compound (`installer/JSON`, `start/callback`,
`telemetry.py/_event_values`, `forest/amber`, `NULL/empty`, every part in its own window, only the
joining the model's).

**76 of 268 kept entries (28%) name no specific at all** and are therefore unconstrained. That is
the honest measure of the guard's reach and it is reported rather than folded into a pass rate: on
more than a quarter of entries the guard had nothing to check.

**The seam signal is 0 of 268.** No kept entry was checked only against the record, so dropping
window overlap cost nothing measurable here — the second run in a row it reads zero.

**The 8 unverified identifiers are the same normalisation, seen from the other side**:
`installer/JSON`, `start/callback`, `telemetry.py/_event_values`, `forest/amber`, `NULL/empty`,
`OAuth-first`, `LLM-based`, `CPU-based`. That measure is recorded and never enforced and has no
compound rule, so it flags exactly what the guard forgives. Both figures are printed; neither is
evidence of fabrication.

## The subject rule: 2 beats re-requested, 2 beats lost, 0 copies stored

**Instruction copying is 0 of 68 stored beats**, down from 8 names across 3 beats. It did not become
correct output: both windows that produced copies before produced them again, on all five ladder
attempts, and the beats were lost.

- `129e9a80…39.jsonl` window 9 — 5 attempts, 5 rejected, subject *"the daily-jar rewrite of
  seat_capex_split"*
- `43492104…67.jsonl` window 29 — 5 attempts, 5 rejected, same subject

Both are windows whose subject matter is itself prompt and digest work, which is where copying was
concentrated before. **A wider temperature did not move this model off the copy**, so the honest
statement is that enforcement converted stored fabrication into lost beats: 2 windows of 65 now have
no beat rather than a beat naming a held-out session's work. Whether the copy or the hole is worse
is a question for the review round; what the ladder cannot do is now measured rather than assumed.

## Generation failures, all four, shown

| session | window | attempts | rule |
|---|---|---|---|
| `129e9a80…39.jsonl` | 4 | 5 | entry cap: 235 runes (*"The staleness defect in the digest system was identified and fixed by introducing prompt-level accounting rules…"*) |
| `129e9a80…39.jsonl` | 9 | 5 | subject unanchored (copy of a worked example) |
| `139be731…bc.jsonl` | 4 | 5 | entry cap: *"the subject of the work is designing the UI/UX for managing Agents alongside People in Atlas' Teams page…"* |
| `43492104…67.jsonl` | 29 | 5 | subject unanchored (copy of a worked example) |

All four are real transcripts; the synthetic pair generated 7 of 7 first time. **The two entry-cap
failures are the same defect the previous sweep reported** — a single sentence that runs past 200
runes, not a paragraph — and raising `beatEventMaxRunes` no longer trades them against entries lost
at `BeatCap`, but it would raise `BeatCap` with it and cost the report further beats. Left alone
here, with the two lost beats counted rather than absorbed.

## Geometry

4,490 of 6,285 spanned turns read by a window (71.4%); 1,795 read by none; 30 of 72 windows carry a
hole marker. Largest assembled prompt 18,188 of the 24,000 budget, **0 over budget, 0 panics, 0
recovered panics**. Two real sessions reached 100% coverage (and both synthetic ones); the lowest real session read 47%.

## Concerns

1. **Two windows now produce no beat rather than a copied one.** The subject rule caught exactly
   what it was measured to catch, and the temperature ladder failed to recover either one. The fix
   for a model that insists on copying an instruction is a prompt change, and nothing here has
   measured one.
2. **The guard's reach is 72% of entries.** 76 of 268 name nothing checkable. That is not a defect
   in the rule — an entry cannot fabricate a specific it does not have — but no run should read
   "2 of 270 dropped" as "268 entries verified."
3. **The report reads 2 of 12 beats under pressure**, down from 4. Measured, deliberate, and the
   quality consequence for the report tier is unmeasured: the last report round was scored at
   `BeatCap` 512.
4. **The entry cap still loses whole beats**, 2 of 65 on real material, both single long sentences.
5. **The synthetic pair carries the whole domain-neutrality question on 7 beats**, and the worked
   examples remain all-engineering because the pinned snapshot holds nothing else.
