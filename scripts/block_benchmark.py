#!/usr/bin/env python3
"""The block benchmark manifest: one row per real block, keyed by identity, with the
input/no-input split and every labelling made so far.

    python scripts/block_benchmark.py build      # (re)generate from local Atlas + label files
    python scripts/block_benchmark.py show       # summarise the manifest

Consumers import `load_benchmark(with_input_only=True)` and get the 64 blocks a text-based
scorer can legitimately be judged on. The 25 without input are kept in the file, flagged, so
the episode-membership work has its own held-out set — they are excluded from text-scoring
benchmarks by default rather than dropped.

⚠️ KEYED BY (session, block start), NEVER BY POSITION. The first benchmark run keyed blocks by
their index in an ordered query; the smoke daemon published during the session, new rows
sorted into the middle, and every label after them pointed at a different block. A block's
identity is what Atlas upserts on, and it is the only key that survives a growing table.

⚠️ `has_input` IS A FACT ABOUT THE TRANSCRIPT, NOT A LABEL. It is whether any USER-stream
message falls inside the block's half-open span — the same read `_span_texts` performs — so
it is recomputed on every `build`, never hand-edited. A block with no input cannot be scored
by any text-based arm, and counting it as a miss would blame the scorer for a structural gap.

Labels carry provenance, as the learning-loop note requires for tags:
  context  — labelled with full knowledge of what the work was (silver, not gold)
  blind    — labelled from block text plus the repo line, no other context
  norepo   — labelled from block text ALONE, repo line ignored; `?` = undecidable
None of the three is a human verdict. The owner's pass is the missing gold; when it exists it
goes in as `human`.

Holds identifiers, counts, minutes and project ids only — no message text.
"""
from __future__ import annotations

import glob
import json
import os
import re
import subprocess
import sys
from datetime import datetime

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(ROOT, "sidecar"))
MANIFEST = os.path.join(ROOT, "scripts", "testdata", "block_benchmark.jsonl")
CONTAINER = "keld-atlas-postgres-1"

#: Where the labelling passes were written. Scratchpad paths are session-specific, so the
#: manifest is the durable copy; these are read only on `build` and only if present.
LABEL_DIR = os.environ.get("KELD_BENCH_LABELS", "")
LABEL_INPUT = os.path.join(LABEL_DIR, "LABEL_INPUT.md")
LABEL_FILES = {"context": "labels_context.tsv", "blind": "labels_blind.tsv",
               "norepo": "labels_norepo.tsv"}


def psql(sql):
    r = subprocess.run(["docker", "exec", "-i", CONTAINER, "psql", "-U", "keld", "-d",
                        "keld", "-t", "-A", "-F", "\t", "-c", sql],
                       capture_output=True, text=True)
    if r.returncode != 0:
        raise SystemExit(f"psql failed: {r.stderr.strip()}")
    return [l for l in r.stdout.splitlines() if l.strip()]


def pg_ts(s):
    t = s.strip().replace(" ", "T")
    if re.search(r"[+-]\d{2}$", t):
        t += ":00"
    try:
        return datetime.fromisoformat(t)
    except ValueError:
        return None


def block_key(session_id, start_ts):
    return f"{session_id[:8]}@{start_ts[:16]}"


def read_labels():
    """B-number -> key via LABEL_INPUT.md, then each label file -> {key: labels}."""
    if not LABEL_DIR or not os.path.exists(LABEL_INPUT):
        return {}
    bkey = {}
    for line in open(LABEL_INPUT):
        m = re.match(r"### (B\d+) · (\d{4}-\d\d-\d\d \d\d:\d\d) · \S+ · session `(\w+)`", line)
        if m:
            bkey[m.group(1)] = f"{m.group(3)}@{m.group(2)}"
    out = {}
    for prov, fname in LABEL_FILES.items():
        path = os.path.join(LABEL_DIR, fname)
        if not os.path.exists(path):
            continue
        for line in open(path):
            p = line.rstrip("\n").split("\t")
            if p[0].startswith("B") and p[0] in bkey:
                out.setdefault(bkey[p[0]], {})[prov] = sorted(
                    x.strip() for x in p[1].split(",") if x.strip())
    return out


