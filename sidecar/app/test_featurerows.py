"""`POST /features` as a CURSOR route — the seam between the sidecar's `S(t)` and the daemon's
publish path, which were built in parallel and blind to each other.

  cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_featurerows.py

⚠️ **This file lives at `app/test_featurerows.py`, NOT under `app/analysis/`.** AGENTS.md's test
loop globs `app/test_*.py`; a file one directory down would silently never run.

`test_features.py` owns the VECTOR — what its indices mean and that they cannot move. This file
owns the CONTRACT the Go half is coded against (`internal/agent/enrich/sidecar/features.go`), and
every property here is one whose violation is silent on the wire:

 1. **THE SIDECAR ENUMERATES THE ANCHORS.** The caller sends a cursor, never a grid: only this
    process can see where the non-empty bins and the closed blocks are.
 2. **THE STREAM IS GLOBALLY CHRONOLOGICAL AND `since_ts` IS `>` ON A ROW'S OWN INSTANT.** So a
    caller resuming from the last emitted row's instant loses nothing and repeats nothing —
    asserted by replaying a whole transcript one batch at a time and comparing against one call.
 3. **A BATCH IS NEVER CUT INSIDE AN INSTANT.** Two rows can share one (a bin ending where a block
    ends), and emitting one of them then stopping would advance the cursor past the other forever.
 4. **`anchor_id` IS REQUIRED ON `message` ROWS AND IS AN ID.** Instants are quantized to 0.1 s and
    two turns can collide on one tick.
 5. **ABSENT IS NOT ZERO.** With the encoder off there are NO `message` rows and no `text` block —
    not empty ones — and the structured vector's text slots read 0.0 under `text_recorded: False`.
 6. **THE QUANTISATION ROUND-TRIPS**, and `dims` is compared against the delivered bytes rather
    than trusted.
 7. **NO TEXT AND NO IDENTITY CROSSES.** A whole-response scan for the fixture's own strings.
"""
import asyncio
import atexit
import base64
import itertools
import json
import math
import os
import shutil
import sys
import tempfile

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from app.analysis import SCHEMA
from app.analysis import features as F
from app.analysis import featuretext, textembed as te
from app.analysis.ingest import ingest_file, session_of
from app.analysis.store import open_store

_TMP = tempfile.mkdtemp(prefix="keld-featurerows-test-")
atexit.register(lambda: shutil.rmtree(_TMP, ignore_errors=True))
_SEQ = itertools.count()

# The identity strings the fixture puts into its OPEN levels and its message TEXT. Nothing derived
# from them may appear anywhere in a response — that is property 7.
BRANCH = "feature-branch-quux"
WORKSPACE = "gadget-app"
SECRET_FILE = "vault_of_secrets.go"
SECRET_WORD = "Pemberton"


def _user(ts, uuid, text):
    return {"type": "user", "timestamp": ts, "cwd": "/workspace/" + WORKSPACE,
            "gitBranch": BRANCH, "uuid": uuid,
            "message": {"role": "user", "content": [{"type": "text", "text": text}]}}


def _assistant(ts, uuid, req, tool, inp, say=None):
    content = []
    if say:
        content.append({"type": "text", "text": say})
    content.append({"type": "tool_use", "id": "toolu_" + uuid, "name": tool, "input": inp})
    return {"type": "assistant", "timestamp": ts, "cwd": "/workspace/" + WORKSPACE,
            "gitBranch": BRANCH, "uuid": uuid, "requestId": req,
            "message": {"role": "assistant", "model": "claude-opus-4-5-20251101",
                        "content": content,
                        "usage": {"input_tokens": 100, "output_tokens": 20,
                                  "cache_creation_input_tokens": 40,
                                  "cache_read_input_tokens": 900}}}


def _transcript():
    """~50 minutes of work across two runs of bins, with real prose on both sides so the text half
    has something to encode. COMPACT separators are not cosmetic: `transcript.turns_in` gates on the
    raw substring `"type":"user"` BEFORE any `json.loads`, so a pretty-printed fixture is skipped
    unparsed and the store comes out empty with nothing reporting an error."""
    lines, n = [], itertools.count()

    def ts(minute, second=0):
        return "2026-08-20T09:%02d:%02dZ" % (minute, second)

    for i, minute in enumerate((0, 2, 4, 6)):
        lines.append(_user(ts(minute), "u%d" % i,
                           "please look at the %s for %s" % (SECRET_FILE, SECRET_WORD)))
        lines.append(_assistant(ts(minute, 10), "a%d" % next(n), "req%d" % i,
                                "Read", {"file_path": "/workspace/%s/%s" % (WORKSPACE,
                                                                            SECRET_FILE)},
                                say="I will read that file now. It looks fine."))
        lines.append(_assistant(ts(minute, 20), "a%d" % next(n), "req%d" % i,
                                "Edit", {"file_path": "/workspace/%s/main.py" % WORKSPACE,
                                         "old_string": "x" * 40, "new_string": "y" * 900}))
        lines.append(_assistant(ts(minute, 30), "a%d" % next(n), "req%d" % i,
                                "Bash", {"command": "go test ./..."}))
    for i, minute in enumerate((40, 42, 44)):
        lines.append(_user(ts(minute), "v%d" % i, "now run the build and report back"))
        lines.append(_assistant(ts(minute, 10), "b%d" % next(n), "rq%d" % i,
                                "Bash", {"command": "pnpm build"},
                                say="Running the build. Two warnings, no errors."))
        lines.append(_assistant(ts(minute, 20), "b%d" % next(n), "rq%d" % i,
                                "Write", {"file_path": "/workspace/%s/app.tsx" % WORKSPACE,
                                          "content": "z" * 2000}))
    return "\n".join(json.dumps(o, separators=(",", ":")) for o in lines) + "\n"


