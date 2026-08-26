"""`S(t)` — the structured feature vector. What its INDICES mean, and that they cannot move.

  cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_features.py

⚠️ **This file lives at `app/test_features.py`, NOT under `app/analysis/`.** AGENTS.md's test loop
globs `app/test_*.py`; a file one directory down would silently never run, which is the one test
failure nothing reports.

THE PROPERTIES THIS FILE EXISTS FOR, in order of how expensive getting them wrong is:

 1. **A CLOSED VOCABULARY IS FROZEN FROM `vocab.py`, NEVER FROM A STORE.** A machine that never
    ran `pandoc` must still emit a zero in the `document` toolchain slot, or index `i` means two
    different things on two machines and the corpus is silently incoherent. Asserted by building
    the manifest against the tables directly, and by computing a row on a fixture whose levels
    hold almost none of those values and checking the width is unchanged.
 2. **NO IDENTITY, ANYWHERE.** The open levels (`term` 1,334 distinct on a live store, `file`
    687) contribute five shape statistics and nothing else. Asserted structurally — no manifest
    slot may name an open level's VALUE — and by a whole-row scan for the fixture's own strings.
 3. **THE SHELLS ARE DISJOINT.** Nested windows would count the same minutes four times. Asserted
    on the bounds arithmetic, and by the property that summing three shells reproduces the hour.
 4. **`workstreams.payload` IS NOT THE INPUT.** A distribution cannot be recovered from a
    dominant value, so this is a mistake that cannot be repaired after a corpus is collected.
    Asserted against the module's own AST rather than its text — this module's DOCSTRINGS name
    the things it must not use, at length and on purpose, so a substring scan would fail on the
    very prose that records the decision (`_code_names`).
 5. **ABSENT IS NOT ZERO.** A store ingested without `KELD_CAPTURE=1` reports
    `capture_recorded: False` and a captured one reports True, on the SAME transcript, so the
    flag is measured rather than asserted about a constant.
 6. **THE SPEC VERSION AND THE MANIFEST DIGEST RIDE EVERY ROW**, and the digest moves when a slot
    moves.

Everything else is composition and is treated as such: this file does not re-derive numbers that
`test_analysis_window.py`, `test_analysis_prior.py`, `test_analysis_dynamics.py`,
`test_magnitude.py` and `test_latency.py` already own.

The fixture is a real transcript through a real `ingest_file` into a real SQLite store, not a
stub — `active_bins` reads the `bin` table, the capture flag comes from `parse_state`, and the
shell bounds are evaluated against the store's own 0.1 s quantization. All three are things a
fake would have to fake correctly.
"""
import ast
import atexit
import inspect
import itertools
import json
import math
import os
import shutil
import sys
import tempfile

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from app.analysis import features as F
from app.analysis import vocab
from app.analysis.ingest import ingest_file, session_of
from app.analysis.levels import LEVELS
from app.analysis.store import open_store

_TMP = tempfile.mkdtemp(prefix="keld-features-test-")
atexit.register(lambda: shutil.rmtree(_TMP, ignore_errors=True))
_SEQ = itertools.count()

# The identity strings the fixture puts into OPEN levels. Nothing derived from them may appear in
# a vector or in a manifest slot -- that is property 2, and these are what it is checked against.
BRANCH = "feature-branch-xyzzy"
WORKSPACE = "widget-app"
SECRET_FILE = "vault_of_secrets.go"


def _user(ts, uuid, text):
    return {"type": "user", "timestamp": ts, "cwd": "/workspace/" + WORKSPACE,
            "gitBranch": BRANCH, "uuid": uuid,
            "message": {"content": [{"type": "text", "text": text}]}}


def _assistant(ts, uuid, req, tool, inp):
    return {"type": "assistant", "timestamp": ts, "cwd": "/workspace/" + WORKSPACE,
            "gitBranch": BRANCH, "uuid": uuid, "requestId": req,
            "message": {"role": "assistant", "model": "claude-opus-4-5-20251101",
                        "content": [{"type": "thinking", "thinking": "", "signature": "sig"},
                                    {"type": "tool_use", "id": "toolu_" + uuid, "name": tool,
                                     "input": inp}],
                        "usage": {"input_tokens": 100, "output_tokens": 20,
                                  "cache_creation_input_tokens": 40,
                                  "cache_read_input_tokens": 900}}}


