"""Pure analysis helpers for the load tiers. All assertions are relative to a
same-run baseline, so these are simple, dependency-free statistics."""


def slope(xs, ys):
    """Least-squares slope of ys over xs."""
    n = len(xs)
    if n < 2:
        return 0.0
    mx = sum(xs) / n
    my = sum(ys) / n
    num = sum((x - mx) * (y - my) for x, y in zip(xs, ys))
    den = sum((x - mx) ** 2 for x in xs)
    return num / den if den else 0.0


def steady(series, warmup_frac=0.2):
    """Drop the leading warmup fraction so ramp/JIT transients don't skew stats."""
    k = int(len(series) * warmup_frac)
    return series[k:]


def rss_slope_mb_per_min(samples):
    """samples: list of (t_seconds, rss_mb). Returns MB/min."""
    if len(samples) < 2:
        return 0.0
    xs = [t for t, _ in samples]
    ys = [r for _, r in samples]
    return slope(xs, ys) * 60.0


def relative_drop(baseline, stressed):
    """Fraction by which `stressed` fell below `baseline` (0..1)."""
    if baseline <= 0:
        return 0.0
    return (baseline - stressed) / baseline


def nonincreasing(values, tol_frac):
    """True if each value is <= previous within a tolerance (noise margin)."""
    for prev, cur in zip(values, values[1:]):
        if cur > prev * (1.0 + tol_frac):
            return False
    return True
