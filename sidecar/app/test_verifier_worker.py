"""Standalone tests for Task 6b: the verifier runs in its own recycled worker child, not the
FastAPI parent. Run:
  cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_verifier_worker.py

Fakes only — no real model, no real 3 GB spawn, no real llama_cpp anywhere in this process.
"""
import os
import queue
import sys

from app.worker import handle, serve


# --- Step 1a: the verify op --------------------------------------------------------------------

class _StubVerifierModel:
    """The shape app/verifier.py's Verifier exposes: verify(block_text, dims, project) ->
    (bool, float). No classify_text/extract_entities/create_schema/extract at all — the point
    of this fake is that it CANNOT serve a GLiNER2-shaped request."""

    def __init__(self, verdict=True, seconds=0.02):
        self._verdict, self._seconds = verdict, seconds
        self.calls = []

    def verify(self, block_text, dims, project):
        self.calls.append((block_text, dims, project))
        return self._verdict, self._seconds


def test_handle_verify_routes_to_the_model_and_returns_the_shape():
    model = _StubVerifierModel(verdict=True, seconds=0.017)
    out = handle({"op": "verify", "block_text": "did some payments work",
                  "dims": {"repo": "acme-billing"}, "project": {"title": "Payments"}}, model)
    assert out == {"verdict": True, "seconds": 0.017}, out
    assert model.calls == [("did some payments work", {"repo": "acme-billing"},
                            {"title": "Payments"})], model.calls


def test_handle_verify_coerces_verdict_and_seconds_types():
    """The wire shape is a bool and a float; a model handing back e.g. numpy scalars or an int
    must not leak that through to the response dict `attribute_block` consumes."""
    class WeirdTypesModel:
        def verify(self, block_text, dims, project):
            return 1, 3  # truthy int, int seconds

    out = handle({"op": "verify", "block_text": "x", "dims": {}, "project": {}}, WeirdTypesModel())
    assert out["verdict"] is True and isinstance(out["seconds"], float), out


def test_handle_verify_no_verdict_is_false_not_dropped():
    model = _StubVerifierModel(verdict=False, seconds=0.01)
    out = handle({"op": "verify", "block_text": "x", "dims": {}, "project": {}}, model)
    assert out == {"verdict": False, "seconds": 0.01}, out


def test_existing_ops_are_untouched_by_the_verify_dispatch():
    """Corrective surgery on a shared dispatch: every op that predates this task must still
    behave exactly as before."""
    class GlinerLikeModel:
        def classify_text(self, text, tasks, include_confidence=False, max_len=None):
            return {"task_type": "debug"}

        def extract_entities(self, text, labels, max_len=None):
            return {"entities": {"email": ["a@b.com"]}}

        def create_schema(self):
            return self

        def entities(self, labels):
            return self

        def classification(self, task, options):
            return self

        def extract(self, text, schema, include_confidence=False, max_len=None):
            return {"entities": {"email": ["a@b.com"]}, "sensitivity": "pii"}

    m = GlinerLikeModel()
    out = handle({"op": "classify", "text": "hi", "tasks": {"task_type": ["debug"]}}, m)
    assert out["results"]["task_type"][0]["label"] == "debug"
    out = handle({"op": "entities", "text": "mail a@b.com", "labels": {"email": "Email"}}, m)
    assert out["entities"][0]["label"] == "email"
    out = handle({"op": "extract", "text": "x", "labels": {"email": "Email"},
                  "tasks": {"sensitivity": ["none", "pii"]}}, m)
    assert "entities" in out
    try:
        handle({"op": "nope", "text": "x"}, m)
        assert False, "expected ValueError"
    except ValueError:
        pass


# --- Step 1b: model-agnostic warm-up ------------------------------------------------------------

class _Q:
    """A minimal fake multiprocessing.Queue: FIFO get()/put(), no blocking semantics needed
    since serve()'s loop only ever calls get() once per queued item here."""

    def __init__(self, items=None):
        self.items = list(items or [])
        self.put_items = []

    def get(self):
        if not self.items:
            raise queue.Empty()
        return self.items.pop(0)

    def put(self, x):
        self.put_items.append(x)


class _VerifyOnlyModel:
    """Exposes ONLY verify() — no classify_text, no warm(). The model-agnostic warm-up must not
    require classify_text to reach {"ready": True}: today's fallback (a bare try/except around
    a classify_text call) already tolerates the AttributeError, so this pins that it keeps
    tolerating it once a `warm()` seam exists alongside it."""

    def verify(self, block_text, dims, project):
        return True, 0.01


def test_serve_warms_up_a_model_with_no_classify_text_and_no_warm():
    req_q = _Q([None])   # ready immediately, then shut down
    resp_q = _Q()
    serve(req_q, resp_q, lambda: _VerifyOnlyModel())
    assert resp_q.put_items[0] == {"ready": True}, resp_q.put_items


