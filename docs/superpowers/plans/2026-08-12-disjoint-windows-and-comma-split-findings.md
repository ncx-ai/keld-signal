# Tasks 1 and 2 of the production beat design — measured

Prerequisites for the first production sweep: the subject-token comma split, and disjoint beat
windows. Both landed with revert-and-fail checks on real input. Nothing here uses the model
server; every number is from an offline walk of the pinned corpus snapshot.

Commits: `fix(study): a number is one subject term…`, `feat(study): beat windows are disjoint…`,
`docs(study): the two sibling subject leaks…`.

## Task 1 — the comma split

`subjectTokens` split on every `,`, so `1,400.00` was stored as `400.00`. Measured on
`testdata/nontech/finance-close.jsonl`, **six of the twelve slots** in `SessionRecord.Subjects` —
the block the prompt labels *measured — authoritative* — held amounts nobody wrote:

| before | after (whole, as written) |
|---|---|
| `900.00` `100.00` `180.00` `200.00` `400.00` `629.60` | `4,900.00` `1,100.00` `1,284,660.00` `1,400.00` `14,200.00` `1,200.00` `1,840.00` |

The verbatim gate cannot catch this, and that is the part worth keeping: it is a **substring**
test, and `400.00` is a substring of `1,400.00`. A fractured amount passes every check the record
has, so a beat anchoring against the record inherits a wrong figure with the record's authority
behind it.

**The rule.** A `,` is a token character only where it separates the digit groups of one number:
a digit immediately before, and *exactly three* digits after. Exactly three keeps it a
thousands-separator rule — `1,2,3` and `12,34567` still split, a clause-ending comma
(`148.00, and credit cash`) and a date's comma (`March 5, 2026`) are untouched.

`subjectTokenRune` could not express it — a per-rune class sees one rune — so **the two
tokenisers are now one**: `subjectTokenSpans` is the implementation and `subjectTokens` returns
its spans as strings, instead of a `FieldsFunc` that agreed with it only by inspection.

### The routes that share the tokeniser, and what moves

| route | what moves |
|---|---|
| `SessionRecord.Observe` → `Subjects` | the defect; whole amounts replace their tails |
| `RecentSubjects` → the digest recency anchor | same terms, same correction |
| `distinctiveTerms` → `SynopsisLag` / `SubjectShifted` / `BeatContradictsRecord` | numeric members of the compared term sets change spelling |
| `sessionTermSet` → the DF table | keys change; **no decision moves** — an amount reaches `distinctiveToken` by `strongIdentifier`'s digit rule and returns before the table is consulted, so the changed entries are never queried |
| `ungroundedTerms` (beat_compose) and beat entity candidates (beat_entities) | same correction; grounding gets stricter in the right direction, since `400.00` was "verbatim present" only by substring. *(Both files were deleted by task 3 while this landed.)* |
| `beatSubjectTermList` (beat_series) | **output unchanged** — numeric tokens are rejected by `containsLetter` either way. The fix only makes its docstring's own example (`1,650.55` is an amount, not a subject) true as written for the first time. |

### Revert-and-fail

Reverting the comma clause fails both tests on real input:
`TestRecordSubjectsHoldWholeAmountsNotThousandsFragments` lists all six real fragments —
*"subject `400.00` never appears in the transcript except after a comma"* — and
`TestSubjectTokensKeepNumbersWholeAcrossThousandsSeparators` fails on sentences lifted out of the
fixture. The record-level test asserts the general property first (no numeric subject may be
only the tail of a longer number in the source) and the named `1,400.00` / `400.00` case second,
so it keeps failing on fragments the corpus grows later.

### The two sibling leaks — recorded, not fixed

Now noted at their sites (`beatOmittedNotice`, and the candidate loop in `session_record.go`):

- The hole marker's own words are in the **rendered** window, so any route that tokenises or
  substring-matches it reads `omitted`/`turns`/`context` as transcript — the same way the role
  labels do, which is how `assistant` reached a beat's candidate list. Measured: it does **not**
  reach `SessionRecord.Subjects` (`Observe` reads `Turn.Text`; 20 corpus sessions with a
  representative DF table produced no such term). It lands in whatever reads the rendered
  string — which now includes the verbatim anchoring check.
