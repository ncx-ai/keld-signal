"""Run: cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_attribution_endpoint.py

Two halves, deliberately: `attribute_block` — the whole decision, exercised against plain fakes
with no HTTP, no file and no environment — and the `/attribute` route, exercised by calling the
handler directly (as `/blocks` and `/features` are tested) so the confinement check, the span
read, the encoder ADAPTATION and the pending path are pinned without a live model.
"""
import asyncio
import datetime as _dt
import json
import os
import tempfile
import time

from app.analysis import attribution
from app.analysis import concepts as concepts_mod

PROJECTS = [
    {"id": "proj_pay", "title": "Payments", "team": "Eng",
     "description": "Stripe billing migration.", "repos": ["acme-billing"],
     "keywords": ["stripe"], "ticket_key": "PAY"},
]


class PayEncoder:
    """The shape `score_block` takes: `.encode(texts) -> [vec, ...]`. NOT the production
    encoder's shape — see the route tests below, which is where that adaptation lives."""

    def encode(self, texts):
        return [[1.0, 0.0] if "Payments" in t else [0.9, 0.1] for t in texts]


class StubVerifier:
    def verify(self, block_text, dims, project):
        return True, 0.01


# --- the decision ---------------------------------------------------------------------------

def test_attributed_full_path():               # AC-3
    attribution.set_projects(PROJECTS)
    out = attribution.attribute_block(
        ["stripe webhook retries again"], {"repo": "acme-billing"},
        encoder=PayEncoder(), verifier_obj=StubVerifier())
    assert out["status"] == "attributed"
    ids = [p["id"] for p in out["projects"]]
    assert ids == ["proj_pay"]
    meta = out["attribution"]
    assert meta["encoder_state"] == "warm" and meta["embed_ms"] >= 0


def test_weights_absent_is_degraded():         # AC-4 (AMENDED 2026-09-01)
    attribution.set_projects(PROJECTS)
    out = attribution.attribute_block(
        ["fix PAY-12 dunning"], {"repo": "acme-billing"}, encoder=None, verifier_obj=None)
    assert out["status"] == "degraded:weights_unavailable"
    # No guessing without the model: the caller retries this block once weights land.
    assert out["projects"] == []
    assert out["attribution"]["encoder_state"] == "absent"


def test_no_projects_is_skipped():             # AC-1 status
    attribution.set_projects([])
    out = attribution.attribute_block(["anything"], {}, encoder=PayEncoder(), verifier_obj=None)
    assert out["status"] == "skipped:no_projects" and out["projects"] == []


def test_source_labels():
    attribution.set_projects(PROJECTS)
    out = attribution.attribute_block(
        ["stripe webhook retries again"], {}, encoder=PayEncoder(), verifier_obj=None)
    assert out["projects"][0]["source"] in ("embedding", "metadata", "verifier")


def test_a_span_with_no_text_in_either_stream_is_terminal_not_pending():
    """A closed block can hold no words at all. The encoder has nothing to embed and no later
    sweep can change that, so the answer is the benchmarked path's own empty answer — never
    `pending`, which would have the daemon retry a block that can never move."""
    attribution.set_projects(PROJECTS)
    out = attribution.attribute_block([], {"repo": "acme-billing"},
                                      encoder=PayEncoder(), verifier_obj=None)
    assert out["status"] == "attributed" and out["projects"] == []
    assert out["attribution"]["encoder_state"] == "warm"
    assert out["attribution"]["verifier"] == "not_needed"


def test_a_span_with_only_assistant_text_is_scored_not_terminal():
    """The agent-only block: a prompt in an EARLIER block triggered work that ran on into this
    one, so it has no user turn but several assistant turns describing the work. Until
    2026-09-03 this was terminal-empty by construction — 24 of 25 such blocks on a real machine
    had assistant text and none could be attributed. Now the assistant stream is scored, and
    `concepts` receives NO vectors (there are no user words to lift phrases from)."""
    attribution.set_projects(PROJECTS)
    out = attribution.attribute_block([], {}, encoder=PayEncoder(), verifier_obj=None,
                                      asst_texts=["I migrated the stripe webhooks for you"])
    assert out["status"] == "attributed", out
    assert [p["id"] for p in out["projects"]] == ["proj_pay"], out
    assert out["concepts"] == [], out["concepts"]
    assert out["attribution"]["centred"] is False and out["attribution"]["background_n"] == 0


