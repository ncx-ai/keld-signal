"""Per-message text embedding: the bounds, the streams, the scalars, and the privacy invariant.

  cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_textembed.py

These run against a STUB encoder — a deterministic hash-to-vector behind the same `encode`
interface — so the whole file is milliseconds and needs no weights. That is deliberate and not a
shortcut: what is being tested here is the policy (what gets read, where it is cut, what is
published, what a missing model does), and none of it is a property of Qwen3. The real encoder is
exercised separately by a measurement run, whose numbers are in the commit message.

The properties this file exists for, in order:

  1. No text crosses the module boundary. Pinned by reflection over the published type, not by
     reading the code, because a later edit adding a field is exactly what a reading misses.
  2. A `tool_result` block is never encoded. It is the reason `transcript.turns_in` is fast.
  3. A message is never cut mid-sentence, and a drop is declared.
  4. Absent weights are "no vectors and a stated reason", never a crash.
  5. The projection is orthogonal — cosine and inner products survive it EXACTLY, or training
     changes and the whole treatment is a silent corruption instead of a hardening.
"""
import math
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from app.analysis import textembed as te


# ---- helpers ------------------------------------------------------------------------------------

class StubEncoder:
    """A deterministic encoder behind the real `encode` interface: text -> a unit vector whose
    direction depends only on the text. Same-text-same-vector is what lets the cosine assertions
    below be exact."""

    def __init__(self, dim=None, fail=None):
        self.dim = dim or te.DIM_PUBLISH
        self.fail = fail
        self.seen = []

    def encode(self, texts):
        if self.fail:
            return [], self.fail
        self.seen.extend(texts)
        out = []
        for t in texts:
            h = 0
            v = [0.0] * self.dim
            for ch in t:
                h = (h * 131 + ord(ch)) % 1000003
                v[h % self.dim] += 1.0
            out.append(te.normalize(v) if any(v) else [1.0] + [0.0] * (self.dim - 1))
        return out, te.STATUS_OK


def _on():
    os.environ["KELD_TEXTEMBED"] = "1"


def _msg(t, stream, text):
    return te.Message(t, stream, text)


def _epoch(iso):
    from app.analysis.capture import epoch
    return epoch(iso)


# ---- 1. the privacy invariant ---------------------------------------------------------------------

def test_published_row_holds_no_text():
    """The published per-message row carries an instant, a stream TAG and a vector. Nothing else
    may be a string, and `stream` is the one exception because it is drawn from a closed
    three-value vocabulary this module owns.

    By reflection over `__slots__`, not by reading: a field added later is precisely what a
    reading of the class misses, and this is the invariant the whole subsystem rests on."""
    _on()
    msgs = [_msg(100.0, te.USER, "Fix the flaky ingest test. It fails on the tail parse.")]
    vecs, _ = te.embed(msgs, StubEncoder())
    assert len(vecs) == 1
    row = vecs[0]
    # `id` is the transcript TURN'S UUID, not a fragment of the message: an instant is not a
    # sufficient message key (the series quantizes to 0.1 s and two turns can collide on one tick),
    # so a published `message` row keyed on its instant could upsert its neighbour at Atlas. It is
    # bounded and whitespace-checked again at the Go decode boundary (`enrich.ValidAnchorID`).
    assert set(te.MessageVector.__slots__) == {"t", "stream", "vector", "chunks",
                                               "dropped_chars", "id"}
    for slot in te.MessageVector.__slots__:
        val = getattr(row, slot)
        if slot == "stream":
            assert val in te.STREAMS
            continue
        assert not isinstance(val, str), f"{slot} is a string on the published row"
    assert isinstance(row.dropped_chars, int)     # a COUNT, never what was dropped


