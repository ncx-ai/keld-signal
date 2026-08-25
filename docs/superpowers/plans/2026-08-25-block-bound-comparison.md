# Block Bound Comparison Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Establish what should end a block, by comparing four bounds against the measured failure of a time cap.

**Architecture:** Extends `scripts/block_sizing_eval.py`, which already computes cut points once per session and applies a bound as a pure function afterwards. Three new bound functions join the existing time one, all with the same signature, so the sweep and the metrics are shared. Nothing is shipped; `blocks.py` (Phase 1) will be written against the winner.

**Tech Stack:** Python via `~/.keld/study-venv/bin/python` (imports `sizer_eval`, needs pandas), `sidecar/app/analysis/*` on `PYTHONPATH=sidecar`, standalone test script (no pytest).

## Global Constraints

- Rules are FIXED in `~/keld/refseries-context/blocks/BLOCK-BOUND-PREREGISTRATION.md`. Implement them; do not restate from memory and do not move a bar to produce a pass.
- **The bar: an arm ships only if ≥ 95% of its blocks can attribute something.** That is the threshold at which the merge rule becomes unnecessary, and the merge rule is what failed last time (88.6% of merges changed a published value against a 5% bar).
- ⚠️ **THE MATCHED-DURATION CONTROL IS NOT OPTIONAL.** Arms B and D produce longer blocks, and longer blocks hold more evidence, so a raw `can_attribute%` win proves nothing — arm A at a larger N gets the same lift for free. An arm must beat the time cap **whose median block duration matches its own**, by ≥ 10 points. Without this the comparison measures block size wearing a different hat.
- Legibility constraint, stated in advance so it cannot be applied selectively: **p90 block duration over 4 hours fails**, even if the arm wins on attribution.
- ⚠️ `can_attribute` (already in the module) is the primary metric — per-level `attribution(...).reason == "attributed"`, NOT `block_evidence`'s pooled sum. That distinction was a bug once already.
- Every pooled figure is reported beside its session-median. The corpus holds an ~11.7-day session contributing ~17% of blocks at the 10m cap.
- ⚠️ **Do not reuse the old selection rule.** "Smallest cap within 5 points of the next" fires immediately on a shallow monotonic curve, which is exactly the shape the time sweep produced. It is not in this pre-registration.
- The frozen store (`~/keld/refseries-context/dynamics/frozen-corpus.db`, 496 sessions) is **READ-ONLY**. Tests build synthetic stores in a TemporaryDirectory.
- Results go in `~/keld/refseries-context/blocks/`. **NEVER in `docs/` or the repo** — real developer transcripts.
- ⚠️ Five `float`-into-`int` type errors exist in `scripts/block_sizing_eval.py` (reported around the stats-assembly lines). Fix them as part of Task 1 — they are in the file you are already editing.
- Never `git add -A` / `git add .` / `git checkout` / `git stash` / `git clean`. Stage only paths you edit.
- These carry uncommitted owner work and must NOT be touched: `.gitignore`, `internal/agent/daemon/custom_passes.go`, `custom_passes_test.go`, `daemon.go`, `scripts/context_value.py`, `scripts/prompt-v9.md`, `internal/agent/publish/report.go`, `internal/agent/publish/report_show_test.go`.
- Do not start the keld daemon. Never a broad `pkill -f`.

## File Structure

| File | Change |
|---|---|
| `scripts/block_sizing_eval.py` | three new bound functions, a shared arm runner, the matched-duration control; the float/int fixes |
| `scripts/test_block_sizing_eval.py` | their tests (13 already pass; ADD, do not restructure) |
| `~/keld/refseries-context/blocks/block-bound.json` | raw per-arm output |
| `~/keld/refseries-context/blocks/BLOCK-BOUND-RESULTS.md` | the written result, nulls included |

Existing interfaces to reuse, NOT redefine: `CAPS`, `cut_points`, `Block`, `form_blocks`,
`block_evidence`, `can_attribute`, `session_bounds`, `merge_thin`, `CachingStore`, `DB`.

---

### Task 1: The three new bounds

**Files:**
- Modify: `scripts/block_sizing_eval.py`
- Modify: `scripts/test_block_sizing_eval.py`

