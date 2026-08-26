# Signal Embeddings — Step 1 (Capture) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Persist four signals the reference-series store currently computes and throws away — per-role character counts, the raw token split, tool-call outcomes, and a per-bin byte-offset index — behind a fingerprinted `capture` toggle, so a later step can build feature vectors from them.

**Architecture:** Three of the four signals need **no new table**: `turn_magnitude`'s `kind` column is a documented extension point ("New magnitudes are therefore data rather than DDL"), so `say`/`tok`/tool-outcome rows are routed there at the store boundary. `levels.events_for_turns` is **not modified** — it already emits `say` and `tok` rows and they are simply no longer discarded, which keeps the `/analyze` oracle's row shape byte-identical. Tool outcomes and byte offsets come from one new cheap pass over raw lines (`capture.py`) that never calls `json.loads`. Only the byte-offset index adds DDL.

**Tech Stack:** Python 3.12 (sidecar venv at `~/.keld/sidecar-venv`), native SQLite, no new dependencies. Tests are standalone scripts with a `__main__` runner — **no pytest**.

**Spec:** `docs/superpowers/specs/2026-08-26-signal-embeddings-design.md` (§ "What is wired through at ingest", § "Toggles")

## Global Constraints

- **Run everything with `~/.keld/sidecar-venv/bin/python`**, never the host interpreter. Host python is 3.14 and lacks the wheels.
- **Run tests as:** `cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_<name>.py`. Each test file ends with the standard runner; there is no pytest.
- **No new dependency may be added to `sidecar/requirements.txt`.**
- **The store must never hold message text.** Character counts and token counts are numbers and are permitted; a byte of a transcript body is not. The `event` table's existing invariant — no text-derived level, no empty `ref` — must remain true and is pinned by a test that stays green.
- **`levels.events_for_turns` output must stay byte-identical.** `analyze_window_by_parse` is the ORACLE for the store path; changing what that function emits changes the oracle. Every change in this plan is at the store boundary or in a new module.
- **Default OFF.** `KELD_CAPTURE` defaults to `"0"`. Nothing in this plan changes behaviour for a user who does not set it.
- **No `STATE_VERSION` bump.** See Task 2 for why: a bump forces a reparse of every store on upgrade, including for users who will never turn capture on.
- Style: match the surrounding code. This codebase writes long explanatory docstrings that state *why*, and marks decisions someone will try to revert with `⚠️`. Follow that.

---

## File Structure

| file | responsibility | change |
|---|---|---|
| `sidecar/app/analysis/magnitude.py` | the vocabulary of magnitude kinds | add `CAPTURE_KINDS`; `KINDS` stays the cost kinds |
| `sidecar/app/analysis/store.py` | persistence | scope `has_magnitudes`; route `say`/`tok` in `_aggregate_mag`; add `bin_offset` DDL + accessors |
| `sidecar/app/analysis/capture.py` | **NEW** — one raw-line pass yielding tool outcomes and per-bin byte offsets, without `json.loads` | create |
| `sidecar/app/analysis/ingest.py` | the tail parse and its checkpoint | `capture_mode()` fingerprint; per-line byte offsets from `_read_complete_lines`; call `capture.py`; thread the flag |
| `sidecar/app/test_capture.py` | **NEW** — tests for the new pass | create |
| `sidecar/app/test_store.py` | store contract | one test renamed + re-argued; new tests for routing and `bin_offset` |
| `sidecar/app/test_ingest.py` | ingest contract | new tests for the fingerprint and the offset index |

---

### Task 1: Stop `has_magnitudes` answering for kinds that are not costs

`Store.has_magnitudes` asks "does this window carry a magnitude of ANY kind?" and does not filter on `kind`. It is the gate between a truthful "authored 0 bytes" and an honest "no record" (`magnitude.authored`), and its answer reaches the published block payload as `authored_status`.

⚠️ **This task must land before any new kind exists.** The moment `say_user` rows are stored, every window containing a message would report `has_magnitudes == True`, flipping `authored_status` from "no record" to "authored 0 bytes" on windows where nothing was ever costed. That is a silent change to a published field, caused by a table gaining rows that have nothing to do with the question being asked.

**Files:**
- Modify: `sidecar/app/analysis/store.py:1329-1345` (`has_magnitudes`)
- Test: `sidecar/app/test_store.py`

**Interfaces:**
- Consumes: `magnitude.KINDS` — the existing `(TOKENS, REQUEST_TOKENS, EDIT_BYTES)` tuple. It has exactly one other consumer (`test_magnitude.py:241`) and is not changed by this plan.
- Produces: `Store.has_magnitudes(session, start, end) -> bool`, unchanged signature, now scoped to cost kinds.

- [ ] **Step 1: Write the failing test**

Add to `sidecar/app/test_store.py`:

```python
def test_has_magnitudes_ignores_non_cost_kinds():
    """`has_magnitudes` gates `magnitude.authored`'s "no record" answer, which reaches the
    published `authored_status`. It must answer for COST kinds only.

    ⚠️ Without the kind filter, storing any other magnitude — a character count, a token split,
    a tool-error count — would flip a window from "we never looked" to "we looked and it was
    zero", on windows where nothing was ever costed. That is a published field changing because
    an unrelated table gained rows.
    """
    with tempfile.TemporaryDirectory() as tmp:
        st = open_store(os.path.join(tmp, "s.db"))
        capture = (round(T0 + 5, 1), SESSION, None, None, False, "mag", "say_asst", "", 1873.0)
        st.upsert_events(SESSION, [capture], source_line=1)
        assert st.has_magnitudes(SESSION, T0 - 1, T0 + 3600) is False, \
            "a non-cost magnitude must not answer the authored-record question"
        cost = (round(T0 + 6, 1), SESSION, None, None, False, "mag", "edit_bytes", "", 42.0)
        st.upsert_events(SESSION, [cost], source_line=2)
        assert st.has_magnitudes(SESSION, T0 - 1, T0 + 3600) is True, \
            "a cost magnitude must still answer it"
        st.close()
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_store.py
```

Expected: FAIL on the first assertion — `has_magnitudes` returns `True` for the `say_asst` row, because the current query has no `kind` predicate.

- [ ] **Step 3: Write minimal implementation**

Replace the query in `sidecar/app/analysis/store.py`:

```python
    def has_magnitudes(self, session, start, end):
        """Does this window carry a COST magnitude — one of `magnitude.KINDS`?

        The gate between a truthful "authored 0 bytes" and an honest "no record" -- see
        `magnitude.authored`. It is a separate question from `turn_magnitudes(kind=EDIT_BYTES)`
        being empty, because a v5 store upgraded in place holds NO magnitudes until its next
        ingest (see this module's 5 -> 6 schema note), and reporting 0 bytes authored on the
        strength of never having looked is precisely the plausible wrong number this series keeps
        paying for.

        ⚠️ IT IS SCOPED TO THE COST KINDS, AND USED TO SAY "ANY KIND". `turn_magnitude` is a
        deliberate extension point -- its `kind` is a dimension, so a new magnitude is data
        rather than DDL -- and the capture kinds (`magnitude.CAPTURE_KINDS`: character counts,
        the token split, tool outcomes) now live in the same table. Unscoped, this question would
        be answered "yes, we looked" by the mere presence of a message, on a window where nothing
        was ever costed. The predicate is what keeps a published field from moving because an
        unrelated row arrived.
        """
        start, end = _epoch(start), _epoch(end)
        if not end > start:
            return False
        holes = ",".join("?" * len(magnitude.KINDS))
        return self._conn().execute(f"""
            SELECT 1 FROM turn_magnitude
            WHERE session = ? AND ts >= ? AND ts < ? AND kind IN ({holes})
            LIMIT 1""",
            (session, start, end) + tuple(magnitude.KINDS)).fetchone() is not None
```