- `x100`, `appendTurn`'s collapsed-tool-run marker, passes `distinctiveToken` by
  `strongIdentifier`'s digit rule, which returns **before** the DF gate. A marker this package
  writes can take a slot in the authoritative block. Latent rather than live on this corpus: real
  runs collapse to `x2`/`x3`, below `dfMinTermLen`.

## Task 2 — disjoint windows

Stride equals window. Same walk for both arms (`TestCorpusBeatWindowGeometry`, pinned snapshot
`study-corpus-snapshot-2026-08-11T2130`, no session-transcript injection, 20 whole sessions,
cadence every 5 user prompts), so the comparison is not method-dependent.

| | overlapping (before) | disjoint (after) |
|---|---:|---:|
| beat windows | 128 | 128 |
| turns spanned by the beats' strides | 13,677 | 13,677 |
| **turns read by some window** | **7,679** (56.1%) | **8,904** (65.1%) |
| **turns read by NO window** | **5,998** | **4,773** |
| **windows carrying a hole marker** | **68 of 128** | **58 of 128** |
| holes marked | 68 of 68 | 58 of 58 |
| mean window | 13,576 runes | 12,362 runes |
| largest assembled beat prompt (budget 24,000) | 18,107 | 18,076 |
| backstop firings | 0 of 128 | 0 of 128 |

1,225 turns that no beat had ever read are now read, and 10 fewer windows have a hole at all.
The counts differ from the design's (137 windows / 14,154 turns / 6,228 unread) because that run
prepended the harness's own session transcript; both arms here exclude it, and both denominators
are identical.

The extra-beat-on-long-stride option is **not** implemented. Its price, measured on the same
walk: 213 beats instead of 128 (1.7× the inference) for 100% coverage.

**Disjointness is asserted structurally**, not by a text match: every window's turns must be the
suffix of that beat's own stride. The probe still reports the text-recurrence figure but now
labels it for what it is — identical turns recurring between consecutive windows, 27,715 runes
(2.1%), which is coincidental repetition of lines like `tool: Read …` and rises with window size,
not carried material. The old geometry's 0.7% is the same measure on windows a quarter the size.

### The hole marker is charged to the budget

Rune-consistent, two-pass, exactly as before — kept through the rewrite, and now with a check
that reproduces the real failure:

- `TestBeatWindowChargesItselfForTheHoleMarker` — fixture of 16 turns of **exactly 1,000**
  rendered runes, sized to land *on* the 16,000 bound. Reverting the charge: **16,111 against
  16,000**. (The older drop test asserts the same bound but leaves hundreds of runes of slack, so
  it passes either way — that is why this one exists.)
- The real corpus, same revert: **25 windows over `BeatWindowChars`**, by up to **+110 runes**
  (+14, +35, +55, +58, +60, +70, +74, +79, +101, +110, …) across keld-signal and keld-atlas
  sessions.

### API

`BeatWindow` loses `OverlapTurns` / `OverlapRunes` / `PrevSpanRunes` / `PrevWindowRunes`, and
`BeatCoverage` loses the three `Overlap*Pct` methods, rather than keeping fields that can now
only read zero. `BeatCoverage` gains `Holed` (windows carrying the marker) and `UnreadTurns()`
(the deficit in the unit it is paid in); `BeatWindow` gains `Holed()`. The hole marker now leads
the window — with stride equal to window the dropped turns are always the head of the beat's own
stride — and its *wording*, not its position, is what stops it reading as "the session started
earlier".

## Verification

`go build ./...`, `go test ./...`, `go vet ./...`, `go vet -tags llmstudy ./...` all clean at the
final commit; `gofmt -l` clean apart from the three pre-existing files
(`internal/agent/enrich/pipeline_custom_test.go`, `internal/agent/enrich/types.go`,
`internal/paths/paths.go`). `DefaultPromptCharBudget`, ctx, `insightsMatch`,
`significantWords`, `insightMatchRatio`, `staleOverlapRatio` and `BeatCap` are untouched, and
nothing here consumes the report tier's 4 runes of headroom.

Verification ran in a separate worktree pinned to the branch base plus these changes only,
because task 3/4 were being written into the same working tree at the same time and the package
did not compile there for most of this work.