# Its own project list so the vector cache is COLD here: `project_vectors` memoises per content
# hash, so a list another test already embedded would be scored against that test's encoder.
MID_PROJECTS = [{"id": "proj_mid", "title": "Ambiguity", "team": "Eng",
                 "description": "Work that lands in the band.", "repos": [], "keywords": []}]


class MidEncoder:
    """Block text at 0.52 vs the project and 0.50 vs NULL_DOC: the cut is
    max(null, top - MARGIN) = 0.50 and the pair sits 0.02 above it — inside VERIFY_HALO, so
    it is borderline and the verifier is the thing that decides.

    ⚠️ The null is matched by IDENTITY against `attribution.NULL_DOC`, never by a phrase
    copied out of it — see `test_attribution_scoring.GeomEncoder` for the reword that broke
    the copied-phrase version and why it failed as a logic bug rather than a fixture one."""

    def encode(self, texts):
        out = []
        for t in texts:
            if t == attribution.NULL_DOC: out.append([0.0, 1.0, 0.0])
            elif "Ambiguity" in t: out.append([1.0, 0.0, 0.0])
            else: out.append([0.52, 0.50, 0.6926])
        return out


def test_verifier_absent_states_which_absence():   # AC-6
    """`opted_out` (the operator switched it off) and `unavailable` (it was needed and could
    not run) are different facts. An implementation reporting the first for both looks right
    on every other test here."""
    attribution.set_projects(MID_PROJECTS)
    opted = attribution.attribute_block(["ambiguous"], {}, encoder=MidEncoder(),
                                        verifier_obj=None)
    assert opted["attribution"]["verifier"] == "opted_out", opted["attribution"]
    gone = attribution.attribute_block(["ambiguous"], {}, encoder=MidEncoder(),
                                       verifier_obj=None, verifier_absent="unavailable")
    assert gone["attribution"]["verifier"] == "unavailable", gone["attribution"]


def test_the_answer_carries_no_text_OUTSIDE_concepts():
    """Ids, confidences, closed enums and integer timings — never a project's description, never
    the caller's dims, and outside `concepts` never a word of the block. The route returns this
    dict verbatim.

    ⚠️ **THIS TEST WAS `test_the_answer_carries_no_text` AND ITS CLAIM HAS NARROWED.** `concepts`
    (2026-09-02) publishes phrases lifted from the block's own words, so the absolute form of
    this assertion is no longer true and pretending otherwise by quietly dropping the test would
    be the worst available outcome. What still holds, and is what the checks below now pin, is
    everything the narrowing does NOT touch:

      * the PROJECT side is unchanged — a description, a title and a team are still incapable of
        crossing, which is the half `named_terms` showed can hold a real person's name;
      * the caller's `dims` still cannot echo back;
      * `attribution` — the pass's report on itself — is still structurally text-free, which is
        the invariant `enrich.AttributionMeta`'s reflection tripwire enforces from the Go side;
      * block text reaches the wire ONLY through `concepts`, bounded to `MAX_WORDS` words and
        `TOP_K` entries, never as a span and never as an offset.

    The third of those is why `concepts` sits beside `attribution` rather than inside it."""
    attribution.set_projects(PROJECTS)
    out = attribution.attribute_block(["stripe webhook retries again"], {"repo": "acme-billing"},
                                      encoder=PayEncoder(), verifier_obj=StubVerifier())
    # The project side and the caller's dims: absolute, unchanged.
    for forbidden in ("acme-billing", "Stripe billing migration", "Payments"):
        assert forbidden not in json.dumps(out), forbidden
    # The meta stays structurally free of block text even though the answer beside it is not.
    assert "stripe" not in json.dumps(out["attribution"]).lower(), out["attribution"]
    # Block text crosses through `concepts` and nowhere else, within its two declared bounds.
    without_concepts = {k: v for k, v in out.items() if k != "concepts"}
    assert "stripe webhook" not in json.dumps(without_concepts), without_concepts
    assert len(out["concepts"]) <= concepts_mod.TOP_K
    for c in out["concepts"]:
        assert len(c["value"].split()) <= concepts_mod.MAX_WORDS, c
        assert set(c) == {"value", "score"}, c   # no span, no offset, no position


# --- the route ------------------------------------------------------------------------------

