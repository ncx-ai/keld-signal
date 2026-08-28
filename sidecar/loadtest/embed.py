"""EMBEDDER tier (~5-8 min, OPT-IN): the text encoder child under SUSTAINED load.

    python -m loadtest embed

⚠️ **This arm exists because every other resource-safety claim in this repo is backed by this
harness and the encoder's were not.** `app/analysis/textembed.py`'s numbers — ~1,711 MB resident,
~1,738 MB peak, 766 ms/message at bf16 — came from ad-hoc single-shot scripts run by hand on a host
that was simultaneously running a live sidecar. A one-shot script cannot see a leak, cannot see an
in-flight peak, cannot see whether idle-unload releases anything, and cannot see whether the encoder
starves the endpoints beside it. Those are the four things this file measures, and the fifth is the
per-message cost over many messages rather than one batch.

It is NOT part of `smoke`. It loads a real 1.2 GB model into a ~1.7-2.4 GB child and takes minutes;
`python -m loadtest smoke` must be unaffected in both cost and behaviour, so this is its own
subcommand and nothing here is imported by that path.

## WHAT IT ESTABLISHES

  E1  no leak         second-half vs first-half mean child RSS over a sustained encode, the same
                      `steady()`/`mean_growth()` method and the same KELD_LOADTEST_LEAK_GROWTH_MB
                      idiom smoke's S1 uses.
  E2  peak bounded    the child's high-water RSS against KELD_LOADTEST_EMBED_PEAK_RSS_MB, AND that
                      /metrics' own `peak_rss_mb` actually SAW it — see the note below.
  E3  idle-unload     the child really dies, the memory really comes back, and the next request
                      really respawns it.
  E4  no starvation   /analyze, /blocks and /features' structured half keep answering DURING an
                      encode pass, at a latency compared against their own idle latency. This is
                      the load-bearing one: a regression here is what a user would feel.
  E5  cost            ms/message and messages/s over many messages, so the 766 ms figure is either
                      confirmed or corrected.

## ⚠️ WHAT THIS ARM FOUND ON ITS FIRST RUN, AND WHY E2 HAS TWO HALVES

`embed.peak_rss_mb` was reporting **1717 MB while the live `rss_mb` beside it read 2072 MB**.
`Encoder.observe_rss` is lock-free by design, but its only caller ran `maybe_unload()` immediately
after it under a BLOCKING acquire of the lock `encode` holds for a whole batch — so the 1 s poll
loop reached the lock-free sample once per BATCH, right after `_release_memory()`: the trough. That
is the RSS-oscillation incident's exact shape one child over (`worker_manager.poll` takes its own
lock non-blocking for precisely this reason). Fixed in `textembed.maybe_unload`; pinned by
`app/test_guard_visibility.py`'s encoder block. E2 therefore asserts both that the peak is bounded
and that the REPORTED peak is not below what a live sample saw — a bound nobody can observe is not
a bound.

## THE MEASUREMENT IS ISOLATED FROM THE MACHINE'S OWN SIDECAR

`KELD_HOME` is a temp dir, so `~/.keld/state/refseries.db` — which a running `keld-agent` holds
open — is never touched, and a `KELD_CAPTURE`/`KELD_TERMS` difference cannot force a reparse of the
real store. Only the weights are read from the real `~/.keld/models`, read-only. Nothing is
downloaded: absent weights are a **SKIP**, never a fetch and never a failure.

`KELD_TERMS=0`. The `term` level is computed at INGEST and precomputed into bins, so it is not part
of the `/analyze` latency E4 measures; leaving it on would only add spaCy's 619 MB to the parent and
its noise to the RSS series.

## THE BASELINE FOR E4 IS "CHILD UP, NOT ENCODING" — NOT "CHILD DOWN"

The variable under test is the ENCODE, not the child's existence. So the encoder is warmed first and
every baseline sample is taken with `encoding == False` (waited for), which isolates one pass in
flight as the only difference between the two arms. A baseline taken with the child down would also
be measuring 1.7 GB of absent resident memory and would flatter the comparison.
"""
import glob
import json
import os
import time

import httpx
import psutil

from loadtest.analysis import mean_growth, steady
from loadtest.harness import SidecarProcess
from loadtest.sampler import Sampler

