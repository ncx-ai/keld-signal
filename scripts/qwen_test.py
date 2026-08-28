#!/usr/bin/env python3
"""Run one prompt against one transcript window.

    scripts/qwen_test.py prompt.md windows/window_01.txt
    scripts/qwen_test.py prompt.md windows/window_01.txt --record windows/record.txt
    scripts/qwen_test.py prompt.md windows/*.txt          # every window, same prompt
    scripts/qwen_test.py prompt.md w.txt --show-prompt    # print exactly what was sent

The prompt file holds BOTH the system prompt and the instruction, separated by a line that is
exactly `---`:

    You are ...                 <- system prompt (everything above the first `---`)
    ---
    ... instruction ...         <- user message (everything below)
    {{RECORD}}
    {{WINDOW}}

With no `---`, the whole file is the user message and no system prompt is sent.

Placeholders, substituted anywhere in either half:

    {{WINDOW}}   the window file's contents
    {{RECORD}}   the --record file's contents (empty string if not given)

If `{{WINDOW}}` appears nowhere, the window is appended to the end of the user message, so a
bare instruction file still works.

Needs llama-server on 127.0.0.1:8099 (override with --url or $DIGEST_URL). Stdlib only.
"""

import argparse
import contextlib
import glob
import json
import os
import re
import socket
import subprocess
import threading
import sys
import time
import urllib.error
import urllib.request

DEFAULT_URL = os.environ.get("DIGEST_URL", "http://127.0.0.1:8099")
GGUF_DIR = os.path.expanduser("~/.keld/models/gguf")

# Short names for --model, so a comparison run is one word rather than a path. Licence is
# recorded beside each because it is a shipping constraint, not trivia: these weights are
# downloaded onto a customer's machine, and a non-OSI licence carries redistribution
# obligations that reach into Keld's own customer agreement.
MODELS = {
    "qwen-4b":     ("Qwen3-4B-Instruct-2507-Q4_K_M.gguf",  "Apache-2.0", "Alibaba, CN"),
    "qwen-4b-q3":  ("Qwen3-4B-Instruct-2507-Q3_K_M.gguf",  "Apache-2.0", "Alibaba, CN"),
    "qwen-1.7b":   ("Qwen3-1.7B-Q4_K_M.gguf",              "Apache-2.0", "Alibaba, CN"),
    "qwen-0.6b":   ("Qwen3-0.6B-Q4_K_M.gguf",              "Apache-2.0", "Alibaba, CN"),
    "granite-3b":  ("granite-4.1-3b-Q4_K_M.gguf",          "Apache-2.0", "IBM, US"),
    "granite-8b":  ("granite-4.1-8b-Q4_K_M.gguf",          "Apache-2.0", "IBM, US"),
}
DEFAULT_MODEL = "qwen-4b"


def resolve_model(name):
    """Accept a short name from MODELS, or a path to any .gguf."""
    if name in MODELS:
        filename, licence, origin = MODELS[name]
        return os.path.join(GGUF_DIR, filename), f"{name} ({licence}, {origin})"
    if name.endswith(".gguf"):
        return os.path.expanduser(name), os.path.basename(name)
    known = ", ".join(sorted(MODELS))
    sys.exit(f"unknown model {name!r}\nknown names: {known}\n"
             f"or pass a path ending in .gguf")


def list_models():
    print(f"{'name':<12} {'present':<8} {'licence':<12} origin")
    for name, (filename, licence, origin) in sorted(MODELS.items()):
        path = os.path.join(GGUF_DIR, filename)
        size = f"{os.path.getsize(path) / 1e9:.1f}G" if os.path.exists(path) else "-"
        print(f"{name:<12} {size:<8} {licence:<12} {origin}")
    print(f"\nin {GGUF_DIR}")

# The production beat schema, for --schema beat. Constrained decoding is what makes the output
# shape reliable; without it the model answers in whatever form it likes, which is fine when you
# are iterating on wording and misleading when you are judging output shape.
BEAT_SCHEMA = {
    "type": "object",
    "properties": {
        "subject": {"type": "string"},
        "events": {"type": "array", "items": {"type": "string"}, "minItems": 1, "maxItems": 4},
    },
    "required": ["subject", "events"],
    "additionalProperties": False,
}



