#!/usr/bin/env python3
"""What a block is ABOUT — phrases ranked by the encoder that is already resident.

The sibling of `terms.py`, and its replacement in every case where the question is "what was
this work about" rather than "who or what was named in it". The two answer different questions
and fail differently:

* `terms.py` DETECTS name-shaped strings — spaCy's English NER plus four identifier regexes —
  and publishes the top twelve by raw count. It has no notion of relevance, so a name mentioned
  once in passing outranks nothing and `Atlas`, `JSON`, `API` and `UI` appear on nearly every
  block of a session about anything. Its own docstring names the intended remedy (corpus LIFT,
  which refseries computes per level), and the block payload publishes raw counts, so that
  remedy has never applied to what a reader actually sees.
* This module RANKS. Candidates are lifted from the block's own words, embedded with the same
  encoder attribution already loaded, and scored by how close each sits to the block's own
  centroid. A phrase mentioned constantly but peripheral to the work ranks LOW, which is the
  ordering `n` can never produce.

⚠️ **An embedding model cannot generate a concept.** It maps text to a point and has no output
vocabulary, so it cannot be asked "what is this about". The question is inverted instead:
enumerate candidate phrases FROM THE TEXT, embed each, and keep the ones nearest the whole. The
consequence worth stating is that nothing here can be invented — every published phrase is a
substring of something the person actually typed. That is a hallucination guarantee a generative
model could not give, and it is the reason this needs no second model.

**MMR, not top-k.** Ranking alone returns near-duplicates: `project attribution`,
`attribution`, `block attribution` all sit beside the centroid and say one thing three times.
Maximal Marginal Relevance trades a little relevance for distance from what is already chosen
(`LAMBDA`), which is what makes eight slots hold eight facts.

⚠️ **PRIVACY: this publishes multi-word fragments of message text, and that is a THIRD
text-derived published signal.** AGENTS.md is explicit that the first two (`named_terms` at
schema v18, `KELD_TEXTEMBED`'s projected vector) were each decided on their own evidence and
that a third must be too, "never by analogy to these two". The argument for this one is that it
is strictly narrower than `named_terms` in what it can carry and strictly more useful:

* Bounded by SHAPE — at most `MAX_WORDS` words, and a candidate is discarded whole rather than
  cut, so no fragment of a sentence longer than three words can cross (`AGENTS.md`'s
  never-cut-mid-sentence rule, applied as a drop rather than a truncation).
* Bounded by COUNT — `TOP_K` per block.
* No span, no offset, no position: a phrase and a score, the same shape `named_terms` crosses in.
* It does NOT reduce exposure to zero. A three-word phrase can hold a person's name and one
  word of context where `named_terms` holds the name alone. Whoever ships this beyond a local
  device owes that trade an explicit decision, exactly as the two before it got one.
"""
import math
import re
import time

#: Phrase length. Three words is where a phrase still names ONE thing; at four it starts being a
#: clause, which is both less useful as a label and more text than this needs to publish.
MAX_WORDS = 3

#: How many phrases a block publishes. Eight fits a reader's eye and a UI row, and MMR means the
#: eighth is a different fact rather than the eighth spelling of the first.
TOP_K = 8

#: The relevance/diversity trade in MMR. 0.7 keeps relevance dominant — a diverse list of
#: irrelevant phrases is worse than a slightly repetitive relevant one.
LAMBDA = 0.7

#: How many candidates reach the encoder. THIS IS THE COST KNOB: everything else in this module
#: is string work measured in microseconds, and the encode is one batch of this many short
#: strings. Candidates are ranked by occurrence before the cut, so the cut removes the long tail
#: of phrases said once, never a phrase the block keeps returning to.
MAX_CANDIDATES = 160

#: A phrase may CONTAIN these; it may not START or END with one. "the block cutter" is a concept
#: and "of the" is not, and that rule needs no frequency list to apply. Deliberately short for
#: `terms.py`'s own stated reason: a long stoplist is a way of hiding a bad extractor. What keeps
#: `Atlas` and `JSON` out of the answer here is the RANKING, not a list.
EDGE_WORDS = frozenset("""
a an the this that these those it its is are was were be been being am
and or but so if then than as at by for from in into of on onto to with without
we you i they he she them us our your their my me him her his hers
do does did done doing have has had having can could should would will shall may might must
not no nor only just very much more most less least also too even still yet
what which who whom whose when where why how there here all any both each few other some such
one two three now new old good bad thing things way ways lot lots kind sort
like get got make made made take taken use used using see saw seen say said says
""".split())

