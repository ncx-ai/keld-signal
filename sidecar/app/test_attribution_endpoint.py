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


def test_a_span_with_no_user_text_is_terminal_not_pending():
    """A closed block can hold no human words at all (a long autonomous run). The encoder has
    nothing to embed and no later sweep can change that, so the answer is the benchmarked
    path's own empty answer — never `pending`, which would have the daemon retry a block that
    can never move."""
    attribution.set_projects(PROJECTS)
    out = attribution.attribute_block([], {"repo": "acme-billing"},
                                      encoder=PayEncoder(), verifier_obj=None)
    assert out["status"] == "attributed" and out["projects"] == []
    assert out["attribution"]["encoder_state"] == "warm"
    assert out["attribution"]["verifier"] == "not_needed"


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
        self.state, self._status, self.seen = state, status, []

    def encode(self, texts):
        self.seen.append(list(texts))
        if self._status != "ok":
            return [], self._status
        return [[1.0, 0.0] if ("Payments" in t or "stripe" in t) else [0.0, 1.0]
                for t in texts], "ok"


class FakeSource:
    def __init__(self, child):
        self.encoder = child


def _main():
    import app.main as m
    return m


def _call(m, **kw):
    return asyncio.run(m.attribute(m.AttributeIn(**kw)))


def _with_roots(path):
    os.environ["KELD_ANALYZE_ROOTS"] = os.path.dirname(path)
    os.environ["KELD_TEXTEMBED"] = "1"
    os.environ["KELD_TEXTEMBED_DIR"] = _TMP      # a real directory: "weights are provisioned"


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


def test_the_route_reads_user_turns_inside_the_span_and_adapts_the_real_encoder_shape():
    """AC-3 end to end without a model: user stream only, `[start, end)` only, and the
    production `(vectors, status)` encoder adapted to what `score_block` takes."""
    m = _main()
    path = _transcript()
    _with_roots(path)
    attribution.set_projects(PROJECTS)
    child = FakeChild()
    was = m._text_source
    m._text_source = lambda: FakeSource(child)
    try:
        out = _call(m, path=path, session_id="s1", start=T0, end=T0 + 600,
                    dims={"repo": "acme-billing"})
    finally:
        m._text_source = was
    assert out["status"] == "attributed", out
    assert [p["id"] for p in out["projects"]] == ["proj_pay"], out
    assert out["attribution"]["encoder_state"] == "warm"
    # The block batch is exactly the in-span USER text: no assistant turn, nothing outside.
    assert [IN_SPAN] in child.seen, child.seen
    for batch in child.seen:
        for text in batch:
            assert ASSISTANT not in text and OUT_OF_SPAN not in text, batch
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
        out = _call(m, path=path, start=T0, end=T0 + 600, dims={})
    finally:
        m._text_source = was
    assert out["status"] == "pending" and out["projects"] == [], out


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

    def encode(self, texts):
        self.seen.append(list(texts))
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


if __name__ == "__main__":
    fns = [(n, f) for n, f in sorted(globals().items()) if n.startswith("test_")]
    for name, fn in fns:
        fn()
    print(f"test_attribution_endpoint: {len(fns)} passed")
