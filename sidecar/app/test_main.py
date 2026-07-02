"""Tests for the sidecar's load-protection guards: input clipping and the
single-inference lock. These import app.main (light — fastapi/pydantic only;
the GLiNER2 model is loaded lazily in lifespan, never at import) so they run
without torch. Runnable under pytest OR standalone: `python app/test_main.py`.
"""
import importlib
import os
import threading
import time


def _reload_main(max_chars: str | None):
    """Reload app.main with KELD_SIDECAR_MAX_CHARS set, so module-level _MAX_CHARS
    picks up the value."""
    if max_chars is None:
        os.environ.pop("KELD_SIDECAR_MAX_CHARS", None)
    else:
        os.environ["KELD_SIDECAR_MAX_CHARS"] = max_chars
    import app.main as m

    return importlib.reload(m)


def test_clip_truncates_above_cap():
    m = _reload_main("100")
    assert len(m._clip("x" * 500)) == 100


def test_clip_leaves_short_text():
    m = _reload_main("100")
    assert m._clip("hello") == "hello"


def test_clip_disabled_when_nonpositive():
    m = _reload_main("0")
    assert m._clip("x" * 5000) == "x" * 5000


def test_default_cap_is_generous():
    m = _reload_main(None)
    assert m._MAX_CHARS == 20000  # clips only pathological inputs


def test_infer_lock_serializes():
    """The lock must be a real mutex: a second acquire blocks while held."""
    m = _reload_main(None)
    assert m._infer_lock.acquire(blocking=False)
    try:
        got_it = m._infer_lock.acquire(blocking=False)
        assert got_it is False, "lock allowed a concurrent holder"
    finally:
        m._infer_lock.release()


def test_infer_lock_serializes_under_threads():
    """Two threads contending for the model lock must not overlap in the
    critical section (proves fan-out inference is serialized)."""
    m = _reload_main(None)
    overlaps = []
    active = {"n": 0}
    state_lock = threading.Lock()

    def worker():
        with m._infer_lock:
            with state_lock:
                active["n"] += 1
                if active["n"] > 1:
                    overlaps.append(True)
            time.sleep(0.02)
            with state_lock:
                active["n"] -= 1

    threads = [threading.Thread(target=worker) for _ in range(5)]
    for t in threads:
        t.start()
    for t in threads:
        t.join()
    assert not overlaps, "model lock permitted concurrent inference"


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn()
        print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
