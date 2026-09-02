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
import time

from app.analysis import concepts

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
    """id -> L2-normalised vector, embedded once per content hash — INCLUDING
    the reserved `NULL_ID` entry for `NULL_DOC`, the "belongs to nothing"
    competitor `score_block` ranks against. It rides the same encode call and
    the same memo as the projects, so it can never be stale relative to them.

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
            vecs = encoder.encode([project_doc(p) for p in projects] + [NULL_DOC])
            out = {p["id"]: _l2(v) for p, v in zip(projects, vecs)}
            out[NULL_ID] = _l2(vecs[-1])
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
# ⚠️ THE DECISION IS RELATIVE, NOT AN ABSOLUTE BAR — `cut = max(null, top - MARGIN)`.
#
# An absolute threshold conflates two different questions, and real-transcript
# evaluation showed it (2026-09-02, 21 real blocks: the best absolute threshold
# still mixed 0.4x false positives in with 0.6+ true ones, because a block's
# scores carry a per-block offset — a chatty block scores mid against
# EVERYTHING, a terse one low against everything). The two questions are:
#
#   LEVEL — does this block belong to anything at all? Answered by a
#   competitor: NULL_DOC below is embedded beside the projects and its
#   similarity is the score "nothing" gets. A project attributes only by
#   BEATING nothing, on the same scale, in the same ranking. No boost applies
#   to it — exact repo/ticket evidence argues for a project, never for "none".
#
#   SHAPE — among things that beat nothing, is there a clear winner? Answered
#   by MARGIN: everything within MARGIN of the top score is assigned (a block
#   can genuinely serve two projects), anything further behind is not.
#
# `cut = max(null_score, top - MARGIN)` is both gates in one expression, and
# VERIFY_HALO around that cut is where the verifier adjudicates — the pairs
# whose distance from the cut is smaller than the score's own noise.
#
# MARGIN/VERIFY_HALO are starting points pending calibration on labeled real
# blocks (see test_attribution_quality.py's docstring); the boost weights are
# benchmark-derived — do not "tidy" any of them without re-measuring.
MARGIN = float(os.environ.get("KELD_ATTRIBUTION_MARGIN", "0.08"))
VERIFY_HALO = float(os.environ.get("KELD_ATTRIBUTION_VERIFY_HALO", "0.04"))
W_REPO, W_TICKET, W_KEYWORD, BOOST_CAP = 0.15, 0.20, 0.05, 0.35

# The reserved competitor. Never a project id (double underscore is not a
# valid declared id), never boosted, never published — it exists only inside
# the ranking, so "none of the above" competes instead of being a threshold.
NULL_ID = "__none__"
NULL_DOC = (
    "General conversation that belongs to no declared project: personal "
    "matters, casual chat, greetings, generic technical questions asked out "
    "of curiosity or for learning, help using a tool, or administrative talk "
    "that serves no specific project or initiative."
)


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

    Returns (scores, borderline, assigned, encoder_used, text_vectors).
    `text_vectors` is the block's own message vectors — the expensive artifact
    of this call, handed back rather than recomputed because `concepts.extract`
    needs the same vectors and a message costs ~1.1-1.6 s to encode. It is `[]`
    whenever the encoder did not run. `scores` is always
    fully populated, including the metadata boost alone when `encoder is
    None` — a human debugging an unattributed block needs to see that boost
    as telemetry. But **with no encoder, NOTHING is assigned and nothing is
    borderline, however large the boost.** The ranking below is over
    similarities the boost only ADJUSTS; with no similarities there is no
    ranking to adjust, and inventing a boost-only rule would be a second,
    unmeasured assignment path. A machine with no model has no answer yet,
    not a weaker answer: the caller publishes `degraded:weights_unavailable`
    with no projects, and a durable job re-attributes the block once weights
    are provisioned.

    The decision (see the MARGIN comment above): `cut = max(null, top - MARGIN)`.
    A project is assigned when its score reaches the cut — i.e. it BEATS the
    "belongs to nothing" competitor AND sits within MARGIN of the winner.
    `borderline` is every project within VERIFY_HALO of the cut, in either
    direction: the pairs whose distance from the decision is smaller than the
    score's own noise, which is exactly where a verifier verdict is worth its
    cost. Note one deliberate consequence: a strong exact-match boost (repo +
    ticket) CAN now carry a project past the null on its own — an exact repo
    and ticket reference is strong evidence regardless of how the prose reads.

    Privacy: `texts` and project descriptions are held in memory only for this
    call; nothing here logs or persists block text or project text."""
    projects, _ = current_projects()
    encoder_used = False
    null_sim = 0.0
    tvecs = []
    sims = {p["id"]: 0.0 for p in projects}
    if encoder is not None and texts:
        pvecs = project_vectors(encoder)
        tvecs = [_l2(v) for v in encoder.encode(texts)]
        for p in projects:
            pv = pvecs[p["id"]]
            sims[p["id"]] = max((_cos(tv, pv) for tv in tvecs), default=0.0)
        null_sim = max((_cos(tv, pvecs[NULL_ID]) for tv in tvecs), default=0.0)
        encoder_used = True
    scores = {}
    for p in projects:
        boost = metadata_boost(p, dims, texts)
        scores[p["id"]] = round(sims[p["id"]] + boost, 4)
    borderline, assigned = [], []
    if encoder_used and scores:
        top = max(scores.values())
        cut = max(null_sim, top - MARGIN)
        for pid, s in scores.items():
            if abs(s - cut) < VERIFY_HALO:
                borderline.append(pid)
            if s >= cut and top > null_sim:
                assigned.append(pid)
    return scores, borderline, assigned, encoder_used, tvecs


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
    overrides, total, verified = {}, 0.0, 0
    for pid in borderline:
        # ⚠️ `by_id.get`, NOT `by_id[pid]`. `borderline` was computed by score_block against
        # a snapshot of current_projects(); this re-reads it, and a POST /projects landing
        # between the two calls (a settings poll, or the daemon re-posting after a sidecar
        # restart) can retire a project id. An unguarded index raised KeyError, which is an
        # uncaught 500 — and the daemon classes a 500 as non-retryable, so it spent one of
        # the job's four attempts on a race that has nothing to do with the block. The
        # project is simply gone: dropping the pair leaves that id un-overridden, so the
        # threshold's own verdict stands, which is the honest answer for a project the org
        # no longer declares.
        project = by_id.get(pid)
        if project is None:
            continue
        verdict, secs = verifier_obj.verify(block_text, dims, project)
        overrides[pid] = bool(verdict)
        verified += 1
        total += secs
    # The COUNT is what was actually adjudicated, not what was queued — pairs_verified
    # riding len(borderline) would report work that a retired project meant never happened.
    return overrides, verified, int(total * 1000)