**Interfaces:**
- Produces, all sharing one signature so the sweep is arm-agnostic:
  - `bound_time(store, session, cuts, lo, hi, n)` — the existing `form_blocks`, wrapped.
  - `bound_evidence_gated(store, session, cuts, lo, hi, n)` — cap at `n` minutes but DEFER: a block reaching the cap while `can_attribute` is False continues to the next cap boundary.
  - `bound_turns(store, session, cuts, lo, hi, n)` — cut every `n` turns.
  - `bound_none(store, session, cuts, lo, hi, n=None)` — detection and the span edges only.
- Each returns `[Block]` and each block must TILE: contiguous, no gap, no overlap, first at `lo`, last at `hi`, `start_reason` chaining from the previous `end_reason`.
- New end reason for arm B: `"bound_deferred"` when a cap boundary was skipped for want of evidence. It is not `"budget"` — a deferred boundary is a different fact and must be countable separately.

- [ ] **Step 1: Write the failing tests**

```python
def test_evidence_gated_defers_past_a_cap_it_cannot_attribute():
    """Arm B's whole point. A block reaching the cap with nothing attributable must keep going
    rather than emitting a block that can say nothing — which is what arm A did on 78% of
    blocks."""
    with tempfile.TemporaryDirectory() as tmp:
        # sparse first 20 min (1 unit), then enough to attribute
        ev = [_ev(60, "branch", "main", n=1.0), _ev(1500, "branch", "main", n=9.0)]
        st = b.CachingStore(_mkstore(tmp, ev))
        blocks = b.bound_evidence_gated(st, SESSION, [], 0.0, 1800.0, 10)
        assert len(blocks) == 1, blocks           # the 10m cap was deferred past
        assert blocks[0].end == 1800.0, blocks[0]
        assert "deferred" in blocks[0].end_reason or blocks[0].end_reason == "session_end", blocks[0]

def test_evidence_gated_cuts_at_the_cap_when_it_can_attribute():
    with tempfile.TemporaryDirectory() as tmp:
        ev = [_ev(60, "branch", "main", n=9.0), _ev(1500, "branch", "main", n=9.0)]
        st = b.CachingStore(_mkstore(tmp, ev))
        blocks = b.bound_evidence_gated(st, SESSION, [], 0.0, 1800.0, 10)
        assert len(blocks) > 1, blocks
        assert blocks[0].end == 600.0, blocks[0]

def test_bound_none_emits_one_block_when_nothing_was_detected():
    blocks = b.bound_none(None, SESSION, [], 0.0, 36000.0)
    assert len(blocks) == 1, blocks
    assert blocks[0].start == 0.0 and blocks[0].end == 36000.0, blocks[0]
    assert blocks[0].end_reason == "session_end", blocks[0]

def test_bound_turns_cuts_every_n_turns():
    with tempfile.TemporaryDirectory() as tmp:
        ev = [_ev(i * 60.0, "branch", "main", n=1.0) for i in range(20)]
        st = b.CachingStore(_mkstore(tmp, ev))
        blocks = b.bound_turns(st, SESSION, [], 0.0, 1200.0, 5)
        assert len(blocks) >= 3, blocks
        for x, y in zip(blocks, blocks[1:]):
            assert x.end == y.start, (x, y)

def test_every_bound_tiles_its_span():
    """The invariant the attribution model rests on, asserted for all four arms at once so a new
    bound cannot be added without it."""
    with tempfile.TemporaryDirectory() as tmp:
        ev = [_ev(i * 60.0, "branch", "main", n=2.0) for i in range(30)]
        st = b.CachingStore(_mkstore(tmp, ev))
        for fn, n in ((b.bound_time, 10), (b.bound_evidence_gated, 10),
                      (b.bound_turns, 5), (b.bound_none, None)):
            blocks = fn(st, SESSION, [600.0], 0.0, 1800.0, n)
            assert blocks[0].start == 0.0, (fn.__name__, blocks)
            assert blocks[-1].end == 1800.0, (fn.__name__, blocks)
            for x, y in zip(blocks, blocks[1:]):
                assert x.end == y.start, (fn.__name__, x, y)
                assert y.start_reason == x.end_reason, (fn.__name__, x, y)
```

- [ ] **Step 2: Run to verify they fail**

Run: `PYTHONPATH=sidecar ~/.keld/study-venv/bin/python scripts/test_block_sizing_eval.py`
Expected: FAIL — the bound functions do not exist.

