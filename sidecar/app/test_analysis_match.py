"""Configured-vocabulary matching. Every case is a behaviour the design spec binds this pass to
(docs/superpowers/specs/2026-08-22-configured-vocabulary-matching-design.md); see that file for
the measurement each one is answering.
"""
import sys, os
sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
from app.analysis.match import compile_vocabulary, match_text, DEFAULT_BUDGET_S


def test_a_present_name_matches_an_absent_one_produces_no_key():
    vocab, rejects = compile_vocabulary({
        "customer": [{"id": "acme", "match": ["ACME"]},
                     {"id": "northwind", "match": ["Northwind"]}]})
    assert rejects == []
    got = match_text("Customer is ACME, three steps remain.", vocab)
    assert got["customer"]["value"] == "acme"
    # A key present in the vocabulary but never mentioned must be ABSENT, not an empty value.
    absent = match_text("no company named here at all", vocab)
    assert "customer" not in absent, absent


def test_word_boundary_acme_does_not_match_acmesoft():
    vocab, _ = compile_vocabulary({"customer": [{"id": "acme", "match": ["ACME"]}]})
    assert "customer" not in match_text("we evaluated acmesoft last quarter", vocab)
    assert "customer" not in match_text("the vendor was myacme inc", vocab)


def test_case_insensitive():
    vocab, _ = compile_vocabulary({"customer": [{"id": "acme", "match": ["ACME"]}]})
    assert match_text("the customer is acme", vocab)["customer"]["value"] == "acme"


def test_multiword_term_matches_whole_after_whitespace_collapse():
    """A name broken across a line by the transcript's own wrapping must still match — the
    spec's explicit reason for collapsing whitespace before matching."""
    vocab, _ = compile_vocabulary({"product": [{"id": "magenta", "match": ["Magenta Model"]}]})
    wrapped = "we are evaluating Magenta \n  Model against the baseline"
    assert match_text(wrapped, vocab)["product"]["value"] == "magenta"


def test_longest_match_wins_across_labels():
    """Magenta model beats Magenta — the spec's own example, and a case that only resolves
    correctly if overlap resolution operates across DIFFERENT labels' spans, not just within one
    label's alternatives."""
    vocab, _ = compile_vocabulary({
        "product": [{"id": "magenta_model", "match": ["Magenta model"]},
                    {"id": "magenta", "match": ["Magenta"]}]})
    got = match_text("Magenta model is the one under evaluation.", vocab)["product"]
    assert got["value"] == "magenta_model"
    assert got["alternates"] == []


def test_literal_terms_with_regex_metacharacters_match_literally():
    """The literal path must `re.escape` terms — a term like 'C++' or 'Together.ai' would silently
    misbehave (or, for 'C++', fail to compile at all: a dangling quantifier) if treated as a raw
    pattern instead of an escaped literal."""
    vocab, rejects = compile_vocabulary({"lang": [{"id": "cpp", "match": ["C++"]}]})
    assert rejects == []
    assert match_text("we rewrote it in C++ last year", vocab)["lang"]["value"] == "cpp"
    assert "lang" not in match_text("no match here", vocab)


def test_never_truncated_a_term_matches_whole_or_not_at_all():
    vocab, _ = compile_vocabulary({"customer": [{"id": "acme", "match": ["Acme Corp"]}]})
    assert "customer" not in match_text("Acme Corporation signed today", vocab)
    assert match_text("Acme Corp signed today", vocab)["customer"]["value"] == "acme"


def test_no_token_budget_full_text_is_scanned():
    """/classify truncates around a ~768-token ceiling (enrich/lenstat); this pass must not. A
    mention placed well past that many characters in must still be found."""
    vocab, _ = compile_vocabulary({"customer": [{"id": "acme", "match": ["ACME"]}]})
    padding = "word " * 2000  # ~10000 chars, far past any /classify token ceiling
    text = padding + "the customer is ACME"
    got = match_text(text, vocab)
    assert got["customer"]["value"] == "acme"