# Frustration as atomic observations, scored in code.
#
# A 1-10 degree judgement from a 3B model collapses: this scale sat pinned at 2, then at 8, then
# at 4 across prompt revisions that never touched its wording. The literature on LLM-as-judge
# says the same thing — a Likert rubric without exemplars drifts to the centre, and decomposing
# it into binary criteria measurably improves agreement (BinEval, boolean verification). Each
# question below is a thing a reader can check in the text; the arithmetic is ours, so it cannot
# drift at all.
#
# `quoting_not_speaking` exists because of a real failure: a message that QUOTED the words
# "FUCKING" and "looks HORRIBLE" while describing someone else's transcript scored 8. Use versus
# mention is invisible at the token level, but it is easy to ask about directly.
TONE_QUESTIONS = {
    "swears_in_own_voice":   ("does the engineer swear, in their own voice?", 3),
    "shouts_in_capitals":    ("do they use CAPITALS or repeated punctuation for emphasis?", 1),
    "criticises_the_output": ("do they say that what was produced for them is bad?", 3),
    "rejects_outright":      ("do they reject something flatly — 'no', 'wrong', 'start over'?", 2),
    "repeats_themselves":    ("do they have to give the same instruction or correction twice?", 2),
    "insults_the_assistant": ("do they insult the assistant personally?", 2),
    "threatens_to_stop":     ("do they threaten to abandon the work or the tool?", 2),
    "quoting_not_speaking":  ("are the charged words QUOTED from somewhere else — another "
                              "transcript, a log, someone else's message — rather than the "
                              "engineer's own feeling right now?", 0),
}


def band(markers, ladder):
    """How many distinct things were observed -> a score, non-linearly.

    Summing per-marker weights saturates: one sharp sentence can fire swearing, capitals,
    criticism and an insult at once, which added to 10 — a score meant for someone threatening
    to abandon the work. Counting DISTINCT observations and mapping them through a ladder keeps
    a single vivid message near the middle, where it belongs, and reserves the top for a stretch
    that shows several different things going wrong.
    """
    return ladder[min(markers, len(ladder) - 1)]


def tone_score(tone):
    """1-10 from the observations. Quoted charge is not the engineer's own feeling."""
    quoting = bool(tone.get("quoting_not_speaking"))
    ignored = {"swears_in_own_voice", "shouts_in_capitals"} if quoting else set()
    markers = [k for k in TONE_QUESTIONS
               if k != "quoting_not_speaking" and tone.get(k) and k not in ignored]
    score = band(len(markers), [1, 3, 5, 6, 7, 8])
    # The top of the scale is not "very cross" — it is "about to stop working with this".
    if tone.get("threatens_to_stop"):
        score = min(score + 2, 10)
    return score


DEMAND_QUESTIONS = {
    # Each is a thing a reader can point at. The previous set asked "are constraints pulling
    # against each other?" and similar — judgements wearing a boolean's clothes, answered `true`
    # five times of five on two windows of three. Atomicity that matters is whether the answer
    # can be CHECKED, not whether it is phrased as a question.
    #
    # Written to hold outside engineering: a month-end close, a campaign plan and a migration
    # all have things that did not come out right, things redone, and figures that had to agree.
    "something_went_wrong":  "an error, a failure, a number that did not agree, work rejected",
    "work_was_redone":       "something earlier revisited, reversed, or replaced",
    "two_things_reconciled": "two separate things made to agree",
    "specialist_vocabulary": "terms specific to a trade a general reader would not know",
    "spans_separate_parts":  "work reaching across parts that are usually handled separately — "
                             "an interface and the service behind it, a document and the system "
                             "it feeds, two teams' territory",
}

# Not asked, counted. The model listed five named subjects on a window and then answered "three
# or more distinct named things?" with false — a contradiction inside one response. Anything the
# harness can count, the harness counts.
SUBJECT_BREADTH = 3


def demand_score(demand, subjects=()):
    markers = sum(1 for k in DEMAND_QUESTIONS if demand.get(k))
    if len(subjects) >= SUBJECT_BREADTH:
        markers += 1
    return band(markers, [1, 3, 5, 6, 8, 9])


