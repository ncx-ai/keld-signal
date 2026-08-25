"""/analyze served from the reference-series store instead of a transcript parse.

  cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_analyze_store.py

The contract this file exists for is ONE property: `analyze_window` must return the SAME answer
whether it is computed by parsing the transcript or served from the store. So every assertion
compares against `analyze_window_by_parse` — the parse implementation, kept as the oracle — and
not against hand-written expectations, which would test this file's arithmetic instead of the
equality that actually has to hold.

The comparison is made at TWO depths on purpose:

  * the ROLLUP (all 19 levels `events_for_turns` can emit), because the published payload exposes
    only 12 of them and the levels `reconcile` owns -- `file`, `dir`, `component`, `ext` -- are
    invisible in it. A store path that got reconcile's SCOPE wrong would pass a payload-only
    comparison on most transcripts;
  * the PAYLOAD, field for field, because that is what crosses the wire.

The fixture is built to be discriminating rather than merely non-empty: the target window's two
edges both fall strictly inside a 5-minute bin (asserted, so the fixture cannot silently drift
into alignment), turns exist on both sides of both edges, and one prose path is mentioned inside
the window whose only declaration is OUTSIDE it -- the case where whole-file reconcile and
window-scoped reconcile give different answers.
"""
import json
import os
import sys
import tempfile
from datetime import datetime, timedelta, timezone

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from app.analysis import latency, magnitude, window, workstreams
from app.analysis.analyze import (PromptNotFound, StoreBehind, WindowExpired, _bounds,
                                  _effort_from_rows, _effort_from_store,
                                  analyze_window, analyze_window_by_parse, _rollup_by_parse,
                                  _rollup_from_store)
from app.analysis.ingest import RECONCILE_SLOT, ingest_file, is_current, session_of, terms_mode
from app.analysis.store import BIN_SECONDS, open_store

# Off-grid on purpose: 13:07:41.317 is not a 5-minute boundary, and the offsets below are
# irregular and sub-second, so no window edge derived from them lands on one either.
BASE_ISO = "2026-08-12T13:07:41.317Z"
BASE = datetime.fromisoformat(BASE_ISO.replace("Z", "+00:00"))
# A FILENAME prefix, not a store key. `ingest.session_of` keys the store on a digest of the
# transcript's absolute path (see its docstring on the collision that forced that), so a
# hardcoded id here would resolve nothing -- these tests derive the key from the path.
FILE_PREFIX = "b7e41c90"
FILENAME = FILE_PREFIX + "-3d5a-4f11-9c02-6ab1e7c40000.jsonl"
PROJDIR = "-workspace-fixture-analyze-aurora-ledger"
CWD = "/workspace/fixture-analyze/aurora-ledger"
BRANCH = "feat/settlement-retries"
MODEL = "acme-llm-7b-preview"

# The prose path mentioned INSIDE the target window whose only declaration is OUTSIDE it. With
# reconcile scoped to the window (what the parse path does) it stays as written; with the stored
# whole-file reconcile it is reattributed to the declared path. That difference is the whole
# reason the rollup, not just the payload, is compared.
# A SUFFIX of the declared path, not the whole of it: reconcile only reattributes a prose path
# that was not itself declared, so an identical string would be a no-op in both scopes.
DECLARED_EARLY = CWD + "/services/api/handlers/retry.py"
DECLARED_REL = "services/api/handlers/retry.py"
PROSE_LATE = "handlers/retry.py"


def _ts(off):
    return (BASE + timedelta(seconds=off)).isoformat().replace("+00:00", "Z")


def _user(off, uuid, text):
    return {"type": "user", "uuid": uuid, "timestamp": _ts(off), "cwd": CWD,
            "gitBranch": BRANCH, "message": {"role": "user", "content": text}}


def _asst(off, uuid, text, blocks=()):
    content = [{"type": "text", "text": text}] + list(blocks)
    return {"type": "assistant", "uuid": uuid, "timestamp": _ts(off), "cwd": CWD,
            "gitBranch": BRANCH, "requestId": "req-" + uuid,
            "message": {"role": "assistant", "model": MODEL, "content": content,
                        "usage": {"input_tokens": 400, "output_tokens": 60,
                                  "cache_creation_input_tokens": 0,
                                  "cache_read_input_tokens": 0}}}


def _tool(name, inp, i=0):
    return {"type": "tool_use", "id": f"toolu_{name}_{i}", "name": name, "input": inp}


# Edit payloads for the diff-magnitude half of the `effort` block. Deliberately of three very
# different extents, because the study's finding is that windows indistinguishable by edit COUNT
# differ by two orders of magnitude in bytes: a 20-byte replacement, a 4000-byte deletion (whose
# new side is nearly empty -- the case `len(new_string)` alone would score as a no-op) and a
# 2500-byte authored file. Filler text with no path, host or capitalised word in it, so it adds
# `mag/edit_bytes` rows and touches no reference level (`magnitude.edit_bytes` is the only thing
# in levels.py permitted to see these keys).
_FIX_OLD, _FIX_NEW = "x" * 16, "y" * 20              # max = 20
_DEL_OLD, _DEL_NEW = "z" * 4000, "w" * 12            # max = 4000
_AUTHORED = "q" * 2500                               # max = 2500
_SECOND_OLD, _SECOND_NEW = "v" * 30, "u" * 90        # max = 90, on the SAME turn as the deletion
# The window's total and the number of TURNS it is spread over -- four edit events across three
# turns, which is the pair the store and the parse path have to agree on.
EDIT_BYTES_IN_WINDOW = 20 + 4000 + 90 + 2500
EDIT_TURNS_IN_WINDOW = 3


