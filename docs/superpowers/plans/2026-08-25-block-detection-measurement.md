# Block Detection Measurement (Phase 0a) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Measure which reference levels detect a block boundary, so Phase 1 knows whether blocks get real boundaries or are cut for length.

**Architecture:** A study script that reuses `scripts/sizer_eval.py`'s scoring **by import, never by copy**, so results are directly comparable to the 2026-08-24 sizer run. It adds two things: a WIDE ground truth over every published allocation level, and a parameterised detection level (`EwmaSizer.level` is a class attribute, so an instance attribute shadows it — the shipped module is not monkeypatched). Correctness is tested against a synthetic store built by hand; only then is it run against the frozen corpus.

**Tech Stack:** Python via `~/.keld/study-venv/bin/python` (`sizer_eval` imports pandas), `sidecar/app/analysis/*` on `PYTHONPATH=sidecar`, standalone test script (no pytest).

## Global Constraints

- The decision rules are FIXED and live in `~/keld/refseries-context/blocks/BLOCK-DETECTION-PREREGISTRATION.md`. Implement them; never restate them from memory in code comments.
- Reuse `sizer_eval.score` **verbatim by import**. Copying it forks the definition of HIT/FP/MISS and destroys comparability with the published 86.4% / 54.8%.
- A level is scored against ground truth **EXCLUDING ITS OWN LEVEL** — and for a pair, excluding **both** its levels. `lang` detecting `lang` flips scores 1.0 and means nothing.
- HIT tolerance is `TOLERANCE_S = BIN_SECONDS` (300s). Imported, never redefined.
- `MIN_EVIDENCE` and `window.attribution` are the SHIPPED ones, imported. Ground truth is deterministic, from the store, never hand-labelled.
- Results are durable in `~/keld/refseries-context/blocks/`. **Never in `docs/`** — the corpus is real developer transcripts.
- The frozen store `~/keld/refseries-context/dynamics/frozen-corpus.db` is **READ-ONLY**. Never call a writing method on it.
- Run everything with `PYTHONPATH=sidecar ~/.keld/study-venv/bin/python`. The sidecar venv lacks pandas by design.
- Do not start the keld daemon. Never a broad `pkill -f`.

## File Structure

| File | Responsibility |
|---|---|
| `scripts/block_detect_eval.py` | The harness: ground truth, the shuffle control, rate arithmetic, the run loop, JSON output. |
| `scripts/test_block_detect_eval.py` | Correctness of the above against a synthetic store. Standalone, no pytest. |
| `~/keld/refseries-context/blocks/block-detect.json` | Raw per-level output. |
| `~/keld/refseries-context/blocks/BLOCK-DETECTION-RESULTS.md` | The written result, including nulls. |

Shared test scaffolding, used by every task (write it once in Task 1):

```python
SESSION = "s1"

def _ev(t, level, ref, n=5.0):
    """One reference event. The 9-tuple `upsert_events` expects:
    (t, session, repo, branch, sidechain, kind, level, ref, n). A single row with n=5.0
    gives that bin evidence 5.0 at share 1.0 — exactly clearing MIN_EVIDENCE with an
    unambiguous dominant value, which is what a ground-truth transition needs on both sides."""
    return (t, SESSION, "repo", None, False, "ref", level, ref, n)

def _mkstore(tmp, events):
    st = open_store(os.path.join(tmp, "state", "refseries.db"))
    st.upsert_events(SESSION, events, source_line=1)
    return st
```

---

### Task 1: Ground truth with level exclusion

**Files:**
- Create: `scripts/block_detect_eval.py`
- Create: `scripts/test_block_detect_eval.py`

