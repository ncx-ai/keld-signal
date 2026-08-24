#!/usr/bin/env python3
"""Measure the three transcript signals the pipeline currently discards, and test each against
the one rule this series has found reliable: is it a size bucket wearing a label?

    PYTHONPATH=sidecar python3 scripts/tool_signal.py frame
    PYTHONPATH=sidecar python3 scripts/tool_signal.py stats
    PYTHONPATH=sidecar python3 scripts/tool_signal.py cost

## What is being measured, and why none of it exists today

`transcript.turns_in` skips a `tool_result`-only line by a substring check BEFORE `json.loads`.
Everything on that line is therefore invisible to every level, every facet and every window
statistic the store publishes: whether the tool failed, how much came back, and (because those
lines carry timestamps too) a large part of the transcript's own clock. Three candidate signals
live there:

    error / retry     the `is_error` flag, and — the quantity that matters — CONSECUTIVE failures
                      on the same file or command. A 2.2% error rate is not a facet. Thrashing is.
    turn latency      inter-turn gaps: rapid steered back-and-forth vs long autonomous stretches.
    output volume     bytes returned by tools — the plausible long-context routing axis.

## The bar each one has to clear, fixed here before any number exists

The routing-class study's lesson was that almost every candidate axis turned out to be a restated
event count: `context_volume` correlated **+0.914** with log window volume, `interactivity`
+0.497. The rule that caught them was correlation with log volume, and the threshold that held was
0.5. So, pre-registered:

    R_SIZE_MAX 0.50     |pearson r| against log1p(window volume) at or above this and the signal
                        IS size, reported as refuted regardless of how interesting its
                        distribution looks. Same threshold against log1p(n_actions), the published
                        action-level evidence count, because "separates something `evidence` does
                        not" is the second half of the question.
    RESID_MIN 0.50      the share of the signal's variance NOT explained by the volume decile
                        (1 - eta^2). Below this, windows of the same size have the same value and
                        the signal adds no partition.
    PREVALENCE_MIN 0.20 a signal present in under this share of windows is not a facet — the
                        brief's own rule ("present in 2% of windows and flat across the rest").
                        Applied only to the error signals, the two that can be absent.

A null is the expected outcome for at least one of the three, and is reported as a result.

## PRIVACY — the hard constraint, enforced by construction

`tool_result` content is file contents and command output: 21.6 MB of it, a larger and more
sensitive surface than prompt text. `project_result` is the ONLY function in this file that sees a
`tool_result` block, and it physically returns a `Result` namedtuple whose every field is an int,
a float or a bool. There is no field a string could occupy, so no payload can reach a window
record, a statistic or the report — the same shape as the Go client leaving `inventory`
unmodelled rather than merely warning about it. `assert_numeric` walks every field of every
emitted record and is called on every run, not just in the tests.

Two identities are needed to see thrashing (the same file failing three times running), and both
are 64-bit BLAKE2b digests of the target string, never the string:

    res_key    the resource — a file path, or for Bash the PROGRAM (argv[0]), which is what "the
               same command" means when a retry adds a flag.
    exact_key  the full target — the whole command line, so "identical retry" is distinguishable
               from "same program, different arguments".

Tool identity is an index into a CLOSED vocabulary declared here; anything unrecognised maps to
one OTHER bucket. A tool name from the transcript cannot escape either.

## The frame is the published one, asserted row-for-row

Windows are rebuilt here from the transcripts with the same grid the routing-class and
observable-facet studies used (span 60 / stride 50, cut inside a per-file loop) and then asserted
to match `observable-frame.ndjson` on (file, start) exactly — 1,022 windows. That join is how
`volume` and `n_actions` enter: the published numbers, not a re-derivation, so "correlated with
log volume" means the same thing here as in the study that set the threshold. The 8-char session
prefix is NOT the key: it collides for 445 of 500 files and keying on it silently merged the same
frame to 550 windows once already, raising no error.
"""
import argparse, collections, glob, hashlib, json, math, os, statistics, sys, time
from datetime import datetime, timedelta

SPAN, STRIDE = 60, 50                  # minutes; stride must not divide span (this series)
EXPECTED_WINDOWS = 1022
OUT = os.path.expanduser("~/keld/refseries-context/transcript-signal")
ROOTS = [os.path.expanduser("~/keld/refseries-context/frozen-corpus/projects"),
         os.path.expanduser("~/keld/refseries-context/frozen-corpus/john-projects")]
PUBLISHED_FRAME = os.path.expanduser(
    "~/keld/refseries-context/facets/observable-frame.ndjson")

R_SIZE_MAX = 0.50
RESID_MIN = 0.50
PREVALENCE_MIN = 0.20
SLOW_GAP_S = 30.0                      # "autonomous stretch" — a gap no human is inside
FAST_GAP_S = 5.0
THRASH_RUN = 2                         # >= 2 consecutive failures on one resource

# CLOSED tool vocabulary. A name outside it maps to OTHER_TOOL, so no transcript-supplied tool
# name is ever retained. Ordering is the identity — append only.
TOOLS = ("Bash", "Read", "Edit", "Write", "MultiEdit", "NotebookEdit", "Glob", "Grep", "LS",
         "Task", "Agent", "WebFetch", "WebSearch", "TodoWrite", "BashOutput", "KillShell",
         "ExitPlanMode", "SlashCommand", "Skill", "AskUserQuestion")
OTHER_TOOL = len(TOOLS)
TOOL_NAME = TOOLS + ("<other>",)

# Where a tool's TARGET lives, in priority order. The resource key uses the first hit; for Bash
# the command is reduced to its program first.
TARGET_KEYS = ("file_path", "notebook_path", "path", "command", "url", "pattern", "query",
               "prompt", "description")