def _transcript():
    """~40 minutes of work across three 5-minute bins, so the inner shells are populated and the
    outer ones are legitimately empty. Real tools, real file extensions, a real model id."""
    lines = []
    n = itertools.count()

    def ts(minute, second=0):
        return "2026-08-20T09:%02d:%02dZ" % (minute, second)

    for i, minute in enumerate((0, 2, 4)):
        lines.append(_user(ts(minute), "u%d" % i, "please look at the %s" % SECRET_FILE))
        lines.append(_assistant(ts(minute, 10), "a%d" % next(n), "req%d" % i,
                                "Read", {"file_path": "/workspace/%s/%s" % (WORKSPACE,
                                                                            SECRET_FILE)}))
        lines.append(_assistant(ts(minute, 20), "a%d" % next(n), "req%d" % i,
                                "Edit", {"file_path": "/workspace/%s/main.py" % WORKSPACE,
                                         "old_string": "x" * 40, "new_string": "y" * 900}))
        lines.append(_assistant(ts(minute, 30), "a%d" % next(n), "req%d" % i,
                                "Bash", {"command": "go test ./..."}))
    for i, minute in enumerate((30, 32, 34)):
        lines.append(_user(ts(minute), "v%d" % i, "now run the build"))
        lines.append(_assistant(ts(minute, 10), "b%d" % next(n), "rq%d" % i,
                                "Bash", {"command": "pnpm build"}))
        lines.append(_assistant(ts(minute, 20), "b%d" % next(n), "rq%d" % i,
                                "Write", {"file_path": "/workspace/%s/app.tsx" % WORKSPACE,
                                          "content": "z" * 2000}))
    # COMPACT separators, and it is not cosmetic: `transcript.turns_in` gates on the raw
    # substring `"type":"user"` BEFORE any `json.loads`, so a pretty-printed fixture is skipped
    # unparsed and the store comes out empty with nothing reporting an error.
    return "\n".join(json.dumps(o, separators=(",", ":")) for o in lines) + "\n"


def _store_with(capture="1"):
    """A fresh store per call, holding the fixture ingested at the given capture setting.

    FRESH, not shared: the capture fingerprint is per store+transcript and a shared store would
    make the two capture tests order-dependent, which the runner's alphabetical order would then
    silently decide.
    """
    d = os.path.join(_TMP, "s%d" % next(_SEQ))
    os.makedirs(d)
    path = os.path.join(d, "fixture-feat-0000.jsonl")
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


def _anchor(st, path):
    """The instant to characterise: just after the fixture's last turn."""
    session = session_of(path)
    start = F.session_start(st, session)
    times = st.turn_times(session, start, start + 86400.0)
    return times[-1] + 1.0


# --- 1. the vocabulary manifest is frozen from vocab.py --------------------------------------

def test_every_histogram_slot_comes_from_a_vocab_table():
    """⚠️ THE INVARIANT THE WHOLE CORPUS RESTS ON. Derive a slot from what a store contains and
    two machines produce vectors of different meaning at the same index."""
    assert set(F.ACTION_SLOTS) == set(vocab.ACTIONS) | {F.OTHER}, F.ACTION_SLOTS
    assert set(F.LANG_SLOTS) == set(vocab.EXT_LANG.values()) | {F.OTHER}, F.LANG_SLOTS
    assert set(F.TOOLCHAIN_SLOTS) == set(vocab.TOOLCHAIN_EXE) | {F.OTHER}, F.TOOLCHAIN_SLOTS
    assert set(F.TOOL_SLOTS) == set(vocab.TOOL_ACTION) | {F.MCP, F.OTHER}, F.TOOL_SLOTS
    assert set(F.ARTIFACT_SLOTS) == (set(vocab.ARTIFACT_EXT) | {"code", F.OTHER}
                                     | set(vocab.ARTIFACT_SKILL.values())), F.ARTIFACT_SLOTS
    # The toolchain slot the docstring names: this machine has never run pandoc, and the slot
    # exists anyway.
    assert "s0.hist.toolchain.document" in F.MANIFEST