#: A candidate is words and the punctuation that lives INSIDE an identifier — a hyphen, a dot, an
#: underscore, a slash. Everything else is a boundary, so a sentence cannot become a candidate.
_WORD = re.compile(r"[A-Za-z][A-Za-z0-9]*(?:[-_./][A-Za-z0-9]+)*")


def _words(text):
    return _WORD.findall(text)


def _ok(phrase_words):
    """A phrase that is worth embedding.

    Edge words are rejected at the EDGES only, so `the block cutter` yields `block cutter` from
    the same window rather than being lost — the shorter n-grams of the same span are separate
    candidates and one of them is clean.
    """
    if not phrase_words:
        return False
    if phrase_words[0].lower() in EDGE_WORDS or phrase_words[-1].lower() in EDGE_WORDS:
        return False
    # A single word must be substantial: two characters is an initialism a name-detector should
    # be finding, not a concept.
    return not (len(phrase_words) == 1 and len(phrase_words[0]) < 4)


def candidates(texts):
    """Every candidate phrase in the block, most-repeated first.

    Counted case-insensitively and reported in the casing the block uses most, the same rule
    `terms.tally` applies for the same reason: `Block cutter` and `block cutter` are one concept
    said twice, and splitting them halves the prominence of the thing the block was about.
    """
    total, surface = {}, {}
    for text in texts:
        ws = _words(text)
        for n in range(1, MAX_WORDS + 1):
            for i in range(len(ws) - n + 1):
                span = ws[i:i + n]
                if not _ok(span):
                    continue
                phrase = " ".join(span)
                key = phrase.lower()
                total[key] = total.get(key, 0) + 1
                surface.setdefault(key, {})
                surface[key][phrase] = surface[key].get(phrase, 0) + 1
    ranked = sorted(total.items(), key=lambda kv: (-kv[1], kv[0]))[:MAX_CANDIDATES]
    return [max(surface[k].items(), key=lambda kv: kv[1])[0] for k, _ in ranked]


def _l2(v):
    n = math.sqrt(sum(x * x for x in v)) or 1.0
    return [x / n for x in v]


def _cos(a, b):
    return sum(x * y for x, y in zip(a, b))


def _centroid(vecs):
    """The block's own point: the mean of its message vectors, re-normalised.

    The MEAN and not the max used in `attribution.score_block`. The two are asking opposite
    questions: scoring wants the single message that best matches a project, because a block's
    messages may each concern a different one; a concept is meant to describe the block AS A
    WHOLE, and ranking against one message would return that message's topic.
    """
    if not vecs:
        return None
    dims = len(vecs[0])
    return _l2([sum(v[i] for v in vecs) / len(vecs) for i in range(dims)])


def _mmr(cand_vecs, doc, k, lam):
    """Maximal Marginal Relevance over already-embedded candidates. Returns indices."""
    rel = [_cos(v, doc) for v in cand_vecs]
    chosen = []
    remaining = list(range(len(cand_vecs)))
    while remaining and len(chosen) < k:
        best, best_score = None, None
        for i in remaining:
            redundancy = max((_cos(cand_vecs[i], cand_vecs[j]) for j in chosen), default=0.0)
            score = lam * rel[i] - (1 - lam) * redundancy
            if best_score is None or score > best_score:
                best, best_score = i, score
        chosen.append(best)
        remaining.remove(best)
    return chosen, rel


def extract(texts, text_vectors, encoder, top_k=TOP_K):
    """The block's concepts: `([{value, score}], elapsed_ms)`.

    `text_vectors` are the block's message vectors, ALREADY COMPUTED by the caller's own scoring
    pass. Re-encoding them here would be the expensive half of the work done twice — a message
    costs ~1.1-1.6 s to encode and a candidate phrase costs a small fraction of that — so the
    caller passes them in and this module pays only for the candidates.

    Returns an empty list rather than raising when there is nothing to work with (no encoder, no
    text, no candidate survives the edge rules). An empty list of concepts is a real answer: a
    block of pure tool-running with no prose has none.
    """
    t0 = time.time()
    doc = _centroid([_l2(v) for v in (text_vectors or [])])
    if encoder is None or doc is None:
        return [], 0
    phrases = candidates(texts)
    if not phrases:
        return [], int((time.time() - t0) * 1000)
    vecs = [_l2(v) for v in encoder.encode(phrases)]
    picked, rel = _mmr(vecs, doc, top_k, LAMBDA)
    out = [{"value": phrases[i], "score": round(rel[i], 4)} for i in picked]
    # Reported best-first by RELEVANCE, not in MMR's own pick order: MMR's order is an artifact
    # of the diversity walk, and a reader scanning a row reads the first pill as the strongest.
    out.sort(key=lambda c: -c["score"])
    return out, int((time.time() - t0) * 1000)