**Interfaces:**
- Consumes: `sizer_eval.{CachingStore, DB, SEED, SPAN_MINUTES, Transition, active_bins, score, MIN_ATTRIBUTED, MIN_TRANSITIONS}`; `app.analysis.store.{BIN_SECONDS, open_store}`; `app.analysis.window.{MIN_EVIDENCE, attribution}`; `app.analysis.workstreams.ALLOCATION`.
- Produces: `ALLOC_LEVELS: tuple[(level, floor)]`, `transitions(store, session, exclude=(), levels=ALLOC_LEVELS) -> (int, list[Transition])`.

`exclude` is a **tuple**, not a single level, so a pair excludes both of its levels with no special case at the call site.

- [ ] **Step 1: Write the failing tests**

```python
def test_transitions_finds_a_flip_between_attributed_bins():
    with tempfile.TemporaryDirectory() as tmp:
        st = _mkstore(tmp, [_ev(10, "branch", "main"), _ev(310, "branch", "feature")])
        n_at, trans = b.transitions(st, SESSION)
        assert n_at == 2, n_at
        assert len(trans) == 1, trans
        assert trans[0].instant == 300.0, trans[0]
        assert (trans[0].before, trans[0].after) == ("main", "feature"), trans[0]

def test_transitions_ignores_a_flip_out_of_absent():
    """A bin below MIN_EVIDENCE is `absent`, and a flip out of absent is not a transition —
    the distinction window.REASONS exists to make."""
    with tempfile.TemporaryDirectory() as tmp:
        st = _mkstore(tmp, [_ev(10, "branch", "main", n=1.0), _ev(310, "branch", "feature")])
        n_at, trans = b.transitions(st, SESSION)
        assert n_at == 1, n_at
        assert trans == [], trans

def test_transitions_excludes_the_level_under_test():
    """The tautology guard. `lang` must not be scored on `lang` flips."""
    with tempfile.TemporaryDirectory() as tmp:
        st = _mkstore(tmp, [_ev(10, "lang", "Go"), _ev(310, "lang", "Python"),
                            _ev(10, "branch", "main"), _ev(310, "branch", "feature")])
        _, both = b.transitions(st, SESSION)
        assert {t.level for t in both} == {"lang", "branch"}, both
        _, without = b.transitions(st, SESSION, exclude=("lang",))
        assert {t.level for t in without} == {"branch"}, without

def test_transitions_excludes_every_level_of_a_pair():
    with tempfile.TemporaryDirectory() as tmp:
        st = _mkstore(tmp, [_ev(10, "lang", "Go"), _ev(310, "lang", "Python"),
                            _ev(10, "branch", "main"), _ev(310, "branch", "feature"),
                            _ev(10, "artifact", "code"), _ev(310, "artifact", "docs")])
        _, out = b.transitions(st, SESSION, exclude=("lang", "branch"))
        assert {t.level for t in out} == {"artifact"}, out
```

- [ ] **Step 2: Run to verify they fail**

Run: `PYTHONPATH=sidecar ~/.keld/study-venv/bin/python scripts/test_block_detect_eval.py`
Expected: FAIL — `block_detect_eval` does not exist.

- [ ] **Step 3: Implement**

```python
ALLOC_LEVELS = tuple((level, floor) for _n, level, floor in ALLOCATION)

def transitions(store, session, exclude=(), levels=ALLOC_LEVELS):
    """Every flip of a dominant allocation value, both sides attributed, for one session.

    `exclude` drops the level(s) under test: a detector scored on its own level's flips
    reports a tautology. A tuple rather than a scalar so a pair needs no special case.
    """
    bins = active_bins(store, session)
    rolls = [(t, store.rollup_window(session, t, t + BIN_SECONDS)) for t in bins]
    n_at, out = 0, []
    for level, floor in levels:
        if level in exclude:
            continue
        prev = None
        for t, rl in rolls:
            a = attribution(rl, level, floor, MIN_EVIDENCE)
            if a.reason != "attributed":
                continue
            n_at += 1
            if prev is not None and a.value != prev[1]:
                out.append(Transition(session, level, float(t), prev[1], a.value,
                                      round((t - prev[0]) / 60.0, 1)))
            prev = (t, a.value)
    return n_at, sorted(out, key=lambda x: x.instant)
```

