#!/usr/bin/env python3
"""Tests for the three-signal harness (`scripts/tool_signal.py`).

Standalone script with a `__main__` runner, never pytest — the repo convention (AGENTS.md).

Every load-bearing behaviour gets a test that BITES, and `--mutations` proves it: each mutation
breaks exactly one mechanism and the runner asserts the matching test then FAILS. A test that
still passes under its own mutation is reported as not biting, which is the only way to know the
suite is worth anything.

    python3 scripts/test_tool_signal.py
    python3 scripts/test_tool_signal.py --mutations
    PYTHONPATH=sidecar python3 scripts/test_tool_signal.py    # also runs the turns_in parity test
"""
import argparse, contextlib, json, os, sys, tempfile

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "sidecar"))
import tool_signal as T


def _res(is_error=False, content="x", uid="u1"):
    return {"type": "tool_result", "tool_use_id": uid, "content": content,
            **({"is_error": True} if is_error else {})}


def _line(**kw):
    return json.dumps(kw, separators=(",", ":"))


# ------------------------------------------------------------------ 1. the privacy boundary

def test_the_projection_returns_only_numbers_and_booleans():
    """The constraint that matters. `tool_result` content is file contents and command output —
    the projection must have nowhere for a string to sit, not merely a warning that it shouldn't.
    Asserted field-by-field, and separately asserted that no field's VALUE equals any substring of
    the payload, so a future field that stores a digest of the text still cannot store the text."""
    payload = "AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI\n/etc/passwd contents\n"
    uses = {"u1": (T.tool_index("Read"), 111, 222)}
    r = T.project_result(_res(content=payload), 1000.0, uses)
    T.assert_numeric(r, "result")
    for f, v in zip(r._fields, r):
        assert isinstance(v, (int, float, bool)), f"{f} is {type(v).__name__}"
        assert not isinstance(v, str), f"{f} leaked a string"
    blob = json.dumps(list(r))
    for secret in ("wJalrXUtnFEMI", "passwd", "SECRET"):
        assert secret not in blob, f"the projection carried {secret!r} out"
    assert r.n_bytes == len(payload.encode()), r.n_bytes


def test_an_unknown_tool_name_cannot_escape_the_closed_vocabulary():
    """An MCP or skill tool name is arbitrary text from the environment. It must collapse to one
    OTHER bucket, not be carried as a string."""
    assert T.tool_index("Read") == T.TOOLS.index("Read")
    assert T.tool_index("mcp__customer_db__query_pii") == T.OTHER_TOOL
    assert T.tool_index("") == T.OTHER_TOOL
    assert isinstance(T.tool_index("anything"), int)


def test_a_string_reaching_a_window_record_fails_the_run():
    """`assert_numeric` is called on every window on every run, not only here — a leak is a
    privacy incident, so it must stop the run rather than wait for a reviewer."""
    T.assert_numeric({"n_results": 1, "has_error": True, "wid": "a#t0-x", "file": "/p",
                      "prefix": "a", "start": "2026-01-01T00:00:00+00:00"}, "window")
    try:
        T.assert_numeric({"n_results": 1, "sample_output": "root:x:0:0"}, "window")
    except AssertionError:
        return
    raise AssertionError("a string field passed assert_numeric — the boundary is not enforced")


def test_byte_length_is_measured_for_both_content_shapes():
    """4999 of 5211 sampled tool_results carry a bare string, 212 a list of blocks. Both must be
    measured or output volume is undercounted on exactly the multi-part results."""
    uses = {}
    a = T.project_result(_res(content="abc\ndef"), 1.0, uses)
    assert (a.n_bytes, a.n_lines, a.n_parts) == (7, 2, 1), a
    b = T.project_result(_res(content=[{"type": "text", "text": "ab"},
                                       {"type": "text", "text": "cde"}]), 1.0, uses)
    assert (b.n_bytes, b.n_parts) == (5, 2), b
    c = T.project_result(_res(content=None), 1.0, uses)
    assert (c.n_bytes, c.n_parts) == (0, 0), c
    d = T.project_result(_res(content="é"), 1.0, uses)
    assert d.n_bytes == 2, "byte length must be utf-8 bytes, not characters"


