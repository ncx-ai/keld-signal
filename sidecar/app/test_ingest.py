"""Incremental ingest of a transcript from a byte offset.

  cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_ingest.py

THE contract here is EQUIVALENCE, and it is not obvious enough to assume. A tail parse is not
automatically equal to a full parse, because two things in this package are retroactive:

  - `workspace.scan_workspace` is a pre-pass over the WHOLE file. A repo-level marker touched in
    the last minute identifies the root of the first (its own docstring says so), so evidence
    that arrives late changes how EARLIER turns resolve — the `workspace`, `workspace_evidence`,
    `remote` and `repo_mentioned` levels, and through them `reconcile`'s grouping.
  - `reconcile.reconcile` resolves prose-mentioned paths against every path a tool DECLARED. A
    declaration in the tail can reattribute a prose path from the head, and `file`/`dir`/`ext`/
    `lang`/`component` rows come only from it.

So the load-bearing tests below ingest a file in N chunks and assert the stored events are
IDENTICAL to ingesting it in one pass — on the committed fixture corpus, and on adversarial
transcripts built specifically so the evidence arrives after the turns it governs. A weaker test
(the store returns *something*, or the counts are close) would let a permanently wrong series
ship silently, since nothing downstream ever re-derives these rows.
"""
import json
import os
import sys
import tempfile

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from app.analysis import window
from app.analysis.ingest import COMPONENT_DEPTH, ingest_file, new_evidence, session_of
from app.analysis.levels import events_for_turns
from app.analysis.reconcile import reconcile
from app.analysis.store import open_store
from app.analysis.transcript import iter_turns

FIXTURES = os.path.join(os.path.dirname(os.path.abspath(__file__)),
                        "analysis", "testdata", "fixture-corpus", "projects")


# ---------------------------------------------------------------- helpers

def _fixture_lines(name):
    d = os.path.join(FIXTURES, name)
    f = [x for x in sorted(os.listdir(d)) if x.endswith(".jsonl")][0]
    with open(os.path.join(d, f)) as fh:
        return name, f, fh.readlines()


def _laid_out(tmp, projdir, fname, lines):
    """Write `lines` at `<tmp>/projects/<projdir>/<fname>`.

    The directory layout is load-bearing, not cosmetic: `root` is `dirname(dirname(path))` and
    `projdir` is the launch cwd with "/" replaced by "-", which `workspace.launch_dir` decodes to
    resolve the workspace. A flat temp copy would resolve to a different workspace than the
    fixture does and the test would be measuring the wrong file.
    """
    d = os.path.join(tmp, "projects", projdir)
    os.makedirs(d, exist_ok=True)
    p = os.path.join(d, fname)
    with open(p, "w") as fh:
        fh.writelines(lines)
    return p


def _dump(store, path):
    """Every stored event, bin and turn magnitude, ordered — the whole observable content.

    `turn_magnitude` is here for the same reason `event` is, and it was added late: a magnitude
    is keyed on the batch ordinal exactly as an event is, so it is subject to the same
    retroactive-and-therefore-unequal risk, and NOTHING else in this file looked at it. The
    defect that proved the point: `mag/request_tokens` is emitted once per `requestId`, and the
    dedup that makes "once" true was local to one `events_for_turns` call — so a request whose
    assistant lines straddled a chunk boundary stored a SECOND row at a different ordinal and the
    window's spend summed both. See `_multi_line_request_lines`.

    `path` is the transcript the store holds. Its key is replaced by `"<session>"`, because the
    store keys on a digest of the transcript's ABSOLUTE path (`ingest.session_of`) and the
    comparisons below build the reference store from a copy of the same transcript at a DIFFERENT
    path — so the raw key differs by construction and would swamp every real difference. That
    loses nothing the column ever carried: each of these stores holds exactly one transcript, so
    the value was a constant on both sides.

    "Exactly one" is ASSERTED, not assumed, and the assertion is the reason the substitution is
    safe. Silently rewriting whatever session a row happens to carry would normalise away the one
    class of defect this column can show — rows landing under a key that is not this
    transcript's — so a foreign key is a failure here rather than a value that gets renamed.

    Compared as a whole rather than through `rollup_window`, because a rollup would hide a
    difference that cancels out across bins, and the point of these tests is that nothing
    differs at all.

    Summed over `source_line`, which is the ONE column that legitimately differs: it records
    which batch a row arrived in, so a four-chunk ingest necessarily spreads rows across four
    ordinals where a single pass uses one. Everything a reader can observe -- `rollup_window`
    sums over it too -- is `(session, ts, level, ref) -> n`, and a genuine double-count still
    shows up here as a doubled `n` rather than being hidden.
    """
    c = store._conn()
    key = session_of(path)
    for table in ("event", "bin", "turn_magnitude", "bin_offset"):
        seen = {r[0] for r in c.execute(f"SELECT DISTINCT session FROM {table}")}
        assert seen <= {key}, (
            f"{table} holds session(s) that are not {os.path.basename(path)}'s: {seen - {key}}")
    canon = lambda r: ("<session>",) + tuple(r[1:])
    ev = [canon(r) for r in c.execute("SELECT session, ts, level, ref, SUM(n) FROM event "
                                      "GROUP BY session, ts, level, ref "
                                      "ORDER BY session, ts, level, ref")]
    bn = [canon(r) for r in c.execute("SELECT session, bin_ts, level, ref, n FROM bin "
                                      "ORDER BY session, bin_ts, level, ref")]
    # Summed over `source_line` on the same argument as `event` above, and the once-per-request
    # magnitudes still show through: a duplicate lands at the ts of a DIFFERENT transcript line,
    # so it appears as an extra (ts, kind) key rather than being folded away.
    mg = [canon(r) for r in c.execute("SELECT session, ts, kind, SUM(value) FROM turn_magnitude "
                                      "GROUP BY session, ts, kind "
                                      "ORDER BY session, ts, kind")]
    # `bin_offset` is MIN()-on-conflict, not summed, so a chunked ingest that touches a bin
    # from several batches must still land on the SAME smallest offset a whole-file ingest
    # would record -- there is no "over" argument the way there is for a count.
    bo = [canon(r) for r in c.execute('SELECT session, bin_ts, "offset" FROM bin_offset '
                                      "ORDER BY session, bin_ts")]
    return sorted(ev), sorted(bn), sorted(mg), sorted(bo)