def test_a_store_that_never_saw_a_value_still_emits_its_slot():
    """The same invariant, MEASURED rather than asserted about a table. The fixture's `toolchain`
    level is empty and its `action` level holds four of twenty-two acts; the width is unchanged
    and every unseen slot is a real 0.0, not an absence."""
    st, path = _store_with()
    row = F.features_at(st, path, _anchor(st, path))
    assert row is not None
    assert len(row["values"]) == F.DIMS, len(row["values"])
    idx = {n: i for i, n in enumerate(F.MANIFEST)}
    for slot in ("document", "spreadsheet", "notebook", "infrastructure"):
        assert row["values"][idx["s0.hist.toolchain." + slot]] == 0.0, slot
    st.close()


def test_slot_order_is_sorted_and_other_is_last():
    """Sorted so the order is a function of the table rather than of dict insertion, and `other`
    last so appending a real value to a table shifts exactly one slot."""
    for slots in (F.ACTION_SLOTS, F.LANG_SLOTS, F.TOOLCHAIN_SLOTS, F.ARTIFACT_SLOTS):
        assert list(slots[:-1]) == sorted(slots[:-1]), slots
        assert slots[-1] == F.OTHER, slots


def test_a_histogram_is_a_normalised_share_and_an_unknown_value_lands_in_other():
    items = [("read", 6), ("edit", 3), ("a physical act nobody invented", 1)]
    h = F.histogram(items, "action", F.ACTION_SLOTS)
    assert abs(sum(h) - 1.0) < 1e-9, sum(h)
    assert abs(h[F.ACTION_SLOTS.index("read")] - 0.6) < 1e-9
    assert abs(h[-1] - 0.1) < 1e-9, h[-1]
    assert F.histogram([], "action", F.ACTION_SLOTS) == [0.0] * len(F.ACTION_SLOTS)


def test_the_model_histogram_is_a_family_not_an_id():
    """A model id carries a date suffix that rolls forward every few months; one-hotting it would
    give every slot a lifetime of one release."""
    assert F.model_family("claude-opus-4-5-20251101") == "opus"
    assert F.model_family("claude-sonnet-4-6") == "sonnet"
    assert F.model_family("<synthetic>") == "synthetic"
    assert F.model_family("acme-llm-7b-preview") == F.OTHER
    assert F.model_family(None) == F.OTHER


# --- 2. no identity, anywhere ----------------------------------------------------------------

OPEN_LEVELS = ("file", "dir", "component", "exe", "verb", "term", "skill", "branch",
               "workspace", "service")


def test_no_open_level_contributes_a_histogram():
    """⚠️ Five shape statistics, never a one-hot and NEVER A HASH: a hash of an identity is a
    fingerprint of that identity."""
    hist_levels = {lv for lv, _slots in F.HISTOGRAMS}
    for lv in OPEN_LEVELS:
        assert lv not in hist_levels, lv
        assert not any(n.startswith("%s.hist.%s." % (s, lv))
                       for s, _a, _b in F.SHELLS for n in F.MANIFEST), lv
    # ...and every level, open or closed, DOES contribute shape.
    for lv in LEVELS:
        assert "s0.shape.%s.top1_share" % lv in F.MANIFEST, lv


def test_no_hashing_anywhere_in_the_module():
    """The one permitted use of a digest is `SPEC_SHA` over the MANIFEST — the slot NAMES, which
    are a public contract. A digest over a rollup value would be an identity fingerprint."""
    names = _code_names(F)
    assert names & {"sha256", "md5", "blake2b", "hash"} == {"sha256"}, names & {"sha256", "md5"}
    assert "hashlib.sha256(\"\\n\".join(MANIFEST)" in inspect.getsource(F)


def test_a_row_carries_no_string_from_the_transcript():
    """The whole vector is floats, and the metadata beside it names only the session digest and
    two instants. The fixture's branch, workspace and filename appear nowhere."""
    st, path = _store_with()
    row = F.features_at(st, path, _anchor(st, path))
    assert all(isinstance(v, float) for v in row["values"])
    blob = json.dumps(row)
    for s in (BRANCH, WORKSPACE, SECRET_FILE, "main.py", "app.tsx", "pnpm"):
        assert s not in blob, s
    st.close()