- [ ] **Step 4: Run the full sidecar suite to verify nothing else moved**

```bash
cd sidecar && for f in app/test_*.py; do PYTHONPATH=. ~/.keld/sidecar-venv/bin/python "$f" >/dev/null 2>&1 || echo "FAIL $f"; done; echo done
```

Expected: no `FAIL` lines. This is the regression check that matters — `test_analysis_blockdigest.py` and `test_analyze_store.py` both exercise `authored_status`.

- [ ] **Step 5: Commit**

```bash
git add sidecar/app/analysis/store.py sidecar/app/test_store.py
git commit -m "store: scope has_magnitudes to the COST kinds

turn_magnitude's kind is a documented extension point, so capture kinds are
about to land in the same table. has_magnitudes asks 'any kind at all', and
its answer reaches the published authored_status via magnitude.authored --
so a character-count row would have flipped windows from 'we never looked'
to 'we looked and it was zero'. Scoped before the new kinds exist, not after."
```

---

### Task 2: The `capture` toggle and its parse-state fingerprint

`KELD_CAPTURE` gates everything else in this plan. Like `KELD_TERMS`, flipping it changes what the store permanently holds, so it must be part of the parse-state checkpoint: a store ingested with capture off holds no capture rows, and no later recomputation can supply them.

⚠️ **No `STATE_VERSION` bump, deliberately.** A bump invalidates every stored state on upgrade and forces a whole-file reparse of every transcript on every machine — including machines that will never set `KELD_CAPTURE=1`, for rows they will never hold. Instead the stored key defaults to `"0"` when absent, so an existing state reads as capture-off and stays usable. Only actually turning capture on costs a reparse, and it costs it once.

**Files:**
- Modify: `sidecar/app/analysis/ingest.py` — add `capture_mode()` near `terms_mode` (~line 121); `_state_is_usable` (~line 331); `_dump_state` (~line 369)
- Test: `sidecar/app/test_ingest.py`

**Interfaces:**
- Produces: `ingest.capture_mode() -> str` — `"1"` or `"0"`, read from `KELD_CAPTURE`. Tasks 3–5 call this to decide whether to emit.
- Produces: `_dump_state(...)` gains a `"capture"` key; `_state_is_usable(raw, nlp, resolved)` gains a capture clause. Signatures unchanged.

- [ ] **Step 1: Write the failing test**

Add to `sidecar/app/test_ingest.py`:

```python
def test_capture_mode_is_part_of_the_parse_state():
    """Flipping KELD_CAPTURE changes what the store permanently holds, so a state written under
    one setting must not be resumed under the other -- the same trap `terms_mode` exists for.

    ⚠️ And a state that PREDATES the key must read as capture-off, not as a mismatch. Treating
    the absent key as unusable would force a whole-file reparse of every transcript on every
    machine at upgrade, including machines that never turn capture on.
    """
    from app.analysis.ingest import _dump_state, _state_is_usable, capture_mode
    prev = os.environ.get("KELD_CAPTURE")
    try:
        os.environ["KELD_CAPTURE"] = "0"
        assert capture_mode() == "0"
        off = _dump_state(new_evidence(), [], [], 0, None)
        assert off["capture"] == "0"
        assert _state_is_usable(off, None) is True

        os.environ["KELD_CAPTURE"] = "1"
        assert capture_mode() == "1"
        assert _state_is_usable(off, None) is False, "capture-on must not resume a capture-off state"
        on = _dump_state(new_evidence(), [], [], 0, None)
        assert _state_is_usable(on, None) is True

        os.environ["KELD_CAPTURE"] = "0"
        assert _state_is_usable(on, None) is False, "capture-off must not resume a capture-on state"

        legacy = dict(off)
        legacy.pop("capture")
        assert _state_is_usable(legacy, None) is True, \
            "a state predating the key must read as capture-off, not force a reparse"
    finally:
        if prev is None:
            os.environ.pop("KELD_CAPTURE", None)
        else:
            os.environ["KELD_CAPTURE"] = prev
```

If `new_evidence` is not already imported in that test file, add it to the existing `from app.analysis.ingest import ...` line.

- [ ] **Step 2: Run test to verify it fails**

```bash
cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_ingest.py
```

Expected: FAIL with `ImportError: cannot import name 'capture_mode'`.

- [ ] **Step 3: Write minimal implementation**

Add to `sidecar/app/analysis/ingest.py`, immediately after `terms_mode`:

```python
def capture_mode():
    """A fingerprint of the CAPTURE pipeline the store's magnitude rows were written with.

    The sibling of `terms_mode`, and it exists for the identical reason: the capture rows
    (`magnitude.CAPTURE_KINDS` -- per-role character counts, the raw token split, tool outcomes
    -- plus the `bin_offset` index) are written at INGEST and are never re-derived. A transcript
    ingested under `KELD_CAPTURE=0` holds none of them, and no later call can supply them,
    because the bytes they came from have already been consumed and the checkpoint advanced.
    So a store holding rows from a run with capture and rows from a run without it is holding
    two incomparable populations, and nothing in the data says which is which.

    ⚠️ THE DEFAULT IS OFF, AND AN ABSENT KEY READS AS OFF. `_state_is_usable` compares against
    `raw.get("capture") or "0"`, so a state written before this key existed is still resumable.
    The alternative -- bumping STATE_VERSION -- would force a whole-file reparse of every
    transcript on every machine at upgrade, to obtain rows that a machine with capture off will
    never hold. Turning capture ON is what costs a reparse, and it costs it once.
    """
    return "1" if os.environ.get("KELD_CAPTURE", "0") == "1" else "0"
```

In `_state_is_usable`, extend the first condition and the docstring's list of reasons:

```python
    if not (bool(raw) and int(raw.get("v") or 0) == STATE_VERSION
            and raw.get("terms") == terms_mode(nlp)
            and (raw.get("capture") or "0") == capture_mode()):
        return False
```

In `_dump_state`'s returned dict, add the key immediately after `"terms"`:

```python
            "capture": capture_mode(),
```

Also add a fifth reason to `_state_is_usable`'s docstring, after the `terms_mode` one:

```
    or it was written under a different capture setting (`capture_mode`);
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_ingest.py
cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_store.py
```

Expected: both PASS.

- [ ] **Step 5: Commit**

