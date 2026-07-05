"""Standalone tests for the CPU-thread scaler. Run:
  cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_cpuscale.py
"""
from app.cpuscale import CpuScaler


def _scaler(**kw):
    kw.setdefault("low", 60.0)
    kw.setdefault("high", 85.0)
    kw.setdefault("max_threads", 20)
    kw.setdefault("min_threads", 1)
    kw.setdefault("disabled", False)
    return CpuScaler(**kw)


def test_full_cores_at_or_below_low():
    s = _scaler()
    assert s.threads_for(60.0) == 20
    assert s.threads_for(10.0) == 20


def test_floor_at_or_above_high():
    s = _scaler()
    assert s.threads_for(85.0) == 1
    assert s.threads_for(99.0) == 1


def test_linear_ramp_midpoint():
    s = _scaler(max_threads=20, min_threads=2)
    # midpoint load 72.5 -> frac 0.5 -> 20 - 0.5*18 = 11
    assert s.threads_for(72.5) == 11


def test_disabled_returns_max():
    s = _scaler(disabled=True)
    assert s.threads_for(99.0) == 20


def test_min_never_exceeds_max():
    s = CpuScaler(low=60.0, high=85.0, max_threads=4, min_threads=8, disabled=False)
    assert s.max_threads == 4
    assert s.threads_for(99.0) == 4  # floor clamped up to max


def test_never_below_one():
    s = _scaler(max_threads=20, min_threads=1)
    assert s.threads_for(84.9) >= 1


def test_default_max_is_half_of_cores():
    import os
    os.environ.pop("KELD_SIDECAR_MAX_THREADS", None)
    expected = max(1, (os.cpu_count() or 1) // 2)
    s = CpuScaler(low=60.0, high=85.0, min_threads=1, disabled=False)
    assert s.max_threads == expected


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn()
        print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