# A `Result` is the whole privacy boundary: every field is a number or a bool, by construction.
Result = collections.namedtuple(
    "Result", "t is_error n_bytes n_lines n_parts tool res_key exact_key")


# --------------------------------------------------------------------- the privacy boundary

def _h64(s):
    """A string -> a 64-bit int. One-way: the only operation the study needs on a target is
    equality with the previous one."""
    return int.from_bytes(hashlib.blake2b(s.encode("utf-8", "replace"), digest_size=8).digest(),
                          "big")


def tool_index(name):
    """Closed vocabulary -> index. Anything else is one bucket, so an arbitrary MCP or skill tool
    name cannot be carried out of the projection."""
    try:
        return TOOLS.index(name)
    except ValueError:
        return OTHER_TOOL


def _program(cmd):
    """A shell command -> its program, for "the same command failed again" when the retry added a
    flag. Deliberately crude (no bashlex): leading env assignments and a path prefix are stripped,
    the first remaining word wins. Only ever hashed."""
    for tok in cmd.replace("\t", " ").split():
        if "=" in tok.split("/")[0] and not tok.startswith(("-", "/", ".")):
            continue                                   # FOO=bar prefix
        if tok in ("sudo", "command", "exec", "time", "env"):
            continue
        return os.path.basename(tok.strip("\"'`(){}"))
    return ""


def _target(tool_name, inp):
    """(resource string, exact string) for a tool_use input. Strings — but only inside the
    projection, which hashes them before returning."""
    if not isinstance(inp, dict):
        return tool_name, tool_name
    for k in TARGET_KEYS:
        v = inp.get(k)
        if isinstance(v, str) and v.strip():
            if k == "command":
                return (_program(v) or tool_name), v
            return v, v
    return tool_name, tool_name


def _text_bytes(content):
    """utf-8 byte length and line count of a tool_result's content, whatever shape it came in.
    Reads the strings; returns two ints."""
    n_bytes = n_lines = n_parts = 0
    if isinstance(content, str):
        blobs = [content]
    elif isinstance(content, list):
        blobs = []
        for b in content:
            if isinstance(b, dict):
                for k in ("text", "data"):
                    if isinstance(b.get(k), str):
                        blobs.append(b[k])
            elif isinstance(b, str):
                blobs.append(b)
    elif content is None:
        blobs = []
    else:
        blobs = [json.dumps(content)]
    for s in blobs:
        n_bytes += len(s.encode("utf-8", "replace"))
        n_lines += s.count("\n") + 1
        n_parts += 1
    return n_bytes, n_lines, n_parts


def project_result(block, t, uses):
    """THE PRIVACY BOUNDARY. A `tool_result` block -> a `Result` of numbers and booleans only.

    This is the only function in the study that reads tool output. It returns a namedtuple with no
    field a string can occupy: `is_error` is a bool, the four sizes are ints, `tool` is an index
    into a closed vocabulary, and the two keys are 64-bit digests. There is nowhere for file
    contents or command output to go, which is the point — a warning would be a promise, this is a
    shape. `assert_numeric` proves it on every run.

    `uses` maps a tool_use id -> (tool index, resource digest, exact digest), harvested from the
    assistant lines. A result whose tool_use is unknown (its assistant line was truncated out of
    the transcript, which happens) still yields a Result, keyed on the digest of its own id, so it
    counts toward volume and error rate but can never join a run with anything else.
    """
    n_bytes, n_lines, n_parts = _text_bytes(block.get("content"))
    uid = block.get("tool_use_id")
    known = uses.get(uid) if isinstance(uid, str) else None
    if known is None:
        h = _h64(uid) if isinstance(uid, str) else 0
        known = (OTHER_TOOL, h, h)
    return Result(t=float(t), is_error=bool(block.get("is_error")), n_bytes=int(n_bytes),
                  n_lines=int(n_lines), n_parts=int(n_parts), tool=int(known[0]),
                  res_key=int(known[1]), exact_key=int(known[2]))


def assert_numeric(obj, where="record"):
    """Prove nothing but numbers and booleans got out. Called on every projected Result and on
    every window record, not only from the tests: a projection that leaks is a privacy incident,
    not a wrong number, so it must fail the run rather than the review."""
    if isinstance(obj, Result):
        for f, v in zip(obj._fields, obj):
            assert isinstance(v, (int, float, bool)) and not isinstance(v, str), (
                f"{where}.{f} is {type(v).__name__} — the projection leaked a non-number")
        return
    for k, v in obj.items():
        if k in ("wid", "file", "prefix", "start"):        # frame identity, never tool content
            continue
        assert isinstance(v, (int, float, bool)) and not isinstance(v, str), (
            f"{where}[{k}] is {type(v).__name__} — a non-number reached the window record")


# --------------------------------------------------------------------- reading a transcript

def _epoch(ts):
    return datetime.fromisoformat(ts.replace("Z", "+00:00"))


def is_turn_line(line):
    """`transcript.turns_in`'s substring filter, replicated verbatim so this study's frame is the
    published frame. Two rules, both before `json.loads`:
      1. the line must announce itself as a user or assistant turn;
      2. a `tool_result` line is skipped UNLESS it also carries a `tool_use` block (a tool_use can
         be echoed back inside a result).
    `test_tool_signal.py` asserts this against the real `turns_in` rather than trusting the copy.
    """
    if '"type":"user"' not in line and '"type":"assistant"' not in line:
        return False
    if '"tool_result"' in line and '"tool_use"' not in line:
        return False
    return True