```bash
git add sidecar/app/analysis/ingest.py sidecar/app/test_ingest.py
git commit -m "ingest: KELD_CAPTURE, fingerprinted into the parse state

The sibling of terms_mode, for the same reason: capture rows are written at
ingest and never re-derived, so a store mixing capture-on and capture-off
runs holds two incomparable populations.

No STATE_VERSION bump -- an absent key reads as capture-off, so an existing
state stays resumable and only turning capture ON costs a reparse. A bump
would have reparsed every transcript on every machine to obtain rows that a
capture-off machine never holds."
```

---

### Task 3: Route `say` and `tok` into `turn_magnitude`

`levels.events_for_turns` already emits these and `Store.upsert_events` drops them. `turn_magnitude`'s own DDL comment names this as the extension point: *"`kind` is a DIMENSION, not a bucketed value... New magnitudes are therefore data rather than DDL."*

⚠️ **`levels.py` is not touched.** Mapping happens at the store boundary, so `events_for_turns`' output stays byte-identical and the `/analyze` oracle (`analyze_window_by_parse`) is unaffected. `test_magnitude.py:241` asserts that function's `mag` rows are a subset of `magnitude.KINDS`; leaving the emitter alone keeps that true.

**Files:**
- Modify: `sidecar/app/analysis/magnitude.py:110-113` (add `CAPTURE_KINDS`)
- Modify: `sidecar/app/analysis/store.py:620-660` (`upsert_events`), `:673-696` (`_aggregate_mag`)
- Modify: `sidecar/app/analysis/ingest.py` (`_ingest_from` — pass the flag)
- Test: `sidecar/app/test_store.py`

**Interfaces:**
- Consumes: `ingest.capture_mode()` from Task 2.
- Produces: `magnitude.CAPTURE_KINDS` — a 7-tuple of kind strings.
- Produces: `Store.upsert_events(session, rows, source_line=0, capture=False)` — one new keyword-only-in-practice argument, defaulting False so every existing caller is unchanged.
- Produces: capture rows readable via the **existing** `Store.turn_magnitudes(session, start, end, kind=...)`. No new read path.

- [ ] **Step 1: Write the failing test**

Add to `sidecar/app/test_store.py`:

```python
def test_capture_routes_say_and_tok_into_turn_magnitude():
    """`say` and `tok` rows are computed by `events_for_turns` and were discarded here. Under
    capture they become `turn_magnitude` rows -- data, not DDL, which is exactly what that
    table's `kind` dimension exists for.

    The kind is `"{row kind}_{row level}"`: a `say`/`user` row becomes `say_user`. Two rows of
    the same kind at the same instant SUM, which is the same arithmetic the cost magnitudes
    already use and what a turn with several think blocks needs.
    """
    rows = [
        (round(T0 + 5, 1), SESSION, None, None, False, "say", "user", "", 1873.0),
        (round(T0 + 5, 1), SESSION, None, None, False, "say", "asst_think", "", 400.0),
        (round(T0 + 5, 1), SESSION, None, None, False, "say", "asst_think", "", 600.0),
        (round(T0 + 5, 1), SESSION, None, None, False, "tok", "in_cached", "", 257333.0),
    ]
    with tempfile.TemporaryDirectory() as tmp:
        st = open_store(os.path.join(tmp, "off.db"))
        st.upsert_events(SESSION, rows, source_line=1)
        assert st.turn_magnitudes(SESSION, T0 - 1, T0 + 60, kind="say_user") == [], \
            "capture defaults OFF and must change nothing"
        st.close()

        st = open_store(os.path.join(tmp, "on.db"))
        st.upsert_events(SESSION, rows, source_line=1, capture=True)
        assert st.turn_magnitudes(SESSION, T0 - 1, T0 + 60, kind="say_user") == \
            [(round(T0 + 5, 1), 1873.0)]
        assert st.turn_magnitudes(SESSION, T0 - 1, T0 + 60, kind="say_asst_think") == \
            [(round(T0 + 5, 1), 1000.0)], "same kind at one instant must SUM"
        assert st.turn_magnitudes(SESSION, T0 - 1, T0 + 60, kind="tok_in_cached") == \
            [(round(T0 + 5, 1), 257333.0)]
        st.close()


def test_capture_rows_never_reach_the_event_table():
    """The store's standing invariant, restated against the new routing: a character count is a
    magnitude, never a reference event. `event` holds level/ref/count and no empty `ref`."""
    rows = [
        (round(T0 + 5, 1), SESSION, None, None, False, "say", "user", "", 1873.0),
        (round(T0 + 5, 1), SESSION, None, None, False, "tok", "out", "", 4211.0),
    ]
    with tempfile.TemporaryDirectory() as tmp:
        path = os.path.join(tmp, "state", "refseries.db")
        st = open_store(path)
        st.upsert_events(SESSION, rows, source_line=1, capture=True)
        st.close()
        raw = sqlite3.connect(path)
        assert not [r for r in raw.execute("SELECT 1 FROM event LIMIT 1")], \
            "capture rows are magnitudes, never events"
        raw.close()
```

Rename the existing `test_say_and_tok_rows_are_not_stored` and re-argue its docstring — its assertions are about the `event` table and stay exactly as they are, but its *name and claim* are now wrong:

```python
def test_say_and_tok_rows_are_not_reference_events():
    """`say` rows carry `len(body)` — a measure of message TEXT — and `tok` rows carry token
    counts. Neither is a reference event, and neither may reach `event`.

    ⚠️ THIS USED TO SAY THEY ARE NOT STORED AT ALL, and that is no longer true: under
    `KELD_CAPTURE=1` they are stored as `turn_magnitude` rows (see
    `test_capture_routes_say_and_tok_into_turn_magnitude`). The invariant that survived the
    change, and the one this test pins, is narrower and is the one that matters: the `event`
    table holds a level, a ref and a count, and nothing derived from message text. A character
    count is a number about text, not text — the same line `magnitude.edit_bytes` already sits
    on.
    """
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_store.py
```

Expected: FAIL with `TypeError: upsert_events() got an unexpected keyword argument 'capture'`.

- [ ] **Step 3: Write minimal implementation**

In `sidecar/app/analysis/magnitude.py`, after the existing `KINDS`:

```python
# The COST kinds. `Store.has_magnitudes` is scoped to exactly this tuple, because it answers
# "was anything costed here", and the capture kinds below are not costs.
KINDS = (TOKENS, REQUEST_TOKENS, EDIT_BYTES)

# The CAPTURE kinds: written only under `KELD_CAPTURE=1`, and never a cost. They ride
# `turn_magnitude` rather than a table of their own because that table's `kind` is a DIMENSION
# -- a new magnitude is data, not DDL (see the table's own comment in store.py).
#
# `say_*` are per-role CHARACTER COUNTS of message text and `tok_*` is the raw token split. Both
# are computed by `levels.events_for_turns` already and were discarded on the way in. The token
# split is NOT a second spelling of `TOKENS`: that one is price-weighted and answers what a turn
# COST, while `tok_in_cached / (tok_in_cached + tok_in_fresh)` answers how much of the context
# was reused, which no cost figure expresses.
#
# `tool_errors` / `tool_result_chars` come from `analysis/capture.py`, not from
# `events_for_turns`, because a `tool_result` line is filtered out before that function sees it.
SAY_USER = "say_user"
SAY_USER_ECHO = "say_user_echo"
SAY_ASST = "say_asst"
SAY_THINK = "say_asst_think"
TOK_OUT = "tok_out"
TOK_IN_FRESH = "tok_in_fresh"
TOK_IN_CACHED = "tok_in_cached"
TOOL_ERRORS = "tool_errors"
TOOL_RESULT_CHARS = "tool_result_chars"
CAPTURE_KINDS = (SAY_USER, SAY_USER_ECHO, SAY_ASST, SAY_THINK,
                 TOK_OUT, TOK_IN_FRESH, TOK_IN_CACHED,
                 TOOL_ERRORS, TOOL_RESULT_CHARS)
```

