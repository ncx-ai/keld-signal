# Block cutter (Phase 1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to
> implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Put the measured block boundary rule — arm A′, a 20-minute cap plus a 15-minute idle
terminator plus detected cuts — into shipped sidecar code, and report the block containing a prompt
on the live `/analyze` path without changing any published value.

**Architecture:** New pure module `sidecar/app/analysis/blocks.py`, mirroring `dynamics.py` /
`prior.py`: functions over stored rows, no I/O of its own. Detection reuses `dynamics.EwmaSizer`
unchanged. `/analyze` gains one additive `block` object. Nothing in the Go publish path changes.

**Tech Stack:** Python 3.12 (sidecar venv), native sqlite3, FastAPI. Standalone test scripts, no
pytest.

## Global Constraints

- **Interpreter: `~/.keld/sidecar-venv/bin/python`** for all sidecar code and tests. Never host
  python. Study scripts under `scripts/` use `~/.keld/study-venv/bin/python` instead.
- **Sidecar tests are standalone scripts, not pytest.** Each ends with a `__main__` runner that
  calls every `test_*` function. Follow the existing files exactly.
- **The measured constants are `MAX_BLOCK_MINUTES = 20` and `IDLE_BINS = 3`** (15 minutes). Both
  come from `~/keld/refseries-context/blocks/BLOCK-BOUND-2-RESULTS.md`. Do not round, retune, or
  "improve" them.
- **There is NO merge rule.** A block below `MIN_EVIDENCE` publishes unattributed. Measured: merging
  changes a published value 88.6% of the time it fires, against a 5% bar. Do not add one.
- **`blocks.py` imports from the sidecar only** — never from `scripts/`. The study script imports
  the sidecar, not the reverse.
- **Privacy:** blocks are computed from stored coordinate rows. No prompt text, no spans, no
  offsets. Do not add a field carrying any.
- **`frontier()` / `tail_closed()` in `analysis/tick.py` are load-bearing and must not be touched.**
- Do not modify `dynamics.EwmaSizer`. Detection must be the shipped detector, unmodified, or what
  ships is not what was measured.
- Never `git add -A`, `git add .`, `git checkout`, `git stash`, `git clean`. Commit with
  `git commit --only -- <explicit paths>` — this tree holds uncommitted files owned by others.
- Do not start the keld daemon. Never a broad `pkill -f`.

---

### Task 1: `blocks.py` — the cutter

**Files:**
- Create: `sidecar/app/analysis/blocks.py`
- Create: `sidecar/app/analysis/test_blocks.py`

**Interfaces:**
- Consumes: `dynamics.EwmaSizer` / `DETECT_LEVEL`, `store.BIN_SECONDS`, `window.MIN_EVIDENCE`.
- Produces:
  - `Block = collections.namedtuple("Block", "start end start_reason end_reason")` (epoch seconds)
  - `MAX_BLOCK_MINUTES = 20`, `IDLE_BINS = 3`
  - `active_bins(store, session) -> [int]`
  - `active_segments(store, session, lo, hi, idle_bins=IDLE_BINS) -> [(float, float)]`
  - `cut_points(store, session, lo, hi, level=DETECT_LEVEL) -> [float]`
  - `cut(store, session, from_ts, to_ts, max_minutes=MAX_BLOCK_MINUTES, idle_bins=IDLE_BINS) -> [Block]`

The reference implementation is `scripts/block_sizing_eval.py`: `form_blocks`, `active_segments`,
`cut_points`, and `with_idle(bound_time)`. Port that behaviour; Task 2 pins the equivalence.

Reasons come from the closed set `session_start` / `detected` / `idle` / `budget` / `session_end`.
Within one active segment: a detected cut inside the cap ends the block `detected`, otherwise the
cap ends it `budget`, otherwise the segment end. At a segment seam the earlier block ends `idle`
and the later one starts `idle`; the first block of the span starts `session_start` and the last
ends `session_end`.

- [ ] **Step 1: Write the failing tests**