def unverified_subjects(subjects, window, record):
    """Subjects that appear in neither the window nor the record.

    A subject is a thing the session names. If the text does not contain it, the model supplied
    it — which is the one failure a list of specifics must not have. Checked here rather than
    asked for, because every field this model is asked to police it fills instead.
    """
    hay = (window + "\n" + record).lower()
    missing = []
    for sub in subjects:
        # A subject may be a phrase with a parenthetical; require its longest bare token to
        # occur, which catches invention without failing on ordinary rewording.
        tokens = [t.strip("(),.:;`'\"") for t in sub.split()]
        tokens = [t for t in tokens if len(t) >= 4]
        if tokens and not any(t.lower() in hay for t in tokens):
            missing.append(sub)
    return missing


def routing_schema(with_frustration):
    """The routing payload, shape-guaranteed by constrained decoding rather than by asking.

    `working_out` comes FIRST because field order is generation order: the model quotes the
    engineer's own words before it scores them, which is what stopped it scoring its own
    sanitised paraphrase (frustration 2 -> 8 on the same window). It is stripped before anything
    is shown or written — raw prompt text is read on-device and must never be published.

    `frustration` is absent from the schema entirely when the engineer said nothing in the
    stretch. Asked, the model answers: told in capitals there was nothing to judge, it still
    returned 7, quoting the assistant. A field that does not exist cannot be filled.
    """
    scores, required = {}, []
    # kind and activity are enums because a free-text field gets filled with whatever is
    # nearest: unconstrained, the model answered kind "debugging" (an activity) and domain
    # "software engineering (backend services, data pipelines)" (a kind, and a list). activity
    # includes null because "several things at once" is a real answer and the model will not
    # volunteer it — the same absence that made it score frustration for a stretch with no
    # engineer in it.
    work = {
        "domain": {"type": "string"},
        "subjects": {"type": "array", "items": {"type": "string"}, "minItems": 1, "maxItems": 5},
        "activity": {"type": ["string", "null"],
                     "enum": ["debugging", "building", "refactoring", "reviewing", "testing",
                              "deploying", "configuring", "investigating", "planning",
                              "documenting", None]},
    }
    props = {"demand": {
        "type": "object",
        "properties": {k: {"type": "boolean"} for k in DEMAND_QUESTIONS},
        "required": list(DEMAND_QUESTIONS),
        "additionalProperties": False,
    }}
    if with_frustration:
        props["tone"] = {
            "type": "object",
            "properties": {k: {"type": "boolean"} for k in TONE_QUESTIONS},
            "required": list(TONE_QUESTIONS),
            "additionalProperties": False,
        }
    return {
        "type": "object",
        "properties": {
            "working_out": {"type": "string"},
            **props,
            "project": {"type": ["string", "null"]},
            "bullets": {"type": "array", "items": {"type": "string"},
                        "minItems": 3, "maxItems": 5},
            "work": {"type": "object", "properties": work,
                     "required": ["domain", "subjects", "activity"],
                     "additionalProperties": False},
        },
        "required": (["working_out"] + (["tone"] if with_frustration else [])
                     + ["project", "bullets", "work", "demand"]),
        "additionalProperties": False,
    }


def as_yaml(obj):
    """Just enough YAML for this fixed shape — no dependency for four keys."""
    out = [f"project: {obj.get('project') if obj.get('project') is not None else 'null'}",
           "bullets:"]
    out += [f"  - {b}" for b in obj.get("bullets", [])]
    if obj.get("work"):
        out.append("work:")
        for k, v in obj["work"].items():
            if isinstance(v, list):
                out.append(f"  {k}:")
                out += [f"    - {x}" for x in v]
            else:
                out.append(f"  {k}: {v if v is not None else 'null'}")
    for key in ("tone", "demand"):
        if obj.get(key):
            marks = [k for k, v in obj[key].items() if v]
            if marks:
                out.append(f"{key}: " + ", ".join(marks))
    if obj.get("unverified_subjects"):
        out.append("unverified_subjects:")
        out += [f"  - {x}" for x in obj["unverified_subjects"]]
    out.append("scores:")
    out += [f"  {k}: {v}" for k, v in obj.get("scores", {}).items()]
    return "\n".join(out)



def rss_mb(pid):
    """Resident set size in MB, from /proc. Linux only, which is where this runs."""
    try:
        with open(f"/proc/{pid}/status", encoding="utf-8") as fh:
            for line in fh:
                if line.startswith("VmRSS:"):
                    return int(line.split()[1]) / 1024
    except OSError:
        pass
    return 0.0