def test_a_missing_is_error_key_is_not_an_error():
    """2247 of 5211 sampled blocks omit `is_error` entirely; 2851 carry False. Treating absence as
    anything but False would multiply the error rate by ~20."""
    uses = {}
    assert T.project_result(_res(), 1.0, uses).is_error is False
    assert T.project_result({"type": "tool_result", "tool_use_id": "u", "content": "x",
                             "is_error": False}, 1.0, uses).is_error is False
    assert T.project_result(_res(is_error=True), 1.0, uses).is_error is True


# ------------------------------------------------------------------ 2. target identity

def test_bash_targets_the_program_so_a_retried_command_is_the_same_resource():
    """"The same command failed again" has to survive the retry adding a flag — otherwise every
    retry is a new resource and no run is ever longer than 1."""
    a, ax = T._target("Bash", {"command": "go test ./internal/agent/..."})
    b, bx = T._target("Bash", {"command": "go test -run TestX ./internal/agent/..."})
    assert a == b == "go", (a, b)
    assert ax != bx, "the exact key must still distinguish the two command lines"
    assert T._target("Bash", {"command": "FOO=1 /usr/bin/env python3 x.py"})[0] == "env"
    assert T._target("Bash", {"command": "sudo make install"})[0] == "make"


def test_a_file_tool_targets_its_path_and_an_unrecognised_input_falls_back_to_the_tool():
    assert T._target("Edit", {"file_path": "/a/b.go", "old_string": "x"})[0] == "/a/b.go"
    assert T._target("Grep", {"pattern": "TODO"})[0] == "TODO"
    assert T._target("Weird", {"unknown": 3})[0] == "Weird"
    assert T._target("Weird", None)[0] == "Weird"


def test_an_orphan_result_cannot_join_a_run_with_anything():
    """A truncated transcript loses the assistant line, so some results have no known tool_use.
    They must still count toward volume and error rate, but keying them together would invent a
    run out of unrelated failures."""
    a = T.project_result(_res(is_error=True, uid="orphan-1"), 1.0, {})
    b = T.project_result(_res(is_error=True, uid="orphan-2"), 2.0, {})
    assert a.tool == T.OTHER_TOOL and a.res_key != b.res_key
    assert T.error_runs([a, b])["max_run"] == 1, "two orphans were fused into a run of 2"
    assert T.error_runs([a, b])["max_run_global"] == 2, "the global reading should see 2 in a row"


# ------------------------------------------------------------------ 3. error runs

def _r(t, err, key):
    return T.Result(t=float(t), is_error=err, n_bytes=1, n_lines=1, n_parts=1, tool=0,
                    res_key=key, exact_key=key)


def test_a_run_is_consecutive_failures_on_one_resource_and_survives_interleaving():
    """The agent retrying a failing edit still reads and greps around it, so a run must not be
    broken by another resource's success landing in between."""
    rs = [_r(1, True, 7), _r(2, False, 9), _r(3, True, 7), _r(4, True, 7)]
    got = T.error_runs(rs)
    assert got["max_run"] == 3, got
    assert got["max_run_global"] == 2, "the global reading is broken by the interleaved success"
    assert got["n_thrash"] == 1 and got["n_err_targets"] == 1, got


def test_a_success_on_the_same_resource_resets_its_run():
    rs = [_r(1, True, 7), _r(2, True, 7), _r(3, False, 7), _r(4, True, 7)]
    got = T.error_runs(rs)
    assert got["max_run"] == 2, got


def test_runs_are_computed_in_time_order_not_list_order():
    """Windows slice results out of file order and sidechain lines interleave, so the input list is
    not reliably sorted. A run read off list order would be fiction."""
    # time order T,F,T -> longest run 1. LIST order is T,T,F -> 2, so the two readings differ.
    rs = [_r(1, True, 7), _r(3, True, 7), _r(2, False, 7)]
    assert T.error_runs(rs)["max_run"] == 1, T.error_runs(rs)
    # and the mirror: list order says 1, time order says 2.
    rs2 = [_r(1, True, 7), _r(3, False, 7), _r(2, True, 7)]
    assert T.error_runs(rs2)["max_run"] == 2, T.error_runs(rs2)