def _read(p, i=0):
    return _tool("Read", {"file_path": p}, i)


def _bash(cmd, i=0):
    return _tool("Bash", {"command": cmd}, i)


# Offsets are irregular and sub-second by design. The target prompt is the last entry at
# 4680.0s, so its 60-minute window is [1080.0, 4680.0) -- 1063.4 falls just outside it and
# 1121.8 just inside, which is what makes the start edge load-bearing rather than decorative.
TURNS = [
    # --- before the target window (excluded by its start edge) ------------------------------
    (0.0, "u", "p01", "Rivermark wants the settlement retry queue fixed before the review."),
    (12.4, "a", "a01", "Reading the retry handler first.", [_read(DECLARED_EARLY)]),
    (41.9, "a", "a02", "Running the suite.", [_bash("go test ./services/api/... -run Retry")]),
    (300.7, "u", "p02", "Halcyon is blocked on this too."),
    (613.2, "a", "a03", "Checking the ledger.",
     [_read(CWD + "/services/api/ledger.go"), _bash("rg -n 'settle' services/api")]),
    (902.6, "u", "p03", "Keep going."),
    (1063.4, "a", "a04", "Almost there.", [_bash("git status --short")]),
    # --- inside the target window -----------------------------------------------------------
    (1121.8, "u", "p04", "Perch reported the same drop after the Rivermark migration."),
    (1263.5, "a", "a05", "Editing the queue.",
     [_tool("Edit", {"file_path": CWD + "/services/api/queue.go",
                     "old_string": _FIX_OLD, "new_string": _FIX_NEW}),
      _bash("go build ./services/api/...")]),
    (1487.2, "a", "a06", "Fetching the runbook.",
     [_tool("mcp__notion__notion-fetch", {"url": "https://notion.example.internal/runbook"})]),
    (1690.9, "u", "p05", "What does Halcyon say about the retry budget?"),
    (1902.3, "a", "a07", "Reading the settings.",
     [_read(CWD + "/services/api/settings.go"), _bash("go vet ./services/api/...")]),
    (2114.7, "a", "a08", "Checking the deploy target.",
     [_bash("curl -s https://ledger.example.internal/health")]),
    (2333.1, "u", "p06", "Try the retry budget at ten."),
    (2560.4, "a", "a09", "Editing again.",
     [_tool("Edit", {"file_path": CWD + "/services/api/queue.go",
                     "old_string": _DEL_OLD, "new_string": _DEL_NEW}, 1),
      # A SECOND edit on the SAME turn. The store sums a turn's edit events into
      # one `turn_magnitude` row, so a parse path counting ROWS rather than
      # grouping by timestamp would report 4 authoring turns where the store
      # reports 3 -- agreeing on every byte and disagreeing on the count.
      _tool("Edit", {"file_path": CWD + "/services/api/settings.go",
                     "old_string": _SECOND_OLD, "new_string": _SECOND_NEW}, 2),
      _bash("go test ./services/api/... -run Budget")]),
    (2790.8, "a", "a10", "Using the review skill.",
     [_tool("Skill", {"skill": "superpowers:test-driven-development"})]),
    (3011.2, "u", "p07", "Rivermark asked for the migration notes too."),
    (3245.6, "a", "a11", "Writing the notes.",
     [_tool("Write", {"file_path": CWD + "/docs/settlement-retries.md",
                      "content": _AUTHORED}),
      _bash("git diff --stat")]),
    (3466.0, "a", "a12", "Re-reading the handler by its short name.",
     [_bash("wc -l " + PROSE_LATE)]),
    (3688.4, "u", "p08", "Good. Now the Perch case."),
    (3901.7, "a", "a13", "Dispatching a subagent.",
     [_tool("Agent", {"subagent_type": "Explore"}), _bash("go test ./services/api/...")]),
    (4122.3, "a", "a14", "Reading the tests.",
     [_read(CWD + "/services/api/queue_test.go"), _bash("python3 -m json.tool payload.json")]),
    (4344.9, "u", "p09", "Ship it."),
    (4560.1, "a", "a15", "Final check.", [_bash("git log --oneline -3")]),
    (4680.0, "u", "TARGET", "Summarise what we did for Rivermark."),
]


def _write(tmp, turns=TURNS):
    """The fixture transcript, at `<root>/<projdir>/<session>.jsonl` -- the shape
    `analyze_window` and `ingest_file` both derive `root`/`projdir` from."""
    d = os.path.join(tmp, "projects", PROJDIR)
    os.makedirs(d, exist_ok=True)
    path = os.path.join(d, FILENAME)
    with open(path, "w") as fh:
        for off, kind, uuid, text, *rest in turns:
            blocks = rest[0] if rest else ()
            o = _user(off, uuid, text) if kind == "u" else _asst(off, uuid, text, blocks)
            fh.write(json.dumps(o, separators=(",", ":")) + "\n")
    return path


def _store(tmp):
    return open_store(os.path.join(tmp, "state", "refseries.db"))


class _FakeSpan:
    def __init__(self, text, label, start, end):
        self.text, self.label_, self.start_char, self.end_char = text, label, start, end


class _FakeDoc:
    def __init__(self, ents):
        self.ents = ents


