# Joint action × file-type signal, and the modality gap — design

**Date:** 2026-10-01
**Status:** design approved conversationally. Measured before specifying; every number below is
from this machine's two corpora.
**Revert:** two INVENTORY entries, one lookup table, one schema bump.

## 1. What this answers

Today `action`, `file`, `dir`, `component`, `ext` and `repo` all publish **independently**. The
pairing between them is computed and thrown away. This spec keeps it.

The question it serves: **what kind of material is this work touching** — source, prose, images,
documents, notebooks. That is a DESCRIPTIVE question about what the work is, and it is the
whole justification for this spec.

⚠️ **THE ROUTING MOTIVATION WAS MEASURED AND IT DOES NOT HOLD. This section used to say the
signal "implies a capability the default model does not have."** It does not, on either half of
that claim, measured over **41,401 tool-calling requests** across both corpora:

| | n | out p50 | cache_creation p50 | cache_read p50 |
|---|---|---|---|---|
| touches image/pdf/etc | 719 | **115** | 838 | 257,515 |
| text only | 40,682 | **393** | 1,176 | 193,322 |

1. **Specialised-format requests are CHEAPER on output, not dearer** — 115 against 393 median
   tokens. The intuition that image work costs more is backwards. What images inflate is
   *context* (cache_read 1.33x), and that is a cache read at roughly a tenth the price.
2. **Every model on the menu was actually used on specialised-format requests** —
   `opus-4-8` (364), `sonnet-5` (134), `opus-5` (120), `fable-5` (92), and `haiku-4-5` (9).
   Nothing was excluded by modality, because these models are all multimodal. "This block has
   images" therefore selects nothing.

Modality would only discriminate if the menu held a **text-only** model. Two non-Claude entries
appear in the corpora (`deepseek-v4-pro`, `fugu-max`) where that might apply, but neither occurs
on a specialised-format request — so it is a hypothetical, not an observed constraint.

**So this spec is justified descriptively and NOT by routing.** If the only reason to want it
were model selection, the measurement above says do not build it. See
`docs/notes/2026-10-01-activity-verb-v2-and-routing.md` §5 for the parallel finding on the
activity axis.

## 2. ⚠️ FOUR LOOKUP TABLES ALREADY EXIST. Do not rebuild them.

A reader reaching for "we need a table of program names to categories" should check these first.

| table | size | maps | example |
|---|---|---|---|
| `vocab.TOOL_ACTION` | 7 acts | harness tool → act | `Read` → `read` |
| `vocab.EXE_ACTION` | **95 programs** → 13 acts | program → act | `pandoc` → `convert a document` |
| `vocab.EXT_LANG` | 51 extensions | extension → language | `.tsx` → `TypeScript` |
| `systems.category_for_program` | 664 brand tokens, 22 categories | **SaaS CLI** → business category | `gh` → `code_hosting`, `stripe` → `finance_billing` |

⚠️ **`systems.category_for_program` is for SaaS CLIs, not local tools.** Measured: it returns
`None` for `ffmpeg`, `psql`, `git` and `kubectl`, and resolves `aws`/`vercel`/`heroku`. Asking it
about a local program is a category error, not a coverage bug.

**The real gap is narrower than "we have no table" and is stated precisely in §4.**

## 3. The join: `action × ext`

### Measured, before building it

Corpus B: 11,702 file-touching tool calls, **52 distinct `action:ext` pairs**.
Corpus A (John): 2,400 calls, **58 distinct pairs**.

Top pairs are immediately readable and are the signal this exists for:

```
edit:.tsx 2573   read:.tsx 1230   read:.py 1226   edit:.py 1079   read:.md 832
```

### Why a pair and not two marginals

A join is strictly more information than its two marginals, and that is the point: `read:.jpg`
is a claim about capability that neither `read` nor `.jpg` makes alone in a block containing
both.

**Precedent:** `system_action` already publishes `<category>:<action>` as a joined pair
(`systems.system_action`), for the same reason and in the same `<a>:<b>` format. This follows it
rather than inventing a shape.

### Published shape

**`file_actions`** — an INVENTORY level, ref name `file_action`, value `"<action>:<ext>"`.

- **Cap 12.** Measured distinct pairs per transcript: corpus B p50=3, p90=7, p99=18, max=31;
  corpus A p50=3, p90=7, p99=11, max=12. **12 sits just above both p90s**, which is the rule the
  existing per-level caps (40/24/16) were set by. A cut is declared in `inventory_omitted`.
  ⚠️ Unlike `activity_classes`, this vocabulary is **open** (52 and 58 observed, and an
  extension is any string), so truncation is possible and the cap is a real bound rather than a
  formality.
- **INVENTORY, not ALLOCATION** — a block touches many file types and no majority is meaningful.
- Emitted in `levels.py`'s existing `for call in o.tool_calls:` loop, where `action_for(tool=...)`
  and the path are **both already in hand** and the pairing is currently discarded.
- An extension of `""` publishes as `(none)`; an action of `None` publishes **no row**, matching
  `_sys_act`'s refusal to guess a verb.

## 4. The modality gap, which is the table that genuinely does not exist

`EXE_ACTION` answers **what act** (`run code`, `convert a document`). It does not answer **what
modality** — and modality is the axis that implies a model capability.

Measured absences from `EXE_ACTION`, every one a media or specialised tool:

```
ffmpeg  imagemagick  sips  jupyter  nbconvert  exiftool
sox  whisper  tesseract  ghostscript  terraform
```

(`pdftotext`, `magick`, `convert` and `pandoc` are already present.)

**Proposed: `vocab.MODALITY`, a declarative table over BOTH extensions and programs**, answering
one closed question — what kind of material is being handled:

