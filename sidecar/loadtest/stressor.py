"""External host CPU / RAM pressure for governor + eviction tests. Separate
processes so the pressure is genuinely external to the sidecar. The RAM stressor
has a hard available-RAM floor and aborts rather than risk OOMing the host."""
import multiprocessing as mp
import time

import psutil


def _cpu_spin(stop):
    x = 0
    while not stop.is_set():
        x = (x + 1) % 1_000_000


def _mem_hold(target_mb, floor_mb, stop):
    blocks = []
    chunk = 64  # MB per allocation step
    allocated = 0
    while allocated < target_mb and not stop.is_set():
        if psutil.virtual_memory().available / (1024.0 * 1024.0) - chunk < floor_mb:
            break  # safety floor: never cross it
        b = bytearray(chunk * 1024 * 1024)
        b[::4096] = b"\x01" * len(b[::4096])  # touch pages -> resident
        blocks.append(b)
        allocated += chunk
    while not stop.is_set():
        time.sleep(0.1)


class CpuStressor:
    def __init__(self, workers):
        self._workers = workers
        self._stop = mp.Event()
        self._procs = []

    def start(self):
        for _ in range(self._workers):
            p = mp.Process(target=_cpu_spin, args=(self._stop,), daemon=True)
            p.start()
            self._procs.append(p)

    def stop(self):
        self._stop.set()
        for p in self._procs:
            p.join(timeout=5)


class MemStressor:
    def __init__(self, target_mb, floor_mb):
        self._target_mb = target_mb
        self._floor_mb = floor_mb
        self._stop = mp.Event()
        self._proc = None

    def start(self):
        self._proc = mp.Process(target=_mem_hold,
                                args=(self._target_mb, self._floor_mb, self._stop),
                                daemon=True)
        self._proc.start()

    def stop(self):
        self._stop.set()
        if self._proc:
            self._proc.join(timeout=10)