class _FakeNlp:
    """Minimal stand-in for a loaded spaCy pipeline, exactly as test_analysis_analyze.py's is:
    a bare capitalised word is the case terms.py's regex SHAPES cannot reach at all, so a real
    test of the `term` level needs some nlp, and a fake keeps the suite off a 600 MB
    spacy.load()."""
    NAMES = ("Rivermark", "Halcyon", "Perch")
    meta = {"lang": "xx", "name": "fake_sm", "version": "0.0.1"}

    def __call__(self, text):
        ents = []
        for n in self.NAMES:
            i = text.find(n)
            if i >= 0:
                ents.append(_FakeSpan(n, "ORG", i, i + len(n)))
        return _FakeDoc(ents)


def _prompt_ids(turns=TURNS):
    return [t[2] for t in turns if t[1] == "u"]


# --- the contract ----------------------------------------------------------------------------

def test_the_store_answers_the_target_window_exactly_as_a_parse_does():
    """The whole task. Rollup first (all 19 levels), then the payload field for field."""
    with tempfile.TemporaryDirectory() as tmp:
        path, st, nlp = _write(tmp), _store(tmp), _FakeNlp()
        ingest_file(st, path, nlp)

        want_rl, w_start, w_end, w_eff = _rollup_by_parse(path, "TARGET", 60, nlp)
        got_rl, g_start, g_end, g_eff = _rollup_from_store(st, path, "TARGET", 60, nlp)
        assert g_eff == w_eff, (w_eff, g_eff)
        assert (g_start, g_end) == (w_start, w_end), (g_start, g_end, w_start, w_end)
        assert got_rl == want_rl, _rl_diff(want_rl, got_rl)

        want = analyze_window_by_parse(path, "TARGET", 60, nlp)
        got = analyze_window(path, "TARGET", 60, nlp, store=st)
        assert got == want, _payload_diff(want, got)

        # Not vacuous: the window must actually hold a rich answer, or equality proves nothing.
        assert len(want_rl) >= 15, sorted(want_rl)
        assert want["evidence"] > 100, want["evidence"]
        attributed = [k for k, v in want["workstreams"].items() if v]
        assert len(attributed) >= 4, want["workstreams"]
        assert all(want["inventory"][k] for k in ("harness_tools", "programs", "named_terms"))
        st.close()


def test_the_effort_block_is_the_two_surviving_transcript_signals_and_nothing_else():
    """The published shape. Six discarded signals were measured; two passed, each since widened
    with a field it had computed all along and never published (`request_tokens`,
    `gap_p50_s`/`gap_p90_s` -- see `app/analysis/__init__.py`'s 13 -> 14 changelog entry). The
    four refuted ones -- raw token weight, tool-output volume, error thrashing, error rate -- must
    still not appear, and `magnitude.TOKENS` in particular is still COMPUTED and stored for the
    weighted rollup, so "not published" is the only thing keeping it out."""
    with tempfile.TemporaryDirectory() as tmp:
        path, st, nlp = _write(tmp), _store(tmp), _FakeNlp()
        ingest_file(st, path, nlp)
        eff = analyze_window(path, "TARGET", 60, nlp, store=st)["effort"]
        assert set(eff) == {"authored_bytes", "authoring_turns", "authored_status",
                            "fast_share", "gaps", "tempo", "tempo_status",
                            "request_tokens", "gap_p50_s", "gap_p90_s"}, sorted(eff)
        for refuted in ("tokens", "token_weight", "out_bytes", "error_rate",
                        "n_errors", "max_err_run", "n_thrash", "slow_share"):
            assert refuted not in eff, f"{refuted} was REFUTED and must not publish"
        st.close()


def test_the_effort_block_carries_the_measured_diff_magnitude_and_its_turn_count():
    """Both numbers, because neither is readable alone: the sum separates a typo fix from
    authoring, and the count is what stops one 4 KB deletion reading as sustained work."""
    with tempfile.TemporaryDirectory() as tmp:
        path, st, nlp = _write(tmp), _store(tmp), _FakeNlp()
        ingest_file(st, path, nlp)
        eff = analyze_window(path, "TARGET", 60, nlp, store=st)["effort"]
        assert eff["authored_bytes"] == EDIT_BYTES_IN_WINDOW, eff
        assert eff["authoring_turns"] == EDIT_TURNS_IN_WINDOW, eff
        assert eff["authored_status"] == "attributed"
        # Three edit turns is UNDER window.MIN_EVIDENCE, and the total is still reported: a sum
        # is a total, not an estimate from a sample. This is the assertion that would fail if
        # anyone gated a magnitude on a count floor.
        assert eff["authoring_turns"] < window.MIN_EVIDENCE
        st.close()


def test_the_effort_block_carries_the_tempo_and_the_share_it_was_computed_from():
    with tempfile.TemporaryDirectory() as tmp:
        path, st, nlp = _write(tmp), _store(tmp), _FakeNlp()
        ingest_file(st, path, nlp)
        eff = analyze_window(path, "TARGET", 60, nlp, store=st)["effort"]
        # The fixture's turns are minutes apart, so every gap is slow -- a real 0.0, earned over
        # enough gaps to state it.
        assert eff["gaps"] >= window.MIN_EVIDENCE, eff
        assert eff["fast_share"] == 0.0
        assert eff["tempo"] == "autonomous"
        assert eff["tempo_status"] == "attributed"
        st.close()


# The target window holds 11 assistant turns, each with a DISTINCT requestId and IDENTICAL usage
# (input_tokens=400, output_tokens=60, no cache activity) -- token_weight = 400 + 5*60 = 700.0 per
# request, and since no requestId repeats, `request_tokens` sums all eleven undeduped.
SPEND_IN_WINDOW = 11 * 700