```python
def test_idle_splits_only_on_a_gap_at_or_over_the_threshold():
    """A gap of k-1 empty bins is a pause inside one segment; k empty bins ends it."""
    st = _store([_ev(0.0), _ev(4 * BIN_SECONDS)])          # 3 empty bins between
    assert len(blocks.active_segments(st, S, 0.0, 5 * BIN_SECONDS, idle_bins=3)) == 2
    st2 = _store([_ev(0.0), _ev(3 * BIN_SECONDS)])         # 2 empty bins between
    assert len(blocks.active_segments(st2, S, 0.0, 4 * BIN_SECONDS, idle_bins=3)) == 1


def test_every_active_bin_lies_in_exactly_one_block():
    """The invariant the whole attribution model rests on. NOT tiling of the span: idle time is
    in no block, on purpose."""
    st = _store([_ev(i * 60.0) for i in range(10)]
                + [_ev(3600.0 + i * 60.0) for i in range(10)])
    lo, hi = _bounds(st)
    bl = blocks.cut(st, S, lo, hi)
    for x, y in zip(bl, bl[1:]):
        assert x.end <= y.start, (x, y)
    for t in blocks.active_bins(st, S):
        assert len([b for b in bl if b.start <= t < b.end]) == 1, t


def test_no_block_is_empty_and_none_exceeds_the_cap():
    st = _store([_ev(i * 60.0) for i in range(10)]
                + [_ev(3600.0 + i * 60.0) for i in range(10)])
    lo, hi = _bounds(st)
    for b in blocks.cut(st, S, lo, hi):
        assert b.end - b.start <= blocks.MAX_BLOCK_MINUTES * 60.0 + 1e-6, b
        assert _evidence(st, b) > 0, b


def test_a_detected_cut_inside_the_cap_ends_the_block_before_the_cap_does():
    """Detection wins over the bound; a block ending `budget` where a cut was available would
    mean the shipped detector is not reaching the cutter."""
    st = _store([_ev(i * 60.0, ref="main") for i in range(6)]
                + [_ev(360.0 + i * 60.0, ref="feature") for i in range(18)])
    lo, hi = _bounds(st)
    bl = blocks.cut(st, S, lo, hi)
    assert any(b.end_reason == "detected" for b in bl), bl


def test_reasons_come_from_the_closed_set_and_chain():
    st = _store([_ev(i * 60.0) for i in range(10)]
                + [_ev(3600.0 + i * 60.0) for i in range(10)])
    lo, hi = _bounds(st)
    bl = blocks.cut(st, S, lo, hi)
    ok = {"session_start", "detected", "idle", "budget", "session_end"}
    assert bl[0].start_reason == "session_start"
    assert bl[-1].end_reason == "session_end"
    for b in bl:
        assert b.start_reason in ok and b.end_reason in ok, b
    for x, y in zip(bl, bl[1:]):
        assert y.start_reason == x.end_reason, (x, y)


def test_a_thin_block_is_kept_unattributed_and_never_merged():
    """0c: the merge rule was measured to change a published value 88.6% of the time it fires,
    against a 5% bar, so it is not built. A thin tail must survive as its own block."""
    st = _store([_ev(i * 60.0) for i in range(10)] + [_ev(3600.0)])
    lo, hi = _bounds(st)
    bl = blocks.cut(st, S, lo, hi)
    thin = [b for b in bl if _evidence(st, b) < MIN_EVIDENCE]
    assert thin, bl
```

- [ ] **Step 2: Run to verify they fail**

Run: `cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/analysis/test_blocks.py`
Expected: FAIL — no module named `blocks`.

- [ ] **Step 3: Implement `blocks.py`**

Port `scripts/block_sizing_eval.py`'s `form_blocks` / `active_segments` / `cut_points` and the
`with_idle` composition into one `cut()`. Reuse `EwmaSizer` by instantiating it and setting
`sz.level` as an instance attribute (the class attribute is shadowed; do not monkeypatch).

- [ ] **Step 4: Run to verify they pass**

- [ ] **Step 5: Commit**

```bash
git commit --only -q -m "blocks: the measured cutter — 20m cap, 15m idle, detected cuts" \
  -- sidecar/app/analysis/blocks.py sidecar/app/analysis/test_blocks.py
```

---

### Task 2: the equivalence oracle

**Files:**
- Modify: `scripts/test_block_sizing_eval.py`

**Interfaces:**
- Consumes: `blocks.cut` from Task 1, and `block_sizing_eval.with_idle(bound_time)`.

The codebase's own idiom: `analyze_window_by_parse` is retained as the ORACLE and never as a
fallback. Same here — the study implementation is the definition of what was measured, so the
shipped cutter is asserted equal to it rather than assumed equal.

- [ ] **Step 1: Write the failing test**

```python
def test_shipped_cutter_is_identical_to_the_measured_arm():
    """What ships must BE what was measured. `blocks.cut` and `with_idle(bound_time)` at the
    shipped constants must agree block-for-block, reason-for-reason, on a store exercising all
    five reasons: a detected cut, a cap, an idle gap and both session edges."""
    from app.analysis import blocks
    with tempfile.TemporaryDirectory() as tmp:
        ev = ([_ev(i * 60.0, "branch", "main", n=2.0) for i in range(6)]
              + [_ev(360.0 + i * 60.0, "branch", "feature", n=2.0) for i in range(24)]
              + [_ev(7200.0 + i * 60.0, "branch", "feature", n=2.0) for i in range(20)])
        st = b.CachingStore(_mkstore(tmp, ev))
        lo, hi = b.session_bounds(st, SESSION)
        study_fn = b.with_idle(b.bound_time, "x")
        b.IDLE_BINS = blocks.IDLE_BINS
        study = study_fn(st, SESSION, b.cut_points(st, SESSION, lo, hi), lo, hi,
                         blocks.MAX_BLOCK_MINUTES)
        st.reset()
        shipped = blocks.cut(st, SESSION, lo, hi)
        assert len(shipped) == len(study), (len(shipped), len(study))
        for s, m in zip(shipped, study):
            assert (s.start, s.end, s.start_reason, s.end_reason) == \
                   (m.start, m.end, m.start_reason, m.end_reason), (s, m)
        reasons = {x.end_reason for x in shipped} | {x.start_reason for x in shipped}
        assert {"detected", "budget", "idle", "session_start", "session_end"} <= reasons, reasons
```

