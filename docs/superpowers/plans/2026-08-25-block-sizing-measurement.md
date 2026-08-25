# Block Sizing Measurement (Phase 0 items 2 & 3) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Derive the maximum block duration, and establish whether merging a thin block forward changes any published value.

**Architecture:** A study script that forms blocks from the SHIPPED detector's rising edges — `EwmaSizer.fire_indices()` already enumerates them all; `plan()` merely takes the last — then sweeps candidate duration caps and applies the merge rule offline. Nothing is shipped; this is a measurement, and `blocks.py` (Phase 1) will be written against its numbers.

**Tech Stack:** Python via `~/.keld/study-venv/bin/python` (imports `sizer_eval`, which needs pandas), `sidecar/app/analysis/*` on `PYTHONPATH=sidecar`, standalone test script (no pytest).

## Global Constraints

- The decision rules are FIXED and live in `~/keld/refseries-context/blocks/BLOCK-SIZING-PREREGISTRATION.md`. Implement them; do not restate them from memory in code comments, and do not move a bar.
- **Candidate caps, exactly: 10, 15, 20, 30, 45, 60, 90, 120 minutes.**
- **Item 2's rule:** the cap is the smallest candidate whose `budget` share is within **5 percentage points** of the next larger cap's. If none qualifies, report that and pick nothing.
- **Item 3's rule:** merge-forward ships if it changes **no published value in more than 5% of merges**.
- ⚠️ Cut points come from the SHIPPED `EwmaSizer` via `fire_indices()`. Do not reimplement the EWMA, and do not edit anything under `sidecar/`.
- ⚠️ **`DETECT_LEVEL` is `branch` and stays `branch`** — Phase 0a refuted `language`, `output_type`, `component`, `skill`, `action` and the `branch+language` pair against pre-registered bars. Do not widen it hoping for more cut points.
- ⚠️ **Causality.** The merge decision belongs to the moment the SUCCESSOR closes. This study computes it offline; if a number only holds because the code could see the future, that invalidates it and the report must say so.
- The frozen store at `~/keld/refseries-context/dynamics/frozen-corpus.db` is **READ-ONLY**. Never call a writing method on it.
- Results are durable in `~/keld/refseries-context/blocks/`. **Never in `docs/`** — the corpus is real developer transcripts.
- Item 4 (tiling equality) is deliberately NOT in this plan. It is a test of Phase 1's implementation.
- Never `git add -A` / `git add .` / `git checkout` / `git stash` / `git clean`. Stage only paths you edit.
- These carry uncommitted owner work and must NOT be touched: `.gitignore`, `internal/agent/daemon/custom_passes.go`, `custom_passes_test.go`, `daemon.go`, `scripts/context_value.py`, `scripts/prompt-v9.md`, `internal/agent/publish/report.go`, `internal/agent/publish/report_show_test.go`.
- Do not start the keld daemon. Never a broad `pkill -f`.

## File Structure

| File | Responsibility |
|---|---|
| `scripts/block_sizing_eval.py` | cut points, block forming, the cap sweep, the merge analysis, JSON + stdout |
| `scripts/test_block_sizing_eval.py` | correctness against a synthetic store. Standalone, no pytest. |
| `~/keld/refseries-context/blocks/block-sizing.json` | raw per-cap output |
| `~/keld/refseries-context/blocks/BLOCK-SIZING-RESULTS.md` | the written result, nulls included |

Shared test scaffolding (write once, in Task 1):

```python
SESSION = "s1"

def _ev(t, level, ref, n=5.0):
    """One reference event. `upsert_events` takes the 9-tuple
    (t, session, repo, branch, sidechain, kind, level, ref, n). One row with n=5.0 gives that
    bin evidence 5.0 at share 1.0 — clearing MIN_EVIDENCE with an unambiguous dominant value."""
    return (t, SESSION, "repo", None, False, "ref", level, ref, n)

def _mkstore(tmp, events):
    st = open_store(os.path.join(tmp, "state", "refseries.db"))
    st.upsert_events(SESSION, events, source_line=1)
    return st
```

---

### Task 1: Cut points and block forming

**Files:**
- Create: `scripts/block_sizing_eval.py`
- Create: `scripts/test_block_sizing_eval.py`

