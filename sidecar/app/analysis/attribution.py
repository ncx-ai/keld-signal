"""Block-level project attribution: org project definitions, their vectors, and
(the scoring half, added by the next change) the hybrid score over a block span.

Definitions flow DOWN from the daemon (POST /projects). Only project IDs are
ever returned upward. Vectors are embedded once per content hash and held in
module state — a handful of short documents, ~1s total on CPU.

⚠️ **"Embedded once per content hash" is a claim about outcome, not about
`_lock`'s hold time.** `_lock` guards `_projects`/`_hash`/`_vectors`/
`_vectors_hash` and is never held across `encoder.encode()` — a slow encode
under a global lock would stall every other endpoint this sidecar serves
concurrently, and it serves them concurrently by design. Instead a second
lock, `_encode_lock`, single-flights the encode itself: only one thread is
ever inside `encoder.encode()` at a time, and every thread queued up behind
it re-checks the cache immediately after acquiring `_encode_lock`, so a wave
of concurrent callers on a cold cache produces exactly one `encode()` call,
not one per caller. Holding `_encode_lock` across the whole encode is
deliberate and cheap here — it happens once per project-list change, for a
handful of short documents, never per request.

⚠️ **The write side re-validates before installing, so a slow writer can
never overwrite a fresher cache entry.** If `set_projects` runs while an
encode is in flight, `_hash` can move out from under the snapshot that encode
started against. The write back into `_vectors`/`_vectors_hash` compares the
encode's target hash to the CURRENT `_hash` and refuses to install a result
for a hash that is no longer current; `project_vectors` then loops and
re-embeds against whatever hash is now live. No caller is ever served a
vector set that does not match `current_projects()`'s hash at the moment it
was produced."""
import hashlib
import json
import threading

_lock = threading.Lock()
_encode_lock = threading.Lock()
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
    """id -> L2-normalised vector, embedded once per content hash.

    Single-flighted: `_encode_lock` ensures only one `encoder.encode()` call
    is ever in flight, and the write back is refused if `_hash` moved while
    encoding — see the module docstring for why."""
    global _vectors, _vectors_hash
    while True:
        with _lock:
            if _vectors is not None and _vectors_hash == _hash:
                return _vectors
        with _encode_lock:
            with _lock:
                # Another thread may have finished the encode while we waited
                # for _encode_lock — re-check before doing any work.
                if _vectors is not None and _vectors_hash == _hash:
                    return _vectors
                projects, target_hash = list(_projects), _hash
            vecs = encoder.encode([project_doc(p) for p in projects])
            out = {p["id"]: _l2(v) for p, v in zip(projects, vecs)}
            with _lock:
                if _hash == target_hash:
                    _vectors, _vectors_hash = out, target_hash
                    return out
                # _hash moved during the encode: this result is for a hash
                # that is no longer current. Discard it and retry against
                # whatever is current now.


def _l2(v):
    n = sum(x * x for x in v) ** 0.5 or 1.0
    return [x / n for x in v]