- [ ] **Step 4: Run to verify they pass**

Run: `PYTHONPATH=sidecar ~/.keld/study-venv/bin/python scripts/test_block_detect_eval.py`
Expected: 4/4 passed.

- [ ] **Step 5: Commit**

```bash
git add scripts/block_detect_eval.py scripts/test_block_detect_eval.py
git commit -m "study(blocks): ground truth over every allocation level, excluding the level under test"
```

---

### Task 2: The shuffled-truth control

**Files:**
- Modify: `scripts/block_detect_eval.py`
- Modify: `scripts/test_block_detect_eval.py`

**Interfaces:**
- Produces: `shuffled(trans, bins, rng) -> list[Transition]`.

- [ ] **Step 1: Write the failing tests**

```python
def test_shuffled_preserves_count_and_lands_only_on_active_bins():
    """Rule 2's control destroys the relationship to the work while preserving how many
    transitions there were and how dense the session is — otherwise it would be testing
    a different session, not the same one with its structure removed."""
    trans = [b.Transition(SESSION, "branch", 300.0, "a", "c", 5.0),
             b.Transition(SESSION, "lang", 900.0, "Go", "Py", 5.0)]
    bins = [0, 300, 600, 900, 1200]
    out = b.shuffled(trans, bins, random.Random(1))
    assert len(out) == len(trans), out
    assert all(t.instant in {float(x) for x in bins} for t in out), out
    assert [t.instant for t in out] == sorted(t.instant for t in out), out

def test_shuffled_actually_moves_something():
    """A control that returned its input would silently make every level pass rule 2."""
    trans = [b.Transition(SESSION, "branch", 300.0, "a", "c", 5.0)] * 8
    out = b.shuffled(trans, [0, 300, 600, 900, 1200, 1500], random.Random(7))
    assert any(t.instant != 300.0 for t in out), out

def test_shuffled_on_no_bins_is_empty():
    assert b.shuffled([b.Transition(SESSION, "branch", 1.0, "a", "c", 0.0)], [],
                      random.Random(1)) == []
```

- [ ] **Step 2: Run to verify they fail**

Run: `PYTHONPATH=sidecar ~/.keld/study-venv/bin/python scripts/test_block_detect_eval.py`
Expected: FAIL — `shuffled` not defined.

- [ ] **Step 3: Implement**

```python
def shuffled(trans, bins, rng):
    """Rule 2: every transition relocated to a random non-empty bin of the SAME session.

    Count and density are preserved; only the relationship to the work is destroyed. This is
    the control that collapsed the EWMA sizer from 86.4% to 24.1% while every fixed sizer
    barely moved — which is what makes a positive result here believable rather than assumed.
    """
    if not bins:
        return []
    choices = [float(x) for x in bins]
    return sorted([t._replace(instant=rng.choice(choices)) for t in trans],
                  key=lambda x: x.instant)
```

- [ ] **Step 4: Run to verify they pass**

Run: `PYTHONPATH=sidecar ~/.keld/study-venv/bin/python scripts/test_block_detect_eval.py`
Expected: 7/7 passed.

- [ ] **Step 5: Commit**

```bash
git add scripts/block_detect_eval.py scripts/test_block_detect_eval.py
git commit -m "study(blocks): the shuffled-truth control, which is what makes a positive believable"
```

---

### Task 3: Rate arithmetic

**Files:**
- Modify: `scripts/block_detect_eval.py`
- Modify: `scripts/test_block_detect_eval.py`

**Interfaces:**
- Produces: `EMPTY_AGG() -> dict`, `merge(into, r) -> dict`, `rates(agg) -> dict` with keys `precision, recall, fire_rate, median_dist_min, hit, fp, miss, windows`.