# --- The block's answer ------------------------------------------------------
#
# The CLOSED status vocabulary, mirrored Go-side as `enrich.Projects*`
# (internal/agent/enrich/attribution.go) and published as `projects_status` on
# a block row. Named here rather than typed as literals at each site for the
# reason `dynamics.REASONS` is: the Go side DROPS a value it does not
# recognise, so a drift between the two halves would silently stop publishing
# an attribution instead of failing.
STATUS_ATTRIBUTED = "attributed"
STATUS_PENDING = "pending"
STATUS_SKIPPED_DISABLED = "skipped:disabled"
STATUS_SKIPPED_NO_PROJECTS = "skipped:no_projects"
STATUS_DEGRADED_WEIGHTS = "degraded:weights_unavailable"
STATUSES = (STATUS_ATTRIBUTED, STATUS_PENDING, STATUS_SKIPPED_DISABLED,
            STATUS_SKIPPED_NO_PROJECTS, STATUS_DEGRADED_WEIGHTS)

# The models this attribution path runs on, reported with every answer. Two
# corpora scored under different models are not comparable and nothing about
# the numbers says so — `featuretext.encoder_ref`'s argument, one facet along.
MODEL_VERSIONS = {"encoder": "qwen3-embedding-0.6b", "verifier": "gemma-4-e2b-q4km"}


def _meta(embed_ms, verify_ms, pairs, encoder_state, verifier_state, concept_ms=0):
    """The pass's report on ITSELF: integer timings, counts and closed enums.

    `encoder_state` is `warm` or `absent`, and there is deliberately no `cold`: this module
    never loads the encoder, so there is no pass that could report having done so. A cold
    child is reported as `status: "pending"` by the route — see
    `enrich.AttributionMeta.EncoderState` for the whole of that reasoning, which these two
    halves must change together.

    ⚠️ Nothing here may ever hold text, a span or an offset — see
    `enrich.AttributionMeta` and the reflection tripwire beside it. A project's
    name is exactly the class of string `named_terms` has already shown can
    hold a real person's."""
    return {"embed_ms": embed_ms, "verify_ms": verify_ms, "concept_ms": concept_ms,
            "pairs_verified": pairs,
            "encoder_state": encoder_state, "verifier": verifier_state,
            "model_versions": dict(MODEL_VERSIONS)}


def stated(status, encoder_state="absent"):
    """A terminal answer that RAN NOTHING, with the reason named.

    The caller owns the environment facts this module deliberately knows
    nothing about — whether the encoder is switched off on this machine, for
    instance — so it needs a way to answer in the same shape. A skip is always
    stated, never a silently empty `projects` list."""
    return {"status": status, "projects": [], "concepts": [],
            "attribution": _meta(0, 0, 0, encoder_state, "not_needed")}


