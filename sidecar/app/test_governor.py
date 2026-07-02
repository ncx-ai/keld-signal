"""Standalone tests for the sidecar Governor. Run:
  cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_governor.py
"""
import asyncio

from app.governor import Governor


def test_interval_zero_at_or_below_low():
    g = Governor(high=85.0, low=60.0, max_interval=2.0)
    assert g.interval_for(60.0) == 0.0
    assert g.interval_for(10.0) == 0.0


def test_interval_max_at_or_above_high():
    g = Governor(high=85.0, low=60.0, max_interval=2.0)
    assert g.interval_for(85.0) == 2.0
    assert g.interval_for(99.0) == 2.0


def test_interval_linear_midpoint():
    g = Governor(high=85.0, low=60.0, max_interval=2.0)
    # midpoint load 72.5 -> frac 0.5 -> 1.0s
    assert abs(g.interval_for(72.5) - 1.0) < 1e-9


def test_observe_ewma_first_sample_seeds():
    g = Governor()
    g.observe(50.0)
    assert g.ewma == 50.0
    g.observe(100.0)  # 0.3*100 + 0.7*50 = 65
    assert abs(g.ewma - 65.0) < 1e-9


def test_await_slot_paces_by_interval():
    # Fake clock + fake sleep: assert first call never waits, second call waits ~max_interval.
    t = {"now": 0.0}
    waited = []

    async def fake_sleep(secs):
        waited.append(secs)
        t["now"] += secs

    g = Governor(high=85.0, low=60.0, max_interval=2.0,
                 clock=lambda: t["now"], sleep=fake_sleep)
    g.observe(99.0)  # force max interval

    async def run():
        await g.await_slot()      # first: no prior start, must not wait
        await g.await_slot()      # second: must wait ~2.0s
    asyncio.run(run())
    assert waited == [2.0] or (len(waited) == 1 and abs(waited[0] - 2.0) < 1e-9)


def test_await_slot_noop_when_disabled():
    waited = []

    async def fake_sleep(secs):
        waited.append(secs)

    g = Governor(disabled=True, sleep=fake_sleep)
    g.observe(99.0)
    asyncio.run(g.await_slot())
    asyncio.run(g.await_slot())
    assert waited == []


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn()
        print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
