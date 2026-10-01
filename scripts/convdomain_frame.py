"""Two sampling strata from WildChat, for the conversation-domain study.

⚠️ THE STRATA ARE THE WHOLE POINT. `candidate` conversations were selected BY KEYWORD, so a
keyword arm scored on them measures the SELECTOR, not the classifier -- it would score near
100% by construction. That is exactly how an earlier keyword baseline looked good at 52%
before collapsing to 17% on data it had not selected. `random` is drawn with no filter at all
and is the only stratum where a false-positive rate is measurable.

⚠️ `stratum` IS RECORDED BUT MUST BE HIDDEN FROM THE LABELLER. Knowing a conversation came
from the candidate pool biases a reader toward finding a domain in it.

⚠️ THE WHOLE CONVERSATION IS SCANNED, not the first turn. Chat drifts: a conversation can open
with small talk and reach a legal question at turn five.
"""
import json, os, random, re, sys

SHARD = "/tmp/claude-1000/wildchat/shard0.parquet"
OUT = "/tmp/claude-1000/convdomain/frame.ndjson"
N_PER_DOMAIN = 10
N_RANDOM = 60
SEED = 20261001

# The selection net. Deliberately generous: precision is established by hand-labelling, and a
# narrow net was exactly what made an earlier pass report 28 candidates where 1,137 exist.
SIGNALS = {
    "legal":     r"\b(nda|non-disclosure|terms (and|&) conditions|privacy policy|contract|clause|indemnif|liabilit|lease agreement|msa|terms of service)\b",
    "marketing": r"\b(landing page|campaign|ad copy|seo|newsletter|email blast|brand voice|press release|social media post|call to action)\b",
    "sales":     r"\b(cold email|outreach|prospect|proposal for|pitch deck|follow[- ]up email|quota|crm|lead gen)\b",
    "financial": r"\b(invoice|balance sheet|p&l|cash flow|journal entry|depreciation|reconcil|budget forecast|financial model|bookkeeping)\b",
    "medical":   r"\b(patient|diagnos|clinical|symptom|prescri|icd-?10|soap note|discharge|treatment plan|medical record)\b",
    "hr":        r"\b(job description|offer letter|performance review|onboarding plan|interview questions for|employee handbook|resume|cover letter)\b",
}
PAT = {k: re.compile(v, re.I) for k, v in SIGNALS.items()}


def render(conv):
    """The conversation as a labeller and an arm both see it."""
    out = []
    for m in conv:
        role = (m.get("role") or "?").upper()
        body = " ".join((m.get("content") or "").split())
        if body:
            out.append(f"{role}: {body}")
    return "\n\n".join(out)


def main():
    import pyarrow.parquet as pq
    os.makedirs(os.path.dirname(OUT), exist_ok=True)
    f = pq.ParquetFile(SHARD)

    pools = {k: [] for k in SIGNALS}
    allrows = []
    scanned = skipped_lang = 0
    for rg in range(f.num_row_groups):
        t = f.read_row_group(rg, columns=["conversation_hash", "language", "turn", "conversation"])
        hashes = t.column("conversation_hash").to_pylist()
        langs = t.column("language").to_pylist()
        turns = t.column("turn").to_pylist()
        convs = t.column("conversation").to_pylist()
        for h, lang, nturn, conv in zip(hashes, langs, turns, convs):
            scanned += 1
            if lang != "English":
                skipped_lang += 1
                continue
            text = render(conv)
            if not text.strip():
                continue
            # ⚠️ Scan the WHOLE conversation, both roles. Chat drifts.
            hits = sorted(k for k, p in PAT.items() if p.search(text))
            rec = {"id": h[:12], "stratum": None, "hit_domains": hits,
                   "turns": nturn, "chars": len(text), "text": text}
            allrows.append(rec)
            for k in hits:
                pools[k].append(rec)

    rnd = random.Random(SEED)
    chosen, taken = [], set()
    for dom in sorted(pools):
        pool = sorted(pools[dom], key=lambda r: r["id"])
        rnd.shuffle(pool)
        n = 0
        for r in pool:
            if r["id"] in taken:
                continue
            r2 = dict(r, stratum="candidate")
            chosen.append(r2)
            taken.add(r["id"])
            n += 1
            if n >= N_PER_DOMAIN:
                break
        print(f"  candidate {dom:10} pool {len(pools[dom]):5}  took {n}")

    rest = sorted((r for r in allrows if r["id"] not in taken), key=lambda r: r["id"])
    rnd.shuffle(rest)
    for r in rest[:N_RANDOM]:
        chosen.append(dict(r, stratum="random"))
        taken.add(r["id"])

    rnd.shuffle(chosen)                 # ⚠️ so file order leaks no stratum
    with open(OUT, "w") as out:
        for r in chosen:
            out.write(json.dumps(r, separators=(",", ":")) + "\n")
    print(f"\nscanned {scanned:,}   non-English skipped {skipped_lang:,}")
    print(f"sampled {len(chosen)}  ({sum(1 for r in chosen if r['stratum']=='candidate')} candidate, "
          f"{sum(1 for r in chosen if r['stratum']=='random')} random)")
    print(f"wrote {OUT}")


if __name__ == "__main__":
    main()