Percentages are rounded to one decimal. `median_dist_min` is in MINUTES and `None` when nothing fired.

- [ ] **Step 1: Write the failing tests**

```python
def test_rates_computes_precision_and_recall_separately():
    """Never an F-score: a false 'the work changed' is a different failure from a missed one,
    and the pre-registration scores them apart."""
    r = b.rates({"hit": 3, "fp": 1, "miss": 2, "fires": 4, "windows": 10, "dists": [60, 120, 300]})
    assert r["precision"] == 75.0, r
    assert r["recall"] == 60.0, r
    assert r["fire_rate"] == 40.0, r
    assert r["median_dist_min"] == 2.0, r

def test_rates_on_an_empty_aggregate_is_zero_not_a_crash():
    r = b.rates(b.EMPTY_AGG())
    assert r["precision"] == 0.0 and r["recall"] == 0.0, r
    assert r["median_dist_min"] is None, r

def test_merge_accumulates_counts_and_distances():
    a = b.EMPTY_AGG()
    b.merge(a, {"hit": 1, "fp": 2, "miss": 3, "fires": 4, "windows": 5, "dists": [10]})
    b.merge(a, {"hit": 1, "fp": 0, "miss": 1, "fires": 1, "windows": 2, "dists": [20, 30]})
    assert (a["hit"], a["fp"], a["miss"], a["fires"], a["windows"]) == (2, 2, 4, 5, 7), a
    assert a["dists"] == [10, 20, 30], a
```

- [ ] **Step 2: Run to verify they fail**

Run: `PYTHONPATH=sidecar ~/.keld/study-venv/bin/python scripts/test_block_detect_eval.py`
Expected: FAIL — `rates` not defined.

- [ ] **Step 3: Implement**

```python
def EMPTY_AGG():
    return {"hit": 0, "fp": 0, "miss": 0, "fires": 0, "windows": 0, "dists": []}

def merge(into, r):
    for k in ("hit", "fp", "miss", "fires", "windows"):
        into[k] += r[k]
    into["dists"] += r["dists"]
    return into

def rates(agg):
    hit, fp, miss = agg["hit"], agg["fp"], agg["miss"]
    prec = hit / (hit + fp) if (hit + fp) else 0.0
    rec = hit / (hit + miss) if (hit + miss) else 0.0
    fire = agg["fires"] / agg["windows"] if agg["windows"] else 0.0
    med = statistics.median(agg["dists"]) / 60.0 if agg["dists"] else None
    return {"precision": round(100 * prec, 1), "recall": round(100 * rec, 1),
            "fire_rate": round(100 * fire, 1),
            "median_dist_min": None if med is None else round(med, 1),
            "hit": hit, "fp": fp, "miss": miss, "windows": agg["windows"]}
```

- [ ] **Step 4: Run to verify they pass**

Run: `PYTHONPATH=sidecar ~/.keld/study-venv/bin/python scripts/test_block_detect_eval.py`
Expected: 10/10 passed.

- [ ] **Step 5: Commit**

```bash
git add scripts/block_detect_eval.py scripts/test_block_detect_eval.py
git commit -m "study(blocks): precision and recall reported apart, never as an F-score"
```

---

### Task 4: The run loop, with the pair excluding both its levels

**Files:**
- Modify: `scripts/block_detect_eval.py`
- Modify: `scripts/test_block_detect_eval.py`

**Interfaces:**
- Produces: `CANDIDATES: tuple[(label, tuple[level, ...])]`, `score_level(cs, sessions, levels_under_test, gt_levels, rng) -> dict`, `run(gt_mode) -> dict`, and a `__main__` taking `--gt {wide,narrow,both}`.

Every candidate's detection levels are a **tuple**, including single levels, so `exclude=` and the sizer assignment have one shape. `EwmaSizer.level` takes the FIRST level of the tuple; a pair's second level is a documented follow-up (the sizer reads one level today) and the pair row is reported as such rather than silently scoring only `branch`.