In `sidecar/app/analysis/store.py`, change `upsert_events` and `_aggregate_mag`:

```python
    def upsert_events(self, session, rows, source_line=0, capture=False):
```

and inside it:

```python
        agg = self._aggregate(rows, source_line)
        mags = self._aggregate_mag(rows, source_line, capture=capture)
```

```python
    @staticmethod
    def _aggregate_mag(rows, source_line, capture=False):
        """`(line, ts, kind) -> value` over the `mag` rows -- plus, under `capture`, the `say`
        and `tok` rows, whose kind is `"{row kind}_{row level}"`.

        Summing is the point, not an incidental: `levels.events_for_turns` emits ONE ROW PER EDIT
        EVENT, deliberately, so a turn with three edits arrives as three rows and its per-turn
        magnitude is their sum. The per-event granularity is preserved where it is needed — in
        the extraction output a study reads directly — and summed where it is used, which is a
        weighted rollup over turns. The same holds for `say_asst_think`: a turn with several
        think blocks emits one row each and the turn's thinking length is their total.

        ⚠️ THE MAPPING IS HERE AND NOT IN `levels.py`. `events_for_turns` is the ORACLE's
        producer -- `analyze_window_by_parse` is asserted equal to the store path over its rows --
        so changing what it emits changes the thing the store is checked against. Routing at this
        boundary leaves that function byte-identical, and leaves `test_magnitude.py`'s assertion
        that its `mag` kinds are a subset of `magnitude.KINDS` true.
        """
        agg = collections.defaultdict(float)
        for r in rows:
            if r[5] == "mag":
                kind = str(r[6])
            elif capture and r[5] in ("say", "tok"):
                kind = f"{r[5]}_{r[6]}"
            else:
                continue
            line = int(r[9]) if len(r) > 9 else int(source_line)
            agg[(line, float(r[0]), kind)] += float(r[8])
        # A ZERO magnitude is the ABSENCE of one, and storing it would be a difference without a
        # distinction that a caller can nonetheless see. `weighted_window_rows` joins on the
        # existence of a magnitude row, so a stored zero makes a ref appear in the rollup with a
        # total of 0.0 -- present, competing for the window, contributing nothing -- while an
        # absent one omits it. Two rollups that agree on every number and disagree on which keys
        # exist is precisely the kind of near-miss this store's equality contract exists to rule
        # out (measured: `<synthetic>` model turns and zero-usage lines produced exactly that).
        return {k: v for k, v in agg.items() if v}
```

Also update `upsert_events`' docstring — the paragraph beginning "What is dropped is `say`" is now wrong:

```
        `ref` rows become `event` rows; `mag` rows become `turn_magnitude` rows (`level` is the
        magnitude's `kind`, `ref` is empty, `n` is the number). Under `capture`, `say` and `tok`
        rows ALSO become `turn_magnitude` rows, under the derived kinds in
        `magnitude.CAPTURE_KINDS`; without it they are dropped as before, which is the default.
        Nothing routes them to `event`: a character count is a number about text, not a
        reference, and the `event` table's no-text invariant is unchanged.
```

In `sidecar/app/analysis/ingest.py`'s `_ingest_from`, thread the flag:

```python
        if rows:
            store.upsert_events(session, rows, source_line=batch_line,
                                capture=capture_mode() == "1")
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_store.py
cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_magnitude.py
cd sidecar && for f in app/test_*.py; do PYTHONPATH=. ~/.keld/sidecar-venv/bin/python "$f" >/dev/null 2>&1 || echo "FAIL $f"; done; echo done
```

Expected: PASS, and no `FAIL` lines. `test_analyze_store.py` holds the oracle equality assertion; it must stay green with capture off.

- [ ] **Step 5: Commit**

```bash
git add sidecar/app/analysis/magnitude.py sidecar/app/analysis/store.py sidecar/app/analysis/ingest.py sidecar/app/test_store.py
git commit -m "store: keep the say and tok rows, as capture magnitudes

Both were computed by events_for_turns and discarded at upsert_events.
turn_magnitude's kind is a documented extension point -- a new magnitude is
data, not DDL -- so they land there under magnitude.CAPTURE_KINDS, gated on
KELD_CAPTURE and off by default.

Mapped at the store boundary rather than in levels.py: events_for_turns
produces the rows analyze_window_by_parse is asserted equal over, so leaving
it byte-identical is what keeps the oracle an oracle.

tok is not a second spelling of the price-weighted TOKENS magnitude. That one
says what a turn cost; the split says how much of the context was reused,
which no cost figure expresses."
```

---

### Task 4: `capture.py` — one raw-line pass for tool outcomes and byte offsets

Tool outcomes and per-bin byte offsets both need to walk the raw lines, and neither can use `json.loads`. They share one pass, in a new module that can be deleted wholesale.

⚠️ **`tool_result` lines are invisible to everything upstream.** `transcript.turns_in` skips them by a substring check *before* `json.loads`, which is what keeps a parse seconds-long rather than minutes-long, and `tool_use_in` skips them too when they carry no `tool_use` block. So the outcome signal cannot come from `events_for_turns` and must come from the raw lines.

⚠️ **The literal is `"is_error":true` and this was measured, not assumed.** Over 29,884 lines of the five largest transcripts on a real machine: the strict literal matched **170** lines against a decoded ground truth of **170**, with **zero disagreements**. A loose `is_error` check matches **3,450** — a 20× over-count, because transcripts contain content that merely mentions the string, including escaped JSON (`is_error\":false`) and prose. Do not loosen it.

**Files:**
- Create: `sidecar/app/analysis/capture.py`
- Create: `sidecar/app/test_capture.py`

**Interfaces:**
- Consumes: nothing from earlier tasks. This is a pure function over raw lines.
- Produces: `capture.scan(lines, offsets) -> (outcomes, bin_offsets)` where
  - `outcomes` is `[(ts_iso: str, is_error: bool, nchars: int), ...]` in file order, one per `tool_result` line
  - `bin_offsets` is `{bin_ts: int}` — the smallest byte offset seen for each 5-minute bin
- Produces: `capture.ERROR_LITERAL` — the exact substring, exported so a test can assert on it rather than duplicating it.

- [ ] **Step 1: Write the failing test**

Create `sidecar/app/test_capture.py`:

