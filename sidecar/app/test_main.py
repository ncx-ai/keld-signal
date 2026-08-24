"""Tests for the sidecar's load-protection guards (input clipping) and the
async endpoints that route inference through the governed InferenceRunner into
the worker (WorkerManager.call). These import app.main (light — fastapi/pydantic
only; the GLiNER2 model lives in a worker child, never at import) so they run
without torch. Runnable under pytest OR standalone: `python app/test_main.py`.
"""
import importlib
import json
import os
import tempfile
import time as _time


def _reload_main(max_chars: str | None):
    """Reload app.main with KELD_SIDECAR_MAX_CHARS set, so module-level _MAX_CHARS
    picks up the value."""
    if max_chars is None:
        os.environ.pop("KELD_SIDECAR_MAX_CHARS", None)
    else:
        os.environ["KELD_SIDECAR_MAX_CHARS"] = max_chars
    import app.main as m

    return importlib.reload(m)


def test_clip_truncates_above_cap():
    m = _reload_main("100")
    assert len(m._clip("x" * 500)) == 100


def test_clip_leaves_short_text():
    m = _reload_main("100")
    assert m._clip("hello") == "hello"


def test_clip_disabled_when_nonpositive():
    m = _reload_main("0")
    assert m._clip("x" * 5000) == "x" * 5000


