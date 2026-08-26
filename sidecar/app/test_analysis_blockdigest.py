"""The v2 block path: which blocks are CLOSED, and what one block's digest holds.

  cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_analysis_blockdigest.py

⚠️ **This file lives at `app/test_analysis_blockdigest.py`, NOT under `app/analysis/`.** AGENTS.md's
test loop globs `app/test_*.py`; a file one directory down would silently never run, which is the
one test failure nothing reports.

THE PROPERTY THIS FILE EXISTS FOR, and it is the only genuinely new concept in the path:

    A CLOSED BLOCK IS ONE NOTHING CAN STILL CHANGE.

    closed(b) == the store has ingested through b's last bin
                 and ( activity exists after b.end  or  now - b.end >= IDLE_SECONDS )

The disjunction is load-bearing and an earlier draft of the design got it wrong: requiring silence
for EVERY block emits nothing at all during an active session, so a full working day would produce
its first block only once the person stopped. Mid-session blocks must close continuously as work
moves past them, and the ONLY provisional block is the trailing one. Both halves are asserted
below, on the same fixture, at two different clocks.

Everything else here is composition, and the tests treat it as such: the digest is checked for
having the same DIMENSION SHAPE `/analyze` produces (the one thing v2 must not quietly narrow),
not for re-deriving numbers that `test_analysis_window.py`, `test_analysis_prior.py`,
`test_analysis_dynamics.py`, `test_magnitude.py` and `test_latency.py` already own.

The fixture is a real transcript through a real `ingest_file` into a real SQLite store, not a
stub: `active_bins` reads the `bin` table, the watermark comes from the `ingest` checkpoint, and
`covers` needs the `prompt` index. Every one of those is a thing a fake would have to fake
correctly, and the closure rule is exactly a claim about how they line up.
"""
import atexit
import asyncio
import datetime as dt
import itertools
import json
import os
import shutil
import sys
import tempfile

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from app.analysis import blocks as blocks_mod
from app.analysis import blockdigest
from app.analysis.ingest import ingest_file, session_of
from app.analysis.store import open_store
from app.analysis.workstreams import ALLOCATION, INVENTORY

_TMP = tempfile.mkdtemp(prefix="keld-blockdigest-test-")
atexit.register(lambda: shutil.rmtree(_TMP, ignore_errors=True))
# A FRESH store per call, not a shared one. The retention test below advances the store's serving
# floor, which is MONOTONIC by design and would silently expire the early blocks of every test
# that ran after it -- and the runner's order is alphabetical, so it did.
_SEQ = itertools.count()

# Two prompts, so `covers` has two episodes to map and the "unknown id" test has a real id to
# survive beside the bogus one.
P1, P2 = "blk-prompt-0001", "blk-prompt-0002"


def _user(ts, uuid, text):
    return {"type": "user", "timestamp": ts, "cwd": "/workspace/widget-app",
            "gitBranch": "trunk", "uuid": uuid,
            "message": {"content": [{"type": "text", "text": text}]}}


def _assistant(ts, uuid, req, file_path):
    return {"type": "assistant", "timestamp": ts, "cwd": "/workspace/widget-app",
            "gitBranch": "trunk", "uuid": uuid, "requestId": req,
            "message": {"role": "assistant", "model": "acme-llm-7b-preview",
                        "content": [{"type": "tool_use", "id": "toolu_" + uuid, "name": "Read",
                                     "input": {"file_path": file_path}}],
                        "usage": {"input_tokens": 100, "output_tokens": 20,
                                  "cache_creation_input_tokens": 0,
                                  "cache_read_input_tokens": 0}}}