**Interfaces:**
- Consumes: `app.analysis.dynamics.{EwmaSizer, DETECT_LEVEL, DETECT_STEP_S}`, `app.analysis.store.{BIN_SECONDS, open_store}`, `app.analysis.window.{MIN_EVIDENCE, attribution}`, `app.analysis.workstreams.ALLOCATION`, `sizer_eval.{CachingStore, DB, active_bins}`.
- Produces:
  - `CAPS = (10, 15, 20, 30, 45, 60, 90, 120)`
  - `cut_points(store, session, lo, hi) -> [float]` — every rising edge, epoch seconds, ascending.
  - `Block = namedtuple("Block", "start end start_reason end_reason")`
  - `form_blocks(cuts, lo, hi, cap_minutes) -> [Block]`

`form_blocks` is pure: it takes cut points and bounds, and emits contiguous blocks. A block ends at
the next cut if that falls within the cap (`end_reason="detected"`), otherwise at `start + cap`
(`end_reason="budget"`). The first block starts at `lo` with `start_reason="session_start"`; the
last ends at `hi` with `end_reason="session_end"`. Each block's `start_reason` is the previous
block's `end_reason`.

- [ ] **Step 1: Write the failing tests**

```python
def test_cut_points_returns_every_rising_edge_not_just_the_last():
    """`EwmaSizer.plan` takes only the final edge; blocks need them all. If this returns one
    cut on a session with several transitions, the whole measurement is of the wrong thing."""
    with tempfile.TemporaryDirectory() as tmp:
        ev = []
        for i, ref in enumerate(["a"] * 12 + ["b"] * 12 + ["c"] * 12):
            ev.append(_ev(i * 60.0, "branch", ref))
        st = b.CachingStore(_mkstore(tmp, ev))
        cuts = b.cut_points(st, SESSION, 0.0, 36 * 60.0)
        assert len(cuts) >= 2, cuts
        assert cuts == sorted(cuts), cuts

def test_blocks_tile_the_span_with_no_gap_and_no_overlap():
    """The invariant the whole attribution model rests on. Not asserted here for the shipped
    implementation — that is Phase 1 — but the measurement is meaningless if its own blocks
    do not tile."""
    blocks = b.form_blocks([600.0, 1800.0], 0.0, 3600.0, cap_minutes=60)
    assert blocks[0].start == 0.0, blocks
    assert blocks[-1].end == 3600.0, blocks
    for x, y in zip(blocks, blocks[1:]):
        assert x.end == y.start, (x, y)

def test_a_cut_beyond_the_cap_yields_a_budget_boundary():
    blocks = b.form_blocks([5400.0], 0.0, 7200.0, cap_minutes=30)
    assert blocks[0].end == 1800.0, blocks[0]
    assert blocks[0].end_reason == "budget", blocks[0]

def test_a_cut_inside_the_cap_yields_a_detected_boundary():
    blocks = b.form_blocks([600.0], 0.0, 3600.0, cap_minutes=30)
    assert blocks[0].end == 600.0, blocks[0]
    assert blocks[0].end_reason == "detected", blocks[0]

def test_each_start_reason_is_the_previous_end_reason():
    blocks = b.form_blocks([600.0, 5400.0], 0.0, 7200.0, cap_minutes=30)
    assert blocks[0].start_reason == "session_start", blocks[0]
    for x, y in zip(blocks, blocks[1:]):
        assert y.start_reason == x.end_reason, (x, y)
    assert blocks[-1].end_reason == "session_end", blocks[-1]
```

- [ ] **Step 2: Run to verify they fail**

Run: `PYTHONPATH=sidecar ~/.keld/study-venv/bin/python scripts/test_block_sizing_eval.py`
Expected: FAIL — module does not exist.

- [ ] **Step 3: Implement**