```python
"""The raw-line capture pass: tool outcomes and per-bin byte offsets.

  cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_capture.py

The property these tests exist for: this pass must see what `json.loads` would see, WITHOUT
calling it. Everything else here is bookkeeping.
"""
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from app.analysis import capture
from app.analysis.store import BIN_SECONDS

TS_A = "2026-08-26T10:00:00.000Z"
TS_B = "2026-08-26T10:07:30.000Z"


def _line(ts, body):
    return '{"type":"user","timestamp":"%s",%s}\n' % (ts, body)


def _lines_with_offsets(lines):
    """Byte offsets as `ingest._read_complete_lines` produces them."""
    offs, at = [], 0
    for L in lines:
        offs.append(at)
        at += len(L.encode("utf-8"))
    return lines, offs


def test_error_flag_matches_only_the_exact_literal():
    """⚠️ Measured on 29,884 real lines: the strict literal scored 170/170 against a decoded
    ground truth with zero disagreements, while a loose `is_error` match scored 3,450 -- a 20x
    over-count from transcript CONTENT that merely mentions the string. Both decoys below are
    taken from that corpus."""
    lines = [
        _line(TS_A, '"message":{"content":[{"type":"tool_result","is_error":true,"x":1}]}'),
        _line(TS_A, '"message":{"content":[{"type":"tool_result","is_error":false}]}'),
        _line(TS_A, '"message":{"content":[{"type":"tool_result","content":"is_error\\":false"}]}'),
        _line(TS_A, '"message":{"content":[{"type":"tool_result","content":"is_error: 327"}]}'),
    ]
    outcomes, _ = capture.scan(*_lines_with_offsets(lines))
    assert [o[1] for o in outcomes] == [True, False, False, False], outcomes


def test_non_tool_result_lines_yield_no_outcome():
    lines = [_line(TS_A, '"message":{"content":"just a prompt"}')]
    outcomes, _ = capture.scan(*_lines_with_offsets(lines))
    assert outcomes == []


def test_outcome_carries_the_lines_own_timestamp_and_size():
    body = '"message":{"content":[{"type":"tool_result","is_error":true,"content":"%s"}]}' % ("x" * 500)
    lines = [_line(TS_B, body)]
    outcomes, _ = capture.scan(*_lines_with_offsets(lines))
    assert len(outcomes) == 1
    ts, err, nchars = outcomes[0]
    assert ts == TS_B and err is True
    assert nchars == len(lines[0]), "size is the whole line, in characters"


def test_bin_offsets_take_the_FIRST_offset_in_each_bin():
    """A bin's offset is where to SEEK to read it, so it is the smallest offset of any line in
    the bin. Later lines in the same bin must not move it."""
    lines = [_line(TS_A, '"a":1'), _line(TS_A, '"b":2'), _line(TS_B, '"c":3')]
    lines, offs = _lines_with_offsets(lines)
    _, bins = capture.scan(lines, offs)
    a_bin = int(capture.epoch(TS_A) // BIN_SECONDS) * BIN_SECONDS
    b_bin = int(capture.epoch(TS_B) // BIN_SECONDS) * BIN_SECONDS
    assert a_bin != b_bin, "fixture must straddle a bin boundary"
    assert bins[a_bin] == offs[0], "the FIRST line of the bin, not the last"
    assert bins[b_bin] == offs[2]


def test_a_line_with_no_timestamp_is_skipped_entirely():
    """`turns_in` skips these too. A line we cannot place in time can neither carry an outcome
    nor anchor a bin."""
    lines = ['{"type":"user","message":{"content":[{"type":"tool_result","is_error":true}]}}\n']
    outcomes, bins = capture.scan(*_lines_with_offsets(lines))
    assert outcomes == [] and bins == {}


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn(); print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_capture.py
```

Expected: FAIL with `ModuleNotFoundError: No module named 'app.analysis.capture'`.

- [ ] **Step 3: Write minimal implementation**

Create `sidecar/app/analysis/capture.py`:

```python
"""The CAPTURE pass: what a raw transcript line says that nothing else reads.

Two signals, one walk over the lines `ingest._read_complete_lines` already holds, and no
`json.loads` anywhere in it.

## WHY THIS IS NOT IN `levels.py` OR `transcript.py`

`transcript.turns_in` SKIPS a `tool_result` line by a substring check performed BEFORE any JSON
decoding, and that skip is load-bearing: a tool result carries no speech and no reference, and it
is where the huge lines are, so skipping it unparsed is what keeps a parse seconds-long rather
than minutes-long. `tool_use_in` skips it too whenever it echoes no `tool_use` block. So the
outcome of a tool call -- whether it FAILED -- is invisible to every existing reader, and
recovering it by parsing those lines would undo the one decision that makes the parse affordable.

This module recovers it without decoding: a substring test for the error flag, `len(line)` for
the size, and a bounded regex for the instant. It is its own module so it can be deleted whole.

## ⚠️ THE ERROR LITERAL IS MEASURED, AND LOOSENING IT COSTS 20x

Over 29,884 lines of the five largest transcripts on a real machine:

    '"is_error":true'   170 matches   against a decoded ground truth of 170, 0 disagreements
    'is_error'        3,450 matches

The difference is transcript CONTENT that merely mentions the string -- escaped JSON inside a
tool result (`is_error\\":false`), prose about the flag, a script that greps for it. The leading
quote is what excludes the escaped form, and the whole literal is what excludes the prose. A
looser check does not degrade gracefully; it reports a 20x error rate.

