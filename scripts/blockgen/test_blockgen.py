#!/usr/bin/env python3
"""Pure-python tests for blockgen.py — no sidecar, no pytest, host python3 only.

Standalone script: every top-level `test_*` function is run by the `__main__` runner at the
bottom, matching the sidecar's own test convention (see AGENTS.md's "Sidecar tests are
standalone scripts"). Run with: `python3 scripts/blockgen/test_blockgen.py`.
"""
import filecmp
import json
import os
import re
import subprocess
import sys
import tempfile
from datetime import datetime, timezone

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import blockgen  # noqa: E402

# Every test that only cares about STRUCTURE (not "does this land on today") pins `--epoch`
# rather than taking the `--end="now"` default: `epoch` mode promises byte-identical output for a
# given seed, which is exactly what the reproducibility tests below need, and it is what keeps
# every other test's assertions independent of the real calendar date the suite happens to run
# on. Deliberately distinct from `blockgen.SYNTHETIC_EPOCH`'s literal value so a test relying on
# one accidentally matching the other would be caught.
TEST_EPOCH = "2025-11-03T09:00:00Z"


def _gen(seed, sessions=8, days=3, profile=None, epoch=TEST_EPOCH):
    prof = profile or blockgen.load_profile(None)
    return blockgen.generate(prof, sessions, days, seed, epoch=epoch)


# ---------------------------------------------------------------------- determinism / identity

def test_same_seed_is_byte_identical():
    with tempfile.TemporaryDirectory() as d1, tempfile.TemporaryDirectory() as d2:
        m1, f1 = _gen(seed=7)
        m2, f2 = _gen(seed=7)
        blockgen.write_batch(d1, m1, f1)
        blockgen.write_batch(d2, m2, f2)
        rel1 = sorted(_all_rel_files(d1))
        rel2 = sorted(_all_rel_files(d2))
        assert rel1 == rel2, f"file layout differs: {rel1} vs {rel2}"
        for rel in rel1:
            a, b = os.path.join(d1, rel), os.path.join(d2, rel)
            assert filecmp.cmp(a, b, shallow=False), f"{rel} differs byte-for-byte across runs"


def _all_rel_files(root):
    out = []
    for dirpath, _dirs, names in os.walk(root):
        for n in names:
            out.append(os.path.relpath(os.path.join(dirpath, n), root))
    return out


def test_different_seeds_share_no_session_or_prompt_id():
    m1, _f1 = _gen(seed=1)
    m2, _f2 = _gen(seed=2)
    ids1 = {s["session_id"] for s in m1["session_list"]}
    ids2 = {s["session_id"] for s in m2["session_list"]}
    assert not (ids1 & ids2), f"shared session ids across seeds: {ids1 & ids2}"
    prompts1 = {p for s in m1["session_list"] for p in s["prompt_ids"]}
    prompts2 = {p for s in m2["session_list"] for p in s["prompt_ids"]}
    assert not (prompts1 & prompts2), f"shared prompt ids across seeds: {prompts1 & prompts2}"


# --------------------------------------------------------------------------------- time base

def _max_line_ts_epoch(files):
    best = None
    for lines in files.values():
        for line in lines:
            ts = line.get("timestamp")
            if not ts:
                continue
            t = datetime.fromisoformat(ts.replace("Z", "+00:00")).timestamp()
            if best is None or t > best:
                best = t
    return best


def test_default_anchors_last_event_to_now():
    """Regression test: the coordinator's blocker. `SYNTHETIC_EPOCH` alone used to put every
    generated block eight months in the past — a developer runs blockgen, opens the Keld Signal
    page (whose main view is TODAY), and sees an empty day, which reads as "the app is broken".
    The DEFAULT (no `--epoch`, no `--end`) must land the corpus's own latest event within a
    couple of minutes of real now."""
    profile = blockgen.load_profile(None)
    before = datetime.now(timezone.utc).timestamp()
    _m, files = blockgen.generate(profile, sessions=3, days=2, seed=42)
    after = datetime.now(timezone.utc).timestamp()
    last = _max_line_ts_epoch(files)
    assert last is not None
    assert before - 5 <= last <= after + 5, (
        f"default anchor landed {after - last:.1f}s away from 'now' — blockgen is still "
        "generating a fixed-past corpus")


def test_explicit_end_anchors_last_event_exactly():
    profile = blockgen.load_profile(None)
    end = "2026-03-15T10:00:00Z"
    _m, files = blockgen.generate(profile, sessions=4, days=3, seed=5, end=end)
    last = _max_line_ts_epoch(files)
    target = datetime.fromisoformat(end.replace("Z", "+00:00")).timestamp()
    assert abs(last - target) < 1.0, f"last event {last} not aligned to --end {target}"


