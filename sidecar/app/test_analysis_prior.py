"""The SESSION PRIOR: the session as it stood before this window, reported beside it.

  cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_analysis_prior.py

THE RULE THIS FILE EXISTS FOR, and every other assertion is subordinate to it:

    CONTRAST, NEVER FALLBACK. The prior is reported ALONGSIDE the window's own answer and never
    supplies one it lacked. AN UNATTRIBUTED WINDOW STAYS UNATTRIBUTED.

Inheriting a session value into a thin window would launder "we do not know" into something
confident -- the defect `window.MIN_EVIDENCE` exists to prevent, and one this project has paid
for twice (`activity_type`'s `transform`, predicted 36 times and right zero; `speech_act`'s
`statement`, 22 times and right zero).

Three further properties, each measured over 1,022 windows of the frozen corpus
(docs/superpowers/specs/2026-08-24-session-prior-results.md, commit b8a2ccf) and each with a
failure this file has to be able to catch:

  * CAUSAL, NOT RETROSPECTIVE. The prior is cut at the window's START. The design spec's literal
    reading put the window inside its own prior, under which `novel` is STRUCTURALLY unable to
    fire -- 0 of 1,022 on all seven dimensions, not a low yield but an impossible one.
  * RECOMPUTE, DO NOT ACCUMULATE. An incrementally-updated prior drifts from the stored events
    with no way to check it (reconcile's `pending` made the same call for the same reason: naive
    chunking differed by up to 4,179 rows on one file).
  * 45.1% OF WINDOWS ARE SESSION-FIRST and have no prior at all. That is arithmetic, not a
    defect: the prior reports `absent` and the block stays visible rather than being suppressed.
"""
import json
import os
import sys
import tempfile
from datetime import datetime, timedelta, timezone

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from app.analysis import workstreams
from app.analysis.analyze import analyze_window, analyze_window_by_parse
from app.analysis.ingest import RECONCILE_SLOT, ingest_file, session_of
from app.analysis.prior import ENABLED, PRIOR_DIMENSIONS, compare, prior_at
from app.analysis.store import DEFAULT_MAX_MB, RetentionPolicy, open_store
from app.analysis.window import MIN_EVIDENCE, REASONS, rollup


def _n(level, ref, n):
    """One rollup row in `events_for_turns` shape -- `window.rollup` reads indices 5-8 only."""
    return (0, "s", "r", "b", False, "ref", level, ref, float(n))


def _rl(level, **counts):
    return rollup([_n(level, ref, n) for ref, n in counts.items()])


# --- THE RULE --------------------------------------------------------------------------------

def test_the_prior_never_supplies_a_value_the_window_lacks():
    """THE RULE. The window is thin (3 observations, under MIN_EVIDENCE) so it has no value of
    its own; the session is emphatic. Every contrast measure is None and NOTHING in the block
    names the session's value as the window's.

    This is the assertion that would fail if anyone ever "improved coverage" by inheriting."""
    thin = _rl("lang", Python=2, Go=1)                       # n=3 < MIN_EVIDENCE
    rich = _rl("lang", TypeScript=400, Python=9)
    got = compare(thin, rich)["language"]
    assert got["agrees"] is None, got
    assert got["departure"] is None, got
    assert got["novel"] is None, got
    # The prior's own value is still REPORTED -- that is the contrast -- but the window keeps its
    # own (absent) answer, which lives in `workstreams` and is not touched here.
    assert got["value"] == "TypeScript", got
    assert workstreams.payload(thin)["workstreams"]["language"] is None, "the window was filled in"


def test_a_window_below_the_floor_is_not_rescued_by_an_agreeing_session():
    """The subtler shape of the same rule: the window HAS evidence and a top value, it is simply
    below the 0.50 share floor. An agreeing session is exactly the tempting case."""
    mixed = _rl("skill", brainstorming=10, executing=9, debugging=8)   # top share 0.37
    got = compare(mixed, _rl("skill", brainstorming=900))["skill"]
    assert got["agrees"] is None and got["departure"] is None and got["novel"] is None, got
    assert workstreams.payload(mixed)["workstreams"]["skill"] is None, mixed


