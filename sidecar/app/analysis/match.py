"""Configured-vocabulary matching: scan resolved prompt text for terms an org has already
declared, and return the matched id — never a span of the text, never an offset, never the text
itself. See docs/superpowers/specs/2026-08-22-configured-vocabulary-matching-design.md.

Two label shapes, one deliberately preferred:

* `match: [...]` — a list of LITERAL terms. This is the RECOMMENDED default: each term is
  `re.escape`d before compiling, so there is no user-authored regex and therefore no
  backtracking surface at all for this path. An admin typing a customer name gets this by
  default and never needs to think about regex safety. Compiled and matched with stdlib `re` —
  it needs no timeout because there is nothing for it to time out on.
* `regex` — a raw, org-authored pattern. Accepted because it already exists in the wire contract
  (`settings.RemoteLabel.Regex`) and predates this module, not because it is preferred. Compiled
  and matched with the third-party `regex` module instead of stdlib `re`, specifically for its
  `timeout=` support (`_match_raw`'s docstring says exactly what that does and does not
  guarantee — read it before trusting the word "budget").

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
import collections

import regex  # third-party (mrab-regex): imported by name for its timeout= support on
              # finditer/search — see _match_raw. Already a transitive dependency of transformers
              # and wordfreq, but pinned explicitly in requirements.txt because importing it
              # directly and relying on it arriving transitively are two different commitments;
              # the latter is the exact trap `numpy` arriving only via `pandas` is (AGENTS.md).

# A key with no match must produce no entry at all (tested in test_analysis_match.py), so callers
# can tell "checked, found nothing" (absent key) apart from "found something with zero evidence"
# (which cannot happen) without a sentinel value.

# `regex.finditer(..., timeout=budget_s)` bounds a SINGLE match call from inside the C matching
# loop — a real interrupt point stdlib `re` structurally lacks (see _match_raw). 100ms is generous
# for any legitimate pattern over realistic prompt sizes (a non-pathological regex over a few KB
# of text runs in microseconds), so anything crossing it is already behaving like a pathological
# one, not a slow-but-fine one.
DEFAULT_BUDGET_S = 0.1


# Catches the textbook catastrophic-backtracking SHAPE: a sub-pattern that can already repeat,
# sitting inside a group that is itself repeated — (a+)+, (a*)+, (a+)*, (.*)+, (x+){2,}. This is a
# STATIC, SYNTACTIC check on the pattern source, not a safety proof: it is a substring search, not
# a parser, so it also fires inside deeper nesting (`((a+)+)+` contains the literal substring
# `(a+)+`) and it can flag a benign pattern that happens to share the shape. That false-positive
# cost is intentional — see the module docstring: the literal `match` path is the one without this
# trade-off at all, and it is the recommended default for exactly this reason.
#
# Kept as DEFENCE IN DEPTH now that raw patterns run under `regex`'s real `timeout=` (below), not
# because it is the primary safeguard anymore — a pattern this catches is refused before it is
# ever run at all, which is strictly better than a bounded-but-nonzero-cost timeout on every call.
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

    Sorted longest-first, so that when two terms of the SAME label both match at the same start
    position, the alternative that wins the internal alternation is the longer one — Python tries
    alternatives in source order and returns the first success. NOTE: this is not what makes
    "longest wins" a *visible* behaviour today — two overlapping terms of one label already
    resolve to the same id and the same count either way, so nothing currently distinguishes the
    sorted from the unsorted order (confirmed by mutation: removing this sort passes every
    existing test). What `_resolve_overlaps` does across DIFFERENT labels is what the tested
    "longest wins" behaviour actually depends on. This sort is kept anyway because the span it
    picks does feed `_resolve_overlaps` (a longer captured span here can still beat a competing
    shorter span from another label), so it is correctness for an untested cross-label
    interaction, not dead code — not a claim that it is load-bearing for anything observed yet.
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
                        raw_pat = regex.compile(src, regex.IGNORECASE)
                    except regex.error:
                        rejects.append({"key": key, "id": lid, "reason": "bad_regex"})
            if literal is None and raw_pat is None:
                if not terms and not src:
                    rejects.append({"key": key, "id": lid, "reason": "no_patterns"})
                continue
            entries.append({"id": lid, "literal": literal, "raw": raw_pat, "poisoned": False})
        if entries:
            compiled[key] = entries
    return compiled, rejects


def _match_raw(entry, text, budget_s):
    """Spans found by `entry`'s raw regex, or `[]` if it has none / is poisoned / blows budget.

    Be honest about what this budget now is. `regex.finditer(..., timeout=budget_s)` checks
    elapsed wall-clock time from INSIDE the C matching loop — the cooperative check-in point
    stdlib `re` structurally lacks, which is the whole reason this module imports `regex` for the
    raw-pattern path instead of using the same engine as the literal path. Measured: stdlib `re`
    against `(a|a)*c`/32 'a's ran past two minutes at 100% CPU with no way to interrupt it short
    of killing the process; the SAME shape (`(a|aa)*b`) under `regex` with `timeout=0.5` raised
    `TimeoutError` and returned control well inside the budget. That is a real, preemptive bound
    on a single call — a genuine improvement over the previous post-hoc-only design, not a
    documentation change.

    What it still does NOT do: make an org-supplied pattern safe in general, or bound total time
    across a request with many raw-regex labels (each label gets its own `budget_s` allowance, so
    N pathological labels can still cost N x budget_s). It is a per-call ceiling, not a system-wide
    one — the static nested-quantifier rejection at compile time remains the cheaper defence
    where it applies (refused before it ever runs, at zero cost, rather than bounded at some cost
    on every call).

    The trip wire is kept on top of the real timeout, not instead of it: a pattern that repeatedly
    burns its full `budget_s` on every request is still wasteful even though each individual call
    is now bounded, so the first `TimeoutError` sets `entry["poisoned"]` and every SUBSEQUENT call
    skips this pattern without even attempting it.
    """
    if entry["raw"] is None or entry["poisoned"]:
        return []
    try:
        return [m.span() for m in entry["raw"].finditer(text, timeout=budget_s)]
    except TimeoutError:
        entry["poisoned"] = True
        return []


def _label_spans(entry, text, budget_s):
    spans = []
    if entry["literal"] is not None:
        spans += [m.span() for m in entry["literal"].finditer(text)]
    spans += _match_raw(entry, text, budget_s)
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


def match_text(text, compiled, budget_s=DEFAULT_BUDGET_S):
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
        spans_by_id = {e["id"]: _label_spans(e, text, budget_s) for e in entries}
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