def read_transcript(path):
    """One pass over the file -> (turns, results). Both projections come off the same read.

    `turns` is exactly what `iter_turns` would have yielded, in file order, so the window grid is
    unchanged. `results` is the new material: every `tool_result` block on ANY line, projected to
    numbers. Tool_use blocks are harvested on the way past to give each result its target.
    """
    turns, results, uses = [], [], {}
    with open(path, errors="replace") as fh:
        for line in fh:
            has_res = '"tool_result"' in line
            has_use = '"tool_use"' in line
            if not has_res and not has_use and not is_turn_line(line):
                continue
            try:
                o = json.loads(line)
            except Exception:
                continue
            ts = o.get("timestamp")
            if is_turn_line(line) and ts:
                turns.append(o)
            content = (o.get("message") or {}).get("content")
            if not isinstance(content, list):
                continue
            for b in content:
                if not isinstance(b, dict):
                    continue
                if b.get("type") == "tool_use":
                    res, exact = _target(str(b.get("name") or ""), b.get("input"))
                    uid = b.get("id")
                    if isinstance(uid, str):
                        uses[uid] = (tool_index(str(b.get("name") or "")), _h64(res),
                                     _h64(exact))
                elif b.get("type") == "tool_result" and ts:
                    r = project_result(b, _epoch(ts).timestamp(), uses)
                    assert_numeric(r, "result")
                    results.append(r)
    return turns, results


# --------------------------------------------------------------------- the three signals

def pct(xs, q):
    """Percentile by linear interpolation on the sorted sample. Own implementation so the numbers
    in RESULTS.md do not depend on which numpy is installed."""
    if not xs:
        return 0.0
    s = sorted(xs)
    if len(s) == 1:
        return float(s[0])
    i = (len(s) - 1) * q
    lo = int(math.floor(i))
    hi = min(lo + 1, len(s) - 1)
    return float(s[lo] + (s[hi] - s[lo]) * (i - lo))


def error_runs(results, key="res_key"):
    """Longest run of CONSECUTIVE failures on one resource, and how many resources thrashed.

    The interesting quantity is not the error rate. It is the same file or command failing again
    immediately — which nothing in the system can currently see. "Consecutive" is read PER
    RESOURCE: a run survives other tools' results landing in between, because an agent retrying a
    failing edit still reads and greps around it. `max_global` is the stricter reading (consecutive
    in the window's whole result stream, any resource) and is reported alongside so the definition
    is visible rather than assumed.
    """
    per = collections.defaultdict(lambda: [0, 0])       # key -> [current run, best run]
    best_global = cur_global = 0
    for r in sorted(results, key=lambda r: r.t):
        k = getattr(r, key)
        st = per[k]
        if r.is_error:
            st[0] += 1
            st[1] = max(st[1], st[0])
            cur_global += 1
            best_global = max(best_global, cur_global)
        else:
            st[0] = 0
            cur_global = 0
    runs = [st[1] for st in per.values() if st[1]]
    return {
        "max_run": max(runs) if runs else 0,
        "max_run_global": best_global,
        "n_thrash": sum(1 for r in runs if r >= THRASH_RUN),
        "n_err_targets": len(runs),
        "n_targets": len(per),
    }


def signals(results, turn_times):
    """A window's three signal blocks. Numbers only, and every one of them derived from material
    the current pipeline never parses."""
    n = len(results)
    errs = [r for r in results if r.is_error]
    byts = [r.n_bytes for r in results]
    runs = error_runs(results)
    gaps = [b - a for a, b in zip(sorted(turn_times), sorted(turn_times)[1:])]
    rec = {
        # --- 1. error / retry
        "n_results": n,
        "n_errors": len(errs),
        "error_rate": round(len(errs) / n, 6) if n else 0.0,
        "max_err_run": runs["max_run"],
        "max_err_run_global": runs["max_run_global"],
        "n_thrash": runs["n_thrash"],
        "n_err_targets": runs["n_err_targets"],
        "n_targets": runs["n_targets"],
        "has_error": bool(errs),
        "has_thrash": runs["n_thrash"] > 0,
        # --- 2. turn latency
        "n_gaps": len(gaps),
        "gap_median": round(pct(gaps, 0.5), 3),
        "gap_p90": round(pct(gaps, 0.9), 3),
        "gap_max": round(max(gaps), 3) if gaps else 0.0,
        "slow_share": round(sum(1 for g in gaps if g >= SLOW_GAP_S) / len(gaps), 6)
                      if gaps else 0.0,
        "fast_share": round(sum(1 for g in gaps if g < FAST_GAP_S) / len(gaps), 6)
                      if gaps else 0.0,
        "turn_span_s": round(max(turn_times) - min(turn_times), 3) if turn_times else 0.0,
        # --- 3. output volume
        "out_bytes": sum(byts),
        "out_bytes_median": round(pct(byts, 0.5), 1),
        "out_bytes_p90": round(pct(byts, 0.9), 1),
        "out_bytes_max": max(byts) if byts else 0,
        "out_lines": sum(r.n_lines for r in results),
        "bytes_per_result": round(sum(byts) / n, 1) if n else 0.0,
    }
    return rec


# --------------------------------------------------------------------- the frame

def windows_of(path, fid, turns, results):
    """One transcript's fixed-grid windows — the same grid, cut the same way, as
    `observable_facets.windows_of`: `t0`/`tN` are the FIRST and LAST turn in file order, the walk
    is `while start < tN`, and an empty slice is skipped. Cut inside the per-file loop so no
    cross-file merge is representable. Results are sliced by their OWN timestamps into the same
    bounds; a window can therefore hold results while holding few turns, which is exactly the
    autonomous-stretch case the latency signal is about.
    """
    t0, tN = _epoch(turns[0]["timestamp"]), _epoch(turns[-1]["timestamp"])
    prefix = os.path.basename(path)[:8]
    start, out = t0, []
    while start < tN:
        end = start + timedelta(minutes=SPAN)
        sl = [o for o in turns if start <= _epoch(o["timestamp"]) < end]
        here, start = start, start + timedelta(minutes=STRIDE)
        if not sl:
            continue
        a, b = here.timestamp(), (here + timedelta(minutes=SPAN)).timestamp()
        rs = [r for r in results if a <= r.t < b]
        rec = signals(rs, [_epoch(o["timestamp"]).timestamp() for o in sl])
        rec["n_turns"] = len(sl)
        rec.update({"wid": f"{prefix}#t{fid:04d}-{here:%Y%m%dT%H%M}", "file": path,
                    "prefix": prefix, "fid": fid, "start": here.isoformat()})
        assert_numeric(rec, "window")
        out.append(rec)
    return out


