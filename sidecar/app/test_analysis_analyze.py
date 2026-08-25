import sys, os, json, math, tempfile
from datetime import datetime
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from app.analysis import blocks
from app.analysis.analyze import analyze_window, PromptNotFound, _block_span
from app.analysis.ingest import session_of
from app.analysis.store import BIN_SECONDS, open_store


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


# --- the block containing the prompt ----------------------------------------------------------
#
# A window is an arbitrary hour; a BLOCK is a piece of work, bounded by the three terminators a
# pre-registered four-arm study over 496 sessions settled on (app/analysis/blocks.py). Until now
# that boundary existed only in the study. These tests hold the live path to two things: that it
# reports the block, and that reporting it changed NOTHING else.

# Every key /analyze answered with at schema 14, i.e. before `block` existed. Written out rather
# than derived from a second call, so a key ADDED by accident fails here instead of quietly
# matching itself.
KEYS_BEFORE_BLOCK = {"schema", "session", "window_start", "window_end", "evidence", "effort",
                     "workstreams", "inventory", "inventory_omitted"}


def _epoch(iso):
    return datetime.fromisoformat(iso).timestamp()


def _block_fixture(tmp):
    """A dense session whose target prompt lands MID-BIN — the ordinary case, since a window
    ending at a prompt's own instant essentially never lands on a 5-minute boundary."""
    return _write(tmp, [
        _turn("2026-08-01T10:00:30Z", "u0", "early work"),
        _turn("2026-08-01T10:03:00Z", "u1", "more work"),
        _turn("2026-08-01T10:07:00Z", "u2", "still going"),
        _turn("2026-08-01T10:12:00Z", "target", "the prompt"),
    ])


def test_analyze_reports_the_block_containing_the_prompt():
    """The live path cuts the session with the shipped cutter and names the block the prompt fell
    in. Opt-in for the same reason `sizer` and `prior` are: the parse-path oracle holds no store
    and cannot cut a session at all."""
    with tempfile.TemporaryDirectory() as tmp:
        p = _block_fixture(tmp)
        out = analyze_window(p, "target", span_minutes=60, store=_store(tmp), block=True)
        b = out["block"]
        assert b is not None, out
        assert b["start_reason"] in blocks.REASONS and b["end_reason"] in blocks.REASONS, b
        # The instant it claims to hold really is inside it, and the span is a real span.
        t = _epoch(out["window_end"])
        assert _epoch(b["start"]) <= t < _epoch(b["end"]), (b, out["window_end"])
        # ... and never longer than the measured cap, which is the whole property arm A' won on:
        # being a plain cap, its maximum block EQUALS its cap.
        assert _epoch(b["end"]) - _epoch(b["start"]) <= blocks.MAX_BLOCK_MINUTES * 60.0 + 1e-6, b


def test_the_block_carries_a_span_and_two_reasons_and_nothing_else():
    """SPAN AND REASONS ONLY. No evidence or attributability field — this repo holds two
    conflicting definitions of a block's thinness (pooled across the allocation levels versus
    `window.attribution`'s per-level gate) and they disagree, so publishing the pooled sum would
    overstate attributability on the wire. Neither ships.

    The keys `start`/`end` are asserted to be INSTANTS here because
    test_payload_carries_the_schema_and_no_prompt_text rejects a bare `"start":` key anywhere
    else in the payload — that guard is aimed at a leaked span object
    ({"text":.., "start":.., "end":..}, character offsets into prompt text), and a block's two
    ends are wall-clock times of the same kind `window_start`/`window_end` already publish."""
    with tempfile.TemporaryDirectory() as tmp:
        out = analyze_window(_block_fixture(tmp), "target", span_minutes=60,
                             store=_store(tmp), block=True)
        b = out["block"]
        assert set(b) == {"start", "end", "start_reason", "end_reason"}, b
        for k in ("start", "end"):
            assert _epoch(b[k]) > 0, b            # parses as an instant, not an offset
        # Nothing else in the payload gained a bare span-shaped key.
        rest = json.dumps({k: v for k, v in out.items() if k != "block"})
        for k in ("text", "span", "start", "end", "offset"):
            assert f'"{k}":' not in rest, k


