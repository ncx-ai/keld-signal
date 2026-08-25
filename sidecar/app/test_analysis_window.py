import re
import sys, os
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from app.analysis import SCHEMA
from app.analysis.window import (MIN_EVIDENCE, attribution, dominant,
                                 min_evidence_for, rollup)
from app.analysis.workstreams import ALLOCATION, payload

R = [(0, "s", "r", "b", False, "ref", "artifact", "code", 9.0),
     (0, "s", "r", "b", False, "ref", "artifact", "prose", 1.0),
     (0, "s", "r", "b", False, "ref", "lang", "Go", 5.0),
     (0, "s", "r", "b", False, "ref", "lang", "Python", 5.0),
     (0, "s", "r", "b", False, "ref", "service", "127.0.0.1", 20.0),
     (0, "s", "r", "b", False, "ref", "service", "github.com", 2.0)]


def test_a_dominant_value_is_reported_with_its_share():
    assert dominant(rollup(R), "artifact")[:2] == ("code", 0.9)


def test_a_tie_is_unattributed_rather_than_an_arbitrary_pick():
    """Multi-label double-counts spend, and a silently chosen winner is the
    plausible-wrong-number failure this work hit roughly twenty times."""
    v, share, _ = dominant(rollup(R), "lang")
    assert v is None and share == 0.5


# --- minimum evidence -----------------------------------------------------------------------
#
# The share floor alone says nothing about how much was counted. One tool call gives share=1.0,
# and AnalyzeLabeled drops `evidence` on the way to the published enrichment, so a report cannot
# tell one observation from five hundred. Measured on the 572-window sample in
# ~/keld/refseries-context/workstreams.ndjson: 330 dimension slots publish share=1.0 off fewer
# than five observations, 129 of them off a single one.

def _n(level, ref, n):
    return (0, "s", "r", "b", False, "ref", level, ref, float(n))


def test_a_single_observation_is_not_a_dominant_value():
    """share=1.0 over one observation is the plausible-wrong-number failure this project keeps
    hitting: a fresh session's first prompt in /tmp would publish `project` at confidence 1.0."""
    v, share, total = dominant(rollup([_n("workspace", "scratch", 1)]), "workspace")
    assert v is None, v
    assert (share, total) == (1.0, 1), (share, total)


def test_an_underevidenced_window_still_reports_its_share_and_total():
    """Unattributed must stay VISIBLE — same reasoning the tie branch already follows. Only the
    value is withheld; the measurement is not."""
    rl = rollup([_n("lang", "Go", 3), _n("lang", "Python", 1)])
    v, share, total = dominant(rl, "lang")
    assert v is None
    assert (share, total) == (0.75, 4)


def test_the_floor_admits_a_window_at_exactly_min_evidence():
    """The bound is inclusive: MIN_EVIDENCE is the first count at which the 0.5 share floor is a
    statement about the population rather than about the sample."""
    rl = rollup([_n("workspace", "widget-app", MIN_EVIDENCE)])
    assert dominant(rl, "workspace")[0] == "widget-app"


def test_min_evidence_does_not_replace_the_share_floor():
    """Two independent conditions. Plenty of evidence, no majority -> still unattributed."""
    rl = rollup([_n("lang", "Go", 40), _n("lang", "Python", 35), _n("lang", "Rust", 30)])
    v, share, total = dominant(rl, "lang")
    assert v is None and total == 105 and share < 0.5


def test_min_evidence_is_overridable_per_call():
    rl = rollup([_n("workspace", "widget-app", 2)])
    assert dominant(rl, "workspace")[0] is None
    assert dominant(rl, "workspace", min_evidence=2)[0] == "widget-app"


def test_the_payload_reports_an_underevidenced_dimension_as_unattributed():
    """The property that actually reaches Atlas: workstreams.payload must not hand a
    one-observation dimension to publish as a real answer."""
    rl = rollup([_n("workspace", "scratch", 1), _n("branch", "main", 1)])
    ws = payload(rl)["workstreams"]
    assert ws["project"] is None and ws["branch"] is None, ws


def test_an_absent_level_produces_no_key_rather_than_an_empty_one():
    assert payload(rollup(R))["workstreams"]["skill"] is None