def anon_mb(pid):
    """The ANONYMOUS resident set — RSS minus the file-backed pages.

    A gguf is mmapped, so most of the weights show up in VmRSS as file-backed pages the kernel
    can evict under pressure. Anonymous memory cannot be evicted, only swapped. Quoting one when
    the other was measured is how two 'RAM usage' figures for the same model disagree by 700 MB,
    so both are printed and labelled.
    """
    try:
        with open(f"/proc/{pid}/smaps_rollup", encoding="utf-8") as fh:
            for line in fh:
                if line.startswith("Anonymous:"):
                    return int(line.split()[1]) / 1024
    except OSError:
        pass
    return 0.0


class PeakRSS(threading.Thread):
    """Sample a process's RSS while it works, and keep the peak.

    Peak, not a reading taken afterwards: the sidecar work on this branch measured RSS
    oscillating between 2,715 MB and 5,692 MB against a 3,409 MB ceiling while a
    between-jobs sample showed it healthy. An instantaneous number taken when the model is
    idle is exactly the measurement that hid that.

    Only meaningful under --cpu. With GPU offload most of the weights live in VRAM and RSS
    understates the true footprint, so the caller does not print it.
    """

    def __init__(self, pid, interval=0.2):
        super().__init__(daemon=True)
        self.pid, self.interval, self._stop = pid, interval, threading.Event()
        self.peak = self.peak_anon = 0.0

    def run(self):
        while not self._stop.is_set():
            self.peak = max(self.peak, rss_mb(self.pid))
            self.peak_anon = max(getattr(self, "peak_anon", 0.0), anon_mb(self.pid))
            self._stop.wait(self.interval)

    def stop(self):
        self.peak = max(self.peak, rss_mb(self.pid))
        self.peak_anon = max(self.peak_anon, anon_mb(self.pid))
        self._stop.set()


@contextlib.contextmanager
def spawn_server(model, label, cpu, threads, ctx, quiet, flash='off', ubatch=512):
    """Run a private llama-server for one model, and stop it after.

    TWO INDEPENDENT REASONS to spawn, kept separate because conflating them silently costs you
    either speed or the right to quote a number:

      * a model other than the one --url already serves. Weights are a property of the server
        process, so there is no way to ask a running server for different ones. This says
        nothing about which device to use — spawning defaults to whatever llama.cpp picks,
        which means GPU when there is one.
      * --cpu, which pins it to CPU at a given thread count with --cache-ram bounded and
        --no-repack. Those three are the viability configuration the ~3.1 GB figure at ctx 8192
        was measured under, and they are what makes a latency number quotable. A GPU run, or a
        run at 18 threads, tells you nothing about a laptop.
    """
    if not os.path.exists(model):
        sys.exit(f"no model at {model} — pass --model")
    with socket.socket() as s:                      # let the OS pick a free port
        s.bind(("127.0.0.1", 0))
        port = s.getsockname()[1]
    # Pinned on BOTH backends, because the target platform is CPU and a GPU run is only useful
    # if it predicts what CPU will do. Left at their defaults these diverge structurally rather
    # than numerically: -fa resolves to `auto`, which means a different attention implementation
    # on CUDA than on CPU, and the batch sizes set the order floating-point sums accumulate in.
    # Same kernels and same batching is the most that can be equalised from outside.
    cmd = ["llama-server", "-m", model, "--ctx-size", str(ctx), "--parallel", "1",
           "--no-warmup", "--jinja", "--port", str(port),
           "--flash-attn", flash, "--batch-size", str(ubatch), "--ubatch-size", str(ubatch)]
    if cpu:
        cmd += ["--device", "none", "--threads", str(threads),
                "--cache-ram", "512", "--no-repack"]
        where = f"CPU-only, {threads} threads"
    else:
        where = "default device (GPU if available)"
    print(f"[starting server: {label}, {where}, ctx {ctx}, port {port}]")
    log = subprocess.DEVNULL if quiet else None
    proc = subprocess.Popen(cmd, stdout=log, stderr=log)
    url = f"http://127.0.0.1:{port}"
    try:
        for _ in range(600):                         # model load is slow on CPU; wait it out
            if proc.poll() is not None:
                sys.exit(f"llama-server exited with {proc.returncode} — rerun without --quiet")
            try:
                with urllib.request.urlopen(url + "/health", timeout=2):
                    break
            except (urllib.error.URLError, OSError):
                time.sleep(1)
        else:
            sys.exit("llama-server did not become healthy within 10 minutes")
        note = ("these timings are quotable" if cpu else
                "NOT a viability configuration — do not quote these timings")
        loaded = rss_mb(proc.pid)
        sampler = PeakRSS(proc.pid)
        sampler.start()
        print(f"[ready — {label}, {where}; {note}]")
        if cpu:
            print(f"[RSS after load: {loaded:,.0f} MB]")
        try:
            yield url, sampler
        finally:
            sampler.stop()
            if cpu:
                print(f"[peak during run: RSS {sampler.peak:,.0f} MB "
                  f"(incl. mmapped weights) · anonymous {sampler.peak_anon:,.0f} MB "
                  f"(not evictable)]")
    finally:
        proc.terminate()
        try:
            proc.wait(timeout=20)
        except subprocess.TimeoutExpired:
            proc.kill()
        print("[server stopped]")