def test_a_clean_window_reports_zeroes_rather_than_absent_keys():
    got = T.error_runs([_r(1, False, 7)])
    assert got == {"max_run": 0, "max_run_global": 0, "n_thrash": 0, "n_err_targets": 0,
                   "n_targets": 1}, got
    assert T.error_runs([])["n_targets"] == 0


# ------------------------------------------------------------------ 4. window statistics

def test_percentiles_interpolate_and_a_single_sample_is_its_own_percentile():
    assert T.pct([1, 2, 3, 4], 0.5) == 2.5
    assert T.pct([5], 0.9) == 5.0
    assert T.pct([], 0.5) == 0.0
    assert T.pct([0, 10], 0.9) == 9.0


def test_gaps_are_differences_between_time_sorted_turns():
    """n turns give n-1 gaps, and out-of-order lines must not produce a negative gap."""
    rec = T.signals([], [100.0, 130.0, 101.0, 102.0])
    assert rec["n_gaps"] == 3, rec
    # sorted 100,101,102,130 -> gaps 1,1,28. Read off LIST order the first gap would be +30.
    assert rec["gap_median"] == 1.0 and rec["gap_max"] == 28.0, rec
    assert rec["turn_span_s"] == 30.0, rec
    assert min(T.signals([], [100.0, 130.0, 101.0])[k] for k in ("gap_median", "gap_max")) > 0, \
        "an out-of-order line produced a negative gap"
    assert T.signals([], [100.0])["n_gaps"] == 0


def test_the_signal_record_is_all_numbers_and_carries_every_measured_axis():
    rec = T.signals([_r(1, True, 7), _r(2, False, 9)], [1.0, 2.0, 40.0])
    T.assert_numeric(rec, "signals")
    for k in ("n_results", "n_errors", "error_rate", "max_err_run", "n_thrash", "gap_median",
              "gap_p90", "slow_share", "fast_share", "out_bytes", "bytes_per_result"):
        assert k in rec, k
    assert rec["error_rate"] == 0.5 and rec["slow_share"] == 0.5 and rec["fast_share"] == 0.5


# ------------------------------------------------------------------ 5. the frame

def test_the_turn_filter_matches_the_real_turns_in():
    """The frame is only the published frame if this filter is `transcript.turns_in`'s filter. So
    assert it against the real one rather than trusting a copy. Skipped (loudly) when
    `app.analysis` is not importable, because the sibling package is under concurrent change."""
    try:
        from app.analysis.transcript import turns_in
    except Exception as e:
        print(f"    SKIP parity vs turns_in: {type(e).__name__} ({e})")
        return
    lines = [
        _line(type="user", timestamp="2026-01-01T00:00:00Z", message={"content": "hi"}),
        _line(type="assistant", timestamp="2026-01-01T00:00:01Z",
              message={"content": [{"type": "tool_use", "id": "u1", "name": "Read",
                                    "input": {"file_path": "/x"}}]}),
        _line(type="user", timestamp="2026-01-01T00:00:02Z",
              message={"content": [_res(content="BIG")]}),                    # skipped today
        _line(type="user", timestamp="2026-01-01T00:00:03Z",
              message={"content": [_res(content="BIG"),
                                   {"type": "tool_use", "id": "u2", "name": "Bash",
                                    "input": {"command": "ls"}}]}),           # kept: has tool_use
        _line(type="summary", timestamp="2026-01-01T00:00:04Z"),
        _line(type="user", message={"content": "no timestamp"}),
        "{not json but has \"type\":\"user\"}\n",
    ]
    want = [o["timestamp"] for o in turns_in(lines)]
    got = [o["timestamp"] for line in lines if T.is_turn_line(line)
           for o in [_safe(line)] if o and o.get("timestamp")]
    assert got == want, f"filter drifted: {got} != {want}"
    assert len(want) == 3, want


