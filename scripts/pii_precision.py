"""Measure the PRECISION of the deterministic PII detector (`POST /pii`) on a real
developer corpus.

Precision — not recall — is the stated requirement for the `sensitivity` facet: a false
`phi` on an org's dashboard is alarming and erodes trust, and is worse than a miss. That
claim was asserted through three tasks and never measured. This script measures it.

WHY THIS RENDERS MATCHED STRINGS AND NOT JUST A TABLE
-----------------------------------------------------
`ssn: 14` is unfalsifiable. Fourteen leaked SSNs and fourteen nine-digit order ids produce
the identical row. Roughly twenty defects in this study surfaced as PLAUSIBLE WRONG NUMBERS
and essentially none was caught by reading an aggregate. So the deliverable is the list of
distinct matches, each rendered as:

  * a MASK — first/last two characters, `40…65`. Enough to tell an SSN from an order id,
    not enough to leak one. Values too short to mask are elided entirely.
  * a SHAPE — digits to `D`, lower to `a`, upper to `A`, punctuation kept literally.
    `DDD-DD-DDDD` versus `DDDDDDDDD` decides the question the mask alone leaves open, and
    it carries no information about the value itself.

PRIVACY. The corpus is real transcripts. The raw matched substring is never written to a
file, never printed, and never leaves this process: `/pii` returns OFFSETS ONLY (by
design — see app/pii.scan), this script slices its own local copy of the text, and the
only thing that survives the slice is the mask, the shape and the length. Everything
durable goes outside the repo.

SAMPLE SIZE IS FIXED BEFORE THE RUN (`SAMPLE_N` below) and the sample is seeded, so the
run is reproducible and the number was not chosen after seeing a result.

Run with the sidecar venv (spaCy/presidio live there):

    ~/.keld/sidecar-venv/bin/python scripts/pii_precision.py \
        --port 34771 --out ~/keld/refseries-context/pii-precision

Start a sidecar yourself first (`sidecar/serve.py --port N`). `/pii` needs no GLiNER2 —
the inference worker must stay `down` for the whole run, and this script asserts it by
reading `/metrics` before and after.
"""
import argparse
import hashlib
import json
import os
import random
import re
import subprocess
import sys
import time
import urllib.request
from collections import Counter, defaultdict

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "sidecar"))
from app.analysis.transcript import iter_turns          # noqa: E402
from app.analysis.text import text_of, is_command_echo  # noqa: E402

# --- PRE-REGISTERED, fixed before any result was looked at -------------------------------
SAMPLE_N = 2000     # prompts. One occurrence = 0.5 per 1,000, so the "a few per thousand"
                    # false-positive threshold is resolvable with several events behind it,
                    # while the distinct-match list stays hand-inspectable.
SEED = 20260823
CORPUS = os.path.expanduser("~/keld/refseries-context/frozen-corpus")
ROOTS = ("projects", "john-projects")
# -----------------------------------------------------------------------------------------


def transcripts():
    """Every transcript in the corpus, in a stable order (the sample must be reproducible)."""
    out = []
    for r in ROOTS:
        base = os.path.join(CORPUS, r)
        for dirpath, _, names in os.walk(base):
            for n in names:
                if n.endswith(".jsonl"):
                    out.append(os.path.join(dirpath, n))
    return sorted(out)


def prompt_ordinals(path):
    """Ordinals of the GENUINE user prompts in one transcript.

    Same idiom as scripts/prose_activity.py: `iter_turns` for speech turns, `text_of` for
    the text blocks, `is_command_echo` to drop the slash-command echoes, skill injections
    and task notifications that are machine-written and not something a person typed.
    """
    out = []
    for i, o in enumerate(iter_turns(path)):
        role = o.get("message", {}).get("role") or o.get("type")
        if role != "user":
            continue
        txt = text_of(o.get("message", {}).get("content"))
        if txt.strip() and not is_command_echo(txt):
            out.append(i)
    return out