def _full_parse_rollup(path, nlp=None):
    """The reference answer: what `analyze.py` computes for the WHOLE file, in one pass.

    Deliberately a copy of `analyze_window`'s derivation (`root`, `repo_root=()`,
    `events_for_turns` then `reconcile`) rather than a call to it, because `analyze_window` takes
    a prompt id and a span; the whole-file rollup is what ingest must reproduce.
    """
    root = os.path.dirname(os.path.dirname(path))
    rows, pending, _n = events_for_turns(list(iter_turns(path)), path, root, (), nlp)
    rec, _stats = reconcile(pending, COMPONENT_DEPTH)
    return window.rollup(rows + rec)


def _ingest_in_chunks(tmp, projdir, fname, lines, cuts):
    """Ingest `lines` in the pieces `cuts` describes, appending to the same path each time."""
    store = open_store(os.path.join(tmp, "chunked.db"))
    path = None
    results = []
    for end in cuts:
        path = _laid_out(tmp, projdir, fname, lines[:end])
        results.append(ingest_file(store, path))
    return store, path, results


def _ingest_whole(tmp, projdir, fname, lines):
    store = open_store(os.path.join(tmp, "whole.db"))
    path = _laid_out(os.path.join(tmp, "w"), projdir, fname, lines)
    return store, path, ingest_file(store, path)


# ---------------------------------------------------------------- synthetic transcripts

def _turn(ts, kind="user", text="hello", cwd="/workspace/kexample/demo", blocks=None,
          uuid=None):
    o = {"type": kind, "timestamp": ts, "cwd": cwd, "gitBranch": "main",
         "message": {"content": ([{"type": "text", "text": text}] + (blocks or []))}}
    if kind == "assistant":
        o["message"]["model"] = "claude-opus-5"
    if uuid:
        o["uuid"] = uuid
    return o


def _tool(name, **inp):
    return {"type": "tool_use", "name": name, "input": inp}


def _late_evidence_lines():
    """A transcript whose workspace evidence arrives AFTER the turns it governs.

    Turn 1 runs before any marker file has been touched, so on a tail-only parse it resolves by
    "the cwd as given, no other signal" [low]. Turn 3 reads `CLAUDE.md` at the checkout root — a
    REPO_MARKER — which in a full parse re-resolves turn 1 to "repo-level marker" [high]. Turn 4
    names a git remote in prose, which a full parse attributes back to turn 1 as well.

    This is the exact shape `scan_workspace`'s docstring warns about, written down as a file.
    """
    cwd = "/workspace/kexample/demo/services/api"
    return [
        _turn("2026-08-12T09:00:00Z", "user", "start on the retry path"),
        _turn("2026-08-12T09:00:30Z", "assistant", "looking",
              cwd=cwd, blocks=[_tool("Bash", command="go test ./internal/retry/...")]),
        _turn("2026-08-12T09:02:00Z", "assistant", "reading the guide",
              cwd=cwd, blocks=[_tool("Read", file_path="/workspace/kexample/demo/CLAUDE.md")]),
        _turn("2026-08-12T09:04:00Z", "user",
              "see github.com/kexample/demo for the upstream", cwd=cwd),
        _turn("2026-08-12T09:07:00Z", "assistant", "done", cwd=cwd),
    ]


def _late_declaration_lines():
    """A transcript where a prose path is MENTIONED before the tool call that DECLARES it.

    Turn 2 mentions `tests/test_retry.py` in a shell command — prose, with no authoritative base.
    Turn 4 declares `services/api/tests/test_retry.py` via a tool's `file_path`. In a full parse
    reconcile() merges the two (the "split share" defect its docstring records); in a per-chunk
    parse the head chunk has no declaration to match and emits the prose path as written.
    """
    cwd = "/workspace/kexample/demo"
    return [
        _turn("2026-08-12T10:00:00Z", "user", "run the retry tests", cwd=cwd),
        _turn("2026-08-12T10:00:30Z", "assistant", "running",
              cwd=cwd, blocks=[_tool("Bash", command="pytest tests/test_retry.py -x")]),
        _turn("2026-08-12T10:02:00Z", "user", "now fix it", cwd=cwd),
        _turn("2026-08-12T10:03:00Z", "assistant", "editing",
              cwd=cwd, blocks=[_tool("Edit",
                                     file_path=cwd + "/services/api/tests/test_retry.py")]),
        _turn("2026-08-12T10:06:00Z", "user", "thanks", cwd=cwd),
    ]


def _stable_lines(n=6, start=0):
    """A transcript whose workspace evidence is COMPLETE IN ITS FIRST TURN, and so never causes
    a reparse however it is chunked.

    The offset mechanics below (append, torn line, truncation, rotation, watermark) are about
    bytes, and a transcript that legitimately reparses partway through — as the fixture corpus
    does, and as `test_late_workspace_evidence_still_equals_a_full_parse` requires it to —
    would mask exactly the thing they are checking. So the marker read and the remote mention
    both land in turn 1: after that, later turns add tool calls and paths but no evidence whose
    derived answer can move.
    """
    cwd = "/workspace/kexample/demo"
    objs = [_turn("2026-08-13T08:00:00Z", "assistant",
                  "starting, upstream is github.com/kexample/demo", cwd=cwd,
                  blocks=[_tool("Read", file_path=cwd + "/CLAUDE.md")])]
    for i in range(1, n):
        m = start + i
        objs.append(_turn(f"2026-08-13T08:{m:02d}:00Z",
                          "user" if i % 2 else "assistant", f"step {i}", cwd=cwd,
                          blocks=[_tool("Bash", command=f"go test ./internal/pkg{i}/...")]))
    return _lines_of(objs)