def join_published(recs):
    """Attach the PUBLISHED `volume` and `n_actions` to each window, joining on (file, start).

    Not re-derived: `volume` is the quantity the routing-class study correlated its candidate axes
    against, and the 0.50 threshold is only meaningful against the same number. The join is
    asserted total in both directions — an unjoined window would silently become a hole in every
    correlation below.
    """
    pub = {}
    with open(PUBLISHED_FRAME) as fh:
        for line in fh:
            o = json.loads(line)
            pub[(o["file"], o["start"])] = o
    assert len(pub) == EXPECTED_WINDOWS, f"published frame is {len(pub)} rows"
    miss = [r["wid"] for r in recs if (r["file"], r["start"]) not in pub]
    assert not miss, (f"{len(miss)} rebuilt windows are absent from the published frame "
                      f"(first: {miss[:3]}) — the grid drifted, so `volume` cannot be joined")
    got = {(r["file"], r["start"]) for r in recs}
    extra = [k for k in pub if k not in got]
    assert not extra, f"{len(extra)} published windows were not rebuilt (first: {extra[:3]})"
    for r in recs:
        o = pub[(r["file"], r["start"])]
        r["volume"] = int(o["volume"])
        r["n_actions"] = int(o["n_actions"])
        r["n_prompts"] = int(o["n_prompts"])
        r["top_action"] = (max(o["actions"].items(), key=lambda kv: kv[1])[0]
                           if o["actions"] else "")


def assert_frame(recs, per_file, files):
    """The frame assertions, carried over verbatim in intent from the observable study: the
    session-collision merge looked perfectly plausible when it happened (550 windows against a
    true 1,022, no error raised), so the count is an ASSERTION and the trap is checked live."""
    n = len(recs)
    assert n == sum(per_file.values()), f"frame rows {n} != per-file recount {sum(per_file.values())}"
    assert len({r["wid"] for r in recs}) == n, "colliding wids — the frame merged windows"
    assert len({(r["file"], r["start"]) for r in recs}) == n, "duplicate (file, start)"
    by_prefix = len({(r["prefix"], r["start"]) for r in recs})
    colliding = sum(f for _p, f in collections.Counter(
        os.path.basename(f)[:8] for f in files).items() if f > 1)
    assert by_prefix < n, ("the prefix-keyed count did NOT come out lower, so this assertion is "
                           "not testing anything on this corpus")
    assert n == EXPECTED_WINDOWS, (
        f"frame is {n} windows, expected {EXPECTED_WINDOWS}. {by_prefix} is what the known "
        f"session-collision bug produces here and it raises no error on its own.")
    print(f"ASSERTED windows={n} files={len(per_file)} distinct_wids={n}")
    print(f"  session-collision check: prefix-keyed would give {by_prefix} "
          f"({n - by_prefix} lost, {round(100 * (n - by_prefix) / n, 1)}%); "
          f"{colliding} of {len(files)} files sit in a colliding prefix group")


def build(roots, out):
    files = sorted(f for r in roots
                   for f in glob.glob(os.path.join(r, "**", "*.jsonl"), recursive=True))
    if not files:
        sys.exit("no transcripts under " + ", ".join(roots))
    recs, per_file, corpus = [], {}, {"files": len(files), "results": 0, "errors": 0,
                                      "bytes": 0, "turns": 0, "gaps": [], "res_bytes": [],
                                      "empty": 0}
    t_start = time.time()
    for fid, path in enumerate(files):
        turns, results = read_transcript(path)
        corpus["results"] += len(results)
        corpus["errors"] += sum(1 for r in results if r.is_error)
        corpus["bytes"] += sum(r.n_bytes for r in results)
        corpus["res_bytes"] += [r.n_bytes for r in results]
        corpus["turns"] += len(turns)
        if not turns:
            corpus["empty"] += 1
            continue
        ts = sorted(_epoch(o["timestamp"]).timestamp() for o in turns)
        corpus["gaps"] += [b - a for a, b in zip(ts, ts[1:])]
        w = windows_of(path, fid, turns, results)
        per_file[path] = len(w)
        recs.extend(w)
    print(f"parsed {len(files)} files in {time.time() - t_start:.1f}s "
          f"(empty={corpus['empty']})")
    print(f"CORPUS tool_result blocks={corpus['results']} "
          f"is_error={corpus['errors']} ({100 * corpus['errors'] / corpus['results']:.2f}%) "
          f"payload={corpus['bytes'] / 2**20:.1f} MB "
          f"median={pct(corpus['res_bytes'], 0.5):.0f} B "
          f"p90={pct(corpus['res_bytes'], 0.9):.0f} B")
    print(f"CORPUS turns={corpus['turns']} inter-turn gaps={len(corpus['gaps'])} "
          f"median={pct(corpus['gaps'], 0.5):.1f}s p90={pct(corpus['gaps'], 0.9):.1f}s "
          f"p99={pct(corpus['gaps'], 0.99):.1f}s")
    tmp = out + ".unverified"
    with open(tmp, "w") as fh:
        for r in recs:
            fh.write(json.dumps(r) + "\n")
    assert_frame(recs, per_file, files)
    join_published(recs)
    with open(tmp, "w") as fh:
        for r in recs:
            fh.write(json.dumps(r) + "\n")
    os.replace(tmp, out)
    print(f"frame -> {out}")
    with open(os.path.join(os.path.dirname(out), "corpus-totals.json"), "w") as fh:
        json.dump({k: v for k, v in corpus.items() if k not in ("gaps", "res_bytes")} |
                  {"gap_median_s": pct(corpus["gaps"], 0.5),
                   "gap_p90_s": pct(corpus["gaps"], 0.9),
                   "gap_p99_s": pct(corpus["gaps"], 0.99),
                   "n_gaps": len(corpus["gaps"]),
                   "result_bytes_median": pct(corpus["res_bytes"], 0.5),
                   "result_bytes_p90": pct(corpus["res_bytes"], 0.9)}, fh, indent=2)