def _transcript(name="fixture-blk-0000"):
    """A transcript cut by BOTH terminators, so one fixture exercises the whole closure rule.

    10:00:00-10:24:45   25 minutes of work  -> a `budget` cut at 10:20 and a second block to 10:25
    10:25    -11:00     35 minutes of silence (7 empty bins, well past `IDLE_BINS`) -> a segment
                        seam, so the 10:20 block ends `idle` and nothing tiles the gap
    11:00:00-11:09:15   10 minutes of work  -> the TRAILING block, ending `session_end`

    Wholly invented, like every fixture in this tree. The exact shape matters: three blocks is the
    smallest number that has a first, a middle and a trailing one, and the closure rule treats the
    trailing one differently from both the others.
    """
    root = os.path.join(_TMP, "%s-%d" % (name, next(_SEQ)))
    os.makedirs(root, exist_ok=True)
    path = os.path.join(root, name + ".jsonl")
    rows = [_user("2026-08-01T10:00:00Z", P1, "build the thing")]
    for i in range(49):                                  # 10:00:15 .. 10:24:15, every 30s
        m, s = divmod(i * 30 + 15, 60)
        rows.append(_assistant(f"2026-08-01T10:{m:02d}:{s:02d}Z", f"blk-a-{i:03d}",
                               f"req-a-{i:03d}", "/workspace/widget-app/api/q.go"))
    rows.append(_user("2026-08-01T11:00:00Z", P2, "now ship it"))
    for i in range(19):                                  # 11:00:15 .. 11:09:15
        m, s = divmod(i * 30 + 15, 60)
        rows.append(_assistant(f"2026-08-01T11:{m:02d}:{s:02d}Z", f"blk-b-{i:03d}",
                               f"req-b-{i:03d}", "/workspace/widget-app/web/app.ts"))
    with open(path, "w") as fh:
        for o in rows:
            fh.write(json.dumps(o, separators=(",", ":")) + "\n")
    return path


def _fixture(name="fixture-blk-0000"):
    """The transcript above, ingested into its own store. Written and ingested separately so the
    endpoint test can point KELD_HOME at the app's own store and ingest into THAT one."""
    path = _transcript(name)
    store = open_store(os.path.join(os.path.dirname(path), "refseries.db"))
    ingest_file(store, path, None, None)
    return store, path


def _cut(store, path):
    session = session_of(path)
    return blocks_mod.cut(store, session, *blockdigest.span_of(store, session))


def _epoch(iso):
    return dt.datetime.fromisoformat(iso.replace("Z", "+00:00")).timestamp()


def _spans(out):
    return [(b["start"], b["end"], b["start_reason"], b["end_reason"]) for b in out["blocks"]]


# --- the fixture says what the closure rule is being asked about ---------------------------------

def test_the_fixture_cuts_into_the_three_blocks_the_closure_rule_needs():
    """Stated as its own test rather than assumed by the others: if the cutter ever produced a
    different shape here, every closure assertion below would still pass while testing something
    else entirely."""
    store, path = _fixture()
    cut = _cut(store, path)
    assert [(b.start_reason, b.end_reason) for b in cut] == [
        ("session_start", "budget"), ("budget", "idle"), ("idle", "session_end")], cut
    assert cut[0].start == _epoch("2026-08-01T10:00:00Z"), cut[0]
    assert cut[-1].end == _epoch("2026-08-01T11:10:00Z"), cut[-1]
    # The dead air belongs to NO block: the blocks do not abut across the gap.
    assert cut[1].end < cut[2].start, cut


# --- closure: the disjunction, both halves -------------------------------------------------------

def test_a_closed_mid_session_block_is_returned_while_the_session_is_still_live():
    """The half an earlier draft of the design got wrong.

    One minute after the last turn — nowhere near the idle threshold — the two blocks with work
    after them are ALREADY final and must be emitted. A `budget` or `idle` cut cannot be revised:
    anything arriving later starts a NEW block. Requiring silence here would mean a full working
    day publishes its first block only after the person stops.
    """
    store, path = _fixture()
    cut = _cut(store, path)
    out = blockdigest.digest_blocks(store, path, now=cut[-1].end + 60)
    assert [b["end_reason"] for b in out["blocks"]] == ["budget", "idle"], _spans(out)
    assert out["blocks"][0]["evidence"] > 0 and out["blocks"][1]["evidence"] > 0, _spans(out)