def test_effort_carries_the_spend_and_the_gap_distribution():
    """The two withheld signals reach the payload: `request_tokens` is the price-weighted spend
    (`magnitude.REQUEST_TOKENS`, once per requestId -- NEVER `magnitude.TOKENS`, which repeats a
    request's cost on every line and would over-count this window's spend by up to 12x), and
    `gap_p50_s`/`gap_p90_s` are the inter-turn gap distribution `fast_share` collapses away."""
    with tempfile.TemporaryDirectory() as tmp:
        path, st, nlp = _write(tmp), _store(tmp), _FakeNlp()
        ingest_file(st, path, nlp)
        eff = analyze_window(path, "TARGET", 60, nlp, store=st)["effort"]
        for k in ("request_tokens", "gap_p50_s", "gap_p90_s"):
            assert k in eff, (k, sorted(eff))
        assert eff["request_tokens"] == SPEND_IN_WINDOW, eff
        assert eff["gap_p50_s"] is not None and eff["gap_p90_s"] is not None, eff
        assert 0 < eff["gap_p50_s"] <= eff["gap_p90_s"], eff
        st.close()


def test_effort_abstains_on_the_gap_percentiles_rather_than_reporting_zero():
    """A missing measurement is `None`, never 0 -- the rule `authored_bytes`/`fast_share` already
    follow. Below `latency.MIN_GAPS` the window has gaps, just too few to state a percentile from,
    so both fields must stay `None` together rather than publish a number computed from a sample
    too thin to mean anything."""
    turns = [(0.0, "u", "p1", "First."), (120.0, "u", "p2", "Second."),
             (240.0, "u", "LAST", "Third.")]
    with tempfile.TemporaryDirectory() as tmp:
        path, st, nlp = _write(tmp, turns), _store(tmp), _FakeNlp()
        ingest_file(st, path, nlp)
        eff = analyze_window(path, "LAST", 60, nlp, store=st)["effort"]
        # Same fixture as test_a_window_with_no_costed_turn_abstains_on_bytes_rather_than_claiming_
        # zero: one gap, well under MIN_GAPS -- thin, not absent, and thin still abstains.
        assert eff["gaps"] == 1, eff
        assert eff["gap_p50_s"] is None and eff["gap_p90_s"] is None, eff
        assert eff["request_tokens"] is None, eff
        assert analyze_window_by_parse(path, "LAST", 60, nlp)["effort"] == eff
        st.close()


def test_the_oracle_still_agrees_on_the_spend_and_gap_percentiles():
    """`analyze_window_by_parse` is the equivalence ORACLE and never a fallback -- adding a field
    to one gatherer and not the other is exactly the silent divergence it exists to catch. Checked
    across every prompt id in the fixture, not just the busiest window."""
    with tempfile.TemporaryDirectory() as tmp:
        path, st, nlp = _write(tmp), _store(tmp), _FakeNlp()
        ingest_file(st, path, nlp)
        for pid in _prompt_ids():
            served = analyze_window(path, pid, 60, nlp, store=st)["effort"]
            oracle = analyze_window_by_parse(path, pid, 60, nlp)["effort"]
            assert served == oracle, (pid, served, oracle)
        # Not vacuous: the busiest window must actually carry a real spend and a real percentile.
        eff = analyze_window(path, "TARGET", 60, nlp, store=st)["effort"]
        assert eff["request_tokens"] and eff["gap_p50_s"], eff
        st.close()


def test_the_effort_block_is_identical_from_the_store_and_from_a_parse():
    """Named separately from the field-for-field payload comparison so that a divergence in this
    block reports as this block rather than as a payload diff -- and so the two paths' DIFFERENT
    inputs (three indexed queries vs. its own rows) are pinned to one answer."""
    with tempfile.TemporaryDirectory() as tmp:
        path, st, nlp = _write(tmp), _store(tmp), _FakeNlp()
        ingest_file(st, path, nlp)
        for pid in _prompt_ids():
            want = analyze_window_by_parse(path, pid, 60, nlp)["effort"]
            got = analyze_window(path, pid, 60, nlp, store=st)["effort"]
            assert got == want, (pid, want, got)
        # Not vacuous: at least one window must carry real bytes AND a real tempo, or the
        # equality above is an agreement about nothing.
        eff = analyze_window(path, "TARGET", 60, nlp, store=st)["effort"]
        assert eff["authored_bytes"] > 1000 and eff["gaps"] > 5, eff
        st.close()


def test_the_oracles_tempo_clock_counts_REFERENCE_rows_only():
    """The two paths must define "a turn" identically or they will disagree on a window neither
    fixture happens to contain. The store's clock is the `event` table (`Store.turn_times`) and
    `turn_magnitude` is deliberately NOT joined into it, so the oracle must not count a `mag` row
    either: an assistant line carrying `usage` but emitting no reference would otherwise be a turn
    on one path and not on the other."""
    ref = lambda t: (t, "s", "repo", None, False, "ref", "tool", "Bash", 1.0)      # noqa: E731
    mag = lambda t, v: (t, "s", None, None, False, "mag", "edit_bytes", "", v)     # noqa: E731
    rows = [ref(0.0), ref(1.0), ref(2.0), ref(3.0), ref(4.0), ref(5.0), mag(9999.0, 113.0)]
    eff = _effort_from_rows(rows)
    assert eff["gaps"] == 5, eff          # five gaps between six turns -- NOT six
    assert eff["authored_bytes"] == 113 and eff["authoring_turns"] == 1, eff