```python
CAPS = (10, 15, 20, 30, 45, 60, 90, 120)

Block = collections.namedtuple("Block", "start end start_reason end_reason")


def cut_points(store, session, lo, hi, level=DETECT_LEVEL):
    """Every rising edge the SHIPPED detector finds in `[lo, hi)`, epoch seconds, ascending.

    `EwmaSizer.plan` returns ONE cut — the last edge inside its budget — because it is sizing a
    slice. Blocks need every edge across the whole session, and `fire_indices` already computes
    exactly that; this only maps the indices back to their bucket instants. Nothing about the
    detector is reimplemented, so what is measured here is what would ship.
    """
    sz = EwmaSizer()
    sz.level = level
    obs = sz.observations(store, session, lo, hi)
    if not obs:
        return []
    idx = sz.fire_indices([x for _t, x in obs])
    return [float(obs[i][0]) for i in sorted(idx)]


def form_blocks(cuts, lo, hi, cap_minutes):
    """Contiguous, non-overlapping blocks over `[lo, hi)`, cut at `cuts`, capped at
    `cap_minutes`.

    A block ends at the next cut when that falls inside the cap (`detected`), otherwise at the
    cap (`budget`). Every boundary states WHICH it was, because a detected edge is a claim the
    branch changed and a cap is only "we had to cut somewhere" — Phase 0a measured that the
    second is the common case, so conflating them would mislabel most boundaries.
    """
    cap = cap_minutes * 60.0
    out, t, reason = [], float(lo), "session_start"
    remaining = [c for c in sorted(cuts) if lo < c < hi]
    while t < hi:
        nxt = next((c for c in remaining if c > t), None)
        if nxt is not None and nxt - t <= cap:
            end, end_reason = nxt, "detected"
        elif t + cap < hi:
            end, end_reason = t + cap, "budget"
        else:
            end, end_reason = float(hi), "session_end"
        out.append(Block(t, end, reason, end_reason))
        t, reason = end, end_reason
    return out
```

- [ ] **Step 4: Run to verify they pass**

Run: `PYTHONPATH=sidecar ~/.keld/study-venv/bin/python scripts/test_block_sizing_eval.py`
Expected: 5/5 passed.

- [ ] **Step 5: Commit**

```bash
git add scripts/block_sizing_eval.py scripts/test_block_sizing_eval.py
git commit -m "study(blocks): blocks from every rising edge, with each boundary stating which it was"
```

---

### Task 2: The cap sweep (item 2)

**Files:**
- Modify: `scripts/block_sizing_eval.py`
- Modify: `scripts/test_block_sizing_eval.py`

**Interfaces:**
- Consumes Task 1's `CAPS`, `cut_points`, `form_blocks`, `Block`.
- Produces: `block_evidence(store, session, block) -> int`, `sweep() -> dict`, `choose_cap(rows) -> (int|None, str)`.

`block_evidence` is the total evidence the block's rollup holds at the allocation levels — the same
quantity `window.attribution` gates on. `choose_cap` implements the pre-registered rule and returns
the chosen cap with the reason, or `(None, reason)` when no candidate qualifies.

- [ ] **Step 1: Write the failing tests**

```python
def test_choose_cap_takes_the_smallest_within_five_points_of_the_next():
    rows = [{"cap": 10, "budget_share": 92.0}, {"cap": 15, "budget_share": 80.0},
            {"cap": 20, "budget_share": 62.0}, {"cap": 30, "budget_share": 59.0},
            {"cap": 45, "budget_share": 57.0}]
    cap, why = b.choose_cap(rows)
    assert cap == 20, (cap, why)          # 62.0 - 59.0 = 3.0 <= 5, and 20 is the smallest such

def test_choose_cap_returns_none_when_no_candidate_qualifies():
    """The pre-registration says report it and pick nothing rather than picking anyway."""
    rows = [{"cap": 10, "budget_share": 95.0}, {"cap": 15, "budget_share": 80.0},
            {"cap": 20, "budget_share": 60.0}, {"cap": 30, "budget_share": 40.0}]
    cap, why = b.choose_cap(rows)
    assert cap is None, (cap, why)
    assert why, "must say why"

def test_block_evidence_counts_the_allocation_rollup():
    with tempfile.TemporaryDirectory() as tmp:
        st = b.CachingStore(_mkstore(tmp, [_ev(10, "branch", "main"),
                                           _ev(20, "lang", "Go")]))
        n = b.block_evidence(st, SESSION, b.Block(0.0, 300.0, "session_start", "session_end"))
        assert n == 10, n            # 5.0 + 5.0 across two allocation levels
```

