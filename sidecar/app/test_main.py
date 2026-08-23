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
                       "kills_idle": 0, "crashes": 0}

    def call(self, req):
        return handle(req, self._model)

    def ready(self):
        return self.state == "ready"

    def worker_rss_mb(self):
        return 0.0


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


def test_analyze_unknown_prompt_is_404_not_an_empty_payload():
    m = _reload_main(None)
    _wire(m)
    try:
        _asyncio.run(m.analyze(m.AnalyzeIn(path=_fixture_transcript(), prompt_id="nope")))
        assert False, "expected 404"
    except HTTPException as e:
        assert e.status_code == 404


# --- named-terms extraction is OFF by default (KELD_TERMS) --------------------------------
#
# The `term` level is the only one that reads message TEXT, and the only one that needs spaCy.
# spaCy is a MODEL, and it would live in the long-lived FastAPI parent — the process AGENTS.md
# guarantees "holds no model and its own RSS stays flat regardless of uptime", and whose ~150 MB
# footprint (KELD_SIDECAR_PARENT_RESERVE_MB) the worker's hard limit is derived from. Measured:
# spacy.load() takes the parent to 619 MB, permanently, because the parent is never recycled.

def test_named_terms_off_by_default_never_imports_spacy():
    """The guarantee is not "the load is cheap", it is "the load does not happen". Asserted by
    poisoning the import itself: _analysis_nlp() swallows every Exception by design (a missing
    model must degrade, not fail the request), so an assert raised inside the guard would be
    silently caught. The recorded list is what survives that."""
    import builtins

    os.environ.pop("KELD_TERMS", None)
    m = _reload_main(None)

    attempted = []
    real_import = builtins.__import__

    def guard(name, *a, **kw):
        if name == "spacy" or name.startswith("spacy."):
            attempted.append(name)
            raise ImportError("spaCy must not be imported when KELD_TERMS is off")
        return real_import(name, *a, **kw)

    builtins.__import__ = guard
    try:
        got = m._analysis_nlp()
    finally:
        builtins.__import__ = real_import

    assert attempted == [], f"spaCy was imported with KELD_TERMS off: {attempted}"
    assert got is None, "the term level must be absent, not a loaded model"


def test_named_terms_are_empty_in_the_payload_when_terms_are_off():
    """Off means the dimension is not reported, not merely that spaCy is skipped: the regex half
    of terms.candidates() needs no model and still runs inside analyze_window, so returning its
    output would make KELD_TERMS read as a performance knob rather than the switch deciding
    whether named terms exist in the payload at all."""
    os.environ.pop("KELD_TERMS", None)
    m = _reload_main(None)
    _wire(m)
    # Message text carrying two shapes the regex half matches with no model at all: CamelCase
    # and an ALL-CAPS acronym. Without them the assertion would pass vacuously.
    path = _fixture_transcript(said="check the UnityPredict rollout for ACME")
    body = _asyncio.run(m.analyze(m.AnalyzeIn(path=path, prompt_id=_fixture_prompt_id())))
    assert body["inventory"]["named_terms"] == [], body["inventory"]["named_terms"]


def test_terms_on_still_computes_the_level_and_reaches_for_spacy():
    """The switch must turn the level back ON, not merely be a permanent kill. Asserted without
    paying for a real spacy.load() (~600 MB, ~2 s): the import is poisoned, so _analysis_nlp()
    takes its established degrade-to-None path and the regex half of terms.candidates() supplies
    the terms — which proves both that the import was ATTEMPTED and that the `term` level is
    computed and reported when KELD_TERMS is on."""
    import builtins

    os.environ["KELD_TERMS"] = "1"
    try:
        m = _reload_main(None)
        _wire(m)
        attempted = []
        real_import = builtins.__import__

        def guard(name, *a, **kw):
            if name == "spacy" or name.startswith("spacy."):
                attempted.append(name)
                raise ImportError("no spacy in this test")
            return real_import(name, *a, **kw)

        builtins.__import__ = guard
        try:
            path = _fixture_transcript(said="check the UnityPredict rollout for ACME")
            body = _asyncio.run(m.analyze(m.AnalyzeIn(path=path, prompt_id=_fixture_prompt_id())))
        finally:
            builtins.__import__ = real_import
    finally:
        os.environ.pop("KELD_TERMS", None)
    assert attempted == ["spacy"], "KELD_TERMS=1 must still reach for the model"
    assert [t["value"] for t in body["inventory"]["named_terms"]] == ["ACME", "UnityPredict"]


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
        return real()

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


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn()
        print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