def test_the_store_path_excludes_the_reconcile_slot_from_the_tempo_clock():
    """`/analyze` recomputes reconciliation at the window's own scope and excludes the stored
    slot from the rollup; the tempo clock is excluded with it, for the same reason and so the two
    paths cannot disagree.

    In practice a reconcile row adds no instant -- it COPIES its turn's timestamp, and a pending
    path is only ever produced inside `levels.py`'s tool_use loop, which emits a `ref/tool` row on
    that same turn. But "in practice" is not a mechanism: a stored reconcile row at an instant of
    its own would be a turn the parse path never saw, and this is the assertion that says so."""
    with tempfile.TemporaryDirectory() as tmp:
        st = open_store(os.path.join(tmp, "s.db"))
        t0 = 1755950400.0
        rows = [(t0 + i, "s", "repo", None, False, "ref", "tool", "Bash", 1.0)
                for i in range(6)]
        st.upsert_events("s", rows, source_line=1)
        st.upsert_events("s", [(t0 + 9999, "s", "repo", None, False, "ref", "file", "a.py", 1.0)],
                         source_line=RECONCILE_SLOT)
        eff = _effort_from_store(st, "s", t0 - 1, t0 + 20000)
        assert eff["gaps"] == 5, eff
        st.close()


def test_a_window_holding_one_turn_reports_no_fast_share_rather_than_zero():
    """THE defect, end to end. A one-turn window has no gap; `0.0` there is the same value a
    genuinely slow window publishes, and the study found it only by naming extremes."""
    turns = [(0.0, "u", "ONLY", "Just the one prompt.")]
    with tempfile.TemporaryDirectory() as tmp:
        path, st, nlp = _write(tmp, turns), _store(tmp), _FakeNlp()
        ingest_file(st, path, nlp)
        eff = analyze_window(path, "ONLY", 60, nlp, store=st)["effort"]
        assert eff["gaps"] == 0
        assert eff["fast_share"] is None, eff
        assert eff["tempo"] is None
        assert eff["tempo_status"] == "absent"
        assert analyze_window_by_parse(path, "ONLY", 60, nlp)["effort"] == eff
        st.close()


def test_a_window_with_no_costed_turn_abstains_on_bytes_rather_than_claiming_zero():
    """`authored_status` exists for this. A window of user turns only carries no magnitude of any
    kind -- which is also the state of a v5 store upgraded in place -- and `0 bytes authored`
    there is a claim made on the strength of never having looked."""
    turns = [(0.0, "u", "p1", "First."), (120.0, "u", "p2", "Second."),
             (240.0, "u", "LAST", "Third.")]
    with tempfile.TemporaryDirectory() as tmp:
        path, st, nlp = _write(tmp, turns), _store(tmp), _FakeNlp()
        ingest_file(st, path, nlp)
        eff = analyze_window(path, "LAST", 60, nlp, store=st)["effort"]
        assert eff["authored_bytes"] is None, eff
        assert eff["authoring_turns"] == 0
        assert eff["authored_status"] == "absent"
        # The tempo half is unaffected: one gap is a measurement, just a thin one. ONE, not
        # two: the window is half-open and ends AT the prompt, so `LAST` is not inside its own
        # window -- three turns, two of them in scope.
        assert eff["gaps"] == 1 and eff["fast_share"] == 0.0
        assert eff["tempo_status"] == "thin" and eff["tempo"] is None
        assert analyze_window_by_parse(path, "LAST", 60, nlp)["effort"] == eff
        st.close()


def test_a_costed_window_that_edited_nothing_reports_zero_bytes_not_absent():
    """The mirror of the test above, and the reason `authored_status` is not just `bytes is
    None`: turns whose cost WAS recorded and which edited nothing genuinely authored zero."""
    turns = [(0.0, "u", "p1", "Read the handler."),
             (30.0, "a", "a1", "Reading.", [_read(DECLARED_EARLY)]),
             (90.0, "a", "a2", "Read.", [_bash("git status --short")]),
             (150.0, "u", "LAST", "Thanks.")]
    with tempfile.TemporaryDirectory() as tmp:
        path, st, nlp = _write(tmp, turns), _store(tmp), _FakeNlp()
        ingest_file(st, path, nlp)
        eff = analyze_window(path, "LAST", 60, nlp, store=st)["effort"]
        assert eff["authored_bytes"] == 0, eff
        assert eff["authored_status"] == "attributed"
        assert analyze_window_by_parse(path, "LAST", 60, nlp)["effort"] == eff
        st.close()


def test_the_effort_payload_carries_no_transcript_string_at_all():
    """The privacy shape, at the payload boundary. Every value in the block is a number, a
    closed-vocabulary label from `latency`/`magnitude`, or None -- so there is no slot a byte of
    `old_string`/`new_string`/`content` could occupy even if something upstream tried."""
    with tempfile.TemporaryDirectory() as tmp:
        path, st, nlp = _write(tmp), _store(tmp), _FakeNlp()
        ingest_file(st, path, nlp)
        eff = analyze_window(path, "TARGET", 60, nlp, store=st)["effort"]
        allowed = set(latency.TEMPOS) | set(latency.STATUSES) | set(magnitude.AUTHORED_STATUSES)
        for k, v in eff.items():
            assert v is None or isinstance(v, (int, float)) or v in allowed, (k, v)
        blob = json.dumps(eff)
        for planted in (_FIX_OLD, _FIX_NEW, _DEL_OLD, _DEL_NEW, _AUTHORED,
                        _SECOND_OLD, _SECOND_NEW):
            assert planted not in blob, "an edit payload reached the effort block"
        # And the payloads really were in the input, or the assertion above proves nothing.
        src = open(path).read()
        assert _AUTHORED in src and _DEL_OLD in src
        st.close()