def test_module_does_not_import_the_classify_token_cap():
    """The 768-token ceiling that governs /classify (lenstat/max_len) must not leak into this
    pass as CODE — there is nothing here for it to bound (see module docstring). `lenstat` and
    `max_len` are named in PROSE (comments/docstrings explaining why the cap does not apply);
    walking the AST rather than grepping the source is what lets this test tell that apart from
    an actual import or a live identifier, instead of tripping on its own commentary."""
    import ast
    path = os.path.join(os.path.dirname(__file__), "analysis", "match.py")
    tree = ast.parse(open(path).read(), filename=path)
    names = set()
    for node in ast.walk(tree):
        if isinstance(node, ast.Import):
            names.update(a.name for a in node.names)
        elif isinstance(node, ast.ImportFrom):
            names.add(node.module or "")
            names.update(a.name for a in node.names)
        elif isinstance(node, ast.Name):
            names.add(node.id)
        elif isinstance(node, ast.keyword) and node.arg:
            names.add(node.arg)
    for forbidden in ("lenstat", "max_len", "MAX_CHARS"):
        assert not any(forbidden in n for n in names), forbidden


def test_tie_on_count_emits_ambiguous_not_an_arbitrary_pick():
    """Two labels of one key, tied at the top: 'ambiguous', never a silent pick — the failure
    mode this study has hit roughly twenty times (window.dominant makes the same call)."""
    vocab, _ = compile_vocabulary({
        "team": [{"id": "engineering", "match": ["Engineering"]},
                 {"id": "operations", "match": ["Operations"]}]})
    got = match_text("Engineering and Operations were both in the room.", vocab)["team"]
    assert got["value"] == "ambiguous"
    assert got["count"] == 1
    assert {"id": "operations", "count": 1} in got["alternates"] \
        or {"id": "engineering", "count": 1} in got["alternates"]


def test_single_value_per_key_most_mentions_wins_others_become_alternates():
    vocab, _ = compile_vocabulary({
        "team": [{"id": "engineering", "match": ["Engineering"]},
                 {"id": "operations", "match": ["Operations"]}]})
    text = "Engineering, Engineering and Engineering again, versus Operations once."
    got = match_text(text, vocab)["team"]
    assert got["value"] == "engineering"
    assert got["count"] == 3
    assert got["alternates"] == [{"id": "operations", "count": 1}]


def test_uncompilable_regex_is_rejected_once_and_other_labels_still_work():
    vocab, rejects = compile_vocabulary({
        "customer": [{"id": "acme", "match": ["ACME"]},
                     {"id": "broken", "regex": "("}]})
    assert rejects == [{"key": "customer", "id": "broken", "reason": "bad_regex"}]
    assert "broken" not in [e["id"] for e in vocab["customer"]]
    assert match_text("customer is ACME", vocab)["customer"]["value"] == "acme"


def test_nested_quantifier_regex_is_rejected_at_load_time():
    """The textbook catastrophic shape is refused before it is ever run against text."""
    vocab, rejects = compile_vocabulary({"x": [{"id": "bad", "regex": "(a+)+$"}]})
    assert rejects == [{"key": "x", "id": "bad", "reason": "nested_quantifier"}]
    assert vocab == {}


def test_label_with_neither_match_nor_regex_is_rejected():
    vocab, rejects = compile_vocabulary({"x": [{"id": "empty"}]})
    assert rejects == [{"key": "x", "id": "empty", "reason": "no_patterns"}]
    assert vocab == {}