_TMP = tempfile.mkdtemp(prefix="keld-attribute-endpoint-")
T0 = 1_756_000_000.0
IN_SPAN = "stripe webhook retries again"
ASSISTANT = "I checked the billing dashboard for you"
OUT_OF_SPAN = "unrelated marketing copy"


def _iso(t):
    return _dt.datetime.fromtimestamp(t, _dt.timezone.utc).isoformat().replace("+00:00", "Z")


def _line(role, t, text, uid):
    return json.dumps({"type": role, "uuid": uid, "timestamp": _iso(t),
                       "message": {"role": role, "content": [{"type": "text", "text": text}]}},
                      separators=(",", ":"))


def _transcript(name="fixture-attr"):
    path = os.path.join(_TMP, name + ".jsonl")
    with open(path, "w") as fh:
        fh.write(_line("user", T0 + 60, IN_SPAN, "u1") + "\n")
        fh.write(_line("assistant", T0 + 70, ASSISTANT, "a1") + "\n")
        fh.write(_line("user", T0 + 3600, OUT_OF_SPAN, "u2") + "\n")
    return path


class FakeChild:
    """The PRODUCTION encoder's shape, which is not `score_block`'s: `textembed.Encoder.encode`
    returns `(vectors, status)` and carries a `state`. The route adapts it; this fake is what
    proves the adaptation exists."""

    def __init__(self, state="ready", status="ok"):
        self.state, self._status, self.seen, self.beats = state, status, [], 0

    def encode(self, texts, on_batch=None):
        # ⚠️ The `on_batch` parameter is not optional decoration on this fake: the production
        # encoder grew it as the attribution watchdog's LIVENESS SIGNAL, and a fake without it
        # raises TypeError inside the adapter — which the warm-up thread swallows, so the only
        # symptom is an encoder that mysteriously never ran. Faithful fakes are what stopped that
        # from being a five-minute mystery twice.
        self.seen.append(list(texts))
        if on_batch is not None:
            self.beats += 1
            on_batch(1, 1, 0.0)
        if self._status != "ok":
            return [], self._status
        return [[1.0, 0.0] if ("Payments" in t or "stripe" in t) else [0.0, 1.0]
                for t in texts], "ok"

    def kill_child(self):
        return False


class FakeSource:
    def __init__(self, child):
        self.encoder = child


def _reset_queue(m):
    """A fresh attribution queue for one test, with the real drain thread disarmed.

    Two things, both necessary. The queue is process state that deliberately survives a request —
    that is its whole job — so tests have to reset it or one test's finished block is the next
    test's `done`. And `_ensure_attrib_worker` starts a REAL background thread that would take
    the job the instant the route submits it: the test would then be racing the worker for its
    own fixture, which shows up as a drain that finds nothing (and, worse, sometimes finds it).
    These tests drive `_attribute_job` directly for determinism; `test_attribqueue` is where the
    queue's own concurrency rules are pinned."""
    from app.analysis import attribqueue
    m._ATTRIB_QUEUE = attribqueue.Queue()
    m._ensure_attrib_worker = lambda: None
    return m._ATTRIB_QUEUE


def _drain(m, expect=1):
    """Run the queued jobs the way the worker thread would, synchronously.

    Deliberately drives `_attribute_job` rather than starting the real thread: the ordering and
    watchdog rules are `test_attribqueue`'s subject, and what these tests need is a determinstic
    "the encode happened" without a sleep loop in the middle of an assertion."""
    q = m._ATTRIB_QUEUE
    ran = 0
    while True:
        job = q.take()
        if job is None:
            break
        result = m._attribute_job(job, q)
        if result is not None:
            q.finish(job.key, result)
        ran += 1
    assert ran == expect, f"drained {ran} jobs, expected {expect}"
    return q


def _main():
    import app.main as m
    return m


def _call(m, **kw):
    return asyncio.run(m.attribute(m.AttributeIn(**kw)))


def _with_roots(path):
    os.environ["KELD_ANALYZE_ROOTS"] = os.path.dirname(path)
    os.environ["KELD_TEXTEMBED"] = "1"
    os.environ["KELD_TEXTEMBED_DIR"] = _TMP      # a real directory: "weights are provisioned"
    _reset_queue(_main())