def split_prompt(text):
    """Everything above the first line that is exactly `---` is the system prompt."""
    lines = text.splitlines()
    for i, line in enumerate(lines):
        if line.strip() == "---":
            return "\n".join(lines[:i]).strip(), "\n".join(lines[i + 1:]).strip()
    return "", text.strip()


def user_turns(window):
    """Only the engineer's own lines, or "" when they said nothing in this stretch.

    For any judgement ABOUT the engineer — tone, frustration, what they asked for — handing the
    model the whole window hands it the assistant's words too, and it will quote those as the
    engineer's. Telling it not to does not work: an instruction naming `user:` twice and
    forbidding `assistant:` outright still produced an assistant quote scored as engineer
    frustration. Removing the material is the fix; forbidding it is not.
    """
    return "\n".join(l for l in window.splitlines() if l.startswith("user: "))


def strip_conditionals(text, has_user_turns):
    """Remove prompt sections that do not apply to this window.

        {{#IF_USER_TURNS}} ...asked only when the engineer spoke... {{/IF_USER_TURNS}}
        {{#IF_NO_USER_TURNS}} ...asked only when they did not... {{/IF_NO_USER_TURNS}}

    This exists because a small model fills every field it is given. Told in capitals that a
    stretch contains no engineer messages and that there is nothing to judge, it still returned
    "FRUSTRATION SCORE: 7", quoting the assistant. It has no not-applicable mode — the same
    absence that left it unable to say "I cannot tell how far along this is", and that left the
    entity pass's `noise` option unused 399 times in 400. A question it is never asked is the
    only one it cannot answer wrongly.
    """
    for tag, keep in (("IF_USER_TURNS", has_user_turns),
                      ("IF_NO_USER_TURNS", not has_user_turns)):
        pattern = re.compile(r"\{\{#" + tag + r"\}\}(.*?)\{\{/" + tag + r"\}\}", re.DOTALL)
        text = pattern.sub((lambda m: m.group(1)) if keep else "", text)
    return text


def fill(text, window, record):
    turns = user_turns(window)
    text = strip_conditionals(text, has_user_turns=bool(turns))
    if "{{WINDOW}}" in text:
        text = text.replace("{{WINDOW}}", window)
    elif "{{USER_TURNS}}" not in text:
        text = text.rstrip() + "\n\n" + window
    text = text.replace("{{USER_TURNS}}", turns or "")
    return text.replace("{{RECORD}}", record)


def call(url, system, user, temp, schema, max_tokens, timeout):
    body = {
        "messages": ([{"role": "system", "content": system}] if system else [])
                    + [{"role": "user", "content": user}],
        "temperature": temp,
        "max_tokens": max_tokens,
    }
    if schema is not None:
        body["response_format"] = {
            "type": "json_schema",
            "json_schema": {"name": "out", "strict": True, "schema": schema},
        }
    req = urllib.request.Request(
        url.rstrip("/") + "/v1/chat/completions",
        data=json.dumps(body).encode(),
        headers={"Content-Type": "application/json"},
    )
    started = time.time()
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        payload = json.load(resp)
    return payload["choices"][0]["message"]["content"], time.time() - started


PAYLOAD_RE = re.compile(r"```(?:yaml|yml)?\s*\n(.*?)```", re.DOTALL)