def pending():
    """`{"status": "pending", "projects": [], "attribution": null}`.

    The one answer this module cannot produce from a decision: it means the
    ENCODER could not answer promptly (a cold child, a backlog), which only the
    caller can see. `attribution` is null rather than a zeroed meta because
    nothing was measured — a zero timing beside an `encoder_state` reads as a
    pass that ran instantly, which is the confident-number-over-nothing failure
    this codebase names everywhere else. The daemon's sweep is the retry loop;
    there is deliberately no second queue inside this process."""
    return {"status": STATUS_PENDING, "projects": [], "concepts": [], "attribution": None}


def attribute_block(texts, dims, encoder, verifier_obj, verifier_absent="opted_out"):
    """ONE block's attribution answer: `{status, projects, attribution}`.

    The whole decision and nothing else — no HTTP, no file, no environment.
    The caller supplies the block's user-turn texts, its deterministic dims, an
    encoder (or `None`) and a verifier (or `None`); it gets back ids,
    confidences, closed enums and integer timings. That split is what makes the
    decision testable against plain fakes and the route a thin adapter over it.

    ⚠️ **`concepts` is the one key here that is NOT an id** — phrases lifted
    from the block's own words and ranked against its own centroid (see
    `concepts.py`, which carries the privacy argument this docstring's
    "no text in either direction" rule is otherwise absolute about). It rides
    THIS answer rather than the block digest because the encoder and the
    message vectors are both already here: computing it in `/blocks` would
    make block emission depend on a model it does not otherwise need, and
    would encode every message a second time.

    Four terminal shapes, each of which STATES itself:

      * `skipped:no_projects` — nothing was declared to match against, so there
        was structurally nothing to attribute to. Checked FIRST, before the
        encoder is looked at: a machine with no projects must never be told to
        come back later.
      * `attributed` — the benchmarked path RAN. `projects` may still be empty,
        and that is a real answer ("none of these projects"), not a failure —
        the discovery's decision table row 5.
      * `degraded:weights_unavailable` — no encoder. ⚠️ **NOTHING is
        attributed, however strong the exact-match evidence** (AC-4 as amended
        2026-09-01). The ranking is over similarities the boost only adjusts;
        with no similarities a boost-only assignment could exist only under a
        second, unmeasured rule; there is exactly one attribution path, the
        benchmarked one. A machine with no model has no answer YET, not a
        weaker answer, and the daemon's durable job re-attributes the block
        once weights are provisioned. The boost is still computed — it rides
        `scores` as telemetry inside this process, and reaches the wire only as
        the confidence of a project the encoder assigned.
      * a block with NO user-turn text — the encoder has nothing to embed and
        no later sweep can change that, so it is the empty `attributed` answer
        rather than `pending`, which would have the daemon retry a block that
        can never move.

    `verifier_absent` names WHY there is no verifier when `verifier_obj is
    None`: `opted_out` (the operator switched it off) or `unavailable` (it was
    needed and could not run). Two different facts, and reporting the first
    when the second happened is the silent narrowing AC-6 forbids.

    Privacy: `texts` and the project documents live in this process for the
    duration of this call and nowhere else. The returned dict is the response
    body verbatim, and holds no text, no span and no offset.
    """
    projects, _ = current_projects()
    encoder_state = "absent" if encoder is None else "warm"
    if not projects:
        return stated(STATUS_SKIPPED_NO_PROJECTS, encoder_state)
    if not texts:
        return {"status": STATUS_ATTRIBUTED, "projects": [], "concepts": [],
                "attribution": _meta(0, 0, 0, encoder_state, "not_needed")}

    t0 = time.time()
    scores, borderline, assigned, encoder_used, tvecs = score_block(texts, dims, encoder)
    embed_ms = int((time.time() - t0) * 1000)
    found, concept_ms = concepts.extract(texts, tvecs, encoder if encoder_used else None)
    overrides, pairs, verify_ms = apply_verifier(texts, dims, scores, borderline, verifier_obj)

    final = []
    for pid in scores:
        # The verdict WINS over the threshold in BOTH directions — that is the
        # verifier's whole job (see apply_verifier).
        inside = overrides[pid] if pid in overrides else pid in assigned
        if inside:
            # ⚠️ TWO SOURCES, NOT THREE. "metadata" is GONE from the published
            # vocabulary (see enrich.ProjectAttribution.Source): the boost is
            # still part of every confidence, but AC-4 as amended means nothing
            # is ever assigned without the encoder, so no answer can carry it.
            # A value no producer can emit is worse than an absent one.
            final.append({"id": pid, "confidence": scores[pid],
                          "source": "verifier" if pid in overrides else "embedding"})

    if verifier_obj is not None:
        verifier_state = "used" if pairs else "not_needed"
    else:
        verifier_state = verifier_absent if borderline else "not_needed"
    status = STATUS_ATTRIBUTED if encoder_used else STATUS_DEGRADED_WEIGHTS
    return {"status": status, "projects": final, "concepts": found,
            "attribution": _meta(embed_ms, verify_ms, pairs,
                                 "warm" if encoder_used else "absent", verifier_state,
                                 concept_ms)}