# The encoder child's peak-RSS cap. Its OWN variable, deliberately not smoke's
# KELD_LOADTEST_PEAK_RSS_MB (6144, the inference worker's parent-process cap): this is a different
# child with a different budget, and one number covering both would have to be the looser of the
# two. 3072 MB is the measured in-flight peak plus ~27% headroom. ⚠️ The peak is NOT a stable
# number — five sustained runs on the same host measured 2072 / 2345 / 2414 / 2432 / 2389 MB: it depends
# on which messages a pass happens to draw and how contended the box is. A cap set just above one
# reading (2560 was the first attempt, 6% over) is a flaky arm, not a tight one. 3072 still trips a
# dtype regression: float32 measured 3126 MB peak on the SAME inputs (textembed._load).
PEAK_RSS_CAP_MB = float(os.environ.get("KELD_LOADTEST_EMBED_PEAK_RSS_MB", "3072"))
# Reused verbatim from smoke's S1 rather than given a sibling: it is the same question (does the
# second half of a steady window sit above the first) against a child whose own in-flight
# oscillation is ~355 MB, which the half-means largely cancel.
LEAK_GROWTH_MB = float(os.environ.get("KELD_LOADTEST_LEAK_GROWTH_MB", "300"))
# How long the sustained encode runs. Long enough for several complete passes — a leak signal over
# fewer than ~4 passes is one batch's oscillation wearing a trend's shape.
SECONDS = float(os.environ.get("KELD_LOADTEST_EMBED_SECONDS", "180"))
# How much slower a structured endpoint may be with a pass in flight before E4 calls it starvation.
# Generous on purpose: these are 0.1 s calls, so a fixed millisecond budget would measure scheduler
# jitter, and the failure this guards against is an order-of-magnitude one (a request queued behind
# the encode lock, or the event loop blocked), not a 20% one.
LATENCY_FACTOR = float(os.environ.get("KELD_LOADTEST_EMBED_LATENCY_FACTOR", "4.0"))
# Idle timeout for the E3 sidecar. Short so the arm does not wait out the 300 s production default;
# the POLICY is identical, only the constant differs.
IDLE_S = float(os.environ.get("KELD_LOADTEST_EMBED_IDLE_S", "10"))
# ⚠️ The transcript is read WHOLE by featuretext on every /features call, so a 90 MB one makes that
# read — not the encode — the thing being measured. The largest transcript under this cap is picked.
MAX_TRANSCRIPT_MB = float(os.environ.get("KELD_LOADTEST_EMBED_MAX_MB", "16"))
# Messages per background pass. Smaller than the production 64 so a pass completes inside the
# sampling window several times over; the arithmetic per message is identical either way.
MAX_ENCODE = int(os.environ.get("KELD_LOADTEST_EMBED_MAX_ENCODE", "8"))

_BASELINE_ROUNDS = int(os.environ.get("KELD_LOADTEST_EMBED_BASELINE_ROUNDS", "3"))


def _report(name, ok, detail):
    print(f"{'PASS' if ok else 'FAIL'} {name}: {detail}")
    return 0 if ok else 1


def _skip(name, detail):
    print(f"SKIP {name}: {detail}")


# ---- pure helpers (unit-tested in loadtest/test_embed.py, no model, no sidecar) ----------------

def pick_transcript(candidates, max_bytes):
    """The largest transcript at or under `max_bytes`, or `None`.

    Largest-under-a-cap rather than largest: see MAX_TRANSCRIPT_MB. Deterministic — ties break on
    the path, so two runs on one machine measure the same file and their numbers are comparable.
    `candidates` is `(path, size)` pairs so the caller owns the stat and this stays testable
    without a filesystem."""
    fits = [(size, path) for path, size in candidates if size <= max_bytes]
    if not fits:
        return None
    return max(fits, key=lambda sp: (sp[0], sp[1]))[1]


def dig(d, *keys):
    """`d[k1][k2]...`, or None if any level is missing.

    A metrics sample taken while the service is still starting has no `embed` block at all, and a
    KeyError in the sampler's consumer would lose the whole series to a transient."""
    cur = d
    for k in keys:
        if not isinstance(cur, dict):
            return None
        cur = cur.get(k)
    return cur


def metric_series(rows, *keys):
    """The non-None values of one nested /metrics field across a sample series."""
    out = []
    for r in rows:
        v = dig(r.metrics, *keys)
        if v is not None:
            out.append(v)
    return out


def pctl(values, q):
    """The `q`-quantile (0..1) by nearest rank. Nearest-rank rather than interpolated because
    these samples are latencies of whole HTTP calls, and an interpolated p90 of nine samples is a
    number no request actually took."""
    if not values:
        return 0.0
    s = sorted(values)
    i = min(len(s) - 1, max(0, int(round(q * (len(s) - 1)))))
    return s[i]