- [ ] **Step 1: Write the failing test**

```python
def test_a_pair_is_scored_with_both_its_levels_excluded_from_ground_truth():
    """The bug this task exists to prevent: scoring `branch+language` against ground truth
    that still contains branch and lang flips, which is the tautology the pre-registration
    forbids and which an earlier draft of this harness had."""
    with tempfile.TemporaryDirectory() as tmp:
        st = _mkstore(tmp, [_ev(10, "lang", "Go"), _ev(310, "lang", "Python"),
                            _ev(10, "branch", "main"), _ev(310, "branch", "feature"),
                            _ev(10, "artifact", "code"), _ev(310, "artifact", "docs")])
        cs = b.CachingStore(st)
        got = b.score_level(cs, [SESSION], ("branch", "lang"), b.ALLOC_LEVELS,
                            random.Random(1))
        assert got["gt_excluded"] == ["branch", "lang"], got["gt_excluded"]
        assert got["gt_transitions"] == 1, got["gt_transitions"]

def test_candidate_labels_are_all_tuples():
    """One shape for singles and pairs, so exclude= never needs a special case."""
    for label, lv in b.CANDIDATES:
        assert isinstance(lv, tuple), (label, lv)
```

- [ ] **Step 2: Run to verify they fail**

Run: `PYTHONPATH=sidecar ~/.keld/study-venv/bin/python scripts/test_block_detect_eval.py`
Expected: FAIL — `score_level` / `CANDIDATES` not defined.

- [ ] **Step 3: Implement**

```python
# `workspace` is excluded as a candidate: zero transitions across 51 sessions, already measured.
CANDIDATES = (("branch", ("branch",)), ("language", ("lang",)),
              ("output_type", ("artifact",)), ("component", ("component",)),
              ("skill", ("skill",)), ("action", ("action",)),
              ("branch+language", ("branch", "lang")))

def score_level(cs, sessions, levels_under_test, gt_levels, rng):
    """One candidate over every qualifying session: real and shuffled, plus coverage."""
    real, shuf = EMPTY_AGG(), EMPTY_AGG()
    n_sample, n_cov, n_trans, skip = 0, 0, 0, collections.Counter()
    floors = dict(ALLOC_LEVELS)
    probe = levels_under_test[0]
    for s in sessions:
        n_at, trans = transitions(cs, s, exclude=levels_under_test, levels=gt_levels)
        bins = active_bins(cs, s)
        if n_at < MIN_ATTRIBUTED:
            skip["too_few_attributed"] += 1; cs.reset(); continue
        if len(trans) < MIN_TRANSITIONS:
            skip["no_transition"] += 1; cs.reset(); continue
        n_sample += 1
        n_trans += len(trans)
        # Rule 5: coverage is REPORTED, never scored. A level with fine precision on 12% of
        # sessions does not solve the non-engineering problem.
        if probe not in floors or has_level(cs, s, probe, floors.get(probe, 0.5)):
            n_cov += 1
        sz = EwmaSizer(name="ewma:" + "+".join(levels_under_test))
        sz.level = probe            # instance attribute shadows the class attribute
        a = anchors_for(cs, s)
        merge(real, score(sz, cs, s, a, trans))
        merge(shuf, score(sz, cs, s, a, shuffled(trans, bins, rng)))
        cs.reset()
    rr, sr = rates(real), rates(shuf)
    return {"detection_levels": list(levels_under_test),
            "gt_excluded": list(levels_under_test),
            "sessions_scored": n_sample, "gt_transitions": n_trans,
            "coverage_pct": round(100 * n_cov / n_sample, 1) if n_sample else 0.0,
            "real": rr, "shuffled": sr,
            "precision_drop": round(rr["precision"] - sr["precision"], 1),
            "skipped": dict(skip)}

def has_level(store, session, level, floor):
    for t in active_bins(store, session):
        if attribution(store.rollup_window(session, t, t + BIN_SECONDS), level, floor,
                       MIN_EVIDENCE).reason == "attributed":
            return True
    return False

def anchors_for(store, session):
    return [float(t) + BIN_SECONDS for t in active_bins(store, session)]

def run(gt_mode):
    st = open_store(DB)
    cs = CachingStore(st)
    sessions = [s for (s,) in st._conn().execute(
        "SELECT DISTINCT session FROM bin ORDER BY session")]
    gt_levels = (tuple((lv, fl) for lv, fl in ALLOC_LEVELS
                       if lv in ("workspace", "branch")) if gt_mode == "narrow"
                 else ALLOC_LEVELS)
    rng = random.Random(SEED)
    out = {"gt_mode": gt_mode, "sessions_seen": len(sessions), "levels": {}}
    for label, lv in CANDIDATES:
        # In NARROW mode the ground truth IS workspace/branch, so excluding `branch` would
        # leave almost nothing to score against. Narrow exists only to replicate the
        # 2026-08-24 numbers, where no exclusion was applied.
        excl = () if gt_mode == "narrow" else lv
        r = score_level(cs, sessions, lv, gt_levels, rng) if excl else \
            score_level_no_exclude(cs, sessions, lv, gt_levels, rng)
        out["levels"][label] = r
        print(f"  {label:<16} n={r['sessions_scored']:<3} "
              f"prec={r['real']['precision']:>5} rec={r['real']['recall']:>5} "
              f"fire={r['real']['fire_rate']:>5} drop={r['precision_drop']:>5} "
              f"cov={r['coverage_pct']:>5}%", flush=True)
    fx = EMPTY_AGG()
    for s in sessions:
        n_at, trans = transitions(cs, s, exclude=(), levels=gt_levels)
        if n_at >= MIN_ATTRIBUTED and len(trans) >= MIN_TRANSITIONS:
            merge(fx, score(FixedSizer(15), cs, s, anchors_for(cs, s), trans))
        cs.reset()
    out["fixed_15"] = rates(fx)
    print(f"  {'FixedSizer(15)':<16} prec={out['fixed_15']['precision']:>5} "
          f"rec={out['fixed_15']['recall']:>5}", flush=True)
    return out
```

