#!/usr/bin/env python3
"""Sidecar-backed tests for blockgen.py: ingest generated transcripts through the REAL sidecar
analysis code (`app.analysis.ingest` + `app.analysis.blocks`, in-process — no HTTP server needed)
and assert the resulting block spans/gaps/repo attribution match what blockgen intended.

Standalone (no pytest); run with the SIDECAR VENV, from the repo root:

    KELD_HOME=$(mktemp -d) PYTHONPATH=sidecar \\
        ~/.keld/sidecar-venv/bin/python scripts/blockgen/test_blockgen_sidecar.py

`KELD_HOME` is set here too (belt and suspenders, see below) — this file must NEVER touch the
developer's real `~/.keld` (AGENTS.md's teleproxy `TestMain` note: a test that mutates the
machine it runs on is a worse defect than the one it checks for).
"""
import os
import shutil
import sys
import tempfile

# ---- isolate KELD_HOME BEFORE importing anything from the sidecar, and ALSO pass an explicit
# store path below rather than relying on the env var alone (belt and suspenders; see module
# docstring). `open_store()`/`Store`'s default path function reads KELD_HOME at CALL time, not
# import time, so either alone would suffice, but AGENTS.md's own history is that "alone" is
# exactly what got the developer's real ~/.keld overwritten once already.
_TMP_KELD_HOME = tempfile.mkdtemp(prefix="blockgen-keld-home-")
os.environ["KELD_HOME"] = _TMP_KELD_HOME

_HERE = os.path.dirname(os.path.abspath(__file__))
_REPO_ROOT = os.path.abspath(os.path.join(_HERE, "..", ".."))
_SIDECAR = os.path.join(_REPO_ROOT, "sidecar")
if _SIDECAR not in sys.path:
    sys.path.insert(0, _SIDECAR)
sys.path.insert(0, _HERE)

import blockgen  # noqa: E402

from app.analysis import blocks as blocks_mod           # noqa: E402
from app.analysis.analyze import _block_span             # noqa: E402
from app.analysis.ingest import ingest_file, session_of  # noqa: E402
from app.analysis.store import open_store                # noqa: E402

TOLERANCE_S = 300.0   # "within one 5-minute bin" per the acceptance criterion

# `--epoch` pins the fixed-base time mode: these tests check relative block spans/gaps, not
# calendar dates, so they don't strictly need it — but pinning it keeps every run reproducible
# rather than riding the real-time `--end="now"` default (blockgen.py's other mode) through a
# test suite for no benefit.
TEST_EPOCH = "2025-11-03T09:00:00Z"

# Every temp directory (and, for `_fresh_store`, standalone file) this run creates, cleaned up by
# the __main__ runner at the bottom — see its own comment for why a standalone script (no pytest
# fixtures) has to track this itself. Directories are `shutil.rmtree`'d; `_CLEANUP_FILES` are
# `os.remove`'d individually, because `tempfile.mkstemp` creates its file directly inside the
# SYSTEM temp directory rather than a fresh one of its own — recording its `dirname` here would
# queue the whole system temp root for deletion.
_CLEANUP_DIRS = [_TMP_KELD_HOME]
_CLEANUP_FILES = []

# REAL git checkouts for the default profile's five repositories, materialised ONCE for the whole
# run (idempotent, and every test here uses the default profile's repository set — see
# blockgen.materialize_workspaces). This is the fix for the coordinator's reported gap: a `cwd`
# that does not exist on disk makes even a real daemon's OWN `.git/config` read come back empty,
# so the sidecar correctly emits no `repo` event for a corpus that was never resolvable to begin
# with. Nothing here stands in for the sidecar's own logic — `vcs_of`/`resolve_workspace` still
# run unmodified against a REAL directory now, the same as they would against a developer's own
# checkout.
_TEST_WORKSPACES_DIR = tempfile.mkdtemp(prefix="blockgen-workspaces-")
_CLEANUP_DIRS.append(_TEST_WORKSPACES_DIR)
blockgen.materialize_workspaces(_TEST_WORKSPACES_DIR, blockgen.load_profile(None))