def test_the_route_is_confined_to_the_analyze_roots():
    """⚠️ The sidecar has NO auth and this route OPENS A TRANSCRIPT as the daemon's user. 403,
    not 404: a rejected path and an unresolvable one are different facts. First statements of
    the handler, before the projects check — so a machine with no projects still refuses."""
    m = _main()
    path = _transcript()
    attribution.set_projects([])
    os.environ["KELD_ANALYZE_ROOTS"] = os.path.join(_TMP, "elsewhere")
    try:
        _call(m, path=path, start=T0, end=T0 + 600)
        raise AssertionError("a path outside the roots must be refused")
    except m.HTTPException as exc:
        assert exc.status_code == 403, exc.status_code


def test_no_projects_answers_without_opening_the_transcript():
    m = _main()
    path = _transcript()
    _with_roots(path)
    attribution.set_projects([])
    out = _call(m, path=os.path.join(os.path.dirname(path), "does-not-exist.jsonl"),
                start=T0, end=T0 + 600)
    assert out["status"] == "skipped:no_projects" and out["projects"] == []


def test_the_route_reads_both_streams_inside_the_span_and_adapts_the_real_encoder_shape():
    """AC-3 end to end without a model: BOTH streams, user first, `[start, end)` only, and the
    production `(vectors, status)` encoder adapted to what `score_block` takes.

    ⚠️ Until 2026-09-03 this test asserted the encoder saw the USER turn ALONE, pinning the rule
    `_span_texts` has since reversed on measurement (28% of real blocks right on user text,
    92% on the whole block — see its docstring). What is pinned now: the assistant turn IS
    encoded, it comes AFTER the user turn (so `tvecs[:len(user)]` is the user slice that
    `concepts` reads), and nothing outside the span is read from either stream."""
    m = _main()
    path = _transcript()
    _with_roots(path)
    attribution.set_projects(PROJECTS)
    child = FakeChild()
    was = m._text_source
    m._text_source = lambda: FakeSource(child)
    try:
        # ⚠️ TWO CALLS, because the encode is no longer inside the request: the first hands the
        # block to the queue and answers `pending`, the drain does the work, the second collects
        # the answer. Everything this test pins about WHAT is encoded is unchanged by that.
        queued = _call(m, path=path, session_id="s1", start=T0, end=T0 + 600,
                       dims={"repo": "acme-billing"})
        assert queued["status"] == "pending", queued
        _drain(m)
        out = _call(m, path=path, session_id="s1", start=T0, end=T0 + 600,
                    dims={"repo": "acme-billing"})
    finally:
        m._text_source = was
    assert out["status"] == "attributed", out
    assert [p["id"] for p in out["projects"]] == ["proj_pay"], out
    assert out["attribution"]["encoder_state"] == "warm"
    # The block batch is the in-span text of BOTH streams, user first; nothing outside the span.
    assert [IN_SPAN, ASSISTANT] in child.seen, child.seen
    for batch in child.seen:
        for text in batch:
            assert OUT_OF_SPAN not in text, batch
    # Coordinates in, ids out.
    blob = json.dumps(out)
    for forbidden in (IN_SPAN, ASSISTANT, OUT_OF_SPAN, "acme-billing", path):
        assert forbidden not in blob, forbidden


def test_a_cold_encoder_answers_pending_and_warms_off_the_request():
    """The daemon's sweep IS the retry loop — this route never blocks on a model load and never
    builds a queue of its own. But it must not answer `pending` forever either, so the child is
    brought up on a background thread, filling the project-vector cache while it is at it."""
    m = _main()
    path = _transcript()
    _with_roots(path)
    # A project list no other test has embedded, so the warm-up has real work to do: the vector
    # cache is per content hash and a warm one would make this test pass on nothing.
    attribution.set_projects([dict(PROJECTS[0], id="proj_cold", title="Cold Start")])
    child = FakeChild(state="down")
    was = m._text_source
    m._text_source = lambda: FakeSource(child)
    try:
        out = _call(m, path=path, start=T0, end=T0 + 600, dims={})
        warm = m._WARM_THREAD
    finally:
        m._text_source = was
    # `concepts` is present-and-empty rather than absent, for the same reason `projects` is: a
    # consumer must never have to tell "no concepts" apart from "this answer shape predates them".
    assert out == {"status": "pending", "projects": [], "concepts": [],
                   "attribution": None}, out
    assert warm is not None
    warm.join(10)
    assert child.seen, "the cold path must bring the encoder up off the request"