`score_level_no_exclude` is `score_level` with `exclude=()`; implement it by giving `score_level`
an `exclude` parameter defaulting to `levels_under_test` rather than writing a second function:

```python
def score_level(cs, sessions, levels_under_test, gt_levels, rng, exclude=None):
    exclude = levels_under_test if exclude is None else exclude
    ...
    n_at, trans = transitions(cs, s, exclude=exclude, levels=gt_levels)
    ...
    return {..., "gt_excluded": list(exclude), ...}
```

and in `run`, call `score_level(..., exclude=() if gt_mode == "narrow" else None)`.

- [ ] **Step 4: Run to verify they pass**

Run: `PYTHONPATH=sidecar ~/.keld/study-venv/bin/python scripts/test_block_detect_eval.py`
Expected: 12/12 passed.

- [ ] **Step 5: Commit**

```bash
git add scripts/block_detect_eval.py scripts/test_block_detect_eval.py
git commit -m "study(blocks): the run loop, with a pair excluding BOTH its levels from ground truth"
```

---

### Task 5: The replication guard, the run, and the written result

**Files:**
- Modify: `scripts/test_block_detect_eval.py`
- Create: `~/keld/refseries-context/blocks/block-detect.json` (output)
- Create: `~/keld/refseries-context/blocks/BLOCK-DETECTION-RESULTS.md` (output)

**Interfaces:** none new. This task runs what Tasks 1–4 built.

- [ ] **Step 1: Write the replication guard**

This is the single most important check in the plan: if `branch` on NARROW ground truth does not
reproduce the published figure, the harness has drifted and no other number here can be trusted.