def test_every_prompt_in_the_transcript_agrees():
    """One window proves one window. Every user prompt in the fixture is a different pair of
    edges landing in a different place relative to the 5-minute grid and to the turns."""
    with tempfile.TemporaryDirectory() as tmp:
        path, st, nlp = _write(tmp), _store(tmp), _FakeNlp()
        ingest_file(st, path, nlp)
        for pid in _prompt_ids():
            want = analyze_window_by_parse(path, pid, 60, nlp)
            got = analyze_window(path, pid, 60, nlp, store=st)
            assert got == want, (pid, _payload_diff(want, got))
        st.close()


def test_both_edges_of_the_target_window_fall_inside_a_bin():
    """The case task 1 chose exact partial edges for. Asserted rather than assumed, so the
    fixture cannot drift into alignment and quietly stop exercising the edges."""
    with tempfile.TemporaryDirectory() as tmp:
        path = _write(tmp)
        _, start, end, _eff = _rollup_by_parse(path, "TARGET", 60, None)
        for name, t in (("start", start.timestamp()), ("end", end.timestamp())):
            assert t % BIN_SECONDS != 0, f"{name} landed on a bin boundary: {t}"
        # And turns must exist on BOTH sides of BOTH edges, or an edge is not being tested.
        offs = [t[0] for t in TURNS]
        lo, hi = 4680.0 - 3600.0, 4680.0
        assert any(o < lo for o in offs) and any(lo <= o < lo + BIN_SECONDS for o in offs)
        assert any(hi - BIN_SECONDS <= o < hi for o in offs) and any(o >= hi for o in offs)


def test_reconcile_is_rescoped_to_the_window_and_not_served_whole_file():
    """`reconcile` resolves a prose path against every path a tool DECLARED, so its answer
    depends on which declarations are in scope. The parse path reconciles the WINDOW's pending;
    the store holds the whole file's. Serving the stored rows would silently reattribute a path
    using a declaration the window never saw."""
    with tempfile.TemporaryDirectory() as tmp:
        path, st = _write(tmp), _store(tmp)
        ingest_file(st, path, None)
        got, start, end, _eff = _rollup_from_store(st, path, "TARGET", 60, None)
        # The stored, whole-file-scoped answer for the same window, i.e. what a plain
        # rollup_window would serve.
        whole = st.rollup_window(session_of(path), round(start.timestamp(), 1),
                                 round(end.timestamp(), 1))
        files_got = dict(got.get("file") or [])
        files_whole = dict(whole.get("file") or [])
        # The discriminating row: mentioned inside the window, declared only outside it. Scoped
        # to the window it stays as written; scoped to the file it is merged into the
        # declaration. Both halves are asserted, so the test cannot pass by the two scopes
        # happening to agree.
        assert PROSE_LATE in files_got, files_got
        assert PROSE_LATE not in files_whole, (
            "the fixture no longer discriminates: whole-file reconcile did not reattribute the "
            "prose path, so this test would pass even if the store served it")
        assert DECLARED_REL in files_whole, files_whole
        # And the window-scoped answer is the PARSE's answer, which is the actual contract.
        assert files_got == dict(_rollup_by_parse(path, "TARGET", 60, None)[0].get("file") or [])
        st.close()


# --- the watermark and staleness -------------------------------------------------------------

def test_a_window_ending_after_the_watermark_is_refused_rather_than_served_partial():
    """Serving a window missing its last minutes would publish a confidently wrong attribution.
    The checkpoint here is deliberately synthetic -- offset/size/mtime claim the whole file was
    read while `watermark_ts` says otherwise -- because that is precisely the state the guard
    exists for: a checkpoint that advanced past turns that were never stored."""
    with tempfile.TemporaryDirectory() as tmp:
        path, st, nlp = _write(tmp), _store(tmp), _FakeNlp()
        ingest_file(st, path, nlp)
        served = analyze_window(path, "TARGET", 60, nlp, store=st, refresh=False)
        state = st.ingest_state(path)
        st.record_ingest(path, state["offset"], state["size"], state["head_sha"],
                         state["mtime"], _ts(3000.0))     # behind the target prompt
        try:
            analyze_window(path, "TARGET", 60, nlp, store=st, refresh=False)
        except StoreBehind:
            assert served["evidence"] > 0, "the refused window was empty anyway"
            return
        raise AssertionError("a window past the watermark was served")


def test_a_stale_store_is_refused_and_not_served_from_what_it_happens_to_hold():
    """A transcript that grew since it was ingested. Refused, not answered from the prefix --
    `workspace.scan_workspace` is a whole-file pre-pass, so bytes AFTER the window still change
    what the turns inside it mean, and a prefix answer is a different answer."""
    with tempfile.TemporaryDirectory() as tmp:
        st, nlp = _store(tmp), _FakeNlp()
        prefix = _write(tmp, TURNS[:-6])
        ingest_file(st, prefix, nlp)
        path = _write(tmp)                     # same path, now the whole transcript
        assert not is_current(st, path, nlp)
        try:
            analyze_window(path, "TARGET", 60, nlp, store=st, refresh=False)
        except StoreBehind:
            return
        raise AssertionError("a stale store served a window")


def test_not_yet_ingested_and_prompt_not_found_stay_distinguishable():
    """One is transient and the caller must retry; the other is permanent. A single failure
    mode for both would either spin forever on a real 404 or drop a window that was one
    second from being ingestable."""
    with tempfile.TemporaryDirectory() as tmp:
        path, st, nlp = _write(tmp), _store(tmp), _FakeNlp()
        try:
            analyze_window(path, "TARGET", 60, nlp, store=st, refresh=False)
            raise AssertionError("an un-ingested transcript answered")
        except StoreBehind:
            pass
        ingest_file(st, path, nlp)
        try:
            analyze_window(path, "no-such-uuid", 60, nlp, store=st, refresh=False)
            raise AssertionError("an unknown prompt answered")
        except PromptNotFound:
            pass
        assert not issubclass(StoreBehind, PromptNotFound)
        assert not issubclass(PromptNotFound, StoreBehind)
        st.close()