def test_shape_statistics_cannot_hold_a_value():
    """`shape` never touches `ref` except to count how many there were — pinned on the signature
    of its output, which is five numbers whatever the values were."""
    a = F.shape([("some-very-identifying-branch-name", 9)])
    b = F.shape([("x", 9)])
    assert a == b, (a, b)
    assert len(a) == len(F.SHAPE_STATS) == 5


def test_norm_entropy_is_zero_for_one_value_and_one_for_a_flat_level():
    one = F.shape([("a", 7)])
    flat = F.shape([("a", 5), ("b", 5), ("c", 5), ("d", 5)])
    assert one[F.SHAPE_STATS.index("norm_entropy")] == 0.0
    assert abs(flat[F.SHAPE_STATS.index("norm_entropy")] - 1.0) < 1e-9
    # ...and it is SCALE-FREE in cardinality, which is the reason for the normalisation: a wide
    # level is not automatically a varied one.
    wide = F.shape([(str(i), 1) for i in range(400)])
    assert abs(wide[F.SHAPE_STATS.index("norm_entropy")] - 1.0) < 1e-9


def test_shape_of_an_empty_level_is_all_zeros():
    assert F.shape([]) == [0.0] * 5


def test_top3_mass_and_top1_share():
    s = F.shape([("a", 5), ("b", 3), ("c", 1), ("d", 1)])
    assert abs(s[F.SHAPE_STATS.index("top1_share")] - 0.5) < 1e-9
    assert abs(s[F.SHAPE_STATS.index("top3_mass")] - 0.9) < 1e-9
    assert abs(s[F.SHAPE_STATS.index("evidence")] - math.log1p(10)) < 1e-9
    assert abs(s[F.SHAPE_STATS.index("n_distinct")] - math.log1p(4)) < 1e-9


# --- 3. the shells are disjoint --------------------------------------------------------------

def test_the_shell_ladder_is_disjoint_and_abutting():
    """⚠️ Nested windows count the same first five minutes four times and come out
    near-collinear. Shells abut exactly: each one's `lo` is the next one's `hi`."""
    at, start = 100000.0, 0.0
    bounds = F.shell_bounds(at, start)
    assert [n for n, _l, _h, _c in bounds] == ["s0", "s1", "s2", "s3", "s4"]
    assert bounds[0][2] == at
    for (_n, lo, _hi, _c), (_n2, _lo2, hi2, _c2) in zip(bounds, bounds[1:]):
        assert lo == hi2, (lo, hi2)
    assert bounds[-1][1] == start


def test_summing_three_shells_reproduces_the_hour():
    """The recoverability claim, arithmetically: a consumer that wants the last hour adds three
    columns. The reverse — recovering a shell from nested windows — needs a subtraction the
    consumer cannot verify."""
    bounds = F.shell_bounds(100000.0, 0.0)
    assert bounds[2][1] == 100000.0 - 3600.0
    assert bounds[0][2] == 100000.0


def test_shells_clamp_to_the_session_start_and_report_coverage():
    """A young session's outer shells must not read as four hours of silence."""
    at, start = 1000.0, 400.0        # a 10-minute-old session
    bounds = dict((n, (lo, hi, c)) for n, lo, hi, c in F.shell_bounds(at, start))
    assert bounds["s0"] == (700.0, 1000.0, 1.0)
    lo, hi, cov = bounds["s1"]
    assert (lo, hi) == (400.0, 700.0)          # truncated by the session start
    assert abs(cov - 300.0 / 900.0) < 1e-9     # 5 of the 15 nominal minutes existed
    for name in ("s2", "s3", "s4"):
        lo, hi, cov = bounds[name]
        assert lo == hi == start and cov == 0.0, (name, lo, hi, cov)


# --- 4. workstreams.payload is not the input --------------------------------------------------