# --- causal, not retrospective ---------------------------------------------------------------

def test_a_value_the_session_never_held_is_novel():
    """Bar 2's product, and the one a per-window view structurally cannot produce: 40 of 91
    windows on the corpus run a skill the session had never run before -- brainstorming ->
    writing-plans -> executing -> debugging, the phase transitions of the workflow."""
    got = compare(_rl("skill", executing_plans=252),
                  _rl("skill", brainstorming=38))["skill"]
    assert got["novel"] is True, got
    assert got["agrees"] is False, got
    assert got["departure"] == 1.0, got


def test_novel_is_not_asked_against_a_session_with_no_evidence_at_that_level():
    """45.1% of windows are session-first. Novelty against an empty prior is not yield, it is a
    session with no history -- so it is None, and the prior says `absent` out loud."""
    got = compare(_rl("skill", executing_plans=30), {})["skill"]
    assert got["status"] == "absent", got
    assert got["novel"] is None, got
    assert got["agrees"] is None, got
    assert got["departure"] is None, got
    assert got["value"] is None and got["evidence"] == 0, got


# --- departure is the measure that works -----------------------------------------------------

def test_departure_is_the_window_share_minus_the_SESSION_SHARE_OF_THE_WINDOW_VALUE():
    """NOT the difference of the two dominant shares, which compares two different values.

    Reproduced from the corpus' largest instance of the motivating case
    (`1d2b97b5#t0447-20260806T0204`): the window is Python 0.571 over 7 observations inside a
    session that is TypeScript 0.886 and gives Python 5.5%. Departure +0.516 states the
    excursion as a number; the difference of dominants (0.571 - 0.886 = -0.315) states nothing."""
    win = _rl("lang", Python=4, TypeScript=3)                               # 0.571
    prior = _rl("lang", TypeScript=240, Python=15, Go=9, Markdown=7)        # 0.886 / Python 0.055
    got = compare(win, prior)["language"]
    assert got["value"] == "TypeScript" and got["share"] == 0.886, got
    assert got["agrees"] is False, got
    assert got["departure"] == 0.516, got
    # and the narrow measure correctly declines: a TypeScript session that has touched Python is
    # not one where Python is absent. `novel` and `departure` catch DIFFERENT things.
    assert got["novel"] is False, got


def test_departure_is_zero_when_the_window_looks_exactly_like_its_session():
    """The twin. A measure that only ever fires is indistinguishable from one that always does."""
    got = compare(_rl("branch", main=50), _rl("branch", main=500))["branch"]
    assert got["departure"] == 0.0 and got["agrees"] is True and got["novel"] is False, got


# --- the prior's own status is one of window.attribution's four reasons ----------------------

def test_agrees_is_undefined_where_the_session_itself_has_no_dominant_value():
    """A `no_majority` prior has no value to agree with, and scoring that as DISAGREEMENT would
    count the session's own ambiguity as the window departing from it. Departure is still
    computed -- the session's share of the window's value is a fact either way."""
    got = compare(_rl("lang", Python=50), _rl("lang", Python=40, TypeScript=39, Go=21))["language"]
    assert got["status"] == "no_majority", got
    assert got["value"] is None, got
    assert got["agrees"] is None, got
    assert got["departure"] == 0.6, got            # 1.0 - 0.4
    assert got["novel"] is False, got


def test_the_prior_status_is_window_attributions_own_four_reasons():
    """`absent` / `thin` / `tie` / `no_majority` are FOUR DIFFERENT FACTS and the prior reports
    which, exactly as the window does. A prior that is itself `no_majority` is informative -- it
    says the window's ambiguity is the session's -- and must never collapse into "no prior"."""
    cases = {"absent": {}, "thin": _rl("branch", main=3),
             "tie": _rl("branch", main=9, dev=9), "no_majority": _rl("branch", a=4, b=3, c=3),
             "attributed": _rl("branch", main=40)}
    for want, rl in cases.items():
        got = compare(_rl("branch", main=10), rl)["branch"]
        assert got["status"] == want, (want, got)
        assert got["status"] in REASONS, got