- [ ] **Step 3: Implement**

Each bound walks the span the way `form_blocks` already does, differing only in what ends a block.
Keep `form_blocks` as arm A's implementation and have `bound_time` delegate to it — arm A is the
measured baseline and its numbers must not move.

`bound_evidence_gated` is the one with a real decision in it. At each candidate cap boundary,
consult `can_attribute(store, session, Block(t, boundary, ...))`. If True, cut there with
`end_reason="budget"` (the same reason arm A uses, so the arms stay comparable). If False, extend to
the next boundary and, when the block eventually closes, mark it `"bound_deferred"` so deferrals
are countable. A detected cut inside the span always wins over the bound, in every arm.

`bound_turns` needs the turn instants; get them from `store.turn_times(session, lo, hi)` if that
exists, otherwise from the distinct `ts` values of the session's events in range — check which is
available rather than assuming, and say in your report which you used.

Also fix the five `float`-into-`int` type errors flagged in this file.

- [ ] **Step 4: Run to verify they pass**

Run: `PYTHONPATH=sidecar ~/.keld/study-venv/bin/python scripts/test_block_sizing_eval.py`
Expected: 18/18 passed (13 existing + 5 new).

- [ ] **Step 5: Commit**

```bash
git add scripts/block_sizing_eval.py scripts/test_block_sizing_eval.py
git commit -m "study(blocks): three more bounds, one signature, all of them tiling"
```

---

### Task 2: The arm sweep and the matched-duration control

**Files:**
- Modify: `scripts/block_sizing_eval.py`
- Modify: `scripts/test_block_sizing_eval.py`

**Interfaces:**
- `ARMS` — a table of `(name, fn, candidates)`: time and evidence-gated over `CAPS`, turns over `(5, 10, 20, 40, 80)`, none over `(None,)`.
- `run_arm(name, fn, n) -> dict` — one arm at one parameter over every session, returning `can_attribute_share`, `merge_share`, `dur_p50`, `dur_p90`, `blocks_per_session`, the end-reason mix, `max_blocks_one_session_share`, and the session-median of `can_attribute_share`.
- `matched_control(arms) -> [dict]` — for every non-time arm row, find the TIME row whose `dur_p50` is closest and report the `can_attribute_share` delta.
- `verdict(rows, control) -> dict` — applies rules 1-4 and returns per-arm PASS/FAIL with the failing rule named.

- [ ] **Step 1: Write the failing tests**

```python
def test_matched_control_pairs_on_median_duration_not_on_parameter():
    """The control's whole job. Pairing arm B's n=10 against arm A's n=10 would compare a
    deferred block against a 10-minute one and call the difference intelligence."""
    arms = [{"arm": "time", "n": 10, "dur_p50": 600.0, "can_attribute_share": 22.2},
            {"arm": "time", "n": 60, "dur_p50": 3300.0, "can_attribute_share": 46.4},
            {"arm": "evidence_gated", "n": 10, "dur_p50": 3200.0, "can_attribute_share": 96.0}]
    ctrl = b.matched_control(arms)
    row = next(c for c in ctrl if c["arm"] == "evidence_gated")
    assert row["matched_time_n"] == 60, row      # 3200 is nearest 3300, NOT 600
    assert abs(row["delta"] - (96.0 - 46.4)) < 0.1, row

def test_verdict_fails_an_arm_that_wins_only_by_being_longer():
    """Rule 2. 96% attributable is worthless if the matched time cap also gets 92%."""
    rows = [{"arm": "evidence_gated", "n": 10, "can_attribute_share": 96.0, "dur_p90": 7200.0}]
    ctrl = [{"arm": "evidence_gated", "n": 10, "matched_time_n": 60, "delta": 4.0}]
    v = b.verdict(rows, ctrl)
    assert v["evidence_gated"]["pass"] is False, v
    assert "matched" in v["evidence_gated"]["why"].lower(), v

def test_verdict_fails_an_arm_on_legibility_even_when_it_wins_attribution():
    """Rule 3, written in advance so it cannot be applied selectively afterwards."""
    rows = [{"arm": "none", "n": None, "can_attribute_share": 99.0, "dur_p90": 20000.0}]
    ctrl = [{"arm": "none", "n": None, "matched_time_n": 120, "delta": 38.0}]
    v = b.verdict(rows, ctrl)
    assert v["none"]["pass"] is False, v
    assert "legib" in v["none"]["why"].lower() or "p90" in v["none"]["why"], v
```