def test_the_trailing_block_is_withheld_until_the_idle_threshold_has_elapsed():
    """The other half. The trailing block's end is "where the data currently stops", not a real
    boundary, so only silence can settle it — and the threshold is measured from `b.end` (the
    bin's end), not from the last turn.

    That is not a detail. `IDLE_SECONDS` after `b.end`, any arriving turn sits at least
    `IDLE_BINS` empty bins after the trailing active bin, so `active_segments` splits and the
    arrival opens a NEW block. Measured from the last TURN instead, a turn arriving exactly
    `IDLE_SECONDS` later can leave only two empty bins — not an idle split — and the block we had
    already emitted would silently grow.
    """
    store, path = _fixture()
    cut = _cut(store, path)
    tail = cut[-1]
    just_short = blockdigest.digest_blocks(store, path,
                                           now=tail.end + blockdigest.IDLE_SECONDS - 1)
    assert [b["end_reason"] for b in just_short["blocks"]] == ["budget", "idle"], _spans(just_short)

    settled = blockdigest.digest_blocks(store, path, now=tail.end + blockdigest.IDLE_SECONDS)
    assert [b["end_reason"] for b in settled["blocks"]] == ["budget", "idle", "session_end"], \
        _spans(settled)
    assert settled["blocks"][-1]["start"] == tail.start, _spans(settled)


def test_the_idle_threshold_is_derived_from_the_cutter_not_restated():
    """A literal here would let someone retune `blocks.IDLE_BINS` and leave the emitter closing
    blocks the next arrival can still extend — the failure would be silent and the block would be
    published twice with different ends."""
    assert blockdigest.IDLE_SECONDS == blocks_mod.IDLE_BINS * 300
    assert blockdigest.IDLE_SECONDS == 900.0


def test_a_store_behind_the_transcript_cannot_settle_the_trailing_block():
    """Silence in the SERIES is not silence on the MACHINE. With unparsed bytes on disk, the
    trailing block may not be the trailing block at all, so the idle branch is switched off and
    only "activity after" closes anything. Mid-session blocks are NOT delayed by it — the whole
    point of the disjunction."""
    store, path = _fixture()
    cut = _cut(store, path)
    out = blockdigest.digest_blocks(store, path, now=cut[-1].end + 10 * 3600, current=False)
    assert [b["end_reason"] for b in out["blocks"]] == ["budget", "idle"], _spans(out)
    # And it is only the trailing one that is held: currency changes nothing for the others.
    assert len(out["blocks"]) == 2, _spans(out)


def test_a_never_ingested_transcript_closes_nothing_and_reports_a_null_watermark():
    """`watermark` is returned even with no blocks, because it is the ONE fact separating "nothing
    has settled yet" from "this transcript has never been ingested". A caller cannot tell those
    apart from an empty list, and they call for opposite actions."""
    store, path = _fixture()
    other = os.path.join(os.path.dirname(path), "never-ingested.jsonl")
    open(other, "w").close()
    out = blockdigest.digest_blocks(store, other, now=1e12)
    assert out == {"blocks": [], "watermark": None}, out

    live = blockdigest.digest_blocks(store, path, now=_epoch("2026-08-01T10:01:00Z"))
    assert live["watermark"] == _epoch("2026-08-01T11:09:15Z"), live["watermark"]


# --- resumption ----------------------------------------------------------------------------------

def test_since_ts_resumes_from_a_block_end_without_duplicating_or_skipping():
    """The caller passes the LAST EMITTED BLOCK'S END and `since_ts` is compared against a block's
    START. Blocks abut inside an active segment (`next.start == prev.end`), so `>=` admits the
    next block and excludes the one already emitted; across the idle gap `next.start > prev.end`
    and it admits it just the same. Walked here one block at a time, and the union must be exactly
    the closed set with nothing repeated."""
    store, path = _fixture()
    cut = _cut(store, path)
    now = cut[-1].end + blockdigest.IDLE_SECONDS
    whole = blockdigest.digest_blocks(store, path, now=now)
    assert len(whole["blocks"]) == 3, _spans(whole)

    seen, cursor = [], None
    for _ in range(3):
        page = blockdigest.digest_blocks(store, path, since_ts=cursor, now=now, max_blocks=1)
        assert len(page["blocks"]) == 1, (cursor, _spans(page))
        seen.append(page["blocks"][0])
        cursor = page["blocks"][0]["end"]
    assert [b["start"] for b in seen] == [b["start"] for b in whole["blocks"]], seen
    # Exhausted: resuming past the last block returns nothing, and still reports the watermark.
    tailed = blockdigest.digest_blocks(store, path, since_ts=cursor, now=now)
    assert tailed["blocks"] == [] and tailed["watermark"] is not None, tailed


