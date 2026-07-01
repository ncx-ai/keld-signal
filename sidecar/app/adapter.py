"""Normalize GLiNER2 raw output into stable shapes.

These are pure functions so they are testable without loading the model.
GLiNER2 may return spans as bare strings or dicts, and classification as a
bare label, a dict, or a scored list — we accept all forms.

Vendored from inference-enrichment (services/sidecar/app/adapter.py); kept in
sync deliberately as a copy, not a runtime dependency on that repo.
"""
from typing import Any


def _coerce_score(obj: Any, default: float = 1.0) -> float:
    if isinstance(obj, dict):
        return float(obj.get("score", obj.get("confidence", default)))
    return default


def normalize_entities(raw: dict, text: str) -> list[dict]:
    out: list[dict] = []
    entities = raw.get("entities", raw) if isinstance(raw, dict) else {}
    for label, items in (entities or {}).items():
        for item in items or []:
            if isinstance(item, dict):
                surface = item.get("text") or item.get("span") or ""
                conf = _coerce_score(item)
            else:
                surface = str(item)
                conf = 1.0
            start = text.find(surface) if surface else -1
            end = start + len(surface) if start >= 0 else -1
            out.append(
                {"text": surface, "label": label, "start": start, "end": end, "confidence": conf}
            )
    return out


def normalize_extract(raw: dict, text: str, task_names: list[str]) -> dict:
    """Split a composed GLiNER2 extract() result into the stable shapes the API
    consumes: {"entities": [...], "results": {task: [ranked labels]}}.

    `raw` looks like {"entities": {label: [strings]}, "<task>": "<label>", ...}.
    """
    entities = normalize_entities(raw, text)              # reads raw["entities"]
    cls_raw = {t: raw[t] for t in task_names if t in raw}  # only the classification keys
    results = normalize_classify(cls_raw)
    return {"entities": entities, "results": results}


def normalize_classify(raw: dict) -> dict[str, list[dict]]:
    out: dict[str, list[dict]] = {}
    for task, val in (raw or {}).items():
        ranked: list[dict] = []
        if isinstance(val, str):
            ranked = [{"label": val, "confidence": 1.0}]
        elif isinstance(val, dict):
            ranked = [{"label": val.get("label"), "confidence": _coerce_score(val)}]
        elif isinstance(val, (list, tuple)):
            for entry in val:
                if isinstance(entry, (list, tuple)) and len(entry) >= 2:
                    ranked.append({"label": entry[0], "confidence": float(entry[1])})
                elif isinstance(entry, dict):
                    ranked.append({"label": entry.get("label"), "confidence": _coerce_score(entry)})
                elif isinstance(entry, str):
                    ranked.append({"label": entry, "confidence": 1.0})
        if not ranked:
            ranked = [{"label": None, "confidence": 0.0}]
        out[task] = ranked
    return out