def test_an_encoder_that_cannot_answer_is_pending_not_a_wrong_answer():
    """A spawn that failed or a child that hung is `degraded:encoder_unavailable` inside
    textembed, which is transient and retried on a cooldown — so the block is `pending`, never
    a confident answer produced without the encoder."""
    m = _main()
    path = _transcript()
    _with_roots(path)
    attribution.set_projects(PROJECTS)
    child = FakeChild(state="ready", status="degraded:encoder_unavailable")
    was = m._text_source
    m._text_source = lambda: FakeSource(child)
    try:
        assert _call(m, path=path, start=T0, end=T0 + 600, dims={})["status"] == "pending"
        _drain(m)
        out = _call(m, path=path, start=T0, end=T0 + 600, dims={})
    finally:
        m._text_source = was
    # `pending` from the WORKER, not merely from the hand-over: the encode ran, the encoder could
    # not answer, and the block is left for a later sweep rather than given a confident answer.
    assert out["status"] == "pending" and out["projects"] == [], out
    assert child.seen, "the worker never reached the encoder"


def test_absent_weights_state_degraded_and_attribute_nothing():   # AC-4, amended
    m = _main()
    path = _transcript()
    _with_roots(path)
    os.environ["KELD_TEXTEMBED_DIR"] = os.path.join(_TMP, "no-such-weights")
    attribution.set_projects(PROJECTS)
    was = m._text_source
    m._text_source = lambda: FakeSource(FakeChild())
    try:
        out = _call(m, path=path, start=T0, end=T0 + 600, dims={"repo": "acme-billing"})
    finally:
        m._text_source = was
        os.environ["KELD_TEXTEMBED_DIR"] = _TMP
    assert out["status"] == "degraded:weights_unavailable", out
    assert out["projects"] == [] and out["attribution"]["encoder_state"] == "absent"


def test_the_encoder_being_switched_off_is_stated():   # AC-6
    m = _main()
    path = _transcript()
    _with_roots(path)
    os.environ["KELD_TEXTEMBED"] = "0"
    attribution.set_projects(PROJECTS)
    try:
        out = _call(m, path=path, start=T0, end=T0 + 600, dims={})
    finally:
        os.environ["KELD_TEXTEMBED"] = "1"
    assert out["status"] == "skipped:disabled" and out["projects"] == [], out


BAND_PROJECTS = [{"id": "proj_band", "title": "Borderline", "team": "Eng",
                  "description": "Work that lands in the band.", "repos": [], "keywords": []}]


class BandChild(FakeChild):
    """Production shape, engineered onto the rank-and-margin rule's halo: the block text
    scores 0.52 against the project and 0.50 against NULL_DOC, so the cut is
    max(null, top - MARGIN) = 0.50 and the pair sits 0.02 above it — inside VERIFY_HALO
    (0.04), so the verifier decides, while the cut alone would still say yes. That pair is
    what separates a verdict from a fallback.

    ⚠️ Null matched by IDENTITY, not by a copied phrase — see `MidEncoder` above."""

    def encode(self, texts, on_batch=None):
        self.seen.append(list(texts))
        if on_batch is not None:
            self.beats += 1
            on_batch(1, 1, 0.0)
        out = []
        for t in texts:
            if t == attribution.NULL_DOC: out.append([0.0, 1.0, 0.0])
            elif "Borderline" in t: out.append([1.0, 0.0, 0.0])
            else: out.append([0.52, 0.50, 0.6926])
        return out, "ok"