def payload_of(answer):
    """The last fenced block — the part meant to be kept.

    A prompt may ask the model to work something out before answering, and that working-out can
    legitimately contain the engineer's own words: quoting them is what makes a tone judgement
    track the evidence instead of the model's own sanitised paraphrase. But raw prompt text is
    read on-device and must never be transmitted, so only the fenced block is a candidate for
    publication and everything above it is scratch. Splitting them here means the boundary is
    mechanical rather than a promise the prompt makes.
    """
    blocks = PAYLOAD_RE.findall(answer)
    return blocks[-1].strip() if blocks else None



def main():
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("prompt", help="markdown file: system prompt, `---`, instruction")
    ap.add_argument("windows", nargs="+", help="one or more window .txt files")
    ap.add_argument("--record", help="record.txt to substitute for {{RECORD}}")
    ap.add_argument("--url", default=DEFAULT_URL, help=f"llama-server (default {DEFAULT_URL})")
    ap.add_argument("--temp", type=float, default=0.0, help="temperature (default 0 = reproducible)")
    ap.add_argument("--schema", choices=["beat", "routing", "sens", "none"], default="none",
                    help="constrain output to the production beat schema (default none)")
    # 1024, not 512: a truncated answer under --schema comes back as invalid JSON, which reads
    # like a prompt failure when it is only a ceiling. Raise further if you ask for long output.
    ap.add_argument("--max-tokens", type=int, default=1024)
    ap.add_argument("--timeout", type=int, default=300)
    ap.add_argument("--show-prompt", action="store_true", help="print the assembled prompt first")
    ap.add_argument("--out", help="also write each answer to this directory")
    ap.add_argument("--cpu", action="store_true",
                    help="pin the spawned server to CPU at --threads, with the viability "
                         "flags; the only way to get a quotable latency figure")
    # 18, not 4: verified that thread count does not change the ANSWER — 18 threads produced
    # markers and scores identical to 4 on the same window — while cutting a run from ~195s to
    # ~130s. So iteration happens at 18 and `--threads 4` is reserved for the one thing it is
    # actually needed for, a laptop-representative latency figure. Output is the target
    # platform's either way, which GPU is not: the quantised matmul kernels differ enough that
    # GPU reports three demand markers where CPU reports one.
    ap.add_argument("--threads", type=int, default=18,
                    help="thread count, --cpu only (default 18; use 4 for laptop latency)")
    ap.add_argument("--ctx", type=int, default=8192,
                    help="context size for a spawned server (default 8192)")
    ap.add_argument("--model", help=f"short name ({', '.join(sorted(MODELS))}) or a .gguf path. "
                                    f"Naming one implies --cpu, since the model is a property "
                                    f"of the server, not the request")
    ap.add_argument("--list-models", action="store_true", help="show known models and exit")
    ap.add_argument("--full", action="store_true",
                    help="print the working-out too, not just the kept payload")
    ap.add_argument("--flash-attn", choices=["on", "off", "auto"], default="off",
                    help="pin flash attention on both backends (default off, for comparability)")
    ap.add_argument("--ubatch", type=int, default=512, help="batch and ubatch size (default 512)")
    ap.add_argument("--quiet", action="store_true", help="silence the spawned server's log")
    args = ap.parse_args()

    if args.list_models:
        list_models()
        return

    # Naming a model means running THAT model, which the shared server on --url cannot do: it
    # was started with one set of weights. So --model spawns a server rather than being silently
    # ignored, which is how a comparison run ends up measuring the wrong model. Spawning is NOT
    # the same as --cpu: by default the spawned server uses whatever device llama.cpp picks.
    spawn = args.cpu or bool(args.model)
    model_path, model_label = resolve_model(args.model or DEFAULT_MODEL)

    def must_read(path, what):
        """A missing input here is nearly always "the splitter has not been run yet", so say that
        rather than raising a traceback at the reader."""
        try:
            return open(path, encoding="utf-8").read()
        except FileNotFoundError:
            hint = ""
            if os.path.basename(path) == "record.txt" or "window" in os.path.basename(path):
                hint = ("\n\nWindow files are produced by the splitter — run it first:\n"
                        "    python3 scripts/qwen_windows.py <transcript.jsonl> -n 10 -o windows/\n"
                        "    ls -t ~/.claude/projects/*/*.jsonl | head   # to pick a transcript")
            sys.exit(f"no {what} at {path}{hint}")

    raw = must_read(args.prompt, "prompt file")
    system, user_tpl = split_prompt(raw)
    record = must_read(args.record, "record file") if args.record else ""
    SENS = {"type": "object",
            "properties": {"has_secret": {"type": "boolean"},
                           "has_personal": {"type": "boolean"}},
            "required": ["has_secret", "has_personal"], "additionalProperties": False}
    schema = BEAT_SCHEMA if args.schema == "beat" else (SENS if args.schema == "sens" else None)

    paths = []
    for pattern in args.windows:
        if any(c in pattern for c in "*?["):
            hits = sorted(glob.glob(pattern))
            if not hits:
                sys.exit(f"no files match {pattern} — has the splitter been run?")
            paths.extend(hits)
        else:
            paths.append(pattern)
    for p in paths:
        must_read(p, "window file")

    if args.out:
        os.makedirs(args.out, exist_ok=True)

    if spawn:
        with spawn_server(model_path, model_label, args.cpu, args.threads,
                          args.ctx, args.quiet, args.flash_attn, args.ubatch) as (url, _sampler):
            run(paths, url, system, user_tpl, record, schema, args)
    else:
        run(paths, args.url, system, user_tpl, record, schema, args)