def test_child_errors_cross_back_as_a_class_name_only():
    """`worker.py` sends `repr(e)` and is right to: its inputs are the caller's own text. Here the
    input is a transcript message, and a tokenizer error's repr can quote it. So the child sends
    the exception's CLASS and nothing else — checked on the real `_serve`, with a queue pair
    standing in for the process."""
    import queue

    class Q:
        def __init__(self, items=()):
            self.q = queue.Queue()
            for i in items:
                self.q.put(i)
            self.out = []

        def get(self, timeout=None):
            return self.q.get(timeout=timeout)

        def put(self, x):
            self.out.append(x)

    secret = "the customer is Northwind Trading and the key is sk-abc"
    req = Q([{"texts": [secret]}, None])
    resp = Q()

    def boom(spec):
        raise RuntimeError(f"tokenizer failed on {secret}")

    saved = te._load
    te._load = boom
    try:
        te._serve(req, resp, {"dir": "/nonexistent"})
    finally:
        te._load = saved
    assert resp.out == [{"ready": False, "error": "RuntimeError"}]
    assert secret not in repr(resp.out)


# ---- 2. what is read ------------------------------------------------------------------------------

def test_tool_result_blocks_are_never_read():
    """A line carrying BOTH a `tool_result` and a `tool_use` block survives `turns_in`'s skip, so
    the guarantee cannot come from the line filter — it has to come from reading by BLOCK TYPE.
    A `tool_result` block's content must never reach the encoder."""
    _on()
    turns = [{
        "timestamp": "2026-08-26T10:00:00.000Z",
        "message": {"role": "user", "content": [
            {"type": "tool_result", "content": "MASSIVE OUTPUT that must never be encoded"},
            {"type": "text", "text": "and here is what I actually said"},
        ]},
    }]
    msgs = te.messages_in(turns, _epoch)
    assert [m.text for m in msgs] == ["and here is what I actually said"]
    enc = StubEncoder()
    te.embed(msgs, enc)
    assert all("MASSIVE OUTPUT" not in s for s in enc.seen)


def test_three_streams_are_separated_and_thinking_is_usually_empty():
    """`user`, `asst`, `think` — never concatenated. And the measured fact about `think`: a
    platform-written transcript's thinking blocks carry a signature and an EMPTY string, so the
    stream reports `skipped:empty` as a stated outcome rather than looking like a stream nobody
    asked for."""
    _on()
    turns = [
        {"timestamp": "2026-08-26T10:00:00.000Z",
         "message": {"role": "user", "content": [{"type": "text", "text": "why is it slow?"}]}},
        {"timestamp": "2026-08-26T10:00:05.000Z",
         "message": {"role": "assistant", "content": [
             {"type": "thinking", "thinking": "", "signature": "abc"},   # the measured shape
             {"type": "text", "text": "because the parse is whole-file."},
         ]}},
    ]
    msgs = te.messages_in(turns, _epoch)
    assert sorted(m.stream for m in msgs) == [te.ASST, te.USER]
    _, statuses = te.embed(msgs, StubEncoder())
    assert statuses[te.USER] == te.STATUS_OK
    assert statuses[te.ASST] == te.STATUS_OK
    assert statuses[te.THINK] == te.STATUS_EMPTY, "an absent stream must SAY it is absent"


def test_a_thinking_block_with_real_text_does_encode():
    """The stream is wired, not stubbed out: a producer that ever writes thinking text populates
    it with no code change. That is the whole reason `think` exists today."""
    _on()
    turns = [{"timestamp": "2026-08-26T10:00:05.000Z",
              "message": {"role": "assistant", "content": [
                  {"type": "thinking", "thinking": "the tail parse must equal a full parse."}]}}]
    msgs = te.messages_in(turns, _epoch)
    assert [m.stream for m in msgs] == [te.THINK]
    _, statuses = te.embed(msgs, StubEncoder())
    assert statuses[te.THINK] == te.STATUS_OK


def test_command_echoes_are_dropped():
    """Machine text in a user-shaped envelope is not the human's words. An embedding of a /login
    echo is the harness talking to itself; it cost the effort signals a 15% machine denominator
    when it went unfiltered there."""
    _on()
    turns = [
        {"timestamp": "2026-08-26T10:00:00.000Z",
         "message": {"role": "user", "content": [
             {"type": "text", "text": "<command-name>/login</command-name>"}]}},
        {"timestamp": "2026-08-26T10:00:01.000Z",
         "message": {"role": "user", "content": [{"type": "text", "text": "real question here"}]}},
    ]
    assert [m.text for m in te.messages_in(turns, _epoch)] == ["real question here"]


# ---- 3. never cut mid-sentence ----------------------------------------------------------------------