def _code_names(mod):
    """Every identifier the module's CODE actually references — names, attributes and imports.

    AST rather than a substring scan, and that is not fastidiousness: this module's docstrings
    NAME the things it must not use, at length and on purpose (`workstreams.payload` is the wrong
    input; `MIN_EVIDENCE` is a label and not a filter). A `"workstreams" not in src` test would
    therefore fail on the very prose that records the decision, and the obvious repair — deleting
    the prose — is the opposite of what should happen. Comments and strings are invisible here.
    """
    tree = ast.parse(inspect.getsource(mod))
    out = set()
    for node in ast.walk(tree):
        if isinstance(node, ast.Name):
            out.add(node.id)
        elif isinstance(node, ast.Attribute):
            out.add(node.attr)
        elif isinstance(node, ast.Import):
            out.update(a.name.split(".")[-1] for a in node.names)
        elif isinstance(node, ast.ImportFrom):
            out.add((node.module or "").split(".")[-1])
            out.update(a.name for a in node.names)
    return out


def test_the_published_payload_is_never_the_input():
    """⚠️ `workstreams.payload` emits only the DOMINANT value per dimension — a presentation
    decision. A distribution cannot be recovered from it, so this is a mistake that cannot be
    repaired after the corpus is collected. Pinned on the module's own AST."""
    names = _code_names(F)
    assert "workstreams" not in names, "features.py must not reach for the published payload"
    assert "payload" not in names
    assert "window_rows" in names or "rollup_window" in names


def test_min_evidence_is_not_applied_as_a_filter():
    """A sub-floor rollup is good input to a model even where it is not honest to publish as an
    attribution. Nothing in the module references the floor or calls `attribution`/`dominant`."""
    names = _code_names(F)
    for banned in ("MIN_EVIDENCE", "attribution", "dominant", "MIN_GAPS"):
        assert banned not in names, banned


def test_a_single_observation_still_produces_a_histogram():
    """The consequence, measured: one observation gives share 1.0 and it is emitted, where
    `workstreams` would label it `thin` and `dynamics` would refuse to compare it."""
    h = F.histogram([("read", 1)], "action", F.ACTION_SLOTS)
    assert h[F.ACTION_SLOTS.index("read")] == 1.0
    s = F.shape([("read", 1)])
    assert s[F.SHAPE_STATS.index("top1_share")] == 1.0


# --- 5. absent is not zero --------------------------------------------------------------------

def test_capture_recorded_reflects_the_store_not_the_environment():
    """⚠️ The two disagree routinely: a dormant transcript keeps its uncaptured rows while the
    environment says capture is on. Measured on the SAME fixture ingested both ways."""
    on, on_path = _store_with("1")
    off, off_path = _store_with("0")
    try:
        assert F.features_at(on, on_path, _anchor(on, on_path))["capture_recorded"] is True
        assert F.features_at(off, off_path, _anchor(off, off_path))["capture_recorded"] is False
    finally:
        on.close(); off.close()


def test_the_capture_slots_are_zero_and_the_width_is_unchanged_without_capture():
    """A missing capture kind means NOT RECORDED, and the flag is the only thing that says so —
    the width must not shrink, or a vector's shape would become a function of the machine."""
    off, path = _store_with("0")
    idx = {n: i for i, n in enumerate(F.MANIFEST)}
    row = F.features_at(off, path, _anchor(off, path))
    assert len(row["values"]) == F.DIMS
    assert row["values"][idx["row.meta.capture_recorded"]] == 0.0
    for shell in ("s0", "s1", "s2"):
        for slot in F.EFFORT_CAPTURE:
            assert row["values"][idx["%s.effort.%s" % (shell, slot)]] == 0.0, (shell, slot)
    off.close()