- [ ] **Step 2: Run to verify they fail**

Run: `PYTHONPATH=sidecar ~/.keld/study-venv/bin/python scripts/test_block_sizing_eval.py`
Expected: FAIL — `choose_cap` not defined.

- [ ] **Step 3: Implement**

```python
ALLOC_LEVELS = tuple((level, floor) for _n, level, floor in ALLOCATION)


def block_evidence(store, session, block):
    """Total allocation-level evidence inside a block — the quantity `window.attribution` gates
    on, so "clears MIN_EVIDENCE" here means the block could attribute something."""
    rl = store.rollup_window(session, block.start, block.end)
    return int(sum(n for level, _floor in ALLOC_LEVELS
                   for _ref, n in (rl.get(level) or [])))


def choose_cap(rows):
    """The pre-registered rule: the smallest cap whose `budget` share is within 5 points of the
    next larger cap's. Returns `(cap, why)` or `(None, why)`.

    A threshold rather than an eyeballed elbow, because the elbow is exactly the judgement a
    pre-registration exists to remove.
    """
    rows = sorted(rows, key=lambda r: r["cap"])
    for a, bb in zip(rows, rows[1:]):
        if a["budget_share"] - bb["budget_share"] <= 5.0:
            return a["cap"], (f"budget share {a['budget_share']:.1f}% at {a['cap']}m is within "
                              f"5 points of {bb['budget_share']:.1f}% at {bb['cap']}m")
    return None, ("no candidate cap's budget share came within 5 points of the next larger "
                  "cap's; the curve had not flattened by 120m")
```

`sweep()` walks every session in the frozen store, computes `cut_points` once per session (they do
not depend on the cap), then for each cap forms blocks and accumulates: block count, `budget` vs
`detected` end counts, durations, and how many blocks clear `MIN_EVIDENCE`. It also records, per
cap, the share of SESSIONS whose every boundary was `budget` — the pre-registration asks for that
separately because those sessions measure the cap and nothing else.

- [ ] **Step 4: Run to verify they pass**

Run: `PYTHONPATH=sidecar ~/.keld/study-venv/bin/python scripts/test_block_sizing_eval.py`
Expected: 8/8 passed.

- [ ] **Step 5: Commit**

```bash
git add scripts/block_sizing_eval.py scripts/test_block_sizing_eval.py
git commit -m "study(blocks): sweep the duration cap against a pre-registered flattening rule"
```

---

### Task 3: The merge rule (item 3), the run, and the write-up

**Files:**
- Modify: `scripts/block_sizing_eval.py`
- Modify: `scripts/test_block_sizing_eval.py`
- Create: `~/keld/refseries-context/blocks/block-sizing.json`, `BLOCK-SIZING-RESULTS.md`

**Interfaces:**
- Produces: `merge_thin(store, session, blocks, min_evidence=MIN_EVIDENCE) -> (merged, stats)`, and a `__main__` taking `--caps`/`--out`.

`stats` carries: how many blocks merged, how many merges CHANGED A PUBLISHED VALUE, and how many
chained (a thin block merging into a successor that is itself thin).

⚠️ **The value comparison is the point of item 3.** For each merge, compute the successor's
published `workstreams` BEFORE the merge and the merged block's AFTER, and compare dimension by
dimension. A merge that only raises an evidence count is safe; one that changes a `value` is a
rewrite of what the window said.

- [ ] **Step 1: Write the failing tests**