def _asst_of_request(ts, rid, text, blocks=None, cwd="/workspace/kexample/demo"):
    """An assistant line belonging to request `rid`, repeating that request's `usage`.

    That repetition is how Claude Code writes a transcript, not a quirk of this fixture: one API
    request emits several assistant lines and EVERY one of them carries the same `usage` object.
    `levels.events_for_turns` therefore emits `mag/tokens` on all of them (the rollup weight) and
    `mag/request_tokens` on the FIRST only (the spend), and it is that "first only" that a chunk
    boundary can break.
    """
    o = _turn(ts, "assistant", text, cwd=cwd, blocks=blocks)
    o["requestId"] = rid
    o["message"]["usage"] = {"input_tokens": 400, "output_tokens": 60,
                             "cache_creation_input_tokens": 0,
                             "cache_read_input_tokens": 0}
    return o


def _multi_line_request_lines():
    """A transcript whose API REQUESTS span several assistant lines each.

    Nothing else in this file carries a `requestId` at all, so `events_for_turns`' once-per-request
    dedup was a structural no-op across the whole chunked-equivalence suite and the defect it
    guards against could not be seen. Here request `req-A` owns three consecutive assistant lines
    and `req-B` owns two, so ANY cut inside either of them re-offers a request whose spend was
    already recorded — and a cut one line at a time (this suite's default) makes every one of
    those cuts.

    The evidence is complete in the first turn, deliberately: a reparse partway through would
    reset the dedup and mask exactly what is being measured (the same reason `_stable_lines`
    front-loads its evidence).
    """
    cwd = "/workspace/kexample/demo"
    return _lines_of([
        _turn("2026-08-14T11:00:00Z", "user", "start on the ledger", cwd=cwd),
        _asst_of_request("2026-08-14T11:00:10Z", "req-A", "reading",
                         blocks=[_tool("Read", file_path=cwd + "/CLAUDE.md")], cwd=cwd),
        _asst_of_request("2026-08-14T11:00:20Z", "req-A", "still the same request",
                         blocks=[_tool("Bash", command="go test ./internal/ledger/...")], cwd=cwd),
        _asst_of_request("2026-08-14T11:00:30Z", "req-A", "and still",
                         blocks=[_tool("Bash", command="go build ./...")], cwd=cwd),
        _turn("2026-08-14T11:01:00Z", "user", "now the retry path", cwd=cwd),
        _asst_of_request("2026-08-14T11:01:10Z", "req-B", "editing",
                         blocks=[_tool("Edit", file_path=cwd + "/internal/retry/queue.go",
                                       old_string="x" * 40, new_string="y" * 60)], cwd=cwd),
        _asst_of_request("2026-08-14T11:01:20Z", "req-B", "second line of B",
                         blocks=[_tool("Bash", command="go vet ./...")], cwd=cwd),
        _turn("2026-08-14T11:02:00Z", "user", "thanks", cwd=cwd),
    ])


def _lines_of(objs):
    return [json.dumps(o, separators=(",", ":")) + "\n" for o in objs]


# ---------------------------------------------------------------- the equivalence tests

def _assert_chunked_equals_whole(objs_or_lines, projdir="-workspace-kexample-demo",
                                 fname="0badc0de-0000-0000-0000-000000000000.jsonl",
                                 cuts=None):
    lines = (objs_or_lines if isinstance(objs_or_lines[0], str)
             else _lines_of(objs_or_lines))
    n = len(lines)
    assert n >= 4, "need at least three chunk boundaries"
    cuts = cuts or list(range(1, n + 1))
    with tempfile.TemporaryDirectory() as tmp:
        cs, cpath, results = _ingest_in_chunks(tmp, projdir, fname, lines, cuts)
        ws, wpath, _r = _ingest_whole(tmp, projdir, fname, lines)
        got, want = _dump(cs, cpath), _dump(ws, wpath)
        assert got == want, (
            f"chunked ingest ({len(cuts)} chunks) differs from a single pass\n"
            f"  only in chunked: {sorted(set(got[0]) - set(want[0]))[:8]}\n"
            f"  only in whole:   {sorted(set(want[0]) - set(got[0]))[:8]}\n"
            f"  mag only in chunked: {sorted(set(got[2]) - set(want[2]))[:8]}\n"
            f"  mag only in whole:   {sorted(set(want[2]) - set(got[2]))[:8]}")
        # And both must equal what the existing one-pass parse path computes.
        session = session_of(cpath)
        served = cs.rollup_window(session, 0, 4e9)
        assert served == _full_parse_rollup(wpath), "store does not match a full parse"
        cs.close(); ws.close()
        return results


def test_chunked_ingest_of_a_fixture_transcript_equals_one_pass():
    """The committed fixture corpus, one line at a time — three chunk boundaries, and the first
    two split the 09:00-09:05 bin, so a partial bin is re-rolled by a later chunk."""
    projdir, fname, lines = _fixture_lines("-workspace-fixture-corpus-anders-aurora-ledger")
    _assert_chunked_equals_whole(lines, projdir, fname)


def test_chunked_ingest_of_the_second_fixture_equals_one_pass():
    projdir, fname, lines = _fixture_lines("-workspace-fixture-corpus-priya-beacon-api")
    _assert_chunked_equals_whole(lines, projdir, fname)


def test_late_workspace_evidence_still_equals_a_full_parse():
    """The retroactive case: a repo marker and a remote arrive after the turns they re-resolve."""
    _assert_chunked_equals_whole(_late_evidence_lines())


def test_late_declaration_still_equals_a_full_parse():
    """The retroactive case for reconcile: a prose path is declared only in a later chunk."""
    _assert_chunked_equals_whole(_late_declaration_lines())


