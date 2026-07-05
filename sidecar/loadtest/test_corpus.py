"""Run: cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python loadtest/test_corpus.py"""
import random
from loadtest.corpus import make_request, LEN_BUCKETS


def test_make_request_returns_known_path_and_body():
    rng = random.Random(1)
    path, body = make_request(rng, target_len=500)
    assert path in ("/classify", "/entities", "/extract")
    assert "text" in body and isinstance(body["text"], str)
    assert len(body["text"]) <= 500 + 200  # roughly bounded to target


def test_make_request_is_deterministic_under_seed():
    a = make_request(random.Random(42), 1000)
    b = make_request(random.Random(42), 1000)
    assert a == b


def test_len_buckets_span_short_to_max():
    assert min(LEN_BUCKETS) <= 300
    assert max(LEN_BUCKETS) >= 20000


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn(); print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