```python
def test_a_thin_block_merges_forward_taking_the_earlier_start():
    with tempfile.TemporaryDirectory() as tmp:
        st = b.CachingStore(_mkstore(tmp, [_ev(10, "branch", "main", n=1.0),
                                           _ev(400, "branch", "main", n=20.0)]))
        blocks = [b.Block(0.0, 300.0, "session_start", "detected"),
                  b.Block(300.0, 600.0, "detected", "session_end")]
        merged, stats = b.merge_thin(st, SESSION, blocks)
        assert len(merged) == 1, merged
        assert merged[0].start == 0.0 and merged[0].end == 600.0, merged[0]
        assert merged[0].end_reason == "session_end", merged[0]
        assert stats["merged"] == 1, stats

def test_merge_reports_when_it_changed_a_published_value():
    """The question item 3 exists to answer. A merge that flips a dominant value has rewritten
    what the window said, which is a different thing from topping up an evidence count."""
    with tempfile.TemporaryDirectory() as tmp:
        st = b.CachingStore(_mkstore(tmp, [_ev(10, "branch", "old", n=4.0),
                                           _ev(400, "branch", "new", n=5.0)]))
        blocks = [b.Block(0.0, 300.0, "session_start", "detected"),
                  b.Block(300.0, 600.0, "detected", "session_end")]
        _merged, stats = b.merge_thin(st, SESSION, blocks)
        assert stats["merged"] == 1, stats
        assert stats["value_changed"] == 1, stats

def test_a_block_clearing_the_floor_is_left_alone():
    with tempfile.TemporaryDirectory() as tmp:
        st = b.CachingStore(_mkstore(tmp, [_ev(10, "branch", "main", n=9.0),
                                           _ev(400, "branch", "main", n=9.0)]))
        blocks = [b.Block(0.0, 300.0, "session_start", "detected"),
                  b.Block(300.0, 600.0, "detected", "session_end")]
        merged, stats = b.merge_thin(st, SESSION, blocks)
        assert len(merged) == 2, merged
        assert stats["merged"] == 0, stats
```

- [ ] **Step 2: Run to verify they fail**

Run: `PYTHONPATH=sidecar ~/.keld/study-venv/bin/python scripts/test_block_sizing_eval.py`
Expected: FAIL — `merge_thin` not defined.

- [ ] **Step 3: Implement**

`merge_thin` walks blocks in order. When a block's `block_evidence` is below `min_evidence`, it is
absorbed into its successor: the merged block takes the earlier `start`, the earlier
`start_reason`, and the successor's `end`/`end_reason`. A thin FINAL block has no successor and is
absorbed backwards into its predecessor instead — record that case separately in `stats` as
`merged_backward`, since it is the one place the forward rule cannot apply.

For each merge, compare `workstreams.payload(rollup_window(successor span))` against
`workstreams.payload(rollup_window(merged span))` and increment `value_changed` when any
dimension's `value` differs (a dimension appearing or disappearing counts as a change).

- [ ] **Step 4: Run the measurement**

```bash
cd /home/dg/keld/keld-signal
PYTHONPATH=sidecar ~/.keld/study-venv/bin/python scripts/block_sizing_eval.py \
  2>&1 | tee ~/keld/refseries-context/blocks/block-sizing-run.txt
```

- [ ] **Step 5: Apply the rules and write the result**

Write `~/keld/refseries-context/blocks/BLOCK-SIZING-RESULTS.md`: the per-cap table, `choose_cap`'s
verdict and its stated reason, the merge statistics at the chosen cap, and PASS/FAIL against each
pre-registered rule. **Report nulls plainly** — if no cap qualifies, or if merging moves values in
more than 5% of merges, that is the finding.

- [ ] **Step 6: Commit**

```bash
git add scripts/block_sizing_eval.py scripts/test_block_sizing_eval.py
git commit -m "study(blocks): the duration cap and the merge rule, measured against fixed bars"
```

Results are NOT committed — they live under `~/keld/refseries-context/blocks/`.

---

## Self-Review

**Spec coverage.** Item 2: Task 2's sweep and `choose_cap`, with the 5-point rule as code rather
than judgement. Item 3: Task 3's `merge_thin` and its `value_changed` count, with the 5% bar applied
in Step 5. The pre-registration's request for the all-`budget` session share is in Task 2's
`sweep()`. Item 4 is explicitly out, in the Global Constraints and the pre-registration both.

**Placeholders.** `sweep()` and Step 5's write-up are described rather than coded — `sweep()` is
accumulation over a shape the earlier tasks fix, and the write-up is prose. Every function with
behaviour a test can pin carries its code.

**Type consistency.** `Block(start, end, start_reason, end_reason)` is produced in Task 1 and
consumed unchanged in Tasks 2 and 3. `choose_cap` takes the row dicts `sweep()` emits, keyed `cap`
and `budget_share`, matching the test fixtures. `block_evidence` returns an int compared against
`MIN_EVIDENCE` in both Tasks 2 and 3.