def test_a_request_spanning_a_chunk_boundary_is_costed_once():
    """The spend series must not grow with how the file was chunked.

    `mag/request_tokens` is recorded once per `requestId`, so a request whose assistant lines are
    split across batches must still contribute ONE row. Cut a line at a time, both of this
    fixture's multi-line requests straddle boundaries, and the whole-file ingest is the oracle.
    """
    _assert_chunked_equals_whole(_multi_line_request_lines())


def test_a_request_split_across_three_batches_is_costed_once():
    """A request can span more than two batches, so remembering only the previous batch's ids is
    not enough. `req-A`'s three lines land in three different chunks here."""
    lines = _multi_line_request_lines()
    _assert_chunked_equals_whole(lines, cuts=[2, 3, 4, len(lines)])


def test_a_state_from_an_older_layout_reparses_and_clears_stale_magnitudes():
    """The REPAIR path for the over-count above, and the reason `STATE_VERSION` was bumped.

    A store written by the code whose request dedup was per-call already holds the duplicate
    `mag/request_tokens` rows, and nothing recomputes them: the `turn_magnitude` key carries the
    batch ordinal, so each duplicate is a legitimate member of its own batch and no upsert
    collapses it. The version bump is what fixes those stores — a state below `STATE_VERSION` is
    unusable, `_ingest_locked` reparses whole, and `clear_session` drops the magnitudes before
    they are re-derived. Simulated here by planting a duplicate row and downgrading the stored
    `v`, which is exactly the state such a store is in.
    """
    lines = _multi_line_request_lines()
    with tempfile.TemporaryDirectory() as tmp:
        st, p, _r = _ingest_whole(tmp, "-workspace-kexample-demo",
                                  "0badc0de-0000-0000-0000-000000000000.jsonl", lines)
        good = _dump(st, p)[2]
        c = st._conn()
        ts, kind, val = [r for r in good if r[2] == "request_tokens"][0][1:]
        c.execute("INSERT INTO turn_magnitude(session, source_line, ts, kind, value) "
                  "VALUES (?,?,?,?,?)", (session_of(p), 999, ts + 5.0, kind, val))
        raw = st.parse_state(p)
        assert raw["v"] == 4, raw["v"]
        raw["v"] = 3
        st.set_parse_state(p, raw)
        assert _dump(st, p)[2] != good, "the planted duplicate did not land"

        ingest_file(st, p)
        assert _dump(st, p)[2] == good, "the reparse did not clear the stale magnitude"
        st.close()


def test_chunk_boundary_splitting_a_five_minute_bin_equals_one_pass():
    """An uneven split whose boundary lands inside a bin, not between two."""
    _assert_chunked_equals_whole(_late_evidence_lines(), cuts=[2, 3, 5])


def _tied_remotes_lines():
    """Six remotes named one per turn, each mentioned exactly ONCE.

    `events_for_turns` emits `repo_mentioned` from `remotes.most_common(3)`, and `Counter`
    breaks a tie by INSERTION ORDER. With every count equal the whole ranking IS insertion
    order, so the accumulated Counter has to be reloaded from the parse state in the order a
    single pass would have built it — a plain dict round-trip, or any re-sorting, silently picks
    a different three.

    Six rather than four, and this is the point the first version of this test missed: the three
    are emitted as three independent rows, so their order among themselves is invisible in the
    store. Only WHICH three differs, and that needs strictly more than three tied candidates
    already carried in the state. Nor may the extras displace the top three, or every batch
    would reparse and the reloaded state would never be exercised at all — which is exactly what
    ties guarantee.
    """
    cwd = "/workspace/kexample/demo"
    names = ["alpha", "bravo", "charlie", "delta", "echo", "foxtrot"]
    objs = [_turn("2026-08-14T07:00:00Z", "assistant", "setting up", cwd=cwd,
                  blocks=[_tool("Read", file_path=cwd + "/CLAUDE.md")])]
    for i, name in enumerate(names):
        # The remote has to be named on a line CARRYING A TOOL_USE BLOCK: `scan_tool_use`
        # only runs REMOTE_REPO over `command + text_of(content)` inside the tool_use loop, so
        # a remote mentioned in bare prose is never evidence at all (measured the hard way —
        # the first version of this fixture recorded zero remotes and the test proved nothing).
        objs.append(_turn(f"2026-08-14T07:{i + 1:02d}:00Z", "assistant",
                          f"comparing against github.com/kexample/{name}", cwd=cwd,
                          blocks=[_tool("Bash",
                                        command=f"git ls-remote github.com/kexample/{name}")]))
    objs.append(_turn("2026-08-14T07:11:00Z", "assistant", "done", cwd=cwd))
    return _lines_of(objs)


def test_tied_remotes_keep_their_order_across_batches():
    _assert_chunked_equals_whole(_tied_remotes_lines())


def test_identical_turns_at_one_timestamp_survive_a_chunk_boundary():
    """Two turns sharing a timestamp AND a reference are one summed row in a single pass and two
    rows of 1 across a boundary — the same total either way, but only because each batch writes
    under its own line ordinal. Let two batches share an ordinal and the second row does not add
    to the first, it REPLACES it (the upsert is `SET n = excluded.n`, deliberately, so a replayed
    tail cannot inflate a window), and the evidence quietly halves.
    """
    cwd = "/workspace/kexample/demo"
    objs = [_turn("2026-08-15T06:00:00Z", "assistant", "setup", cwd=cwd,
                  blocks=[_tool("Read", file_path=cwd + "/CLAUDE.md")])]
    for _ in range(4):                                   # four turns, one instant, one tool
        objs.append(_turn("2026-08-15T06:01:00Z", "assistant", "same instant", cwd=cwd,
                          blocks=[_tool("Bash", command="go build ./...")]))
    _assert_chunked_equals_whole(objs)