def test_the_verifier_is_called_once_per_borderline_project():   # AC-5
    """The one genuine inference on this route. `_verify_call` is the seam through which every
    verdict rides its own dedicated worker child (`_WorkerVerifier` -> `_verifier_manager()` ->
    a `WorkerManager` distinct from GLiNER2's — see `worker.py`/`worker_manager.py`); stubbing
    it here pins the CALL CONTRACT (one call per borderline project, verdict wins the
    threshold) without spawning a real child or loading a real model."""
    m = _main()
    path = _transcript()
    _with_roots(path)
    attribution.set_projects(BAND_PROJECTS)
    # The verifier is OFF by default (2026-09-03); this test is ABOUT the verifier path, so it
    # opts in the way an operator would. Without this the route answers `opted_out` and
    # `_verify_call` is never reached — which would pass the "called once" check vacuously.
    os.environ["KELD_ATTRIBUTION_VERIFIER"] = "1"
    calls = []
    was_source, was_verify = m._text_source, m._verify_call
    m._text_source = lambda: FakeSource(BandChild())
    m._verify_call = lambda text, dims, project: (calls.append(project["id"]), (True, 0.02))[1]
    # A file where the GGUF would be: `weights_path()` stats it, and `_verify_call` — the only
    # thing that would ever open it — is stubbed, so nothing loads a model here.
    os.environ["KELD_VERIFIER_GGUF"] = path

    try:
        assert asyncio.run(m.attribute(m.AttributeIn(
            path=path, start=T0, end=T0 + 600, dims={})))["status"] == "pending"
        _drain(m)
        out = asyncio.run(m.attribute(m.AttributeIn(path=path, start=T0, end=T0 + 600, dims={})))
    finally:
        m._text_source, m._verify_call = was_source, was_verify
        os.environ.pop("KELD_VERIFIER_GGUF", None)
    assert calls == ["proj_band"], calls
    assert [p["id"] for p in out["projects"]] == ["proj_band"], out
    assert out["projects"][0]["source"] == "verifier", out
    meta = out["attribution"]
    assert meta["verifier"] == "used" and meta["pairs_verified"] == 1, meta
    assert meta["verify_ms"] == 20, meta


def test_a_verifier_that_cannot_load_degrades_and_says_so():   # AC-6
    """A borderline pair the verifier cannot judge falls back to the threshold — and the meta
    names it `unavailable`, never `opted_out`. A narrower decision must not look like the full
    one. `_verify_call` stands in for the worker-child call here too — a `_VerifierUnavailable`
    is exactly what it raises for real when the child fails to start or dies mid-job (see
    `_verify_call`'s docstring in main.py), so this pins the degrade path without needing a
    child that actually crashes."""
    m = _main()
    path = _transcript()
    _with_roots(path)
    attribution.set_projects(BAND_PROJECTS)
    was_source, was_verify = m._text_source, m._verify_call
    m._text_source = lambda: FakeSource(BandChild())
    os.environ["KELD_ATTRIBUTION_VERIFIER"] = "1"   # default is OFF; this test needs it wanted
    os.environ["KELD_VERIFIER_GGUF"] = path

    def boom(text, dims, project):
        raise m._VerifierUnavailable()

    m._verify_call = boom

    try:
        assert asyncio.run(m.attribute(m.AttributeIn(
            path=path, start=T0, end=T0 + 600, dims={})))["status"] == "pending"
        _drain(m)
        out = asyncio.run(m.attribute(m.AttributeIn(path=path, start=T0, end=T0 + 600, dims={})))
    finally:
        m._text_source, m._verify_call = was_source, was_verify
        os.environ.pop("KELD_VERIFIER_GGUF", None)
    assert out["status"] == "attributed", out
    assert out["attribution"]["verifier"] == "unavailable", out["attribution"]
    # The threshold still decided, so the pair is attributed by the embedding path.
    assert [p["id"] for p in out["projects"]] == ["proj_band"], out


def test_an_unreadable_transcript_is_refused_rather_than_answered():
    m = _main()
    path = _transcript()
    _with_roots(path)
    attribution.set_projects(PROJECTS)
    was = m._text_source
    m._text_source = lambda: FakeSource(FakeChild())
    try:
        _call(m, path=os.path.join(os.path.dirname(path), "gone.jsonl"),
              start=T0, end=T0 + 600, dims={})
        raise AssertionError("an unreadable transcript must not answer with an attribution")
    except m.HTTPException as exc:
        # ⚠️ 410, AND THE EXACT CODE IS LOAD-BEARING — this assertion said 404 and that
        # was half of a two-sided defect. The Go client reads 404 from this route as
        # "this sidecar has no /attribute route" (version skew) and HOLDS the job
        # forever with no attempt consumed. Under 404 a deleted or rotated transcript
        # inherited that: one permanently-resident job per affected block, re-POSTed
        # every sweep, competing for the 24-job sweep budget with no bound.
        #
        # Note how it hid: this test asserted the sidecar's status against a fake
        # daemon, attrib_test.go asserted the daemon's behaviour against a fake
        # sidecar, and nothing compared the two. The cross-language pin is
        # TestTheAttributeRouteNeverAnswers404ForAnythingButAMissingRoute
        # (internal/agent/enrich/sidecar) — it reads this route's source and drives
        # every status it raises through the real client. Change this number and that
        # test is where it is caught.
        assert exc.status_code == 410, exc.status_code
    finally:
        m._text_source = was