Cost, measured on an 86 MB / 9,016-line transcript: +29.6 ms for the flag test and the size over
every line, against ~790 ms for a whole-file parse -- and a whole-file parse only happens on a
reparse. On an incremental tail it is sub-millisecond.
"""
import re
from datetime import datetime

from app.analysis.store import BIN_SECONDS

# See the module docstring. Do not loosen this, and do not rebuild it from parts at a call site.
ERROR_LITERAL = '"is_error":true'
_TOOL_RESULT = '"tool_result"'
# Bounded on both sides: an ISO instant is 20-32 characters and nothing else in a transcript is
# keyed `timestamp`. `search` scans until it matches, which on a large line is a memchr-speed
# walk rather than a parse.
_TS = re.compile(r'"timestamp":"([^"]{20,32})"')


def epoch(iso):
    """An ISO instant as a wall-clock epoch. A local `_epoch`, as `store.py`, `tick.py` and
    `dynamics.py` each keep -- the conversion is three lines and a shared one would couple four
    modules to buy nothing."""
    return datetime.fromisoformat(iso.replace("Z", "+00:00")).timestamp()


def scan(lines, offsets):
    """`(outcomes, bin_offsets)` for one batch of raw lines.

    `lines` are decoded strings and `offsets` their BYTE offsets in the file, positionally
    aligned -- see `ingest._read_complete_lines`, which produces both and is the only correct
    source of the second (a byte offset recomputed by re-encoding a decoded string is wrong on
    malformed input, which that function admits by decoding with `errors="replace"`).

    `outcomes` is `[(ts_iso, is_error, nchars), ...]` in file order, one entry per `tool_result`
    line. `nchars` is the whole line's length in CHARACTERS, matching the unit `say` rows already
    use for message text; it is a size, and no part of the line is retained.

    `bin_offsets` is `{bin_ts: byte offset}` holding the SMALLEST offset seen in each 5-minute
    bin. Smallest because the offset's only purpose is to be seeked to: a block is bin-aligned by
    construction, so reading it means starting at the first line of its first bin. A later line
    in the same bin must never move it, and a replayed batch must not either -- hence `min`, both
    here and in the upsert.

    A line with no parseable timestamp is skipped entirely, exactly as `turns_in` skips it: a
    line that cannot be placed in time can neither carry an outcome nor anchor a bin.
    """
    outcomes, bins = [], {}
    for line, off in zip(lines, offsets):
        m = _TS.search(line)
        if not m:
            continue
        ts_iso = m.group(1)
        try:
            bin_ts = int(epoch(ts_iso) // BIN_SECONDS) * BIN_SECONDS
        except ValueError:
            continue
        prev = bins.get(bin_ts)
        if prev is None or off < prev:
            bins[bin_ts] = off
        if _TOOL_RESULT in line:
            outcomes.append((ts_iso, ERROR_LITERAL in line, len(line)))
    return outcomes, bins
```

- [ ] **Step 4: Run test to verify it passes**

```bash
cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_capture.py
```

Expected: `6 passed`.

- [ ] **Step 5: Verify the measured cost claim on a real transcript**

⚠️ This is the spec's gate #1 and it must be run, not assumed. Write `/tmp/capture_cost.py`:

```python
import glob, os, sys, time
sys.path.insert(0, "sidecar")
from app.analysis import capture

f = sorted(glob.glob(os.path.expanduser("~/.claude/projects/*/*.jsonl")),
           key=os.path.getsize, reverse=True)[0]
with open(f, "rb") as fh:
    buf = fh.read()
raw = buf.splitlines(True)
offs, at = [], 0
for b in raw:
    offs.append(at); at += len(b)
lines = [b.decode("utf-8", errors="replace") for b in raw]
best = min(min(time.perf_counter() - t for t in [time.perf_counter()])
           for _ in [0])  # warm
t = time.perf_counter(); capture.scan(lines, offs); dt = time.perf_counter() - t
print(f"{os.path.basename(f)[:12]} {os.path.getsize(f)/1e6:.0f} MB {len(lines)} lines")
print(f"capture.scan: {dt*1000:.1f} ms")
```

```bash
cd /home/dg/keld/keld-signal/.claude/worktrees/signal-embeddings && ~/.keld/sidecar-venv/bin/python /tmp/capture_cost.py
```

Gate: **under 250 ms** on the largest transcript. A whole-file parse is ~790 ms and only happens on reparse; a tail is a few lines. If it exceeds the gate, the regex is the suspect — record the number in the commit message either way.

- [ ] **Step 6: Commit**

```bash
git add sidecar/app/analysis/capture.py sidecar/app/test_capture.py
git commit -m "capture: read tool outcomes and bin offsets without decoding a line

A tool_result line is skipped unparsed by turns_in -- a substring check before
json.loads, and it is what keeps a parse seconds-long rather than minutes-long
-- so whether a tool call FAILED is invisible to every existing reader. This
recovers it by substring, size and a bounded timestamp regex, never a parse.

The error literal is measured, not chosen. Over 29,884 real lines,
'\"is_error\":true' scored 170 against a decoded ground truth of 170 with zero
disagreements; a loose 'is_error' scored 3,450, a 20x over-count from
transcript content that merely mentions the string. Loosening it does not
degrade gracefully.

Same pass yields the per-bin byte offset, taking the SMALLEST offset in each
bin because its only purpose is to be seeked to."
```

---

### Task 5: Byte offsets from the reader, the `bin_offset` table, and the ingest wiring

**Files:**
- Modify: `sidecar/app/analysis/store.py:229+` (DDL), plus two new accessors
- Modify: `sidecar/app/analysis/ingest.py:246-262` (`_read_complete_lines`), `_ingest_from`
- Test: `sidecar/app/test_ingest.py`, `sidecar/app/test_store.py`

**Interfaces:**
- Consumes: `capture.scan(lines, offsets)` from Task 4; `ingest.capture_mode()` from Task 2; `magnitude.TOOL_ERRORS` / `TOOL_RESULT_CHARS` from Task 3.
- Produces: `ingest._read_complete_lines(path, offset, size) -> (lines, offsets, end_offset)` — **a third return value**; the sole caller is `_ingest_from`.
- Produces: `Store.upsert_bin_offsets(session, pairs)` where `pairs` is `{bin_ts: offset}`; `Store.bin_offset(session, bin_ts) -> int | None`.

- [ ] **Step 1: Write the failing test**

Add to `sidecar/app/test_ingest.py`:

```python
def test_read_complete_lines_offsets_are_byte_exact_on_invalid_utf8():
    """⚠️ Offsets MUST come from the raw bytes, never from re-encoding the decoded strings.

    `_read_complete_lines` decodes with `errors="replace"`, which turns one invalid byte into
    U+FFFD -- three bytes when re-encoded. So a cumulative sum over `len(line.encode())` drifts
    by two bytes per invalid byte, silently, and every offset after the first bad line points
    into the middle of a record. This fixture has one invalid byte precisely to catch that.
    """
    import tempfile
    from app.analysis.ingest import _read_complete_lines
    with tempfile.TemporaryDirectory() as tmp:
        p = os.path.join(tmp, "t.jsonl")
        raw = [b'{"a":1}\n', b'{"b":"\xff"}\n', b'{"c":3}\n']
        with open(p, "wb") as fh:
            fh.write(b"".join(raw))
        lines, offsets, end = _read_complete_lines(p, 0, os.path.getsize(p))
        assert len(lines) == 3 and len(offsets) == 3
        want, at = [], 0
        for b in raw:
            want.append(at); at += len(b)
        assert offsets == want, f"{offsets} != {want}"
        assert end == at
        naive = []
        at = 0
        for L in lines:
            naive.append(at); at += len(L.encode("utf-8"))
        assert naive != want, "fixture must actually exercise the drift, or it proves nothing"


def test_ingest_writes_bin_offsets_and_tool_outcomes_only_under_capture():
    """The two capture signals that come from raw lines rather than from `events_for_turns`."""
    import tempfile
    from app.analysis import magnitude
    from app.analysis.ingest import ingest_file, session_of
    from app.analysis.store import BIN_SECONDS, open_store
    prev = os.environ.get("KELD_CAPTURE")
    try:
        with tempfile.TemporaryDirectory() as tmp:
            p = os.path.join(tmp, "t.jsonl")
            with open(p, "w") as fh:
                fh.write('{"type":"user","timestamp":"2026-08-26T10:00:00.000Z","cwd":"/w",'
                         '"message":{"role":"user","content":"hello"}}\n')
                fh.write('{"type":"user","timestamp":"2026-08-26T10:01:00.000Z","cwd":"/w",'
                         '"message":{"role":"user","content":[{"type":"tool_result",'
                         '"is_error":true,"content":"boom"}]}}\n')
            sess = session_of(p)

            os.environ["KELD_CAPTURE"] = "0"
            st = open_store(os.path.join(tmp, "off.db"))
            ingest_file(st, p)
            assert st.turn_magnitudes(sess, 0, 4e9, kind=magnitude.TOOL_ERRORS) == [], \
                "capture off must write no outcomes"
            assert st.all_bin_offsets(sess) == {}, "capture off must write no offsets"
            st.close()

            os.environ["KELD_CAPTURE"] = "1"
            st = open_store(os.path.join(tmp, "on.db"))
            ingest_file(st, p)
            errs = st.turn_magnitudes(sess, 0, 4e9, kind=magnitude.TOOL_ERRORS)
            assert [v for _ts, v in errs] == [1.0], errs
            chars = st.turn_magnitudes(sess, 0, 4e9, kind=magnitude.TOOL_RESULT_CHARS)
            assert chars and chars[0][1] > 0
            rows = st.all_bin_offsets(sess)
            assert rows, "capture on must record at least one bin offset"
            assert min(rows.values()) == 0, f"the file's first bin starts at byte 0: {rows}"
            assert all(v % 1 == 0 and v >= 0 for v in rows.values()), rows
            st.close()
    finally:
        if prev is None:
            os.environ.pop("KELD_CAPTURE", None)
        else:
            os.environ["KELD_CAPTURE"] = prev
```

Add to `sidecar/app/test_store.py`:

```python
def test_bin_offset_keeps_the_smallest_and_survives_replay():
    """The offset's only purpose is to be seeked to, so it is the FIRST line of the bin. A
    replayed batch -- which incremental ingest is designed to tolerate -- must not raise it."""
    with tempfile.TemporaryDirectory() as tmp:
        st = open_store(os.path.join(tmp, "s.db"))
        st.upsert_bin_offsets(SESSION, {1000: 500, 1300: 900})
        st.upsert_bin_offsets(SESSION, {1000: 700})
        assert st.bin_offset(SESSION, 1000) == 500, "a later offset must not win"
        st.upsert_bin_offsets(SESSION, {1000: 120})
        assert st.bin_offset(SESSION, 1000) == 120, "an earlier offset must win"
        assert st.bin_offset(SESSION, 9999) is None
        assert st.all_bin_offsets(SESSION) == {1000: 120, 1300: 900}
        st.close()
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_store.py
cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_ingest.py
```

Expected: `AttributeError: 'Store' object has no attribute 'upsert_bin_offsets'`, and a `ValueError` from unpacking two values into three in the `_read_complete_lines` test.

- [ ] **Step 3: Write minimal implementation**

Add to the DDL block in `sidecar/app/analysis/store.py`, after the `prompt` table:

```sql
-- WHERE A BIN'S FIRST LINE IS, in bytes. Written only under `KELD_CAPTURE=1`.
--
-- It exists so a block's own messages can be re-read without re-reading the transcript.
-- ⚠️ `transcript.turns_between` is O(FILE) -- it does `sorted(iter_turns(path))`, a whole-file
-- parse measured at 0.79s on a 90 MB transcript -- so a consumer that opened a block through it
-- would put back exactly the cost this store exists to remove, once per block. A block is
-- bin-aligned by construction (`analyze._block_span` floors and ceils), so a block span maps to
-- a byte range and reading it is one seek and a bounded scan. Go's `resolve.scanFrom` already
-- does the equivalent on its side.
--
-- The value is the SMALLEST offset seen in the bin, enforced on write with MIN() rather than
-- left to insertion order: incremental ingest is designed to replay a tail after a crash, and a
-- replay must not be able to raise the offset past the lines it is meant to reach.
--
-- ~12 rows per active hour. No text: a bin timestamp and a byte position.
CREATE TABLE IF NOT EXISTS bin_offset (
  session  TEXT    NOT NULL,
  bin_ts   INTEGER NOT NULL,
  "offset" INTEGER NOT NULL,
  PRIMARY KEY (session, bin_ts)
) WITHOUT ROWID;
```

Add the accessors to `Store` (place them beside `turn_magnitudes`):

```python
    def upsert_bin_offsets(self, session, pairs):
        """Record where each 5-minute bin's first line starts. `pairs` is `{bin_ts: offset}`.

        MIN() on conflict, not overwrite: see the table's comment. A replayed batch re-presents
        offsets it has already stored, and the smallest is the answer in every case.
        """
        if not pairs:
            return 0
        self._conn().executemany("""
            INSERT INTO bin_offset(session, bin_ts, "offset") VALUES (?,?,?)
            ON CONFLICT(session, bin_ts)
            DO UPDATE SET "offset" = MIN("offset", excluded."offset")
            """, [(session, int(b), int(o)) for b, o in pairs.items()])
        return len(pairs)

    def bin_offset(self, session, bin_ts):
        """The byte offset of `bin_ts`'s first line, or None if the bin was never recorded.

        None is a real answer and means "not recorded" -- a store ingested with capture off holds
        none of these, and that is not the same as a bin starting at byte 0.
        """
        row = self._conn().execute(
            'SELECT "offset" FROM bin_offset WHERE session = ? AND bin_ts = ?',
            (session, int(bin_ts))).fetchone()
        return None if row is None else int(row[0])

    def all_bin_offsets(self, session):
        """Every recorded bin offset for one session, as `{bin_ts: offset}`."""
        return {int(b): int(o) for b, o in self._conn().execute(
            'SELECT bin_ts, "offset" FROM bin_offset WHERE session = ?', (session,))}
```

⚠️ **Add `bin_offset` to `clear_session`.** It is at `store.py:767`, and its delete list is currently four statements ending at line 788. Add a fifth:

```python
            c.execute("DELETE FROM prompt WHERE session = ?", (session,))
            # The offsets go too, and for the reason the prompt index does: a rotated file at
            # the same path is a DIFFERENT conversation, and a stale offset would seek into a
            # file the events no longer describe -- silently, since a byte position is valid
            # for any file long enough to contain it.
            c.execute("DELETE FROM bin_offset WHERE session = ?", (session,))
```

and extend its docstring's opening line to name the table:

```python
        """Drop every event, magnitude, bin, prompt-index and bin-offset row for one session.