def build():
    from app.analysis import textembed
    from app.analysis.capture import epoch
    from app.analysis.transcript import iter_turns

    labels = read_labels()
    rows = psql("select session_id, start_ts, end_ts, coalesce(dimensions::text,'{}'),"
                " coalesce(raw->>'projects','[]') from blocks order by session_id, start_ts;")
    out, skipped = [], 0
    for r in rows:
        sid, start, end, dims, projs = r.split("\t")
        pj = json.loads(projs or "[]")
        if sid in ("contract", "test", "smoke") or any(
                p.get("id", "").startswith("proj_read_only") for p in pj):
            skipped += 1
            continue
        a, b = pg_ts(start), pg_ts(end)
        n_msgs = None
        hits = glob.glob(os.path.expanduser(f"~/.claude/projects/*/{sid}.jsonl"))
        if hits and a and b:
            try:
                n_msgs = sum(1 for m in textembed.messages_in(iter_turns(hits[0]), epoch)
                             if m.stream == textembed.USER
                             and a.timestamp() <= m.t < b.timestamp())
            except Exception:      # noqa: BLE001
                n_msgs = None
        d = json.loads(dims or "{}")
        proj = d.get("project", {}) if isinstance(d.get("project"), dict) else {}
        key = block_key(sid, start)
        out.append({
            "key": key,
            "session": sid,
            "start": start,
            "end": end,
            "minutes": round((b - a).total_seconds() / 60) if a and b else None,
            "has_input": (n_msgs or 0) > 0 if n_msgs is not None else None,
            "user_messages": n_msgs,
            "workspace_dim": proj.get("value"),
            "labels": labels.get(key, {}),
        })
    os.makedirs(os.path.dirname(MANIFEST), exist_ok=True)
    with open(MANIFEST, "w") as fh:
        for row in out:
            fh.write(json.dumps(row, ensure_ascii=False) + "\n")
    print(f"wrote {len(out)} blocks to {os.path.relpath(MANIFEST, ROOT)} "
          f"({skipped} synthetic rows skipped)")
    show()


def load_benchmark(with_input_only=True, require_label=None):
    """The blocks a benchmark may score. `require_label` names a provenance ('context',
    'blind', 'norepo') that must be present and not '?' for the row to be returned."""
    rows = [json.loads(l) for l in open(MANIFEST)]
    if with_input_only:
        rows = [r for r in rows if r["has_input"]]
    if require_label:
        rows = [r for r in rows if require_label in r["labels"]
                and "?" not in r["labels"][require_label]]
    return rows


def show():
    rows = [json.loads(l) for l in open(MANIFEST)]
    wi = [r for r in rows if r["has_input"]]
    wo = [r for r in rows if r["has_input"] is False]
    unk = [r for r in rows if r["has_input"] is None]
    print(f"\n{len(rows)} real blocks")
    print(f"  with input:    {len(wi):>3}  {sum(r['minutes'] or 0 for r in wi):>5} min"
          f"   <- text-based benchmarks run on these")
    print(f"  without input: {len(wo):>3}  {sum(r['minutes'] or 0 for r in wo):>5} min"
          f"   <- held out for episode-membership")
    if unk:
        print(f"  unreadable:    {len(unk):>3}")
    for prov in ("context", "blind", "norepo", "human"):
        n = sum(1 for r in rows if prov in r["labels"])
        q = sum(1 for r in rows if r["labels"].get(prov) == ["?"])
        if n:
            print(f"  labels[{prov}]: {n} blocks" + (f" ({q} undecidable)" if q else ""))
    unl = sum(1 for r in wi if not r["labels"])
    if unl:
        print(f"  with-input blocks still UNLABELLED: {unl}")


if __name__ == "__main__":
    cmd = sys.argv[1] if len(sys.argv) > 1 else "show"
    {"build": build, "show": show}[cmd]()
