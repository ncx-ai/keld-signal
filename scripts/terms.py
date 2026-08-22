#!/usr/bin/env python3
"""Named terms in message TEXT — the one class of fact no reference level can see.

Every one of refseries' 18 levels reads tool-call inputs. The names that attribution actually
needs — a customer, a supplier, an initiative, a model under evaluation — are only ever spoken.
Measured on the one non-engineering session in the corpus: ACME, UnityPredict, Bedrock,
Together.ai, Vertex, Magenta, Developer Preview and Exchange Alpha are all in the conversation and
none is in any tool input.

Three decisions, each measured rather than assumed:

* **Read the conversation, not tool results.** Adding tool_result text finds no additional names
  (8/8 either way) for 2.7x the characters, and its counts track how many times a file was READ
  rather than how important a term is — the same defect that once made an hour of slide editing
  report `pdf 54%` from ten pdftoppm calls.
* **Do not truncate.** The 400-char per-turn clip in `window_text` drops 58% of mentions and takes
  Together.ai and Vertex to zero. That clip exists for an LLM's context window; nothing here is a
  transformer, spaCy's cost is linear, and digest generation has no size constraint. Untruncated
  costs 36 ms/window and recovers 144 mentions against 31.
* **Use spaCy to DETECT, never to TYPE.** On this domain en_core_web_sm calls ACME a facility,
  Magenta a geopolitical entity and Exchange Alpha a person, and half its output is CARDINAL
  numbers. Detection is good; the labels are noise. Types must come from the org's own configured
  vocabulary, not from a general-English model.
"""
import re
import collections

# Numeric/temporal spaCy types carry no attribution signal and are half its raw output.
DROP_TYPES = {"CARDINAL", "ORDINAL", "DATE", "TIME", "MONEY", "PERCENT", "QUANTITY"}

# Shapes spaCy misses because they are identifiers rather than English: a dotted vendor
# (Together.ai), an ALL-CAPS acronym (ACME, TEE), CamelCase (UnityPredict), and hyphenated
# artefact names (keld-acme-routing-scenarios). The last is the case a proper-noun model cannot
# reach at all: the name is lowercase inside a filename.
SHAPES = (
    re.compile(r"\b[A-Za-z][A-Za-z0-9]*\.(?:ai|io|com|co|dev|sh|app)\b"),
    re.compile(r"\b[A-Z]{2,}(?:-[A-Z0-9]+)*\b"),
    re.compile(r"\b[a-z]+(?:-[a-z0-9]+){2,}\b"),
    re.compile(r"\b[A-Z][a-z]+(?:[A-Z][a-z]+)+\b"),
)

# Terms that are about the conversation rather than the work. Kept deliberately short: a long
# stoplist is a way of hiding a bad extractor, and every entry here is one an admin cannot see.
NOISE = {
    "i", "you", "we", "it", "the", "a", "an", "ok", "okay", "yes", "no", "yeah",
    "todo", "done", "wip", "n/a", "tbd", "etc", "eg", "ie", "am", "pm", "utc",
}


def normalize(s):
    """Collapse whitespace and drop a leading article. Never truncates: a cut identifier is a
    false identifier (AGENTS.md), so a term is kept whole or dropped."""
    s = " ".join(s.split()).strip(" \t\n.,;:!?'\"()[]{}")
    low = s.lower()
    for art in ("the ", "a ", "an "):
        if low.startswith(art):
            s = s[len(art):]
            break
    return s.strip()


# A model, a vendor and a cloud named in one breath — "Bedrock/Together.ai/Vertex" — arrive as a
# single span from both spaCy and the shape patterns, which buries every name but the first. Split
# on the separators that join a LIST of names, never on characters that occur inside one (a dot in
# Together.ai, a hyphen in keld-acme-routing-scenarios).
LIST_SEP = re.compile(r"\s*(?:/|,|\band\b|\bor\b|\bvs\.?\b|\|)\s*")


def split_list(term):
    """Yield each name in a term that is really a list of names."""
    parts = [p for p in LIST_SEP.split(term) if p and len(p) > 1]
    return parts if len(parts) > 1 else [term]


def candidates(text, nlp=None):
    """Every candidate term in one message, as normalized surface strings.

    spaCy supplies multi-word names it is genuinely good at finding ("September Developer
    Preview"); the shape regexes supply identifiers it tokenizes away. Both feed one pool — the
    union is what covers each other's blind spots, and neither is trusted for a type.
    """
    out = []
    if nlp is not None:
        for e in nlp(text).ents:
            if e.label_ in DROP_TYPES:
                continue
            out += split_list(normalize(e.text))
    for pat in SHAPES:
        for m in pat.finditer(text):
            out += split_list(normalize(m.group()))
    return [t for t in out if len(t) > 1 and t.lower() not in NOISE and not t.isdigit()]


def tally(messages, nlp=None):
    """Term counts over a sequence of messages.

    Returns per term: total occurrences and the number of distinct MESSAGES it appears in. The
    second is the one that resists a single ranting turn: a name said eight times in one message
    is one mention of one topic, while a name in eight messages is what the stretch is about.
    """
    # Counted case-INSENSITIVELY, reported in the casing the corpus uses most. "Magenta" and
    # "magenta" are one term said 24 times, not two said 9 and 15 — and splitting them halves the
    # prominence of the thing the hour was actually about.
    total, spread, surface = collections.Counter(), collections.Counter(), {}
    for m in messages:
        seen = set()
        for t in candidates(m, nlp):
            k = t.lower()
            total[k] += 1
            surface.setdefault(k, collections.Counter())[t] += 1
            seen.add(k)
        for k in seen:
            spread[k] += 1
    return [{"term": surface[k].most_common(1)[0][0], "n": n, "messages": spread[k]}
            for k, n in sorted(total.items(), key=lambda kv: (-kv[1], kv[0]))]