def test_max_blocks_bounds_the_response_and_bounding_it_loses_nothing():
    """A bound on the RESPONSE, never a loss: the caller resumes from `since_ts` and the next call
    continues where this one stopped. The default is 24 because a block is at most
    `blocks.MAX_BLOCK_MINUTES` (20) long, so 24 of them is eight hours of ACTIVE work — a full
    working day's backlog in one call."""
    store, path = _fixture()
    cut = _cut(store, path)
    now = cut[-1].end + blockdigest.IDLE_SECONDS
    assert blockdigest.DEFAULT_MAX_BLOCKS * blocks_mod.MAX_BLOCK_MINUTES == 480
    first = blockdigest.digest_blocks(store, path, now=now, max_blocks=2)
    assert len(first["blocks"]) == 2, _spans(first)
    rest = blockdigest.digest_blocks(store, path, since_ts=first["blocks"][-1]["end"], now=now)
    assert len(rest["blocks"]) == 1, _spans(rest)
    assert rest["blocks"][0]["start"] == cut[-1].start, _spans(rest)


# --- covers: the block <-> episode mapping -------------------------------------------------------

def test_an_unknown_prompt_id_is_dropped_rather_than_being_fatal():
    """A not-yet-ingested prompt is an ordinary case — the daemon's list can be one poll ahead of
    the store — and a prompt whose instant is unknown simply cannot define an episode. Inventing
    one would either hide an episode or manufacture one, and raising would discard every block in
    the call over one id."""
    store, path = _fixture()
    session = session_of(path)
    assert blockdigest.time_prompts(store, session, ["not-a-prompt"]) == []
    timed = blockdigest.time_prompts(store, session, [P2, "not-a-prompt", P1])
    assert [pid for pid, _ts in timed] == [P1, P2], timed      # and sorted ascending

    now = _cut(store, path)[-1].end + blockdigest.IDLE_SECONDS
    out = blockdigest.digest_blocks(store, path, now=now,
                                    prompt_ids=["ghost-0", P1, "ghost-1", P2])
    ids = {e["prompt_id"] for b in out["blocks"] for e in b["covers"]}
    assert ids == {P1, P2}, ids


def test_covers_rides_each_block_and_is_cut_from_the_whole_session():
    """`covers` is a member of the block object rather than a parallel array, because the response
    is already a list of block objects and a sibling list would have to be re-zipped by every
    consumer.

    It is computed over the WHOLE cut list and then indexed, never over the emitted subset:
    `blocks.covers` closes the final episode at `blocks[-1].end`, so running it on a truncated
    list would report an episode as `complete` inside the last emitted block when it actually
    continues into a block this call has not returned yet. A UI would draw the work as stopping at
    a boundary the cutter chose.
    """
    store, path = _fixture()
    cut = _cut(store, path)
    now = cut[-1].end + blockdigest.IDLE_SECONDS
    partial = blockdigest.digest_blocks(store, path, now=now, prompt_ids=[P1, P2], max_blocks=1)
    b0 = partial["blocks"][0]
    assert [e["prompt_id"] for e in b0["covers"]] == [P1], b0["covers"]
    # P1's episode runs to P2 at 11:00, well past this block's 10:20 end.
    assert b0["covers"][0]["complete"] is False, b0["covers"]
    assert b0["covers"][0]["to"] == b0["end"], b0["covers"]

    whole = blockdigest.digest_blocks(store, path, now=now, prompt_ids=[P1, P2])
    assert [[e["prompt_id"] for e in b["covers"]] for b in whole["blocks"]] == [[P1], [P1], [P2]]
    # No prompts supplied is not an error: the mapping is simply empty for every block.
    bare = blockdigest.digest_blocks(store, path, now=now)
    assert [b["covers"] for b in bare["blocks"]] == [[], [], []], _spans(bare)


# --- the digest itself ---------------------------------------------------------------------------