def prompt_texts(path, wanted):
    """Re-read one transcript and return {ordinal: text} for `wanted` ordinals only.

    Two passes over the corpus rather than one, deliberately: pass one builds the sampling
    frame as (path, ordinal) pairs, which costs nothing to hold; holding every prompt's
    TEXT would be hundreds of MB of real prompt content resident for no reason.
    """
    want, got = set(wanted), {}
    for i, o in enumerate(iter_turns(path)):
        if i not in want:
            continue
        got[i] = text_of(o.get("message", {}).get("content"))
    return got


def mask(v):
    """First/last two characters, or less when the value is too short to spare them.

    A 5-char value rendered 2+2 is a 1-character redaction, i.e. not one. The bands below
    keep at least a third of every rendered value hidden.
    """
    n = len(v)
    if n <= 4:
        return "…"
    if n <= 8:
        return f"{v[0]}…{v[-1]}"
    return f"{v[:2]}…{v[-2:]}"


def shape(v, cap=44):
    """The value's character classes, carrying none of its content.

    This is what actually decides most calls: `DDD-DD-DDDD` is SSN-shaped, `DDDDDDDDD` is
    an order id, `Aaaa Aaaaa` is a name and `aaaa.aaa` is an identifier.
    """
    s = "".join("D" if c.isdigit() else "a" if c.islower() else "A" if c.isupper() else c
                for c in v)
    return s if len(s) <= cap else s[:cap] + "…"


# --- Naming a false positive without leaking a true one ----------------------------------
#
# The deliverable asks for false positives to be NAMED, and privacy forbids reproducing real
# personal data. Those two only coexist if a value can be shown to be public vocabulary
# BEFORE it is rendered. Two independent tests, both conservative:
#
#   1. PUBLIC_VOCAB — a hand-written list of product/technology/company nouns. A hit means
#      the name came from this list, which was written without reading the corpus; the
#      corpus only CONFIRMED a hypothesis, it did not supply the string.
#   2. The value occurs verbatim in this repo's own git-tracked files. The corpus is largely
#      this project's development transcripts, so its vocabulary is the repo's vocabulary,
#      and a token committed to a source tree is not leaked personal data.
#
# Guarded either way: a `Firstname Lastname` shape is NEVER disclosed, however it qualifies,
# because a git trailer or an AGENTS.md credit could put a real person's name in the repo.
PUBLIC_VOCAB = {
    # models / vendors
    "Claude", "Claude Code", "Claude Opus", "Opus", "Sonnet", "Haiku", "Fable", "Anthropic",
    "OpenAI", "GPT", "Gemini", "Codex", "Cowork", "Llama", "Mistral", "GLiNER", "GLiNER2",
    "spaCy", "SpaCy", "Presidio", "PyTorch", "Torch", "HuggingFace",
    # this project
    "Keld", "Atlas", "Signal", "Agent", "Sidecar",
    # tooling / platforms
    "Docker", "Kubernetes", "Redis", "Postgres", "PostgreSQL", "GitHub", "GitLab", "Git",
    "Linux", "macOS", "Windows", "Ubuntu", "Debian", "Manjaro", "Apple", "Google", "AWS",
    "GCP", "Azure", "Python", "Golang", "Rust", "Java", "Node", "React", "FastAPI",
    "Uvicorn", "Pydantic", "PyInstaller", "Inno", "Gatekeeper", "Terraform", "Argo",
    "Prometheus", "Grafana", "Slack", "Notion", "Jira", "Stripe", "Visa", "Mastercard",
    # acronyms and constants that read as proper nouns
    "JSON", "JSONL", "HTTP", "HTTPS", "HTML", "OTEL", "OTLP", "NDJSON", "YAML", "TOML",
    "TODO", "NOTE", "FIXME", "README", "API", "CLI", "CPU", "GPU", "RAM", "RSS", "SSN",
    "PII", "PHI", "PCI", "NER", "LLM", "SDK", "UUID", "SHA", "TDD", "SDD", "MCP",
}
_NAME_SHAPE = re.compile(r"^[A-Z][a-z]+(?: [A-Z][a-z]+)+$")