def _safe(line):
    try:
        return json.loads(line)
    except Exception:
        return None


def test_read_transcript_finds_results_on_lines_the_turn_filter_skips():
    """The whole point: a `tool_result`-only line contributes NO turn and one result. If it
    contributed a turn the grid would move and the 1,022 assertion would fail; if it contributed
    no result there would be no signal to measure."""
    with tempfile.TemporaryDirectory() as d:
        p = os.path.join(d, "abcdef01-x.jsonl")
        open(p, "w").write("\n".join([
            _line(type="assistant", timestamp="2026-01-01T00:00:00Z",
                  message={"content": [{"type": "tool_use", "id": "u1", "name": "Bash",
                                        "input": {"command": "go build ./..."}}]}),
            _line(type="user", timestamp="2026-01-01T00:00:05Z",
                  message={"content": [_res(is_error=True, content="err\nout", uid="u1")]}),
            _line(type="user", timestamp="2026-01-01T00:10:00Z", message={"content": "fix it"}),
        ]) + "\n")
        turns, results = T.read_transcript(p)
        assert len(turns) == 2, [o["type"] for o in turns]
        assert len(results) == 1 and results[0].is_error and results[0].n_bytes == 7, results
        assert results[0].tool == T.TOOLS.index("Bash"), results[0].tool
        w = T.windows_of(p, 3, turns, results)
        assert len(w) == 1, w
        assert w[0]["wid"] == "abcdef01#t0003-20260101T0000", w[0]["wid"]
        assert w[0]["n_turns"] == 2 and w[0]["n_results"] == 1 and w[0]["n_errors"] == 1, w[0]
        assert w[0]["out_bytes"] == 7 and w[0]["n_gaps"] == 1 and w[0]["gap_median"] == 600.0


def test_a_result_outside_the_window_bounds_is_not_counted():
    """Results are sliced on their OWN timestamps. A result that landed after the window closed
    belongs to the next window, and double-counting it would inflate output volume by the
    span/stride overlap."""
    with tempfile.TemporaryDirectory() as d:
        p = os.path.join(d, "aaaaaaaa-y.jsonl")
        open(p, "w").write("\n".join([
            _line(type="assistant", timestamp="2026-01-01T00:00:00Z",
                  message={"content": [{"type": "tool_use", "id": "u1", "name": "Read",
                                        "input": {"file_path": "/a"}}]}),
            _line(type="user", timestamp="2026-01-01T00:05:00Z",
                  message={"content": [_res(content="a" * 10, uid="u1")]}),
            # 02:00 is outside window 0 [00:00,01:00) and inside windows 1 and 2.
            _line(type="user", timestamp="2026-01-01T02:00:00Z",
                  message={"content": [_res(content="b" * 100, uid="u1")]}),
            _line(type="user", timestamp="2026-01-01T02:30:00Z", message={"content": "go on"}),
        ]) + "\n")
        turns, results = T.read_transcript(p)
        w = T.windows_of(p, 0, turns, results)
        assert len(results) == 2, results
        first = [x for x in w if x["start"].endswith("00:00:00+00:00")][0]
        assert first["out_bytes"] == 10, f"a later result leaked into window 0: {first}"
        assert sum(x["n_results"] for x in w) >= 2


def test_the_frame_count_assertion_bites_on_a_merged_frame():
    """A frame keyed on the 8-char prefix comes out at 550 against a true 1,022 and raises nothing
    on its own — so the count is asserted, and the assertion has to actually fire."""
    recs = [{"wid": "a#t0-x", "file": "/a", "prefix": "a", "start": "s"}]
    try:
        T.assert_frame(recs, {"/a": 1}, ["/a"])
    except AssertionError as e:
        assert "1022" in str(e) or "not testing anything" in str(e), e
        return
    raise AssertionError("assert_frame accepted a 1-window frame as if it were 1,022")


# ------------------------------------------------------------------ 6. statistics