def test_a_never_ingested_transcript_is_ingested_on_demand_not_parsed_and_discarded():
    """The fallback is INGEST, not a parse: paying the same cost once and leaving the store
    correct, rather than paying it on every prompt and leaving the store empty."""
    with tempfile.TemporaryDirectory() as tmp:
        path, st, nlp = _write(tmp), _store(tmp), _FakeNlp()
        assert st.ingest_state(path) is None
        got = analyze_window(path, "TARGET", 60, nlp, store=st)
        assert got == analyze_window_by_parse(path, "TARGET", 60, nlp)
        assert is_current(st, path, nlp), "the on-demand answer did not leave a checkpoint"
        st.close()


def test_an_appended_transcript_is_caught_up_on_demand():
    with tempfile.TemporaryDirectory() as tmp:
        st, nlp = _store(tmp), _FakeNlp()
        ingest_file(st, _write(tmp, TURNS[:-6]), nlp)
        path = _write(tmp)
        got = analyze_window(path, "TARGET", 60, nlp, store=st)
        assert got == analyze_window_by_parse(path, "TARGET", 60, nlp)
        st.close()


def test_a_rotated_transcript_does_not_answer_from_the_conversation_it_replaced():
    """A different file at the same path is a different conversation. The prompt index is keyed
    the same way the events are, so a reparse must clear it too — a turn id left behind would
    resolve a window the stored events no longer describe."""
    with tempfile.TemporaryDirectory() as tmp:
        st, nlp = _store(tmp), _FakeNlp()
        first = [(off, k, u, t, *r) for off, k, u, t, *r in TURNS]
        path = _write(tmp, first)
        ingest_file(st, path, nlp)
        before = analyze_window(path, "TARGET", 60, nlp, store=st)
        # Same path, same ids, an hour later and shorter: a rotation, not an append.
        rotated = [(off + 3600.0, k, u, t, *r) for off, k, u, t, *r in TURNS[10:]]
        _write(tmp, rotated)
        got = analyze_window(path, "TARGET", 60, nlp, store=st)
        assert got == analyze_window_by_parse(path, "TARGET", 60, nlp), _payload_diff(
            analyze_window_by_parse(path, "TARGET", 60, nlp), got)
        assert got["window_end"] != before["window_end"], (
            "the fixture no longer discriminates: the rotated file's prompt is at the same "
            "instant, so a stale index would resolve to the same window")
        st.close()


def test_the_query_path_never_opens_the_transcript():
    """The point of the store. A current store must answer from SQLite and a stat() -- not from
    a 4 KB head read, and certainly not from a parse."""
    import builtins
    with tempfile.TemporaryDirectory() as tmp:
        path, st, nlp = _write(tmp), _store(tmp), _FakeNlp()
        ingest_file(st, path, nlp)
        opened, real = [], builtins.open

        def watched(f, *a, **kw):
            if f == path:
                opened.append(f)
            return real(f, *a, **kw)

        builtins.open = watched
        try:
            analyze_window(path, "TARGET", 60, nlp, store=st)
        finally:
            builtins.open = real
        assert not opened, f"the transcript was opened {len(opened)}x by the query path"
        st.close()


# --- the terms pipeline is part of the checkpoint ---------------------------------------------

def test_changing_the_terms_pipeline_reparses_rather_than_serving_stale_terms():
    """`term` is the one level never re-derived: rows ingested without spaCy and rows ingested
    with it are not comparable. A store ingested with KELD_TERMS=0 would otherwise report no
    named_terms forever, silently, after terms were switched back on."""
    with tempfile.TemporaryDirectory() as tmp:
        path, st, nlp = _write(tmp), _store(tmp), _FakeNlp()
        ingest_file(st, path, None)               # ingested with no spaCy at all
        without = analyze_window(path, "TARGET", 60, None, store=st)
        assert not any(t["value"] in _FakeNlp.NAMES for t in without["inventory"]["named_terms"])
        got = analyze_window(path, "TARGET", 60, nlp, store=st)
        assert got == analyze_window_by_parse(path, "TARGET", 60, nlp), _payload_diff(
            analyze_window_by_parse(path, "TARGET", 60, nlp), got)
        assert {t["value"] for t in got["inventory"]["named_terms"]} & set(_FakeNlp.NAMES), (
            got["inventory"]["named_terms"])
        st.close()


def test_a_terms_mode_mismatch_is_refused_when_the_store_is_not_refreshed():
    with tempfile.TemporaryDirectory() as tmp:
        path, st = _write(tmp), _store(tmp)
        ingest_file(st, path, None)
        assert terms_mode(_FakeNlp()) != terms_mode(None)
        try:
            analyze_window(path, "TARGET", 60, _FakeNlp(), store=st, refresh=False)
        except StoreBehind:
            return
        raise AssertionError("a store ingested with a different terms pipeline was served")


# --- privacy ---------------------------------------------------------------------------------