def test_the_prior_carries_the_evidence_it_rests_on():
    """The design's own requirement: "record the evidence count in the payload so a reader can
    see how much session the prior rests on". A prior over 6 observations and one over 600 are
    not the same frame of reference, and the corpus' median prior evidence runs from 86
    (`branch`) to 0 (`skill`)."""
    got = compare(_rl("branch", main=10), _rl("branch", main=317, dev=11))["branch"]
    assert got["evidence"] == 328, got
    assert got["status"] == "attributed", got
    for dim in compare(_rl("branch", main=10), {}).values():
        assert dim["evidence"] == 0, dim


# --- which dimensions, and why ---------------------------------------------------------------

def test_the_enabled_set_is_a_list_and_holds_only_the_three_that_measured_a_contrast():
    """Measured over 1,022 windows: `skill` agreement 25.8% / novelty 44.0% is the signal;
    `language` 70.6% / 2.3% and `branch` 76.1% / 6.1% carry departures. `project` and `model`
    agree 100.0% with ZERO disagreements, zero novel windows, and a maximum departure of +0.000
    and -0.103 -- a contrast field there publishes a constant.

    A LIST, not hardcoded fields, so adding `output_type` (86.7%) or `tooling` (98.5%) -- both
    deliberately excluded for now, both live candidates, and on John's Cowork session carried by
    the prior in 6 of 7 and 4 of 7 windows where the window itself could not attribute -- is a
    one-line change."""
    assert sorted(ENABLED) == ["branch", "language", "skill"], ENABLED
    assert sorted(n for n, _lv, _f in PRIOR_DIMENSIONS) == sorted(ENABLED), PRIOR_DIMENSIONS
    for dead in ("project", "model"):
        assert dead not in ENABLED, dead
        assert dead not in compare(_rl("workspace", keld=9), _rl("workspace", keld=90)), dead
    # DERIVED from the published allocation set, so an INVENTORY level cannot enter it: the
    # `term` level is drawn from message text and has held real person names.
    by_name = {n: (lv, f) for n, lv, f in workstreams.ALLOCATION}
    for name, level, floor in PRIOR_DIMENSIONS:
        assert by_name[name] == (level, floor), (name, level, floor)
    assert "term" not in {lv for _n, lv, _f in PRIOR_DIMENSIONS}


def test_the_prior_uses_the_same_floor_and_evidence_bar_the_window_does():
    """Out of scope for this design: any change to MIN_EVIDENCE or the 0.50 share floor. The
    prior is the same question asked of a wider interval, not a laxer one."""
    just_under = {"main": MIN_EVIDENCE - 1}
    assert compare(_rl("branch", main=9), _rl("branch", **just_under))["branch"]["status"] == "thin"
    at = {"main": MIN_EVIDENCE}
    assert compare(_rl("branch", main=9), _rl("branch", **at))["branch"]["status"] == "attributed"
    # and the share floor is 0.50, not something the prior relaxes to buy coverage
    assert compare(_rl("branch", main=9),
                   _rl("branch", main=50, dev=50))["branch"]["status"] == "tie"
    assert compare(_rl("branch", main=9),
                   _rl("branch", main=49, dev=48, x=3))["branch"]["status"] == "no_majority"


def test_prior_at_is_the_same_attribution_the_window_gets():
    p = prior_at(_rl("lang", Python=8, Go=2), "lang", 0.5)
    assert p == {"value": "Python", "share": 0.8, "evidence": 10, "status": "attributed"}, p


# --- against a real store --------------------------------------------------------------------

BASE = datetime(2026, 8, 20, 9, 3, 17, 400000, tzinfo=timezone.utc)
FILENAME = "b41c9de2-7a10-4f22-9c03-51290abcd100.jsonl"
PROJDIR = "-workspace-fixture-prior-aurora-ledger"
CWD = "/workspace/fixture-prior/aurora-ledger"


def _ts(off):
    return (BASE + timedelta(seconds=off)).isoformat().replace("+00:00", "Z")


