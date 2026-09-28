#!/usr/bin/env python3
"""Run the work-domain classifier over the SYNTHETIC transcripts.

⚠️ SYNTHETIC DATA. Two independent generators (Fable, Sonnet) wrote the same six sessions from
one brief. Running BOTH is the validity check: if they agree, the result is generator-
independent; if they diverge, the test is measuring the generator and no number from it stands.

`eng_billing` is the HARD NEGATIVE — an engineer building invoicing/accrual features, dense in
finance vocabulary but unambiguously engineering work. If it classifies as `finance`, the
classifier keys on vocabulary rather than activity and every other figure here is inflated.
"""
import json, os, re, sys, collections
HERE=os.path.dirname(os.path.abspath(__file__)); sys.path.insert(0,HERE)
from activity_domain_probe import DOMAINS, LABELS, pack, text_of, split_sentences, FENCE

EXPECT={"legal":"legal","finance":"finance","medical":"medical","support":"support",
        "operations":"operations","eng_billing":"engineering"}

def turns_of(path):
    out=[]
    for line in open(path, errors="ignore"):
        line=line.strip()
        if not line: continue
        try: o=json.loads(line)
        except Exception: continue
        if o.get("type") not in ("user","assistant"): continue
        t=re.sub(r"\s+"," ",FENCE.sub(" ",text_of((o.get("message") or {}).get("content")))).strip()
        if not t or t.startswith("<"): continue
        out.append(("USER" if o["type"]=="user" else "ASSISTANT", t))
    return out

from gliner2 import GLiNER2
ex=GLiNER2.from_pretrained("fastino/gliner2-large-v1")

results={}
for tag in ("synth_fable","synth_sonnet"):
    d=f"/tmp/claude-1000/{tag}"
    gen=tag.split("_")[1]
    print(f"\n=== {gen.upper()}")
    print(f"  {'file':14s} {'expected':12s} {'top':12s} {'share':>6s}  {'ok':4s}  runner-up")
    hits=0
    for f in sorted(os.listdir(d)):
        if not f.endswith(".jsonl"): continue
        stem=f[:-6]
        subs=pack(turns_of(os.path.join(d,f)))
        v=collections.Counter()
        for s in subs:
            lab=ex.classify_text(s,{"domain":{"labels":LABELS}},include_confidence=True)["domain"]["label"].split(":")[0]
            v[lab]+=1
        n=sum(v.values()) or 1
        top,c=v.most_common(1)[0]
        exp=EXPECT[stem]; ok = top==exp
        hits+=ok
        ru=v.most_common(2)[1] if len(v)>1 else ("-",0)
        mark="OK" if ok else ("**" if stem=="eng_billing" else "miss")
        print(f"  {stem:14s} {exp:12s} {top:12s} {c/n:5.0%}  {mark:4s}  {ru[0]}({ru[1]/n:.0%})")
        results[(gen,stem)]=(top, dict(v))
    print(f"  -> {hits}/6 correct")

print("\n=== GENERATOR AGREEMENT (the validity check) ===")
agree=0
for stem in sorted(EXPECT):
    a=results.get(("fable",stem),("?",{}))[0]; b=results.get(("sonnet",stem),("?",{}))[0]
    same=a==b; agree+=same
    print(f"  {stem:14s} fable={a:12s} sonnet={b:12s} {'agree' if same else 'DIVERGE'}")
print(f"  -> generators agree on {agree}/6")
