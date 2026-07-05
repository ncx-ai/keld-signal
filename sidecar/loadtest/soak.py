"""Soak tier (opt-in, long): slow-leak slope (K1), CPU stress sweep (K2), and a
real model unload/reload (K4) with the evict threshold configured just below the
measured baseline available% so the true host is never driven to 5%. Deterministic
eviction transitions (K3, incl. idle) are covered by app/test_memwatch.py."""
import os
import random
import time

import httpx
import psutil

from loadtest.analysis import rss_slope_mb_per_min, steady, nonincreasing, mean_growth
from loadtest.driver import run_load
from loadtest.harness import SidecarProcess
from loadtest.sampler import Sampler


def _report(name, ok, detail):
    print(f"{'PASS' if ok else 'FAIL'} {name}: {detail}")
    return 0 if ok else 1


def _rss_mb(pid):
    return psutil.Process(pid).memory_info().rss / (1024.0 * 1024.0)


def _k1_k2(minutes, live):
    fails = 0
    # Idle eviction off so a quiet moment doesn't unload mid-measurement.
    sc = SidecarProcess(env={"KELD_SIDECAR_IDLE_UNLOAD_S": "0"})
    sc.start()
    try:
        rng = random.Random(11)
        # K1: sustained moderate load; RSS slope over the whole run.
        sm = Sampler(sc.pid, sc.base_url + "/metrics", interval=1.0)
        sm.start()
        dur = minutes * 60.0
        if live:
            _live_load(sc, dur, rng)
        else:
            run_load(sc.base_url, duration_s=dur, concurrency=4, rng=rng, target_len=2000)
        rows = sm.stop()
        steady_rows = steady(rows, warmup_frac=0.15)
        slope = rss_slope_mb_per_min([(r.t, r.rss_mb) for r in steady_rows])
        # Robust leak signal: second-half vs first-half mean. Endpoint drift is
        # unreliable against the ~±100 MB run-to-run oscillation (a flat 45-min run
        # can show ~50 MB first-vs-last purely from noise); the half-means average
        # that out, and the slope corroborates. A real leak shows large positive growth.
        growth = mean_growth([r.rss_mb for r in steady_rows])
        fails += _report("K1 slow-leak", growth < 150 and slope < 2.0,
                         f"growth={growth:.0f} MB slope={slope:.3f} MB/min")

        # K2: CPU stress sweep 0 -> high; throughput must be non-increasing.
        from loadtest.stressor import CpuStressor
        cores = os.cpu_count() or 4
        rates = []
        for w in (0, max(1, cores // 2), cores, cores * 2):
            st = CpuStressor(workers=w) if w else None
            if st:
                st.start()
            res = run_load(sc.base_url, duration_s=20, concurrency=8, rng=rng, target_len=2000)
            if st:
                st.stop()
            rates.append(len([r for r in res if r.status == 200]) / 20.0)
            print(f"  sweep workers={w} rate={rates[-1]:.1f}/s")
        fails += _report("K2 cpu-sweep-monotonic", nonincreasing(rates, tol_frac=0.20),
                         f"rates={['%.1f' % r for r in rates]}")
        res = run_load(sc.base_url, duration_s=20, concurrency=8, rng=rng, target_len=2000)
        recov = len([r for r in res if r.status == 200]) / 20.0
        fails += _report("K2 recovers", recov >= rates[0] * 0.7,
                         f"baseline={rates[0]:.1f}/s recovered={recov:.1f}/s")
    finally:
        sc.stop()
    return fails


def _k4_real_eviction():
    """Configure evict-mark just below current available% and reload gate small,
    apply a bounded memory stressor to cross it, and assert the model actually
    unloads (RSS drops ~model_cost) and reloads on recovery."""
    fails = 0
    vm = psutil.virtual_memory()
    avail_pct = vm.available / vm.total * 100.0
    evict_at = max(1.0, avail_pct - 3.0)  # trip with a small stressor; never 5% of a full box
    env = {
        "KELD_SIDECAR_EVICT_AVAIL_PCT": f"{evict_at:.1f}",
        "KELD_SIDECAR_RESTORE_HOLD_S": "5",     # shortened for the test
        "KELD_SIDECAR_RELOAD_MARGIN_MB": "256",
        "KELD_SIDECAR_MEM_POLL_S": "1",
        "KELD_SIDECAR_IDLE_UNLOAD_S": "0",
    }
    sc = SidecarProcess(env=env)
    sc.start()
    try:
        m0 = httpx.get(sc.base_url + "/metrics", timeout=2.0).json()
        cost = m0["memory"]["model_cost_mb"] or 0.0
        rss0 = _rss_mb(sc.pid)

        from loadtest.stressor import MemStressor
        need_mb = int((avail_pct - evict_at + 1.0) / 100.0 * vm.total / (1024.0 * 1024.0))
        ms = MemStressor(target_mb=need_mb, floor_mb=1024)
        ms.start()

        state, rss1 = "loaded", rss0
        for _ in range(30):
            time.sleep(1)
            m = httpx.get(sc.base_url + "/metrics", timeout=2.0).json()
            state = m["model_state"]
            rss1 = _rss_mb(sc.pid)
            if state in ("evicted", "reloading"):
                break
        fails += _report("K4 evicts", state in ("evicted", "reloading"), f"state={state}")

        r = httpx.post(sc.base_url + "/classify",
                       json={"text": "hi", "tasks": {"task_type": ["codegen", "other"]}},
                       timeout=5.0)
        fails += _report("K4 503-while-evicted", r.status_code == 503, f"status={r.status_code}")
        dropped = rss0 - rss1
        fails += _report("K4 rss-released", cost == 0.0 or dropped > cost * 0.4,
                         f"rss0={rss0:.0f} rss1={rss1:.0f} dropped={dropped:.0f} cost={cost:.0f}")

        ms.stop()
        reloaded = False
        m = {"model_state": state}
        for _ in range(40):
            time.sleep(1)
            m = httpx.get(sc.base_url + "/metrics", timeout=2.0).json()
            if m["model_state"] == "loaded":
                reloaded = True
                break
        fails += _report("K4 reloads", reloaded, f"final_state={m['model_state']}")
    finally:
        sc.stop()
    return fails


def _live_load(sc, dur, rng):
    t0 = time.monotonic()
    while time.monotonic() - t0 < dur:
        run_load(sc.base_url, duration_s=10, concurrency=4, rng=rng, target_len=2000)
        try:
            m = httpx.get(sc.base_url + "/metrics", timeout=2.0).json()
            rss = _rss_mb(sc.pid)
            print(f"  t={time.monotonic()-t0:6.0f}s rss={rss:7.0f}MB "
                  f"state={m['model_state']} ewma={m['governor']['cpu_ewma']} "
                  f"interval={m['governor']['current_interval_ms']}ms "
                  f"completed={m['counts']['completed']}")
        except Exception:
            pass


def run(minutes=30.0, live=False):
    fails = 0
    fails += _k1_k2(minutes, live)
    fails += _k4_real_eviction()
    print(f"\nsoak: {'ALL PASS' if fails == 0 else str(fails) + ' FAILED'}")
    return fails
