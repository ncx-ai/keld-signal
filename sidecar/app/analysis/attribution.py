"""Block-level project attribution: org project definitions, their vectors, and
(the scoring half, added by the next change) the hybrid score over a block span.

Definitions flow DOWN from the daemon (POST /projects). Only project IDs are
ever returned upward. Vectors are embedded once per content hash and held in
module state — a handful of short documents, ~1s total on CPU."""
import hashlib
import json
import threading

_lock = threading.Lock()
_projects: list[dict] = []
_hash = ""
_vectors: dict[str, list[float]] | None = None
_vectors_hash = ""


def set_projects(projects):
    global _projects, _hash, _vectors, _vectors_hash
    canon = json.dumps(projects, sort_keys=True, separators=(",", ":"))
    h = hashlib.sha256(canon.encode()).hexdigest()[:16]
    with _lock:
        if h != _hash:
            _projects, _hash = list(projects), h
            if _vectors_hash != h:
                _vectors, _vectors_hash = None, ""
    return h


def current_projects():
    with _lock:
        return list(_projects), _hash


def project_doc(p):
    parts = [f"{p.get('title', '')} ({p.get('team', '')})", p.get("description", "")]
    if p.get("keywords"):
        parts.append("Keywords: " + ", ".join(p["keywords"]))
    return "\n".join(s for s in parts if s.strip())


def project_vectors(encoder):
    """id -> L2-normalised vector, embedded once per content hash."""
    global _vectors, _vectors_hash
    with _lock:
        if _vectors is not None and _vectors_hash == _hash:
            return _vectors
        projects, h = list(_projects), _hash
    vecs = encoder.encode([project_doc(p) for p in projects])
    out = {p["id"]: _l2(v) for p, v in zip(projects, vecs)}
    with _lock:
        _vectors, _vectors_hash = out, h
    return out


def _l2(v):
    n = sum(x * x for x in v) ** 0.5 or 1.0
    return [x / n for x in v]