def test_correlation_and_eta2_agree_with_hand_computable_cases():
    assert abs(T.pearson([1, 2, 3], [2, 4, 6]) - 1.0) < 1e-12
    assert abs(T.pearson([1, 2, 3], [6, 4, 2]) + 1.0) < 1e-12
    assert T.pearson([1, 1, 1], [1, 2, 3]) == 0.0
    assert abs(T.spearman([1, 2, 3], [10, 100, 1000]) - 1.0) < 1e-12, "rank r must be 1 here"
    assert abs(T.eta2([[1, 1], [3, 3]]) - 1.0) < 1e-12, "fully separated groups are eta2 1"
    assert T.eta2([[1, 3], [1, 3]]) < 1e-12, "identical groups are eta2 0"


def test_the_verdict_columns_follow_the_pre_registered_thresholds():
    """The refutation has to be mechanical: a signal at or above R_SIZE_MAX is SIZE whatever its
    distribution looks like."""
    recs = [{"volume": i + 1, "n_actions": i + 1, "x": i, "y": (i * 7919) % 13,
             "file": f"/f{i // 4}"} for i in range(200)]
    lv = [__import__("math").log1p(r["volume"]) for r in recs]
    la = [__import__("math").log1p(r["n_actions"]) for r in recs]
    d = T.describe("x", recs, lambda r: r["x"], lv, la)
    assert d["size_confounded"] is True and abs(d["r_log_volume"]) >= T.R_SIZE_MAX, d
    d2 = T.describe("y", recs, lambda r: r["y"], lv, la)
    assert d2["size_confounded"] is False, d2
    # `x` rises monotonically with the file id, so transcript identity explains nearly all of it;
    # `y` cycles inside every file, so it explains almost none. eta2_file has to see that.
    assert d["eta2_file"] > 0.9 and d2["eta2_file"] < 0.2, (d["eta2_file"], d2["eta2_file"])
    assert d["n_multi_window_files"] == 50, d["n_multi_window_files"]


# ------------------------------------------------------------------ mutations

@contextlib.contextmanager
def _patch(name, value):
    old = getattr(T, name)
    setattr(T, name, value)
    try:
        yield
    finally:
        setattr(T, name, old)


def _leaky_project(block, t, uses):
    r = T.project_result(block, t, uses)
    return type("Leaky", (), {"_fields": r._fields + ("content",),
                              "__iter__": lambda s: iter(tuple(r) + (str(block.get("content")),))
                              })()


MUTATIONS = [
    # (name, what it breaks, how, the test that must then fail)
    ("is_error absence becomes an error",
     lambda: _patch("project_result", lambda b, t, u: T.Result(
         float(t), b.get("is_error") is not False, 1, 1, 1, 0, 0, 0)),
     "test_a_missing_is_error_key_is_not_an_error"),
    ("byte length counts characters, not utf-8 bytes",
     lambda: _patch("_text_bytes", lambda c: (len(c) if isinstance(c, str) else 0, 0, 0)),
     "test_byte_length_is_measured_for_both_content_shapes"),
    ("a run is read off list order, not time order",
     lambda: _patch("error_runs", lambda rs, key="res_key": T.error_runs(
         list(rs), key) if False else _list_order_runs(rs, key)),
     "test_runs_are_computed_in_time_order_not_list_order"),
    ("a run breaks on any interleaved success",
     lambda: _patch("error_runs", lambda rs, key="res_key": _global_only_runs(rs)),
     "test_a_run_is_consecutive_failures_on_one_resource_and_survives_interleaving"),
    ("bash targets the whole command line",
     lambda: _patch("_program", lambda cmd: cmd),
     "test_bash_targets_the_program_so_a_retried_command_is_the_same_resource"),
    ("an unknown tool name is kept as a string",
     lambda: _patch("tool_index", lambda n: n),
     "test_an_unknown_tool_name_cannot_escape_the_closed_vocabulary"),
    ("assert_numeric waves strings through",
     lambda: _patch("assert_numeric", lambda o, where="x": None),
     "test_a_string_reaching_a_window_record_fails_the_run"),
    ("the window keeps results from outside its bounds",
     lambda: _patch("windows_of", _unbounded_windows),
     "test_a_result_outside_the_window_bounds_is_not_counted"),
    ("the frame count is merely printed, never asserted",
     lambda: _patch("assert_frame", lambda recs, per_file, files: None),
     "test_the_frame_count_assertion_bites_on_a_merged_frame"),
    ("the size threshold is read the wrong way round",
     lambda: _patch("R_SIZE_MAX", 1.5),
     "test_the_verdict_columns_follow_the_pre_registered_thresholds"),
]