class _WarmableModel:
    """Exposes an explicit warm(): must be called INSTEAD of the classify_text fallback."""

    def __init__(self):
        self.warmed = False
        self.classify_text_called = False

    def warm(self):
        self.warmed = True

    def classify_text(self, *a, **kw):    # must never be reached when warm() exists
        self.classify_text_called = True
        return {}

    def verify(self, block_text, dims, project):
        return True, 0.01


def test_serve_prefers_warm_over_classify_text_when_both_exist():
    model = _WarmableModel()
    req_q, resp_q = _Q([None]), _Q()
    serve(req_q, resp_q, lambda: model)
    assert model.warmed is True
    assert model.classify_text_called is False
    assert resp_q.put_items[0] == {"ready": True}


def test_serve_warm_up_failure_still_reaches_ready():
    """A warm() that raises must not crash the child before it ever signals ready — the same
    resilience the classify_text fallback already had."""
    class BoomOnWarm:
        def warm(self):
            raise RuntimeError("boom")

        def verify(self, block_text, dims, project):
            return True, 0.01

    req_q, resp_q = _Q([None]), _Q()
    serve(req_q, resp_q, lambda: BoomOnWarm())
    assert resp_q.put_items[0] == {"ready": True}


def test_serve_gliner2_path_is_untouched():
    """The pre-existing GLiNER2 warm-up (classify_text, bare try/except) must still fire for a
    model with no warm() of its own — GLiNER2's sunset is explicitly deferred; this task must
    not touch its behaviour."""
    class ClassicGlinerModel:
        def __init__(self):
            self.warmup_call = None

        def classify_text(self, text, tasks, include_confidence=False, max_len=None):
            self.warmup_call = (text, tasks)
            return {}

    model = ClassicGlinerModel()
    req_q, resp_q = _Q([None]), _Q()
    serve(req_q, resp_q, lambda: model)
    assert model.warmup_call == ("warm up the model", {"_warmup": ["a", "b"]})
    assert resp_q.put_items[0] == {"ready": True}


# --- Step 1c: the verifier's own manager, own spawn, own ceiling --------------------------------

def test_verifier_manager_has_its_own_spawn_fn_and_rss_ceiling_distinct_from_gliner2s():
    from app.worker_manager import WorkerManager, _default_spawn

    import app.main as m

    m._state.pop("verifier_wm", None)
    try:
        gliner_wm = WorkerManager()  # the shared manager's own construction, same defaults
        verifier_wm = m._verifier_manager()

        assert verifier_wm._spawn_fn is m._verifier_spawn
        assert verifier_wm._spawn_fn is not gliner_wm._spawn_fn
        assert verifier_wm._spawn_fn is not _default_spawn

        assert verifier_wm._margin == m._VERIFIER_RSS_MARGIN_MB
        assert verifier_wm._margin != gliner_wm._margin, (
            "the verifier's RSS margin must be its own value, not GLiNER2's "
            "KELD_SIDECAR_RSS_MARGIN_MB default")

        # Idempotent: the same manager instance is reused, not rebuilt, on a second call.
        assert m._verifier_manager() is verifier_wm
    finally:
        m._state.pop("verifier_wm", None)


def test_verifier_manager_is_not_built_at_module_import_or_lifespan_start():
    """Lazy on first genuine need, never eagerly. Importing app.main and starting its lifespan
    must not build (let alone spawn) the verifier's manager."""
    import app.main as m

    assert m._state.get("verifier_wm") is None


# --- Step 5: the regression guard — the parent never imports llama_cpp --------------------------

def test_importing_app_main_never_imports_llama_cpp():
    assert "llama_cpp" not in sys.modules, (
        "llama_cpp must not already be loaded before this test imports app.main")
    import app.main  # noqa: F401
    assert "llama_cpp" not in sys.modules, (
        "importing app.main must not import llama_cpp — it must load only inside the "
        "verifier's worker CHILD (app.worker's model_factory), never the parent")


def test_attribute_block_with_a_stubbed_verifier_never_imports_llama_cpp():
    """The full THIS-PROCESS path an /attribute call takes: attribute_block scoring a block
    against a stubbed encoder and a stubbed verifier. Nothing on that path may reach for
    llama_cpp — the real `verifier.Verifier()` is built only inside `_verifier_model_factory`,
    which runs only inside the spawned child (see `_verifier_spawn`), never here."""
    from app.analysis import attribution

    class StubEncoder:
        def encode(self, texts):
            return [[1.0, 0.0] for _ in texts]

    class StubVerifier:
        def verify(self, block_text, dims, project):
            return True, 0.01

    projects = [{"id": "proj_pay", "title": "Payments", "team": "Eng",
                "description": "Stripe billing.", "repos": [], "keywords": []}]
    attribution.set_projects(projects)
    out = attribution.attribute_block(["stripe webhook retries again"], {"repo": "acme-billing"},
                                      encoder=StubEncoder(), verifier_obj=StubVerifier())
    assert out["status"] == "attributed", out
    assert "llama_cpp" not in sys.modules, (
        "an attribution run with a stubbed verifier must not import llama_cpp")