def test_end_mode_with_a_fixed_instant_is_also_byte_identical():
    """`--end` is real-time-dependent only through its `"now"` default; given the SAME explicit
    instant twice, it must reproduce byte-identical output exactly like `--epoch` does — the
    shift is one deterministic number, not a second source of entropy."""
    profile = blockgen.load_profile(None)
    end = "2026-05-01T00:00:00Z"
    m1, f1 = blockgen.generate(profile, sessions=5, days=3, seed=9, end=end)
    m2, f2 = blockgen.generate(profile, sessions=5, days=3, seed=9, end=end)
    assert m1 == m2
    assert f1 == f2


def test_epoch_and_end_are_mutually_exclusive():
    profile = blockgen.load_profile(None)
    try:
        blockgen.generate(profile, sessions=2, days=1, seed=0,
                          epoch="2026-01-01T00:00:00Z", end="now")
    except ValueError:
        pass
    else:
        raise AssertionError("generate() accepted both epoch= and end=")


def _run_cli(root, *extra_args):
    """Invoke the CLI with `--out`/`--workspaces` both nested inside `root` (a caller-owned
    `tempfile.TemporaryDirectory()`), so a test never leaks a real git-checkout tree to the CLI's
    own default `--workspaces` location BESIDE `--out` — i.e. outside whatever the test itself is
    cleaning up. Returns `(out_dir, workspaces_dir, completed_process)`."""
    out_dir = os.path.join(root, "out")
    workspaces_dir = os.path.join(root, "workspaces")
    result = subprocess.run(
        [sys.executable, os.path.join(os.path.dirname(__file__), "blockgen.py"),
        "--out", out_dir, "--workspaces", workspaces_dir, *extra_args],
        capture_output=True, text=True)
    return out_dir, workspaces_dir, result


def test_cli_default_end_lands_near_today():
    with tempfile.TemporaryDirectory() as root:
        out_dir, _ws, result = _run_cli(root, "--sessions", "2", "--days", "1", "--seed", "1")
        assert result.returncode == 0, result.stderr
        manifest = json.load(open(os.path.join(out_dir, "manifest.json")))
        assert manifest["anchor"]["mode"] == "end"
        end_ts = datetime.fromisoformat(
            manifest["anchor"]["end"].replace("Z", "+00:00")).timestamp()
        now = datetime.now(timezone.utc).timestamp()
        assert abs(now - end_ts) < 120, "CLI default --end did not resolve to real 'now'"


def test_cli_epoch_flag_pins_the_old_fixed_base():
    with tempfile.TemporaryDirectory() as root:
        out_dir, _ws, result = _run_cli(root, "--sessions", "2", "--days", "1", "--seed", "1",
                                        "--epoch", TEST_EPOCH)
        assert result.returncode == 0, result.stderr
        manifest = json.load(open(os.path.join(out_dir, "manifest.json")))
        assert manifest["anchor"] == {"mode": "epoch", "epoch": "2025-11-03T09:00:00.000Z"}


def test_cli_rejects_epoch_and_end_together():
    with tempfile.TemporaryDirectory() as root:
        _out, _ws, result = _run_cli(root, "--sessions", "1", "--days", "1",
                                     "--epoch", TEST_EPOCH, "--end", "now")
        assert result.returncode != 0


def test_cli_workspaces_flag_materialises_real_checkouts():
    """The CLI plumbing for the coordinator's D1 fix: `--workspaces` actually reaches
    `materialize_workspaces`, and every generated session's `cwd` points inside it rather than at
    the CLI's own default location."""
    with tempfile.TemporaryDirectory() as root:
        out_dir, workspaces_dir, result = _run_cli(
            root, "--sessions", "2", "--days", "1", "--seed", "1", "--epoch", TEST_EPOCH)
        assert result.returncode == 0, result.stderr
        manifest = json.load(open(os.path.join(out_dir, "manifest.json")))
        for session in manifest["session_list"]:
            assert session["cwd"].startswith(workspaces_dir + os.sep), (
                f"cwd {session['cwd']} does not point inside --workspaces {workspaces_dir}")
            git_config = os.path.join(session["cwd"], ".git", "config")
            assert os.path.isfile(git_config), f"no real .git/config at {session['cwd']}"
            with open(git_config) as f:
                config_text = f.read()
            assert f'url = https://{session["repo_remote"]}.git' in config_text, (
                f"{git_config} does not carry the intended remote {session['repo_remote']!r}")