# The repo-vocabulary test is NECESSARY BUT NOT SUFFICIENT, and this is the counterexample
# that proved it: one match cleared it — it occurs many times in committed docs — and is a
# real person's given name, carried into the repo inside a machine hostname. So the test is
# backstopped by manual review, and a value review rejects is pinned here by DIGEST, never
# by literal, because writing the literal into this file would be the very disclosure the
# entry exists to prevent.
NEVER_DISCLOSE = {
    # a real given name embedded in a `<Name>s-Mac-mini.local` hostname
    "60ffd0fb64c59bbee171a8f7656464c1",
}


def _digest(value):
    return hashlib.sha256(value.encode()).hexdigest()[:32]

# Words within +/- 60 chars of a match that decide what the digits actually are. Reporting
# WHICH of these fired leaks nothing (the list is fixed here, not drawn from the corpus) and
# is what separates a leaked SSN from an order id when the mask cannot.
CONTEXT_WORDS = (
    "ssn", "social security", "credit", "card", "payment", "invoice", "billing",
    "phone", "call", "tel", "mobile", "address", "street", "zip", "email",
    "order", "account", "customer", "patient", "employee",
    "port", "pid", "epoch", "timestamp", "commit", "sha", "hash", "uuid", "version",
    "line", "byte", "chars", "token", "ms", "seconds", "id=", "test", "example",
)

_REPO_TEXT = [None]


def repo_text():
    """Every git-tracked text file in this repo, concatenated once, for the vocabulary test."""
    if _REPO_TEXT[0] is None:
        root = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..")
        try:
            names = subprocess.run(["git", "-C", root, "ls-files"], capture_output=True,
                                   text=True, check=True).stdout.split("\n")
        except Exception:
            names = []
        buf = []
        for nm in names:
            p = os.path.join(root, nm)
            if not nm or not os.path.isfile(p) or os.path.getsize(p) > 2_000_000:
                continue
            try:
                buf.append(open(p, errors="replace").read())
            except Exception:
                pass
        _REPO_TEXT[0] = "\n".join(buf)
    return _REPO_TEXT[0]


def disclosable(value):
    """May this matched string be written down in full? See the block comment above."""
    if _NAME_SHAPE.match(value) or _digest(value) in NEVER_DISCLOSE:
        return False
    if value in PUBLIC_VOCAB:
        return True
    # A single token, present in the repo's own committed text. Multi-word phrases and
    # digit-bearing values are excluded: a phrase is likelier to be prose a person wrote,
    # and a number is exactly the class this study must not print.
    if " " in value or any(c.isdigit() for c in value) or len(value) < 2:
        return False
    return repo_text().count(value) >= 3


def context_words(text, start, end, window=60):
    lo = text[max(0, start - window):start].lower()
    hi = text[end:end + window].lower()
    return sorted({w for w in CONTEXT_WORDS if w in lo or w in hi})


def luhn(d):
    if not d or not d.isdigit():
        return False
    tot, alt = 0, False
    for c in reversed(d):
        n = int(c)
        if alt:
            n *= 2
            if n > 9:
                n -= 9
        tot += n
        alt = not alt
    return tot % 10 == 0


def enclosing_token_shape(text, start, end):
    """The shape of the whitespace-delimited token the match sits inside.

    Decides whether a 13-digit `credit_card` is a standalone number or a fragment sliced out
    of a longer id — the single most useful fact about a numeric false positive, and it
    carries no content.
    """
    lo = start
    while lo > 0 and not text[lo - 1].isspace():
        lo -= 1
    hi = end
    while hi < len(text) and not text[hi].isspace():
        hi += 1
    return shape(text[lo:hi], cap=52), (lo != start or hi != end)