def test_a_long_message_is_split_at_sentence_boundaries():
    """Split, then mean-pool the chunk vectors — never truncated mid-clause. 46 of 47 generated
    beats came out mid-clause from a 200-rune cap; that is the measured cost of the other choice."""
    sentences = ["This is sentence number %d and it says something." % i for i in range(40)]
    chunks, dropped = te.sentence_chunks(" ".join(sentences), cap=200)
    assert len(chunks) > 1
    assert dropped == 0
    for c in chunks:
        assert len(c) <= 200
        assert c.endswith("."), f"chunk does not end at a sentence boundary: {c[-30:]!r}"
    # Nothing is lost: every sentence appears in exactly one chunk.
    joined = " ".join(chunks)
    for s in sentences:
        assert joined.count(s) == 1


def test_an_oversize_sentence_is_dropped_whole_and_declared():
    """A cut identifier is a FALSE identifier, so a single sentence over the cap is dropped rather
    than cut — and the drop is DECLARED, `omittedNotice`'s rule one level up. A silently shorter
    input is the same defect as a silently shorter output."""
    _on()
    monster = "x" * 500                                   # one "sentence", no boundary in it
    chunks, dropped = te.sentence_chunks("Short one. " + monster, cap=200)
    assert chunks == ["Short one."]
    assert dropped == 500
    vecs, _ = te.embed([_msg(1.0, te.USER, "Short one. " + monster)], StubEncoder(), cap=200)
    assert vecs[0].dropped_chars == 500, "the row must carry the count, or the loss is invisible"


def test_a_message_that_is_all_oversize_yields_no_vector():
    """No vector, and a stated reason for there being none — not a vector of the first 200
    characters, which is the failure this rule exists to prevent."""
    _on()
    vecs, statuses = te.embed([_msg(1.0, te.USER, "y" * 5000)], StubEncoder())
    assert vecs == []
    assert statuses[te.USER] == te.STATUS_EMPTY


def test_chunks_of_one_message_pool_into_one_vector():
    """A message is ONE row however many chunks it took. Encoding per message is what makes the
    cost one forward pass per message; a row per chunk would put the shell arithmetic on a
    different unit than the one the spec sizes."""
    _on()
    long = " ".join("Sentence %d about the ingest path." % i for i in range(60))
    enc = StubEncoder()
    vecs, _ = te.embed([_msg(1.0, te.USER, long)], enc, dim=te.DIM_PUBLISH)
    assert len(vecs) == 1
    assert vecs[0].chunks > 1
    assert len(enc.seen) == vecs[0].chunks
    assert abs(sum(x * x for x in vecs[0].vector) - 1.0) < 1e-9


# ---- 4. degrading to no vectors ------------------------------------------------------------------

def test_absent_weights_are_a_stated_status_not_a_crash():
    """A missing model costs the text half of a row and nothing else. `Encoder` resolves its
    weights directory, finds none, and says so."""
    _on()
    enc = te.Encoder(weights="", clock=lambda: 0.0)
    out, status = enc.encode(["anything"])
    assert out == []
    assert status == te.STATUS_NO_WEIGHTS
    assert enc.state == te.UNAVAILABLE


def test_the_feature_off_spawns_nothing():
    """OFF is not compute-and-discard: no child, and torch is never imported by this path."""
    os.environ["KELD_TEXTEMBED"] = "0"
    spawned = []
    enc = te.Encoder(spawn_fn=lambda spec: spawned.append(spec), weights="/tmp")
    out, status = enc.encode(["anything"])
    assert out == [] and status == te.STATUS_DISABLED
    assert spawned == [], "the encoder must not be loaded at all when the feature is off"
    _on()


def test_an_unavailable_encoder_is_retried_on_a_cooldown_not_latched_and_not_per_call():
    """Provisioning is asynchronous, so a failure must not latch — a daemon restart to pick up a
    download that already finished is the wrong recovery. But a failed spawn costs seconds, so it
    must not be retried per call either."""
    _on()
    now = [0.0]
    attempts = []

    def spawn(spec):
        attempts.append(now[0])
        raise OSError("no")

    enc = te.Encoder(spawn_fn=spawn, weights="/tmp", clock=lambda: now[0], retry_s=300)
    enc.encode(["a"])
    assert len(attempts) == 1
    now[0] = 10.0
    enc.encode(["a"])
    assert len(attempts) == 1, "retried per call"
    now[0] = 400.0
    enc.encode(["a"])
    assert len(attempts) == 2, "latched instead of retried on the cooldown"


