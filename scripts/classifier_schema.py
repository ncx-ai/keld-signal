#!/usr/bin/env python3
"""JSON schema for one custom classifier, with abstention always representable.

Run it to check a template file:  python3 scripts/classifier_schema.py <gallery.json>

The rule this exists to enforce: a classifier must always have a legal way to say "not here".
Measured on the shipped gallery, three of ten templates had no catch-all label — `Sprint /
Iteration` offers only Sprint 24, Sprint 25 and Backlog — so the enum made "no sprint was
mentioned" unrepresentable and the model returned `Backlog`, which reads as a judgement and is
really the least-committal token available. `Customer Lifecycle Stage` staged a customer that
did not exist in the window, for the same reason.

Leaving it to each template is not enough: an org configures its own labels, and one that
defines three repositories and no fallback would silently get nearest-match answers. So the
schema adds the escape hatch when the label set lacks one, for every classifier, always.
"""
import json
import re
import sys

# Words that mean "none of the above" in a label. Deliberately short and checked as whole
# tokens: matching substrings would treat "Generali" (an insurer, a plausible account name) as
# a catch-all, which is the ordinary-English trap this project has paid for repeatedly.
CATCH_ALL_TOKENS = {"general", "none", "other", "n/a", "na", "nothing", "unknown",
                    "unspecified", "not applicable"}

# Readable, not a sentinel. `__none__` was in the enum and the model still answered `Backlog`
# for a window that mentions no sprint: a legal option it has no semantic reason to prefer is
# not an option. AGENTS.md records the same finding for GLiNER — "classifiers score against
# readable label DESCRIPTIONS, not bare id strings, the label wording is load-bearing" — and it
# holds for a generative model choosing between enum members too.
ABSTAIN = "Not mentioned in this work"


def _is_catch_all(label):
    text = label.lower().strip()
    if text in CATCH_ALL_TOKENS:
        return True
    return any(w in CATCH_ALL_TOKENS for w in text.replace("/", " ").split())


def has_catch_all(labels):
    return any(_is_catch_all(l) for l in labels)


def field_schema(kind, labels=()):
    """One classifier's schema fragment.

    single_label      an enum. ABSTAIN is appended when the org's labels offer no way out.
    multi_label       an array; the empty array already means "none", so no addition is needed.
                      uniqueItems because a run returned ["Website / Blog", "Mobile app",
                      "Website / Blog", "Email / Newsletter"] — duplicates the schema allowed.
    entity_extraction an array of strings; empty means none found. Values still need checking
                      against the source text, which is a caller's job, not a schema's.
    """
    labels = list(labels)
    if kind == "single_label":
        if not labels:
            return {"type": "string", "enum": [ABSTAIN]}
        values = labels if has_catch_all(labels) else labels + [ABSTAIN]
        return {"type": "string", "enum": values}
    if kind == "multi_label":
        # uniqueItems is declared and NOT enforced: llama.cpp compiles the schema to a GBNF
        # grammar, and a grammar cannot express uniqueness. A run returned ["Website / Blog",
        # "Mobile app", "Website / Blog", "Email / Newsletter"] under this very constraint.
        # Kept for correctness of the declared schema; callers must dedupe (see dedupe()).
        return {"type": "array", "uniqueItems": True,
                "items": {"type": "string", "enum": labels or [ABSTAIN]}}
    if kind in ("entity_extraction", "structured_extraction"):
        return {"type": "array", "uniqueItems": True, "items": {"type": "string"}}
    raise ValueError(f"unknown classifier type {kind!r}")


def dedupe(value):
    """Order-preserving dedupe for array answers, since uniqueItems is not enforced."""
    if not isinstance(value, list):
        return value
    seen, out = set(), []
    for v in value:
        if v not in seen:
            seen.add(v)
            out.append(v)
    return out