def test_capture_slots_are_populated_with_capture_on():
    """The other half: with capture on the same shells carry real token, character and
    thinking-incidence numbers, so the flag distinguishes two real states rather than one."""
    on, path = _store_with("1")
    idx = {n: i for i, n in enumerate(F.MANIFEST)}
    row = F.features_at(on, path, _anchor(on, path))
    vals = row["values"]
    got = {s: vals[idx["s4.effort." + s]] for s in F.EFFORT_CAPTURE}
    # s4 is the outer shell and holds the whole fixture's history on this anchor... unless the
    # session is under four hours old, in which case the inner shells do. Sum the ladder.
    total = {s: sum(vals[idx["%s.effort.%s" % (sh, s)]] for sh, _a, _b in F.SHELLS)
             for s in F.EFFORT_CAPTURE}
    assert total["tok_in_cached"] > 0.0, got
    assert total["tok_out"] > 0.0
    assert total["say_user_chars"] > 0.0
    assert total["say_asst_think_blocks"] > 0.0, "thinking-block INCIDENCE must be captured"
    on.close()


def test_no_feature_is_built_on_thinking_length():
    """⚠️ `magnitude.SAY_THINK` (`say_asst_think`) is emitted and NEVER STORED — every
    platform-written thinking block carries an empty string (measured 10,741 blocks, 0 of nonzero
    length) and `_aggregate_mag` drops zeros. A length feature would read all-zero on every
    machine forever and would look like a signal that had gone quiet."""
    names = _code_names(F)
    assert "SAY_THINK_BLOCKS" in names
    assert "SAY_THINK" not in names
    assert not any("say_asst_think" == n.rsplit(".", 1)[-1] for n in F.MANIFEST)
    assert any(n.endswith(".say_asst_think_blocks") for n in F.MANIFEST)


def test_cost_recorded_is_per_shell_and_capture_recorded_is_per_row():
    """A shell holding no assistant turn genuinely has no cost to record even on a captured
    transcript, so the cost gate is per shell; capture is a transcript property, so its flag is
    one dimension rather than five identical ones."""
    assert sum(1 for n in F.MANIFEST if n.endswith(".effort.cost_recorded")) == len(F.SHELLS)
    assert sum(1 for n in F.MANIFEST if n == "row.meta.capture_recorded") == 1


# --- 6. the spec version and the manifest digest ----------------------------------------------

def test_the_spec_version_and_digest_ride_every_payload():
    st, path = _store_with()
    out = F.features(st, path, [_anchor(st, path)])
    assert out["feature_spec"] == F.FEATURE_SPEC_VERSION
    assert out["spec_sha"] == F.SPEC_SHA
    assert out["dims"] == F.DIMS == len(F.MANIFEST)
    assert len(out["rows"]) == 1
    st.close()


def test_the_digest_moves_when_a_slot_moves():
    """A version constant alone cannot catch this: the person who edits `vocab.EXT_LANG` is not
    thinking about features.py."""
    import hashlib
    base = hashlib.sha256("\n".join(F.MANIFEST).encode()).hexdigest()[:16]
    moved = list(F.MANIFEST)
    moved[3], moved[4] = moved[4], moved[3]
    assert base == F.SPEC_SHA
    assert hashlib.sha256("\n".join(moved).encode()).hexdigest()[:16] != F.SPEC_SHA


def test_the_manifest_is_unique_and_aligned():
    assert len(set(F.MANIFEST)) == len(F.MANIFEST), "duplicate slot name"
    assert F.manifest() is F.MANIFEST


def test_the_row_group_widths_are_what_the_spec_says():
    """The published arithmetic, stated once so a change is a deliberate edit here. The spec's
    own totals (265/shell, 89/row, 1,414) were approximate — these are the real ones, and the
    module docstring records why each group is the size it is."""
    per_shell = (len(F.ACTION_SLOTS) + len(F.ARTIFACT_SLOTS) + len(F.LANG_SLOTS)
                 + len(F.TOOLCHAIN_SLOTS) + len(F.TOOL_SLOTS) + len(F.VCS_SLOTS)
                 + len(F.MODEL_SLOTS)
                 + len(LEVELS) * len(F.SHAPE_STATS)
                 + len(F.EFFORT_SLOTS))
    row = (len(F.DYNAMIC_DIMENSIONS) * (len(F.DYNAMIC_SCALARS) + len(F.STATUSES)
                                        + len(F.READINGS))
           + len(F.PRIOR_DIMENSIONS) * len(F.PRIOR_SCALARS)
           + len(F.POSITION_SLOTS) + 1)
    assert per_shell == 267, per_shell
    assert row == 93, row
    assert F.DIMS == len(F.SHELLS) * per_shell + row == 1428, F.DIMS