def test_embed_reports_the_encoders_reason_per_stream():
    """When the encoder cannot serve, every stream that HAD messages says why. A silent empty list
    reads as 'the engineer said nothing', which is a different and false claim."""
    _on()
    msgs = [_msg(1.0, te.USER, "a sentence"), _msg(2.0, te.ASST, "another sentence")]
    vecs, statuses = te.embed(msgs, StubEncoder(fail=te.STATUS_NO_WEIGHTS))
    assert vecs == []
    assert statuses[te.USER] == te.STATUS_NO_WEIGHTS
    assert statuses[te.ASST] == te.STATUS_NO_WEIGHTS
    assert statuses[te.THINK] == te.STATUS_EMPTY
    assert set(statuses.values()) <= set(te.STATUSES)


# ---- 5. the projection ---------------------------------------------------------------------------

def test_the_projection_is_orthogonal_and_preserves_cosine_exactly():
    """The whole justification of the treatment. If it did not preserve inner products it would be
    a silent corruption of every training vector rather than a hardening measure."""
    m = te.projection(dim=32, seed=7)
    # Q^T Q == I
    for i in range(32):
        for j in range(32):
            dot = sum(m[k][i] * m[k][j] for k in range(32))
            assert abs(dot - (1.0 if i == j else 0.0)) < 1e-9
    a = te.normalize([math.sin(i) for i in range(32)])
    b = te.normalize([math.cos(i * 0.7) for i in range(32)])
    before = te.cosine(a, b)
    after = te.cosine(te.project(a, m), te.project(b, m))
    assert abs(before - after) < 1e-9
    assert abs(sum(x * y for x, y in zip(a, b))
               - sum(x * y for x, y in zip(te.project(a, m), te.project(b, m)))) < 1e-9


def test_the_projection_is_deterministic_from_the_seed_and_changes_with_it():
    """Generated from a seed held in configuration and issued by Keld — so the same seed must give
    the same matrix on every machine in the fleet, and a different seed a different one."""
    assert te.projection(dim=16, seed=3) == te.projection(dim=16, seed=3)
    assert te.projection(dim=16, seed=3) != te.projection(dim=16, seed=4)


def test_mrl_truncation_is_a_prefix_slice():
    """A prefix, re-normalised — no second forward pass, and the direction of the first 256
    components is untouched."""
    v = te.normalize([float(i + 1) for i in range(1024)])
    t = te.truncate(v, 256)
    assert len(t) == 256
    assert abs(sum(x * x for x in t) - 1.0) < 1e-12
    ratio = t[0] / v[0]
    for i in range(256):
        assert abs(t[i] - v[i] * ratio) < 1e-12


# ---- 6. shells and the derived scalars ---------------------------------------------------------------

def _v(t, stream, vec):
    return te.MessageVector(t, stream, vec, 1, 0)


def _unit(i, dim=8):
    v = [0.0] * dim
    v[i] = 1.0
    return v


def test_shells_are_disjoint_and_look_back():
    """`[0,5m)`, `[5,20m)`, `[20,60m)`, `[60,240m)`, `[240m, start)` — a message lands in exactly
    one, and a message at or after the anchor lands in none."""
    anchor = 10_000.0
    vs = [_v(anchor - 60, te.USER, _unit(0)),        # 1 min
          _v(anchor - 600, te.USER, _unit(1)),       # 10 min
          _v(anchor - 3000, te.USER, _unit(2)),      # 50 min
          _v(anchor - 9000, te.USER, _unit(3)),      # 150 min
          _v(anchor - 90000, te.USER, _unit(4)),     # 1500 min
          _v(anchor + 60, te.USER, _unit(5))]        # the future
    shells = te.shells_for(anchor, vs)
    assert [len(s) for s in shells] == [1, 1, 1, 1, 1]
    assert sum(len(s) for s in shells) == 5, "the anchor's future must belong to no shell"