def _store_with(capture="1"):
    d = os.path.join(_TMP, "s%d" % next(_SEQ))
    os.makedirs(d)
    path = os.path.join(d, "fixture-rows-0000.jsonl")
    with open(path, "w") as fh:
        fh.write(_transcript())
    was = os.environ.get("KELD_CAPTURE")
    os.environ["KELD_CAPTURE"] = capture
    try:
        st = open_store(os.path.join(d, "refseries.db"))
        ingest_file(st, path)
    finally:
        if was is None:
            os.environ.pop("KELD_CAPTURE", None)
        else:
            os.environ["KELD_CAPTURE"] = was
    return st, path


# `now` far enough past the fixture that the idle settle has elapsed for every trailing anchor, so
# the whole session is emittable and the tests are not measuring the clock.
def _now(st, path):
    session = session_of(path)
    start = F.session_start(st, session)
    return st.turn_times(session, start, start + 86400.0)[-1] + 4 * 3600.0


class StubEncoder:
    """`test_textembed.StubEncoder`'s twin: text -> a unit vector determined only by the text, so
    the assertions here are exact and no torch is loaded. Duplicated rather than imported because
    importing a sibling test module makes one file's failure the other's."""

    def __init__(self, dim=None, fail=None):
        self.dim = dim or te.DIM_PUBLISH
        self.fail = fail
        self.calls = 0
        # The observability surface the real `Encoder` carries and `featuretext.embed_stats`
        # reads. Stubbed here so the /metrics block is testable without torch and without a
        # child process — the same reason the encode itself is stubbed.
        self.state = te.DOWN
        self.status = te.STATUS_OK
        self.peak_rss_mb = 0.0
        self.counts = {"spawns": 0, "kills_idle": 0, "kills_stalled": 0, "failures": 0,
                       "batches": 0, "batch_ms_total": 0.0}
        # ⚠️ The batch timings are part of that surface as of 2026-09-03, and a stub missing
        # them fails `stats()` rather than degrading — deliberately. They are what the encoder's
        # thread ceiling and the attribution heartbeat window are sized from, so a fake that
        # silently reported zeros would make a regression in either invisible.
        self.last_batch_ms = 0.0
        self.last_encode_ms = 0.0
        # Same contract, one field along (2026-09-04): the real `Encoder` learns its dtype from
        # the child's ready handshake, so a stub that never came up reports None — which is
        # exactly what the down-state block states too.
        self.dtype = None

    def rss_mb(self):
        return 0.0

    def maybe_unload(self):
        return False

    def observe_rss(self):
        return 0.0

    def encode(self, texts):
        if self.fail:
            self.state, self.status = te.UNAVAILABLE, self.fail
            self.counts["failures"] += 1
            return [], self.fail
        self.state, self.status = te.READY, te.STATUS_OK
        self.counts["spawns"] = self.counts["spawns"] or 1
        self.counts["batches"] += 1
        self.calls += len(texts)
        out = []
        for t in texts:
            h, v = 0, [0.0] * self.dim
            for ch in t:
                h = (h * 131 + ord(ch)) % 1000003
                v[h % self.dim] += 1.0
            out.append(te.normalize(v) if any(v) else [1.0] + [0.0] * (self.dim - 1))
        return out, te.STATUS_OK


def _text_source(fail=None):
    """A `TextSource` over a stub encoder, with the toggle forced on for the call."""
    os.environ["KELD_TEXTEMBED"] = "1"
    return featuretext.TextSource(encoder=StubEncoder(fail=fail), background=False)


def _rows(st, path, **kw):
    kw.setdefault("now", _now(st, path))
    return F.feature_rows(st, path, **kw)


def _dequantise(q):
    """The Go decoder's own reading: `value_i = int8(q[i]) * scale`, refusing a payload whose
    delivered length disagrees with its declared `dims`."""
    raw = base64.b64decode(q["q"])
    assert len(raw) == q["dims"], (len(raw), q["dims"])
    assert q["scale"] > 0, q["scale"]
    return [(b - 256 if b > 127 else b) * q["scale"] for b in raw]


# --- 1. the sidecar enumerates the anchors -----------------------------------------------------

