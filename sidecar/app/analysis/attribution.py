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
import logging
import os
import re
import threading
import time

from app.analysis import concepts

_log = logging.getLogger(__name__)

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

# ⚠️ **THE WORDING IS MEASURED, AND ITS CATEGORIES ARE NOT INTERCHANGEABLE.**
# This document is embedded beside the projects and every decision ranks against
# it, so a clause here is a published-behaviour change: re-run
# `test_attribution_quality.py` on any edit.
#
# Two clauses were REMOVED on 2026-09-02 after a five-way sweep over the 100
# fixtures (each arm re-encodes only this string, so the conversation and project
# vectors are shared and the arms differ in nothing else):
#
#   * "generic technical questions asked out of curiosity or for learning" —
#     it described an engineer asking about THEIR OWN declared project, and
#     conv_046 is the proof: "for our component library — should focus styles
#     use outline or box-shadow?" attributed to nothing, because a real
#     design-system question read as idle curiosity. Deleting it fixed that
#     conversation and broke none, and cost NOTHING in trap coverage (11 of the
#     19 no-project fixtures held, unchanged): the traps are earned by the
#     "work that belongs elsewhere" clause below, never by this one. Measured
#     f1 0.724 -> 0.740, precision 0.774 -> 0.787, embedding-only arm.
#   * an abstract-study clause ("explanation of a concept in the abstract").
#     Tried in two shapes and both LOST — the better of them took precision to
#     0.681 and shown-label trust from 77% to 68% — because "a concept in the
#     abstract" also describes a design discussion about work in progress. The
#     two fixtures it exists for (conv_074's RAG tutoring, conv_096's
#     internet-thread argument) are not worth that, and they are the only two.
#
# ⚠️ **AND A LONGER DOCUMENT IS A WEAKER ONE HERE.** The most complete wording
# tried (every category enumerated, ~90 words) scored WORST of all five arms —
# f1 0.698, breaking three conversations and fixing none. Adding a category
# dilutes the ones already present; this is not a list to extend freely.
#
# ⚠️ **WHAT THIS WORDING DOES *NOT* FIX, SO NOBODY RE-LITIGATES IT HERE.** The
# null also beat every project on a REAL 50-hour session at every window size,
# and that symptom is NOT lexical: deleting the offending clause left the
# block-scale null score identical (0.623) and the session-scale one slightly
# worse. Measured cause — this document describes SPEECH while `project_doc`
# composes an ARTIFACT ("Title (Team)", description, keywords), so it is the
# only register-matched document in the set and collects a similarity bonus on
# anything a person types: a project beat it on just 34% of that session's 182
# individual messages (median margin -0.069). Reframing the project documents
# into this register moves that to 72% (+0.049) and flips the session to
# attributing — but it MOVES the hub rather than removing it (proj_devex, whose
# own description is written in process register, then wins the whole session at
# 0.797) and it trades trust for coverage on the fixtures. That is per-document
# hubness, the column-wise twin of the per-block offset MARGIN already exists
# for, and its fix is score normalisation, not prose. See
# `docs/notes/whats-next-attribution.md`.
NULL_DOC = (
    "Conversation serving none of the declared projects: personal matters "
    "(family, health, money, travel, food, fitness, films), casual chat and "
    "greetings, administrative talk, help operating a tool, and work that "
    "belongs elsewhere — a personal side project or hobby project, a friend's "
    "or a client's company, freelance work, a tutorial being written, or "
    "interview preparation."
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


# --- Scoring mode, pooling and centring: measured on 2026-09-03 -------------------
#
# ⚠️ BOTH ARE MEASURED, AND NEITHER IS ENOUGH ALONE. Over 61 real, labelled blocks
# on the machine that built this (docs/notes/whats-next-attribution.md §9):
#
#   user text only, per-message MAX, centred ........  28% of blocks right (shipped input)
#   whole block (user + assistant), MEAN, NOT centred   59%
#   whole block (user + assistant), MEAN, centred ....  92%   <- this
#   whole block, MAX, centred ........................  82%
#
# The user's own words are often "continue" or "why is it so slow?"; the reply is
# the paragraph that names the sidecar, the encoder and the eval. Reading the whole
# block is what reaches the 24-of-25 blocks with NO user text at all. MEAN beats
# MAX over a block of ~12 messages for the reason the session-window study found —
# the best of N draws is high for anything — and mean is also what lets the
# assistant's many turns be read without one tangent deciding the block.
#
# CENTRING subtracts each document's mean similarity over the messages this machine
# has scored so far. It exists because the null document is written as SPEECH and
# every project document as an ARTIFACT, so the null out-scored every project on 66%
# of individual messages regardless of topic; and because the projects' own baselines
# spanned 0.093 against a MARGIN of 0.08, i.e. one project could beat another on
# register alone. Removing the per-document baseline is what turns 59% into 92%.
# It is a running mean of SCALARS — never a stored vector, which `featuretext`'s
# header explains this codebase does not persist — and it is GATED: below
# MIN_BACKGROUND messages the offsets are all zero and the decision is exactly the
# pre-centring one, because an offset from ten messages measured non-monotone
# (decision agreement with the settled offsets 77% at n=5, 74% at n=10, 93% at
# n=50, 97% at n=100), and a half-fitted offset is not a gentler correction but a
# different one.
# The scoring rule, and its one-step rollback. `block-mean-centred` is what was measured
# and ships. `KELD_ATTRIBUTION_SCORING=user-max` restores the pre-2026-09-03 decision
# EXACTLY — user turns only, per-message MAX, no centring, no baseline observed — so a
# machine that regresses can be put back with one environment variable and no code change.
# The mode is stamped into `model_versions.scoring`, so rows from the two rules are never
# mistaken for comparable. Read at import, like every other KELD_* knob in this module.
SCORING_DEFAULT, SCORING_LEGACY = "block-mean-centred", "user-max"
SCORING = os.environ.get("KELD_ATTRIBUTION_SCORING", SCORING_DEFAULT).strip().lower() \
    or SCORING_DEFAULT
MIN_BACKGROUND = int(os.environ.get("KELD_ATTRIBUTION_MIN_BACKGROUND", "50"))


def legacy_scoring():
    return SCORING == SCORING_LEGACY


def _pool(values, how):
    """`how` is "mean" (the measured rule) or "max" (the legacy one) — a parameter chosen by
    the scoring mode, never a module constant with a single value."""
    values = list(values)
    if not values:
        return 0.0
    return max(values) if how == "max" else sum(values) / len(values)


class Offsets:
    """Per-document centring state: for each document (every project, and the
    null), the running mean of cos(message, document) over the messages this
    machine has scored. Held as (sum, count) pairs keyed by a hash of the
    document's TEXT, so a reworded project or null starts a fresh baseline
    rather than inheriting one measured against different words.

    Persisted as a small JSON of scalars under KELD_HOME/state, written
    atomically, so a restart does not reset the gate. A vector is never
    written: two floats per document is the whole file.

    ⚠️ ALL-OR-NOTHING GATE. `for_docs` returns offsets only when EVERY current
    document has at least MIN_BACKGROUND observations; otherwise None, and the
    caller scores uncentred. A newly declared project therefore switches
    centring OFF for everyone until it has its own baseline — deliberately.
    Centring some documents and not others would hand the uncentred one its
    raw register bias against corrected competitors, which is the failure this
    class exists to remove, applied to one column."""

    #: Blocks remembered so a RETRIED job does not fold the same messages in twice. A held
    #: job (publish failure, sidecar respawn) is re-POSTed and re-scored; without this the
    #: baseline would tilt toward whichever blocks happened to be retried. Bounded, oldest
    #: dropped: after this many blocks a repeat is both unlikely and harmless.
    MAX_SEEN = 2000

    def __init__(self, path=None):
        self.path = path
        self._stats = {}
        self._seen = []
        self._lock = threading.Lock()
        self._save_failed = False
        if path and os.path.exists(path):
            try:
                with open(path) as fh:
                    raw = json.load(fh)
                # {"stats": {...}, "seen": [...]} — or the first-day flat shape, stats only.
                stats = raw.get("stats", raw) if isinstance(raw, dict) else {}
                self._stats = {k: [float(v[0]), int(v[1])] for k, v in stats.items()
                               if isinstance(v, list) and len(v) == 2}
                seen = raw.get("seen", []) if isinstance(raw, dict) else []
                self._seen = [str(x) for x in seen][-self.MAX_SEEN:]
            except (OSError, ValueError, TypeError, AttributeError):
                self._stats, self._seen = {}, []

    @staticmethod
    def key(doc_text, stream="user"):
        """One baseline per (stream, document). The two streams are different registers —
        a person's terse prompts and a model's polished prose — and centring assistant text
        against a baseline diluted with user messages under-corrects it: on the 23 real
        blocks with no user text, per-stream centring scored F1 0.717 against 0.606 for a
        single mixed baseline, and it was also better on the with-input blocks (0.782 vs
        0.772; 0.806 vs 0.781 on the text-only judge)."""
        return hashlib.sha256(f"{stream}\x1f{doc_text}".encode()).hexdigest()[:16]

    def count(self, keys):
        with self._lock:
            return min((self._stats.get(k, [0.0, 0])[1] for k in keys), default=0)

    def for_docs(self, keys):
        with self._lock:
            if any(self._stats.get(k, [0.0, 0])[1] < MIN_BACKGROUND for k in keys):
                return None
            return {k: self._stats[k][0] / self._stats[k][1] for k in keys}

    def observe(self, streams, block_key=None):
        """Fold one block into every document's running mean, per stream.

        `streams` is `{stream: (doc_vectors, message_vectors)}` — every stream of ONE block
        in one call, so the block is remembered once whether it had one stream or two.
        Called AFTER the block is scored, so a block never centres on itself. A `block_key`
        already seen is a retried job and is skipped whole; `None` (tests, the eval's own
        bookkeeping) is always folded."""
        if not any(vecs for _, vecs in streams.values()):
            return
        with self._lock:
            if block_key is not None:
                if block_key in self._seen:
                    return
                self._seen.append(block_key)
                if len(self._seen) > self.MAX_SEEN:
                    del self._seen[:len(self._seen) - self.MAX_SEEN]
            for doc_vectors, message_vectors in streams.values():
                for k, dv in doc_vectors.items():
                    s, n = self._stats.get(k, [0.0, 0])
                    for mv in message_vectors:
                        s += _cos(mv, dv)
                        n += 1
                    self._stats[k] = [s, n]
            self._save()

    def _save(self):
        if not self.path:
            return
        try:
            os.makedirs(os.path.dirname(self.path), exist_ok=True)
            tmp = self.path + ".tmp"
            with open(tmp, "w") as fh:
                json.dump({"stats": self._stats, "seen": self._seen}, fh)
            os.chmod(tmp, 0o600)
            os.replace(tmp, self.path)
            self._save_failed = False
        except OSError as exc:
            # A baseline that cannot be saved is re-learned after a restart — never fatal.
            # But silent is not the same as harmless: a machine that can never persist will
            # re-learn from zero on EVERY restart and sit uncentred for its first 50 messages
            # each time. Say so once per process, not once per block.
            if not self._save_failed:
                self._save_failed = True
                _log.warning("attribution: centring baseline could not be saved to %s (%s); "
                             "it will be re-learned after a restart", self.path,
                             exc.__class__.__name__)


def default_offsets_path():
    """`~/.keld/state/attribution-offsets.json`, beside `refseries.db` and
    `prompt-lengths.json`, honouring KELD_HOME the way `store.default_path` does."""
    home = os.environ.get("KELD_HOME") or os.path.join(os.path.expanduser("~"), ".keld")
    return os.path.join(home, "state", "attribution-offsets.json")


USER_STREAM, ASST_STREAM = "user", "asst"


def score_block(texts, dims, encoder, offsets=None, n_user=None, block_key=None):
    """Hybrid score per project over one block's texts — the WHOLE block, user
    and assistant turns alike, as the caller hands them.

    Similarity is per-message cosine, pooled by MEAN under the shipped scoring mode (see
    the measurement above `SCORING` for why not MAX; `KELD_ATTRIBUTION_SCORING=user-max`
    is the legacy rule), then centred by subtracting
    each document's running baseline when `offsets` is given and its gate is
    met. Text vectors are L2-normalised here before the dot product (`_l2`,
    the same helper `project_vectors` uses): the real encoder
    (`textembed._encode_batch`) already returns unit vectors, so this is a
    no-op in production, but the function does not trust an arbitrary
    `encoder` argument to have normalised its own output — a caller wiring in
    a different encoder gets a correct cosine rather than a silently wrong one.

    `n_user` is how many leading entries of `texts` are the user's own turns; the rest are
    assistant turns. It decides which stream's baseline each message is centred against
    (see `Offsets.key`). `None` means every text is the user's — the shape every pre-2026-09-03
    caller and test has.

    Returns (scores, borderline, assigned, encoder_used, text_vectors, centring).
    `centring` is `{"applied": bool, "background_n": int}` — what the meta
    publishes so a row centred on 300 messages is distinguishable from one that
    was not centred at all. `text_vectors` is the block's own message vectors,
    in the order of `texts` — the expensive artifact of this call, handed back
    rather than recomputed because `concepts.extract` needs the USER subset of
    them and a message costs ~1.1-1.6 s to encode. It is `[]`
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
    centring = {"applied": False, "background_n": 0}
    if encoder is not None and texts:
        pvecs = project_vectors(encoder)
        tvecs = [_l2(v) for v in encoder.encode(texts)]
        n_u = len(texts) if n_user is None else max(0, min(n_user, len(texts)))
        streams = [USER_STREAM] * n_u + [ASST_STREAM] * (len(texts) - n_u)
        docs = {p["id"]: pvecs[p["id"]] for p in projects}
        docs[NULL_ID] = pvecs[NULL_ID]
        doc_text = {p["id"]: project_doc(p) for p in projects}
        doc_text[NULL_ID] = NULL_DOC

        how = "max" if legacy_scoring() else "mean"
        # One offset per (stream, document), keyed by document TEXT so a reworded
        # project starts a fresh baseline. Gated on the streams THIS block uses.
        # Under the legacy rule there is no centring and no baseline is observed.
        off = None
        used_streams = sorted(set(streams))
        if offsets is not None and not legacy_scoring():
            keys = {(st, did): Offsets.key(doc_text[did], st)
                    for st in used_streams for did in docs}
            off = offsets.for_docs(keys.values())
            centring["background_n"] = offsets.count(keys.values())
            centring["applied"] = off is not None

        def centred_sims(did):
            for st, tv in zip(streams, tvecs):
                yield _cos(tv, docs[did]) - (off[keys[(st, did)]] if off else 0.0)

        for p in projects:
            sims[p["id"]] = _pool(centred_sims(p["id"]), how)
        null_sim = _pool(centred_sims(NULL_ID), how)
        encoder_used = True
        if offsets is not None and not legacy_scoring():
            # Observe AFTER deciding, so no block centres on itself — every stream of the
            # block in one call, so a retried block is skipped whole.
            offsets.observe({st: ({Offsets.key(doc_text[did], st): docs[did] for did in docs},
                                  [tv for s_, tv in zip(streams, tvecs) if s_ == st])
                             for st in used_streams}, block_key)
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
    return scores, borderline, assigned, encoder_used, tvecs, centring


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
#
# ⚠️ `null_doc` IS IN HERE FOR EXACTLY THAT REASON, AND IT IS NOT A MODEL. Every
# decision on this path ranks against `NULL_DOC` (see `score_block`), so its
# wording is as load-bearing as the encoder's identity: rewording it makes two
# corpora incomparable in precisely the way this map exists to declare. The
# 2026-09-02 reword is what proved the need — blocks scored before and after it
# sit side by side in Atlas with nothing on the row to separate them, which
# defeats anyone measuring the pipeline against hand-labelled blocks. It is a
# truncated sha256 of the document itself, so it cannot drift from the text the
# way a hand-maintained version string would and nobody has to remember to bump
# it. It rides the existing open map rather than a new field:
# `enrich.AttributionMeta` models this as `map[string]string` and Atlas stores the
# raw body, so both halves carry a new key unchanged and no schema bump is owed —
# this is an identifier, not a vocabulary.
MODEL_VERSIONS = {"encoder": "qwen3-embedding-0.6b", "verifier": "gemma-4-e2b-q4km",
                  "null_doc": hashlib.sha256(NULL_DOC.encode()).hexdigest()[:8],
                  # The scoring RULE, for the same reason as null_doc: rows scored
                  # user-only/max/uncentred and rows scored whole-block/mean/centred
                  # are not comparable, and nothing else on the row would say so.
                  "scoring": ("user-max-uncentred-v0" if legacy_scoring()
                              else "block-mean-centred-perstream-v1")}


def _meta(embed_ms, verify_ms, pairs, encoder_state, verifier_state, concept_ms=0,
          centring=None):
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
    c = centring or {"applied": False, "background_n": 0}
    return {"embed_ms": embed_ms, "verify_ms": verify_ms, "concept_ms": concept_ms,
            "pairs_verified": pairs,
            "encoder_state": encoder_state, "verifier": verifier_state,
            # Whether the per-document baseline was subtracted, and over how many
            # messages it had been measured. A count and a flag: a consumer can tell
            # a row centred on 300 messages from one scored before the gate opened,
            # which matters because the two decisions differ on ~40% of blocks.
            "centred": bool(c.get("applied")), "background_n": int(c.get("background_n", 0)),
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


def attribute_block(texts, dims, encoder, verifier_obj, verifier_absent="opted_out",
                    asst_texts=(), offsets=None, block_key=None):
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
      * a block with NO text in EITHER stream — the encoder has nothing to
        embed and no later sweep can change that, so it is the empty
        `attributed` answer rather than `pending`, which would have the daemon
        retry a block that can never move. ⚠️ This used to say "no USER-turn
        text", and that made 24 of 25 agent-only blocks on a real machine
        permanently unattributable while their assistant turns said exactly
        what the work was. A block is now empty only when both streams are.

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
    texts = list(texts)
    # The legacy rule read the user stream alone; under it the assistant turns are dropped
    # here so `score_block` sees exactly the pre-2026-09-03 input.
    asst_texts = [] if legacy_scoring() else list(asst_texts)
    if not texts and not asst_texts:
        return {"status": STATUS_ATTRIBUTED, "projects": [], "concepts": [],
                "attribution": _meta(0, 0, 0, encoder_state, "not_needed")}

    # ⚠️ THE WHOLE BLOCK IS SCORED; ONLY THE USER'S WORDS FEED CONCEPTS AND THE
    # VERIFIER. Scoring reads user and assistant turns together (see SCORING for
    # the measurement — 28% right on user text alone, 92% on the whole block,
    # and the assistant's reply is the only text a block with no prompt has).
    # `concepts` stays on USER text: it PUBLISHES phrases, and the privacy
    # argument in concepts.py was made for a person's own words, not for prose a
    # model generated about them. The verifier prompt likewise keeps the user's
    # words — it is off by default and its prompt is capped at 2,500 chars, so
    # widening it is a separate, measured decision. The user texts come FIRST in
    # the scored list so their vectors are the leading slice of `tvecs`.
    t0 = time.time()
    scores, borderline, assigned, encoder_used, tvecs, centring = score_block(
        texts + asst_texts, dims, encoder, offsets, n_user=len(texts), block_key=block_key)
    embed_ms = int((time.time() - t0) * 1000)
    user_vecs = tvecs[:len(texts)] if tvecs else []
    found, concept_ms = concepts.extract(texts, user_vecs, encoder if encoder_used else None)
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
                                 concept_ms, centring)}