def _fresh_store():
    fd, path = tempfile.mkstemp(prefix="blockgen-refseries-", suffix=".db")
    os.close(fd)
    os.remove(path)   # Store creates it itself, 0600, on first use
    _CLEANUP_FILES.append(path)
    return open_store(path=path)


def _generate(seed, sessions=6, days=3, profile=None):
    prof = dict(profile or blockgen.load_profile(None))
    # Every test in this file uses the default profile's repository set, materialised once above
    # into `_TEST_WORKSPACES_DIR` — a test that ever passes a custom `profile` with a DIFFERENT
    # repository set would need to materialise its own workspaces first.
    prof["workspace_root"] = _TEST_WORKSPACES_DIR
    manifest, files = blockgen.generate(prof, sessions, days, seed, epoch=TEST_EPOCH)
    out_dir = tempfile.mkdtemp(prefix="blockgen-out-")
    _CLEANUP_DIRS.append(out_dir)
    blockgen.write_batch(out_dir, manifest, files)
    return manifest, out_dir


def _resolved_from_real_checkout(session_entry):
    """`resolved.repo` derived the same way a real daemon would derive it: read the checkout's
    OWN `.git/config` (never blockgen's manifest metadata directly) and normalise it. This is
    what makes ingestion here a genuine end-to-end check — hard-coding `resolved.repo` from the
    manifest's `repo_remote` field, which every version of this test did until now, could not
    have caught the coordinator's defect: a `cwd` pointing nowhere would still have produced a
    "correct-looking" `resolved.repo` in the test even though the real daemon could never have
    supplied one from that same, non-existent path."""
    origin_url = blockgen.read_origin_url(session_entry["cwd"])
    return {"repo": blockgen.normalise_remote(origin_url)}


def _ingest_session(store, out_dir, session_entry):
    path = os.path.join(out_dir, session_entry["rel_path"])
    resolved = _resolved_from_real_checkout(session_entry)
    ingest_file(store, path, nlp=None, resolved=resolved)
    return session_of(path)


def _blocks_for(store, session_key):
    span = _block_span(store, session_key)
    if span is None:
        return []
    return blocks_mod.cut(store, session_key, *span)


# ------------------------------------------------------------------------------ parsing / index

def test_every_generated_line_parses_through_the_real_readers():
    """`transcript.turns_in`/`tool_use_in` must accept every line blockgen writes without
    raising, and every user/assistant line must actually be picked up (not silently skipped by
    the substring gate)."""
    from app.analysis.transcript import tool_use_in, turns_in

    manifest, out_dir = _generate(seed=1, sessions=4)
    for session in manifest["session_list"]:
        path = os.path.join(out_dir, session["rel_path"])
        with open(path) as f:
            raw_lines = f.readlines()
        turns = list(turns_in(raw_lines))
        n_user_asst_lines = sum(1 for ln in raw_lines
                                if '"type":"user"' in ln or '"type":"assistant"' in ln)
        assert len(turns) == n_user_asst_lines, (
            f"{session['session_id']}: turns_in parsed {len(turns)} of {n_user_asst_lines} "
            "user/assistant lines")
        tool_lines = list(tool_use_in(raw_lines))
        assert len(tool_lines) > 0, f"{session['session_id']}: no tool_use lines recognised"


def test_every_human_prompt_indexes_under_prompt_id_not_uuid():
    store = _fresh_store()
    manifest, out_dir = _generate(seed=2, sessions=3)
    for session in manifest["session_list"]:
        skey = _ingest_session(store, out_dir, session)
        for pid in session["prompt_ids"]:
            t = store.prompt_time(skey, pid)
            assert t is not None, f"promptId {pid} did not resolve in the prompt index"
        # A uuid that never appears as a promptId anywhere must NOT resolve — otherwise this
        # test would pass even if ingest secretly indexed uuid instead of promptId (the exact
        # regression AGENTS.md documents: "8 of 8 prompts partial" when only uuid was indexed).
        bogus_uuid = "00000000-0000-4000-8000-000000000000"
        assert bogus_uuid not in session["prompt_ids"]
        assert store.prompt_time(skey, bogus_uuid) is None


