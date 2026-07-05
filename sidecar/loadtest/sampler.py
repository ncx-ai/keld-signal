"""Poll the sidecar process RSS/CPU and its /metrics into a time series."""
import threading
import time
from dataclasses import dataclass, field

import httpx
import psutil


@dataclass
class Sample:
    t: float
    rss_mb: float
    cpu_pct: float
    metrics: dict = field(default_factory=dict)


class Sampler:
    def __init__(self, pid, metrics_url, interval=0.5):
        self._proc = psutil.Process(pid)
        self._url = metrics_url
        self._interval = interval
        self._rows = []
        self._stop = threading.Event()
        self._thread = None
        self._t0 = None

    def _loop(self):
        self._proc.cpu_percent(None)  # prime
        while not self._stop.is_set():
            try:
                rss = self._proc.memory_info().rss / (1024.0 * 1024.0)
                cpu = self._proc.cpu_percent(None)
            except psutil.NoSuchProcess:
                break
            metrics = {}
            try:
                metrics = httpx.get(self._url, timeout=2.0).json()
            except Exception:
                pass
            self._rows.append(Sample(time.monotonic() - self._t0, rss, cpu, metrics))
            self._stop.wait(self._interval)

    def start(self):
        self._t0 = time.monotonic()
        self._thread = threading.Thread(target=self._loop, daemon=True)
        self._thread.start()

    def stop(self):
        self._stop.set()
        if self._thread:
            self._thread.join(timeout=5)
        return self._rows