def test_the_skill_level_publishes_under_the_name_skill_not_workflow():
    """The dimension is the argument to a `Skill` tool call -- `superpowers:writing-plans`,
    `anthropic-skills:pptx` -- and skills exist for everything, not only for processes, so
    `workflow` INFLATES what the level holds. It is written from exactly two sources
    (levels.py: `inp["skill"]` on a `Skill` tool_use, and a turn's `attributionSkill`), and
    only 38.4% of 198 corpus transcripts carry ANY skill evidence at all: a reader who sees
    `workflow` expects a dimension populated for everyone, while a reader who sees `skill` asks
    the right next question, which is what the other 61.6% of sessions look like.

    Asserted on the PUBLISHED payload as well as on ALLOCATION, because the list is what the
    Go client, dynamics and the prior all derive their vocabulary from."""
    names = {n for n, _lv, _f in ALLOCATION}
    assert "skill" in names and "workflow" not in names, sorted(names)
    assert {n: lv for n, lv, _f in ALLOCATION}["skill"] == "skill", ALLOCATION
    ws = payload(rollup([_n("skill", "superpowers:writing-plans", 20)]))["workstreams"]
    # Asserted on MEMBERSHIP before value, so a payload that still emits the old key fails by
    # assertion rather than dying on a KeyError three lines down -- a crash is a weaker signal
    # than a statement of what was expected, and this file's runner only catches the latter.
    assert "skill" in ws and "workflow" not in ws, sorted(ws)
    assert ws["skill"] == {"value": "superpowers:writing-plans", "share": 1.0, "evidence": 20,
                           "provenance": "known:tool_inputs"}, ws


def test_payload_carries_its_schema_version():
    """These values land in financial reports; a silent shape change is the reproducibility
    failure the earlier handoff called out, so the payload is versioned from the first release."""
    assert payload(rollup(R))["schema"] == SCHEMA == 12


# --- the floor generalised to an arbitrary slice length ---------------------------------------
#
# `/analyze` is gaining a short recent SLICE read against a longer baseline, so the floor has to
# hold at 5 minutes as well as 60. The derivation (see MIN_EVIDENCE) mentions no duration at all
# — it is a statement about the NUMBER OF OBSERVATIONS — so these pin that the generalisation is
# the same constant, that it is computed from the share floor rather than typed, and that the
# 60-minute behaviour is byte-for-byte what it was.

def test_the_floor_is_computed_from_the_share_floor_not_typed():
    """MIN_EVIDENCE is the OUTPUT of its own derivation, so the two cannot drift apart.

    This inspects the SOURCE, deliberately, and it is the only test here that does. Asserting
    `MIN_EVIDENCE == min_evidence_for(0.5, 0.05)` passes just as happily against a typed `= 5`,
    which is the vacuous version of this test — confirmed by mutation. What must hold is that the
    constant cannot be edited independently of the argument that produced it: change alpha or the
    formula and the constant follows, or the comment above it becomes a lie about the shipped
    number. Only the assignment expresses that, so only the assignment can be checked.
    """
    import inspect
    import app.analysis.window as w
    src = inspect.getsource(w)
    assert re.search(r"^MIN_EVIDENCE = min_evidence_for\(0\.5, 0\.05\)", src, re.M), \
        "MIN_EVIDENCE must be computed by min_evidence_for, not typed"
    assert MIN_EVIDENCE == min_evidence_for(0.5, 0.05) == 5


def test_the_floor_tracks_the_share_floor_and_the_significance_level():
    """The only two inputs. A stricter share floor needs MORE observations to be distinguishable
    from its own null, and a stricter alpha needs more too — neither is a duration."""
    assert min_evidence_for(0.5, 0.01) == 7          # 0.5**7 = 0.0078 <= 0.01, 0.5**6 = 0.0156
    assert min_evidence_for(0.75, 0.05) == 11        # 0.75**11 = 0.042 <= 0.05
    assert min_evidence_for(0.5, 0.5) == 1           # the floor itself is already significant
    assert min_evidence_for(0.5, 0.06) == 5          # 0.5**5 = 0.031; n=4 is 0.0625 > 0.06


def test_the_derived_floor_is_the_first_n_that_clears_alpha():
    """The property, not the arithmetic: n clears it and n-1 does not."""
    for floor in (0.5, 0.6, 0.75, 0.9):
        for alpha in (0.1, 0.05, 0.01):
            n = min_evidence_for(floor, alpha)
            assert floor ** n <= alpha, (floor, alpha, n)
            assert n == 1 or floor ** (n - 1) > alpha, (floor, alpha, n)