def per_message_ms(encoded, elapsed_s):
    """ms per message. `None` for a window that encoded nothing — an unmeasured cost is not 0.0,
    which is this repo's standing rule one measurement down."""
    if not encoded:
        return None
    return (elapsed_s / encoded) * 1000.0


# ---- sidecar-facing helpers ---------------------------------------------------------------------

def _weights_dir():
    """The real encoder weights, or None. Read through textembed's own resolver so this arm cannot
    disagree with the code it is testing about where they live. Nothing downloads."""
    from app.analysis import textembed as te
    return te.weights_dir()


def _transcripts():
    home = os.path.expanduser("~")
    pats = [os.path.join(home, ".claude", "projects", "*", "*.jsonl")]
    out = []
    for p in pats:
        for f in glob.glob(p):
            try:
                out.append((f, os.path.getsize(f)))
            except OSError:
                pass
    return out


def _user_prompt_ids(path, limit_lines=400000):
    """User-turn uuids in file order, for /analyze's `prompt_id`.

    ⚠️ **A `tool_result` line is USER-SHAPED and is NOT in the store's prompt index.**
    `transcript.turns_in` skips those lines unparsed — that skip is what keeps a parse
    seconds-long — so nothing indexes them, and /analyze answers 404 for one. Measured on the
    fixture this arm picks: the LAST user-shaped line of the transcript is a `tool_result`, and
    an arm that took it got 404 on every /analyze sample. So only turns carrying real text
    qualify.

    Read by scanning for the shape rather than by importing the parser: this is a load-test
    fixture, and reaching into `transcript.turns_in` would couple the arm to the code it is
    measuring."""
    out = []
    with open(path, "r", errors="replace") as fh:
        for n, line in enumerate(fh):
            if n > limit_lines:
                break
            if '"type":"user"' not in line and '"type": "user"' not in line:
                continue
            try:
                o = json.loads(line)
            except Exception:
                continue
            if o.get("type") != "user" or not o.get("uuid"):
                continue
            c = (o.get("message") or {}).get("content")
            texty = isinstance(c, str) or (
                isinstance(c, list) and any(isinstance(b, dict) and b.get("type") == "text"
                                            for b in c))
            if texty:
                out.append(str(o["uuid"]))
    return out


def _resolve_prompt_id(client, base_url, path, ids, tries=8):
    """The newest prompt id /analyze can actually answer for, or None.

    Probed rather than assumed: a prompt is 404 if the store never indexed its line and 410 if its
    evidence has been pruned, and neither is visible from the file. Newest first, because a recent
    prompt's 60-minute look-back is the part of the session most likely to hold evidence."""
    for pid in list(reversed(ids))[:tries]:
        try:
            r = client.post(base_url + "/analyze",
                            json={"path": path, "prompt_id": pid, "span_minutes": 60},
                            timeout=60.0)
            if r.status_code == 200:
                return pid
        except Exception:
            continue
    return None


def _embed(client, base_url):
    try:
        return client.get(base_url + "/metrics", timeout=10.0).json().get("embed") or {}
    except Exception:
        return {}


def _wait(client, base_url, pred, timeout, poll=0.5):
    """Poll /metrics' embed block until `pred` or the deadline. Returns the last block seen."""
    deadline = time.monotonic() + timeout
    m = {}
    while time.monotonic() < deadline:
        m = _embed(client, base_url)
        if pred(m):
            return m
        time.sleep(poll)
    return m


def _timed(client, url, body):
    t0 = time.monotonic()
    try:
        r = client.post(url, json=body, timeout=60.0)
        status = r.status_code
    except Exception:
        status = 0
    return status, time.monotonic() - t0


def _tree_rss_mb(pid):
    """RSS of the sidecar process and every descendant, in MB. This is what "the memory really
    came back" means: the encoder child's own reading goes to 0.0 the instant the handle is
    dropped, which would be true of a leaked process too."""
    try:
        p = psutil.Process(pid)
        total = p.memory_info().rss
        for c in p.children(recursive=True):
            try:
                total += c.memory_info().rss
            except psutil.Error:
                pass
        return total / (1024.0 * 1024.0)
    except psutil.Error:
        return 0.0