# ------------------------------------------------------------------------------------ blocks

def test_block_spans_match_intended_active_runs_within_one_bin():
    manifest, out_dir = _generate(seed=3, sessions=5)
    store = _fresh_store()
    checked_runs = 0
    for session in manifest["session_list"]:
        skey = _ingest_session(store, out_dir, session)
        blocks = _blocks_for(store, skey)
        assert blocks, f"{session['session_id']}: sidecar produced no blocks at all"

        # Group the sidecar's own blocks into contiguous runs the same way blockgen intended:
        # consecutive blocks that abut (gap == 0) belong to one run; a gap means a break.
        sidecar_runs = []
        cur = [blocks[0]]
        for b in blocks[1:]:
            if b.start == cur[-1].end:
                cur.append(b)
            else:
                sidecar_runs.append(cur)
                cur = [b]
        sidecar_runs.append(cur)

        assert len(sidecar_runs) == len(session["runs"]), (
            f"{session['session_id']}: intended {len(session['runs'])} active runs, "
            f"sidecar produced {len(sidecar_runs)}")

        for intended, got in zip(session["runs"], sidecar_runs):
            got_start, got_end = got[0].start, got[-1].end
            assert abs(got_start - intended["start_ts"]) <= TOLERANCE_S, (
                f"{session['session_id']}: run start off by "
                f"{abs(got_start - intended['start_ts'])}s")
            assert abs(got_end - intended["end_ts"]) <= TOLERANCE_S, (
                f"{session['session_id']}: run end off by "
                f"{abs(got_end - intended['end_ts'])}s")
            expected_blocks = intended["n_blocks"]
            assert len(got) == expected_blocks, (
                f"{session['session_id']}: intended {expected_blocks} 20-min blocks in this "
                f"run, sidecar cut {len(got)}")
            for blk in got[:-1]:
                dur = blk.end - blk.start
                assert abs(dur - 20 * 60) <= 1e-6, f"non-terminal block not 20 minutes: {dur}s"
            checked_runs += 1
    assert checked_runs > 0


def test_every_intended_break_appears_as_a_gap_between_blocks():
    manifest, out_dir = _generate(seed=4, sessions=5)
    store = _fresh_store()
    checked_breaks = 0
    for session in manifest["session_list"]:
        skey = _ingest_session(store, out_dir, session)
        blocks = _blocks_for(store, skey)
        gaps = [(blocks[i].end, blocks[i + 1].start) for i in range(len(blocks) - 1)
               if blocks[i + 1].start > blocks[i].end]
        assert len(gaps) == len(session["breaks"]), (
            f"{session['session_id']}: intended {len(session['breaks'])} breaks, "
            f"sidecar shows {len(gaps)} gaps between blocks")
        for intended, (gap_start, gap_end) in zip(session["breaks"], gaps):
            assert gap_end - gap_start >= 15 * 60 - 1e-6, (
                f"{session['session_id']}: gap {gap_end - gap_start}s is under the 15-minute "
                "idle threshold")
            assert abs(gap_start - intended["start_ts"]) <= TOLERANCE_S
            assert abs(gap_end - intended["end_ts"]) <= TOLERANCE_S
            checked_breaks += 1
        # The reason at each seam must read `idle`, never `budget` or `session_end` (blocks.py's
        # own REASONS vocabulary) — a break is a claim there was no work, not an arithmetic cut.
        for i in range(len(blocks) - 1):
            if blocks[i + 1].start > blocks[i].end:
                assert blocks[i].end_reason == "idle"
                assert blocks[i + 1].start_reason == "idle"
    assert checked_breaks > 0