def _turn(off, uuid, kind="a", tools=(), branch="main", skill=None):
    if kind == "u":
        return {"type": "user", "uuid": uuid, "timestamp": _ts(off), "cwd": CWD,
                "gitBranch": branch, "message": {"role": "user", "content": "next step"}}
    content = [{"type": "text", "text": "working"}] + [
        {"type": "tool_use", "id": f"toolu_{uuid}_{i}", "name": "Read",
         "input": {"file_path": p}} for i, p in enumerate(tools)]
    if skill:
        content.append({"type": "tool_use", "id": f"toolu_{uuid}_s", "name": "Skill",
                        "input": {"skill": skill}})
    return {"type": "assistant", "uuid": uuid, "timestamp": _ts(off), "cwd": CWD,
            "gitBranch": branch, "requestId": "req-" + uuid,
            "message": {"role": "assistant", "model": "acme-llm-7b-preview",
                        "content": content,
                        "usage": {"input_tokens": 100, "output_tokens": 20,
                                  "cache_creation_input_tokens": 0,
                                  "cache_read_input_tokens": 0}}}


def _two_phase(first="main", second="feature/ledger-split", first_lang="ts", second_lang="py",
               first_skill="superpowers:brainstorming",
               second_skill="superpowers:executing-plans", minutes=120):
    """Two hours: the first on `first`, the second on `second`. The target prompt ends the
    second hour, so a 60-minute window sees ONLY the second phase and its prior sees only the
    first. Every contrast measure is therefore maximal and unambiguous."""
    turns, i = [], 0
    for minute in range(minutes):
        half = minute < minutes // 2
        br = first if half else second
        ext, sk = (first_lang, first_skill) if half else (second_lang, second_skill)
        for sub in (7.0, 23.0, 41.0):
            i += 1
            turns.append(_turn(minute * 60 + sub, f"t{i:04d}", branch=br,
                               tools=[f"{CWD}/services/api/queue{i}.{ext}"], skill=sk))
    turns.append(_turn(minutes * 60, "TARGET", kind="u", branch=second))
    return turns


def _write(tmp, turns, name=FILENAME):
    d = os.path.join(tmp, "projects", PROJDIR)
    os.makedirs(d, exist_ok=True)
    path = os.path.join(d, name)
    with open(path, "w") as fh:
        for o in turns:
            fh.write(json.dumps(o, separators=(",", ":")) + "\n")
    return path


def _served(tmp, turns, **kw):
    path = _write(tmp, turns)
    st = open_store(os.path.join(tmp, "state", "refseries.db"))
    ingest_file(st, path, None)
    out = analyze_window(path, "TARGET", 60, None, store=st, prior=True, **kw)
    return path, st, out


def test_the_prior_is_cut_at_the_window_START_not_at_its_end():
    """THE CORRECTION the measurement forced on the spec, end to end. Cut at "now" -- the daemon
    recomputing over everything ingested -- the window's own evidence sits inside its own prior,
    and `novel` becomes structurally unable to fire (0/1022 on all seven dimensions, and
    `skill` agreement moves 25.8% -> 83.8% purely from the window agreeing with itself).

    Here the second hour is a branch, a language and a skill the first hour never held. If the
    prior reached past the window's start, not one of the three could be novel."""
    with tempfile.TemporaryDirectory() as tmp:
        _path, st, out = _served(tmp, _two_phase())
        dims = out["prior"]["dimensions"]
        assert set(dims) == set(ENABLED), dims
        br = dims["branch"]
        assert br["evidence"] > MIN_EVIDENCE, br
        assert {k: v for k, v in br.items() if k != "evidence"} == {
            "value": "main", "share": 1.0, "status": "attributed",
            "agrees": False, "departure": 1.0, "novel": True}, br
        assert dims["language"]["value"] == "TypeScript", dims["language"]
        assert dims["language"]["novel"] is True, dims["language"]
        assert dims["skill"]["value"] == "superpowers:brainstorming", dims["skill"]
        assert dims["skill"]["novel"] is True, dims["skill"]
        # ... and the window itself still says what IT saw, unchanged by any of this.
        ws = out["workstreams"]
        assert ws["branch"]["value"] == "feature/ledger-split", ws["branch"]
        assert ws["language"]["value"] == "Python", ws["language"]
        st.close()