```

Change `_read_complete_lines` in `sidecar/app/analysis/ingest.py`:

```python
def _read_complete_lines(path, offset, size):
    """The complete lines in `[offset, size)`, their BYTE offsets, and the offset just past the
    last one.

    A trailing fragment with no newline is NOT consumed. The watcher can signal mid-write, and
    advancing the checkpoint over a half-written record would drop it permanently — the same
    rule `AGENTS.md` states for text generally, applied to the one delimiter JSONL has.

    ⚠️ THE OFFSETS ARE MEASURED ON THE RAW BYTES, BEFORE DECODING, AND THAT IS NOT COSMETIC.
    This function decodes with `errors="replace"`, which turns one invalid byte into U+FFFD --
    three bytes when re-encoded. A caller computing offsets by accumulating
    `len(line.encode())` over the decoded strings therefore drifts by two bytes per invalid
    byte, silently, and every offset after the first bad line points into the middle of a
    record. Splitting the bytes first and decoding each segment costs nothing and cannot drift.
    """
    if size <= offset:
        return [], [], offset
    with open(path, "rb") as fh:
        fh.seek(offset)
        buf = fh.read(size - offset)
    cut = buf.rfind(b"\n")
    if cut < 0:
        return [], [], offset
    raw = buf[:cut + 1].splitlines(True)
    offsets, at = [], offset
    for b in raw:
        offsets.append(at)
        at += len(b)
    return [b.decode("utf-8", errors="replace") for b in raw], offsets, offset + cut + 1