def test_the_digest_carries_the_same_dimension_shape_analyze_produces():
    """v2 changes the UNIT of attribution, not the vocabulary. Every ALLOCATION dimension and
    every INVENTORY dimension `/analyze` publishes for a window must publish for a block, in the
    same shape — a narrowing here would be invisible on the wire and would look like a quiet
    session.

    Compared against `/analyze`'s real output on the same transcript rather than against a list
    restated here, which is what makes this catch a change made in `workstreams.py`.
    """
    from app.analysis.analyze import analyze_window     # v1, imported by the TEST only

    store, path = _fixture()
    cut = _cut(store, path)
    out = blockdigest.digest_blocks(store, path, now=cut[-1].end + blockdigest.IDLE_SECONDS)
    block = out["blocks"][0]
    window = analyze_window(path, P2, 60, None, store=store, refresh=False,
                            sizer=None, prior=True)

    assert set(block["workstreams"]) == set(window["workstreams"]), (
        set(block["workstreams"]) ^ set(window["workstreams"]))
    assert set(block["workstreams"]) == {name for name, _l, _f in ALLOCATION}
    assert set(block["inventory"]) == set(window["inventory"]), (
        set(block["inventory"]) ^ set(window["inventory"]))
    assert set(block["inventory"]) == {name for name, _l, _c in INVENTORY}
    for name, dim in block["workstreams"].items():
        assert set(dim) == set(window["workstreams"][name]), (name, dim)
    assert set(block["effort"]) == set(window["effort"]), set(block["effort"]) ^ set(window["effort"])
    assert set(block["prior"]) == set(window["prior"]), set(block["prior"]) ^ set(window["prior"])
    assert block["schema"] == window["schema"] and block["session"] == window["session"]


def test_a_block_reports_its_own_span_and_the_two_boundary_reasons():
    """Epoch seconds, the unit `since_ts` and `covers` are in — a consumer that has to parse an
    ISO string back into a float to advance its cursor is being handed the wrong type. The two
    reasons come from the closed `blocks.REASONS` vocabulary and are never interchangeable:
    `budget` is only "we had to cut somewhere" while `idle` claims there was no work at all."""
    store, path = _fixture()
    cut = _cut(store, path)
    out = blockdigest.digest_blocks(store, path, now=cut[-1].end + blockdigest.IDLE_SECONDS)
    for b, cb in zip(out["blocks"], cut):
        assert isinstance(b["start"], float) and isinstance(b["end"], float), b
        assert (b["start"], b["end"]) == (cb.start, cb.end), (b, cb)
        assert b["start_reason"] in blocks_mod.REASONS and b["end_reason"] in blocks_mod.REASONS
        assert abs(b["block_minutes"] - (cb.end - cb.start) / 60.0) < 1e-6, b


def test_the_dynamics_and_prior_blocks_are_scoped_to_the_block():
    """Both are composition over BOUNDS, and the bounds are the block's. The dynamics sizer is
    confined to the block, so it can never open a retention surface this path has not checked; the
    prior is cut at the block's START, half-open, so no evidence is counted on both sides and the
    block is never inside its own frame of reference."""
    store, path = _fixture()
    cut = _cut(store, path)
    out = blockdigest.digest_blocks(store, path, now=cut[-1].end + blockdigest.IDLE_SECONDS)
    first, last = out["blocks"][0], out["blocks"][-1]

    dyn = first["dynamics"]
    assert _epoch(dyn["baseline_start"]) >= cut[0].start, dyn
    assert _epoch(dyn["slice_end"]) <= cut[0].end, dyn
    assert dyn["slice_minutes"] + dyn["baseline_minutes"] <= first["block_minutes"] + 1e-6, dyn

    # A session's FIRST block has nothing before it, so every prior dimension is `absent` and
    # every contrast is None. CONTRAST, NEVER FALLBACK: the prior does not supply a value.
    assert all(d["status"] == "absent" and d["agrees"] is None
               for d in first["prior"]["dimensions"].values()), first["prior"]
    # A later block does have one, and it is a real rollup rather than a copy of the block's.
    assert any(d["status"] != "absent" for d in last["prior"]["dimensions"].values()), last["prior"]