⚠️ The last assertion matters: an equivalence test over a fixture that exercises only one reason
would pass while proving nothing. If the fixture does not produce all five, adjust the FIXTURE
until it does — never the assertion.

- [ ] **Step 2: Run to verify it fails** (before Task 1 is merged it cannot import; after, it must
      pass). Expected on a deliberate one-off perturbation of `blocks.MAX_BLOCK_MINUTES`: FAIL.

- [ ] **Step 3: Make it pass** — no implementation expected; if it fails, Task 1 is wrong and that
      is the finding. Report rather than editing the test to agree.

- [ ] **Step 4: Commit**

```bash
git commit --only -q -m "blocks: pin the shipped cutter equal to the measured arm" \
  -- scripts/test_block_sizing_eval.py
```

---

### Task 3: `/analyze` reports the block containing the prompt

**Files:**
- Modify: `sidecar/app/analysis/analyze.py` (`_payload`, and its call sites)
- Modify: `sidecar/app/test_main.py` or the nearest existing analyze test
- Modify: `sidecar/app/analysis/test_blocks.py` (one integration test)

**Interfaces:**
- Consumes: `blocks.cut` from Task 1.
- Produces: an additive `block` key on the `/analyze` payload:
  `{"start": iso, "end": iso, "start_reason": str, "end_reason": str}`, or `null` when the
  prompt's instant falls in no block (it is inside an idle gap).

⚠️ **ADDITIVE ONLY. Do not change `window_start`, `window_end`, or any workstreams/dynamics/prior
value.** The 60-minute look-back stays exactly as it is. This task makes the measured boundary
*visible* on the live path; changing what the window characterises is a later phase with its own
schema bump and eval re-run. A reviewer finding this task altering an existing published value
should treat it as a Critical defect.

Go tolerates unknown `/analyze` fields (`internal/agent/enrich/sidecar/analyze_test.go` pins that
`DisallowUnknownFields` is deliberately absent), so no Go change is required and none should be
made in this task.

- [ ] **Step 1: Write the failing tests**

```python
def test_analyze_reports_the_block_containing_the_prompt():
    """The live path computes A' boundaries. Additive: every pre-existing key keeps its value."""
    # build a store + transcript fixture the way the existing analyze tests do
    out = analyze_window(path, prompt_id, store=st)
    assert out["block"] is not None
    assert out["block"]["start_reason"] in {"session_start", "detected", "idle", "budget"}
    assert out["block"]["end_reason"] in {"detected", "idle", "budget", "session_end"}


def test_adding_the_block_changed_no_existing_analyze_field():
    """The regression that matters. Same call, block key removed, must equal the payload the
    previous schema produced for every other key."""
    out = analyze_window(path, prompt_id, store=st)
    before = {k: v for k, v in out.items() if k != "block"}
    assert set(before) == EXPECTED_KEYS_BEFORE_BLOCK
    assert before["window_start"] == ... and before["window_end"] == ...
```

- [ ] **Step 2: Run to verify they fail**

Run: `cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_main.py`

- [ ] **Step 3: Implement**

Compute the block set once for the session span and select the block containing the prompt's
instant. If `SCHEMA` in `analysis/analyze.py` (or wherever it is defined) versions the payload
shape, bump it and say so in the commit message — check how it is consumed before deciding.

- [ ] **Step 4: Run the FULL sidecar suite**

```bash
cd sidecar
for f in app/test_*.py app/analysis/test_*.py; do
  PYTHONPATH=. ~/.keld/sidecar-venv/bin/python "$f" || echo "FAILED $f"; done
```
Every file must pass. Paste the output.

- [ ] **Step 5: Run the Go suite** — it must be untouched by this change.

```bash
go test ./internal/agent/enrich/... ./internal/agent/publish/...
```

- [ ] **Step 6: Commit**

```bash
git commit --only -q -m "blocks: /analyze reports the block containing the prompt" \
  -- sidecar/app/analysis/analyze.py sidecar/app/test_main.py sidecar/app/analysis/test_blocks.py
```

---

## Self-Review

**Spec coverage.** Phase 1 of `2026-08-25-signal-block-pipeline-design.md`: the module (Task 1),
the constants and the no-merge decision (Task 1, pinned by a test), equality with what was measured
(Task 2), and reaching the live path (Task 3). Phases 2 (`covers`), 3 (the Go wire), 4 (tick as
trigger) and 5 (default flip) are OUT OF SCOPE and remain gated on Atlas learning a time+identity
join.

**Placeholders.** Task 3's fixture construction is described rather than coded, because it must
follow whatever the existing analyze tests already do — the implementer reads that file. Every
other step carries its code.

**Type consistency.** `Block` fields are epoch-second floats throughout `blocks.py`; only
`/analyze`'s payload converts to ISO, matching `window_start`/`window_end`'s existing format.
`cut()` keeps the study's `(store, session, lo, hi, ...)` argument order.