# --- the hand-over: the encode is no longer inside the request -------------------------------

def test_the_route_answers_pending_immediately_while_the_encoder_is_busy():
    """T15. THE test for this design.

    The encode used to run inside the request, and the daemon bounds that request at two
    minutes — so a block that took longer was abandoned mid-encode, its answer discarded, and
    the job re-queued to be encoded from scratch behind the encode nobody had cancelled.
    Measured on a real machine: 79 spooled jobs, an encoder pinned at 194% CPU for 1h48m, two
    blocks attributed in fifteen minutes.

    A child that would block for a very long time stands in for that here. If the route still
    awaited the encode this test would hang rather than fail, which is the honest shape of the
    bug: the caller waits, and waiting is exactly what it must never do."""
    m = _main()
    path = _transcript()
    _with_roots(path)
    attribution.set_projects(PROJECTS)

    class NeverAnswers(FakeChild):
        def encode(self, texts, on_batch=None):
            raise AssertionError("the request path must not reach the encoder")

    was = m._text_source
    m._text_source = lambda: FakeSource(NeverAnswers())
    try:
        started = time.monotonic()
        out = _call(m, path=path, session_id="s1", start=T0, end=T0 + 600, dims={})
        elapsed = time.monotonic() - started
    finally:
        m._text_source = was
    assert out["status"] == "pending", out
    assert elapsed < 1.0, f"the request path waited {elapsed:.1f}s"
    assert m._ATTRIB_QUEUE.stats()["waiting"] == 1, m._ATTRIB_QUEUE.stats()


def test_re_asking_for_a_queued_block_does_not_read_the_transcript_again():
    """T18. The daemon re-POSTs every pending block on every 45s sweep, up to 24 of them. If
    each of those re-read its transcript, a queued backlog would cost two dozen file scans a
    minute to answer a question this process already knows the answer to — and on a 90 MB
    transcript that is not free. The queue is therefore consulted BEFORE the span is read."""
    m = _main()
    path = _transcript()
    _with_roots(path)
    attribution.set_projects(PROJECTS)
    was_source, was_span = m._text_source, m._span_texts
    reads = []
    m._text_source = lambda: FakeSource(FakeChild())
    m._span_texts = lambda p, a, b: (reads.append(p), was_span(p, a, b))[1]
    try:
        assert _call(m, path=path, session_id="s1", start=T0, end=T0 + 600)["status"] == "pending"
        assert len(reads) == 1, reads
        for _ in range(10):          # ten sweeps' worth of re-asking
            assert _call(m, path=path, session_id="s1", start=T0,
                         end=T0 + 600)["status"] == "pending"
        assert len(reads) == 1, f"the transcript was re-read {len(reads)} times"
        assert m._ATTRIB_QUEUE.counts["submitted"] == 1, m._ATTRIB_QUEUE.counts
    finally:
        m._text_source, m._span_texts = was_source, was_span


def test_the_stored_answer_is_returned_on_a_later_call_and_only_once():
    """T17/T3 at the route. The daemon collects on its next sweep; a second collect must find
    nothing, because the daemon has deleted its durable job by then and a re-delivery could only
    ever belong to a block that no longer exists."""
    m = _main()
    path = _transcript()
    _with_roots(path)
    attribution.set_projects(PROJECTS)
    was = m._text_source
    m._text_source = lambda: FakeSource(FakeChild())
    try:
        _call(m, path=path, session_id="s1", start=T0, end=T0 + 600, dims={"repo": "acme-billing"})
        _drain(m)
        first = _call(m, path=path, session_id="s1", start=T0, end=T0 + 600,
                      dims={"repo": "acme-billing"})
        assert first["status"] == "attributed", first
        assert [p["id"] for p in first["projects"]] == ["proj_pay"], first
        assert first["attribution"]["embed_ms"] >= 0, first["attribution"]
        # Asking again re-queues it as a NEW block rather than replaying the old answer.
        second = _call(m, path=path, session_id="s1", start=T0, end=T0 + 600,
                       dims={"repo": "acme-billing"})
        assert second["status"] == "pending", second
    finally:
        m._text_source = was