```python
def test_branch_on_narrow_ground_truth_replicates_the_published_figure():
    """SIZER-RESULTS.md published EwmaSizer on `branch` at 86.4% precision / 54.8% recall.
    NARROW ground truth + no exclusion is that same configuration. Allow +/- 5 points for
    the anchor-set difference (this harness anchors on every active bin); a miss outside
    that means the harness moved and every other result in this run is void.

    SKIPPED, not failed, when the frozen store is absent — the store is 60 MB and lives
    outside the repo, so a checkout without it must still be able to run the unit tests.
    """
    if not os.path.exists(b.DB):
        print("    (skipped: frozen store not present)")
        return
    out = b.run("narrow")
    got = out["levels"]["branch"]["real"]
    assert abs(got["precision"] - 86.4) <= 5.0, f"precision {got['precision']} vs 86.4"
    assert abs(got["recall"] - 54.8) <= 5.0, f"recall {got['recall']} vs 54.8"
```

- [ ] **Step 2: Run it**

Run: `PYTHONPATH=sidecar ~/.keld/study-venv/bin/python scripts/test_block_detect_eval.py`
Expected: 13/13 passed. **If the replication assertion fails, STOP** — do not proceed to the wide
run, and report the discrepancy. A harness that cannot reproduce a known result cannot establish
a new one.

- [ ] **Step 3: Run the measurement**

```bash
cd /home/dg/keld/keld-signal
PYTHONPATH=sidecar ~/.keld/study-venv/bin/python scripts/block_detect_eval.py --gt both \
  2>&1 | tee ~/keld/refseries-context/blocks/block-detect-run.txt
```

- [ ] **Step 4: Apply the decision rules and write the result**

Write `~/keld/refseries-context/blocks/BLOCK-DETECTION-RESULTS.md` with a row per candidate:
precision, recall, fire rate, median distance, precision drop under shuffling, coverage — and for
each, PASS or FAIL against each of the six numbered rules in the pre-registration, naming which
rule failed. **Report nulls plainly.** If nothing beats `branch`, the result is that blocks are
`budget`-cut for sessions without branch activity, and that is the finding.

- [ ] **Step 5: Commit**

```bash
git add scripts/test_block_detect_eval.py
git commit -m "study(blocks): pin the replication guard; results are durable out-of-repo"
```

Results are NOT committed — they live under `~/keld/refseries-context/blocks/` because they derive
from real developer transcripts.

---

## Self-Review

**Spec coverage.** The pre-registration's six decision rules: rule 1 (beats fixed) needs the
`fixed_15` baseline — Task 4 emits it. Rule 2 (survives shuffling) — Task 2, surfaced as
`precision_drop`. Rule 3 (not degenerate) — `fire_rate` in Task 3. Rule 4 (a pair earns its second
level) — Task 4's `CANDIDATES` includes the pair; the rule is applied in Task 5's write-up. Rule 5
(coverage reported) — `coverage_pct` in Task 4. Rule 6 (a null is a result) — Task 5 Step 4
states it. Both ground-truth modes — Task 4's `--gt`. The replication check — Task 5 Step 1.

**One gap I am leaving open deliberately.** `EwmaSizer` reads ONE level, so the pair candidate
scores on `branch` alone while excluding both from ground truth. That makes it a conservative
lower bound for the pair, not a measurement of a two-level detector — a real two-level encoding is
a change to `EwmaSizer.observations` and belongs in Phase 1 if rule 4 looks close. Task 4 says so
in the code; Task 5's write-up must repeat it rather than reporting the pair as if it were the
real thing.

**Placeholders:** none. Every step carries its code.

**Type consistency:** `transitions(..., exclude=())` takes a tuple everywhere; `CANDIDATES` values
are tuples including singletons; `score_level`'s `exclude=None` defaults to the levels under test;
`rates()` keys are consumed identically in Task 4's print and Task 5's write-up.