# ------------------------------------------------------------------------------- branch/ticket

def test_ticket_branches_get_distinct_numbers_at_realistic_session_count():
    """Regression test: the coordinator's second defect. Every ticket used to be numbered "-100"
    (ATLAS-100, KELD-100, SDK-100 — three distinct KEYS, but the NUMBER never moved), because the
    old per-repo branch WRR made `main` the near-universal first pick and starved the ticket
    category of enough draws to ever repeat a repository. `--sessions 8` must now produce at
    least two ticket-carrying branches whose NUMBERS differ, proving the counter actually
    advances rather than restarting at 100 for every repository."""
    pat = re.compile(r"^feature/([A-Z]+)-(\d+)-")
    for seed in (0, 1, 7, 123):
        m, _f = _gen(seed=seed, sessions=8)
        tickets = []
        for s in m["session_list"]:
            found = pat.match(s["branch"])
            if found:
                tickets.append((found.group(1), int(found.group(2))))
        assert len(tickets) >= 2, f"seed={seed}: fewer than 2 ticket branches: {tickets}"
        assert len(set(tickets)) == len(tickets), f"seed={seed}: duplicate ticket keys: {tickets}"
        numbers = {n for _prefix, n in tickets}
        assert len(numbers) >= 2, (
            f"seed={seed}: every ticket still numbered the same: {tickets}")


def test_uuid_and_prompt_id_are_never_equal_on_a_user_line():
    """Contract: 'every human prompt lands in the sidecar's prompt index under promptId (NOT
    uuid)'. Real Claude Code transcripts never make the two equal on a user line (measured on a
    real transcript in this repo); blockgen must not accidentally make the test of that
    distinction vacuous by emitting uuid == promptId."""
    _m, files = _gen(seed=3, sessions=4)
    checked = 0
    for lines in files.values():
        for line in lines:
            if line.get("type") == "user" and "promptId" in line:
                assert line["uuid"] != line["promptId"]
                checked += 1
    assert checked > 0


# ------------------------------------------------------------------------------ line shapes

REQUIRED_USER_KEYS = ("promptId", "uuid", "parentUuid", "timestamp", "sessionId", "cwd",
                     "gitBranch", "version")
REQUIRED_ASST_KEYS = ("requestId", "uuid", "timestamp", "sessionId", "cwd", "version")


def test_every_line_parses_and_required_lines_carry_required_fields():
    _m, files = _gen(seed=11, sessions=6)
    n_user = n_asst = n_bookkeeping = 0
    for rel_path, lines in files.items():
        for raw in lines:
            # Round-trip through JSON exactly as the sidecar's readers would see it on disk.
            encoded = json.dumps(raw)
            decoded = json.loads(encoded)
            t = decoded.get("type")
            if t == "user":
                for k in REQUIRED_USER_KEYS:
                    assert k in decoded, f"{rel_path}: user line missing {k!r}"
                assert isinstance(decoded["message"]["content"], str)
                assert decoded["timestamp"].endswith("Z")
                n_user += 1
            elif t == "assistant":
                for k in REQUIRED_ASST_KEYS:
                    assert k in decoded, f"{rel_path}: assistant line missing {k!r}"
                msg = decoded["message"]
                assert "model" in msg and "usage" in msg
                u = msg["usage"]
                for k in ("input_tokens", "output_tokens", "cache_read_input_tokens",
                         "cache_creation_input_tokens"):
                    assert k in u, f"assistant usage missing {k!r}"
                assert isinstance(msg["content"], list) and msg["content"]
                for b in msg["content"]:
                    assert b["type"] in ("text", "tool_use")
                    if b["type"] == "tool_use":
                        assert b["name"] in ("Bash", "Read", "Edit", "Write", "Grep")
                n_asst += 1
            else:
                n_bookkeeping += 1
    assert n_user > 0 and n_asst > 0 and n_bookkeeping > 0


def test_bookkeeping_lines_carry_no_timestamp():
    """These must be the shapes `transcript.turns_in` skips via its substring check before ever
    parsing JSON — i.e. genuinely untimestamped, matching real Claude Code opens."""
    _m, files = _gen(seed=4, sessions=2)
    for lines in files.values():
        for line in lines[:3]:
            assert line["type"] in ("mode", "custom-title", "file-history-snapshot")
            assert "timestamp" not in line