def test_the_watermark_never_moves_backwards():
    """Measured on the real corpus, 9 of 9,937 turns (0.09%) carry a timestamp earlier than the
    line before them. A watermark that simply took the batch's last turn would RETREAT on one of
    those — and a retreating watermark makes windows that were already answerable stop being
    answerable, which downstream reads as a transcript that lost data."""
    lines = _stable_lines()
    early = _lines_of([_turn("2026-08-13T07:00:00Z", "user", "an out-of-order line",
                             cwd="/workspace/kexample/demo")])
    with tempfile.TemporaryDirectory() as tmp:
        st = open_store(os.path.join(tmp, "s.db"))
        p = _laid_out(tmp, PROJ, FNAME, lines)
        high = ingest_file(st, p).watermark_ts
        p = _laid_out(tmp, PROJ, FNAME, lines + early)
        r = ingest_file(st, p)
        assert r.watermark_ts == high, (high, r.watermark_ts)
        assert st.watermark(p) == high
        st.close()


def test_a_reparse_replaces_rather_than_duplicates():
    """A reparse re-reads lines already stored under a different batch ordinal. Unless the
    session's events are cleared first, every row is stored twice and every window doubles."""
    results = _assert_chunked_equals_whole(_late_evidence_lines())
    assert any(r.reparsed for r in results[1:]), \
        "late evidence should have forced at least one reparse: " + repr(results)


# ---------------------------------------------------------------- the offset mechanics

PROJ, FNAME = "-workspace-kexample-demo", "0badc0de-1111-2222-3333-444455556666.jsonl"


def test_ingesting_twice_with_no_change_parses_zero_new_lines():
    projdir, fname, lines = PROJ, FNAME, _stable_lines()
    with tempfile.TemporaryDirectory() as tmp:
        st = open_store(os.path.join(tmp, "s.db"))
        p = _laid_out(tmp, projdir, fname, lines)
        first = ingest_file(st, p)
        assert first.new_lines == len(lines), first
        again = ingest_file(st, p)
        assert again.new_lines == 0, again
        assert again.reparsed is False, again
        assert again.watermark_ts == first.watermark_ts, (first, again)
        st.close()


def test_appending_parses_only_the_appended_lines():
    projdir, fname, lines = PROJ, FNAME, _stable_lines()
    with tempfile.TemporaryDirectory() as tmp:
        st = open_store(os.path.join(tmp, "s.db"))
        p = _laid_out(tmp, projdir, fname, lines[:3])
        assert ingest_file(st, p).new_lines == 3
        p = _laid_out(tmp, projdir, fname, lines)
        r = ingest_file(st, p)
        assert r.new_lines == len(lines) - 3, r
        assert r.reparsed is False, r
        st.close()


def test_a_partially_written_trailing_line_is_not_consumed():
    """The watcher can signal mid-write. A JSONL line without its newline is not a line yet;
    consuming it would advance the offset past a record that was never parsed."""
    projdir, fname, lines = PROJ, FNAME, _stable_lines()
    with tempfile.TemporaryDirectory() as tmp:
        st = open_store(os.path.join(tmp, "s.db"))
        torn = lines[:2] + [lines[2][:40]]           # no trailing newline
        p = _laid_out(tmp, projdir, fname, torn)
        r = ingest_file(st, p)
        assert r.new_lines == 2, r
        assert st.ingest_state(p)["offset"] == sum(len(x) for x in lines[:2]), \
            st.ingest_state(p)
        p = _laid_out(tmp, projdir, fname, lines)    # the line completes, plus the rest
        r2 = ingest_file(st, p)
        assert r2.new_lines == len(lines) - 2, r2
        assert r2.reparsed is False, r2
        st.close()


def test_a_file_that_shrank_below_its_offset_triggers_a_full_reparse():
    projdir, fname, lines = PROJ, FNAME, _stable_lines()
    with tempfile.TemporaryDirectory() as tmp:
        st = open_store(os.path.join(tmp, "s.db"))
        p = _laid_out(tmp, projdir, fname, lines)
        ingest_file(st, p)
        p = _laid_out(tmp, projdir, fname, lines[:2])
        r = ingest_file(st, p)
        assert r.reparsed is True, r
        assert r.new_lines == 2, r
        # And the store now holds the truncated file, not the union of both.
        ref = open_store(os.path.join(tmp, "ref.db"))
        p2 = _laid_out(os.path.join(tmp, "r"), projdir, fname, lines[:2])
        ingest_file(ref, p2)
        assert _dump(st, p) == _dump(ref, p2), "a reparse must replace, not merge"
        st.close(); ref.close()


def test_changed_head_bytes_trigger_a_full_reparse():
    """Rotation: the path is the same and the file is no smaller, but it is a different file."""
    projdir, fname, lines = PROJ, FNAME, _stable_lines()
    other = _stable_lines(14, start=30)               # a different, bigger file at the same path
    with tempfile.TemporaryDirectory() as tmp:
        st = open_store(os.path.join(tmp, "s.db"))
        p = _laid_out(tmp, projdir, fname, lines)
        ingest_file(st, p)
        p = _laid_out(tmp, projdir, fname, other)
        assert os.path.getsize(p) >= sum(len(x) for x in lines), \
            "rotation must be detected by the head bytes, not by the file having shrunk"
        r = ingest_file(st, p)
        assert r.reparsed is True, r
        assert r.new_lines == len(other), r
        st.close()


def test_the_watermark_is_the_last_ingested_turn_and_never_past_it():
    projdir, fname, lines = PROJ, FNAME, _stable_lines()
    stamps = [json.loads(x)["timestamp"] for x in lines]
    with tempfile.TemporaryDirectory() as tmp:
        st = open_store(os.path.join(tmp, "s.db"))
        for k in range(1, len(lines) + 1):
            p = _laid_out(tmp, projdir, fname, lines[:k])
            r = ingest_file(st, p)
            assert r.watermark_ts == max(stamps[:k]), (k, r.watermark_ts)
            assert st.watermark(p) == r.watermark_ts
            assert r.watermark_ts <= max(stamps), r
        st.close()