def scan(port, text, timeout=120):
    req = urllib.request.Request(f"http://127.0.0.1:{port}/pii",
                                 json.dumps({"text": text}).encode(),
                                 {"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        return json.load(resp)


def metrics(port):
    with urllib.request.urlopen(f"http://127.0.0.1:{port}/metrics", timeout=30) as resp:
        return json.load(resp)


def run(port, out_dir):
    os.makedirs(out_dir, exist_ok=True)
    files = transcripts()
    print(f"corpus: {len(files)} transcripts under {CORPUS}", flush=True)

    frame = []
    t0 = time.time()
    for fi, path in enumerate(files):
        try:
            for ordinal in prompt_ordinals(path):
                frame.append((fi, ordinal))
        except Exception as e:
            print(f"  skip {os.path.basename(path)}: {type(e).__name__}", flush=True)
        if (fi + 1) % 100 == 0:
            print(f"  framed {fi+1}/{len(files)} files, {len(frame)} prompts "
                  f"({time.time()-t0:.0f}s)", flush=True)
    print(f"frame: {len(frame)} genuine user prompts ({time.time()-t0:.0f}s)", flush=True)

    n = min(SAMPLE_N, len(frame))
    sample = random.Random(SEED).sample(frame, n)
    print(f"sample: {n} prompts (SAMPLE_N={SAMPLE_N}, seed={SEED})", flush=True)

    by_file = defaultdict(list)
    for fi, ordinal in sample:
        by_file[fi].append(ordinal)

    before = metrics(port)
    findings = defaultdict(lambda: {"count": 0, "scores": set(), "files": set(),
                                    "ctx": set(), "tokshape": set(), "partial": False})
    per_type_spans = Counter()
    per_type_prompts = Counter()
    rollup = Counter()
    scanned = truncated = failed = 0
    chars = 0
    t0 = time.time()

    for fi in sorted(by_file):
        path = files[fi]
        texts = prompt_texts(path, by_file[fi])
        tid = os.path.basename(path)[:8]
        for ordinal in sorted(by_file[fi]):
            text = texts.get(ordinal, "")
            if not text.strip():
                continue
            try:
                res = scan(port, text)
            except Exception as e:
                failed += 1
                print(f"  scan failed {tid}#{ordinal}: {type(e).__name__} {str(e)[:60]}",
                      flush=True)
                continue
            scanned += 1
            chars += len(text)
            if res.get("truncated"):
                truncated += 1
            seen_types = set()
            for sp in res.get("spans", []):
                # The raw value exists only inside this loop body. Nothing derived from it
                # beyond mask/shape/length ever leaves the process.
                value = text[sp["start"]:sp["end"]]
                kind = sp["type"]
                per_type_spans[kind] += 1
                seen_types.add(kind)
                rec = findings[(kind, value)]
                rec["count"] += 1
                rec["scores"].add(round(float(sp["score"]), 2))
                rec["files"].add(tid)
                rec["ctx"].update(context_words(text, sp["start"], sp["end"]))
                ts, partial = enclosing_token_shape(text, sp["start"], sp["end"])
                rec["tokshape"].add(ts)
                rec["partial"] = rec["partial"] or partial
            for k in seen_types:
                per_type_prompts[k] += 1
            # What the ORG actually sees. `sensitivity` is a rollup, not a list of types:
            # internal/agent/enrich/labels.go -> SensitivityFromEntity, first match wins,
            # order encodes severity (phi > pci > secrets > pii). Credentials come from the
            # Go-side gitleaks layer and are out of scope here, so `secrets` cannot appear.
            if "ssn" in seen_types:
                rollup["phi"] += 1
            elif "credit_card" in seen_types:
                rollup["pci"] += 1
            elif seen_types:
                rollup["pii"] += 1
            else:
                rollup["none"] += 1
            if scanned % 250 == 0:
                el = time.time() - t0
                print(f"  scanned {scanned}/{n} ({el:.0f}s, {el/scanned*1000:.0f}ms/prompt)",
                      flush=True)

    after = metrics(port)
    stats = {
        "sample_n": SAMPLE_N, "seed": SEED, "corpus": CORPUS,
        "transcripts": len(files), "frame_prompts": len(frame),
        "scanned": scanned, "scan_failures": failed, "truncated": truncated,
        "mean_prompt_chars": round(chars / scanned, 1) if scanned else 0,
        "elapsed_s": round(time.time() - t0, 1),
        "worker_state_before": before["worker"]["state"],
        "worker_state_after": after["worker"]["state"],
        "worker_submitted_before": before["counts"]["submitted"],
        "worker_submitted_after": after["counts"]["submitted"],
        "pii_served": after["counts"].get("pii_served"),
        "pii_failed": after["counts"].get("pii_failed"),
        "per_type_spans": dict(per_type_spans),
        "per_type_prompts": dict(per_type_prompts),
        "rate_per_1000_prompts": {k: round(v * 1000 / scanned, 2)
                                  for k, v in sorted(per_type_prompts.items())} if scanned else {},
        "span_rate_per_1000_prompts": {k: round(v * 1000 / scanned, 2)
                                       for k, v in sorted(per_type_spans.items())} if scanned else {},
        "distinct_matches": len(findings),
        "published_sensitivity": dict(rollup),
        "published_sensitivity_per_1000": {k: round(v * 1000 / scanned, 2)
                                           for k, v in sorted(rollup.items())} if scanned else {},
    }

    rows = []
    for (kind, value), rec in findings.items():
        digits = "".join(c for c in value if c.isdigit())
        rows.append({"type": kind,
                     # The ONLY path by which a raw matched value can reach a file, and only
                     # after `disclosable` has shown it to be public vocabulary.
                     "name": value if disclosable(value) else None,
                     "mask": mask(value), "shape": shape(value), "chars": len(value),
                     "count": rec["count"], "scores": sorted(rec["scores"]),
                     "transcripts": len(rec["files"]),
                     "example_transcript": sorted(rec["files"])[0],
                     "context": sorted(rec["ctx"]),
                     "token_shapes": sorted(rec["tokshape"])[:3],
                     "sliced_from_longer_token": rec["partial"],
                     # How often the value occurs in this repo's own committed text. A
                     # scalar, so it is safe for every match including the undisclosed
                     # ones: it separates project vocabulary (high) from a string a person
                     # typed once into a prompt (zero).
                     "repo_hits": repo_text().count(value) if len(value) > 1 else -1,
                     "n_digits": len(digits),
                     "luhn": luhn(digits) if digits else False})
    rows.sort(key=lambda r: (r["type"], -r["count"], r["mask"]))

    with open(os.path.join(out_dir, "stats.json"), "w") as f:
        json.dump(stats, f, indent=2)
    with open(os.path.join(out_dir, "findings.jsonl"), "w") as f:
        for r in rows:
            f.write(json.dumps(r) + "\n")

    print(json.dumps(stats, indent=2))
    print(f"\n{len(rows)} distinct matches -> {out_dir}/findings.jsonl")
    return stats, rows


def render(out_dir):
    """Print every distinct match as a markdown table, grouped by type. Masked only."""
    rows = [json.loads(l) for l in open(os.path.join(out_dir, "findings.jsonl"))]
    for kind in sorted({r["type"] for r in rows}):
        sub = [r for r in rows if r["type"] == kind]
        print(f"\n### `{kind}` — {len(sub)} distinct, {sum(r['count'] for r in sub)} occurrences\n")
        print("| value | shape | chars | n | txs | scores | enclosing token | context words |")
        print("|---|---|---|---|---|---|---|---|")
        for r in sub:
            sc = ",".join(str(x) for x in r["scores"])
            shown = r["name"] or r["mask"]
            tok = (r["token_shapes"] or [""])[0]
            tok += " (sliced)" if r["sliced_from_longer_token"] else ""
            ctx = ", ".join(r["context"][:6])
            print(f"| `{shown}` | `{r['shape']}` | {r['chars']} | {r['count']} | "
                  f"{r['transcripts']} | {sc} | `{tok}` | {ctx} |")


if __name__ == "__main__":
    ap = argparse.ArgumentParser()
    ap.add_argument("--port", type=int)
    ap.add_argument("--out", default=os.path.expanduser("~/keld/refseries-context/pii-precision"))
    ap.add_argument("--render", action="store_true", help="re-render findings.jsonl only")
    a = ap.parse_args()
    if a.render:
        render(a.out)
    elif a.port:
        run(a.port, a.out)
    else:
        ap.error("--port is required unless --render")
