"""Inference worker — a child process that owns the GLiNER2 model and runs one
op at a time. The parent (WorkerManager) sends request dicts on req_q and reads
response dicts on resp_q. Isolating inference here means recycling this process
reclaims its heap via process exit — the only cross-platform memory reset.

No heavy module-level imports: torch/gliner2 are imported lazily so the spawn
re-import stays cheap and the parent never pulls in torch."""
import gc

from app.adapter import normalize_classify, normalize_entities, normalize_extract


def _release_memory() -> None:
    """Return transient inference memory to the OS after each request so the
    worker's RSS does not stay inflated between jobs. A transformer forward pass
    allocates large activation buffers that are freed on return but not
    necessarily handed back to the OS by the allocator — so RSS ratchets up under
    a burst and only the RSS-ceiling recycle (which cannot fire while a job holds
    the manager lock) would reclaim it. gc.collect() drops any cycles first;
    malloc_trim(0) then releases freed glibc arenas back to the OS. Both are
    best-effort and must never raise: malloc_trim is Linux/glibc-only (absent on
    macOS), and a trim failure must not kill the worker."""
    try:
        gc.collect()
    except Exception:
        pass
    try:
        import ctypes
        ctypes.CDLL("libc.so.6").malloc_trim(0)
    except Exception:
        pass


def _max_len(req: dict):
    """The request's token cap, or None for no truncation.

    This is THE bound on a single inference's transient memory: gliner2's own
    max_len default is None (no truncation), so activation memory grows with
    sequence length until the host swaps. The daemon derives the value adaptively
    (internal/agent/enrich/lenstat) and sends it per request.

    0 is treated as unset, not as an empty window: it is Go's zero value, so a
    cap that was never configured must mean "no cap" rather than "truncate
    everything away"."""
    n = req.get("max_len")
    if not n or int(n) <= 0:
        return None
    return int(n)


def handle(req: dict, model) -> dict:
    """Run one request against the model and return the final endpoint-shaped,
    normalized dict. Pure w.r.t. the model object, so it is unit-testable with a
    stub."""
    op = req["op"]
    if op == "verify":
        # A different request shape for a different model (app/verifier.py's Verifier):
        # block_text/dims/project, not text/tasks/labels. Dispatched from the same handle()
        # so both models share worker.py's serve() loop and worker_manager.py's
        # spawn/recycle/RSS machinery unmodified — not because they share a request shape.
        # Checked before `text = req["text"]` below, which a verify request never sends.
        verdict, seconds = model.verify(req.get("block_text", ""), req.get("dims"), req.get("project"))
        return {"verdict": bool(verdict), "seconds": float(seconds)}
    text = req["text"]
    max_len = _max_len(req)
    if op == "classify":
        raw = model.classify_text(text, req["tasks"], include_confidence=True,
                                  max_len=max_len)
        return {"results": normalize_classify(raw)}
    if op == "entities":
        raw = model.extract_entities(text, req["labels"], max_len=max_len)
        return {"entities": normalize_entities(raw, text)}
    if op == "extract":
        schema = model.create_schema().entities(req["labels"])
        for task, options in req["tasks"].items():
            schema = schema.classification(task, options)
        raw = model.extract(text, schema, include_confidence=True, max_len=max_len)
        return normalize_extract(raw, text, list(req["tasks"].keys()))
    raise ValueError(f"unknown op: {op}")


def _apply_threads(n):
    if n:
        import torch
        torch.set_num_threads(int(n))


def serve(req_q, resp_q, model_factory) -> None:
    """Child entrypoint: load + warm the model, signal ready, then serve one
    request at a time until a None sentinel. Each response is
    {"ok": True, "result": {...}} or {"ok": False, "error": "..."}."""
    model = model_factory()
    # Model-agnostic warm-up: call the product's own warm() when it has one, so a worker child
    # hosting a model that isn't GLiNER2 (e.g. app/verifier.py's Verifier, which has no
    # classify_text) still reaches {"ready": True} via an actual warm-up rather than an
    # AttributeError swallowed by the fallback's bare except. GLiNER2's warm-up path (below) is
    # unchanged and untouched for models exposing no warm() of their own.
    warm = getattr(model, "warm", None)
    if callable(warm):
        try:
            warm()
        except Exception:
            pass
    else:
        try:
            model.classify_text("warm up the model", {"_warmup": ["a", "b"]})
        except Exception:
            pass
    resp_q.put({"ready": True})
    while True:
        req = req_q.get()
        if req is None:
            return
        try:
            _apply_threads(req.get("threads"))
            resp_q.put({"ok": True, "result": handle(req, model)})
        except Exception as e:  # never let one bad request kill the worker
            resp_q.put({"ok": False, "error": repr(e)})
        # Hand the request's transient activation memory back to the OS so RSS
        # stays flat between jobs (runs on success and failure alike).
        _release_memory()
