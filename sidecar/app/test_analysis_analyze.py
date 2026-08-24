import sys, os, json, tempfile
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from app.analysis.analyze import analyze_window, PromptNotFound
from app.analysis.store import open_store


def _store(tmp):
    """A store under the test's own tmpdir. Passed EXPLICITLY: `analyze_window` now serves from
    the reference series, and its default is the real one at ~/.keld/state/refseries.db, which a
    unit test must never write to. The equivalence between this path and a parse is the subject
    of app/test_analyze_store.py; these tests are about the window's own semantics."""
    return open_store(os.path.join(tmp, "state", "refseries.db"))


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
        out = analyze_window(p, "target", span_minutes=60, store=_store(tmp))
        assert out["window_end"] == "2026-08-01T10:30:00+00:00", out["window_end"]
        assert out["window_start"] == "2026-08-01T09:30:00+00:00", out["window_start"]


def test_unknown_prompt_id_raises_rather_than_returning_an_empty_window():
    """An empty payload and a missing prompt are different facts; conflating them would publish
    'nothing was happening' for what is really a resolution failure."""
    with tempfile.TemporaryDirectory() as tmp:
        p = _write(tmp, [_turn("2026-08-01T10:00:00Z", "known")])
        try:
            analyze_window(p, "nope", store=_store(tmp))
        except PromptNotFound:
            return
        raise AssertionError("expected PromptNotFound")


class _FakeSpan:
    """Minimal stand-in for a spaCy entity span: terms.candidates only ever reads
    .text/.label_/.start_char/.end_char (see app/analysis/terms.py)."""

    def __init__(self, text, label, start, end):
        self.text = text
        self.label_ = label
        self.start_char = start
        self.end_char = end


class _FakeDoc:
    def __init__(self, ents):
        self.ents = ents


class _FakeNlp:
    """Minimal stand-in for a loaded spaCy pipeline: `nlp(text) -> object with .ents`. A person's
    name (a single capitalized word) is the case the SHAPES regexes in terms.py cannot reach at
    all — they need NER — so a real test of `named_terms` needs *some* nlp, real or fake. A fake
    is used rather than the real `en_core_web_sm` so this test does not depend on whether spaCy
    happens to be installed on the machine running the suite (app.main._analysis_nlp degrades to
    None when it isn't, and analyze_window must degrade the same way)."""
    NAMES = ("Federico", "Daniel")

    def __call__(self, text):
        ents = []
        for name in self.NAMES:
            i = text.find(name)
            if i >= 0:
                ents.append(_FakeSpan(name, "PERSON", i, i + len(name)))
        return _FakeDoc(ents)


def test_named_terms_are_populated_from_message_text():
    """`term` (published as inventory.named_terms) is the ONLY level in this whole package that
    reads message TEXT rather than tool-call inputs (see terms.py's module docstring) — a
    person's name is only ever spoken, never a tool argument, so every other level is
    structurally blind to it. Regression: workstreams.INVENTORY originally omitted it, so
    analyze_window ran spaCy over every message (the expensive part of the call) and then threw
    the result away."""
    with tempfile.TemporaryDirectory() as tmp:
        # Both mentions must land BEFORE the target prompt: the window is [start, end) and end
        # is the prompt's own timestamp (see test_window_ends_at_the_prompt_and_looks_back), so
        # the prompt's own text is excluded from what it summarizes.
        p = _write(tmp, [
            _turn("2026-08-01T10:00:00Z", text="Federico asked about the rollout timeline"),
            _turn("2026-08-01T10:03:00Z", text="let's loop in Daniel too"),
            _turn("2026-08-01T10:05:00Z", "target", "sounds good"),
        ])
        out = analyze_window(p, "target", span_minutes=60, nlp=_FakeNlp(),
                             store=_store(tmp))
        terms = {t["value"] for t in out["inventory"]["named_terms"]}
        assert {"Federico", "Daniel"} <= terms, terms


def test_named_terms_carry_only_value_and_count():
    """No span, no offset, no surrounding message text — the same wire-shape discipline
    test_match_endpoint_wire_shape_carries_no_span_offset_or_text already holds /match to."""
    with tempfile.TemporaryDirectory() as tmp:
        p = _write(tmp, [
            _turn("2026-08-01T10:00:00Z", text="Federico is on point here"),
            _turn("2026-08-01T10:05:00Z", "target", "thanks"),
        ])
        out = analyze_window(p, "target", span_minutes=60, nlp=_FakeNlp(),
                             store=_store(tmp))
        entries = out["inventory"]["named_terms"]
        assert entries, "expected at least one named term"
        for e in entries:
            assert set(e.keys()) == {"value", "n"}, e


def test_payload_carries_the_schema_and_no_prompt_text():
    with tempfile.TemporaryDirectory() as tmp:
        p = _write(tmp, [_turn("2026-08-01T10:00:00Z", "t", "secret customer name here")])
        out = analyze_window(p, "t", store=_store(tmp))
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