def test_the_worker_beats_once_per_batch_so_the_watchdog_can_see_it():
    """The wiring between the two halves: the queue's heartbeat is only fed because the worker
    hands `Encoder.encode` a callback. Nothing else connects them, and a silent worker would be
    killed after one window however healthy it was."""
    m = _main()
    path = _transcript()
    _with_roots(path)
    attribution.set_projects(PROJECTS)
    child = FakeChild()
    was = m._text_source
    m._text_source = lambda: FakeSource(child)
    try:
        _call(m, path=path, session_id="s1", start=T0, end=T0 + 600)
        _drain(m)
    finally:
        m._text_source = was
    assert child.beats >= 1, "the worker never fed the heartbeat"


def test_the_terminal_statuses_are_still_answered_without_the_queue():
    """T16. Every decision that needs no encoder must still be made on the request path. If the
    queue swallowed one of these the daemon would be told `pending` about a block that can never
    move, and would ask again forever."""
    m = _main()
    path = _transcript()

    # no projects declared
    _with_roots(path)
    attribution.set_projects([])
    out = _call(m, path=path, session_id="s1", start=T0, end=T0 + 600)
    assert out["status"] == "skipped:no_projects", out
    assert m._ATTRIB_QUEUE.stats()["waiting"] == 0, "a decidable block was queued"

    # the text encoder is switched off on this machine
    _with_roots(path)
    attribution.set_projects(PROJECTS)
    was = m._text_source
    m._text_source = lambda: None
    try:
        out = _call(m, path=path, session_id="s1", start=T0, end=T0 + 600)
    finally:
        m._text_source = was
    assert out["status"] == "skipped:disabled", out
    assert m._ATTRIB_QUEUE.stats()["waiting"] == 0, "a decidable block was queued"

    # the weights are not provisioned
    _with_roots(path)
    os.environ["KELD_TEXTEMBED_DIR"] = os.path.join(_TMP, "no-such-weights")
    m._text_source = lambda: FakeSource(FakeChild())
    try:
        out = _call(m, path=path, session_id="s1", start=T0, end=T0 + 600)
    finally:
        m._text_source = was
        os.environ["KELD_TEXTEMBED_DIR"] = _TMP
    assert out["status"] == "degraded:weights_unavailable", out
    assert m._ATTRIB_QUEUE.stats()["waiting"] == 0, "a decidable block was queued"


def test_a_quarantined_block_stops_being_pending():
    """Four genuine failures retire a block. Answering `pending` after that would have the
    daemon re-POST it on every sweep forever — a job that can never finish and can never be
    given up on, which is the leak `pending` is otherwise safe from."""
    m = _main()
    path = _transcript()
    _with_roots(path)
    attribution.set_projects(PROJECTS)
    q = m._ATTRIB_QUEUE
    was = m._text_source
    m._text_source = lambda: FakeSource(FakeChild())
    try:
        _call(m, path=path, session_id="s1", start=T0, end=T0 + 600)
        for _ in range(4):                       # four heartbeat kills
            job = q.take()
            q.fail(job.key, "heartbeat")
        out = _call(m, path=path, session_id="s1", start=T0, end=T0 + 600)
    finally:
        m._text_source = was
    assert out["status"] == "degraded:weights_unavailable", out
    assert q.stats()["quarantined"] == 1, q.stats()


def test_the_metrics_block_appears_only_once_a_queue_exists():
    """/metrics must describe this subsystem without CREATING it: a machine with attribution off
    reports the absence, exactly as `verifier` reports `built: false`."""
    m = _main()
    was, m._ATTRIB_QUEUE = m._ATTRIB_QUEUE, None
    try:
        assert m._attribution_stats() is None
    finally:
        m._ATTRIB_QUEUE = was
    q = _reset_queue(m)
    q.submit(__import__("app.analysis.attribqueue", fromlist=["x"]).Job("s@1", "/t", 1.0, 2.0))
    stats = m._attribution_stats()
    assert stats["waiting"] == 1 and stats["running"] is False, stats
    assert "heartbeat_timeout_s" in stats and "counts" in stats, stats


if __name__ == "__main__":
    fns = [(n, f) for n, f in sorted(globals().items()) if n.startswith("test_")]
    for name, fn in fns:
        fn()
    print(f"test_attribution_endpoint: {len(fns)} passed")