def test_an_ingest_that_adds_no_turns_leaves_the_watermark_where_it_was():
    """A tail of non-turn lines (a `tool_result`, a summary record) advances the byte offset but
    not the watermark: no turn was ingested, so nothing new is answerable."""
    projdir, fname, lines = PROJ, FNAME, _stable_lines()
    with tempfile.TemporaryDirectory() as tmp:
        st = open_store(os.path.join(tmp, "s.db"))
        p = _laid_out(tmp, projdir, fname, lines)
        first = ingest_file(st, p)
        filler = json.dumps({"type": "summary", "summary": "x"}) + "\n"
        p = _laid_out(tmp, projdir, fname, lines + [filler])
        r = ingest_file(st, p)
        assert r.watermark_ts == first.watermark_ts, (first, r)
        assert st.ingest_state(p)["offset"] == os.path.getsize(p), st.ingest_state(p)
        st.close()


# ---------------------------------------------------------------- privacy

def test_no_prompt_text_or_message_content_reaches_the_store():
    """The store holds reference EVENTS, so a sentence a user wrote must appear nowhere in the
    database file — not in `event`, and not in the parse state this module newly persists.

    The sentence is ordinary lowercase prose on purpose. `term` is the one level drawn from
    message text (store.py flags it as the sensitive one) and it legitimately keeps single
    NAME-SHAPED tokens — a CamelCase word, a hyphenated identifier, a dotted vendor. What must
    never survive is the message itself: the prose around those tokens, which is what carries
    the meaning and what the privacy invariant is actually about.
    """
    marker = "quokka pineapple rutabaga before the weekend deploy"
    objs = _late_evidence_lines()
    objs[0]["message"]["content"][0]["text"] = marker
    with tempfile.TemporaryDirectory() as tmp:
        st = open_store(os.path.join(tmp, "s.db"))
        p = _laid_out(tmp, "-workspace-kexample-demo", "0badc0de-0000.jsonl", _lines_of(objs))
        ingest_file(st, p)
        st.close()
        for suffix in ("", "-wal", "-shm"):
            f = os.path.join(tmp, "s.db" + suffix)
            if os.path.exists(f):
                with open(f, "rb") as fh:
                    blob = fh.read()
                for phrase in (marker, "quokka pineapple", "rutabaga before"):
                    assert phrase.encode() not in blob, (f, phrase)


def test_ingest_never_stores_say_or_tok_rows():
    projdir, fname, lines = _fixture_lines("-workspace-fixture-corpus-anders-aurora-ledger")
    with tempfile.TemporaryDirectory() as tmp:
        st = open_store(os.path.join(tmp, "s.db"))
        p = _laid_out(tmp, projdir, fname, lines)
        ingest_file(st, p)
        levels = {r[0] for r in st._conn().execute("SELECT DISTINCT level FROM event")}
        assert not ({"user", "user_echo", "asst", "asst_think", "out", "in_fresh",
                     "in_cached"} & levels), levels
        st.close()


# ---------------------------------------------------------------- misc

def test_a_transcript_with_no_turns_at_all_is_checkpointed_not_skipped():
    """Otherwise every ingest re-reads the same turnless prefix forever."""
    with tempfile.TemporaryDirectory() as tmp:
        st = open_store(os.path.join(tmp, "s.db"))
        junk = [json.dumps({"type": "summary", "summary": "x"}) + "\n" for _ in range(3)]
        p = _laid_out(tmp, "-workspace-kexample-demo", "0badc0de-0000.jsonl", junk)
        r = ingest_file(st, p)
        assert r.new_lines == 0 and r.watermark_ts is None, r
        assert st.ingest_state(p)["offset"] == os.path.getsize(p)
        st.close()


def test_a_checkpoint_without_carried_state_reparses_rather_than_tail_parsing_blind():
    """A store written before `parse_state` existed has a valid-looking byte offset and no
    evidence behind it. Resuming from that offset would parse the tail with empty workspace
    evidence and an empty `pending` — wrong, and silent, because the offset itself is fine."""
    lines = _stable_lines()
    with tempfile.TemporaryDirectory() as tmp:
        st = open_store(os.path.join(tmp, "s.db"))
        p = _laid_out(tmp, PROJ, FNAME, lines[:3])
        ingest_file(st, p)
        st._conn().execute("DELETE FROM parse_state")       # what a v1 store looks like
        p = _laid_out(tmp, PROJ, FNAME, lines)
        r = ingest_file(st, p)
        assert r.reparsed is True, r
        ref = open_store(os.path.join(tmp, "ref.db"))
        rp = _laid_out(os.path.join(tmp, "r"), PROJ, FNAME, lines)
        ingest_file(ref, rp)
        assert _dump(st, p) == _dump(ref, rp)
        st.close(); ref.close()


def test_a_missing_transcript_raises_rather_than_reporting_nothing_new():
    with tempfile.TemporaryDirectory() as tmp:
        st = open_store(os.path.join(tmp, "s.db"))
        try:
            ingest_file(st, os.path.join(tmp, "nope.jsonl"))
        except FileNotFoundError:
            st.close()
            return
        raise AssertionError("expected FileNotFoundError")


def test_ingest_state_survives_reopening_the_store():
    """Cross-batch state must live in the database, not in one process's memory: the sidecar is
    restarted (and its inference worker recycled) as a matter of routine."""
    objs = _late_declaration_lines()
    lines = _lines_of(objs)
    with tempfile.TemporaryDirectory() as tmp:
        db = os.path.join(tmp, "s.db")
        for k in range(1, len(lines) + 1):
            st = open_store(db)                       # a fresh Store object every chunk
            p = _laid_out(tmp, "-workspace-kexample-demo", "0badc0de-0000.jsonl", lines[:k])
            ingest_file(st, p)
            st.close()
        st = open_store(db)
        ref = open_store(os.path.join(tmp, "ref.db"))
        p2 = _laid_out(os.path.join(tmp, "r"), "-workspace-kexample-demo",
                       "0badc0de-0000.jsonl", lines)
        ingest_file(ref, p2)
        assert _dump(st, p) == _dump(ref, p2)
        st.close(); ref.close()