# --------------------------------------------------------------------- statistics

def pearson(xs, ys):
    n = len(xs)
    if n < 2:
        return 0.0
    mx, my = sum(xs) / n, sum(ys) / n
    sx = math.sqrt(sum((x - mx) ** 2 for x in xs))
    sy = math.sqrt(sum((y - my) ** 2 for y in ys))
    if sx == 0 or sy == 0:
        return 0.0
    return sum((x - mx) * (y - my) for x, y in zip(xs, ys)) / (sx * sy)


def _ranks(xs):
    order = sorted(range(len(xs)), key=lambda i: xs[i])
    out, i = [0.0] * len(xs), 0
    while i < len(order):
        j = i
        while j + 1 < len(order) and xs[order[j + 1]] == xs[order[i]]:
            j += 1
        r = (i + j) / 2 + 1
        for k in range(i, j + 1):
            out[order[k]] = r
        i = j + 1
    return out


def spearman(xs, ys):
    return pearson(_ranks(xs), _ranks(ys))


def eta2(groups):
    """Between-group share of variance. Here the groups are volume DECILES, so eta^2 answers "how
    much of this signal is just window size" in a way a linear r cannot: a monotone but curved
    dependence on volume shows up here even when r is modest."""
    allv = [v for g in groups for v in g]
    if len(allv) < 2:
        return 0.0
    gm = sum(allv) / len(allv)
    sst = sum((v - gm) ** 2 for v in allv)
    if sst == 0:
        return 0.0
    ssb = sum(len(g) * (sum(g) / len(g) - gm) ** 2 for g in groups if g)
    return ssb / sst


