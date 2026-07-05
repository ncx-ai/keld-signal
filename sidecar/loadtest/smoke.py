"""Smoke tier (~2-3 min): gross leak, flat-vs-rate, one CPU-throttle step,
backpressure, and idle no-spin. Assertions are relative to a same-run baseline
with generous margins. Returns the number of failed checks."""
import os
import random

import httpx

from loadtest.analysis import mean_growth, steady, relative_drop
from loadtest.driver import run_load, flood
from loadtest.harness import SidecarProcess
from loadtest.sampler import Sampler

import psutil

PEAK_RSS_CAP_MB = float(os.environ.get("KELD_LOADTEST_PEAK_RSS_MB", "6144"))
# Leak tolerance as second-half-minus-first-half RSS growth over the steady window.
# RSS here is static (weights + one bounded transient) but oscillates ~±150 MB and
# takes ~15-20s of first-touch torch allocation to plateau, so we measure a
# post-warmup window and tolerate that noise; a real leak grows sustainedly (far
# larger). The soak (K1) is the authoritative long-window leak test.
LEAK_GROWTH_MB = float(os.environ.get("KELD_LOADTEST_LEAK_GROWTH_MB", "300"))
# Concurrency kept modest: one inference already saturates many cores (torch
# intra-op parallelism), so a high fan-out just self-saturates the box.
_CONC = int(os.environ.get("KELD_LOADTEST_CONCURRENCY", "4"))


def _report(name, ok, detail):
    print(f"{'PASS' if ok else 'FAIL'} {name}: {detail}")
    return 0 if ok else 1


def _avg(rows, attr="rss_mb"):
    vals = [getattr(r, attr) for r in rows]
    return sum(vals) / len(vals) if vals else 0.0


def run(quick=True):
    fails = 0
    # Idle eviction off during smoke so it doesn't unload mid-measurement.
    sc = SidecarProcess(env={"KELD_SIDECAR_IDLE_UNLOAD_S": "0"})
    sc.start()
    try:
        rng = random.Random(7)

        # --- S5: idle no-spin --- measured FIRST, when the model is warmed but no
        # work has been submitted (a genuinely idle moment). Per-process CPU is
        # unaffected by other processes, so a busy host doesn't skew it. Three
        # blocking 1s reads, each with a real interval (no sampler dt artifact).
        proc = psutil.Process(sc.pid)
        proc.cpu_percent(None)  # prime
        idle_cpu = max(proc.cpu_percent(interval=1.0) for _ in range(3))
        fails += _report("S5 idle-no-spin", idle_cpu < 25, f"idle_cpu_max={idle_cpu:.0f}% (<25)")

        # --- S1/S2: baseline at low rate, then high rate ---
        sm = Sampler(sc.pid, sc.base_url + "/metrics", interval=0.5)
        sm.start()
        run_load(sc.base_url, duration_s=20, concurrency=1, rng=rng, target_len=2000)
        rows_low = sm.stop()

        sm = Sampler(sc.pid, sc.base_url + "/metrics", interval=0.5)
        sm.start()
        res_hi = run_load(sc.base_url, duration_s=45, concurrency=_CONC, rng=rng, target_len=2000)
        rows_hi = sm.stop()

        # Discard 40% warmup, then compare second-half vs first-half mean RSS. A
        # leak shows sustained growth; the ~±150 MB noise cancels. (Verified flat
        # past ~20s with a standalone diagnostic.)
        rss = [r.rss_mb for r in steady(rows_hi, warmup_frac=0.4)]
        growth = mean_growth(rss)
        peak = max((r.rss_mb for r in rows_hi), default=0.0)
        fails += _report("S1 no-leak", growth < LEAK_GROWTH_MB, f"growth={growth:.0f} MB over window (<{LEAK_GROWTH_MB})")
        fails += _report("S1 peak-rss", peak < PEAK_RSS_CAP_MB, f"peak={peak:.0f} MB (<{PEAK_RSS_CAP_MB})")

        rss_low = _avg(steady(rows_low))
        rss_hi = _avg(steady(rows_hi))
        drift = abs(rss_hi - rss_low)
        fails += _report("S2 flat-vs-rate", drift < 400, f"low={rss_low:.0f} high={rss_hi:.0f} drift={drift:.0f} MB (<400)")

        base = [r for r in res_hi if r.status == 200]
        base_rate = len(base) / 45.0

        # --- S3: CPU throttle under external stress ---
        from loadtest.stressor import CpuStressor
        cpu = CpuStressor(workers=max(2, (os.cpu_count() or 4)))
        cpu.start()
        sm = Sampler(sc.pid, sc.base_url + "/metrics", interval=0.5)
        sm.start()
        res_st = run_load(sc.base_url, duration_s=30, concurrency=_CONC, rng=rng, target_len=2000)
        rows_st = sm.stop()
        cpu.stop()
        st_rate = len([r for r in res_st if r.status == 200]) / 30.0
        ewma_max = max((s.metrics.get("governor", {}).get("cpu_ewma") or 0 for s in rows_st), default=0)
        drop = relative_drop(base_rate, st_rate)
        fails += _report("S3 cpu-throttle", drop > 0.15 and ewma_max >= 60,
                         f"base={base_rate:.1f}/s stressed={st_rate:.1f}/s drop={drop:.0%} ewma_max={ewma_max:.0f}")

        # --- S6: CPU thread scaling --- threads under stress should drop below the
        # unstressed baseline (the idle ceiling), whatever that ceiling is.
        def _threads(rows):
            return [s.metrics.get("governor", {}).get("cpu_threads") for s in rows
                    if s.metrics.get("governor", {}).get("cpu_threads") is not None]
        base_thr = max(_threads(rows_hi), default=(os.cpu_count() or 1))
        stressed = _threads(rows_st)
        thr_min = min(stressed) if stressed else base_thr
        fails += _report("S6 cpu-thread-scaling", thr_min < base_thr,
                         f"stressed_min={thr_min} < baseline {base_thr}")

        # --- S4: backpressure --- (n < 2*queue_max so the backlog drains quickly for S5)
        res_flood = flood(sc.base_url, n=100, target_len=8000)
        got_503 = sum(1 for r in res_flood if r.status == 503)
        got_200 = sum(1 for r in res_flood if r.status == 200)
        maxq = 0
        try:
            m = httpx.get(sc.base_url + "/metrics", timeout=2.0).json()
            maxq = m["runner"]["queue_depth"]
        except Exception:
            pass
        fails += _report("S4 backpressure", got_503 >= 1 and got_200 >= 1,
                         f"200={got_200} 503={got_503} queue_depth_now={maxq}")
    finally:
        sc.stop()
    print(f"\nsmoke: {'ALL PASS' if fails == 0 else str(fails) + ' FAILED'}")
    return fails