def test_a_sessions_first_window_reports_an_absent_prior_rather_than_no_block():
    """45.1% of windows on the corpus. Arithmetic, not a defect -- and visibly absent rather
    than suppressed, or a reader takes the blank for a bug and, worse, someone fills it."""
    with tempfile.TemporaryDirectory() as tmp:
        # 40 minutes of work, target at the end: the 60-minute window opens BEFORE the session
        # does, so there is nothing before it at all.
        _path, st, out = _served(tmp, _two_phase(
            minutes=40, second="main", second_lang="ts",
            second_skill="superpowers:brainstorming"))
        assert "prior" in out, out.keys()
        dims = out["prior"]["dimensions"]
        assert set(dims) == set(ENABLED), dims
        for name, d in dims.items():
            assert d["status"] == "absent", (name, d)
            assert d["value"] is None and d["evidence"] == 0, (name, d)
            assert d["agrees"] is None and d["departure"] is None and d["novel"] is None, (name, d)
        # the window is answered normally beside it
        assert out["workstreams"]["branch"]["value"] == "main", out["workstreams"]
        st.close()


def test_the_prior_is_recomputed_from_the_stored_events_never_accumulated():
    """RECOMPUTE, DO NOT ACCUMULATE -- the same reasoning that made reconcile's `pending`
    recomputed-whole-per-batch rather than merged (naive chunking differed by up to 4,179 rows
    on one file).

    Two assertions, and BOTH are needed. On its own, "the same window answers the same twice"
    is satisfied by any accumulator that has stopped moving — including one that answered from a
    per-session cache and never looked at the bounds again. So the first assertion is that two
    DIFFERENT windows of one session get DIFFERENT priors, which is what a session-keyed
    accumulator collapses; the second is that neither depends on the order they were asked in,
    nor on the store handle being warm, which is what a DRIFTING accumulator breaks."""
    with tempfile.TemporaryDirectory() as tmp:
        turns = _two_phase()
        path, st, late = _served(tmp, turns)
        early = analyze_window(path, "t0100", 60, None, store=st, prior=True)
        # The early window opens the session and the late one has an hour behind it. A prior
        # computed once per session and reused would make these identical.
        assert early["prior"] != late["prior"], early["prior"]
        assert early["prior"]["dimensions"]["branch"]["evidence"] == 0, early["prior"]
        assert late["prior"]["dimensions"]["branch"]["evidence"] > 0, late["prior"]
        # ... and neither depends on the order, nor on a warm handle.
        assert analyze_window(path, "TARGET", 60, None, store=st,
                              prior=True)["prior"] == late["prior"]
        st.close()
        cold = open_store(os.path.join(tmp, "state", "refseries.db"))
        assert analyze_window(path, "TARGET", 60, None, store=cold, prior=True,
                              refresh=False)["prior"] == late["prior"]
        assert analyze_window(path, "t0100", 60, None, store=cold, prior=True,
                              refresh=False)["prior"] == early["prior"]
        cold.close()


def test_the_prior_and_the_window_are_rolled_up_the_SAME_WAY():
    """`departure` subtracts one share from the other, so the two sides have to be computed
    alike or the number is meaningless -- the rule `dynamics` states for its own two sides.

    `language` is the dimension that makes this real: `lang` rows exist ONLY through reconcile,
    whose answer depends on which declarations are in scope, so a prior read from the stored
    FILE-scoped reconciliation while the window re-scopes its own would compare two different
    quantities. Asserted on the CALLS, not on a label."""
    with tempfile.TemporaryDirectory() as tmp:
        path = _write(tmp, _two_phase())
        st = open_store(os.path.join(tmp, "state", "refseries.db"))
        ingest_file(st, path, None)
        seen, real = [], st.window_rows

        def spy(session, start, end, exclude_slots=()):
            seen.append(tuple(exclude_slots))
            return real(session, start, end, exclude_slots)

        st.window_rows = spy
        analyze_window(path, "TARGET", 60, None, store=st, prior=True, refresh=False)
        assert len(seen) == 2, seen
        assert seen[0] == seen[1] == (RECONCILE_SLOT,), seen
        st.close()