def test_blocks_never_span_more_than_max_block_minutes():
    manifest, out_dir = _generate(seed=5, sessions=4)
    store = _fresh_store()
    for session in manifest["session_list"]:
        skey = _ingest_session(store, out_dir, session)
        for b in _blocks_for(store, skey):
            assert (b.end - b.start) <= blocks_mod.MAX_BLOCK_MINUTES * 60 + 1e-6


def test_default_now_anchored_corpus_cuts_real_blocks_near_today():
    """End-to-end version of the coordinator's D1 blocker: a corpus generated with blockgen's
    DEFAULT time base (no --epoch, no --end — i.e. anchored to real 'now') must ingest and cut
    into real blocks whose latest span lands within minutes of today, not eight months in the
    past. Uses `blockgen.generate` directly rather than `_generate`'s epoch-pinned helper, since
    the whole point here is to exercise the un-pinned default."""
    import time as _time

    profile = dict(blockgen.load_profile(None))
    profile["workspace_root"] = _TEST_WORKSPACES_DIR
    manifest, files = blockgen.generate(profile, sessions=3, days=2, seed=77)
    out_dir = tempfile.mkdtemp(prefix="blockgen-out-now-")
    _CLEANUP_DIRS.append(out_dir)
    blockgen.write_batch(out_dir, manifest, files)

    store = _fresh_store()
    latest_block_end = None
    for session in manifest["session_list"]:
        skey = _ingest_session(store, out_dir, session)
        for b in _blocks_for(store, skey):
            if latest_block_end is None or b.end > latest_block_end:
                latest_block_end = b.end
    assert latest_block_end is not None, "no blocks were cut at all"
    assert _time.time() - latest_block_end < 3600, (
        f"the latest real block ends {(_time.time() - latest_block_end) / 3600:.1f}h in the "
        "past — the default anchor is not landing near today")


# --------------------------------------------------------------------------------- repo level

def test_intended_repository_resolves_as_the_full_remote():
    manifest, out_dir = _generate(seed=6, sessions=5)
    store = _fresh_store()
    checked = 0
    for session in manifest["session_list"]:
        skey = _ingest_session(store, out_dir, session)
        rows = store._conn().execute(
            "SELECT DISTINCT ref FROM event WHERE session=? AND level='repo'", (skey,)
        ).fetchall()
        refs = {r[0] for r in rows}
        assert refs == {session["repo_remote"]}, (
            f"{session['session_id']}: repo level resolved to {refs}, "
            f"wanted {{{session['repo_remote']!r}}}")
        checked += 1
    assert checked == len(manifest["session_list"])


def test_repo_and_vcs_resolve_from_a_real_git_checkout():
    """The coordinator's exact reported gap, end to end. A synthetic `cwd` that does not exist on
    disk (this generator's behaviour before real workspace materialisation) makes even a REAL
    daemon's own `.git/config` read come back empty — the sidecar was never wrong to emit no
    `repo` event for a corpus that was never resolvable to begin with. Confirms three things
    together, none of which the other repo-level tests in this file exercised on their own before
    `_ingest_session` started reading a real checkout instead of trusting the manifest:

    1. the materialised checkout's OWN `.git/config` (`git config --get remote.origin.url`, not
       blockgen's manifest metadata) normalises to exactly the intended remote;
    2. the resulting `repo` event in the store matches that same remote;
    3. `vcs` reads plain `git` — never `"git (reported, unverifiable)"`, the fallback
       `workspace.vcs_of` takes specifically because it refuses to trust `gitBranch` alone for a
       `cwd` it cannot stat as a real directory.
    """
    manifest, out_dir = _generate(seed=42, sessions=5)
    store = _fresh_store()
    checked = 0
    for session in manifest["session_list"]:
        origin_url = blockgen.read_origin_url(session["cwd"])
        assert origin_url, f"{session['cwd']} is not a real git checkout at all"
        normalised = blockgen.normalise_remote(origin_url)
        assert normalised == session["repo_remote"], (
            f"materialised checkout's own remote ({normalised!r}) does not match the intended "
            f"one ({session['repo_remote']!r})")

        skey = _ingest_session(store, out_dir, session)

        repo_rows = store._conn().execute(
            "SELECT DISTINCT ref FROM event WHERE session=? AND level='repo'", (skey,)
        ).fetchall()
        assert {r[0] for r in repo_rows} == {session["repo_remote"]}, (
            f"{session['session_id']}: repo level did not resolve from the real checkout")

        vcs_rows = store._conn().execute(
            "SELECT DISTINCT ref FROM event WHERE session=? AND level='vcs'", (skey,)
        ).fetchall()
        vcs_values = {r[0] for r in vcs_rows}
        assert vcs_values == {"git"}, (
            f"{session['session_id']}: vcs read {vcs_values}, wanted plain 'git' — cwd is not "
            "resolving as a real, on-disk checkout")
        checked += 1
    assert checked == len(manifest["session_list"])


