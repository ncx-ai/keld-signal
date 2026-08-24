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


def _tool(ts, blocks, cwd="/workspace/proj"):
    return {"type": "assistant", "timestamp": ts, "cwd": cwd,
            "message": {"content": blocks}}


def _use(name, tid, **inp):
    return {"type": "tool_use", "name": name, "id": tid, "input": inp}


# The tool names that map to ten DISTINCT acts, and the shell programs that add six more —
# sixteen in one window, above the fixed top-12 every other inventory dimension is cut at.
_ACT_TOOLS = [("Read", {"file_path": "a.go"}), ("Grep", {"pattern": "x"}),
              ("Edit", {"file_path": "b.go"}), ("Write", {"file_path": "c.go"}),
              ("Agent", {"subagent_type": "Explore"}), ("AskUserQuestion", {}),
              ("SendUserFile", {}), ("Artifact", {}), ("WebFetch", {"url": "https://x.test/a"}),
              ("Skill", {"skill": "dataviz"})]
_ACT_CMDS = ["pytest tests", "make build", "pip install requests", "git log",
             "docker ps", "psql -c 'select 1'"]
# Programs beyond the six above, so `exe` clears twelve distinct values in the same window and
# its own cut is demonstrated rather than assumed.
_EXTRA_CMDS = ["cat a.txt", "grep -n x a.txt", "curl https://x.test/b", "pandoc a.md -o a.pdf",
               "cp a.txt b.txt", "python3 s.py", "sqlite3 d.db .tables", "jq . a.json",
               "tar -cf a.tar a.txt", "wc -l a.txt"]


def _busy_window(tmp):
    rows = []
    blocks = [_use(n, f"t{i}", **inp) for i, (n, inp) in enumerate(_ACT_TOOLS)]
    blocks += [_use("Bash", f"b{i}", command=c)
               for i, c in enumerate(_ACT_CMDS + _EXTRA_CMDS)]
    rows.append(_tool("2026-08-01T10:00:00Z", blocks))
    rows.append(_turn("2026-08-01T10:30:00Z", "target", "what did I just do"))
    return _write(tmp, rows)


def test_physical_acts_publish_the_action_level():
    """`action` was extracted, stored, and fed to dynamics for the whole life of this package and
    published NOWHERE — measured: zero occurrences in workstreams.py. It is an INVENTORY
    dimension, not an ALLOCATION one: 97.8% coverage but a top share of only p50 0.403 over 22
    values, p50 7 distinct per window, and no floor recovers it (0.612 even at 0.30). That is
    `named_terms`' profile, and the same resolution applies — "what was done", not "what owns the
    hour". Results: ~/keld/refseries-context/act-artifact/RESULTS.md."""
    with tempfile.TemporaryDirectory() as tmp:
        out = analyze_window(_busy_window(tmp), "target", span_minutes=60, store=_store(tmp))
        acts = {a["value"]: a["n"] for a in out["inventory"]["physical_acts"]}
        assert acts, out["inventory"]
        # A hour of real work reads AND edits AND tests — the plurality that refuted it as an
        # allocation dimension is exactly what makes it worth publishing as an inventory one.
        assert {"read", "edit", "create", "search", "test", "build"} <= set(acts), sorted(acts)
        assert all(n >= 1 for n in acts.values()), acts
        for a in out["inventory"]["physical_acts"]:
            assert set(a.keys()) == {"value", "n"}, a


def test_physical_acts_are_published_whole_because_the_vocabulary_is_closed():
    """No top-N cut, and that is a DECISION, not an omission. Every other inventory dimension
    takes a fixed top-12 whose boundary is resolved by an unrepresented tie (114/572 windows
    disagree on `programs`' published set for exactly that reason). `action` cannot need it: the
    vocabulary is closed at len(ACTIONS) == 22 by construction, and windows carry p90 12 / max 16
    distinct — so a 12-cut would buy nothing and would silently drop acts from the ~10% of
    windows above it, by arbitrary tie-break. `programs` is asserted alongside so this test
    cannot pass by the cut having been removed for everyone."""
    from app.analysis.vocab import ACTIONS
    with tempfile.TemporaryDirectory() as tmp:
        out = analyze_window(_busy_window(tmp), "target", span_minutes=60, store=_store(tmp))
        acts = [a["value"] for a in out["inventory"]["physical_acts"]]
        assert len(acts) > 12, f"premise: the window must exceed the cut to test it, got {acts}"
        assert set(acts) <= set(ACTIONS), sorted(set(acts) - set(ACTIONS))
        assert len(out["inventory"]["programs"]) == 12, (
            "premise: `programs` must still be cut, or this asserts nothing about `action`: "
            f"{out['inventory']['programs']}")


def test_physical_acts_never_come_from_message_text():
    """The privacy question, answered in code rather than assumed. `named_terms` needs a
    vocabulary filter before it can leave the device because `term` reads message TEXT; `action`
    is added in levels.py at exactly two places, both inside the tool_use branch — from a tool
    NAME and from a shell command's argv, each through vocab.action_for, whose every return is a
    closed-vocabulary literal. So prose that merely NAMES acts contributes none."""
    with tempfile.TemporaryDirectory() as tmp:
        p = _write(tmp, [
            _turn("2026-08-01T10:00:00Z",
                  text="Federico asked us to read the file, edit it, then commit and test"),
            _turn("2026-08-01T10:05:00Z", "target", "go"),
        ])
        # The spaCy stand-in is wired ON PURPOSE, so the text-derived level is genuinely
        # populated in this window. Without it `physical_acts` would come back empty because
        # nothing was extracted at all, and the assertion would hold for a dimension pointed at
        # `term` just as well as for one pointed at `action` — i.e. it would test nothing.
        out = analyze_window(p, "target", span_minutes=60, nlp=_FakeNlp(), store=_store(tmp))
        assert out["inventory"]["named_terms"], (
            "premise: the text-derived level must be populated, or this asserts nothing")
        assert out["inventory"]["physical_acts"] == [], out["inventory"]["physical_acts"]


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