def test_default_char_clip_does_not_preempt_the_token_cap():
    # The char clip bounds TOKENIZER cost, not memory; max_len (in tokens) is the
    # memory bound. So the default must not bind before the token cap does.
    # Regression: at 8000 chars (~1100 word tokens) the clip silently became the
    # real constraint and the daemon's adaptive token cap had no effect above it.
    m = _reload_main(None)
    words = 2000  # comfortably more word tokens than any cap the daemon sends
    prose = "refactor the authentication middleware so tokens are validated " * (words // 8)
    assert m._clip(prose) == prose, (
        f"char clip cut a {len(prose)}-char / ~{words}-word prompt at "
        f"{m._MAX_CHARS} chars, pre-empting the token cap")


import asyncio as _asyncio

from app.cpuscale import CpuScaler
from app.governor import Governor
from app.runner import InferenceRunner, QueueFull
from app.metrics import Counts
from app.worker import handle
from fastapi import HTTPException


class _FakeModel:
    def classify_text(self, text, tasks, include_confidence=False, max_len=None):
        # Mirror gliner2: with include_confidence it returns {"label","confidence"} dicts
        # carrying the real score; without it, a bare label string (→ adapter fabricates 1.0).
        if include_confidence:
            return {t: {"label": opts[0], "confidence": 0.73} for t, opts in tasks.items()}
        return {t: opts[0] for t, opts in tasks.items()}  # top label = first option

    def extract_entities(self, text, labels, max_len=None):
        return {"entities": {}}

    def create_schema(self):
        return self

    def entities(self, labels):
        return self

    def classification(self, task, options):
        return self

    def extract(self, text, schema, include_confidence=False, max_len=None):
        return {"entities": {}}


class _FakeWM:
    """Stands in for WorkerManager: call() runs worker.handle against a fake
    model in-thread (the runner still executes it single-flight), so endpoints
    see the same already-normalized payload the real worker returns."""
    def __init__(self, model=None, state="ready"):
        self._model = model or _FakeModel()
        self.state = state
        self.model_cost_mb = None
        self.counts = {"recycles": 0, "kills_timeout": 0, "kills_pressure": 0,
                       "kills_idle": 0, "kills_hard": 0, "crashes": 0}
        # The rest of the surface build_metrics() reads. Previously omitted, which meant no test
        # could call m.metrics() at all (see test_analyze_returns_a_payload_without_touching_the
        # _runner's note); the retention task has to assert on the REAL endpoint's store block,
        # so the fake now covers what /metrics reads rather than routing around it.
        self.peak_rss_mb = None

    def call(self, req):
        return handle(req, self._model)

    def ready(self):
        return self.state == "ready"

    def worker_rss_mb(self):
        return 0.0

    def ceiling_mb(self):
        return None

    def hard_limit_mb(self):
        return None

    def parent_reserve_mb(self):
        return None

    def budget_shortfall_mb(self):
        return None


def _wire(main, queue_max=8, wm=None, runner=None):
    gov = Governor(disabled=True)
    scaler = CpuScaler()
    runner = runner or InferenceRunner(gov, queue_max=queue_max)
    main._state.clear()
    main._state["governor"] = gov
    main._state["scaler"] = scaler
    main._state["runner"] = runner
    main._state["wm"] = wm or _FakeWM()
    return runner


def test_classify_endpoint_routes_through_worker():
    m = _reload_main(None)
    runner = _wire(m)

    async def run():
        runner.start()
        try:
            out = await m.classify(m.ClassifyIn(text="hello", tasks={"task_type": ["a", "b"]}))
            assert out["results"]["task_type"][0]["label"] == "a"
            # real GLiNER2 score must survive — not the fabricated 1.0 from a bare-string label
            assert out["results"]["task_type"][0]["confidence"] == 0.73
        finally:
            await runner.stop()
    _asyncio.run(run())


def test_extract_endpoint_queue_full_returns_503():
    m = _reload_main(None)
    runner = _wire(m, queue_max=1)

    async def run():
        runner.start()
        try:
            import threading
            release = threading.Event()

            def block(_):
                release.wait(2.0)
                return {"entities": {}}

            # Occupy consumer + fill the single queue slot.
            t1 = _asyncio.create_task(runner.submit(block, 0))
            await _asyncio.sleep(0.05)
            t2 = _asyncio.create_task(runner.submit(block, 1))
            await _asyncio.sleep(0.05)
            status = None
            try:
                await m.extract(m.ExtractIn(text="hi", labels={}, tasks={}))
            except HTTPException as e:
                status = e.status_code
            assert status == 503
            release.set()
            await _asyncio.gather(t1, t2)
        finally:
            release.set()
            await runner.stop()
    _asyncio.run(run())


def test_health_state_down_when_no_worker():
    m = _reload_main(None)
    m._state.clear()
    h = m.health()
    assert h["ok"] is False and h["state"] == "down"


def test_health_ok_when_worker_down_but_service_up():
    # DOWN is a serve-on-demand state: /health must report ok=True so the
    # daemon's supervisor + readiness gate open and the first request triggers a
    # lazy worker spawn. (Regression: a lazy DOWN reporting ok=False deadlocks the
    # supervisor — it never sees ready, exhausts restarts, and no sidecar runs.)
    m = _reload_main(None)
    _wire(m, wm=_FakeWM(state="down"))
    h = m.health()
    assert h["ok"] is True and h["state"] == "down"


def test_health_not_ok_when_held():
    m = _reload_main(None)
    _wire(m, wm=_FakeWM(state="held"))
    assert m.health()["ok"] is False


def test_dispatch_503_when_worker_held():
    # Memory pressure holds the worker; endpoints shed with 503 rather than block.
    m = _reload_main(None)
    _wire(m, wm=_FakeWM(state="held"))
    body = m.ClassifyIn(text="hi", tasks={"task_type": ["codegen", "other"]})
    try:
        _asyncio.run(m.classify(body))
        assert False, "expected 503"
    except HTTPException as e:
        assert e.status_code == 503


def test_classify_sheds_503_and_counts_when_queue_full():
    from app.metrics import Counts

    class _FullRunner:
        async def submit(self, *a, **k):
            raise QueueFull()

    m = _reload_main(None)
    _wire(m, runner=_FullRunner())
    m._state["counts"] = Counts()

    body = m.ClassifyIn(text="hi", tasks={"task_type": ["codegen", "other"]})
    try:
        _asyncio.run(m.classify(body))
        assert False, "expected 503"
    except HTTPException as e:
        assert e.status_code == 503
    assert m._state["counts"].shed_503 == 1


# --- POST /vocabulary, POST /match --------------------------------------------------------
#
# These bypass _dispatch/the single-flight runner entirely (see the block comment above
# app.main._match_budget_s). Every test below either proves that independence directly, or
# exercises the total-raw-regex-time budget decision made at wiring time (match.py itself
# already has 19 tests for the matcher's own behaviour; these are endpoint-level).

def test_vocabulary_endpoint_installs_and_returns_rejects():
    m = _reload_main(None)
    m._state.clear()
    body = m.VocabularyIn(vocabulary={
        "customer": [{"id": "acme", "match": ["ACME"]},
                     {"id": "broken", "regex": "("}],
    })
    out = m.install_vocabulary(body)
    assert out == {"rejects": [{"key": "customer", "id": "broken", "reason": "bad_regex"}]}
    assert "acme" in [e["id"] for e in m._state["vocabulary"]["customer"]]


def test_vocabulary_endpoint_counts_raw_labels_for_budget_division():
    m = _reload_main(None)
    m._state.clear()
    body = m.VocabularyIn(vocabulary={
        "customer": [{"id": "acme", "match": ["ACME"]},       # literal: not counted
                     {"id": "north", "regex": "Northwind"}],   # raw: counted
        "team": [{"id": "eng", "regex": "Engineering"}],       # raw: counted
    })
    m.install_vocabulary(body)
    assert m._state["vocab_raw_count"] == 2


def test_match_endpoint_present_and_absent():
    m = _reload_main(None)
    m._state.clear()
    m.install_vocabulary(m.VocabularyIn(vocabulary={
        "customer": [{"id": "acme", "match": ["ACME"]}]}))

    async def run():
        hit = await m.match(m.MatchIn(text="the customer is ACME today"))
        assert hit["customer"]["value"] == "acme"
        miss = await m.match(m.MatchIn(text="no company named here"))
        assert "customer" not in miss
    _asyncio.run(run())


def test_match_endpoint_with_no_vocabulary_installed_returns_empty():
    m = _reload_main(None)
    m._state.clear()  # no /vocabulary call at all — not even the key present
    got = _asyncio.run(m.match(m.MatchIn(text="ACME is the customer")))
    assert got == {}


def test_match_endpoint_wire_shape_carries_no_span_offset_or_text():
    m = _reload_main(None)
    m._state.clear()
    m.install_vocabulary(m.VocabularyIn(vocabulary={
        "customer": [{"id": "acme", "match": ["ACME"]}]}))
    secret = "Customer is ACME, per the confidential RFP."
    got = _asyncio.run(m.match(m.MatchIn(text=secret)))
    entry = got["customer"]
    assert set(entry.keys()) == {"value", "confidence", "count", "alternates"}
    blob = repr(got)
    assert "span" not in blob and "offset" not in blob
    assert secret not in blob and "RFP" not in blob


def test_match_endpoint_answers_while_runner_busy_and_with_no_worker_wired():
    """The property that distinguishes /match from every other endpoint: it must not go
    through _dispatch -> runner.submit at all. Proven two ways in one test — the
    single-flight runner is fully occupied (queue_max=1, one blocking job inflight) AND
    there is no `wm` in _state at all — yet /match still answers well inside a short
    timeout, because it never awaits either of them."""
    m = _reload_main(None)
    runner = _wire(m, queue_max=1)
    del m._state["wm"]  # /match must have no dependency on a worker being present at all
    m.install_vocabulary(m.VocabularyIn(vocabulary={
        "customer": [{"id": "acme", "match": ["ACME"]}]}))

    async def run():
        runner.start()
        try:
            import threading
            release = threading.Event()

            def block(_):
                release.wait(2.0)
                return {"entities": {}}

            # Occupy the runner's single in-flight slot so anything routed through it
            # would have to wait behind this call.
            t1 = _asyncio.create_task(runner.submit(block, 0))
            await _asyncio.sleep(0.05)

            got = await _asyncio.wait_for(
                m.match(m.MatchIn(text="the customer is ACME")), timeout=1.0)
            assert got["customer"]["value"] == "acme"

            release.set()
            await t1
        finally:
            release.set()
            await runner.stop()
    _asyncio.run(run())


def test_match_budget_uses_module_default_with_no_raw_labels():
    m = _reload_main(None)
    m._state.clear()
    m._state["vocab_raw_count"] = 0
    from app.analysis.match import DEFAULT_BUDGET_S
    assert m._match_budget_s() == DEFAULT_BUDGET_S


def test_match_budget_divides_total_cap_across_raw_labels():
    m = _reload_main(None)
    m._state.clear()
    m._state["vocab_raw_count"] = 5
    assert abs(m._match_budget_s() - m._MATCH_TOTAL_BUDGET_S / 5) < 1e-9


def test_match_budget_floor_stops_collapse_on_a_large_vocabulary():
    """Without a floor, a vocabulary with enough raw-regex labels would drive the
    per-label share toward 0 and starve even an ordinary pattern of any real time to
    run. The floor accepts the total cap no longer holding exactly in that regime,
    the same trade-off match.py's own static nested-quantifier filter already makes."""
    m = _reload_main(None)
    m._state.clear()
    m._state["vocab_raw_count"] = 10_000
    assert m._match_budget_s() == m._MATCH_BUDGET_FLOOR_S


def test_match_endpoint_passes_the_divided_budget_into_match_text():
    """End-to-end proof of the wiring, not just the pure math above: install a
    vocabulary with several raw-regex labels and confirm /match actually invokes
    match_text with the DIVIDED per-label budget — not match.py's own
    DEFAULT_BUDGET_S — so N raw-regex labels are bounded to N x (total/N) ==
    total, rather than N x DEFAULT_BUDGET_S (the blow-up a per-label ceiling
    alone would allow). Captures the real argument match_text was called with,
    rather than inferring it from timing, so it can't be timing-flaky."""
    m = _reload_main(None)
    m._state.clear()
    n = 8
    vocab_raw = {f"key{i}": [{"id": f"slow{i}", "regex": "a"}] for i in range(n)}
    m.install_vocabulary(m.VocabularyIn(vocabulary=vocab_raw))
    assert m._state["vocab_raw_count"] == n

    seen = {}

    def spy(text, compiled, budget_s):
        seen["budget_s"] = budget_s
        return {}

    real_match_text = m.match_text
    m.match_text = spy
    try:
        _asyncio.run(m.match(m.MatchIn(text="anything")))
    finally:
        m.match_text = real_match_text

    expected = max(m._MATCH_BUDGET_FLOOR_S, m._MATCH_TOTAL_BUDGET_S / n)
    assert seen["budget_s"] == expected
    # Sanity: strictly less than the undivided per-label default, i.e. this really
    # is a smaller-than-default budget, not accidentally the old constant.
    from app.analysis.match import DEFAULT_BUDGET_S
    assert expected < DEFAULT_BUDGET_S


def test_vocabulary_survives_a_real_worker_recycle():
    """The design spec's stated reason the vocabulary lives in the FastAPI PARENT,
    not on the WorkerManager or in the spawned child, is that a worker recycle
    (kill the child, respawn fresh) must never silently empty it — a recycle only
    reclaims the CHILD process's heap; app.main._state is the parent's own dict
    and is never touched by WorkerManager.kill/spawn/poll (grep confirms neither
    worker_manager.py nor runner.py references `_state` at all). That is true by
    construction today; this test exercises it rather than assuming it holds
    forever.

    Drives an ACTUAL recycle through WorkerManager.poll() — the same
    ceiling-exceeded path app/test_worker_manager.py's
    test_poll_rss_ceiling_recycles_when_idle exercises (fake spawn/rss/ram, so no
    real process or model is needed, but the SAME WorkerManager class and the
    SAME kill/spawn/poll code) — then confirms /match still answers correctly
    from the SAME vocabulary object afterwards.

    What this would catch: moving vocabulary storage onto the WorkerManager
    instance or into the spawned child (so a fresh worker generation no longer
    has it), or wiring the recycle/kill path to clear or replace
    app.main._state instead of only tearing down the worker process. Confirmed
    by mutation: temporarily making WorkerManager._kill() pop
    app.main._state["vocabulary"] on a "recycles" kill turns this test from
    PASS to FAIL (and back to PASS once reverted) — see the M2 verification
    notes, not re-run here since it requires editing worker_manager.py itself.
    It would NOT catch a bug where /match reads the right dict but the wrong
    key; that is covered separately by test_match_endpoint_present_and_absent.
    """
    from app.worker_manager import WorkerManager

    m = _reload_main(None)
    m._state.clear()
    m.install_vocabulary(m.VocabularyIn(vocabulary={
        "customer": [{"id": "acme", "match": ["ACME"]}]}))
    vocab_ref = m._state["vocabulary"]  # identity checked below, not just value

    class _FakeProc:
        def __init__(self):
            self.pid = 4242
            self._alive = True

        def is_alive(self):
            return self._alive

        def kill(self):
            self._alive = False

        def join(self, timeout=None):
            pass

    class _FakeQueue:
        def __init__(self, items=None):
            self.items = list(items or [])

        def put(self, x):
            pass

        def get(self, timeout=None):
            if not self.items:
                import queue
                raise queue.Empty()
            return self.items.pop(0)

    def spawn_fn():
        return _FakeProc(), _FakeQueue(), _FakeQueue([{"ready": True}])

    # margin_mb=1000 -> ceiling = model_cost_mb(2700, from rss_fn below) + 1000 = 3700
    wm = WorkerManager(spawn_fn=spawn_fn, rss_fn=lambda pid: 2700.0,
                        ram_fn=lambda: (50.0, 9000.0), clock=lambda: 100.0,
                        job_deadline_s=5.0, live_poll_s=1.0, spawn_timeout_s=5.0,
                        idle_timeout_s=600.0, evict_pct=5.0, margin_mb=1000.0)
    wm._spawn()
    assert wm.state == "ready" and wm.counts["recycles"] == 0

    wm._hard_limit_mb = 9000.0        # isolate: exercise the drift-ceiling recycle
    wm._rss_fn = lambda pid: 4000.0   # path, not the separate hard-limit kill path
    wm.poll()
    assert wm.state == "down" and wm.counts["recycles"] == 1  # a REAL recycle happened

    # The property under test: the parent's vocabulary is the SAME object, untouched.
    assert m._state["vocabulary"] is vocab_ref
    got = _asyncio.run(m.match(m.MatchIn(text="the customer is ACME")))
    assert got["customer"]["value"] == "acme"


# --- POST /analyze -------------------------------------------------------------------------
#
# Deliberately bypasses _dispatch/the single-flight runner, for the same reason /match does (see
# the block comment above app.main._match_budget_s): a transcript read plus regex/spaCy work is
# not an inference. This file has no fastapi TestClient anywhere (every endpoint above is called
# directly, async ones via _asyncio.run, with state set up through _wire) — that convention is
# followed here too rather than introducing a `_client()` helper the file doesn't otherwise use.

def _fixture_prompt_id():
    return "fixture-prompt-0001"


def _fixture_transcript(said="look at the thing"):
    """A small, wholly-invented transcript in its own directory. This is a privacy-critical
    repo, so no real names, paths, or repos — see sidecar/app/analysis/testdata/ for the
    convention this mirrors. `said` is the EARLIER turn's text because the window is
    [start, end) — the target prompt is the window's exclusive upper bound, never inside it.

    Also points KELD_ANALYZE_ROOTS at the directory it just created: /analyze is confined to the
    configured transcript roots (see the block above test_analyze_accepts_a_path_inside_a_
    configured_root), so a fixture under /tmp is otherwise a 403. Tests asserting the rejection
    paths override the variable AFTER calling this.

    And KELD_HOME, for a harder reason: /analyze now serves from the reference-series store,
    whose default location is ~/.keld/state/refseries.db. Left unset, every test in this file
    would ingest its fixture into the developer's or the CI runner's REAL store. Set here rather
    than in each test because it must be in force before the first _store() call, and _store()
    resolves lazily on the first request — the same reason KELD_ANALYZE_ROOTS is set here.

    Uses mkdtemp (not a `with TemporaryDirectory()` block): the file
    must outlive this helper call, not just a `with` body. Shape matches
    sidecar/app/test_analysis_analyze.py's fixture, already proven to round-trip through
    analyze_window."""
    tmp = tempfile.mkdtemp(prefix="keld-analyze-test-")
    path = os.path.join(tmp, "fixture001-0000.jsonl")
    rows = [
        {"type": "user", "timestamp": "2026-08-01T10:00:00Z", "cwd": "/workspace/widget-app",
         "message": {"content": [{"type": "text", "text": said}]}},
        {"type": "user", "timestamp": "2026-08-01T10:05:00Z", "cwd": "/workspace/widget-app",
         "uuid": _fixture_prompt_id(),
         "message": {"content": [{"type": "text", "text": "now fix the bug"}]}},
    ]
    with open(path, "w") as fh:
        for o in rows:
            fh.write(json.dumps(o, separators=(",", ":")) + "\n")
    os.environ["KELD_ANALYZE_ROOTS"] = tmp
    os.environ["KELD_HOME"] = os.path.join(tmp, "keld-home")
    return path


def test_analyze_returns_a_payload_without_touching_the_runner():
    """/analyze is a transcript read plus regex/spaCy work, not inference. It must answer with no
    worker ever spawned — that is what distinguishes it from /classify. Checked directly against
    the counts object (as test_classify_sheds_503_and_counts_when_queue_full does below) rather
    than through a live m.metrics() call: _FakeWM here only stands in for the pieces the other
    endpoints touch (state/call/ready/worker_rss_mb) and is missing several attributes the real
    WorkerManager always has (peak_rss_mb, ceiling_mb(), hard_limit_mb()) that only /metrics'
    own builder reads — reproducing the whole real WorkerManager surface in the fake just to take
    this one reading would be scope creep this task doesn't need."""
    from app.metrics import Counts

    m = _reload_main(None)
    _wire(m)
    m._state["counts"] = Counts()

    body = _asyncio.run(m.analyze(
        m.AnalyzeIn(path=_fixture_transcript(), prompt_id=_fixture_prompt_id())))
    assert body["schema"] >= 1
    assert "workstreams" in body and "inventory" in body
    assert m._state["counts"].submitted == 0, "must not have gone through the runner"
    assert m._state["counts"].analyze_served == 1


def test_analyze_reports_how_the_window_is_changing_not_only_what_it_holds():
    """The DYNAMICS block on the endpoint. /analyze answers what a window contains; on its own
    that is a state, and a state cannot say whether the hour just turned over or has looked like
    this all day. The block is the recent slice read against its own longer baseline
    (app/analysis/dynamics.py); its metrics are unit-tested in app/test_analysis_dynamics.py, so
    what is pinned HERE is that the endpoint actually carries it, named and self-describing.

    The `baseline_absent` assertion is the load-bearing one. This fixture is two turns five
    minutes apart, so a 15-minute slice holds the work and the 45-minute baseline holds nothing —
    and with no baseline evidence, EVERY slice value is "absent from the baseline", which an
    unguarded turnover would report as 1.0: total churn, on a session that never changed
    anything. Naming the status instead is the whole of what `window.attribution` was shipped
    for."""
    from app.analysis import workstreams

    m = _reload_main(None)
    _wire(m)
    body = _asyncio.run(m.analyze(
        m.AnalyzeIn(path=_fixture_transcript(), prompt_id=_fixture_prompt_id())))
    d = body["dynamics"]
    # The shipped sizer is the EWMA (Task 3's measured winner). On THIS fixture -- two turns five
    # minutes apart -- it has almost no stream to read and correctly does not fire, so the block
    # arrives on the fixed 15-minute fallback: the sizer NAME says which sizer answered and
    # `fallback` says which of its two paths did, which is the whole point of reporting both.
    assert d["sizer"] == "ewma", d
    assert d["sizer_detail"]["fallback"] is True, d["sizer_detail"]
    assert (d["slice_minutes"], d["baseline_minutes"]) == (15.0, 45.0), d
    assert d["slice_end"] == body["window_end"], d
    assert d["baseline_start"] == body["window_start"], (
        "the baseline reached outside the window /analyze already validated")
    assert d["source"] == "bin+event" and d["reconcile_scope"] == "file", d
    assert set(d["dimensions"]) == {n for n, _lv, _f in workstreams.ALLOCATION}, d["dimensions"]
    proj = d["dimensions"]["project"]
    assert proj["status"] == "baseline_absent", proj
    assert proj["turnover"] is None and proj["changed"] is None, proj


def test_analyze_unknown_prompt_is_404_not_an_empty_payload():
    m = _reload_main(None)
    _wire(m)
    try:
        _asyncio.run(m.analyze(m.AnalyzeIn(path=_fixture_transcript(), prompt_id="nope")))
        assert False, "expected 404"
    except HTTPException as e:
        assert e.status_code == 404


def test_a_window_the_store_cannot_answer_yet_is_503_not_404():
    """/analyze serves from the reference series, so "not ingested yet" is a real outcome and it
    must be TRANSIENT to the caller. 503 is the only status the Go client's post() waits and
    retries through; a 404 (a permanent "prompt not in this transcript") or a 500 would fail the
    workstreams facet and publish the profile as "partial" for a window that was one append away
    from answerable.

    The condition is produced by the real mechanism rather than a stub: the transcript's last
    line — the target prompt's own — is written WITHOUT its newline, exactly as a watcher signal
    arriving mid-write leaves it. Ingest will not consume a torn record (see
    ingest._read_complete_lines), so the store cannot answer, and it knows it cannot.
    """
    from app.metrics import Counts

    m = _reload_main(None)
    _wire(m)
    m._state["counts"] = Counts()
    path = _fixture_transcript()
    with open(path) as fh:
        text = fh.read()
    with open(path, "w") as fh:
        fh.write(text.rstrip("\n"))          # the prompt's line, mid-write
    try:
        _asyncio.run(m.analyze(m.AnalyzeIn(path=path, prompt_id=_fixture_prompt_id())))
        assert False, "expected 503"
    except HTTPException as e:
        assert e.status_code == 503, e.status_code
    assert m._state["counts"].analyze_not_ingested == 1
    assert m._state["counts"].submitted == 0, "the refusal went through the runner"


def test_a_window_whose_evidence_was_pruned_is_410_not_503():
    """Retention's refusal, on the wire. It must NOT be the 503 above: 503 is the one status the
    Go client waits and retries through, and a pruned window is never coming back — retrying it
    would spin forever. 410 Gone falls into the client's `default: return false, false` ("genuine
    error — do not spin forever"), so the workstreams facet publishes as `partial`, which is the
    honest outcome for a window whose evidence no longer exists.

    Produced by the real mechanism: the store is ingested, then a prune is recorded that covers
    the window's own start.
    """
    from app.metrics import Counts

    m = _reload_main(None)
    _wire(m)
    m._state["counts"] = Counts()
    path, pid = _fixture_transcript(), _fixture_prompt_id()
    # Ingest, then declare everything up to now pruned.
    _asyncio.run(m.ingest(m.IngestIn(path=path)))
    st = m._store()
    st.note_pruned("event", _time.time() + 1.0, 1)
    try:
        _asyncio.run(m.analyze(m.AnalyzeIn(path=path, prompt_id=pid)))
        assert False, "expected 410"
    except HTTPException as e:
        assert e.status_code == 410, e.status_code
    assert m._state["counts"].analyze_expired == 1
    assert m._state["counts"].analyze_not_ingested == 0, "an expiry was counted as a lag"
    assert m._state["counts"].submitted == 0, "the refusal went through the runner"


def test_metrics_reports_the_reference_series_store():
    """The store block on the real endpoint, not just the builder."""
    m = _reload_main(None)
    _wire(m)
    _asyncio.run(m.ingest(m.IngestIn(path=_fixture_transcript())))
    out = m.metrics()
    s = out["store"]
    assert s is not None and "error" not in s, s
    assert s["rows"]["event"] > 0 and s["rows"]["bin"] > 0
    assert s["max_mb"] > 0 and s["live_mb"] > 0
    assert s["oldest_event_ts"] is not None
    assert s["serving_floor_ts"] is None
    assert s["pruned"]["event"]["rows"] == 0


def test_metrics_answers_even_when_the_store_cannot_be_opened():
    """/health and /metrics must survive everything. An unopenable store already reports 503 on
    /analyze; it must not take the observability endpoint down too."""
    m = _reload_main(None)
    _wire(m)
    path = _fixture_transcript()
    old = os.environ.get("KELD_HOME")
    os.environ["KELD_HOME"] = path            # a FILE where a directory must be
    try:
        assert m.metrics()["store"] is None
    finally:
        if old is None:
            os.environ.pop("KELD_HOME", None)
        else:
            os.environ["KELD_HOME"] = old


def test_an_unopenable_store_is_503_and_never_falls_back_to_a_parse():
    """There is one production path. A store that will not open is reported, not worked around:
    a silent switch to a second implementation of the same answer is how a divergence between
    them goes unnoticed."""
    m = _reload_main(None)
    _wire(m)
    path = _fixture_transcript()
    os.environ["KELD_HOME"] = path            # a FILE where a directory must be
    try:
        _asyncio.run(m.analyze(m.AnalyzeIn(path=path, prompt_id=_fixture_prompt_id())))
        assert False, "expected 503"
    except HTTPException as e:
        assert e.status_code == 503, e.status_code
    assert m._store() is None


# --- the service must serve without GLiNER2 -------------------------------------------------
#
# This is the client-side analysis and enrichment service, not a GLiNER2 wrapper: GLiNER2 was
# the first use case, not the precondition. /analyze needs no model at all, and the property
# below is what makes that structural rather than incidental.

def _no_spacy(fn):
    """Run fn with `import spacy` poisoned, and return (result, attempted).

    Every test here would otherwise pay a real spacy.load(): ~600 MB and ~2 s each. Poisoning
    the import exercises _analysis_nlp()'s established degrade-to-None path, which also proves
    the load was ATTEMPTED — and the regex half of terms.candidates() still supplies terms, so
    the level's output is still observable."""
    import builtins

    attempted = []
    real_import = builtins.__import__

    def guard(name, *a, **kw):
        if name == "spacy" or name.startswith("spacy."):
            attempted.append(name)
            raise ImportError("no spacy in this test")
        return real_import(name, *a, **kw)

    builtins.__import__ = guard
    try:
        return fn(), attempted
    finally:
        builtins.__import__ = real_import


def test_the_service_answers_analyze_with_gliner2_never_loaded():
    """The load-bearing property of the whole design. Driven through the REAL lifespan and the
    REAL WorkerManager — a _FakeWM could not fail this test, and the thing under test is
    precisely that nothing in the startup or the request path reaches for a model."""
    from app.worker_manager import DOWN, WorkerManager

    m = _reload_main(None)
    path = _fixture_transcript()

    async def run():
        async with m.lifespan(m.app):
            assert isinstance(m._state["wm"], WorkerManager), (
                "this must run against the real manager, or it proves nothing about spawning")
            assert m.health()["ok"] is True, "the service must serve before any model exists"
            assert m._state["wm"].state == DOWN
            body = await m.analyze(m.AnalyzeIn(path=path, prompt_id=_fixture_prompt_id()))
            assert m._state["wm"].state == DOWN, "/analyze spawned a GLiNER2 worker"
            assert m._state["counts"].submitted == 0, "/analyze went through the runner"
            return body

    body, _ = _no_spacy(lambda: _asyncio.run(run()))
    assert "workstreams" in body and "inventory" in body


def test_named_terms_are_on_by_default():
    m = _reload_main(None)
    _wire(m)
    assert m._terms_status() == m._TERMS_OK

    def call():
        path = _fixture_transcript(said="check the UnityPredict rollout for ACME")
        return _asyncio.run(m.analyze(m.AnalyzeIn(path=path, prompt_id=_fixture_prompt_id())))

    body, attempted = _no_spacy(call)
    assert attempted == ["spacy"], "the term level must reach for the model by default"
    assert [t["value"] for t in body["inventory"]["named_terms"]] == ["ACME", "UnityPredict"]
    # spaCy was unavailable here, and that is DEGRADED, not skipped: the regex shapes are a
    # genuine partial measurement, so they are reported with the reason beside them.
    assert body["named_terms_status"] == "degraded:spacy_unavailable", body


def test_keld_terms_0_switches_the_level_off_and_says_so():
    """Off means the dimension is not reported, not merely that spaCy is skipped: the regex half
    needs no model and still runs, so returning its output would contradict the status beside it
    and make the switch read as a performance knob."""
    os.environ["KELD_TERMS"] = "0"
    try:
        m = _reload_main(None)
        _wire(m)
        assert m._terms_status() == m._TERMS_DISABLED

        def call():
            path = _fixture_transcript(said="check the UnityPredict rollout for ACME")
            return _asyncio.run(m.analyze(m.AnalyzeIn(path=path, prompt_id=_fixture_prompt_id())))

        body, attempted = _no_spacy(call)
    finally:
        os.environ.pop("KELD_TERMS", None)
    assert attempted == [], "switched off must mean no load is attempted at all"
    assert body["inventory"]["named_terms"] == []
    assert body["named_terms_status"] == "skipped:disabled"


def test_spacy_length_guard_is_restored():
    """20_000_000 disabled spaCy's own per-document bound outright (880k chars measured at
    14.7 s / 3.4 GB transient). The replacement is a real bound, and over-length messages are
    SKIPPED by the NER pass rather than cut — the regex shapes still read them in full, so no
    message leaves the level and nothing is read half-way."""
    from app.analysis.terms import candidates

    m = _reload_main(None)
    assert 0 < m._TERMS_MAX_LEN <= 1_000_000, m._TERMS_MAX_LEN

    class _Boom:
        max_length = 10

        def __call__(self, text):
            raise AssertionError("spaCy must not be handed text above its own max_length")

    got = candidates("x" * 50 + " UnityPredict", _Boom())
    assert "UnityPredict" in got, got


def test_analyze_never_resolves_the_nlp_on_the_event_loop():
    """_analysis_nlp() used to be evaluated as an ARGUMENT to run_in_executor, so the whole
    multi-second, several-hundred-MB spaCy load ran on the event loop — blocking /health and
    /metrics, the exact thing this endpoint's executor hop exists to prevent (see its
    docstring). It must be resolved inside the executor instead."""
    import threading

    m = _reload_main(None)
    _wire(m)
    seen = []
    real = m._analysis_nlp

    def recording():
        seen.append(threading.get_ident())
        return None     # deliberately not real(): recording the thread is the whole assertion,
                        # and a genuine spacy.load() here would cost the test ~600 MB

    m._analysis_nlp = recording
    try:
        _asyncio.run(m.analyze(
            m.AnalyzeIn(path=_fixture_transcript(), prompt_id=_fixture_prompt_id())))
    finally:
        m._analysis_nlp = real
    assert seen, "the nlp was never resolved at all"
    assert seen[0] != threading.get_ident(), (
        "the spaCy load ran on the event-loop thread; it must run inside the executor")


def test_analyze_response_carries_no_prompt_text():
    m = _reload_main(None)
    _wire(m)
    body = _asyncio.run(m.analyze(
        m.AnalyzeIn(path=_fixture_transcript(), prompt_id=_fixture_prompt_id())))
    dumped = json.dumps(body)
    for k in ("text", "span", "offset"):
        assert f'"{k}":' not in dumped, k


# --- POST /ingest --------------------------------------------------------------------------
#
# The watcher's signal that a transcript advanced. It exists so the parse happens when the file
# grows rather than inside an /analyze request, where it lands on a job's per-pass deadline. Same
# properties as /analyze: coordinates in (a path, nothing else), the same root confinement, no
# inference, off the runner, and no prompt text in the response.


def test_ingest_leaves_the_store_able_to_answer_without_a_request_path_parse():
    """The whole point of the endpoint, asserted where it shows: after the signal, the window is
    answerable with refresh OFF — i.e. /analyze has nothing left to ingest. `refresh=False` is
    the only way to state "no parse happened in this call" without stubbing something."""
    from app.analysis.analyze import analyze_window
    from app.metrics import Counts

    m = _reload_main(None)
    _wire(m)
    m._state["counts"] = Counts()
    path = _fixture_transcript()

    body = _asyncio.run(m.ingest(m.IngestIn(path=path)))
    assert body["new_lines"] == 2, body
    assert m._state["counts"].ingest_served == 1
    assert m._state["counts"].submitted == 0, "the signal went through the inference runner"

    out = analyze_window(path, _fixture_prompt_id(), 60, m._analysis_nlp(),
                         store=m._store(), refresh=False)
    assert out["schema"] >= 1 and "workstreams" in out, out

    # A second signal for an unchanged file is a stat and a no-op: the watcher signals per poll,
    # so this is the common case, and it must not reparse.
    again = _asyncio.run(m.ingest(m.IngestIn(path=path)))
    assert again["new_lines"] == 0 and again["reparsed"] is False, again


def test_ingest_is_confined_to_the_configured_roots():
    """Same allowlist, same reasoning as /analyze (see the block above _default_analyze_roots):
    the sidecar has no auth, and this endpoint opens an arbitrary path AS THE DAEMON'S USER and
    writes what it derives into the store. Unconfined it is the same confused deputy /analyze
    would be, with a persistence side effect."""
    from app.metrics import Counts

    m = _reload_main(None)
    _wire(m)
    m._state["counts"] = Counts()
    path = _fixture_transcript()
    os.environ["KELD_ANALYZE_ROOTS"] = os.path.join(os.path.dirname(path), "elsewhere")
    try:
        _asyncio.run(m.ingest(m.IngestIn(path=path)))
        assert False, "expected 403"
    except HTTPException as e:
        assert e.status_code == 403, e.status_code
    assert m._state["counts"].ingest_rejected == 1


def test_ingest_of_a_vanished_transcript_is_404():
    """A signal for a file that has since been deleted is a fact about the file, not a transient
    condition: 404, so the caller does not treat it as "ask again"."""
    from app.metrics import Counts

    m = _reload_main(None)
    _wire(m)
    m._state["counts"] = Counts()
    path = _fixture_transcript()
    gone = os.path.join(os.path.dirname(path), "ffffffff-0000.jsonl")
    try:
        _asyncio.run(m.ingest(m.IngestIn(path=gone)))
        assert False, "expected 404"
    except HTTPException as e:
        assert e.status_code == 404, e.status_code
    assert m._state["counts"].ingest_missing == 1


def test_an_unopenable_store_makes_ingest_503():
    m = _reload_main(None)
    _wire(m)
    path = _fixture_transcript()
    os.environ["KELD_HOME"] = path            # a FILE where a directory must be
    try:
        _asyncio.run(m.ingest(m.IngestIn(path=path)))
        assert False, "expected 503"
    except HTTPException as e:
        assert e.status_code == 503, e.status_code


def test_ingest_response_carries_no_prompt_text():
    m = _reload_main(None)
    _wire(m)
    said = "the marker sentence that must not come back"
    dumped = json.dumps(_asyncio.run(m.ingest(m.IngestIn(path=_fixture_transcript(said=said)))))
    assert said not in dumped, dumped
    for k in ("text", "span", "offset", "path"):
        assert f'"{k}":' not in dumped, k


def test_ingest_never_resolves_the_nlp_on_the_event_loop():
    """Same defect /analyze had, and the same consequence: the spaCy load is seconds long and
    hundreds of MB, and on the event loop it blocks /health and /metrics. It must also be the
    SAME pipeline /analyze resolves — `term` rows are never re-derived, so an ingest under a
    different pipeline forces the reparse `terms_mode` exists to force."""
    import threading

    m = _reload_main(None)
    _wire(m)
    seen = []
    real = m._analysis_nlp

    def spy():
        seen.append(threading.get_ident())
        return real()

    m._analysis_nlp = spy
    try:
        _asyncio.run(m.ingest(m.IngestIn(path=_fixture_transcript())))
    finally:
        m._analysis_nlp = real
    assert seen, "the nlp was never resolved at all"
    assert seen[0] != threading.get_ident(), (
        "the nlp resolved on the event-loop thread; it must run inside the executor")


# --------------------------------------------------------------------- /pii
#
# POST /pii is the sensitivity facet's detector. Like /analyze and /match it must answer with
# GLiNER2 never loaded — that is the whole reason it exists as its own endpoint rather than a
# worker op: `sensitivity` has to work under ml_backend:"deterministic" and whenever the model is
# absent. Unlike /analyze (coordinates in), it takes TEXT, the same as /classify — loopback,
# on-device. What it must never do is hand any of that text BACK: the response carries offsets and
# types only, and the caller (which already holds the text) slices its own copy.
#
# WHOLLY SYNTHETIC, as everywhere in this repo: area 456 / group 72 / serial 8391 is a
# structurally valid SSA assignment that belongs to nobody. Same value test_pii.py uses.
_SYNTHETIC_SSN = "456-72-8391"
_SSN_PROMPT = f"Employee SSN {_SYNTHETIC_SSN} was in the payload."


def test_the_service_answers_pii_with_gliner2_never_loaded():
    """The load-bearing property, for /pii this time. Driven through the REAL lifespan and the
    REAL WorkerManager — a _FakeWM proves nothing about spawning — and with the REAL presidio
    scan, because a stub would also pass with the defect present.

    This one test pays a genuine spaCy + presidio load (~2 s, ~1 GB). That is the cost of
    asserting the real path; every other test here stubs the scan."""
    from app.worker_manager import DOWN, WorkerManager

    m = _reload_main(None)

    async def run():
        async with m.lifespan(m.app):
            assert isinstance(m._state["wm"], WorkerManager), (
                "this must run against the real manager, or it proves nothing about spawning")
            assert m.health()["ok"] is True, "the service must serve before any model exists"
            assert m._state["wm"].state == DOWN
            body = await m.detect_pii(m.PiiIn(text=_SSN_PROMPT))
            assert m._state["wm"].state == DOWN, "/pii spawned a GLiNER2 worker"
            assert m._state["counts"].submitted == 0, "/pii went through the inference runner"
            return body

    body = _asyncio.run(run())
    hits = [s for s in body["spans"] if s["type"] == "ssn"]
    assert hits, body
    assert _SSN_PROMPT[hits[0]["start"]:hits[0]["end"]] == _SYNTHETIC_SSN, hits


def test_pii_response_carries_offsets_and_types_but_never_the_matched_text():
    """The privacy line for this endpoint. Returning the matched substring would put raw PII into
    an HTTP body and within reach of any future log line, for no gain: the caller has the text and
    the offsets index it. (/entities does return raw spans — an older contract with its own
    reasoning, not a precedent to copy.)"""
    m = _reload_main(None)
    _wire(m)

    def fake_scan(text, regions=None):
        start = text.index(_SYNTHETIC_SSN)
        return [{"type": "ssn", "start": start, "end": start + len(_SYNTHETIC_SSN),
                 "score": 0.85}]

    m.pii_scan = fake_scan
    body = _asyncio.run(m.detect_pii(m.PiiIn(text=_SSN_PROMPT)))
    for span in body["spans"]:
        assert set(span) == {"type", "start", "end", "score"}, span
    dumped = json.dumps(body)
    assert _SYNTHETIC_SSN not in dumped, dumped
    for word in ("Employee", "payload"):
        assert word not in dumped, dumped


def test_pii_regions_ride_the_request_and_default_when_absent():
    """The region set is a per-REQUEST field, not a sidecar-startup one.

    That is the whole shaping decision: the daemon polls Atlas for org settings on a
    live interval, so an org changing `pii_regions` must take effect on the next prompt
    rather than on the next sidecar restart. An absent field means "caller has no
    opinion" and app.pii falls back to its own default (KELD_PII_REGIONS, else `us`) —
    which is distinct from an explicit empty list, meaning "universal tier only".
    """
    m = _reload_main(None)
    _wire(m)
    seen = []

    def recording(text, regions=None):
        seen.append(regions)
        return []

    m.pii_scan = recording
    _asyncio.run(m.detect_pii(m.PiiIn(text=_SSN_PROMPT, regions=["uk", "in"])))
    _asyncio.run(m.detect_pii(m.PiiIn(text=_SSN_PROMPT)))
    _asyncio.run(m.detect_pii(m.PiiIn(text=_SSN_PROMPT, regions=[])))
    assert seen == [["uk", "in"], None, []], seen


def test_pii_never_loads_a_spacy_model():
    """/pii used to borrow the shared en_core_web_sm so presidio would not load a second one.
    It now needs NO model: dropping SpacyRecognizer (measured ~1% precision on real prompts)
    left only pattern recognizers, and app.pii builds its own blank tokenizer. So the
    invariant flips — resolving _analysis_nlp() here would RE-CREATE the dependency, and the
    whole point is that with KELD_TERMS=0 nothing in the /pii path loads the ~50 MB model that
    sits permanently in this never-recycled parent and is subtracted from the inference
    worker's hard limit."""
    m = _reload_main(None)
    _wire(m)
    seen = []
    real = m._analysis_nlp
    m._analysis_nlp = lambda: seen.append(1)
    m.pii_scan = lambda text, regions=None: []
    try:
        _asyncio.run(m.detect_pii(m.PiiIn(text=_SSN_PROMPT)))
    finally:
        m._analysis_nlp = real
    assert not seen, "/pii resolved the shared spaCy pipeline; it must not need one at all"


def test_pii_scan_never_runs_on_the_event_loop():
    """The model is gone but the work is not: presidio's first-call import is seconds long and
    a large document is not free. It must stay on the executor, or /health and /metrics stall
    behind it."""
    import threading

    m = _reload_main(None)
    _wire(m)
    seen = []

    def recording(text, regions=None):
        seen.append(threading.get_ident())
        return []

    m.pii_scan = recording
    _asyncio.run(m.detect_pii(m.PiiIn(text=_SSN_PROMPT)))
    assert seen, "the scan never ran at all"
    assert seen[0] != threading.get_ident(), (
        "the pii scan ran on the event-loop thread; it must run inside the executor")


def test_pii_failure_never_surfaces_or_logs_prompt_text():
    """A presidio exception can carry the analysed string. Neither the response detail nor any log
    line may repeat it — an error path is exactly where raw prompt text escapes."""
    import logging

    m = _reload_main(None)
    _wire(m)

    def boom(text, regions=None):
        raise ValueError(f"presidio choked on {text!r}")

    m.pii_scan = boom
    records = []

    class _Capture(logging.Handler):
        def emit(self, record):
            records.append(record)

    root = logging.getLogger()
    handler = _Capture()
    root.addHandler(handler)
    previous = root.level
    root.setLevel(logging.DEBUG)
    raised = None
    try:
        try:
            _asyncio.run(m.detect_pii(m.PiiIn(text=_SSN_PROMPT)))
        except HTTPException as exc:
            raised = exc
    finally:
        root.removeHandler(handler)
        root.setLevel(previous)

    assert raised is not None, "a failed scan must not be reported as an empty clean result"
    assert raised.__suppress_context__, (
        "the original exception is still chained; any traceback render would reprint its message, "
        "and that message can quote the analysed text")
    assert _SYNTHETIC_SSN not in str(raised.detail), raised.detail
    assert "Employee" not in str(raised.detail), raised.detail
    assert records, "a failure that is never logged is invisible to an operator"
    for record in records:
        message = record.getMessage()
        assert _SYNTHETIC_SSN not in message, message
        assert "Employee" not in message, message


def test_pii_bounds_the_text_it_scans_and_says_so():
    """The char clip bounds spaCy's per-document cost the same way it bounds tokenizer cost for
    the inference endpoints. Dropping a tail must be VISIBLE, though: unscanned text is undetected
    PII, and the caller has to be able to report the facet as partial rather than clean."""
    m = _reload_main("64")
    _wire(m)
    seen = {}

    def fake_scan(text, regions=None):
        seen["len"] = len(text)
        return []

    m.pii_scan = fake_scan
    short = _asyncio.run(m.detect_pii(m.PiiIn(text="short prompt")))
    assert short["truncated"] is False, short
    body = _asyncio.run(m.detect_pii(m.PiiIn(text="x" * 500)))
    assert seen["len"] == 64, seen
    assert body["truncated"] is True, body


def test_pii_counters_are_separate_from_inference_load():
    m = _reload_main(None)
    _wire(m)
    m._state["counts"] = Counts()
    m.pii_scan = lambda text, regions=None: []
    _asyncio.run(m.detect_pii(m.PiiIn(text=_SSN_PROMPT)))
    counts = m._state["counts"]
    assert counts.pii_served == 1 and counts.submitted == 0, vars(counts)


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn()
        print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