def test_the_prior_block_is_opt_in_so_the_parse_oracle_stays_comparable():
    """`analyze_window_by_parse` is the EQUIVALENCE ORACLE and structurally cannot compute a
    prior -- a second, much wider rollup is exactly what the store bought and the parse path did
    not have. Opt-in is what keeps the field-for-field equality expressible; defaulting it on
    would mean either weakening the comparison to "equal except for these keys" or asserting
    nothing about the digest at all. Same contract `sizer` already has."""
    with tempfile.TemporaryDirectory() as tmp:
        path = _write(tmp, _two_phase())
        st = open_store(os.path.join(tmp, "state", "refseries.db"))
        ingest_file(st, path, None)
        plain = analyze_window(path, "TARGET", 60, None, store=st)
        assert "prior" not in plain, plain.keys()
        assert plain == analyze_window_by_parse(path, "TARGET", 60, None), "the oracle diverged"
        st.close()


def test_the_prior_never_reaches_below_the_retention_floor_and_says_so():
    """A prior starts before the window it contrasts, so it reaches under the serving floor
    whenever one exists -- and must CLAMP rather than raise, or every window on a pruned store
    would 410 for a block that is decoration. The truncation is REPORTED: a silently shorter
    input is the defect `omittedNotice` exists to prevent, one level up."""
    with tempfile.TemporaryDirectory() as tmp:
        path, st, out = _served(tmp, _two_phase())
        assert out["prior"]["clamped"] is False, out["prior"]
        # Prune everything before the window's own start: the window is still answerable and the
        # prior is now empty, but the response says the horizon cut it rather than implying the
        # session had no history.
        end = datetime.fromisoformat(out["window_end"]).timestamp()
        # Everything older than 65 minutes goes: the 60-minute window survives whole and its
        # prior does not, which is the one configuration that isolates this behaviour.
        st.enforce_retention(now=end, force=True, policy=RetentionPolicy(
            max_mb=DEFAULT_MAX_MB, retain_days=65.0 / 1440.0, term_retain_days=65.0 / 1440.0))
        assert st.serving_floor() is not None, "nothing was pruned; the assertion below is vacuous"
        clamped = analyze_window(path, "TARGET", 60, None, store=st, prior=True, refresh=False)
        assert clamped["prior"]["clamped"] is True, clamped["prior"]
        # The window is still served -- clamping, not refusing -- and the prior is honestly
        # SHORTER rather than silently the same: 5 minutes of session survive, not 60.
        assert clamped["workstreams"]["branch"]["value"] == "feature/ledger-split", clamped
        was = out["prior"]["dimensions"]["branch"]["evidence"]
        now = clamped["prior"]["dimensions"]["branch"]["evidence"]
        assert 0 < now < was / 5, (was, now)
        st.close()


def test_a_tick_window_carries_its_own_prior_cut_at_its_own_start():
    """A tick-emitted window is not a lesser window. The tick characterises the slices no
    prompt's look-back reaches (55% of turns -> 99.5% with it), and those windows publish through
    the same conversion the prompt's do -- so a reader who saw the prior beside one and not the
    other would have no way to know why.

    Discriminating on the thing that is easy to get wrong: each window recomputes its OWN prior,
    cut at its OWN start. A prior computed once per tick and shared would give the LATER window
    a prior that contains the earlier one's evidence and vice versa; here the first window's
    prior is empty (it opens the session) and the second's is not."""
    from app.analysis.tick import tick

    with tempfile.TemporaryDirectory() as tmp:
        turns = _two_phase()
        path = _write(tmp, turns)
        st = open_store(os.path.join(tmp, "state", "refseries.db"))
        ingest_file(st, path, None)
        end = BASE + timedelta(minutes=120)
        out = tick(st, path, cursor_ts=BASE.timestamp(), prompt_ts=[],
                   now=(end + timedelta(minutes=90)).timestamp(), span_minutes=60, nlp=None,
                   prior=True)
        assert len(out["windows"]) >= 2, out
        first, second = out["windows"][0], out["windows"][1]
        assert first["prior"]["dimensions"]["branch"]["status"] == "absent", first["prior"]
        assert second["prior"]["dimensions"]["branch"]["evidence"] > 0, second["prior"]
        # and the second window's prior stops at the second window's own start
        assert second["prior"]["dimensions"]["branch"]["value"] == "main", second["prior"]
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