def test_the_floor_does_not_take_a_duration():
    """The one thing a caller must not be able to do is scale the floor by slice length. A
    duration-scaled floor makes the significance of a published attribution a function of the
    slice, while `share` and `value` look identical either way — and `evidence` is dropped before
    publish, so no reader could tell. That is the defect MIN_EVIDENCE exists to prevent,
    reintroduced through the back door."""
    import inspect
    params = set(inspect.signature(min_evidence_for).parameters)
    assert params == {"floor", "alpha"}, params


def test_sixty_minute_dominance_is_unchanged():
    """The regression pin for what production publishes TODAY. Every one of these was the answer
    before the slice work began and must still be, field for field."""
    assert dominant(rollup(R), "artifact") == ("code", 0.9, 10)
    assert dominant(rollup(R), "lang") == (None, 0.5, 10)
    assert dominant(rollup(R), "workspace") == (None, 0.0, 0)
    assert dominant(rollup([_n("workspace", "scratch", 1)]), "workspace") == (None, 1.0, 1)
    assert dominant(rollup([_n("workspace", "w", 5)]), "workspace") == ("w", 1.0, 5)
    assert dominant(rollup([_n("lang", "Go", 3), _n("lang", "Py", 1)]), "lang") == (None, 0.75, 4)


# --- WHY a slot is unattributed --------------------------------------------------------------
#
# Measured over 20,000 windows of the frozen corpus (see scripts/evidence_floor.py): at a
# 5-minute slice, `tooling` is unattributed 97.3% of the time and 77.8 points of that is NO
# EVIDENCE AT ALL at that level, not a floor rejection. A dynamics comparison that reads "absent"
# as "the dominant value changed" would report a context switch out of an empty level. So the
# reason is a first-class output, and the four reasons are four different facts.

def test_every_way_a_slot_fails_is_named():
    a = attribution(rollup(R), "artifact")
    assert (a.reason, a.value, a.share, a.evidence) == ("attributed", "code", 0.9, 10)
    assert attribution(rollup(R), "workspace").reason == "absent"
    assert attribution(rollup(R), "lang").reason == "tie"
    assert attribution(rollup([_n("workspace", "scratch", 1)]), "workspace").reason == "thin"
    assert attribution(rollup([_n("lang", "Go", 40), _n("lang", "Py", 35),
                               _n("lang", "Rs", 30)]), "lang").reason == "no_majority"


def test_an_absent_level_is_not_a_thin_one():
    """`bin` is sparse and a level can simply never have fired. Reporting that as `thin` would
    invite a caller to widen the slice to fix something no slice length can fix — measured:
    `skill`'s `absent` share only falls 77.5% -> 55.3% from 5 to 60 minutes, while its `thin`
    share is under 3% at every length."""
    absent = attribution(rollup([]), "workspace")
    assert (absent.reason, absent.evidence) == ("absent", 0)
    thin = attribution(rollup([_n("workspace", "w", 4)]), "workspace")
    assert (thin.reason, thin.evidence) == ("thin", 4)


def test_too_little_evidence_outranks_a_tie():
    """One observation each is not a genuinely split window; it is two observations. Naming it
    `tie` would say the work was divided when the truth is that nothing was counted yet."""
    rl = rollup([_n("lang", "Go", 1), _n("lang", "Py", 1)])
    assert attribution(rl, "lang").reason == "thin"


def test_dominant_and_attribution_cannot_disagree():
    """`dominant` is a projection of `attribution`, so a value is returned exactly when the
    reason is `attributed` — there is no fourth path by which one could say yes and the other
    no."""
    cases = [[], [_n("workspace", "w", 1)], [_n("workspace", "w", 5)],
             [_n("lang", "Go", 3), _n("lang", "Py", 3)],
             [_n("lang", "Go", 40), _n("lang", "Py", 35), _n("lang", "Rs", 30)],
             [_n("artifact", "code", 9), _n("artifact", "prose", 1)]]
    for rows in cases:
        for level in ("workspace", "lang", "artifact"):
            rl = rollup(rows)
            v, share, tot = dominant(rl, level)
            a = attribution(rl, level)
            assert (v, share, tot) == (a.value, a.share, a.evidence), (rows, level)
            assert (v is not None) == (a.reason == "attributed"), (rows, level)


def test_loopback_is_not_an_external_system():
    """127.0.0.1 and localhost are 85% of the raw service level and would otherwise be the top
    'system this org depends on'."""
    ext = payload(rollup(R))["inventory"]["external_systems"]
    assert [e["value"] for e in ext] == ["github.com"], ext


