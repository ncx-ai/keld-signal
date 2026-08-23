"""Configured-vocabulary matching: scan resolved prompt text for terms an org has already
declared, and return the matched id — never a span of the text, never an offset, never the text
itself. See docs/superpowers/specs/2026-08-22-configured-vocabulary-matching-design.md.

Two label shapes, one deliberately preferred:

* `match: [...]` — a list of LITERAL terms. This is the RECOMMENDED default: each term is
  `re.escape`d before compiling, so there is no user-authored regex and therefore no
  backtracking surface at all for this path. An admin typing a customer name gets this by
  default and never needs to think about regex safety.
* `regex` — a raw, org-authored pattern. Accepted because it already exists in the wire contract
  (`settings.RemoteLabel.Regex`) and predates this module, not because it is preferred. Unlike
  Go's RE2, Python's `re` backtracks, so an org-supplied pattern here is a real
  denial-of-service surface that the literal path does not have. See `compile_vocabulary` and
  `_match_raw` for what is done about that, and read the honesty note in `_match_raw` before
  trusting the word "budget".

Pure functions over data: no I/O, no FastAPI, no settings polling, no import from `scripts/`, no
pandas (see `app/analysis/__init__.py`). A later task wires `POST /vocabulary` and `POST /match`
around this.

No token cap. `/classify` truncates to a `max_len` because GLiNER2 is a transformer whose memory
scales with sequence length (`enrich/lenstat`). Nothing here is a transformer: matching is a
regex scan, its cost is linear in text length, and the full resolved text is always scanned. Do
not import a token/char cap into this module for consistency with `/classify` — there is nothing
to be consistent about; the constraint that motivates one does not exist for the other.
"""
import re
import time
import collections

# A key with no match must produce no entry at all (tested in test_analysis_match.py), so callers
# can tell "checked, found nothing" (absent key) apart from "found something with zero evidence"
# (which cannot happen) without a sentinel value.

# The wall-clock budget below cannot preempt an in-flight `re` match (see `_match_raw`'s
# docstring) — it only bounds how quickly a pathological label gets disabled AFTER a slow call
# finally returns. 100ms is generous for any legitimate pattern over realistic prompt sizes (a
# non-pathological regex over a few KB of text runs in microseconds), so anything crossing it is
# already behaving like a pathological one, not a slow-but-fine one.
DEFAULT_BUDGET_S = 0.1


# Catches the textbook catastrophic-backtracking SHAPE: a sub-pattern that can already repeat,
# sitting inside a group that is itself repeated — (a+)+, (a*)+, (a+)*, (.*)+, (x+){2,}. This is a
# STATIC, SYNTACTIC check on the pattern source, not a safety proof: it is a substring search, not
# a parser, so it also fires inside deeper nesting (`((a+)+)+` contains the literal substring
# `(a+)+`) and it can flag a benign pattern that happens to share the shape. That false-positive
# cost is intentional — see the module docstring: the literal `match` path is the one without this
# trade-off at all, and it is the recommended default for exactly this reason.
#
# It does NOT catch every catastrophic shape. Alternation-based blowup with no nested quantifier
# at all — `(a|a)+`, `(a|ab)*` — has no `[+*]` inside the group and slips past this check
# entirely. Measured on this machine: `(a|a)*c` against 26 'a's took 2.8s; against 32 'a's it did
# not return within two minutes at 100% CPU, and the curve does not level off. That gap is exactly
# why `_match_raw` also carries a runtime budget below — imperfect, but real defense in depth.
_NESTED_QUANTIFIER = re.compile(r"\([^()]*[+*][^()]*\)(?:[+*]|\{\d*,\d*\})")


def _collapse(s):
    """Whitespace collapsed to single spaces, so a name broken across a line still matches a
    multi-word term, and so a term itself (which may have been typed with irregular spacing) is
    compared on the same footing as the text it is matched against."""
    return " ".join(s.split())


def _looks_catastrophic(pattern_src):
    return bool(_NESTED_QUANTIFIER.search(pattern_src))


