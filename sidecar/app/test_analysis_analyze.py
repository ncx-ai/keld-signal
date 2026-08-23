import sys, os, json, tempfile
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from app.analysis.analyze import analyze_window, PromptNotFound


def _write(tmp, rows):
    p = os.path.join(tmp, "abcd1234-0000.jsonl")
    with open(p, "w") as fh:
        for o in rows:
            fh.write(json.dumps(o, separators=(",", ":")) + "\n")
    return p


def _turn(ts, uuid=None, text="hello", cwd="/workspace/proj"):
    o = {"type": "user", "timestamp": ts, "cwd": cwd,
         "message": {"content": [{"type": "text", "text": text}]}}
    if uuid:
        o["uuid"] = uuid
    return o


def test_window_ends_at_the_prompt_and_looks_back():
    """The window is the hour of work LEADING TO the prompt, so a turn after it is excluded."""
    with tempfile.TemporaryDirectory() as tmp:
        p = _write(tmp, [
            _turn("2026-08-01T10:00:00Z", text="early work"),
            _turn("2026-08-01T10:30:00Z", "target", "the prompt"),
            _turn("2026-08-01T11:00:00Z", text="later work"),
        ])
        out = analyze_window(p, "target", span_minutes=60)
        assert out["window_end"] == "2026-08-01T10:30:00+00:00", out["window_end"]
        assert out["window_start"] == "2026-08-01T09:30:00+00:00", out["window_start"]


def test_unknown_prompt_id_raises_rather_than_returning_an_empty_window():
    """An empty payload and a missing prompt are different facts; conflating them would publish
    'nothing was happening' for what is really a resolution failure."""
    with tempfile.TemporaryDirectory() as tmp:
        p = _write(tmp, [_turn("2026-08-01T10:00:00Z", "known")])
        try:
            analyze_window(p, "nope")
        except PromptNotFound:
            return
        raise AssertionError("expected PromptNotFound")


def test_payload_carries_the_schema_and_no_prompt_text():
    with tempfile.TemporaryDirectory() as tmp:
        p = _write(tmp, [_turn("2026-08-01T10:00:00Z", "t", "secret customer name here")])
        out = analyze_window(p, "t")
        assert out["schema"] >= 1
        assert "secret customer name here" not in json.dumps(out)
        # Exact JSON KEYS, not bare substrings: "window_start"/"window_end" are required fields
        # (see test_window_ends_at_the_prompt_and_looks_back) and both legitimately contain
        # "start"/"end" as substrings. What this guards against is a leaked span-object key
        # (the shape an entity extractor emits: {"text":.., "start":.., "end":.., "offset":..}),
        # which would show up as its own quoted JSON key, not as a suffix of a longer one.
        dumped = json.dumps(out)
        for k in ("text", "span", "start", "end", "offset"):
            assert f'"{k}":' not in dumped, k


if __name__ == "__main__":
    fns = [(n, f) for n, f in sorted(globals().items()) if n.startswith("test_")]
    bad = 0
    for n, f in fns:
        try:
            f(); print(f"PASS {n}")
        except AssertionError as e:
            bad += 1; print(f"FAIL {n}: {e}")
    print(f"\n{len(fns)-bad}/{len(fns)} passed")
    sys.exit(1 if bad else 0)
