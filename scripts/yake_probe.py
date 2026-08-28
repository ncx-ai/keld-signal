#!/usr/bin/env python3
"""Feasibility probe: does YAKE surface subject-matter phrases `named_terms` cannot reach?

EXPERIMENT ONLY. Nothing here is wired into the sidecar, the daemon, or
`sidecar/requirements.txt`. YAKE is installed into a THROWAWAY venv; it is never added to the
sidecar venv. Pre-registration: ~/keld/refseries-context/yake/PREREGISTRATION.md.

Three stages, deliberately separate processes, because the two extractors cannot live in one
interpreter and rule 2 of the pre-registration requires them to see IDENTICAL text:

    project   stdlib only        transcript -> a JSON dump of message bodies
    terms     sidecar venv       the dump -> `named_terms` (spaCy + wordfreq, the INCUMBENT)
    yake      throwaway venv     the dump -> YAKE keyphrases (the CHALLENGER)

The dump is the seam. Both extractors read the same file, so any difference in their output is a
difference in the extractor and not in the projection.

TEXT PROJECTION — the same one `levels.py` already applies when it builds `named_terms`:

  * `transcript.iter_turns` for the turns, `text.text_of` for the body. Not a new reader.
  * USER text and ASSISTANT text, both. That is what `levels.py` does (it calls `terms.tally` in
    both branches), and the ground truth this is scored against lives on both sides of the
    conversation — a supplier is named by the person, the shortlist that comes back names it too.
  * `text.is_command_echo` drops user turns that are machine text in a user envelope. Without it
    the keyphrase list fills with `<system-reminder>` and skill-file boilerplate, which would be
    an artefact of the projection rather than a finding about YAKE. `levels.py` applies it to
    user turns only, and so does this.
  * NO truncation. `terms.py` measured that the 400-char per-turn clip drops 58% of mentions and
    takes Together.ai and Vertex to zero. Both extractors here are linear; neither is a
    transformer with a context window.

CHUNKING — the choice the pre-registration asks to be stated rather than assumed:

YAKE scores a candidate on four features, and two of them (TPos, the median sentence position;
and the left/right dispersion window) are properties of the DOCUMENT, not of the term. On a
single 14 MB blob TPos is log(3 + median-sentence-index) over ~10^5 sentences, so every candidate
past the opening pages lands in the same narrow band and position stops discriminating at all —
one of YAKE's four features quietly switches itself off. Per-TURN is the opposite failure: a
40-word turn gives every candidate a near-perfect position score, no dispersion signal, and a
`dedupLim` applied to a handful of candidates.

So the unit here is a CHUNK of consecutive turns under a character budget, split only at turn
boundaries — which is both the scale the analysis package already works at (a window) and the
project's standing rule that text read as language is bounded at a logical delimiter, never at a
rune count. Per-chunk top-K lists are then aggregated the way `terms.tally` aggregates: by
SPREAD, the number of chunks a phrase reaches the top of, tie-broken by its best score. `terms.py`
argues that case explicitly — a term said eight times in one message is one mention of one topic,
a term in eight messages is what the stretch is about.

`--blob` runs the single-document variant too, so the choice is visible in the results rather
than asserted here.
"""
import argparse
import collections
import json
import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "sidecar"))

from app.analysis.transcript import iter_turns          # noqa: E402
from app.analysis.text import text_of, is_command_echo  # noqa: E402


# ---------------------------------------------------------------- stage 1: project

def project(path):
    """[{role, text}] for one transcript — the shared input both extractors read."""
    out = []
    for o in iter_turns(path):
        msg = o.get("message") or {}
        body = text_of(msg.get("content"))
        if not body.strip():
            continue
        role = "user" if o.get("type") == "user" else "asst"
        if role == "user" and is_command_echo(body):
            continue
        out.append({"role": role, "text": body})
    return out


def chunks(messages, budget):
    """Consecutive messages grouped under a character budget, split only at turn boundaries.

    A single message larger than the budget becomes its own chunk rather than being cut: an
    identifier or a sentence sliced at a rune count is a false one (AGENTS.md).
    """
    cur, size = [], 0
    for m in messages:
        if cur and size + len(m["text"]) > budget:
            yield cur
            cur, size = [], 0
        cur.append(m)
        size += len(m["text"])
    if cur:
        yield cur


# ---------------------------------------------------------------- stage 2a: incumbent