def _compile_literal(terms):
    """One case-insensitive pattern matching any of `terms` whole, or None if `terms` is empty.

    Anchored with lookaround, not `\\b`: `\\b` requires a WORD character adjacent to the boundary,
    which is wrong for a term that starts or ends on punctuation (an org name like "Acme Corp."
    ends on a literal dot — `\\b` there would need the FOLLOWING character to be a word character,
    which at end-of-string it is not, so a trailing-period term could fail to anchor at all).
    `(?<!\\w)` / `(?!\\w)` require only the ABSENCE of an adjoining word character, which is the
    actual "whole word" rule and holds regardless of what the term itself starts or ends with.

    Sorted longest-first: when two terms both match starting at the same position (`Magenta
    model` and `Magenta` both start at "Magenta"), Python's alternation tries alternatives in
    source order and returns the first success — so ordering the alternatives longest-first is
    what makes "longest wins" a property of the compiled pattern itself, not a fact deduced later.
    Cross-LABEL overlap (two different labels, not just two terms of one label) is resolved
    afterward in `_resolve_overlaps`, because that case spans compiled patterns.
    """
    cleaned = sorted({_collapse(t) for t in terms if _collapse(t)}, key=len, reverse=True)
    if not cleaned:
        return None
    alt = "|".join(re.escape(t) for t in cleaned)
    return re.compile(r"(?<!\w)(?:" + alt + r")(?!\w)", re.IGNORECASE)


def compile_vocabulary(raw):
    """Compile an org's declared vocabulary into matchers.

    `raw` is `{key: [label, ...]}`. Each `label` is a dict with:
      - `id` (required): the identifier published on a match.
      - `match` (optional): a list of literal terms — the RECOMMENDED path (see module docstring).
      - `regex` (optional): a raw pattern — accepted for wire compatibility, not recommended.
    A label needs at least one of `match`/`regex` to do anything.

    Returns `(compiled, rejects)`:
      - `compiled` is `{key: [entry, ...]}` where `entry` is a plain dict
        `{"id", "literal", "raw", "poisoned"}` — `literal`/`raw` are compiled patterns or None,
        `poisoned` starts False and is flipped by `match_text` (see `_match_raw`). Only labels
        that produced at least one usable matcher are included; a key left with no labels is
        omitted entirely, matching "a key with no match produces no entry" one level up.
      - `rejects` is `[{"key", "id", "reason"}, ...]` for anything dropped — `"no_patterns"`
        (neither field given), `"bad_regex"` (failed to compile), `"nested_quantifier"` (matched
        the static shape above) — so a caller can report it once per distinct reject set, the way
        `rejectReporter` already does Go-side for custom passes, rather than staying silent.
    """
    compiled = {}
    rejects = []
    for key, labels in raw.items():
        entries = []
        for lb in labels or []:
            lid = lb.get("id")
            if not lid:
                rejects.append({"key": key, "id": lb.get("id", ""), "reason": "missing_id"})
                continue
            terms = lb.get("match") or []
            src = lb.get("regex") or ""
            literal = _compile_literal(terms)
            raw_pat = None
            if src:
                if _looks_catastrophic(src):
                    rejects.append({"key": key, "id": lid, "reason": "nested_quantifier"})
                else:
                    try:
                        raw_pat = re.compile(src, re.IGNORECASE)
                    except re.error:
                        rejects.append({"key": key, "id": lid, "reason": "bad_regex"})
            if literal is None and raw_pat is None:
                if not terms and not src:
                    rejects.append({"key": key, "id": lid, "reason": "no_patterns"})
                continue
            entries.append({"id": lid, "literal": literal, "raw": raw_pat, "poisoned": False})
        if entries:
            compiled[key] = entries
    return compiled, rejects