def test_neither_the_response_nor_the_store_carries_message_text():
    with tempfile.TemporaryDirectory() as tmp:
        path, st, nlp = _write(tmp), _store(tmp), _FakeNlp()
        ingest_file(st, path, nlp)
        got = analyze_window(path, "TARGET", 60, nlp, store=st)
        dumped = json.dumps(got)
        for phrase in ("wants the settlement retry queue", "reported the same drop",
                       "Summarise what we did"):
            assert phrase not in dumped, phrase
        for k in ("text", "span", "offset"):
            assert f'"{k}":' not in dumped, k
        st.close()
        with open(os.path.join(tmp, "state", "refseries.db"), "rb") as fh:
            blob = fh.read()
        for phrase in (b"wants the settlement retry queue", b"reported the same drop"):
            assert phrase not in blob, phrase


# --- helpers ---------------------------------------------------------------------------------

def _rl_diff(want, got):
    out = []
    for lv in sorted(set(want) | set(got)):
        a, b = dict(want.get(lv) or []), dict(got.get(lv) or [])
        if a != b:
            out.append((lv, [(k, a.get(k), b.get(k))
                             for k in sorted(set(a) | set(b)) if a.get(k) != b.get(k)][:6]))
    return out


def _payload_diff(want, got):
    out = []
    for k in sorted(set(want) | set(got)):
        if want.get(k) != got.get(k):
            if isinstance(want.get(k), dict):
                out += [(f"{k}.{kk}", want[k].get(kk), got[k].get(kk))
                        for kk in sorted(set(want[k]) | set(got[k]))
                        if want[k].get(kk) != got[k].get(kk)]
            else:
                out.append((k, want.get(k), got.get(k)))
    return out



# --- retention: a window whose evidence was pruned ------------------------------------------

def test_a_window_below_the_serving_floor_is_refused_not_served_narrower():
    """THE RETENTION CONTRACT. Pruned events are not a degraded edge — `/analyze` serves every
    window with `exclude_slots=(RECONCILE_SLOT,)`, and `Store.window_rows` answers an
    excluded-slot query entirely from `event`, so a pruned window loses its evidence outright.
    Measured on this fixture: evidence 179 -> 36 with every bin intact, `project`/`branch`/
    `model` silently null, and `is_current` still True so nothing refuses. That is the plausible
    wrong number this project keeps paying for, so the floor makes it a refusal."""
    with tempfile.TemporaryDirectory() as tmp:
        path, st, nlp = _write(tmp), _store(tmp), _FakeNlp()
        ingest_file(st, path, nlp)
        served = analyze_window(path, "TARGET", 60, nlp, store=st, refresh=False)
        assert served["evidence"] > 0

        # A floor after the window's start: its evidence is, by declaration, gone.
        start, _end = _bounds(st.prompt_time(session_of(path), "TARGET"), 60)
        st.note_pruned("event", start.timestamp() + 1.0, 1)

        try:
            analyze_window(path, "TARGET", 60, nlp, store=st, refresh=False)
        except WindowExpired:
            return
        raise AssertionError("a window below the serving floor was served")


def test_a_window_above_the_serving_floor_still_serves_exactly():
    """The floor must not become a blanket refusal — every live digest is above it."""
    with tempfile.TemporaryDirectory() as tmp:
        path, st, nlp = _write(tmp), _store(tmp), _FakeNlp()
        ingest_file(st, path, nlp)
        want = analyze_window(path, "TARGET", 60, nlp, store=st, refresh=False)

        start, _end = _bounds(st.prompt_time(session_of(path), "TARGET"), 60)
        st.note_pruned("event", start.timestamp() - 60.0, 1)      # entirely below the window

        assert analyze_window(path, "TARGET", 60, nlp, store=st, refresh=False) == want


def test_expiry_is_permanent_and_distinct_from_being_behind():
    """`StoreBehind` means "ask again" and the Go client retries through it. A pruned window is
    never coming back, so it must NOT be the same exception: retrying it would spin forever on
    something permanent."""
    assert not issubclass(WindowExpired, StoreBehind)
    assert not issubclass(StoreBehind, WindowExpired)
    assert not issubclass(WindowExpired, PromptNotFound)


def test_expiry_wins_over_being_behind_when_both_are_true():
    """A stale store AND a pruned window. Reporting the transient one would make the caller
    retry a window that can never be answered."""
    with tempfile.TemporaryDirectory() as tmp:
        st, nlp = _store(tmp), _FakeNlp()
        path = _write(tmp)
        ingest_file(st, path, nlp)
        start, _end = _bounds(st.prompt_time(session_of(path), "TARGET"), 60)
        st.note_pruned("event", start.timestamp() + 1.0, 1)
        # Now make the store stale as well.
        state = st.ingest_state(path)
        st.record_ingest(path, state["offset"] - 1, state["size"], state["head_sha"],
                         state["mtime"], state["watermark_ts"])
        assert not is_current(st, path, nlp)
        try:
            analyze_window(path, "TARGET", 60, nlp, store=st, refresh=False)
        except WindowExpired:
            return
        except StoreBehind:
            raise AssertionError("reported the transient failure for a permanently pruned window")
        raise AssertionError("served a window that was both stale and expired")


def test_ingest_enforces_retention_so_nothing_else_has_to_schedule_it():
    """There is no background thread in this service and retention must not add one. Both
    writers — the watcher's /ingest and /analyze's on-demand refresh — go through `ingest_file`,
    so that is where it rides."""
    with tempfile.TemporaryDirectory() as tmp:
        path, st, nlp = _write(tmp), _store(tmp), _FakeNlp()
        calls = []
        real = st.enforce_retention

        def spy(**kw):
            calls.append(kw)
            return real(**kw)

        st.enforce_retention = spy
        ingest_file(st, path, nlp)
        assert calls, "ingest_file did not enforce retention"
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