def test_two_assistant_lines_per_request_share_usage_and_request_id():
    _m, files = _gen(seed=5, sessions=3)
    by_request = {}
    for lines in files.values():
        for line in lines:
            if line.get("type") != "assistant":
                continue
            by_request.setdefault(line["requestId"], []).append(line)
    assert by_request
    for rid, group in by_request.items():
        assert len(group) == 2, f"request {rid} has {len(group)} assistant lines, want 2"
        u0, u1 = group[0]["message"]["usage"], group[1]["message"]["usage"]
        assert u0 == u1, f"request {rid}: usage differs across its own lines"
        assert group[0]["message"]["model"] == group[1]["message"]["model"]


# -------------------------------------------------------------------------- workspace planting

def test_workspace_evidence_is_planted():
    """Contract: plant the repo so workspace.py resolves it — a marker-file Read plus a
    tool_use Bash command mentioning the remote URL."""
    _m, files = _gen(seed=6, sessions=3)
    for session in _m["session_list"]:
        lines = files[session["rel_path"]]
        remote_url = f"https://{session['repo_remote']}.git"
        saw_marker_read = False
        saw_remote_command = False
        for line in lines:
            if line.get("type") != "assistant":
                continue
            for b in line["message"]["content"]:
                if b["type"] != "tool_use":
                    continue
                if b["name"] == "Read" and b["input"].get("file_path", "").endswith("README.md"):
                    saw_marker_read = True
                if b["name"] == "Bash" and remote_url in b["input"].get("command", ""):
                    saw_remote_command = True
        assert saw_marker_read, f"{session['session_id']}: no REPO_MARKER Read planted"
        assert saw_remote_command, f"{session['session_id']}: no remote URL planted"


def test_materialize_workspaces_writes_a_real_resolvable_checkout():
    """Contract (coordinator's D1 gap): a materialised repository must be a REAL git checkout,
    not merely a directory containing files that look like one. Confirms it end to end using
    real `git` itself — the same tool any daemon-side reader could reasonably shell out to —
    rather than re-parsing `.git/config` by hand a second time here."""
    profile = blockgen.load_profile(None)
    with tempfile.TemporaryDirectory() as workspaces_dir:
        root = blockgen.materialize_workspaces(workspaces_dir, profile)
        for repo in profile["repositories"]:
            checkout = os.path.join(root, repo["workspace"])
            assert os.path.isdir(os.path.join(checkout, ".git"))
            marker = blockgen.PRIMARY_MARKER.get(repo["language"], "README.md")
            assert os.path.exists(os.path.join(checkout, marker)), (
                f"{checkout}: no {marker} package marker")
            origin_url = blockgen.read_origin_url(checkout)
            assert origin_url == f"https://{repo['remote']}.git", (
                f"{checkout}: origin is {origin_url!r}, wanted the profile's own remote")
            assert blockgen.normalise_remote(origin_url) == repo["remote"]

        # Idempotent: materialising twice into the same directory must not fail or drift.
        root2 = blockgen.materialize_workspaces(workspaces_dir, profile)
        assert root2 == root
        again = blockgen.read_origin_url(os.path.join(root, profile["repositories"][0]["workspace"]))
        assert again == f"https://{profile['repositories'][0]['remote']}.git"


def test_output_layout_matches_sanitised_cwd_convention():
    _m, files = _gen(seed=8, sessions=5)
    for session in _m["session_list"]:
        expect_dir = blockgen.sanitize_cwd(session["cwd"])
        assert session["sanitized_cwd"] == expect_dir
        assert session["rel_path"] == f"{expect_dir}/{session['session_id']}.jsonl"
        assert session["rel_path"] in files
        # No literal "/" survives sanitisation (it's a single flat directory name).
        assert expect_dir.count("/") == 0


# -------------------------------------------------------------------------------- fixed profile

def test_repo_assignment_matches_profile_weights_exactly():
    """METADATA MUST BE GENERATED, NOT RANDOM: repo counts over N sessions must be an exact
    function of the profile weights (deterministic WRR), not a sampling outcome."""
    profile = blockgen.load_profile(None)
    n = 50
    m, _f = _gen(seed=9, sessions=n, profile=profile)
    counts = {}
    for s in m["session_list"]:
        counts[s["repo_remote"]] = counts.get(s["repo_remote"], 0) + 1
    total_weight = sum(r["weight"] for r in profile["repositories"])
    for r in profile["repositories"]:
        expected = n * r["weight"] / total_weight
        got = counts.get(r["remote"], 0)
        assert abs(got - expected) <= 1, (
            f"{r['remote']}: expected ~{expected:.1f} sessions, got {got}")

    # Re-running the same weighted assignment with a DIFFERENT seed changes timing/text/ids but
    # must reproduce the exact same per-repo counts (WRR does not consult the rng stream).
    m2, _f2 = _gen(seed=123456, sessions=n, profile=profile)
    counts2 = {}
    for s in m2["session_list"]:
        counts2[s["repo_remote"]] = counts2.get(s["repo_remote"], 0) + 1
    assert counts == counts2