def test_dispersion_drift_and_novelty():
    """The three scalars, on vectors whose cosines are known by construction."""
    anchor = 10_000.0
    # A shell of two orthogonal messages: mean cos to their centroid is cos(45deg).
    shell = [_v(anchor - 60, te.USER, _unit(0)), _v(anchor - 120, te.USER, _unit(1))]
    stats = te.shell_stats(shell, te.USER)
    assert abs(stats["dispersion"] - (1.0 - math.sqrt(0.5))) < 1e-9
    assert stats["drift"] is None, "no previous shell means nothing was compared"
    assert stats["novelty"] is None, "no earlier message means novelty is unmeasured, not 1.0"
    # Drift against an orthogonal previous centroid is 1.0; against itself, 0.0.
    assert abs(te.shell_stats(shell, te.USER, prev_centroid=_unit(7))["drift"] - 1.0) < 1e-9
    same = te.shell_stats(shell, te.USER, prev_centroid=stats["centroid"])
    assert abs(same["drift"]) < 1e-9
    # Novelty: a message identical to an earlier one is 0 novel; an orthogonal one is 1.
    earlier = [_v(anchor - 900, te.USER, _unit(0))]
    n = te.shell_stats(shell, te.USER, earlier=earlier)["novelty"]
    assert abs(n - 0.5) < 1e-9              # mean of 0.0 (identical) and 1.0 (orthogonal)


def test_absent_is_never_zero():
    """A drift of 0.0 says the work did not move; an absent previous shell says nothing was
    compared. Rendering the second as the first is the single misreading the evidence-floor work
    across this package exists to prevent."""
    stats = te.shell_stats([], te.USER)
    assert stats["status"] == te.STATUS_EMPTY
    assert stats["n"] == 0
    for k in ("centroid", "dispersion", "drift", "novelty"):
        assert stats[k] is None, f"{k} defaulted instead of stating absence"


def test_the_ladder_walks_oldest_first_so_drift_looks_back():
    """`drift` is against the PREVIOUS shell, i.e. the one further back in time. The ladder is
    written newest-first, so the walk has to be reversed — get that wrong and every drift is
    computed against the future."""
    anchor = 10_000.0
    vs = [_v(anchor - 60, te.USER, _unit(0)),        # newest shell
          _v(anchor - 600, te.USER, _unit(1)),       # the one before it
          _v(anchor - 3000, te.USER, _unit(1))]      # and the one before that
    rows = te.ladder(anchor, vs)
    assert rows[2]["streams"][te.USER]["drift"] is None          # oldest populated: no previous
    assert abs(rows[1]["streams"][te.USER]["drift"]) < 1e-9      # identical to its predecessor
    assert abs(rows[0]["streams"][te.USER]["drift"] - 1.0) < 1e-9  # orthogonal to its predecessor


def test_streams_are_never_pooled_together():
    """Different registers. A `user` shell's centroid must not move because the assistant spoke."""
    anchor = 10_000.0
    vs = [_v(anchor - 60, te.USER, _unit(0)), _v(anchor - 60, te.ASST, _unit(1))]
    rows = te.ladder(anchor, vs)
    u = rows[0]["streams"][te.USER]
    assert u["n"] == 1
    assert abs(te.cosine(u["centroid"], _unit(0)) - 1.0) < 1e-9


def test_a_message_is_encoded_once_and_reused_by_every_shell_containing_it():
    """The cost claim. Shells overlap across rows: the same `MessageVector` is placed into a
    different shell for a later anchor, and no re-encode happens because `shells_for` takes
    vectors, not text."""
    _on()
    enc = StubEncoder()
    msgs = [_msg(10_000.0 - 60, te.USER, "one message.")]
    vecs, _ = te.embed(msgs, enc)
    assert len(enc.seen) == 1
    a = te.shells_for(10_000.0, vecs)
    b = te.shells_for(10_000.0 + 900, vecs)          # 15 minutes later
    assert len(a[0]) == 1 and a[1] == []
    assert a[0][0] is b[1][0], "the same vector, re-shelled — not re-encoded"
    assert len(enc.seen) == 1


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn(); print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