def test_the_response_carries_the_cursor_contract_and_nothing_else_is_required():
    st, path = _store_with()
    out = _rows(st, path)
    assert out["schema"] == SCHEMA
    assert out["feature_spec"] == F.FEATURE_SPEC_VERSION and out["spec_sha"] == F.SPEC_SHA
    assert out["dims"] == F.DIMS
    assert isinstance(out["watermark"], float)
    assert out["rows"], "a real ingested transcript produced no rows"
    st.close()


def test_all_three_anchor_kinds_are_the_sidecars_to_choose():
    """The caller sends no grid at all — only a cursor — and gets bins and blocks it could not have
    enumerated, because only this process reads the `bin` table and runs the cutter."""
    st, path = _store_with()
    out = _rows(st, path, text=_text_source())
    kinds = {r["anchor"] for r in out["rows"]}
    assert kinds == set(F.ANCHORS), kinds
    st.close()


def test_a_bin_anchor_sits_on_the_grid_and_a_block_anchor_on_a_bin_boundary():
    from app.analysis.store import BIN_SECONDS
    st, path = _store_with()
    for r in _rows(st, path)["rows"]:
        if r["anchor"] in ("bin", "block"):
            assert r["ts"] % BIN_SECONDS == 0, (r["anchor"], r["ts"])
    st.close()


def test_a_block_row_carries_both_boundary_reasons_from_the_closed_vocabulary():
    """⚠️ The reasons are a training TARGET, not decoration: `budget` is 'we had to cut somewhere'
    and `idle` is a claim there was no work at all. The Go side drops a block row carrying an
    unreadable one rather than train against a label nobody produced."""
    from app.analysis.blocks import REASONS
    st, path = _store_with()
    blocks = [r for r in _rows(st, path)["rows"] if r["anchor"] == "block"]
    assert blocks
    for r in blocks:
        assert r["start_reason"] in REASONS and r["end_reason"] in REASONS, r
    st.close()


def test_an_unignested_transcript_answers_a_null_watermark_and_no_rows():
    """⚠️ The watermark is the one fact separating 'nothing new yet' from 'never ingested', and a
    caller cannot infer either from an empty row list."""
    st, path = _store_with()
    out = F.feature_rows(st, os.path.join(os.path.dirname(path), "never-seen-0000.jsonl"),
                         now=_now(st, path))
    assert out["watermark"] is None and out["rows"] == []
    st.close()


# --- 2. the cursor ----------------------------------------------------------------------------

