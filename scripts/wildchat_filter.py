#!/usr/bin/env python3
"""Filter a WildChat shard for PROFESSIONAL DOMAIN WORK, then emit blind views.

⚠️ Keywords SELECT candidates; they are never ground truth. WildChat is consumer ChatGPT
usage, so a hit for "adverse event" may be someone asking about side effects rather than
clinical ops writing a narrative. The blind pass decides both (a) the domain and (b) whether
this is WORK or a QUESTION. Requiring >=2 distinct domain terms and >=4 turns biases toward
work without deciding it.
"""
import json, os, re, random, sys
sys.path.insert(0,"/tmp/claude-1000/pqlib")
import pyarrow.parquet as pq

SHARD="/tmp/claude-1000/wildchat/shard0.parquet"; OUT="/tmp/claude-1000/wildchat"

TERMS={
 "legal":     ["indemnif","governing law","force majeure","termination for convenience",
               "the agreement","msa ","non-disclosure","liability cap","counterparty","redline"],
 "medical":   ["adverse event","informed consent","inclusion criteria","exclusion criteria",
               "clinical trial","protocol amendment","case report form","serious adverse",
               "investigator","dosing regimen"],
 "finance":   ["accrual","journal entry","trial balance","accounts payable","accounts receivable",
               "month-end","general ledger","reconcile the","cost center","depreciation"],
 "support":   ["escalate","sla ","customer reported","ticket number","troubleshoot",
               "root cause","workaround","reproduce the issue","support queue"],
 "marketing": ["subject line","landing page","call to action","open rate","click-through",
               "campaign","brand voice","positioning statement","audience segment"],
 "sales":     ["prospect","pipeline","cold email","proposal for","discovery call",
               "objection handling","quota","upsell","close the deal"],
}

def flat(conv, cap=7000):
    out=[]; n=0
    for t in conv or []:
        c=(t.get("content") or "").strip()
        if not c: continue
        c=" ".join(c.split())
        if n+len(c)>cap: break
        out.append(("USER" if t.get("role")=="user" else "ASSISTANT", c)); n+=len(c)
    return out

hits={d:[] for d in TERMS}
f=pq.ParquetFile(SHARD); scanned=0
for batch in f.iter_batches(batch_size=2000, columns=["conversation_hash","conversation","language","turn"]):
    d=batch.to_pylist()
    for row in d:
        scanned+=1
        if (row.get("language") or "")!="English": continue
        if (row.get("turn") or 0) < 2: continue
        conv=flat(row.get("conversation"))
        if len(conv)<4: continue
        blob=" ".join(t[1] for t in conv).lower()
        if len(blob)<800: continue
        for dom,terms in TERMS.items():
            m={t for t in terms if t in blob}
            if len(m)>=2:
                hits[dom].append({"hash":row["conversation_hash"],"matched":sorted(m),
                                  "turns":conv,"chars":len(blob)})
    if scanned>=60000: break

print(f"scanned {scanned} conversations\n")
random.seed(0); picked=[]
for dom in TERMS:
    pool=hits[dom]
    print(f"  {dom:10s} {len(pool):5d} candidates")
    random.shuffle(pool)
    for c in pool[:8]:
        c["retrieval_domain"]=dom; picked.append(c)
random.shuffle(picked)
for i,c in enumerate(picked,1): c["id"]=f"W{i:03d}"
json.dump(picked, open(f"{OUT}/candidates.json","w"), indent=1)
print(f"\npicked {len(picked)} for blind labelling")

with open(f"{OUT}/blind_views.txt","w") as fh:
    for c in picked:
        fh.write("="*100+f"\n[{c['id']}]  turns={len(c['turns'])}\n"+"="*100+"\n\n")
        for role,txt in c["turns"][:10]:
            fh.write(f"{role}: {txt[:900]}\n\n")
print(f"wrote {OUT}/blind_views.txt  ({os.path.getsize(OUT+'/blind_views.txt')} bytes)")
