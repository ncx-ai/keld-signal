"""Inference worker — a child process that owns the GLiNER2 model and runs one
op at a time. The parent (WorkerManager) sends request dicts on req_q and reads
response dicts on resp_q. Isolating inference here means recycling this process
reclaims its heap via process exit — the only cross-platform memory reset.

No heavy module-level imports: torch/gliner2 are imported lazily so the spawn
re-import stays cheap and the parent never pulls in torch."""
from app.adapter import normalize_classify, normalize_entities, normalize_extract


def handle(req: dict, model) -> dict:
    """Run one request against the model and return the final endpoint-shaped,
    normalized dict. Pure w.r.t. the model object, so it is unit-testable with a
    stub."""
    op = req["op"]
    text = req["text"]
    if op == "classify":
        raw = model.classify_text(text, req["tasks"], include_confidence=True)
        return {"results": normalize_classify(raw)}
    if op == "entities":
        raw = model.extract_entities(text, req["labels"])
        return {"entities": normalize_entities(raw, text)}
    if op == "extract":
        schema = model.create_schema().entities(req["labels"])
        for task, options in req["tasks"].items():
            schema = schema.classification(task, options)
        raw = model.extract(text, schema, include_confidence=True)
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