def test_adding_the_block_changed_no_existing_analyze_field():
    """The regression that matters. The block is ADDITIVE: the window is still the 60 minutes
    ending at the prompt, and every field schema 14 answered with is byte-identical. A reviewer
    finding this task altering a published value should treat it as a Critical defect, so the
    check is dict equality against the same call with the block off — not a spot check."""
    with tempfile.TemporaryDirectory() as tmp:
        p = _block_fixture(tmp)
        st = _store(tmp)
        without = analyze_window(p, "target", span_minutes=60, store=st)
        with_block = analyze_window(p, "target", span_minutes=60, store=st, block=True)
        assert set(without) == KEYS_BEFORE_BLOCK, sorted(without)
        assert set(with_block) == KEYS_BEFORE_BLOCK | {"block"}, sorted(with_block)
        assert {k: v for k, v in with_block.items() if k != "block"} == without
        # And the look-back itself is untouched: 60 minutes ending at the prompt, stated as
        # literals so a change to the anchoring cannot pass by moving both sides together.
        assert without["window_end"] == "2026-08-01T10:12:00+00:00", without["window_end"]
        assert without["window_start"] == "2026-08-01T09:12:00+00:00", without["window_start"]
        # The block is NOT the window. If these ever coincided the test above would be vacuous.
        assert (with_block["block"]["start"], with_block["block"]["end"]) != (
            without["window_start"], without["window_end"]), with_block["block"]


def test_the_block_span_is_bin_aligned_so_no_leading_evidence_falls_outside_it():
    """`blocks.cut` requires a BIN-ALIGNED `from_ts` and does not say so: `active_segments`
    filters on bin STARTS, so a bin straddling `lo` is excluded from every segment while
    `rollup_window` still counts the evidence inside it — events at 100s/250s/400s cut from
    lo=150 put the 250s event in no block at all.

    A prompt's instant essentially never lands on a 5-minute boundary, so handing `cut` a raw
    prompt instant or a raw `window_start` would drop leading evidence on nearly every call and
    publish a block starting later than the work did. The span therefore comes from bin starts
    and is aligned explicitly; this pins both halves — the alignment, and the covering it buys."""
    with tempfile.TemporaryDirectory() as tmp:
        p = _block_fixture(tmp)
        st = _store(tmp)
        out = analyze_window(p, "target", span_minutes=60, store=st, block=True)
        t = _epoch(out["window_end"])
        assert t % BIN_SECONDS != 0, (
            "premise: the prompt must land mid-bin, or this asserts nothing")
        lo, hi = _block_span(st, session_of(p))
        assert lo % BIN_SECONDS == 0 and hi % BIN_SECONDS == 0, (lo, hi)
        # The prompt's OWN bin — the one holding the evidence closest to the window's end — lies
        # wholly inside the block, front edge included.
        own_bin = math.floor(t / BIN_SECONDS) * BIN_SECONDS
        b = out["block"]
        assert _epoch(b["start"]) <= own_bin, (b, own_bin)
        # And no active bin of the session escapes the cut the payload was read off.
        bl = blocks.cut(st, session_of(p), lo, hi)
        for bin_ts in blocks.active_bins(st, session_of(p)):
            assert len([x for x in bl if x.start <= bin_ts < x.end]) == 1, (bin_ts, bl)


def test_a_prompt_that_falls_in_dead_air_is_in_no_block():
    """`null` is a REAL ANSWER. Blocks tile a session's ACTIVE part and not the session — tiling
    the silence was measured to cost 65 points of attribution — so a prompt landing in a gap is
    in no block, and saying so is not a failure. Here the prompt arrives an hour after the last
    work and contributes no reference event of its own, which is exactly that shape."""
    with tempfile.TemporaryDirectory() as tmp:
        p = _write(tmp, [
            _turn("2026-08-01T10:00:00Z", "u0", "work"),
            _turn("2026-08-01T10:03:00Z", "u1", "more work"),
            # No cwd: nothing to attribute, so this turn opens no bin of its own.
            {"type": "user", "timestamp": "2026-08-01T11:00:00Z", "uuid": "target",
             "message": {"content": [{"type": "text", "text": "back again"}]}},
        ])
        st = _store(tmp)
        out = analyze_window(p, "target", span_minutes=60, store=st, block=True)
        assert "block" in out, sorted(out)
        assert out["block"] is None, out["block"]
        # The premise: there WAS a session to cut, it just does not reach the prompt.
        assert blocks.cut(st, session_of(p), *_block_span(st, session_of(p))), "empty fixture"


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
