"""Run: cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python loadtest/test_analysis.py"""
from loadtest.analysis import slope, steady, rss_slope_mb_per_min, relative_drop, nonincreasing


def test_slope_of_flat_is_zero():
    assert abs(slope([0, 1, 2, 3], [5, 5, 5, 5])) < 1e-9


def test_slope_positive():
    assert abs(slope([0, 1, 2], [0, 2, 4]) - 2.0) < 1e-9


def test_steady_drops_warmup_fraction():
    assert steady(list(range(10)), warmup_frac=0.2) == list(range(2, 10))


def test_rss_slope_per_minute():
    # +60 mb over 60 s == +60 mb/min
    assert abs(rss_slope_mb_per_min([(0.0, 100.0), (60.0, 160.0)]) - 60.0) < 1e-6


def test_relative_drop():
    assert abs(relative_drop(100.0, 25.0) - 0.75) < 1e-9


def test_nonincreasing_within_tolerance():
    assert nonincreasing([10.0, 9.9, 8.0, 8.05], tol_frac=0.05) is True
    assert nonincreasing([10.0, 20.0], tol_frac=0.05) is False


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn(); print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