| modality | extensions | programs |
|---|---|---|
| `image` | `.png .jpg .jpeg .gif .webp .svg .heic` | `imagemagick magick convert sips exiftool` |
| `video` | `.mp4 .mov .webm .mkv` | `ffmpeg ffprobe` |
| `audio` | `.mp3 .wav .m4a .flac` | `sox whisper` |
| `document` | `.pdf .docx .pptx .odt` | `pdftotext pdftoppm qpdf ghostscript libreoffice pandoc` |
| `notebook` | `.ipynb` | `jupyter nbconvert` |
| `tabular` | `.csv .tsv .xlsx .parquet` | — |
| `text` | everything else | — |

⚠️ **This is a DECLARATIVE LOOKUP, not an inference**, and that is the whole reason it is
proposed. The vendor table (`systems.py`) is the one approach in this codebase that has worked,
because it is true by construction and its absence from a corpus is not evidence against it.
Six measured attempts to *infer* semantics from text have failed on this question.

⚠️ **`whisper` and `tesseract` are themselves ML tools.** They are listed under the modality
they consume, not as a capability claim about the agent.

⚠️ **The modality table is the part of this spec most at risk of being built for a reason that
does not survive.** Its original motivation was routing, and §1 retires that. What remains is
descriptive: a block that spent an afternoon on `.jpg` files is doing different work from one
that spent it on `.go` files, and that is worth publishing on its own terms. If that
justification is not enough for a reader, the correct response is to build §3 alone and leave
§4 unbuilt — the join carries most of the descriptive value and costs far less.

## 5. ⚠️ A KNOWN UNDERCOUNT, stated because it will otherwise be discovered as a defect

Every measurement in §3 reads `file_path` / `notebook_path` — **harness tool inputs only**. A
`Bash` call running `ffmpeg input.mov` carries no `file_path`, so it was **invisible** to the
12.25% / 3.63% specialised-format figures in
`docs/notes/2026-10-01-activity-verb-v2-and-routing.md`.

So the real specialised share is **higher than measured, by an unmeasured amount.** The §4 table
covering programs as well as extensions is what closes this, because `shell.bash_refs` already
extracts argv for exactly this kind of question. **The corrected share must be re-measured once
the table exists** — do not carry the 12.25% figure forward as if it were complete.

## 6. Privacy

- **No new data source.** Everything comes from tool-call inputs already parsed.
- **No file contents.** This spec does not read, open, or stat any file. The content invariant
  (`magnitude.edit_bytes` is the only function that may see `old_string`/`new_string`/`content`,
  and may return only an `int`) is untouched.
- **No new filesystem access**, so `KELD_ANALYZE_ROOTS` confinement is unaffected.
- **What crosses is coarser than what already does:** an act verb, a file extension, a modality
  name. No path, no filename, no content. `files`/`directories` already publish
  workspace-relative paths, which are strictly more identifying.
- ⚠️ **Honest statement, not a dismissal:** a join is more information than its marginals. The
  increment here is small and bounded by two closed-ish vocabularies, but it is not zero, and
  claiming otherwise would be the "both halves already publish" fallacy.

## 7. Scope

**In:** the `file_actions` level; `vocab.MODALITY` and a `modalities` level; extending
`EXE_ACTION` with the eleven missing programs; a schema bump; the re-measurement in §5.

**Out:** reading file contents from disk — rejected, and not only on privacy. **The transcript is
a historical record; the filesystem is current state.** `KELD_BLOCKS_BACKFILL` defaults ON, so
historical blocks are reprocessed, and a file read today would be attributed to work done weeks
ago after refactors, renames or deletion — silently, with nothing downstream re-deriving those
rows. Content-derived signal, if ever wanted, should come from what the agent **authored**
(already in the transcript, historically correct) rather than from the filesystem.

**Out:** routing. Also out: within-format semantics (is this `.md` a design doc or a changelog) —
studied 2026-10-01 and **parked**, the corpus being 48% agent process artifacts with 6 of 8
target types absent.

## 8. What the evidence does and does not support

**Does NOT support the routing case at all** — see §1. The measurement there is the single most
important fact in this document and it is a negative one.

**Does:** specialised-format work is real, varies between people, and is **bimodal at session
level** — measured across 201 of John's sessions and 474 of corpus B's, the median session for
**both** is 0% specialised, while 6 of John's 29 substantial sessions exceed 25% (p90 53.8%,
max 100%) against **0 of 111** for corpus B (max 23.8%).

**Does not:** support any per-person claim. Both medians are 0%. The signal is a property of the
**block**, not the person, and must be published and consumed that way.

⚠️ **An earlier version of this comparison reported 54% for John and a "15x per-person
difference."** That was one atypical 7-hour session — the only file in the frozen-corpus
snapshot, 148 of whose lines were `attachment` records. The full corpus is 405 transcripts and
gives 12.25%. Recorded because the error is instructive: a snapshot directory was assumed to
represent the corpus it was named after, and its size was never checked against the source.

## 9. Open, dated

- **The §5 undercount is unquantified as of 2026-10-01.** Re-measure the specialised share with
  program evidence included, and correct the figures in the routing note.
- **`terraform` is in the missing-programs list but has no modality** — it is infrastructure, not
  material. It belongs in `EXE_ACTION` (act: `run a service` or similar) and **not** in
  `MODALITY`. Named here so it is not swept into the table for being on the same list.
- **The modality table's coverage is unmeasured against a corpus that uses these tools.** Neither
  corpus here does media work at volume. The table is true by construction, so absence is not
  evidence against it — but its *usefulness* is untested and should be stated as such wherever
  the level is consumed.