# --- composition: the row itself ---------------------------------------------------------------

def test_a_real_row_is_finite_and_mostly_nonzero_where_it_should_be():
    """A vector of NaNs or infinities poisons a training run silently; a vector that is entirely
    zero is a bug that looks like a quiet machine."""
    st, path = _store_with()
    row = F.features_at(st, path, _anchor(st, path))
    vals = row["values"]
    assert all(math.isfinite(v) for v in vals), "non-finite value in the vector"
    nz = sum(1 for v in vals if v != 0.0)
    assert nz > 60, nz
    st.close()


def test_changed_is_two_slots_because_it_is_three_state():
    """⚠️ Collapsing `None` onto `False` would publish 'we checked, nothing moved' for a
    comparison that could not be made — the single misreading the evidence-floor work exists to
    prevent."""
    assert "row.dyn.branch.changed_known" in F.MANIFEST
    assert "row.dyn.branch.changed_true" in F.MANIFEST
    st, path = _store_with()
    idx = {n: i for i, n in enumerate(F.MANIFEST)}
    vals = F.features_at(st, path, _anchor(st, path))["values"]
    for name, _l, _f in F.DYNAMIC_DIMENSIONS:
        known = vals[idx["row.dyn.%s.changed_known" % name]]
        true = vals[idx["row.dyn.%s.changed_true" % name]]
        assert known in (0.0, 1.0) and true in (0.0, 1.0)
        assert not (true == 1.0 and known == 0.0), name    # unknown-but-true is incoherent
    st.close()


def test_the_dynamics_and_prior_vocabularies_are_the_shipped_ones():
    """DERIVED from `dynamics`/`prior` rather than restated, so a dimension dropped there cannot
    leave a dead column here — and cannot silently widen the published vocabulary either."""
    from app.analysis.dynamics import DYNAMIC_DIMENSIONS, READINGS, STATUSES
    from app.analysis.prior import PRIOR_DIMENSIONS
    assert F.DYNAMIC_DIMENSIONS is DYNAMIC_DIMENSIONS
    assert F.PRIOR_DIMENSIONS is PRIOR_DIMENSIONS
    assert F.STATUSES is STATUSES and F.READINGS is READINGS
    # An INVENTORY level is structurally not addable to either — which is what keeps `term` out.
    assert all(n in ("branch", "output_type", "language", "skill")
               for n, _l, _f in F.PRIOR_DIMENSIONS)


def test_an_anchor_before_the_session_produces_no_row():
    """An honest 'there is nothing to characterise', never a row of zeros — which would enter a
    training set as a real observation of nothing happening."""
    st, path = _store_with()
    start = F.session_start(st, session_of(path))
    assert F.features_at(st, path, start - 10.0) is None
    assert F.features_at(st, path, start) is None
    st.close()


def test_a_batch_preserves_order_and_honours_the_bound():
    st, path = _store_with()
    at = _anchor(st, path)
    ats = [at - 600.0, at - 300.0, at]
    out = F.features(st, path, ats)
    assert [r["at"] for r in out["rows"]] == ats
    assert len(F.features(st, path, ats, max_rows=2)["rows"]) == 2
    st.close()


def test_the_retroactive_column_list_is_derived_and_nonempty():
    """The one declared leakage: `workspace.scan_workspace` is a whole-file pre-pass, so a
    `CLAUDE.md` read at 17:00 re-resolves the 09:00 turns. Measured bound 0.7% of appends
    (53/7,571). A training run drops these columns rather than rediscovering it."""
    assert F.RETROACTIVE_GROUPS
    assert all(g in F.MANIFEST for g in F.RETROACTIVE_GROUPS)
    assert "s0.shape.file.top1_share" in F.RETROACTIVE_GROUPS
    assert "s0.hist.vcs.git" in F.RETROACTIVE_GROUPS
    # `tool` is counted from tool-call metadata and is not revised by a later workspace read.
    assert "s0.hist.tool.Read" not in F.RETROACTIVE_GROUPS


