#!/usr/bin/env python3
"""Can deterministic file evidence supply the MODALITY half of an atv1 verb?

8 of 17 verbs are `modality.operation` (text/code/image/video/audio); 9 are bare operations.
This asks, on the same 100 windows and the same gold labels:
  1. what modality do the window's file extensions imply?
  2. how often does that match the gold verb's modality?
  3. how often are SEVERAL modalities present at once -- the multi-label case?
"""
import json, os, re, sys, collections
from datetime import datetime, timedelta
sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "sidecar"))
from app.analysis.vocab import EXT_LANG, ARTIFACT_EXT
from app.analysis.paths import PATH_INPUTS

SPAN = 60
SAMPLE = os.path.expanduser("~/keld/refseries-context/facets/activity-rerun-sample.ndjson")
LABELS = os.path.join(os.path.dirname(os.path.abspath(__file__)), "activity-atv1-hand-labels.txt")

IMAGE = {".png",".jpg",".jpeg",".gif",".svg",".webp",".bmp",".tiff",".ico"}
VIDEO = {".mp4",".mov",".webm",".mkv",".avi"}
AUDIO = {".mp3",".wav",".m4a",".flac",".ogg"}
PROSE = {".md",".txt",".rst",".docx",".doc",".odt",".rtf",".pdf",".tex"}

def modality_of(ext):
    if ext in EXT_LANG: return "code"
    if ext in IMAGE: return "image"
    if ext in VIDEO: return "video"
    if ext in AUDIO: return "audio"
    if ext in PROSE: return "text"
    return None

def _ep(ts): return datetime.fromisoformat(ts.replace("Z","+00:00"))

def exts_in(path, start_iso):
    if not os.path.exists(path): return None
    start=_ep(start_iso); end=start+timedelta(minutes=SPAN)
    c=collections.Counter()
    for line in open(path, errors="ignore"):
        try: o=json.loads(line)
        except Exception: continue
        ts=o.get("timestamp")
        if not ts: continue
        try: t=_ep(ts)
        except Exception: continue
        if not (start<=t<end): continue
        blocks=(o.get("message") or {}).get("content")
        if not isinstance(blocks,list): continue
        for b in blocks:
            if not isinstance(b,dict) or b.get("type")!="tool_use": continue
            for k in PATH_INPUTS:
                v=(b.get("input") or {}).get(k)
                if isinstance(v,str) and "." in os.path.basename(v):
                    c[os.path.splitext(v)[1].lower()]+=1
    return c

gold={}
for line in open(LABELS):
    m=re.match(r"^(\d{3})\s+(\S+)",line)
    if m: gold[int(m.group(1))]=m.group(2)
recs=[json.loads(l) for l in open(SAMPLE)][:len(gold)]

MOD_OF_VERB={"text":"text","code":"code","image":"image","video":"video","audio":"audio"}
def gold_modality(gid):
    v=gid.rsplit(".",1)[0] if gid.count(".")>1 else gid
    head=gid.split(".")[0]
    return MOD_OF_VERB.get(head)

rows=[]
for i,r in enumerate(recs,1):
    c=exts_in(r["file"], r["start"])
    if c is None: rows.append((i,None,None,None)); continue
    mods=collections.Counter()
    for e,n in c.items():
        m=modality_of(e)
        if m: mods[m]+=n
    rows.append((i,mods,gold[i],gold_modality(gold[i])))

have=[r for r in rows if r[1] is not None]
withmod=[r for r in have if r[1]]
print(f"windows with a transcript          : {len(have)}/100")
print(f"windows with any modality evidence : {len(withmod)}")
print()
multi=[r for r in withmod if len(r[1])>1]
print(f"windows with >=2 modalities present: {len(multi)}/{len(withmod)}  ({len(multi)/len(withmod):.0%})")
print("  modality-set frequency:", collections.Counter(tuple(sorted(r[1])) for r in withmod).most_common(6))
print()
gm=[r for r in withmod if r[3]]
hit=sum(1 for r in gm if r[3] in r[1])
top=sum(1 for r in gm if r[1].most_common(1)[0][0]==r[3])
print(f"gold verbs that HAVE a modality    : {len(gm)}/{len(withmod)}")
print(f"  gold modality PRESENT in file evidence : {hit}/{len(gm)} = {hit/len(gm):.3f}   <- multi-label recall")
print(f"  gold modality is the TOP modality      : {top}/{len(gm)} = {top/len(gm):.3f}   <- single-label accuracy")
print()
bare=[r for r in withmod if not r[3]]
print(f"gold verbs with NO modality (research/review/plan/...): {len(bare)}")
print("  what file evidence says about them:", collections.Counter(r[1].most_common(1)[0][0] for r in bare).most_common())
print("  -> file types propose a modality for these anyway; they are the false-positive population")
