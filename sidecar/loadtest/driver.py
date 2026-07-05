"""Fire sidecar requests concurrently for a duration (or an n-request flood)."""
import concurrent.futures
import random
import time
from dataclasses import dataclass

import httpx

from loadtest.corpus import make_request, LEN_BUCKETS


@dataclass
class Result:
    t: float
    status: int
    latency_s: float
    path: str


def _one(client, base_url, rng, target_len, t0):
    tl = target_len if target_len is not None else rng.choice(LEN_BUCKETS)
    path, body = make_request(rng, tl)
    start = time.monotonic()
    try:
        r = client.post(base_url + path, json=body, timeout=60.0)
        status = r.status_code
    except Exception:
        status = 0
    return Result(time.monotonic() - t0, status, time.monotonic() - start, path)


def run_load(base_url, duration_s, concurrency, rng, target_len=None):
    results = []
    t0 = time.monotonic()
    deadline = t0 + duration_s
    with httpx.Client() as client, \
            concurrent.futures.ThreadPoolExecutor(max_workers=concurrency) as ex:
        while time.monotonic() < deadline:
            futs = [ex.submit(_one, client, base_url, random.Random(rng.random()),
                              target_len, t0) for _ in range(concurrency)]
            for f in futs:
                results.append(f.result())
    return results


def flood(base_url, n, target_len, rng=None):
    rng = rng or random.Random(0)
    t0 = time.monotonic()
    with httpx.Client() as client, \
            concurrent.futures.ThreadPoolExecutor(max_workers=n) as ex:
        futs = [ex.submit(_one, client, base_url, random.Random(rng.random()),
                          target_len, t0) for _ in range(n)]
        return [f.result() for f in futs]