# --- files / directories / components ---------------------------------------------------------
#
# `reconcile()` has written `file`/`dir`/`component` rows since this package existed; these three
# publish that data for the first time, as INVENTORY (a frequency distribution), not ALLOCATION —
# see workstreams.INVENTORY's own comment for the corpus measurement that set each cap.

def test_the_three_path_levels_publish_as_inventory_with_correct_counts():
    rl = rollup([_n("file", "internal/agent/daemon/daemon.go", 5),
                 _n("file", "sidecar/app/main.py", 3),
                 _n("dir", "internal/agent/daemon", 5),
                 _n("dir", "sidecar/app", 3),
                 _n("component", "internal/agent/daemon", 5),
                 _n("component", "sidecar/app", 3)])
    inv = payload(rl)["inventory"]
    assert inv["files"] == [{"value": "internal/agent/daemon/daemon.go", "n": 5},
                            {"value": "sidecar/app/main.py", "n": 3}], inv["files"]
    assert inv["directories"] == [{"value": "internal/agent/daemon", "n": 5},
                                  {"value": "sidecar/app", "n": 3}], inv["directories"]
    assert inv["components"] == [{"value": "internal/agent/daemon", "n": 5},
                                 {"value": "sidecar/app", "n": 3}], inv["components"]


def test_the_file_cap_truncates_at_forty_and_reports_the_cut():
    """45 distinct files, strictly descending counts so the boundary is unambiguous: f00 (45)
    down to f44 (1). The cap (40) keeps f00..f39 and reports the 5 it dropped."""
    rows = [_n("file", f"f{i:02d}.py", 45 - i) for i in range(45)]
    files = payload(rollup(rows))["inventory"]["files"]
    assert len(files) == 40, len(files)
    assert files[0] == {"value": "f00.py", "n": 45}
    assert files[-1] == {"value": "f39.py", "n": 6}
    survivors = {f["value"] for f in files}
    assert survivors.isdisjoint({"f40.py", "f41.py", "f42.py", "f43.py", "f44.py"}), survivors


def test_directories_and_components_cap_at_their_own_measured_n():
    rl = rollup([_n("dir", f"d{i:02d}", 30 - i) for i in range(30)] +
                [_n("component", f"c{i:02d}", 20 - i) for i in range(20)])
    inv = payload(rl)["inventory"]
    assert len(inv["directories"]) == 24, len(inv["directories"])
    assert len(inv["components"]) == 16, len(inv["components"])


def test_inventory_omitted_reports_the_cut_per_dimension():
    rl = rollup([_n("file", f"f{i:02d}.py", 45 - i) for i in range(45)] +
                [_n("dir", f"d{i:02d}", 30 - i) for i in range(30)] +
                [_n("component", f"c{i:02d}", 20 - i) for i in range(20)])
    assert payload(rl)["inventory_omitted"] == {"files": 5, "directories": 6, "components": 4}


def test_inventory_omitted_is_empty_when_nothing_is_cut():
    """An untruncated payload carries an empty dict, not nine zeros nobody needs to read."""
    assert payload(rollup(R))["inventory_omitted"] == {}


def test_a_dimension_under_its_own_cap_is_not_named_in_inventory_omitted():
    rl = rollup([_n("file", f"f{i:02d}.py", 45 - i) for i in range(45)])
    omitted = payload(rl)["inventory_omitted"]
    assert omitted == {"files": 5}, omitted
    assert "directories" not in omitted and "components" not in omitted


# The privacy assertion the brief requires: no published file/dir/component value may look
# absolute, home-relative, a Windows drive path, or escape the workspace via `../`. This is not
# re-litigating the corpus scan (already done, see workstreams.INVENTORY's comment) — it pins the
# invariant so a future change to `payload()` that ever let a raw value through unfiltered fails
# HERE rather than in a privacy review.
ABS_OR_ESCAPING = re.compile(r"^/|^~|^[A-Za-z]:|\.\.(/|$)")


def test_no_published_path_value_is_absolute_or_escapes_the_workspace():
    rl = rollup([_n("file", "internal/agent/daemon/daemon.go", 9),
                 _n("file", "sidecar/app/main.py", 3),
                 _n("dir", "internal/agent/daemon", 9),
                 _n("dir", "sidecar/app", 3),
                 _n("component", "internal/agent/daemon", 9),
                 _n("component", "sidecar/app", 3)])
    inv = payload(rl)["inventory"]
    for dim in ("files", "directories", "components"):
        for entry in inv[dim]:
            assert not ABS_OR_ESCAPING.search(entry["value"]), \
                f"{dim} published a non-workspace-relative value: {entry['value']!r}"