def _match_raw(entry, text, budget_s, now):
    """Spans found by `entry`'s raw regex, or `[]` if it has none / is poisoned / blows budget.

    Be honest about what this budget is: Python's `re` engine cannot be interrupted mid-match —
    there is no cooperative check-in point a pure-Python wrapper can hook, and a naive
    `signal.alarm` or thread-timeout does not stop a C-level backtracking loop already in flight
    (confirmed empirically: killing the process was the only way to stop a 32-char adversarial
    match against `(a|a)*c` that had already run past two minutes at 100% CPU). So this is NOT a
    preemptive limit — the FIRST call to a pathological pattern still pays its real cost, however
    long that is, before this function ever gets to look at the clock.

    What it IS: a post-hoc trip wire. Once a call is measured to have taken longer than
    `budget_s`, `entry["poisoned"]` is set and every SUBSEQUENT call skips this pattern without
    even attempting it — turning "hangs on every request forever" into "hangs once, then never
    again for the life of this process". That is a real, if partial, guarantee: combined with the
    static nested-quantifier rejection at compile time, it is the strongest honest mechanism
    available without adding a hard-kill (subprocess-per-match) mechanism, which is out of scope
    for a pure function over data. It is not a bound on worst-case latency for the first hit, and
    this docstring exists so nobody downstream mistakes it for one.

    The over-budget call's own result is discarded, not returned — "degrades to absent" per the
    spec, so a label that just proved itself dangerous does not get to publish from the very call
    that proved it.
    """
    if entry["raw"] is None or entry["poisoned"]:
        return []
    t0 = now()
    found = [m.span() for m in entry["raw"].finditer(text)]
    if now() - t0 > budget_s:
        entry["poisoned"] = True
        return []
    return found


def _label_spans(entry, text, budget_s, now):
    spans = []
    if entry["literal"] is not None:
        spans += [m.span() for m in entry["literal"].finditer(text)]
    spans += _match_raw(entry, text, budget_s, now)
    return spans


def _resolve_overlaps(spans_by_id):
    """Collapse overlapping spans across labels of one key to a per-id mention count.

    Classic longest-match-first interval scheduling: sort every span (from every label) by
    length descending, then greedily keep a span only if it does not overlap one already kept.
    This is what makes "longest wins" hold across DIFFERENT labels' patterns (`Magenta model`
    from one label beating `Magenta` from another), not just between terms of the same label
    (which `_compile_literal`'s alternative ordering already handles) — a shorter span that
    merely overlaps a longer, already-kept one is dropped outright, not counted as a mention for
    its own label at all.
    """
    all_spans = sorted(
        ((start, end, lid) for lid, spans in spans_by_id.items() for start, end in spans),
        key=lambda s: (-(s[1] - s[0]), s[0]))
    kept = []
    counts = collections.Counter()
    for start, end, lid in all_spans:
        if any(start < k_end and end > k_start for k_start, k_end in kept):
            continue
        kept.append((start, end))
        counts[lid] += 1
    return counts


def match_text(text, compiled, budget_s=DEFAULT_BUDGET_S, now=time.monotonic):
    """Match resolved prompt text against a compiled vocabulary (`compile_vocabulary`'s output).

    Returns `{key: {"value", "confidence", "count", "alternates"}}`. A key with no match is
    ABSENT from the dict — never present with an empty or zero value — so a caller can tell
    "nothing matched" apart from "this key wasn't even in scope" only by key presence, matching
    the rest of this package's convention (see `window.dominant`, `workstreams.payload`).

    Confidence is always `1.0`, meaning the string was PRESENT, not that it is the right answer —
    this pass makes no claim about correctness, only about occurrence.

    Multi-label breaks cost allocation: if several labels of one key match, the one with the most
    mentions wins as `value`; the rest are listed under `alternates` (each `{"id", "count"}`),
    never silently dropped. A TIE at the top emits `value == "ambiguous"` rather than an arbitrary
    pick — a silently chosen winner among near-equals is the plausible-wrong-number failure this
    study has hit roughly twenty times (see `window.dominant`, which makes the same choice by
    returning `None` on a tie).

    No span, no offset, no source text appears anywhere in the return value — that is the privacy
    invariant this pass exists to uphold (publish identity, never content), asserted on the wire
    shape in test_analysis_match.py rather than left as an implementation detail.
    """
    if not text or not compiled:
        return {}
    text = _collapse(text)
    out = {}
    for key, entries in compiled.items():
        spans_by_id = {e["id"]: _label_spans(e, text, budget_s, now) for e in entries}
        counts = _resolve_overlaps(spans_by_id)
        if not counts:
            continue
        ranked = sorted(counts.items(), key=lambda kv: (-kv[1], kv[0]))
        top_id, top_n = ranked[0]
        tied = len(ranked) > 1 and ranked[1][1] == top_n
        out[key] = {
            "value": "ambiguous" if tied else top_id,
            "confidence": 1.0,
            "count": top_n,
            "alternates": [{"id": lid, "count": n} for lid, n in ranked[1:]],
        }
    return out