def test_a_previously_pathological_pattern_now_returns_within_budget():
    """`(a|aa)*b` is the alternation-based shape with no nested quantifier — it has no `[+*]`
    inside its group, so it slips past the static `_looks_catastrophic` filter, and it is exactly
    the shape that hangs stdlib `re` (measured separately: the related `(a|a)*c` against 32 'a's
    ran past two minutes at 100% CPU under `re` with no way to interrupt it short of killing the
    process). Under this module's raw-pattern engine (`regex`, not `re`) the same shape returns
    near-instantly — this is the practical fix the M1 review's finding produced, not just a
    changed docstring. Input kept small so a REGRESSION back to `re`-like behaviour would fail
    this test fast rather than wedging the suite."""
    import time
    vocab, rejects = compile_vocabulary({"x": [{"id": "evil", "regex": "(a|aa)*b"}]})
    assert rejects == []  # confirms it truly bypassed the static filter
    text = "a" * 40  # never followed by 'b': forces exhaustive backtracking under a naive engine
    t0 = time.perf_counter()
    got = match_text(text, vocab, budget_s=0.5)
    elapsed = time.perf_counter() - t0
    assert "x" not in got, got
    assert elapsed < 2.0, elapsed  # generous margin; typically sub-millisecond
    assert vocab["x"][0]["poisoned"] is False  # never even approached the budget


def test_a_pattern_that_exceeds_the_wall_clock_budget_is_disabled():
    """Exercises the actual `regex.TimeoutError` -> poison -> absent path deterministically,
    without depending on machine-specific backtracking timing (which the previous version of
    this test did, and which a faster CPU or a `regex`-module upgrade could silently make flaky).
    A trivial, entirely non-pathological pattern against a large text and a near-zero budget is
    enough to force `regex`'s real timeout to fire — proving the WIRING (catch -> poison ->
    degrade to absent) works, independent of whatever timing any particular pattern happens to
    have on this machine."""
    vocab, rejects = compile_vocabulary({"x": [{"id": "slow", "regex": "a"}]})
    assert rejects == []
    entry = vocab["x"][0]
    text = "b" * 5_000_000 + "a"
    got = match_text(text, vocab, budget_s=1e-9)
    assert "x" not in got, got
    assert entry["poisoned"] is True
    # The trip wire holds: a second call skips the pattern entirely rather than re-attempting
    # (and re-paying for) a pattern already proven to blow its budget.
    got2 = match_text(text, vocab, budget_s=1e-9)
    assert "x" not in got2, got2


def test_default_budget_is_generous_for_ordinary_patterns():
    """A normal regex over ordinary text must not be mistaken for pathological."""
    vocab, _ = compile_vocabulary({"customer": [{"id": "acme", "regex": r"ACME|Acme Corp"}]})
    got = match_text("the customer is ACME", vocab, budget_s=DEFAULT_BUDGET_S)
    assert got["customer"]["value"] == "acme"


def test_output_shape_carries_no_span_offset_or_prompt_text():
    """The privacy invariant asserted on the wire shape, not left as an implementation detail:
    only id/count/confidence/alternates may appear — never a span, an offset, or the source
    text itself."""
    vocab, _ = compile_vocabulary({
        "customer": [{"id": "acme", "match": ["ACME"]},
                     {"id": "northwind", "match": ["Northwind"]}]})
    secret_text = "Customer is ACME, and Northwind was the runner-up, per the RFP."
    got = match_text(secret_text, vocab)
    entry = got["customer"]
    assert set(entry.keys()) == {"value", "confidence", "count", "alternates"}
    assert entry["confidence"] == 1.0
    assert isinstance(entry["value"], str) and isinstance(entry["count"], int)
    for alt in entry["alternates"]:
        assert set(alt.keys()) == {"id", "count"}
    blob = repr(got)
    assert "span" not in blob and "offset" not in blob
    assert secret_text not in blob and "RFP" not in blob


def test_empty_text_and_empty_vocabulary_produce_no_matches():
    vocab, _ = compile_vocabulary({"customer": [{"id": "acme", "match": ["ACME"]}]})
    assert match_text("", vocab) == {}
    assert match_text("customer is ACME", {}) == {}


if __name__ == "__main__":
    fns = [v for k, v in sorted(globals().items()) if k.startswith("test_")]
    for fn in fns:
        fn(); print(f"PASS {fn.__name__}")
    print(f"\n{len(fns)} passed")