def can_abstain(kind, labels=()):
    """Whether the schema admits an answer meaning "the text does not say"."""
    s = field_schema(kind, labels)
    if s["type"] == "array":
        return True                                  # [] is always legal
    return ABSTAIN in s["enum"] or has_catch_all(list(labels))


def main(path):
    templates = json.load(open(path))["templates"]
    print(f"{'classifier':28} {'type':20} {'own catch-all':14} abstention")
    added = 0
    for t in templates:
        labels = [l["label"] if isinstance(l, dict) else l for l in (t.get("labels") or [])]
        own = has_catch_all(labels) if labels else None
        ok = can_abstain(t["type"], labels)
        if labels and not own and t["type"] == "single_label":
            added += 1
        print(f"{t['name']:28} {t['type']:20} "
              f"{('yes' if own else 'NO' if own is not None else '-'):14} "
              f"{'yes' if ok else 'NO — BROKEN'}")
    print(f"\n{len(templates)} templates, {added} given an explicit {ABSTAIN} by the schema")
    broken = [t["name"] for t in templates
              if not can_abstain(t["type"],
                                 [l["label"] if isinstance(l, dict) else l
                                  for l in (t.get("labels") or [])])]
    if broken:
        print("CANNOT ABSTAIN:", broken)
        return 1
    print("every classifier can say 'not here'")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1] if len(sys.argv) > 1
                  else "/home/dg/Downloads/classifier-gallery-templates.json"))


# ---------------------------------------------------------------------------
# Extractive or inferential, derived from the label set.
#
# The admin picks one of three types in the gallery UI — "One label", "Every label that fits",
# "Find names" — and that decides the OUTPUT shape, not the method. The method depends on
# something else: whether the labels name things in this org or name concepts.
#
#   project_alpha, acme/web-app, ENG-4521, Northwind, sprint_24   are names. The text either
#     contains them or it does not, so the model can quote and code can match. Measured: this
#     abstains correctly off-domain where forced choice fabricated acme/web-app for a month-end
#     close.
#   engineering, finance, prospect, onboarding, linkedin           are concepts. Nobody writes
#     "this is engineering work", so there is no span to quote — asking for one returned nothing
#     on 8 of 10 fields for a marketing window. These must be classified against the labels.
#
# Derived rather than configured, because the gallery has no field for it and asking admins to
# understand the distinction would be asking them to know how the model fails.
NAMEY = ("/", "_", "-")           # acme/web-app, project_alpha, sprint_24
CONCEPT_WORDS = {"engineering", "finance", "sales", "marketing", "operations", "legal", "design",
                 "data", "support", "hr", "people", "prospect", "onboarding", "active",
                 "renewal", "risk", "high", "medium", "low", "general", "internal", "other"}


def is_name_like(label):
    """A label that names a specific thing rather than a category."""
    t = label.strip()
    if not t:
        return False
    if any(c.isdigit() for c in t):                       # ENG-4521, sprint_24, Q4
        return True
    if any(c in t for c in NAMEY):                        # acme/web-app, project_alpha
        return True
    words = [w for w in re.split(r"[\s_/]+", t.lower()) if w]
    if all(w in CONCEPT_WORDS for w in words):            # engineering, at risk, general
        return False
    return t[0].isupper() and t.lower() not in CONCEPT_WORDS   # Northwind, Globex, Initech


def classifier_shape(kind, labels=()):
    """"extractive" -> quote a span, match it in code. "inferential" -> classify against labels.

    `entity_extraction` ("Find names" in the UI) is always extractive — it has no label set to
    classify against. Otherwise the majority of the label set decides, ignoring catch-alls,
    which are neither names nor concepts but escape hatches.
    """
    if kind in ("entity_extraction", "structured_extraction"):
        return "extractive"
    real = [l for l in labels if not _is_catch_all(l)]
    if not real:
        return "inferential"
    namey = sum(1 for l in real if is_name_like(l))
    return "extractive" if namey * 2 > len(real) else "inferential"