def run(paths, url, system, user_tpl, record, schema, args):
    for path in paths:
        window = open(path, encoding="utf-8").read()
        user = fill(user_tpl, window, record)

        nusers = len([l for l in window.splitlines() if l.startswith("user: ")])
        warn = "  ⚠ NO ENGINEER TURNS — tone/frustration is unsupported here" if nusers == 0 else ""
        print(f"\n{'=' * 78}\n{os.path.basename(path)}  "
              f"({len(window)} chars window, {nusers} engineer turns, "
              f"{len(system) + len(user)} chars prompt){warn}\n{'=' * 78}")
        if args.show_prompt:
            if system:
                print(f"--- SYSTEM ---\n{system}\n")
            print(f"--- USER ---\n{user}\n--- ANSWER ---")

        per_window = schema
        if args.schema == "routing":
            per_window = routing_schema(with_frustration=nusers > 0)

        try:
            answer, secs = call(url, system, user, args.temp, per_window,
                                args.max_tokens, args.timeout)
        except urllib.error.URLError as err:
            sys.exit(f"\ncannot reach {url}: {err}\n"
                     f"is llama-server running? try: curl -s {url}/health")

        scratch = None
        if per_window is not None:
            try:
                parsed = json.loads(answer)
                if args.schema == "routing":
                    scratch = parsed.pop("working_out", None)
                    tone = parsed.pop("tone", None)
                    demand = parsed.pop("demand", None)
                    parsed["scores"] = {}
                    if tone is not None:
                        parsed["scores"]["frustration"] = tone_score(tone)
                        parsed["tone"] = tone
                    subs_for_score = (parsed.get("work") or {}).get("subjects") or []
                    if demand is not None:
                        parsed["scores"]["complexity"] = demand_score(demand, subs_for_score)
                        parsed["demand"] = demand
                    subs = (parsed.get("work") or {}).get("subjects") or []
                    bad = unverified_subjects(subs, window, record)
                    if bad:
                        parsed["unverified_subjects"] = bad
                    answer = as_yaml(parsed)
                else:
                    answer = json.dumps(parsed, indent=2, ensure_ascii=False)
            except json.JSONDecodeError:
                print("[schema was requested but the answer is not valid JSON]")

        if scratch is not None and args.full:
            print(f"--- WORKING OUT (discarded, may quote the engineer verbatim) ---\n"
                  f"{scratch}\n--- KEPT ---")
        payload = payload_of(answer)
        if payload is not None and not args.full:
            print(payload)
            scratch = len(answer) - len(payload)
            print(f"\n[{secs:.1f}s · {scratch} chars of working-out discarded; --full to see it]")
        else:
            print(answer)
            print(f"\n[{secs:.1f}s]")

        if args.out:
            # Only ever the payload: the scratch may hold the engineer's own words verbatim.
            name = os.path.splitext(os.path.basename(path))[0] + (".yaml" if payload else ".txt")
            with open(os.path.join(args.out, name), "w", encoding="utf-8") as fh:
                fh.write((payload if payload is not None else answer) + "\n")


if __name__ == "__main__":
    main()