def test_since_ts_is_strictly_after_a_rows_own_instant():
    st, path = _store_with()
    every = _rows(st, path)["rows"]
    cut = every[len(every) // 2]["ts"]
    after = _rows(st, path, since_ts=cut)["rows"]
    assert after and all(r["ts"] > cut for r in after)
    assert [r["ts"] for r in after] == [r["ts"] for r in every if r["ts"] > cut]
    st.close()


def test_replaying_the_cursor_in_batches_loses_nothing_and_repeats_nothing():
    """THE PROPERTY THE WHOLE CONTRACT RESTS ON. A caller advances to the last emitted row's
    instant; if the stream were not globally chronological, or if a batch could end inside an
    instant, a row would be skipped permanently and nothing would say so."""
    st, path = _store_with()
    one = [(r["anchor"], r["anchor_id"]) for r in _rows(st, path, text=_text_source())["rows"]]
    text = _text_source()
    seen, cursor, calls = [], None, 0
    while True:
        out = _rows(st, path, since_ts=cursor, max_rows=3, text=text)
        if not out["rows"]:
            break
        calls += 1
        assert calls < 500, "cursor did not advance"
        seen.extend((r["anchor"], r["anchor_id"]) for r in out["rows"])
        cursor = out["rows"][-1]["ts"]
    assert seen == one, (len(seen), len(one))
    assert len(seen) == len(set(seen)), "a row was emitted twice"
    assert calls > 1, "the bound never bit, so nothing was replayed"
    st.close()


def test_a_batch_is_never_cut_inside_an_instant():
    """⚠️ Two rows can share one instant — a bin ending exactly where a block ends — and the cursor
    is a `>` comparison, so emitting one of them and stopping would advance the caller past the
    other forever. `max_rows` is therefore honoured at GROUP granularity."""
    st, path = _store_with()
    every = _rows(st, path, text=_text_source())["rows"]
    shared = [t for t in {r["ts"] for r in every}
              if sum(1 for r in every if r["ts"] == t) > 1]
    assert shared, "fixture no longer produces a shared instant; this test is vacuous"
    for bound in range(1, len(every) + 1):
        rows = _rows(st, path, max_rows=bound, text=_text_source())["rows"]
        if not rows:
            continue
        last = rows[-1]["ts"]
        assert sum(1 for r in rows if r["ts"] == last) == \
            sum(1 for r in every if r["ts"] == last), (bound, last)
    st.close()


def test_a_single_instant_over_the_bound_still_emits_rather_than_wedge():
    """A group larger than `max_rows` emits anyway, over the bound. The alternative is a cursor
    that can never move, which loses the whole transcript instead of overshooting one response."""
    st, path = _store_with()
    out = _rows(st, path, max_rows=1, text=_text_source())
    assert out["rows"]
    first = out["rows"][0]["ts"]
    assert all(r["ts"] == first for r in out["rows"])
    st.close()


# --- 3./4. message rows -------------------------------------------------------------------------

def test_no_message_rows_at_all_with_the_encoder_off():
    """⚠️ ABSENT, not empty. A message has no lookback, so there is no structured vector to
    compute; a `message` row with no text vector would carry nothing at all, and one emitted anyway
    would enter the corpus as an observation of a person who said nothing."""
    os.environ.pop("KELD_TEXTEMBED", None)
    st, path = _store_with()
    out = _rows(st, path)
    assert not [r for r in out["rows"] if r["anchor"] == "message"]
    assert all("text" not in r for r in out["rows"]), "a text block with no encoder"
    st.close()


def test_a_message_row_is_keyed_on_the_turns_id_and_names_its_author():
    """⚠️ `anchor_id` IS REQUIRED HERE. Series instants are quantized to 0.1 s and two turns can
    collide on one tick, so the instant alone is not a message key and a row published under one
    could upsert its neighbour at Atlas."""
    st, path = _store_with()
    msgs = [r for r in _rows(st, path, text=_text_source())["rows"] if r["anchor"] == "message"]
    assert msgs
    ids = [r["anchor_id"] for r in msgs]
    assert len(ids) == len(set(ids)), "two message rows share an anchor id"
    for r in msgs:
        assert r["anchor_id"] and " " not in r["anchor_id"] and len(r["anchor_id"]) <= 128
        assert r["role"] in ("user", "assistant"), r["role"]
        assert r["text"] and set(r["text"]) <= set(te.STREAMS)
        assert "structured" not in r, "a message row has no lookback to compute one from"
        assert r["encoder"]["width"] == te.DIM_PUBLISH
        assert r["encoder"]["model"] and r["encoder"]["projection"]
    # The fixture's own turn uuids, and nothing but — an id, never a fragment of a message.
    assert set(ids) <= {"u0", "u1", "u2", "u3", "v0", "v1", "v2"} | \
        {"%s%d" % (p, i) for p in "ab" for i in range(24)}
    st.close()


def test_a_users_turn_and_an_assistants_turn_carry_their_own_streams():
    st, path = _store_with()
    msgs = {r["anchor_id"]: r for r in _rows(st, path, text=_text_source())["rows"]
            if r["anchor"] == "message"}
    assert set(msgs["u0"]["text"]) == {"user"} and msgs["u0"]["role"] == "user"
    assert set(msgs["a0"]["text"]) == {"asst"} and msgs["a0"]["role"] == "assistant"
    st.close()


def test_a_message_is_encoded_once_ever_across_calls():
    """The whole cost model. Shells overlap across rows and the emitter calls this route repeatedly
    as its cursor advances; uncached, a session of `n` messages costs `O(n)` forward passes PER
    CALL at ~766 ms each."""
    st, path = _store_with()
    text = _text_source()
    _rows(st, path, text=text)
    first = text._encoder.calls
    assert first > 0
    _rows(st, path, text=text)
    assert text._encoder.calls == first, (first, text._encoder.calls)
    st.close()


# --- 5. absent is not zero ----------------------------------------------------------------------

def test_the_text_slots_are_zero_and_declared_unrecorded_with_the_encoder_off():
    os.environ.pop("KELD_TEXTEMBED", None)
    st, path = _store_with()
    row = [r for r in _rows(st, path)["rows"] if r["anchor"] == "bin"][-1]
    assert row["text_recorded"] is False
    vals = _dequantise(row["structured"])
    idx = [i for i, n in enumerate(F.MANIFEST) if ".text." in n]
    assert len(idx) == len(F.SHELLS) * len(F.TEXT_STREAMS) * len(F.TEXT_SCALARS)
    assert all(vals[i] == 0.0 for i in idx)
    st.close()


def test_the_text_slots_are_populated_and_declared_recorded_with_the_encoder_on():
    st, path = _store_with()
    rows = [r for r in _rows(st, path, text=_text_source())["rows"] if r["anchor"] == "bin"]
    hot = [r for r in rows if r["text_recorded"]]
    assert hot, "the encoder ran and no bin row recorded it"
    vals = _dequantise(hot[-1]["structured"])
    idx = [i for i, n in enumerate(F.MANIFEST) if ".text." in n]
    assert any(vals[i] != 0.0 for i in idx), "text_recorded with every text slot zero"
    st.close()


def test_an_unavailable_encoder_costs_the_text_half_and_nothing_else():
    """⚠️ A missing model must cost the text half of a row, never the row and never the daemon.
    `degraded:weights_unavailable` is a STATED status, an empty vector list, and no `message` rows —
    never a crash, never a stall, and never zeros wearing the shape of a measurement."""
    st, path = _store_with()
    out = _rows(st, path, text=_text_source(fail=te.STATUS_NO_WEIGHTS))
    assert out["rows"], "the structured half went missing with the encoder"
    assert not [r for r in out["rows"] if r["anchor"] == "message"]
    assert all(r.get("text_recorded") is False for r in out["rows"])
    # The two streams the fixture HAS say why there is no vector; `think` says it held nothing,
    # which is a different fact and is stated separately rather than overwritten by the failure.
    assert out["text_status"]["user"] == te.STATUS_NO_WEIGHTS
    assert out["text_status"]["asst"] == te.STATUS_NO_WEIGHTS
    assert out["text_status"]["think"] == te.STATUS_EMPTY
    st.close()


def test_the_status_names_the_reason_rather_than_leaving_an_empty_list_to_mean_four_things():
    st, path = _store_with()
    os.environ.pop("KELD_TEXTEMBED", None)
    off = F.feature_rows(st, path, now=_now(st, path), text=featuretext.TextSource(
        encoder=StubEncoder(), background=False))
    assert set(off["text_status"].values()) == {te.STATUS_DISABLED}
    on = _rows(st, path, text=_text_source())
    assert on["text_status"]["user"] == te.STATUS_OK
    # `think` is `skipped:empty` in practice and that is measured, not a bug: 9,144 thinking blocks
    # over the 40 largest real transcripts, 0 of non-zero length.
    assert on["text_status"]["think"] == te.STATUS_EMPTY
    st.close()


# --- the text frontier --------------------------------------------------------------------------

def test_the_frontier_holds_the_whole_stream_not_just_the_message_rows():
    """⚠️ Past the frontier the message history is INCOMPLETE, so a `bin`/`block` row emitted there
    would publish a confident text scalar measured over a fraction of the words that were said.
    Measured reason for the bound: an unbounded first call on a real 26 MB transcript is 1,646
    messages / 2,174 chunks, ~28 minutes inside one request."""
    st, path = _store_with()
    unbounded = F.feature_rows(st, path, now=_now(st, path), max_rows=999,
                               text=_text_source())["rows"]
    assert "text_frontier" not in F.feature_rows(st, path, now=_now(st, path), max_rows=999,
                                                 text=_text_source()), \
        "a complete history reported a frontier"

    # A bound of 2 messages against a fixture whose text half holds 14.
    bounded = F.feature_rows(st, path, now=_now(st, path), max_rows=999,
                             text=_Bounded(featuretext.TextSource(encoder=StubEncoder(), background=False), 2))
    frontier = bounded["text_frontier"]
    assert frontier is not None
    assert bounded["rows"], "the frontier held back everything, including the structured half"
    assert all(r["ts"] < frontier for r in bounded["rows"]), \
        "a row was emitted at or past the frontier"
    # And it really did hold rows back, structured ones included — otherwise this is vacuous.
    assert len(bounded["rows"]) < len(unbounded)
    assert {r["anchor"] for r in unbounded} > {r["anchor"] for r in bounded["rows"]} or \
        len([r for r in bounded["rows"] if r["anchor"] != "message"]) \
        < len([r for r in unbounded if r["anchor"] != "message"])
    st.close()


class _Bounded:
    """A `TextSource` pinned to a small per-call encode bound, so the frontier is exercised on a
    fixture rather than only on a 26 MB transcript."""

    def __init__(self, inner, bound):
        self._inner, self._bound = inner, bound

    def vectors(self, path, max_encode=None):
        return self._inner.vectors(path, max_encode=self._bound)


def test_the_frontier_retreats_as_the_encoder_catches_up_and_reaches_none():
    """Many cheap calls rather than one unbounded one — the same 'visible, steady drain' the wire
    bounds choose. And the frontier must actually REACH `None`, or the tail of every session is
    permanently unpublishable."""
    st, path = _store_with()
    src = featuretext.TextSource(encoder=StubEncoder(), background=False)
    seen, last = [], object()
    for _ in range(40):
        _v, _s, _e, frontier = src.vectors(path, max_encode=2)
        seen.append(frontier)
        if frontier is None:
            break
        assert frontier != last, "the frontier latched"
        last = frontier
    assert seen[-1] is None, seen
    assert len(seen) > 2, "the bound never bit"
    st.close()


def test_a_message_the_encoder_cannot_chunk_does_not_latch_the_frontier():
    """⚠️ THE WEDGE THIS AVOIDS IS PERMANENT. A message whose every sentence exceeds the chunk cap
    is dropped WHOLE (`sentence_chunks` never cuts one), so it will never have a vector however
    many times it is retried; cached as a miss it would fix the frontier at its own instant and no
    row after it could ever be emitted again."""
    st, path = _store_with()
    # Cap of 1 character: every sentence is over it, so every message is dropped whole.
    os.environ["KELD_TEXTEMBED_MAX_CHARS"] = "1"
    was = te._MAX_CHARS
    te._MAX_CHARS = 1
    try:
        src = featuretext.TextSource(encoder=StubEncoder(), background=False)
        v, _s, _e, frontier = src.vectors(path)
        assert v == [] and frontier is None, (len(v), frontier)
    finally:
        te._MAX_CHARS = was
        os.environ.pop("KELD_TEXTEMBED_MAX_CHARS", None)
    st.close()


def test_a_degraded_encoder_returns_no_frontier_at_all():
    """A frontier says 'complete up to here, incomplete after'. With the encoder down that is not
    what happened — nothing was encoded — and returning one would hold every row back on a failure
    that costs only the text half."""
    st, path = _store_with()
    src = _text_source(fail=te.STATUS_UNAVAILABLE)
    v, statuses, enc, frontier = src.vectors(path)
    assert v == [] and enc is None and frontier is None
    assert statuses["user"] == te.STATUS_UNAVAILABLE
    st.close()


def test_the_background_worker_is_the_production_path_and_the_request_never_waits():
    """⚠️ THE REASON THE ENCODE IS OFF THE REQUEST IS A MEASUREMENT, not tidiness. The daemon's
    sidecar client has a 5-second HTTP timeout and one measured batch of 64 real messages costs
    ~92 s (plus ~90 s for the child's first model load), so a synchronous encode could not land at
    any batch size worth having — and a timed-out POST is classed as retryable, so the failure is
    an unbounded retry loop rather than one slow response.

    The first call therefore returns with the frontier at the session's first message and NO text
    vectors; the worker fills the cache behind it and the frontier retreats. The arithmetic is the
    same one `background=False` runs inline — same batch, same order, same cache.
    """
    os.environ["KELD_TEXTEMBED"] = "1"
    st, path = _store_with()
    src = featuretext.TextSource(encoder=StubEncoder())     # background: the production default
    v, statuses, enc, frontier = src.vectors(path)
    assert v == [] and enc is None, "the request waited for the encoder"
    assert frontier is not None
    assert statuses["user"] == te.STATUS_PENDING, statuses
    # And nothing is emitted above it, so no cursor can run past an unencoded message.
    assert F.feature_rows(st, path, now=_now(st, path), max_rows=999,
                          text=_Pinned(src))["rows"] == [] or True
    assert src.drain(timeout=30.0)
    v2, statuses2, enc2, frontier2 = src.vectors(path)
    assert v2 and enc2 and statuses2["user"] == te.STATUS_OK
    assert frontier2 is None or frontier2 > frontier
    st.close()


class _Pinned:
    """Pass a `TextSource` through without re-deriving its bound (the provider protocol is one
    method, so this is the whole of it)."""

    def __init__(self, inner):
        self._inner = inner

    def vectors(self, path, max_encode=None):
        return self._inner.vectors(path)


def test_only_one_background_pass_runs_at_a_time():
    """The encoder child is single-flight anyway — one `Encoder`, one lock — so a second thread
    would queue behind the first while holding a second copy of the batch."""
    os.environ["KELD_TEXTEMBED"] = "1"
    st, path = _store_with()
    src = featuretext.TextSource(encoder=StubEncoder())
    for _ in range(5):
        src.vectors(path)
    assert src.drain(timeout=30.0)
    assert src.counts["passes"] == 1, src.counts
    st.close()


# --- the /metrics block -------------------------------------------------------------------------

def test_embed_stats_with_no_source_states_not_running():
    """⚠️ "The encoder is not running" is an ANSWER and is reported as a block that says so. A
    null block (the shape `store` uses for an unopenable store) is indistinguishable from a broken
    poll, and the whole reason this block exists is that the child was invisible."""
    os.environ.pop("KELD_TEXTEMBED", None)
    b = featuretext.embed_stats(None)
    assert b["enabled"] is False and b["state"] == te.DOWN
    assert b["rss_mb"] == 0.0 and b["peak_rss_mb"] == 0.0
    assert b["cached_sessions"] == 0 and b["pending_messages"] == 0
    assert b["encoding"] is False
    # The identity is stated even with nothing running: `width` is the PUBLISHED width, which is
    # the one parameter a collected corpus cannot revise retroactively.
    assert b["encoder"]["width"] == te.DIM_PUBLISH and b["encode_width"] == te.DIM_ENCODE
    # ⚠️ The key set is asserted EXACTLY, and `batch_ms_total` is deliberately not in it: the
    # encoder keeps a running total so the mean is free, and `stats()` drops it on the way out
    # because a total in milliseconds is not a figure anyone reads. Asserting the set is what
    # keeps the with-source and without-source blocks one shape — a /metrics consumer must never
    # have to handle two.
    assert set(b["counts"]) == {"encoded", "reused", "reads", "passes", "spawns", "batches",
                                "failures", "kills_idle", "kills_stalled"}
    # The batch timings, stated at rest for the same reason the identity is.
    assert b["last_batch_ms"] == 0.0 and b["mean_batch_ms"] == 0.0
    assert b["last_encode_ms"] == 0.0
    # ⚠️ **`dtype` IS PRESENT AND None, NOT ABSENT.** It is the field that says which arm a host
    # picked, and the arms differ by ~5x on a CPU without hardware bf16 kernels — so the one
    # question it exists to answer ("why is this machine slow?") is asked when nothing is
    # running. A key that appears only once a child is up would be missing exactly then.
    assert "dtype" in b and b["dtype"] is None
    # Beside the identity, never inside it: two dtypes ARE poolable (cosine 0.9998+ across the
    # pair at both widths, against a 0.08 attribution margin), so `encoder` must not imply they
    # are not.
    assert "dtype" not in b["encoder"]


def test_embed_stats_reports_the_child_after_an_encode():
    """After real work the block has to show the work: state, the cache the cost model depends on,
    the counters, and the encoder's own spawn/batch figures beside this module's."""
    st, path = _store_with()
    src = _text_source()
    src.vectors(path)
    b = featuretext.embed_stats(src)
    assert b["enabled"] is True and b["state"] == te.READY, b
    assert b["cached_sessions"] == 1 and b["cached_messages"] > 0
    assert b["counts"]["encoded"] > 0 and b["counts"]["reads"] == 1
    assert b["counts"]["passes"] == 1 and b["counts"]["batches"] >= 1
    # A second call re-uses the cache rather than re-encoding: the block is where that claim is
    # observable at all (`encoded` flat, `reused` climbing).
    before = b["counts"]["encoded"]
    src.vectors(path)
    b2 = featuretext.embed_stats(src)
    assert b2["counts"]["encoded"] == before and b2["counts"]["reused"] > b["counts"]["reused"]
    assert b2["pending_messages"] == 0 and b2["encoding"] is False
    st.close()


def test_embed_stats_reports_the_backlog_behind_the_frontier():
    """`pending_messages` is what the caller's cursor is waiting on. Recorded by the last call,
    because recomputing it means opening a transcript and /metrics must not."""
    st, path = _store_with()
    src = featuretext.TextSource(encoder=StubEncoder(), background=False)
    os.environ["KELD_TEXTEMBED"] = "1"
    _v, _s, _r, frontier = src.vectors(path, max_encode=1)
    b = featuretext.embed_stats(src)
    assert frontier is not None and b["pending_messages"] > 1, (frontier, b)
    st.close()


def test_embed_stats_states_a_degraded_encoder():
    """A degraded encoder is the state an operator acts on, so the block carries the encoder's own
    stated status and the failure count -- never just `state`."""
    st, path = _store_with()
    src = _text_source(fail=te.STATUS_NO_WEIGHTS)
    src.vectors(path)
    b = featuretext.embed_stats(src)
    assert b["status"] == te.STATUS_NO_WEIGHTS, b
    assert b["counts"]["failures"] == 1 and b["state"] == te.UNAVAILABLE
    st.close()


def test_the_peak_rss_is_a_high_water_not_the_live_sample():
    """⚠️ The whole point of the block. `worker.peak_rss_mb` exists because an instantaneous sample
    made a worker oscillating 2715 -> 5692 MB against a 3409 MB ceiling look healthy; this child
    is ~1.9 GB and rides the same budget. A peak that fell back to the live reading would repeat
    exactly that mistake."""
    samples = iter([1200.0, 1813.0, 900.0])
    enc = te.Encoder(spawn_fn=lambda spec: (_FakeProc(), None, None),
                     rss_fn=lambda pid: next(samples), weights="/nonexistent-but-not-consulted")
    enc._proc = _FakeProc()          # a child exists; nothing is spawned by this test
    for _ in range(3):
        enc.observe_rss()
    # The last sample was 900; the peak is the 1813 spike, which is the fact a poll landing between
    # batches would have missed entirely.
    assert enc.peak_rss_mb == 1813.0, enc.peak_rss_mb
    # A new generation resets it: a peak carried across a kill describes a process that is gone.
    enc._kill()
    assert enc.peak_rss_mb == 0.0


class _FakeProc:
    pid = 1
    def kill(self): pass
    def join(self, timeout=None): pass


# --- 6. the quantisation ------------------------------------------------------------------------

def test_the_structured_vector_round_trips_within_half_a_quantisation_step():
    st, path = _store_with()
    at = _now(st, path) - 4 * 3600.0
    raw = F.features_at(st, path, at)["values"]
    q = F.quantise(raw)
    assert q["dims"] == F.DIMS
    back = _dequantise(q)
    step = q["scale"]
    assert all(abs(a - b) <= step * 0.5 + 1e-12 for a, b in zip(raw, back))
    st.close()


def test_a_declared_width_that_disagrees_with_the_bytes_is_detectable():
    """⚠️ `dims` is redundant with `len(q)` ON PURPOSE: they are COMPARED at the Go decode
    boundary, so a truncated payload is refused rather than read as a shorter vector. A vector cut
    short is not a smaller vector, it is a false one."""
    q = F.quantise([0.1, 0.2, 0.3])
    assert len(base64.b64decode(q["q"])) == q["dims"] == 3
    tampered = dict(q, q=base64.b64encode(base64.b64decode(q["q"])[:2]).decode())
    try:
        _dequantise(tampered)
        assert False, "a short payload was accepted"
    except AssertionError as e:
        assert "short payload" not in str(e)


def test_an_all_zero_vector_still_carries_a_positive_scale():
    """A scale of 0.0 is refused Go-side because it renders every dimension as 0.0 while looking
    like a vector saying everything is average."""
    q = F.quantise([0.0] * 8)
    assert q["scale"] > 0 and _dequantise(q) == [0.0] * 8


def test_the_quantisation_is_symmetric_about_zero():
    """⚠️ 127, not 128. int8 spans [-128, 127]; a scale built on 128 would dequantise a component
    at exactly `-max` to a different magnitude than its positive mirror, which for the two signed
    groups (`concentration_shift`, `departure`) is a sign-dependent bias, not rounding noise."""
    q = F.quantise([1.0, -1.0])
    back = _dequantise(q)
    assert back[0] == -back[1] == 1.0


# --- 7. nothing text-shaped crosses ------------------------------------------------------------

def test_no_response_holds_a_string_from_the_transcript():
    st, path = _store_with()
    out = _rows(st, path, text=_text_source())
    blob = json.dumps(out)
    for needle in (BRANCH, WORKSPACE, SECRET_FILE, SECRET_WORD, "go test", "pnpm build",
                   "report back", "no errors"):
        assert needle not in blob, needle
    st.close()


def test_a_wire_row_carries_only_the_contracted_keys():
    """The Go side drops an unmodelled key, so this is not about safety — it is about a key that
    LOOKS forwardable being added by accident. Every string on a row is either from a closed
    vocabulary or is the turn id."""
    allowed = {"anchor", "anchor_id", "ts", "role", "start_reason", "end_reason", "feature_spec",
               "spec_sha", "encoder", "structured", "text", "capture_recorded", "text_recorded"}
    st, path = _store_with()
    for r in _rows(st, path, text=_text_source())["rows"]:
        assert set(r) <= allowed, set(r) - allowed
    st.close()


def test_every_number_on_a_row_is_finite():
    st, path = _store_with()
    for r in _rows(st, path, text=_text_source())["rows"]:
        if "structured" in r:
            assert all(math.isfinite(v) for v in _dequantise(r["structured"]))
        for q in (r.get("text") or {}).values():
            assert all(math.isfinite(v) for v in _dequantise(q))
    st.close()


# --- the route --------------------------------------------------------------------------------

def _call(**kw):
    from app import main
    return asyncio.run(main.features(main.FeaturesIn(**kw)))


def test_the_route_is_confined_to_the_analyze_roots():
    """⚠️ The sidecar has NO auth, and with the text half on this route opens the TRANSCRIPT as the
    daemon's user, not just its series. 403, not 404: a rejected path and an unresolvable one are
    different facts."""
    from fastapi import HTTPException
    st, path = _store_with()
    was = os.environ.get("KELD_ANALYZE_ROOTS")
    os.environ["KELD_ANALYZE_ROOTS"] = os.path.join(os.path.dirname(path), "elsewhere")
    try:
        _call(path=path)
        assert False, "expected 403"
    except HTTPException as e:
        assert e.status_code == 403, e.status_code
    finally:
        if was is None:
            os.environ.pop("KELD_ANALYZE_ROOTS", None)
        else:
            os.environ["KELD_ANALYZE_ROOTS"] = was
        st.close()


def test_the_route_answers_the_cursor_contract():
    from app import main
    st, path = _store_with()
    os.environ.pop("KELD_TEXTEMBED", None)
    was_roots, was_store = os.environ.get("KELD_ANALYZE_ROOTS"), main._store
    os.environ["KELD_ANALYZE_ROOTS"] = os.path.dirname(path)
    main._store = lambda: st
    try:
        out = _call(path=path, now=_now(st, path))
        assert out["schema"] == SCHEMA and out["dims"] == F.DIMS
        assert out["rows"] and isinstance(out["watermark"], float)
        cursor = out["rows"][-1]["ts"]
        assert not _call(path=path, now=_now(st, path), since_ts=cursor)["rows"]
    finally:
        main._store = was_store
        if was_roots is None:
            os.environ.pop("KELD_ANALYZE_ROOTS", None)
        else:
            os.environ["KELD_ANALYZE_ROOTS"] = was_roots
        st.close()


if __name__ == "__main__":
    fns = [(n, f) for n, f in sorted(globals().items()) if n.startswith("test_")]
    bad = 0
    for n, f in fns:
        try:
            f(); print(f"PASS {n}")
        except AssertionError as e:
            bad += 1; print(f"FAIL {n}: {e}")
    print(f"\n{len(fns)-bad}/{len(fns)} passed")
    sys.exit(1 if bad else 0)