def _list_order_runs(rs, key="res_key"):
    per, best, cur = {}, 0, 0
    for r in rs:
        k = getattr(r, key)
        st = per.setdefault(k, [0, 0])
        if r.is_error:
            st[0] += 1; st[1] = max(st[1], st[0]); cur += 1; best = max(best, cur)
        else:
            st[0] = 0; cur = 0
    runs = [s[1] for s in per.values() if s[1]]
    return {"max_run": max(runs) if runs else 0, "max_run_global": best,
            "n_thrash": sum(1 for r in runs if r >= T.THRASH_RUN),
            "n_err_targets": len(runs), "n_targets": len(per)}


def _global_only_runs(rs):
    best = cur = 0
    for r in sorted(rs, key=lambda r: r.t):
        cur = cur + 1 if r.is_error else 0
        best = max(best, cur)
    return {"max_run": best, "max_run_global": best,
            "n_thrash": 1 if best >= T.THRASH_RUN else 0,
            "n_err_targets": 1 if best else 0,
            "n_targets": len({r.res_key for r in rs})}


def _unbounded_windows(path, fid, turns, results):
    import datetime as _dt
    t0 = T._epoch(turns[0]["timestamp"])
    tN = T._epoch(turns[-1]["timestamp"])
    start, out = t0, []
    while start < tN:
        end = start + _dt.timedelta(minutes=T.SPAN)
        sl = [o for o in turns if start <= T._epoch(o["timestamp"]) < end]
        here, start = start, start + _dt.timedelta(minutes=T.STRIDE)
        if not sl:
            continue
        rec = T.signals(results, [T._epoch(o["timestamp"]).timestamp() for o in sl])
        rec["n_turns"] = len(sl)
        rec.update({"wid": f"{os.path.basename(path)[:8]}#t{fid:04d}-{here:%Y%m%dT%H%M}",
                    "file": path, "prefix": os.path.basename(path)[:8], "fid": fid,
                    "start": here.isoformat()})
        out.append(rec)
    return out


def tests():
    return {k: v for k, v in sorted(globals().items())
            if k.startswith("test_") and callable(v)}


def run(names=None):
    fns = tests()
    failed = []
    for name, fn in fns.items():
        if names and name not in names:
            continue
        try:
            fn()
            print(f"PASS {name}")
        except Exception as e:
            failed.append(name)
            print(f"FAIL {name}: {type(e).__name__}: {e}")
    return failed


def mutate():
    """Every mutation must break its test. A mutation the suite survives is reported LOUDLY: it
    means that behaviour is untested, which is worse than a failing test because it looks fine."""
    bad = 0
    for label, patch, target in MUTATIONS:
        assert target in tests(), f"mutation names a test that does not exist: {target}"
        with patch():
            try:
                tests()[target]()
                print(f"NOT-BITING [{label}] -> {target} still passed")
                bad += 1
            except Exception as e:
                print(f"BITES      [{label}] -> {target}: {type(e).__name__}")
    print(f"\n{len(MUTATIONS) - bad}/{len(MUTATIONS)} mutations confirmed to bite")
    return bad


if __name__ == "__main__":
    ap = argparse.ArgumentParser()
    ap.add_argument("--mutations", action="store_true")
    a = ap.parse_args()
    failed = run()
    print(f"\n{len(tests()) - len(failed)}/{len(tests())} passed")
    bad = mutate() if a.mutations else 0
    sys.exit(1 if failed or bad else 0)