# ---------------------------------------------------------------- the `repo` fingerprint
#
# `repo` is the SECOND level that is never re-derived, and unlike `term` it does not come out of
# the transcript at all: it arrives with the request, because only the daemon may read a
# checkout's .git/config. So a tail parsed while the daemon sent nothing stores turns that no
# later recomputation can supply a `repo` row for -- the exact trap `terms_mode` exists for -- and
# `repo_mode` joins the parse-state fingerprint to force a reparse instead.

RESOLVED = {"repo": "github.com/ncx-ai/keld-atlas", "git_branch": "main", "project": "keld"}
OTHER = {"repo": "github.com/ncx-ai/keld-signal", "git_branch": "main", "project": "keld"}


def _repo_values(store, path):
    """The distinct `repo` refs the store holds, from BOTH the events and the precomputed bins.

    Both, deliberately: `repo` is an ALLOCATION level so it is in `PRECOMPUTED_LEVELS`, and a
    reparse that cleared the events while leaving a stale bin behind would answer the interior of
    every historical window with the old identity while the edges answered with the new one --
    visible in a rollup and invisible in an events-only check."""
    ev, bn, _mg, _bo = _dump(store, path)
    return {r[3] for r in ev if r[2] == "repo"} | {r[3] for r in bn if r[2] == "repo"}


def test_a_store_ingested_without_facts_reparses_once_when_they_arrive():
    """The whole reason the fingerprint exists. Without it the transcript's HISTORY would report
    the repository as unattributed forever, because the ingest that would have written those rows
    already happened -- and the byte offset would look perfectly valid while it did."""
    projdir, fname, lines = PROJ, FNAME, _stable_lines()
    with tempfile.TemporaryDirectory() as tmp:
        st = open_store(os.path.join(tmp, "s.db"))
        p = _laid_out(tmp, projdir, fname, lines)
        ingest_file(st, p)
        assert not _repo_values(st, p), "premise: no facts, no rows"
        r = ingest_file(st, p, None, RESOLVED)
        assert r.reparsed is True, r
        assert r.new_lines == len(lines), r
        assert _repo_values(st, p) == {RESOLVED["repo"]}, \
            "the reparse must install the level over the whole history"
        st.close()


def test_a_tail_ingest_with_the_same_facts_does_not_reparse():
    """The fingerprint must cost one reparse, not one per signal. Steady state is the common
    case: every /ingest and every /analyze refresh for a transcript carries the same identity."""
    projdir, fname, lines = PROJ, FNAME, _stable_lines()
    with tempfile.TemporaryDirectory() as tmp:
        st = open_store(os.path.join(tmp, "s.db"))
        p = _laid_out(tmp, projdir, fname, lines[:4])
        ingest_file(st, p, None, RESOLVED)
        p = _laid_out(tmp, projdir, fname, lines)
        r = ingest_file(st, p, None, RESOLVED)
        assert r.reparsed is False, r
        assert r.new_lines == len(lines) - 4, r
        st.close()


def test_an_empty_identity_never_displaces_a_stored_one():
    """⚠️ THE ASYMMETRY, and it is load-bearing rather than tidy. TWO writers reach ingest -- the
    watcher's /ingest and /analyze's own on-demand refresh -- and they resolve the facts from
    different starting points, so one legitimately has an identity where the other has none (a
    transcript whose directory has since been removed, a job with no cwd). Invalidating
    symmetrically would make those two alternate WHOLE-FILE reparses of the same file forever,
    each installing its own fingerprint. Asymmetric, the store adopts an identity the first time
    any caller supplies one and is never reset by a caller that has none."""
    projdir, fname, lines = PROJ, FNAME, _stable_lines()
    with tempfile.TemporaryDirectory() as tmp:
        st = open_store(os.path.join(tmp, "s.db"))
        p = _laid_out(tmp, projdir, fname, lines)
        ingest_file(st, p, None, RESOLVED)
        r = ingest_file(st, p, None, None)             # the other writer, with nothing resolved
        assert r.reparsed is False, r
        assert r.new_lines == 0, r
        assert _repo_values(st, p) == {RESOLVED["repo"]}, \
            "a caller with no facts must not erase the rows a caller with facts wrote"
        st.close()


def test_a_genuinely_different_identity_does_reparse():
    """The other direction: a repository that was renamed, or a directory that now resolves to a
    different checkout, is a real change of answer and the stored rows are wrong. One reparse,
    and the old value must be REPLACED rather than accumulated beside the new one."""
    projdir, fname, lines = PROJ, FNAME, _stable_lines()
    with tempfile.TemporaryDirectory() as tmp:
        st = open_store(os.path.join(tmp, "s.db"))
        p = _laid_out(tmp, projdir, fname, lines)
        ingest_file(st, p, None, RESOLVED)
        r = ingest_file(st, p, None, OTHER)
        assert r.reparsed is True, r
        assert _repo_values(st, p) == {OTHER["repo"]}, \
            "a reparse must replace the old identity, not merge two of them"
        st.close()