- [ ] **Step 2: Run to verify they fail**

Run: `PYTHONPATH=sidecar ~/.keld/study-venv/bin/python scripts/test_block_sizing_eval.py`
Expected: FAIL — `matched_control` / `verdict` not defined.

- [ ] **Step 3: Implement**

`run_arm` reuses the per-session loop the existing `sweep()` already has: cut points once per
session, then the arm's bound, then the metrics. `merge_share` comes from `merge_thin`'s stats —
under arm B it should be near zero by construction, and if it is not, arm B does not work as
designed and that is a finding.

`verdict` applies, in order: rule 1 (`can_attribute_share >= 95`), rule 2 (matched delta `>= 10`),
rule 3 (`dur_p90 <= 4*3600`), rule 4 (fewest parameters among survivors). Name the failing rule in
`why`; do not return a bare boolean.

- [ ] **Step 4: Run to verify they pass**

Expected: 21/21 passed.

- [ ] **Step 5: Commit**

```bash
git add scripts/block_sizing_eval.py scripts/test_block_sizing_eval.py
git commit -m "study(blocks): the matched-duration control, so a bound cannot win by being longer"
```

---

### Task 3: Run it and write it up

**Files:**
- Create: `~/keld/refseries-context/blocks/block-bound.json`, `BLOCK-BOUND-RESULTS.md`

- [ ] **Step 1: Run**

```bash
cd /home/dg/keld/keld-signal
PYTHONPATH=sidecar ~/.keld/study-venv/bin/python scripts/block_sizing_eval.py --bounds \
  2>&1 | tee ~/keld/refseries-context/blocks/block-bound-run.txt
```
The sweep is minutes, not seconds. Let it run.

- [ ] **Step 2: Write the results**

`BLOCK-BOUND-RESULTS.md`: a row per arm and parameter, the matched-duration control table, and
`verdict`'s PASS/FAIL per arm with the failing rule named. Carry forward, both required and neither
newly measured here:

1. **The time baseline it is compared against** — 22.2% attributable at 10m, 61.2% at 120m, 91-99%
   of blocks ending on the bound, and the merge rule's failure (7,225 of 9,773 merged, 6,987
   chained, 88.6% of merge events changing a published value).
2. **The detector blind spot every arm inherits** — three EQUAL-weight work segments leave the later
   ones tied with the first at cumulative weight, so the alphabetical tie-break keeps the original
   value as mode and TWO real transitions collapse into ONE edge. Verified against the shipped code,
   never measured on the corpus.

**Report nulls plainly.** If no arm reaches 95%, the finding is that no bound fixes this and the
problem is upstream in the detector's 7% recall — which is Part B's territory, not a cut point's.

- [ ] **Step 3: Commit the script changes only**

Results are NOT committed; they live under `~/keld/refseries-context/blocks/`.

---

## Self-Review

**Spec coverage.** Four arms: Task 1. All six metrics incl. session-medians: Task 2's `run_arm`.
The matched-duration control: Task 2, with a test pinning that it pairs on duration and not on
parameter — the specific way it could be silently wrong. Rules 1-4: `verdict`, with tests for the
two that are easiest to skip (an arm winning only by length, and legibility overriding a win).
Rule 5's null: Task 3 Step 2. The carry-forwards: Task 3 Step 2.

**Placeholders.** `run_arm`'s accumulation and Step 2's prose are described rather than coded —
accumulation reuses a loop that exists, and the write-up is prose. Every function a test can pin
carries its code. `bound_turns`' turn-instant source is deliberately left to be checked rather than
assumed, with an instruction to report which was used.

**Type consistency.** All four bounds share
`(store, session, cuts, lo, hi, n) -> [Block]`; `bound_none` takes `n=None` so the sweep can call
them uniformly. `matched_control` consumes the row dicts `run_arm` emits, keyed `arm`, `n`,
`dur_p50`, `can_attribute_share`, matching the test fixtures. `verdict` reads `can_attribute_share`,
`dur_p90` and the control's `delta` — all produced upstream.