# --- C3: the verifier's manager must be POLLED, not merely constructed -------------------------
#
# ⚠️ CONSTRUCTED IS NOT ENOUGH, AND THE TEST ABOVE ONLY EVER PROVED CONSTRUCTED.
# WorkerManager.poll() is the SOLE driver of the RSS ceiling, the recycle, the idle unload
# and the pressure eviction. A manager nobody polls has none of them: the llama.cpp child
# holds its quantized weights and its 4096-token KV cache for the entire life of the sidecar,
# unbounded and unmeasured, on a budget already carrying two other children. So these tests
# assert the CALL, through the real lifespan poll loop.

def test_the_lifespan_poll_loop_polls_the_verifier_manager():
    """Drive the REAL `lifespan` and assert the real poll loop reaches a lazily-built verifier
    manager. A fake manager here is the point: it records that poll() was CALLED."""
    import asyncio

    import app.main as m

    class _RecordingWM:
        def __init__(self):
            self.polls = 0
            self.state = "down"

        def poll(self):
            self.polls += 1

        def shutdown(self):
            pass

    os.environ["KELD_SIDECAR_MEM_POLL_S"] = "0.01"
    m._state.pop("verifier_wm", None)
    rec = _RecordingWM()

    async def run():
        async with m.lifespan(m.app):
            # Built LAZILY: it does not exist when the loop starts, which is exactly the case
            # a closure over the value at lifespan time would get wrong forever.
            await asyncio.sleep(0.05)
            m._state["verifier_wm"] = rec
            for _ in range(200):
                if rec.polls > 0:
                    return
                await asyncio.sleep(0.01)

    try:
        asyncio.run(run())
    finally:
        os.environ.pop("KELD_SIDECAR_MEM_POLL_S", None)
        m._state.pop("verifier_wm", None)

    assert rec.polls > 0, (
        "the verifier's WorkerManager was never polled — it therefore has no RSS ceiling, "
        "no recycling, no idle unload and no pressure eviction")


def test_the_verifier_reserve_subtracts_the_other_children_from_the_shared_budget():
    """Three children, one KELD_SIDECAR_MEM_BUDGET_MB. The verifier's manager must not read
    the budget as though only the FastAPI parent were spending it, or its hard limit is at its
    most generous exactly when the GLiNER2 worker and the encoder are both resident."""
    import app.main as m

    class _Peak:
        def __init__(self, peak):
            self.peak_rss_mb = peak

    class _Src:
        def __init__(self, peak):
            self.encoder = _Peak(peak)

    real_parent, real_wm, real_src = m._parent_rss_mb, m._state.get("wm"), m._TEXT_SOURCE
    try:
        m._parent_rss_mb = lambda: 600.0
        m._state["wm"] = _Peak(2400.0)
        m._TEXT_SOURCE = _Src(1700.0)
        assert m._verifier_reserve_rss() == 4700.0, m._verifier_reserve_rss()
    finally:
        m._parent_rss_mb = real_parent
        m._TEXT_SOURCE = real_src
        if real_wm is None:
            m._state.pop("wm", None)
        else:
            m._state["wm"] = real_wm


def test_metrics_reports_the_verifier_child_and_says_when_it_was_never_built():
    """An unobserved child and a child that never spawned looked identical in /metrics, which
    is how an unbounded llama.cpp process stayed invisible. `built: false` is the answer for
    the second, and it must never BUILD the manager to produce it."""
    import app.main as m

    m._state.pop("verifier_wm", None)
    assert m._verifier_stats() == {"built": False, "state": None}
    assert m._state.get("verifier_wm") is None, "/metrics must never build (let alone spawn) the verifier"

    from app.worker_manager import WorkerManager

    wm = WorkerManager(spawn_fn=lambda: (_ for _ in ()).throw(AssertionError("must not spawn")),
                       rss_fn=lambda pid: 0.0, parent_rss_fn=lambda: 10.0)
    m._state["verifier_wm"] = wm
    try:
        stats = m._verifier_stats()
        assert stats["built"] is True and stats["state"] == "down", stats
        for key in ("rss_mb", "peak_rss_mb", "recycles", "kills", "siblings_reserve_mb"):
            assert key in stats, (key, stats)
    finally:
        m._state.pop("verifier_wm", None)


if __name__ == "__main__":
    fns = [(n, f) for n, f in sorted(globals().items()) if n.startswith("test_")]
    for name, fn in fns:
        fn()
    print(f"test_verifier_worker: {len(fns)} passed")
