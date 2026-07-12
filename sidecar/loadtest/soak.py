"""Soak tier (opt-in, long): slow-leak slope (K1), CPU stress sweep (K2), and a
real worker recycle under memory pressure (K4) with the evict threshold
configured just below the measured baseline available% so the true host is
never driven to 5%. Deterministic recycle transitions (K3, incl. idle) are
covered by app/test_worker_manager.py."""
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


def _worker_pid(parent_pid):
    """The inference worker is the sidecar (parent) process's only child; None
    if it hasn't been spawned yet (or between recycles)."""
    try:
        children = psutil.Process(parent_pid).children()
        return children[0].pid if children else None
    except psutil.NoSuchProcess:
        return None


def _k1_k2(minutes, live):
    fails = 0
    # Idle eviction off so a quiet moment doesn't unload mid-measurement.
    sc = SidecarProcess(env={"KELD_SIDECAR_IDLE_UNLOAD_S": "0"})
    sc.start()
    try:
        # Unlike the old in-process model, the inference worker is spawned
        # lazily on first request (/health going "ok" only means the parent
        # can serve on demand). Warm it up first so the whole measurement
        # window sees a steady, already-loaded worker.
        httpx.post(sc.base_url + "/classify",
                   json={"text": "warm up", "tasks": {"task_type": ["codegen", "other"]}},
                   timeout=60.0)
        worker_pid = _worker_pid(sc.pid)
        rng = random.Random(11)
        # K1: sustained moderate load; RSS slope over the whole run. Sample the
        # worker process — it holds the model; the parent's own RSS stays flat
        # by design and would make this assertion vacuous.
        sm = Sampler(worker_pid or sc.pid, sc.base_url + "/metrics", interval=1.0)
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


def _k4_real_recycle():
    """Configure the RSS margin / evict-pct just below current available% and
    apply a bounded memory stressor to cross it, then assert the worker is
    held (killed) under pressure, requests 503 while held, the parent
    service's own RSS stays flat throughout (it never holds the model), and
    the worker recovers to serving on demand once headroom returns."""
    fails = 0
    vm = psutil.virtual_memory()
    avail_pct = vm.available / vm.total * 100.0
    evict_at = max(1.0, avail_pct - 3.0)  # trip with a small stressor; never 5% of a full box
    env = {
        "KELD_SIDECAR_EVICT_AVAIL_PCT": f"{evict_at:.1f}",
        "KELD_SIDECAR_RSS_MARGIN_MB": "256",   # small so a modest stressor trips it
        "KELD_SIDECAR_MEM_POLL_S": "1",
        "KELD_SIDECAR_IDLE_UNLOAD_S": "0",
    }
    sc = SidecarProcess(env=env)
    sc.start()
    try:
        # Spawn + warm the worker before applying pressure so model_cost_mb is
        # known and the worker is READY (poll()'s pressure check is a no-op
        # unless the worker is already up).
        r0 = httpx.post(sc.base_url + "/classify",
                        json={"text": "hi", "tasks": {"task_type": ["codegen", "other"]}},
                        timeout=60.0)
        fails += _report("K4 warm-up", r0.status_code == 200, f"status={r0.status_code}")

        m0 = httpx.get(sc.base_url + "/metrics", timeout=2.0).json()
        cost = m0["worker"]["model_cost_mb"] or 0.0
        worker_rss0 = m0["worker"]["worker_rss_mb"] or 0.0
        parent_rss0 = _rss_mb(sc.pid)

        from loadtest.stressor import MemStressor
        need_mb = int((avail_pct - evict_at + 1.0) / 100.0 * vm.total / (1024.0 * 1024.0))
        ms = MemStressor(target_mb=need_mb, floor_mb=1024)
        ms.start()

        state, worker_rss1 = m0["worker"]["state"], worker_rss0
        for _ in range(30):
            time.sleep(1)
            m = httpx.get(sc.base_url + "/metrics", timeout=2.0).json()
            state = m["worker"]["state"]
            worker_rss1 = m["worker"]["worker_rss_mb"] or 0.0
            if state == "held":
                break
        fails += _report("K4 recycles-under-pressure", state == "held", f"state={state}")

        r = httpx.post(sc.base_url + "/classify",
                       json={"text": "hi", "tasks": {"task_type": ["codegen", "other"]}},
                       timeout=5.0)
        fails += _report("K4 503-while-held", r.status_code == 503, f"status={r.status_code}")

        dropped = worker_rss0 - worker_rss1
        fails += _report("K4 worker-rss-released", cost == 0.0 or dropped > cost * 0.4,
                         f"worker_rss0={worker_rss0:.0f} worker_rss1={worker_rss1:.0f} "
                         f"dropped={dropped:.0f} cost={cost:.0f}")

        parent_rss1 = _rss_mb(sc.pid)
        fails += _report("K4 parent-rss-flat", abs(parent_rss1 - parent_rss0) < 100,
                         f"parent_rss0={parent_rss0:.0f} parent_rss1={parent_rss1:.0f}")

        ms.stop()
        recovered = False
        m = {"worker": {"state": state}}
        for _ in range(40):
            time.sleep(1)
            m = httpx.get(sc.base_url + "/metrics", timeout=2.0).json()
            if m["worker"]["state"] != "held":
                recovered = True
                break
        fails += _report("K4 recovers", recovered, f"final_state={m['worker']['state']}")

        # Serve-on-demand: a fresh request respawns the worker and succeeds.
        r2 = httpx.post(sc.base_url + "/classify",
                        json={"text": "hi", "tasks": {"task_type": ["codegen", "other"]}},
                        timeout=60.0)
        fails += _report("K4 serves-after-recovery", r2.status_code == 200, f"status={r2.status_code}")
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
            print(f"  t={time.monotonic()-t0:6.0f}s parent_rss={rss:7.0f}MB "
                  f"worker_state={m['worker']['state']} "
                  f"worker_rss={m['worker']['worker_rss_mb']}MB "
                  f"ewma={m['governor']['cpu_ewma']} "
                  f"interval={m['governor']['current_interval_ms']}ms "
                  f"completed={m['counts']['completed']}")
        except Exception:
            pass


def run(minutes=30.0, live=False):
    fails = 0
    fails += _k1_k2(minutes, live)
    fails += _k4_real_recycle()
    print(f"\nsoak: {'ALL PASS' if fails == 0 else str(fails) + ' FAILED'}")
    return fails