```

In `_ingest_from`, take the third value and run the pass:

```python
    lines, offsets, end_offset = _read_complete_lines(path, offset, size)
```

and, immediately before the `with store.transaction():` block:

```python
    # The CAPTURE pass: two signals that cannot come from `events_for_turns`. A `tool_result`
    # line is filtered out before that function sees it (see `capture.py`), and a byte offset is
    # not a property of a turn at all. One walk, no `json.loads`.
    capture_on = capture_mode() == "1"
    outcomes, bin_offsets = capture.scan(lines, offsets) if capture_on else ([], {})
    outcome_rows = []
    for ts_iso, is_error, nchars in outcomes:
        t = quantize(_capture_epoch(ts_iso))
        base = (t, session, None, None, False)
        if is_error:
            outcome_rows.append(base + ("mag", magnitude.TOOL_ERRORS, "", 1.0))
        outcome_rows.append(base + ("mag", magnitude.TOOL_RESULT_CHARS, "", float(nchars)))
```

and inside the transaction, after the existing `upsert_events` call:

```python
        if outcome_rows:
            store.upsert_events(session, outcome_rows, source_line=batch_line, capture=True)
        if bin_offsets:
            store.upsert_bin_offsets(session, bin_offsets)
```

Add the imports at the top of `ingest.py`:

```python
from app.analysis import capture, magnitude
from app.analysis.capture import epoch as _capture_epoch
```

and ensure `quantize` is imported (it is already used elsewhere in the analysis package; if `ingest.py` lacks it, add `from app.analysis.levels import quantize`).

⚠️ **A tool-error row is emitted only when there IS an error.** A zero would be dropped by `_aggregate_mag` anyway ("a ZERO magnitude is the ABSENCE of one"), so emitting one would be a no-op that reads as intent. `tool_result_chars` is emitted for every tool result, error or not, because a result's size is a real fact regardless of outcome.

- [ ] **Step 4: Run the full suite**

```bash
cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_store.py
cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_ingest.py
cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_capture.py
cd sidecar && for f in app/test_*.py; do PYTHONPATH=. ~/.keld/sidecar-venv/bin/python "$f" >/dev/null 2>&1 || echo "FAIL $f"; done; echo done
```

Expected: PASS, no `FAIL` lines. `test_ingest.py` holds the chunked-equivalence comparator (a file ingested in N chunks must equal one whole-file ingest) — it must stay green **with capture on**, which is the property that says the new rows are incremental-safe.

- [ ] **Step 5: Verify chunked equivalence explicitly under capture**

The existing comparator may not cover the new tables. Check whether `test_ingest.py`'s chunked-equivalence test compares `turn_magnitude` and `bin_offset`; if it compares a fixed list of tables, add both. This is the trap that `STATE_VERSION` 3→4 was written to repair — a comparator that did not look at `turn_magnitude` is exactly why the duplicate-request bug survived.

```bash
cd sidecar && grep -n "turn_magnitude\|bin_offset\|def _tables\|DISTINCT" app/test_ingest.py | head -20
```

If either table is absent from the comparison, add it, re-run, and fix any divergence before committing.

- [ ] **Step 6: Commit**

```bash
git add sidecar/app/analysis/store.py sidecar/app/analysis/ingest.py sidecar/app/test_store.py sidecar/app/test_ingest.py
git commit -m "ingest: bin_offset index and tool-outcome magnitudes, under capture

bin_offset records where each 5-minute bin's first line starts, so a block's
messages can be re-read with one seek and a bounded scan. Without it the only
route is transcript.turns_between, which is O(FILE) -- 0.79s on a 90MB
transcript -- and calling that once per block would put back exactly the cost
the reference-series store exists to remove.

_read_complete_lines now returns byte offsets measured on the RAW BYTES. It
decodes with errors=replace, so one invalid byte becomes three when
re-encoded; offsets accumulated over the decoded strings drift two bytes per
bad byte and every later offset lands mid-record. Pinned by a fixture that
contains one.

MIN() on conflict, so a replayed tail cannot raise an offset past the lines it
is meant to reach."
```

---

## Self-Review

**Spec coverage.** The spec's § "What is wired through at ingest" names four items: `say` (Task 3), `tok` (Task 3), tool outcome (Tasks 4–5), per-turn reconstruction (already satisfied — `event.source_line` plus the new `turn_magnitude` rows keyed on `(session, source_line, ts, kind)` reconstruct a turn without new code; a test in Task 3 exercises the round trip). `bin_offset` (Tasks 4–5). § "Toggles" names three; this plan implements `capture` only, which is correct — `features` and `publish` govern code that does not exist until steps 2 and 3. The spec's gate #1 (tool-outcome parse cost) is Task 4 Step 5.

**Not covered here, deliberately:** the `/features` endpoint, `features.py`, `textembed.py`, the Go emitter and the publish path all belong to steps 2–4 of the spec's delivery order.

**Type consistency.** `capture.scan(lines, offsets)` returns `(outcomes, bin_offsets)` and is called that way in Task 5. `Store.upsert_bin_offsets` takes a dict and `capture.scan` returns a dict. `magnitude.TOOL_ERRORS` / `TOOL_RESULT_CHARS` are defined in Task 3 and consumed in Task 5. `_read_complete_lines`' third return value is introduced and consumed in the same task. `capture.epoch` is used by the Task 4 test and by Task 5's ingest wiring under the alias `_capture_epoch`.

**Unverified assumption the implementer must check, not assume:** Task 5's ingest test asserts `min(bin_offsets.values()) == 0`, i.e. the first bin's offset is byte 0. That holds because the fixture is a fresh file ingested from offset 0. If `ingest_file` ends up taking a different path for a two-line file, read the actual dict and assert against the real first offset rather than weakening the test to `>= 0` — an offset assertion that cannot fail is worse than none, because it reads as coverage.

**One thing this plan does not prove, stated so it is not mistaken for proven:** that the capture rows are correct *in aggregate over a real corpus*. Task 5 Step 5 checks chunked-equivalence, which is the property that says incremental ingest equals a whole-file ingest — necessary, not sufficient. Whether `tok_in_cached` sums to something sane on a real transcript is a question for step 2, when there is a consumer to read it. Do not add an aggregate-plausibility test here; it would assert this plan's own arithmetic.

---

## Execution Handoff

**Plan complete and saved to `docs/superpowers/plans/2026-08-26-embeddings-capture.md`. Two execution options:**

**1. Subagent-Driven (recommended)** — a fresh subagent per task, review between tasks, fast iteration

**2. Inline Execution** — execute tasks in this session using executing-plans, batch execution with checkpoints

**Which approach?**
