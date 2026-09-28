#!/usr/bin/env python3
"""Pull REAL professional-domain conversations from WildChat (allenai/WildChat-1M, ungated).

Why this and not synthesis: two independent generators disagreed on 2/3 of synthetic sessions,
so the synthetic route measures the generator. WildChat is real text written by people doing
their own work, with no awareness it would ever be classified — the generator confound is gone.

⚠️ WildChat is CONSUMER ChatGPT usage. A hit for "adverse event" may be someone asking about
drug side effects rather than clinical ops writing a safety narrative. Retrieval keywords are
therefore WEAK labels and are NOT used as ground truth — they only select candidates. The blind
labelling pass decides both (a) is this the claimed domain and (b) is this WORK or a QUESTION.
"""
import json, urllib.request, urllib.parse, time, os, random

DS="allenai/WildChat-1M"; BASE="https://datasets-server.huggingface.co"
OUT="/tmp/claude-1000/wildchat"

TERMS={
 "legal":      ["indemnification", "clause", "contract review"],
 "medical":    ["patient presented", "adverse event", "clinical trial protocol"],
 "finance":    ["accounts payable", "balance sheet", "journal entry"],
 "support":    ["customer complaint", "escalate the ticket"],
 "marketing":  ["email subject line", "landing page copy"],
 "sales":      ["sales proposal", "cold outreach email"],
}

def search(term, length=6, tries=4):
    u=(f"{BASE}/search?dataset={urllib.parse.quote(DS)}&config=default&split=train"
       f"&query={urllib.parse.quote(term)}&offset=0&length={length}")
    for i in range(tries):
        try:
            return json.loads(urllib.request.urlopen(u, timeout=60).read())
        except Exception as e:
            if i==tries-1:
                print(f"    [{term}] gave up: {str(e)[:60]}"); return None
            time.sleep(5*(i+1))

def flatten(conv, cap=6000):
    out=[]
    for t in conv or []:
        r=t.get("role"); c=(t.get("content") or "").strip()
        if not c: continue
        out.append(("USER" if r=="user" else "ASSISTANT", " ".join(c.split())))
    n=0; keep=[]
    for role,txt in out:
        if n+len(txt)>cap: break
        keep.append((role,txt)); n+=len(txt)
    return keep

seen=set(); rows=[]
for dom, terms in TERMS.items():
    got=0
    for t in terms:
        r=search(t)
        time.sleep(2)
        if not r: continue
        for item in (r.get("rows") or []):
            row=item.get("row") or {}
            h=row.get("conversation_hash")
            if not h or h in seen: continue
            if (row.get("language") or "English")!="English": continue
            turns=flatten(row.get("conversation"))
            if len(turns)<2 or sum(len(x[1]) for x in turns)<400: continue
            seen.add(h); got+=1
            rows.append({"id":f"W{len(rows)+1:03d}","retrieval_domain":dom,"term":t,
                         "hash":h,"turns":turns})
    print(f"  {dom:10s} {got} conversations")

random.seed(0); random.shuffle(rows)
for i,r in enumerate(rows,1): r["id"]=f"W{i:03d}"
json.dump(rows, open(f"{OUT}/candidates.json","w"), indent=1)
print(f"\nwrote {len(rows)} candidates -> {OUT}/candidates.json")

# BLIND views: conversation text only. No retrieval domain, no search term.
with open(f"{OUT}/blind_views.txt","w") as fh:
    for r in rows:
        fh.write("="*100+f"\n[{r['id']}]\n"+"="*100+"\n\n")
        for role,txt in r["turns"]:
            fh.write(f"{role}: {txt[:1100]}\n\n")
print(f"wrote blind views -> {OUT}/blind_views.txt")