def test_a_block_whose_evidence_was_pruned_is_dropped_not_answered_from_what_survived():
    """`Store.window_rows` serves an excluded-slot query entirely from `event` (a `bin` row has no
    slot to filter on) and this module always excludes the reconcile slot, so a pruned block would
    come back with a confident share computed off whatever fraction of its rows outlived the
    horizon. That is a plausible wrong number, which is worse than no number.

    DROPPED rather than raised, unlike `/analyze`'s 410: this route answers about a RANGE, and one
    unanswerable block must not discard the answerable ones behind it — the same call `tick.py`
    makes."""
    store, path = _fixture()
    cut = _cut(store, path)
    now = cut[-1].end + blockdigest.IDLE_SECONDS
    assert len(blockdigest.digest_blocks(store, path, now=now)["blocks"]) == 3
    store.note_pruned("event", cut[1].start, 1)          # the floor now sits inside the session
    out = blockdigest.digest_blocks(store, path, now=now)
    assert [b["start"] for b in out["blocks"]] == [cut[1].start, cut[2].start], _spans(out)


def test_the_response_carries_no_prompt_text():
    """Coordinates only. The fixture's prompts say "build the thing" and "now ship it" and its
    tool inputs name absolute paths; neither may appear anywhere in the payload, at any depth.
    `inventory.files` publishes WORKSPACE-RELATIVE paths, which is the whole reason `reconcile()`
    resolves them before this module ever sees a value."""
    store, path = _fixture()
    cut = _cut(store, path)
    out = blockdigest.digest_blocks(store, path, now=cut[-1].end + blockdigest.IDLE_SECONDS,
                                    prompt_ids=[P1, P2])
    blob = json.dumps(out)
    for forbidden in ("build the thing", "now ship it", "/workspace/", "cwd", "message"):
        assert forbidden not in blob, forbidden


# --- the endpoint ---------------------------------------------------------------------------------

def test_the_blocks_endpoint_is_confined_to_the_analyze_roots_and_answers_off_the_runner():
    """The same allowlist `/analyze`, `/ingest` and `/tick` use. The sidecar has NO auth, so an
    unconfined `/blocks` is a confused deputy: it opens a path as the daemon's user and answers
    with that user's workspaces, branches and named terms. 403, not 404 — a rejected path and an
    unresolvable one are different facts.

    Also the only assertion here that the route answers with no inference worker ever spawned,
    which is what makes this an analysis service rather than a GLiNER2 wrapper."""
    path = _transcript("fixture-blk-endpoint")
    root = os.path.dirname(path)
    os.environ["KELD_ANALYZE_ROOTS"] = root
    os.environ["KELD_HOME"] = os.path.join(root, "keld-home")
    import app.main as m

    # Ingested into the APP'S store, resolved through KELD_HOME, rather than into a store of our
    # own: the endpoint reads `_store()` and nothing else, so a fixture store beside the
    # transcript would leave it answering about an empty series and the test would pass on a
    # payload that proves nothing.
    store = m._store()
    ingest_file(store, path, None, None)

    outside = os.path.join(_TMP, "elsewhere.jsonl")
    open(outside, "w").close()
    try:
        asyncio.run(m.blocks(m.BlocksIn(path=outside)))
        raise AssertionError("a path outside the roots must be refused")
    except m.HTTPException as exc:
        assert exc.status_code == 403, exc.status_code

    cut = _cut(store, path)
    body = m.BlocksIn(path=path, prompts=[P1, P2],
                      now=cut[-1].end + blockdigest.IDLE_SECONDS)
    out = asyncio.run(m.blocks(body))
    assert [b["end_reason"] for b in out["blocks"]] == ["budget", "idle", "session_end"], \
        _spans(out)
    # The status rides every block for `/analyze`'s reason: an empty `named_terms` is otherwise
    # not self-describing — a block that held no terms and a level that never ran look identical.
    assert all("named_terms_status" in b for b in out["blocks"]), out["blocks"][0].keys()
    assert m._state.get("wm") is None, "the endpoint must not spawn an inference worker"


if __name__ == "__main__":
    fails = 0
    for name, fn in sorted(globals().items()):
        if name.startswith("test_") and callable(fn):
            try:
                fn()
                print("ok   %s" % name)
            except Exception as exc:                       # noqa: BLE001 - a runner reports all
                fails += 1
                import traceback
                print("FAIL %s: %s" % (name, exc))
                traceback.print_exc()
    print("%d failure(s)" % fails)
    sys.exit(1 if fails else 0)
