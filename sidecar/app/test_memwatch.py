"""Standalone tests for the sidecar memory-pressure eviction state machine. Run:
  cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_memwatch.py
"""
from app.memwatch import (
    MemoryWatch, LOADED, EVICTED, RELOADING, DORMANT, NONE, EVICT, RELOAD,
)


def _watch(samples, *, evict_pct=5.0, margin=1024.0, hold=60.0):
    """A MemoryWatch driven by a scripted (avail_pct, avail_mb) sequence and a
    fake clock that advances 1s per poll."""
    t = {"now": 0.0}
    seq = list(samples)

    def clock():
        return t["now"]

    def sampler():
        v = seq.pop(0)
        t["now"] += 1.0
        return v

    return MemoryWatch(evict_pct=evict_pct, reload_margin_mb=margin,
                       restore_hold_s=hold, disabled=False,
                       clock=clock, sampler=sampler)


def test_evict_when_avail_pct_at_or_below_mark():
    w = _watch([(4.0, 500.0)], evict_pct=5.0)
    assert w.poll(LOADED, model_cost_mb=2000.0) == EVICT


def test_no_evict_above_mark():
    w = _watch([(6.0, 500.0)], evict_pct=5.0)
    assert w.poll(LOADED, model_cost_mb=2000.0) == NONE


def test_has_headroom_uses_model_cost_plus_margin():
    w = _watch([(50.0, 3100.0)], margin=1024.0)
    # 2000+1024 = 3024; 3100 >= 3024 -> True
    assert w.has_headroom(3100.0, 2000.0) is True
    assert w.has_headroom(3000.0, 2000.0) is False  # 3000 < 3024


def test_reload_only_after_hold_duration():
    # headroom present every poll; hold=3s. Needs 3 continuous seconds.
    w = _watch([(50.0, 4000.0)] * 5, hold=3.0, margin=1024.0)
    a1 = w.poll(EVICTED, 2000.0)
    a2 = w.poll(EVICTED, 2000.0)
    a3 = w.poll(EVICTED, 2000.0)
    a4 = w.poll(EVICTED, 2000.0)
    assert [a1, a2, a3] == [NONE, NONE, NONE]
    assert a4 == RELOAD


def test_hold_resets_when_headroom_lost():
    w = _watch([(50.0, 4000.0), (50.0, 4000.0), (50.0, 2000.0),
                (50.0, 4000.0), (50.0, 4000.0), (50.0, 4000.0)],
               hold=2.0, margin=1024.0)
    acts = [w.poll(EVICTED, 2000.0) for _ in range(6)]
    assert RELOAD in acts
    assert acts.index(RELOAD) >= 4  # not before the reset + a fresh hold


def test_disabled_never_acts():
    t = {"now": 0.0}
    w = MemoryWatch(disabled=True, clock=lambda: t["now"],
                    sampler=lambda: (1.0, 10.0))
    assert w.poll(LOADED, 2000.0) == NONE
    assert w.poll(EVICTED, 2000.0) == NONE


def test_reloading_state_is_noop():
    w = _watch([(50.0, 9000.0)])
    assert w.poll(RELOADING, 2000.0) == NONE


def test_poll_records_last_sample():
    w = _watch([(12.5, 777.0)])
    w.poll(LOADED, 2000.0)
    assert w.last_avail_pct == 12.5
    assert w.last_avail_mb == 777.0


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn()
        print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