def run_terms(messages):
    import spacy
    from app.analysis import terms
    nlp = spacy.load("en_core_web_sm")
    # levels.py calls tally() per message; tally() itself takes a sequence, and calling it once
    # over the whole list gives the same per-term totals plus a real message-spread count.
    return terms.tally([m["text"] for m in messages], nlp)


# ---------------------------------------------------------------- stage 2b: challenger

def run_yake(messages, budget, top, ngram, dedup, blob):
    import yake
    kw = yake.KeywordExtractor(lan="en", n=ngram, dedupLim=dedup,
                               dedupFunc="seqm", windowsSize=1, top=top)
    spread, best, surface = collections.Counter(), {}, {}
    nchunks = 0
    for ch in chunks(messages, budget):
        nchunks += 1
        text = "\n\n".join(m["text"] for m in ch)
        try:
            res = kw.extract_keywords(text)
        except Exception as e:                      # one pathological chunk must not end the run
            print(f"  chunk {nchunks}: {type(e).__name__}: {e}", file=sys.stderr)
            continue
        for phrase, score in res:
            k = " ".join(phrase.split()).lower()
            if not k:
                continue
            spread[k] += 1
            best[k] = min(best.get(k, 1e9), score)
            surface.setdefault(k, collections.Counter())[" ".join(phrase.split())] += 1
    ranked = [{"phrase": surface[k].most_common(1)[0][0], "chunks": n, "best_score": round(best[k], 5)}
              for k, n in sorted(spread.items(), key=lambda kv: (-kv[1], best[kv[0]], kv[0]))]

    out = {"mode": "chunked", "n_chunks": nchunks, "budget": budget, "ranked": ranked}
    if blob:
        text = "\n\n".join(m["text"] for m in messages)
        out["blob"] = [{"phrase": p, "score": round(s, 5)}
                       for p, s in yake.KeywordExtractor(
                           lan="en", n=ngram, dedupLim=dedup, dedupFunc="seqm",
                           windowsSize=1, top=top * 3).extract_keywords(text)]
    return out


# ---------------------------------------------------------------- scoring

GROUND_TRUTH = ["ACME", "UnityPredict", "Bedrock", "Together.ai", "Vertex",
                "Magenta", "Developer Preview", "Exchange Alpha"]


def recall(items, gt=GROUND_TRUTH):
    """Which ground-truth items a list of surface strings contains.

    Substring, case-insensitive, on a whitespace-normalized haystack: a phrase like
    "acme routing scenarios" surfaces ACME, and "september developer preview" surfaces Developer
    Preview. Deliberately generous — the question is whether the item REACHES an admin's eye, and
    a stricter equality test would score presentation rather than discovery.
    """
    hay = " | ".join(items).lower()
    return {g: (g.lower() in hay) for g in gt}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("stage", choices=["project", "terms", "yake"])
    ap.add_argument("--transcript")
    ap.add_argument("--dump", required=True)
    ap.add_argument("--out")
    ap.add_argument("--budget", type=int, default=20000)
    ap.add_argument("--top", type=int, default=20)
    ap.add_argument("--ngram", type=int, default=3)
    ap.add_argument("--dedup", type=float, default=0.9)
    ap.add_argument("--blob", action="store_true")
    a = ap.parse_args()

    if a.stage == "project":
        msgs = project(a.transcript)
        json.dump(msgs, open(a.dump, "w"))
        nu = sum(1 for m in msgs if m["role"] == "user")
        print(f"messages={len(msgs)} user={nu} asst={len(msgs) - nu} "
              f"chars={sum(len(m['text']) for m in msgs)} -> {a.dump}")
        return

    msgs = json.load(open(a.dump))
    if a.stage == "terms":
        res = {"extractor": "named_terms", "ranked": run_terms(msgs)}
        surfaces = [r["term"] for r in res["ranked"]]
    else:
        res = run_yake(msgs, a.budget, a.top, a.ngram, a.dedup, a.blob)
        res["extractor"] = "yake"
        surfaces = [r["phrase"] for r in res["ranked"]]

    res["recall_top30"] = recall(surfaces[:30])
    res["recall_all"] = recall(surfaces)
    res["n_terms"] = len(surfaces)
    json.dump(res, open(a.out, "w"), indent=1)
    print(f"{res['extractor']}: {len(surfaces)} terms -> {a.out}")
    print("  top30 recall:", sum(res["recall_top30"].values()), "/ 8   ",
          "all:", sum(res["recall_all"].values()), "/ 8")


if __name__ == "__main__":
    main()