def _env(home, weights, **extra):
    env = {
        # Isolated store: the machine's own keld-agent holds ~/.keld/state/refseries.db open, and a
        # KELD_CAPTURE/KELD_TERMS difference would force it to reparse. Never touch it.
        "KELD_HOME": home,
        "KELD_TEXTEMBED": "1",
        # Explicit, because KELD_HOME above moved the default lookup away from the real weights.
        "KELD_TEXTEMBED_DIR": weights,
        "KELD_TERMS": "0",
        "KELD_SIDECAR_MAX_THREADS": "2",
        "KELD_TEXTEMBED_MAX_ENCODE": str(MAX_ENCODE),
    }
    env.update(extra)
    return env


# ---- the arm ------------------------------------------------------------------------------------

def run(seconds=None, quick=False):
    """Returns the number of failed checks. 0 with a SKIP when there are no weights."""
    seconds = SECONDS if seconds is None else float(seconds)
    if quick:
        seconds = min(seconds, 45.0)

    weights = _weights_dir()
    if not weights:
        _skip("embed", "no encoder weights at ~/.keld/models/qwen3-embedding-0.6b "
                       "(this arm never downloads) — nothing to measure")
        return 0
    path = os.environ.get("KELD_LOADTEST_TRANSCRIPT") or \
        pick_transcript(_transcripts(), MAX_TRANSCRIPT_MB * 1024 * 1024)
    if not path:
        _skip("embed", f"no Claude Code transcript under {MAX_TRANSCRIPT_MB:.0f} MB in "
                       "~/.claude/projects (set KELD_LOADTEST_TRANSCRIPT)")
        return 0
    prompt_ids = _user_prompt_ids(path)
    print(f"transcript: {path} ({os.path.getsize(path) / 1e6:.1f} MB, "
          f"{len(prompt_ids)} user prompts)")
    print(f"weights:    {weights}")

    import tempfile
    fails = 0
    with tempfile.TemporaryDirectory(prefix="keld-loadtest-embed-") as home:
        fails += _sustained(home, weights, path, prompt_ids, seconds)
        fails += _idle_unload(home, weights, path)
    print(f"\nembed: {'ALL PASS' if fails == 0 else str(fails) + ' FAILED'}")
    return fails