def test_each_session_bound_to_exactly_one_repository():
    _m, files = _gen(seed=10, sessions=6)
    for session in _m["session_list"]:
        lines = files[session["rel_path"]]
        cwds = {line["cwd"] for line in lines if "cwd" in line}
        assert cwds == {session["cwd"]}, f"session touches more than one cwd: {cwds}"
        branches = {line.get("gitBranch") for line in lines if line.get("type") in
                   ("user", "assistant")}
        assert branches == {session["branch"]}


def test_custom_profile_overrides_repositories():
    custom = blockgen.load_profile(None)
    custom["repositories"] = [
        {"remote": "github.com/acme/only-repo", "workspace": "only-repo",
         "language": "python", "ticket_prefix": "ONLY", "weight": 1},
    ]
    m, files = _gen(seed=1, sessions=4, profile=custom)
    for s in m["session_list"]:
        assert s["repo_remote"] == "github.com/acme/only-repo"
    assert all("only-repo" in rel for rel in files)


# ------------------------------------------------------------------------------- timing shape

def test_active_run_and_break_shape_matches_contract_bounds():
    m, _f = _gen(seed=12, sessions=10)
    saw_a_run = saw_a_break = False
    for s in m["session_list"]:
        for run in s["runs"]:
            dur_min = (run["end_ts"] - run["start_ts"]) / 60.0
            assert 0 < dur_min <= 6 * 20 + 1e-6
            assert 1 <= run["n_blocks"] <= 6
            saw_a_run = True
        for br in s["breaks"]:
            gap_min = (br["end_ts"] - br["start_ts"]) / 60.0
            assert gap_min >= 15.0, f"break shorter than 15 minutes: {gap_min}"
            assert gap_min <= 65.0, f"break wildly over the 60-minute contract bound: {gap_min}"
            saw_a_break = True
    assert saw_a_run and saw_a_break


def test_breaks_never_dangle_after_the_last_run():
    """Regression test: a break belongs strictly BETWEEN two runs. `build_session_runs` used to
    sometimes commit a trailing break with no run after it (when the session's target duration
    was reached right after adding the break rather than a run), which inflated the intended
    break count past anything the sidecar's cutter could ever show — there is no block on the
    far side of a dangling break to form a gap against. Swept across many seeds because the bug
    depended on the exact remaining budget at loop exit, which only some seeds hit."""
    for seed in range(40):
        m, _f = _gen(seed=seed, sessions=15)
        for s in m["session_list"]:
            assert len(s["breaks"]) == max(0, len(s["runs"]) - 1), (
                f"seed={seed} session={s['session_id']}: {len(s['runs'])} runs but "
                f"{len(s['breaks'])} breaks")


def test_session_duration_within_contract_bounds():
    m, _f = _gen(seed=13, sessions=15)
    for s in m["session_list"]:
        if not s["runs"]:
            continue
        total = s["runs"][-1]["end_ts"] - s["runs"][0]["start_ts"]
        assert total <= 4.6 * 3600, f"session far exceeds 4.5h bound: {total/3600:.2f}h"


def test_cli_smoke_produces_expected_layout():
    with tempfile.TemporaryDirectory() as root:
        out_dir, _ws, result = _run_cli(root, "--sessions", "3", "--days", "2", "--seed", "99")
        assert result.returncode == 0, result.stderr
        assert os.path.exists(os.path.join(out_dir, "manifest.json"))
        manifest = json.load(open(os.path.join(out_dir, "manifest.json")))
        assert len(manifest["session_list"]) == 3
        for s in manifest["session_list"]:
            assert os.path.exists(os.path.join(out_dir, s["rel_path"]))


# -------------------------------------------------------------------------------- __main__

if __name__ == "__main__":
    tests = [(name, fn) for name, fn in sorted(globals().items())
            if name.startswith("test_") and callable(fn)]
    failed = 0
    for name, fn in tests:
        try:
            fn()
        except Exception as exc:  # noqa: BLE001 - a test runner reports, never re-raises silently
            failed += 1
            print(f"FAIL {name}: {type(exc).__name__}: {exc}")
        else:
            print(f"ok   {name}")
    print(f"\n{len(tests) - failed}/{len(tests)} passed")
    raise SystemExit(1 if failed else 0)
