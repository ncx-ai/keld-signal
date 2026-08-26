# Weighting a file touch by what was DONE to it

**Status:** spec. Not implemented. The measurement it prescribes has not been run.

## The problem, in one line

Every file touch emits at weight 1.0, so a block that skimmed twelve files while rewriting one
publishes what it skimmed.

Measured act distribution over the corpus:

| act | events | share |
|---|---|---|
| read | 17,834 | 34.5% |
| search | 8,919 | 17.3% |
| edit | 5,923 | 11.5% |
| run code | 3,742 | 7.2% |
| create | 1,723 | 3.3% |

**Reads outnumber edits 3:1** and searches outnumber them too. `reconcile()` emits one `lang`/`file`/
`dir`/`component`/`artifact`/`ext` row per touch at weight 1.0 regardless, so the published value of
every path-derived dimension is a popularity contest that reading wins.

## ⚠️ The constraint that dominates the design

`window.attribution` computes:

    total = sum(n for _, n in items)

**Evidence IS the sum of weights.** And `MIN_EVIDENCE = 5` is derived as a SAMPLE SIZE — under the
0.50 share floor read as a null hypothesis, unanimity over *n* observations has probability
`0.5**n`, which first clears 5% at n=5.

So weighting an edit at 3.0 makes **two edits clear a floor calibrated for five observations**. That
silently lowers the floor, invisibly, and no existing test would catch it. It is the same defect
class AGENTS.md already documents for duration-scaled floors: it makes the significance of a
published attribution a function of something other than how many times we looked.

Removing the floor was measured at **P(false attribution) 0.031 → 0.50**, so this is not a small
leak. **The floor must remain a count of observations.**

**Therefore: weights determine the SHARE. Evidence remains a COUNT. The two must be carried
separately.**

## What makes this cheap — two facts established before designing

1. **`n` is `REAL`** in both `event` and `bin`. Fractional weights are already storable; no schema
   change, no `STATE_VERSION` bump, no reparse.
2. **Reconcile rows are recomputed at query time.** `/analyze` calls
   `reconcile(pending_in(store, path, lo, hi), COMPONENT_DEPTH)` and adds its rows to
   `window_rows(..., exclude_slots=(RECONCILE_SLOT,))`. The six path-derived levels are therefore
   NOT served from stored bins for the digest, so a weight change takes effect without touching
   history.
3. **The act is available at emit time.** `levels.py:340`'s `pending.append` sits inside the loop
   that already branches on the tool `name`, and `vocab.TOOL_ACTION` maps tool → act
   (`read`/`edit`/`create`/`search`/...), with `bash_refs` supplying acts for shell commands.

## Design

**Carry the act down into `pending`, weight in `reconcile`, and give `attribution` a separate
count.**

    # levels.py — one more element, the act, per pending path
    pending.append((base, rel.rstrip("/"), from_input, root_key, act))

    # vocab.py — the weight table, one place, closed set
    ACT_WEIGHT = {"create": W_WRITE, "edit": W_WRITE, "read": 1.0, "search": 1.0, ...}

    # reconcile.py — emit the weighted row AND a parallel count
    rows.append(b + ("ref", "lang", EXT_LANG[ext], ACT_WEIGHT.get(act, 1.0)))
    counts[("lang", EXT_LANG[ext])] += 1

    # window.py — attribution takes optional counts; the floor reads them, the share does not
    def attribution(rl, level, floor=0.5, min_evidence=MIN_EVIDENCE, counts=None): ...

**Why `counts` is optional and defaulted to `None`:** every level that is not path-derived still
emits at weight 1.0, where count == mass exactly. Those callers pass nothing and their behaviour is
bit-identical to today. Only the reconcile path supplies counts, so the blast radius is the six path
levels and nothing else. `dynamics` and `prior` consume `rollup`/`attribution` too — with no counts
supplied they are unaffected, which must be pinned by a test rather than assumed.

⚠️ **`evidence` on the wire stays the COUNT**, not the mass. It is the number the consumer needs to
judge a `thin`/`attributed` status against, and a weighted mass in that field would silently change
what every published `evidence` means.

## The empirical protocol

⚠️ **There is no ground truth for "what language was this block really about."** This is a judgement
informed by measurement, not a validation. That is stated here so the results are not later read as
proof.

**Pre-register before running:**

1. **Sweep** `W_WRITE` over 1.0 (control), 2.0, 3.0, 5.0, 10.0 with reads and searches at 1.0.
2. **Report, per weight:** how many of the 1,502 blocks change their published value on each of the
   six path levels; how many gain a value, lose one, or switch; and the change in the `attributed` /
   `thin` / `absent` split.
3. **Stability is the primary test.** If the published answer is materially the same across 2.0-5.0,
   the exact weight is not load-bearing and the middle of that range ships — the same argument the
   idle threshold rests on. **If the answer keeps moving across the whole range, the weight is doing
   arbitrary work and the change does not ship.**
4. **Hand-review a sample of 20 changed blocks**, drawn randomly, judged blind to the weight that
   produced them: does the new value read as a better description of that block's work? Report the
   count that do, that do not, and that are unclear. A majority must read as improvements.
5. **The floor must be provably unmoved.** A test asserting that a block's `evidence` equals its
   observation count under every weight, and that `attributed` still requires >= 5 observations.

**Bars, fixed before the run:**
- Ships only if the sampled review is a **majority improvement** and the answer is **stable across
  2.0-5.0**.
- Does NOT ship if `evidence` diverges from the observation count anywhere, at any weight.
- Does NOT ship if any non-path level's published value changes at all — that would mean the
  optional-`counts` containment failed.

## Open questions the spec does not settle

- **`search` weight.** Searching is 17.3% of acts and is arguably not engagement with a file at all
  — a `Grep` hit lists paths the person never opened. A case exists for weighting it BELOW read.
  Unmeasured; the sweep holds it at 1.0 and a second sweep can follow.
- **Whether `file`/`dir`/`component` should be weighted at all.** They are INVENTORY levels
  answering "which paths were hot", where a frequency distribution over touches may be the honest
  answer regardless of act. The case for weighting is strongest on `lang` and `artifact`, which are
  ALLOCATION and make a dominance claim. Consider shipping the weight to ALLOCATION levels only.
- **`run code` / `test` (7.2% / 6.6%)** touch files without reading or writing them. Unclassified
  here; they currently fall through to 1.0.
