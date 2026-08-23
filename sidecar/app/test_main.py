"""Tests for the sidecar's load-protection guards (input clipping) and the
async endpoints that route inference through the governed InferenceRunner into
the worker (WorkerManager.call). These import app.main (light — fastapi/pydantic
only; the GLiNER2 model lives in a worker child, never at import) so they run
without torch. Runnable under pytest OR standalone: `python app/test_main.py`.
"""
import importlib
import os


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


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn()
        print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