def deciles(recs, by):
    xs = sorted(recs, key=lambda r: r[by])
    n = max(1, len(xs) // 10)
    return [xs[i:i + n] for i in range(0, len(xs), n)][:10]


def describe(name, recs, get, log_vol, log_act):
    xs = [get(r) for r in recs]
    nz = [x for x in xs if x]
    dec = deciles(recs, "volume")
    e2 = eta2([[get(r) for r in d] for d in dec])
    r_vol = pearson(xs, log_vol)
    r_act = pearson(xs, log_act)
    # Independent of size is necessary but NOT sufficient: white noise is also independent of
    # size. `eta2_file` is the between-TRANSCRIPT share of variance over the 271 files holding
    # more than one window — how much of the signal is a property of the session rather than of
    # the individual hour. A signal at chance here separates nothing, it just jitters.
    by_file = collections.defaultdict(list)
    for r in recs:
        by_file[r["file"]].append(get(r))
    multi = [g for g in by_file.values() if len(g) > 1]
    e2f = eta2(multi)
    return {
        "signal": name,
        "n": len(xs), "nonzero": len(nz), "prevalence": round(len(nz) / len(xs), 4),
        "mean": round(statistics.fmean(xs), 4), "sd": round(statistics.pstdev(xs), 4),
        "p10": round(pct(xs, 0.10), 3), "median": round(pct(xs, 0.5), 3),
        "p90": round(pct(xs, 0.90), 3), "p99": round(pct(xs, 0.99), 3),
        "max": round(max(xs), 3),
        "r_log_volume": round(r_vol, 4), "rho_log_volume": round(spearman(xs, log_vol), 4),
        "r_log_n_actions": round(r_act, 4),
        "eta2_volume_decile": round(e2, 4), "resid_share": round(1 - e2, 4),
        "eta2_file": round(e2f, 4), "n_multi_window_files": len(multi),
        "size_confounded": abs(r_vol) >= R_SIZE_MAX or abs(r_act) >= R_SIZE_MAX,
        "flat_within_size": (1 - e2) < RESID_MIN,
    }


def profile(r):
    return (f"vol={r['volume']} acts={r['n_actions']} top={r['top_action'] or '-'} "
            f"turns={r['n_turns']} prompts={r['n_prompts']} results={r['n_results']} "
            f"err={r['n_errors']}/run{r['max_err_run']} "
            f"gapmed={r['gap_median']}s p90={r['gap_p90']}s slow={r['slow_share']:.2f} "
            f"out={r['out_bytes'] / 1024:.0f}KB")


def extremes(recs, get, name, k=5):
    """Named windows at both ends, with their profile. Required, and for a reason: about twenty
    defects in this study surfaced as plausible wrong aggregates and essentially none was caught
    by reading a mean."""
    s = sorted(recs, key=lambda r: (get(r), r["wid"]))
    lines = [f"  {name}: HIGH"]
    for r in reversed(s[-k:]):
        lines.append(f"    {get(r):>10} {r['wid']}  {profile(r)}")
    lines.append(f"  {name}: LOW (first {k} by wid among the lowest)")
    for r in s[:k]:
        lines.append(f"    {get(r):>10} {r['wid']}  {profile(r)}")
    return lines


SIGNALS = [
    ("error/n_errors", lambda r: r["n_errors"]),
    ("error/error_rate", lambda r: r["error_rate"]),
    ("error/max_err_run", lambda r: r["max_err_run"]),
    ("error/n_thrash", lambda r: r["n_thrash"]),
    ("latency/gap_median", lambda r: r["gap_median"]),
    ("latency/gap_p90", lambda r: r["gap_p90"]),
    ("latency/slow_share", lambda r: r["slow_share"]),
    ("latency/fast_share", lambda r: r["fast_share"]),
    ("output/out_bytes", lambda r: r["out_bytes"]),
    ("output/log_out_bytes", lambda r: math.log1p(r["out_bytes"])),
    ("output/bytes_per_result", lambda r: r["bytes_per_result"]),
    ("output/out_bytes_median", lambda r: r["out_bytes_median"]),
]


def stats(frame, out):
    recs = [json.loads(l) for l in open(frame)]
    assert len(recs) == EXPECTED_WINDOWS, f"{len(recs)} windows, expected {EXPECTED_WINDOWS}"
    log_vol = [math.log1p(r["volume"]) for r in recs]
    log_act = [math.log1p(r["n_actions"]) for r in recs]
    rows = [describe(n, recs, g, log_vol, log_act) for n, g in SIGNALS]
    print(f"{'signal':26} {'preval':>7} {'median':>9} {'p90':>10} {'max':>11} "
          f"{'r_logvol':>9} {'rho':>7} {'r_acts':>7} {'eta2':>6} {'resid':>6} {'eta2f':>6}"
          f"  verdict")
    for d in rows:
        v = ("SIZE" if d["size_confounded"] else
             "FLAT-IN-SIZE" if d["flat_within_size"] else "independent")
        print(f"{d['signal']:26} {d['prevalence']:>7.3f} {d['median']:>9.3f} {d['p90']:>10.3f} "
              f"{d['max']:>11.1f} {d['r_log_volume']:>9.3f} {d['rho_log_volume']:>7.3f} "
              f"{d['r_log_n_actions']:>7.3f} {d['eta2_volume_decile']:>6.3f} "
              f"{d['resid_share']:>6.3f} {d['eta2_file']:>6.3f}  {v}")

    # --- the error signal's own question: how often, and does density separate windows
    n = len(recs)
    err = [r for r in recs if r["n_errors"]]
    thr = [r for r in recs if r["n_thrash"]]
    runs = collections.Counter(r["max_err_run"] for r in recs)
    print(f"\nERROR PREVALENCE  windows with >=1 error: {len(err)} ({100*len(err)/n:.1f}%); "
          f"with a run>=2 on one resource: {len(thr)} ({100*len(thr)/n:.1f}%); "
          f"run>=3: {sum(1 for r in recs if r['max_err_run'] >= 3)}")
    print("  max_err_run distribution: " +
          ", ".join(f"{k}:{v}" for k, v in sorted(runs.items())))
    print(f"  error_rate among windows that have any error: "
          f"median={pct([r['error_rate'] for r in err], .5):.3f} "
          f"p90={pct([r['error_rate'] for r in err], .9):.3f} "
          f"max={max(r['error_rate'] for r in err):.3f}")
    # Does an error window look different at all, or is it just a bigger window?
    for field in ("volume", "n_results", "n_turns", "out_bytes"):
        a = pct([r[field] for r in err], .5)
        b = pct([r[field] for r in recs if not r["n_errors"]], .5)
        print(f"  median {field}: error-windows={a:.0f} clean-windows={b:.0f} "
              f"ratio={a / b if b else float('nan'):.2f}")

    # --- latency: is it size again, and are there two regimes?
    #
    # ELIGIBILITY, and it is load-bearing. A window with no gap at all reports gap_median 0 and
    # fast_share 0.0 — the same value a genuinely SLOW window reports, so the two are indis-
    # tinguishable in an aggregate. Found by reading the named extremes: 12c80ab4#t0002-
    # 20260721T1632 holds ONE turn, and it was sitting at the bottom of the fast_share ranking
    # next to 16c69396#t0303-20260820T1352, whose gaps really do run to 257s. A ratio over fewer
    # than MIN_EVIDENCE (5) observations is not a ratio (window.MIN_EVIDENCE's own derivation), so
    # the latency numbers are restated over windows carrying at least 5 gaps, excluded-and-counted
    # rather than silently kept.
    MIN_GAPS = 5
    thin = [r for r in recs if r["n_gaps"] < MIN_GAPS]
    elig = [r for r in recs if r["n_gaps"] >= MIN_GAPS]
    print(f"\nLATENCY ELIGIBILITY  windows with 0 gaps: "
          f"{sum(1 for r in recs if not r['n_gaps'])}; with <{MIN_GAPS}: {len(thin)}; "
          f"eligible: {len(elig)}")
    elv = [math.log1p(r["volume"]) for r in elig]
    ela = [math.log1p(r["n_actions"]) for r in elig]
    for name, get in SIGNALS:
        if not name.startswith("latency/"):
            continue
        d = describe(name, elig, get, elv, ela)
        print(f"  eligible-only {name:22} median={d['median']:8.3f} p90={d['p90']:9.3f} "
              f"r_logvol={d['r_log_volume']:+.3f} eta2={d['eta2_volume_decile']:.3f} "
              f"resid={d['resid_share']:.3f} -> "
              f"{'SIZE' if d['size_confounded'] else 'independent'}")
        rows.append({**d, "signal": name + " [n_gaps>=5]"})

    gm = [r["gap_median"] for r in recs]
    print(f"\nLATENCY  gap_median: p10={pct(gm,.1):.1f}s median={pct(gm,.5):.1f}s "
          f"p90={pct(gm,.9):.1f}s p99={pct(gm,.99):.1f}s max={max(gm):.0f}s")
    print(f"  windows whose gap_median >= {SLOW_GAP_S}s: "
          f"{sum(1 for r in recs if r['gap_median'] >= SLOW_GAP_S)}; "
          f"< {FAST_GAP_S}s: {sum(1 for r in recs if r['gap_median'] < FAST_GAP_S)}")
    print(f"  slow_share vs fast_share correlation: "
          f"{pearson([r['slow_share'] for r in recs], [r['fast_share'] for r in recs]):.3f}")
    print(f"  gap_median vs n_prompts r={pearson(gm, [r['n_prompts'] for r in recs]):.3f}; "
          f"vs n_turns r={pearson(gm, [r['n_turns'] for r in recs]):.3f}")

    # --- output volume: the axis with the worst prior
    ob = [r["out_bytes"] for r in recs]
    print(f"\nOUTPUT  out_bytes: median={pct(ob,.5)/1024:.1f}KB p90={pct(ob,.9)/1024:.1f}KB "
          f"p99={pct(ob,.99)/1024:.1f}KB max={max(ob)/1024:.0f}KB total={sum(ob)/2**20:.1f}MB")
    print(f"  bytes_per_result vs log volume r="
          f"{pearson([r['bytes_per_result'] for r in recs], log_vol):.3f} "
          f"(the size-free reading: mean payload PER tool call)")
    print(f"  log out_bytes vs log n_results r="
          f"{pearson([math.log1p(r['out_bytes']) for r in recs], [math.log1p(r['n_results']) for r in recs]):.3f}")

    # eta2_file has to be read against a chance baseline: with 271 multi-window files the
    # between-group share is not 0 even for pure noise. This is that baseline, computed by
    # permuting the signal across windows with the file sizes held fixed.
    import random
    rnd = random.Random(0)
    for name, get in SIGNALS:
        xs = [get(r) for r in recs]
        sizes = [c for c in collections.Counter(r["file"] for r in recs).values() if c > 1]
        chance = []
        for _ in range(20):
            sh = xs[:]
            rnd.shuffle(sh)
            i, groups = 0, []
            for c in sizes:
                groups.append(sh[i:i + c]); i += c
            chance.append(eta2(groups))
        d = next(x for x in rows if x["signal"] == name)
        d["eta2_file_chance"] = round(statistics.fmean(chance), 4)
        d["session_structured"] = d["eta2_file"] > 2 * d["eta2_file_chance"]
    print("\nSESSION STRUCTURE  eta2 of transcript identity vs a permuted chance baseline "
          "(a signal at chance separates nothing)")
    for d in [x for x in rows if "eta2_file_chance" in x]:
        print(f"  {d['signal']:26} eta2_file={d['eta2_file']:.3f} "
              f"chance={d['eta2_file_chance']:.3f} "
              f"{'STRUCTURED' if d['session_structured'] else 'AT CHANCE'}")

    # --- the cross-checks RESULTS.md cites. Here rather than in a throwaway script so every
    # number in the write-up is reproduced by one command.
    thr = [r for r in recs if r["n_thrash"]]
    tot = sum(r["n_errors"] for r in recs)
    print(f"\nCROSS-CHECKS")
    print(f"  thrash windows={len(thr)} over {len({r['file'] for r in thr})} distinct files; "
          f"they hold {sum(r['n_errors'] for r in thr)} of {tot} window-summed errors "
          f"({100 * sum(r['n_errors'] for r in thr) / tot:.1f}%)")
    hi = [r for r in recs if r["error_rate"] >= 0.10]
    cl = [r for r in recs if r["n_results"] >= 5 and not r["n_errors"]]
    def _mix(rs):
        c = collections.Counter(r["top_action"] for r in rs)
        return ", ".join(f"{k or '-'}:{v}" for k, v in c.most_common(4))
    print(f"  error_rate>=0.10: {len(hi)} windows, top_action {_mix(hi)}")
    print(f"  clean with >=5 results: {len(cl)} windows, top_action {_mix(cl)}")
    sub = [r for r in recs if r["n_results"] >= 5]
    print(f"  on the {len(sub)} windows with >=5 results: "
          f"bytes_per_result vs log volume r="
          f"{pearson([r['bytes_per_result'] for r in sub], [math.log1p(r['volume']) for r in sub]):+.3f}; "
          f"vs fast_share r="
          f"{pearson([r['bytes_per_result'] for r in sub], [r['fast_share'] for r in sub]):+.3f} "
          f"(the two survivors overlap); error_rate vs fast_share r="
          f"{pearson([r['error_rate'] for r in sub], [r['fast_share'] for r in sub]):+.3f}")
    # Content-duplicate transcripts. The published frame does not dedup by hash (routing_class
    # did, which is why it reports 1,019); a duplicate pair shows up as two identical windows and
    # would otherwise read as an independent replication.
    seen, dups, dupwin = {}, 0, 0
    for f in sorted({r["file"] for r in recs}):
        with open(f, "rb") as fh:
            h = hashlib.sha256(fh.read()).hexdigest()
        if h in seen:
            dups += 1
            dupwin += sum(1 for r in recs if r["file"] == f)
        seen[h] = f
    print(f"  content-duplicate transcripts: {dups} redundant files, {dupwin} redundant windows "
          f"of {len(recs)}")

    lines = []
    for name, get in SIGNALS:
        lines += extremes(recs, get, name)
    print("\nNAMED WINDOWS\n" + "\n".join(lines))
    with open(out, "w") as fh:
        json.dump({"thresholds": {"R_SIZE_MAX": R_SIZE_MAX, "RESID_MIN": RESID_MIN,
                                  "PREVALENCE_MIN": PREVALENCE_MIN},
                   "n_windows": n, "signals": rows,
                   "error_prevalence": {"any": len(err), "thrash2": len(thr),
                                        "runs": {str(k): v for k, v in sorted(runs.items())}},
                   "named": lines}, fh, indent=2)
    print(f"\n-> {out}")


# --------------------------------------------------------------------- parse cost

def cost(roots, repeats):
    """Verify the claimed cost of parsing what is currently skipped. Three arms over the same
    files, so the difference isolates one thing each:

        read   iterate the lines, no json at all  — the bytes we already read
        turns  today's filter (tool_result skipped unparsed)
        full   turns + the tool_result projection

    The claim under test is +36% / ~0.5 ms per MB, and specifically that the cost is reading bytes
    rather than `json.loads`. `read` is what decides that: if `full - turns` is close to
    `turns - read`, the extra work is parsing; if `read` already dominates, it is I/O.
    """
    files = sorted(f for r in roots
                   for f in glob.glob(os.path.join(r, "**", "*.jsonl"), recursive=True))
    mb = sum(os.path.getsize(f) for f in files) / 2**20
    print(f"{len(files)} files, {mb:.1f} MB, {repeats} repeats")

    def arm_read():
        n = 0
        for p in files:
            with open(p, errors="replace") as fh:
                for line in fh:
                    n += len(line)
        return n

    def arm_turns():
        n = 0
        for p in files:
            with open(p, errors="replace") as fh:
                for line in fh:
                    if not is_turn_line(line):
                        continue
                    try:
                        o = json.loads(line)
                    except Exception:
                        continue
                    if o.get("timestamp"):
                        n += 1
        return n

    def arm_min():
        """turns + the MINIMAL new work: parse the currently-skipped lines and take the error flag
        and the byte length. No target identity, so no hashing and no tool_use harvest — this is
        the cheap two-thirds of the signal (error rate, output volume) without the thrash key."""
        n = 0
        for p in files:
            with open(p, errors="replace") as fh:
                for line in fh:
                    if '"tool_result"' not in line and not is_turn_line(line):
                        continue
                    try:
                        o = json.loads(line)
                    except Exception:
                        continue
                    c = (o.get("message") or {}).get("content")
                    if isinstance(c, list):
                        for b in c:
                            if isinstance(b, dict) and b.get("type") == "tool_result":
                                n += _text_bytes(b.get("content"))[0] + int(
                                    bool(b.get("is_error")))
        return n

    def arm_full():
        n = 0
        for p in files:
            t, r = read_transcript(p)
            n += len(t) + len(r)
        return n

    # How much of the corpus sits on lines today's filter never parses. This is the quantity the
    # "+36% is bytes we already read" claim turns on: bytes are read either way, but json.loads
    # only runs on the kept lines today.
    kept = skipped = 0
    for p in files:
        with open(p, errors="replace") as fh:
            for line in fh:
                if is_turn_line(line):
                    kept += len(line)
                else:
                    skipped += len(line)
    print(f"  bytes on lines parsed today: {kept / 2**20:.1f} MB; "
          f"on lines skipped unparsed: {skipped / 2**20:.1f} MB "
          f"({100 * skipped / (kept + skipped):.1f}% of the corpus)")

    res = {}
    for name, fn in (("read", arm_read), ("turns", arm_turns), ("min", arm_min),
                     ("full", arm_full)):
        ts = []
        for _ in range(repeats):
            t0 = time.perf_counter()
            got = fn()
            ts.append(time.perf_counter() - t0)
        res[name] = min(ts)
        print(f"  {name:6} best={min(ts):7.3f}s median={statistics.median(ts):7.3f}s "
              f"({min(ts) * 1000 / mb:6.2f} ms/MB)  n={got}")
    d = res["full"] - res["turns"]
    dm = res["min"] - res["turns"]
    print(f"\nfull vs turns: +{d:.3f}s = +{100 * d / res['turns']:.1f}%  "
          f"({1000 * d / mb:.2f} ms per MB of transcript)")
    print(f"min  vs turns: +{dm:.3f}s = +{100 * dm / res['turns']:.1f}%  "
          f"({1000 * dm / mb:.2f} ms/MB) — error flag + byte length only, no thrash key")
    print(f"  of `turns`, {100 * res['read'] / res['turns']:.1f}% is the bare line read "
          f"({1000 * res['read'] / mb:.2f} ms/MB) — json.loads on today's kept lines costs "
          f"{1000 * (res['turns'] - res['read']) / mb:.2f} ms/MB, the new lines cost "
          f"{1000 * d / mb:.2f} ms/MB")
    with open(os.path.join(OUT, "parse-cost.json"), "w") as fh:
        json.dump({"files": len(files), "mb": round(mb, 2), "repeats": repeats,
                   "best_s": {k: round(v, 4) for k, v in res.items()},
                   "delta_pct": round(100 * d / res["turns"], 2),
                   "delta_pct_min": round(100 * dm / res["turns"], 2),
                   "ms_per_mb": {k: round(1000 * v / mb, 3) for k, v in res.items()},
                   "delta_ms_per_mb": round(1000 * d / mb, 3),
                   "delta_ms_per_mb_min": round(1000 * dm / mb, 3),
                   "mb_parsed_today": round(kept / 2**20, 1),
                   "mb_skipped_today": round(skipped / 2**20, 1)}, fh, indent=2)


def main():
    ap = argparse.ArgumentParser()
    sub = ap.add_subparsers(dest="cmd", required=True)
    b = sub.add_parser("frame"); b.add_argument("--roots", nargs="+", default=ROOTS)
    b.add_argument("-o", default=os.path.join(OUT, "signal-frame.ndjson"))
    s = sub.add_parser("stats")
    s.add_argument("--frame", default=os.path.join(OUT, "signal-frame.ndjson"))
    s.add_argument("-o", default=os.path.join(OUT, "signal-stats.json"))
    c = sub.add_parser("cost"); c.add_argument("--roots", nargs="+", default=ROOTS)
    c.add_argument("--repeats", type=int, default=3)
    a = ap.parse_args()
    os.makedirs(OUT, exist_ok=True)
    if a.cmd == "frame":
        build(a.roots, a.o)
    elif a.cmd == "stats":
        stats(a.frame, a.o)
    else:
        cost(a.roots, a.repeats)


if __name__ == "__main__":
    main()