# --- `repo`: a series level fed by the DAEMON, not a stamp on the payload --------------------
#
# `repo` is the one ALLOCATION dimension whose rows the sidecar cannot produce for itself:
# /analyze and /ingest are confined to KELD_ANALYZE_ROOTS precisely so they cannot open a repo's
# .git/config as the daemon's user. The facts arrive on the request and are written as EVENTS
# during ingest, which is what these pin -- because the alternative design (overlay the value
# onto the digest) would look identical from a single payload and would not roll up, bin, or
# carry an evidence count.

def test_repo_is_an_allocation_dimension_with_its_own_provenance():
    """It rolls up exactly like its siblings -- same `dominant` call, real share, real evidence
    count -- but its `provenance` is DIFFERENT, and that difference is the whole point: a reader
    who cannot tell "we counted this from tool inputs" from "the daemon read this off disk"
    cannot judge either."""
    names = {n for n, _lv, _f in ALLOCATION}
    assert "repo" in names, sorted(names)
    assert {n: lv for n, lv, _f in ALLOCATION}["repo"] == "repo", ALLOCATION
    ws = payload(rollup([_n("repo", "github.com/ncx-ai/keld-atlas", 40)]))["workstreams"]
    assert ws["repo"] == {"value": "github.com/ncx-ai/keld-atlas", "share": 1.0, "evidence": 40,
                          "provenance": "known:daemon_git"}, ws
    # And every sibling keeps the constant it always had -- the override is per dimension, not a
    # rename of the field's default.
    assert payload(rollup([_n("branch", "main", 20)]))["workstreams"]["branch"]["provenance"] \
        == "known:tool_inputs"


def test_repo_is_absent_not_empty_when_nothing_resolved_it():
    """A PROJECT DIRECTORY IS NOT NECESSARILY A REPOSITORY. A scratch dir, a mounted share, a
    documents tree -- real work happens in directories that were never `git init`ed, and the
    daemon sends "" for them. No rows are written, so the dimension is unattributed exactly like
    any other level that saw nothing. Never an empty string, and never the directory name:
    `project` remains the identity there.

    Asserted on a rollup that HAS other levels, so this is "repo specifically said nothing"
    rather than "the payload was empty"."""
    ws = payload(rollup([_n("workspace", "scratch-notes", 30)]))["workstreams"]
    assert ws["repo"] is None, ws["repo"]
    assert ws["project"] == {"value": "scratch-notes", "share": 1.0, "evidence": 30,
                             "provenance": "known:tool_inputs"}, ws["project"]


def test_repo_publishes_beside_project_and_never_instead_of_it():
    """Measured over 54 real transcripts, `workspace -> repo` is a PERFECT 1:1 mapping and
    `repo`'s cardinality is strictly LOWER (4 distinct workspaces -> 3 distinct repos), because
    `tmp` is real work in a directory that is not a checkout. So replacing `project` with `repo`
    would lose a distinction the series can make. Both ship, always."""
    rl = rollup([_n("repo", "github.com/ncx-ai/keld-signal", 30),
                 _n("workspace", "keld-signal", 30)])
    ws = payload(rl)["workstreams"]
    assert ws["repo"]["value"] == "github.com/ncx-ai/keld-signal"
    assert ws["project"]["value"] == "keld-signal"


def test_repo_reports_no_dynamics_and_no_prior():
    """Both withheld on the SAME measurement, not by oversight: 0 of 50 real transcripts span
    more than one repository (34 of 50 span more than one DIRECTORY and none of them changes
    repo), so a dynamic would be identically 0.000 and a prior would agree 100% of the time --
    `project`'s exact disqualification, one level coarser. Publishing a constant is what those
    two measurements exist to prevent."""
    from app.analysis.dynamics import DROPPED_DIMENSIONS, DYNAMIC_DIMENSIONS
    from app.analysis.prior import ENABLED, PRIOR_DIMENSIONS
    assert "repo" in DROPPED_DIMENSIONS, DROPPED_DIMENSIONS
    assert "repo" not in {n for n, _lv, _f in DYNAMIC_DIMENSIONS}
    assert "repo" not in ENABLED, ENABLED
    assert "repo" not in {n for n, _lv, _f in PRIOR_DIMENSIONS}


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn(); print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