def test_workspace_level_resolves_to_the_repo_basename():
    """A second, independent check that workspace.py actually resolved the checkout from the
    planted evidence (marker file + launch directory), not merely that `resolved.repo` rode
    along unchecked — the `repo` ALLOCATION row is only emitted `if repo` (the resolved
    WORKSPACE name), see levels.events_for_turns."""
    manifest, out_dir = _generate(seed=7, sessions=4)
    store = _fresh_store()
    for session in manifest["session_list"]:
        skey = _ingest_session(store, out_dir, session)
        rows = store._conn().execute(
            "SELECT DISTINCT ref FROM event WHERE session=? AND level='workspace'", (skey,)
        ).fetchall()
        refs = {r[0] for r in rows}
        assert refs == {session["workspace"]}, (
            f"{session['session_id']}: workspace resolved to {refs}, "
            f"wanted {{{session['workspace']!r}}}")


def test_different_repositories_never_cross_contaminate_a_session():
    """Every session is bound to exactly one repository end to end: the store's own `repo` rows
    for one session must never include another profile repo's remote."""
    manifest, out_dir = _generate(seed=8, sessions=6)
    store = _fresh_store()
    all_remotes = {r["remote"] for r in blockgen.DEFAULT_PROFILE["repositories"]}
    for session in manifest["session_list"]:
        skey = _ingest_session(store, out_dir, session)
        rows = store._conn().execute(
            "SELECT DISTINCT ref FROM event WHERE session=? AND level='repo'", (skey,)
        ).fetchall()
        refs = {r[0] for r in rows}
        assert refs <= all_remotes
        assert refs == {session["repo_remote"]}


# -------------------------------------------------------------------------------- __main__

def _cleanup():
    """Remove every temp directory/file this run created — a standalone script has no pytest
    fixture teardown, so this is the only thing that will ever do it. Best-effort: a cleanup
    failure must never turn a passing test run into a failing one."""
    for path in _CLEANUP_FILES:
        for candidate in (path, path + "-wal", path + "-shm"):   # SQLite WAL/SHM siblings
            try:
                os.remove(candidate)
            except OSError:
                pass
    for d in _CLEANUP_DIRS:
        shutil.rmtree(d, ignore_errors=True)


if __name__ == "__main__":
    tests = [(name, fn) for name, fn in sorted(globals().items())
            if name.startswith("test_") and callable(fn)]
    failed = 0
    try:
        for name, fn in tests:
            try:
                fn()
            except Exception as exc:  # noqa: BLE001 - report, never re-raise silently
                failed += 1
                import traceback
                print(f"FAIL {name}: {type(exc).__name__}: {exc}")
                traceback.print_exc()
            else:
                print(f"ok   {name}")
    finally:
        _cleanup()
    print(f"\n{len(tests) - failed}/{len(tests)} passed")
    print(f"(KELD_HOME, workspaces and every generated corpus were isolated under temp "
         f"directories and have been removed)")
    raise SystemExit(1 if failed else 0)