def _sustained(home, weights, path, prompt_ids, seconds):
    """E1/E2/E4/E5: one sidecar, idle-unload OFF so nothing unloads mid-measurement."""
    fails = 0
    sc = SidecarProcess(env=_env(home + "/a", weights, KELD_TEXTEMBED_IDLE_UNLOAD_S="0"))
    os.makedirs(home + "/a", exist_ok=True)
    sc.start()
    try:
        c = httpx.Client(timeout=600.0)
        t0 = time.monotonic()
        r = c.post(sc.base_url + "/ingest", json={"path": path})
        print(f"ingest:     {r.status_code} in {time.monotonic() - t0:.2f}s {r.text[:80]}")

        feat = {"path": path, "max_rows": 16}
        blocks = {"path": path}
        prompt_id = _resolve_prompt_id(c, sc.base_url, path, prompt_ids)
        analyze = {"path": path, "prompt_id": prompt_id or "", "span_minutes": 60}
        if prompt_id is None:
            # A stated SKIP, not a silent pass and not a FAIL: /analyze having no answerable
            # prompt in this transcript is a property of the fixture, not of the encoder.
            _skip("E4 analyze-under-load", "no prompt id this store can answer for")

        # --- warm: the first /features call spawns the child and starts a pass. Timed, because
        # the child's first model load is a real cost and featuretext.py quotes ~90 s for it.
        t0 = time.monotonic()
        c.post(sc.base_url + "/features", json=feat)
        m = _wait(c, sc.base_url, lambda m: m.get("state") == "ready", 300.0, poll=0.25)
        load_s = time.monotonic() - t0
        if m.get("state") != "ready":
            fails += _report("E0 encoder-ready", False,
                             f"state={m.get('state')} status={m.get('status')} after {load_s:.0f}s")
            return fails
        fails += _report("E0 encoder-ready", True,
                         f"child up in {load_s:.1f}s, rss={m.get('rss_mb')} MB")

        # --- E4 baseline: child UP, no pass in flight (see the module docstring). ---
        base = {"analyze": [], "blocks": [], "features": []}
        for _ in range(_BASELINE_ROUNDS):
            _wait(c, sc.base_url, lambda m: not m.get("encoding"), 300.0)
            if prompt_id:
                base["analyze"].append(_timed(c, sc.base_url + "/analyze", analyze))
            base["blocks"].append(_timed(c, sc.base_url + "/blocks", blocks))
            base["features"].append(_timed(c, sc.base_url + "/features", feat))

        # --- the sustained encode: keep a pass in flight for the whole window, and time the three
        # structured endpoints inside it. Sampling runs at 0.5 s throughout, as smoke's does.
        sm = Sampler(sc.pid, sc.base_url + "/metrics", interval=0.5)
        sm.start()
        m0 = _embed(c, sc.base_url)
        t0 = time.monotonic()
        load = {"analyze": [], "blocks": [], "features": []}
        deadline = t0 + seconds
        while time.monotonic() < deadline:
            # /features both restarts a pass when the previous one ends AND is a timed sample.
            load["features"].append(_timed(c, sc.base_url + "/features", feat))
            if prompt_id:
                load["analyze"].append(_timed(c, sc.base_url + "/analyze", analyze))
            load["blocks"].append(_timed(c, sc.base_url + "/blocks", blocks))
            time.sleep(1.0)
        elapsed = time.monotonic() - t0
        m1 = _embed(c, sc.base_url)
        rows = sm.stop()

        # --- E1: no leak, smoke's S1 method against the CHILD's RSS. ---
        rss = [v for v in metric_series(rows, "embed", "rss_mb") if v > 0]
        growth = mean_growth(steady(rss, warmup_frac=0.4))
        fails += _report("E1 no-leak", growth < LEAK_GROWTH_MB,
                         f"growth={growth:+.0f} MB over {len(rss)} samples (<{LEAK_GROWTH_MB:.0f})")

        # --- E2: the peak is bounded, AND the reported peak saw what a live sample saw. ---
        live_peak = max(rss, default=0.0)
        reported = max(metric_series(rows, "embed", "peak_rss_mb"), default=0.0)
        fails += _report("E2 peak-rss", live_peak < PEAK_RSS_CAP_MB,
                         f"peak={live_peak:.0f} MB (<{PEAK_RSS_CAP_MB:.0f}) "
                         f"resident={pctl(rss, 0.5):.0f} MB")
        # ⚠️ The guard-visibility property, end to end. A reported peak far BELOW a live sample
        # means the poll loop is sampling troughs — the exact regression this arm found and
        # test_guard_visibility.py now pins.
        #
        # The 5% is SAMPLING GRANULARITY, not slack in the property. The sidecar's own guard polls
        # at 1 Hz (KELD_SIDECAR_MEM_POLL_S) while this sampler reads /metrics at 2 Hz, so the live
        # series can catch an allocation the guard's next tick has not reached yet. Measured, the
        # two failure modes are nowhere near each other: after the fix, 2314 against a live 2345
        # (98.7%); before it, 1717 against a live 2072 (82.9%), because every sample was a
        # post-`_release_memory()` trough. A 95% floor separates them by 12 points.
        seen = (reported / live_peak) if live_peak else 1.0
        fails += _report("E2 peak-observed", seen >= 0.95,
                         f"/metrics peak_rss_mb={reported:.0f} MB is {seen:.1%} of the live max "
                         f"{live_peak:.0f} MB (>=95%; trough-sampling reads ~83%)")

        # --- E5: cost per message over many messages. ---
        # ⚠️ Reported against BUSY time, not wall time. A pass encodes `KELD_TEXTEMBED_MAX_ENCODE`
        # messages and then stops until the next /features call restarts one, so wall time folds
        # this arm's own ~2 s polling gap into the model's cost and inflates it. Busy time is the
        # `encoding: true` sample count times the sample interval — the same field a machine's own
        # /metrics reports, so the number is reproducible off a live sidecar.
        encoded = (dig(m1, "counts", "encoded") or 0) - (dig(m0, "counts", "encoded") or 0)
        batches = (dig(m1, "counts", "batches") or 0) - (dig(m0, "counts", "batches") or 0)
        busy = sum(1 for v in metric_series(rows, "embed", "encoding") if v) * 0.5
        ms = per_message_ms(encoded, busy)
        wall_ms = per_message_ms(encoded, elapsed)
        fails += _report("E5 encode-cost", ms is not None and encoded >= 8,
                         f"{encoded} messages / {batches} batches, "
                         f"{busy:.0f}s encoding of {elapsed:.0f}s wall = "
                         f"{('%.0f ms/message' % ms) if ms else 'n/a'} busy "
                         f"({('%.0f' % wall_ms) if wall_ms else 'n/a'} ms/message wall), "
                         f"{(encoded / busy) if busy else 0:.2f} msg/s while encoding")

        # --- E4: the structured endpoints under encoder load. ---
        for name, body_key in (("analyze", "analyze"), ("blocks", "blocks"), ("features", "features")):
            b = [s for st, s in base[body_key] if st == 200]
            l = [s for st, s in load[body_key] if st == 200]
            errs = sum(1 for st, _ in load[body_key] if st != 200)
            if not b or not l:
                fails += _report(f"E4 {name}-under-load", False,
                                 f"no successful samples (base={len(b)} load={len(l)} "
                                 f"base_status={[st for st, _ in base[body_key]]} "
                                 f"load_errors={errs})")
                continue
            b50, l50, l90 = pctl(b, 0.5), pctl(l, 0.5), pctl(l, 0.9)
            # Compared against a FLOOR as well as a factor: these calls are ~0.1 s, and 4x of a
            # 3 ms baseline is a threshold that measures nothing but scheduler jitter.
            budget = max(b50 * LATENCY_FACTOR, 0.5)
            fails += _report(f"E4 {name}-under-load", l50 <= budget and errs == 0,
                             f"idle p50={b50 * 1000:.0f}ms -> load p50={l50 * 1000:.0f}ms "
                             f"p90={l90 * 1000:.0f}ms (budget {budget * 1000:.0f}ms), "
                             f"n={len(l)} errors={errs}")
    finally:
        sc.stop()
        time.sleep(1.0)
    return fails


