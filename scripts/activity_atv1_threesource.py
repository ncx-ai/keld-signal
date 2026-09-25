#!/usr/bin/env python3
"""Three sources, each feeding the axis it actually knows about.

The organising principle, from everything measured 2026-09-24/25:

    file extensions  -> MODALITY   (code/text/image/video/audio)   recall 1.000 on code
    prose            -> the VERB and the 9 non-modality operations  0.700 with `docs` wording
    team membership  -> the CONTEXT prior                           false domains 11 -> 4

Evidence informs the axis it is ABOUT. Act counts on the verb axis were refuted five times;
team on the verb axis is a category error (an image is image.create whoever made it).

Deterministic evidence PROPOSES candidates, it never gates -- so it cannot be wrong in the way
a gate can, only silent. Output is a RANKED MULTI-LABEL list of legal atv1 ids.
"""
import csv, json, os, re, sys, collections, datetime as dt
HERE = os.path.dirname(os.path.abspath(__file__)); sys.path.insert(0, HERE)
sys.path.insert(0, os.path.join(HERE, "..", "sidecar"))
from activity_atv1 import DOCS
from app.analysis.vocab import EXT_LANG
from app.analysis.paths import PATH_INPUTS

CSVP = os.path.expanduser("~/Downloads/keld-activity-types-v1.csv")
IMAGE = {".png",".jpg",".jpeg",".gif",".svg",".webp",".bmp"}
VIDEO = {".mp4",".mov",".webm",".mkv"}
AUDIO = {".mp3",".wav",".m4a",".flac"}
TEXTE = {".md",".txt",".rst",".docx",".doc",".pdf",".pptx",".ppt",".xlsx",".csv",".key",".tex"}
NEW_TOOLS  = {"Write","NotebookEdit","create_file","SendUserFile"}
MOD_TOOLS  = {"Edit","MultiEdit","str_replace_editor"}

def modality_of(ext):
    if ext in EXT_LANG: return "code"
    if ext in IMAGE: return "image"
    if ext in VIDEO: return "video"
    if ext in AUDIO: return "audio"
    if ext in TEXTE: return "text"
    return None

# modality x (new|mod) -> the verb that names it. NOT an intent inference: the modality is
# already known from the extension, and new-vs-modify is the tool's own identity.
MOD_VERB = {("code","new"):"code.write",  ("code","mod"):"code.edit",
            ("text","new"):"text.create", ("text","mod"):"text.transform",
            ("image","new"):"image.create",("video","new"):"video.create",
            ("audio","new"):"audio.create"}

def legal_ids():
    ctx = collections.defaultdict(set)
    for r in csv.DictReader(open(CSVP)):
        ctx[r["verb"]].add(r["context"])
    return ctx
CTX_OF = legal_ids()

def snap(verb, context):
    """verb + desired context -> a LEGAL atv1 id. Never invents one."""
    ok = CTX_OF.get(verb)
    if not ok: return None
    if context in ok: return verb if context == "none" else f"{verb}.{context}"
    for fb in ("general","none"):
        if fb in ok: return verb if fb == "none" else f"{verb}.{fb}"
    return f"{verb}.{sorted(ok)[0]}"

def file_evidence(turns):
    """WRITE-side modality: what was PRODUCED, not what was read. Includes SendUserFile.files,
    which paths.PATH_INPUTS does not cover -- the gap that made John's .pptx invisible."""
    props = collections.Counter()
    for o in turns:
        c = (o.get("message") or {}).get("content")
        if not isinstance(c, list): continue
        for b in c:
            if not isinstance(b, dict) or b.get("type") != "tool_use": continue
            nm = b.get("name"); inp = b.get("input") or {}
            if nm not in NEW_TOOLS and nm not in MOD_TOOLS: continue
            kind = "new" if nm in NEW_TOOLS else "mod"
            paths = []
            for k in PATH_INPUTS:
                v = inp.get(k)
                if isinstance(v, str): paths.append(v)
            fv = inp.get("files")
            if isinstance(fv, list): paths += [x for x in fv if isinstance(x, str)]
            for p in paths:
                if "." not in os.path.basename(p): continue
                m = modality_of(os.path.splitext(p)[1].lower())
                if not m: continue
                v = MOD_VERB.get((m, kind)) or MOD_VERB.get((m, "new"))
                if v: props[v] += 1
    return props

def rank(verb_votes, file_props, context, n_sub):
    """Deterministic proposals enter at ONE SUB-WINDOW'S WEIGHT -- present in the ranking,
    never dominating it."""
    floor = 1.0 / max(n_sub, 1)
    total = sum(verb_votes.values()) or 1
    share = {v: c / total for v, c in verb_votes.items()}
    for v in file_props:
        share[v] = max(share.get(v, 0.0), floor)
    z = sum(share.values()) or 1
    out = []
    for v, s in sorted(share.items(), key=lambda kv: -kv[1]):
        i = snap(v, context)
        if i: out.append((i, s / z))
    return out