def test_chunked_ingest_with_facts_still_equals_one_pass():
    """The file's own central contract, extended to the new argument: a tail parse must equal a
    full parse, `repo` rows included. They are written per turn from a constant, so this is the
    cheap half of the equivalence -- which is exactly why it should be pinned rather than
    assumed."""
    projdir, fname, lines = PROJ, FNAME, _stable_lines()
    with tempfile.TemporaryDirectory() as tmp:
        db = os.path.join(tmp, "s.db")
        for k in range(1, len(lines) + 1):
            st = open_store(db)
            p = _laid_out(tmp, projdir, fname, lines[:k])
            ingest_file(st, p, None, RESOLVED)
            st.close()
        st = open_store(db)
        ref = open_store(os.path.join(tmp, "ref.db"))
        p2 = _laid_out(os.path.join(tmp, "r"), projdir, fname, lines)
        ingest_file(ref, p2, None, RESOLVED)
        assert _dump(st, p) == _dump(ref, p2)
        st.close(); ref.close()


def test_capture_mode_is_part_of_the_parse_state():
    """Flipping KELD_CAPTURE changes what the store permanently holds, so a state written under
    one setting must not be resumed under the other -- the same trap `terms_mode` exists for.

    ⚠️ And a state that PREDATES the key must read as capture-off, not as a mismatch. Treating
    the absent key as unusable would force a whole-file reparse of every transcript on every
    machine at upgrade, including machines that never turn capture on.
    """
    from app.analysis.ingest import _dump_state, _state_is_usable, capture_mode
    prev = os.environ.get("KELD_CAPTURE")
    try:
        os.environ["KELD_CAPTURE"] = "0"
        assert capture_mode() == "0"
        off = _dump_state(new_evidence(), [], [], 0, None)
        assert off["capture"] == "0"
        assert _state_is_usable(off, None) is True

        os.environ["KELD_CAPTURE"] = "1"
        assert capture_mode() == "1"
        assert _state_is_usable(off, None) is False, "capture-on must not resume a capture-off state"
        on = _dump_state(new_evidence(), [], [], 0, None)
        assert _state_is_usable(on, None) is True

        os.environ["KELD_CAPTURE"] = "0"
        assert _state_is_usable(on, None) is False, "capture-off must not resume a capture-on state"

        legacy = dict(off)
        legacy.pop("capture")
        assert _state_is_usable(legacy, None) is True, \
            "a state predating the key must read as capture-off, not force a reparse"
    finally:
        if prev is None:
            os.environ.pop("KELD_CAPTURE", None)
        else:
            os.environ["KELD_CAPTURE"] = prev


def test_read_complete_lines_offsets_are_byte_exact_on_invalid_utf8():
    """⚠️ Offsets MUST come from the raw bytes, never from re-encoding the decoded strings.

    `_read_complete_lines` decodes with `errors="replace"`, which turns one invalid byte into
    U+FFFD -- three bytes when re-encoded. So a cumulative sum over `len(line.encode())` drifts
    by two bytes per invalid byte, silently, and every offset after the first bad line points
    into the middle of a record. This fixture has one invalid byte precisely to catch that.
    """
    import tempfile
    from app.analysis.ingest import _read_complete_lines
    with tempfile.TemporaryDirectory() as tmp:
        p = os.path.join(tmp, "t.jsonl")
        raw = [b'{"a":1}\n', b'{"b":"\xff"}\n', b'{"c":3}\n']
        with open(p, "wb") as fh:
            fh.write(b"".join(raw))
        lines, offsets, end = _read_complete_lines(p, 0, os.path.getsize(p))
        assert len(lines) == 3 and len(offsets) == 3
        want, at = [], 0
        for b in raw:
            want.append(at); at += len(b)
        assert offsets == want, f"{offsets} != {want}"
        assert end == at
        naive = []
        at = 0
        for L in lines:
            naive.append(at); at += len(L.encode("utf-8"))
        assert naive != want, "fixture must actually exercise the drift, or it proves nothing"


def test_ingest_writes_bin_offsets_and_tool_outcomes_only_under_capture():
    """The two capture signals that come from raw lines rather than from `events_for_turns`."""
    import tempfile
    from app.analysis import magnitude
    from app.analysis.ingest import ingest_file, session_of
    from app.analysis.store import open_store
    prev = os.environ.get("KELD_CAPTURE")
    try:
        with tempfile.TemporaryDirectory() as tmp:
            p = os.path.join(tmp, "t.jsonl")
            with open(p, "w") as fh:
                fh.write('{"type":"user","timestamp":"2026-08-26T10:00:00.000Z","cwd":"/w",'
                         '"message":{"role":"user","content":"hello"}}\n')
                fh.write('{"type":"user","timestamp":"2026-08-26T10:01:00.000Z","cwd":"/w",'
                         '"message":{"role":"user","content":[{"type":"tool_result",'
                         '"is_error":true,"content":"boom"}]}}\n')
            sess = session_of(p)

            os.environ["KELD_CAPTURE"] = "0"
            st = open_store(os.path.join(tmp, "off.db"))
            ingest_file(st, p)
            assert st.turn_magnitudes(sess, 0, 4e9, kind=magnitude.TOOL_ERRORS) == [], \
                "capture off must write no outcomes"
            assert st.all_bin_offsets(sess) == {}, "capture off must write no offsets"
            st.close()

            os.environ["KELD_CAPTURE"] = "1"
            st = open_store(os.path.join(tmp, "on.db"))
            ingest_file(st, p)
            errs = st.turn_magnitudes(sess, 0, 4e9, kind=magnitude.TOOL_ERRORS)
            assert [v for _ts, v in errs] == [1.0], errs
            chars = st.turn_magnitudes(sess, 0, 4e9, kind=magnitude.TOOL_RESULT_CHARS)
            assert chars and chars[0][1] > 0
            rows = st.all_bin_offsets(sess)
            assert rows, "capture on must record at least one bin offset"
            assert min(rows.values()) == 0, f"the file's first bin starts at byte 0: {rows}"
            assert all(v % 1 == 0 and v >= 0 for v in rows.values()), rows
            st.close()
    finally:
        if prev is None:
            os.environ.pop("KELD_CAPTURE", None)
        else:
            os.environ["KELD_CAPTURE"] = prev


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn(); print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