def _idle_unload(home, weights, path):
    """E3: the child dies on idle, the memory comes back, and the next request respawns it.

    Its own sidecar because `KELD_TEXTEMBED_IDLE_UNLOAD_S` is read once, at `Encoder`
    construction — there is no way to switch it on inside the process the other phases needed it
    switched off in."""
    fails = 0
    os.makedirs(home + "/b", exist_ok=True)
    sc = SidecarProcess(env=_env(home + "/b", weights,
                                 KELD_TEXTEMBED_IDLE_UNLOAD_S=str(IDLE_S),
                                 KELD_TEXTEMBED_MAX_ENCODE="4"))
    sc.start()
    try:
        c = httpx.Client(timeout=600.0)
        c.post(sc.base_url + "/ingest", json={"path": path})
        feat = {"path": path, "max_rows": 8}
        c.post(sc.base_url + "/features", json=feat)
        m = _wait(c, sc.base_url, lambda m: m.get("state") == "ready", 300.0, poll=0.25)
        if m.get("state") != "ready":
            return fails + _report("E3 idle-unload", False,
                                   f"child never came up: {m.get('state')}/{m.get('status')}")
        _wait(c, sc.base_url, lambda m: not m.get("encoding"), 300.0)
        up_tree = _tree_rss_mb(sc.pid)
        up_rss = _embed(c, sc.base_url).get("rss_mb") or 0.0

        # The idle clock runs from the last completed batch, and the poll loop decides at 1 Hz.
        m = _wait(c, sc.base_url, lambda m: m.get("state") == "down", IDLE_S + 30.0)
        down_tree = _tree_rss_mb(sc.pid)
        released = up_tree - down_tree
        killed = (dig(m, "counts", "kills_idle") or 0)
        fails += _report("E3 idle-unload-fires",
                         m.get("state") == "down" and killed >= 1,
                         f"state={m.get('state')} kills_idle={killed} after ~{IDLE_S:.0f}s idle")
        # ⚠️ Measured on the PROCESS TREE, not on embed.rss_mb: that field reads 0.0 the moment the
        # handle is dropped, which a leaked child would also produce. The claim is that ~1.7 GB
        # comes back to the OS, so the tree is what has to shrink.
        fails += _report("E3 idle-unload-releases", released > 1000.0,
                         f"tree rss {up_tree:.0f} -> {down_tree:.0f} MB "
                         f"(released {released:.0f} MB; child was {up_rss:.0f} MB)")

        # And it must come back. A lazily-respawning child that never respawns is an encoder that
        # silently stops producing the text half after the first quiet period.
        c.post(sc.base_url + "/features", json=feat)
        m = _wait(c, sc.base_url, lambda m: m.get("state") == "ready", 300.0, poll=0.25)
        spawns = dig(m, "counts", "spawns") or 0
        fails += _report("E3 respawn-on-demand",
                         m.get("state") == "ready" and spawns >= 2,
                         f"state={m.get('state')} spawns={spawns} rss={m.get('rss_mb')} MB")
    finally:
        sc.stop()
        time.sleep(1.0)
    return fails
