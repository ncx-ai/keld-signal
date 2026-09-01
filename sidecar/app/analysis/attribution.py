"""Block-level project attribution: org project definitions, their vectors, and
the hybrid score (embedding similarity + deterministic metadata boost) over a
block span.

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
import os
import re
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


# --- Scoring: embedding similarity + deterministic metadata boost ----------
#
# THRESHOLD/BAND/W_*/BOOST_CAP are benchmark-derived (measured against a
# labeled corpus in embedding-experiment/), not tuned by feel. Do not "tidy"
# these values without re-running that benchmark.
THRESHOLD = float(os.environ.get("KELD_ATTRIBUTION_THRESHOLD", "0.49"))
BAND = float(os.environ.get("KELD_ATTRIBUTION_BAND", "0.08"))
W_REPO, W_TICKET, W_KEYWORD, BOOST_CAP = 0.15, 0.20, 0.05, 0.35


def metadata_boost(project, dims, texts):
    """Deterministic boost from exact matches — repo, ticket key, keywords.

    Works with no model resident; that is the point (spec AC-4): a machine
    whose encoder weights were never provisioned still attributes work by
    repo, ticket key and keyword matching alone."""
    blob = " ".join(str(v) for v in (dims or {}).values()).lower()
    text = "\n".join(texts).lower()
    boost = 0.0
    for repo in project.get("repos") or []:
        if repo.lower() in blob or repo.lower() in text:
            boost += W_REPO
    tk = project.get("ticket_key")
    if tk and re.search(rf"\b{re.escape(tk)}-\d+", text + " " + blob, re.I):
        boost += W_TICKET
    boost += W_KEYWORD * sum(1 for kw in project.get("keywords") or []
                             if kw.lower() in text)
    return min(boost, BOOST_CAP)


def _cos(a, b):
    return sum(x * y for x, y in zip(a, b))


def score_block(texts, dims, encoder):
    """Hybrid score per project over one block's user-turn texts.

    Similarity is per-text MAX (not mean) across a block's texts against each
    project's vector: a block's several messages may each concern a different
    project, and averaging would wash out the one that matters. Text vectors
    are L2-normalised here before the dot product (`_l2`, the same helper
    `project_vectors` uses): the real encoder (`textembed._encode_batch`)
    already returns unit vectors, so this is a no-op in production, but the
    function does not trust an arbitrary `encoder` argument to have normalised
    its own output — a caller wiring in a different encoder gets a correct
    cosine rather than a silently wrong one.

    Returns (scores, borderline, assigned, encoder_used). `scores` is always
    fully populated, including the metadata boost alone when `encoder is
    None` — a human debugging an unattributed block needs to see that boost
    as telemetry. But **with no encoder, NOTHING is assigned and nothing is
    borderline, however large the boost.** `THRESHOLD`/`BAND` are tuned for a
    similarity-plus-boost score, and `BOOST_CAP` (0.35) sits below
    `THRESHOLD - BAND` (0.41) by construction, so a boost-only score can never
    cross them — that is not a bug to route around with a second, unmeasured
    assignment rule. There is exactly one attribution path, the benchmarked
    one: a machine with no model has no answer yet, not a weaker answer. The
    caller publishes `degraded:weights_unavailable` with no projects, and a
    durable job re-attributes the block once weights are provisioned.

    Privacy: `texts` and project descriptions are held in memory only for this
    call; nothing here logs or persists block text or project text."""
    projects, _ = current_projects()
    encoder_used = False
    sims = {p["id"]: 0.0 for p in projects}
    if encoder is not None and texts:
        pvecs = project_vectors(encoder)
        tvecs = [_l2(v) for v in encoder.encode(texts)]
        for pid, pv in pvecs.items():
            sims[pid] = max((_cos(tv, pv) for tv in tvecs), default=0.0)
        encoder_used = True
    scores, borderline, assigned = {}, [], []
    for p in projects:
        boost = metadata_boost(p, dims, texts)
        s = sims[p["id"]] + boost
        scores[p["id"]] = round(s, 4)
        if encoder_used:
            if abs(s - THRESHOLD) < BAND:
                borderline.append(p["id"])
            if s >= THRESHOLD:
                assigned.append(p["id"])
    return scores, borderline, assigned, encoder_used


def apply_verifier(texts, dims, scores, borderline, verifier_obj):
    """Verdicts for borderline pairs only. The verdict WINS over the threshold
    — that is the verifier's whole job (benchmark: fixes exactly the cases the
    threshold cannot call).

    `borderline` is empty whenever the encoder didn't run (score_block's own
    rule), so this does nothing at all in that case: no model load, no work.
    A `None` verifier — the caller opted out, or weights aren't provisioned —
    is handled the same way.

    Privacy: `texts` and the borderline projects' descriptions are formatted
    into a prompt and held in memory only for the `verify()` call this
    function makes — same rule as `score_block` above, and more load-bearing
    here, since this is the function that actually hands block text to a
    language model. Nothing here logs or persists block text or project
    text; `Verifier.verify()` (`app/verifier.py`) runs the model locally and
    the text never leaves the machine."""
    if not borderline or verifier_obj is None:
        return {}, 0, 0
    projects, _ = current_projects()
    by_id = {p["id"]: p for p in projects}
    block_text = "\n".join(texts)
    overrides, total = {}, 0.0
    for pid in borderline:
        verdict, secs = verifier_obj.verify(block_text, dims, by_id[pid])
        overrides[pid] = bool(verdict)
        total += secs
    return overrides, len(borderline), int(total * 1000)