def test_reconcile_is_rescoped_per_shell():
    """The STORED reconcile slot is whole-file: reading it directly would attribute a path using
    a declaration the shell never saw — a leak of the future into a causal feature, and a silent
    one. Pinned on the composition, since the arithmetic itself is `reconcile.py`'s."""
    src = inspect.getsource(F._rollup_at)
    assert "exclude_slots=(RECONCILE_SLOT,)" in src
    assert "pending_in(store, path, lo, hi)" in src


def test_features_never_opens_a_transcript():
    """A QUERY, never a parse. `/features` is driven by a sampling grid, so a whole-file parse
    inside one row would be paid dozens of times per call."""
    names = _code_names(F)
    for banned in ("open", "turns_in", "turns_between", "ingest_file", "analyze_window",
                   "iter_turns", "analyze"):
        assert banned not in names, banned


# --- POST /features ----------------------------------------------------------------------------
#
# Called DIRECTLY (async ones through asyncio.run) rather than through a fastapi TestClient, the
# convention `test_main.py` already follows for /analyze, /blocks and /tick: a series query is not
# an inference, and standing up the app's lifespan would spawn a worker to exercise a route that
# never touches one. `main._store` is monkeypatched to the fixture store for the same reason
# `KELD_HOME` is set in test_main's fixture — otherwise the test writes into the developer's real
# series.

def _call_features(**kw):
    import asyncio
    from app import main
    return asyncio.run(main.features(main.FeaturesIn(**kw)))


def test_the_endpoint_is_confined_to_the_analyze_roots():
    """⚠️ The sidecar has NO auth. /features reads a transcript's series as the daemon's user and
    answers with numbers derived from it, so it takes the SAME allowlist /analyze, /ingest, /tick
    and /blocks take. 403, not 404: a rejected path and an unresolvable one are different facts."""
    from fastapi import HTTPException
    st, path = _store_with()
    was = os.environ.get("KELD_ANALYZE_ROOTS")
    os.environ["KELD_ANALYZE_ROOTS"] = os.path.join(os.path.dirname(path), "elsewhere")
    try:
        _call_features(path=path, ats=[_anchor(st, path)])
        assert False, "expected 403"
    except HTTPException as e:
        assert e.status_code == 403, e.status_code
    finally:
        if was is None:
            os.environ.pop("KELD_ANALYZE_ROOTS", None)
        else:
            os.environ["KELD_ANALYZE_ROOTS"] = was
        st.close()


def test_the_endpoint_returns_rows_and_the_manifest_only_on_request():
    """The manifest is `DIMS` strings — an order of magnitude more bytes than the vectors it
    describes — and a caller needs it once per build, which `spec_sha` is what makes safe."""
    from app import main
    st, path = _store_with()
    was_roots, was_store = os.environ.get("KELD_ANALYZE_ROOTS"), main._store
    os.environ["KELD_ANALYZE_ROOTS"] = os.path.dirname(path)
    main._store = lambda: st
    try:
        out = _call_features(path=path, ats=[_anchor(st, path)])
        assert out["dims"] == F.DIMS and out["spec_sha"] == F.SPEC_SHA
        assert len(out["rows"]) == 1 and len(out["rows"][0]["values"]) == F.DIMS
        assert "manifest" not in out
        withm = _call_features(path=path, ats=[_anchor(st, path)], manifest=True)
        assert withm["manifest"] == list(F.MANIFEST)
    finally:
        main._store = was_store
        if was_roots is None:
            os.environ.pop("KELD_ANALYZE_ROOTS", None)
        else:
            os.environ["KELD_ANALYZE_ROOTS"] = was_roots
        st.close()


def test_the_request_model_cannot_assert_capture():
    """⚠️ Whether the capture rows exist is a property of the STORE, fingerprinted per transcript
    into `parse_state`. A caller-supplied flag would let a daemon assert capture over a transcript
    whose rows were written without it — the incoherent-corpus failure the fingerprint prevents."""
    from app import main
    assert "capture" not in main.FeaturesIn.model_fields
    assert set(main.FeaturesIn.model_fields) == {"path", "ats", "max_rows", "manifest"}


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
