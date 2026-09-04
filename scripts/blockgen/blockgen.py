#!/usr/bin/env python3
"""blockgen — a synthetic Claude Code JSONL transcript generator for Keld Signal v3, lane G / D1.

Writes transcripts shaped exactly like the ones `sidecar/app/analysis/transcript.py` parses
(`turns_in`, `tool_use_in`), so the REAL sidecar can ingest them unchanged and cut them into
focus blocks (`sidecar/app/analysis/blocks.py`). Two layers, deliberately in this order:

  1. A prompt/response TEXT generator (`PromptGenerator` below) — plausible developer language,
     never real prompt text. Uses `faker` if it happens to be importable; ships a built-in
     fallback (a small weighted vocabulary of developer nouns/verbs/files + templates) because
     faker is not a project dependency and must not become a hard import.
  2. A BLOCK generator on top of it — the timing/session structure (active runs of 20-minute
     cap blocks, idle gaps, session lengths, token rates) that makes the sidecar's own cutter
     (`MAX_BLOCK_MINUTES=20`, `IDLE_BINS=3`) produce the blocks this script intended.

METADATA IS GENERATED, NOT RANDOM: a FIXED profile of five repositories (see `DEFAULT_PROFILE`)
drives every session. Each session is bound to exactly ONE repository — its `cwd`, `gitBranch`
and remote evidence all agree — via a deterministic weighted round-robin (`WRR` below), never a
weighted coin flip, so repo counts are an exact function of the profile's weights and the
session count, not a sampling outcome.

Determinism: every random decision is drawn from one `random.Random(seed)` instance, in a fixed
call order, and every id (session/prompt/message uuid) is derived from that same stream via
`det_uuid` rather than `uuid.uuid4()`. Wall-clock time never enters the non-`--live` path — dates
are offset from a fixed synthetic epoch (`SYNTHETIC_EPOCH`), never `time.time()` — so the same
seed reproduces byte-identical output regardless of when or where it is run, and two different
seeds practically never share an id.

See `docs/v3/contracts.md`, section "The block generator (`scripts/blockgen/`)", for the binding
contract this implements, and `README.md` in this directory for the measured numbers behind the
default profile.
"""
from __future__ import annotations

import argparse
import copy
import json
import math
import os
import random
import sys
import time
import uuid as uuidlib
from datetime import datetime, timezone

try:
    from faker import Faker      # optional; not a project dependency (see module docstring)
    HAVE_FAKER = True
except ImportError:
    HAVE_FAKER = False

# --------------------------------------------------------------------------------------- shapes
#
# The sidecar's own constants, mirrored here as plain numbers (not imported — this script must
# run under a bare host python3, with no sidecar/venv dependency) so the timing math below can be
# checked against them by eye. See sidecar/app/analysis/blocks.py and store.py.
BIN_SECONDS = 300                  # store.BIN_SECONDS: the series' rollup granularity
MAX_BLOCK_MINUTES = 20             # blocks.MAX_BLOCK_MINUTES: the budget cut
IDLE_BINS = 3                      # blocks.IDLE_BINS: 3 consecutive empty bins == idle (15 min)

VERSION_STRING = "2.5.0"

# The one wall-clock-independent anchor every non-live transcript is built relative to, so the
# same seed produces byte-identical output on any machine on any day.
SYNTHETIC_EPOCH = datetime(2026, 1, 5, 13, 0, 0, tzinfo=timezone.utc).timestamp()


# ============================================================================ the fixed profile

DEFAULT_PROFILE = {
    # Five org-like repositories, each session bound to exactly one (see WRR below). Weights are
    # the FIXED, deterministic mix — not a suggestion for `random.choices` — so repo counts over
    # N sessions are an exact function of these numbers and N, never a sampling artifact.
    "repositories": [
        {"remote": "github.com/ncx-ai/keld-signal", "workspace": "keld-signal",
         "language": "go", "ticket_prefix": "KELD", "weight": 3},
        {"remote": "github.com/ncx-ai/keld-atlas", "workspace": "keld-atlas",
         "language": "go", "ticket_prefix": "ATLAS", "weight": 3},
        {"remote": "github.com/ncx-ai/sdk-testbench", "workspace": "sdk-testbench",
         "language": "python", "ticket_prefix": "SDK", "weight": 2},
        {"remote": "github.com/ncx-ai/atlas-telemetry-typescript",
         "workspace": "atlas-telemetry-typescript", "language": "typescript",
         "ticket_prefix": "TELTS", "weight": 1},
        {"remote": "github.com/ncx-ai/atlas-telemetry-python",
         "workspace": "atlas-telemetry-python", "language": "python",
         "ticket_prefix": "TELPY", "weight": 1},
    ],
    # A synthetic, dot-free filesystem root for every checkout. Dot-free is deliberate: Claude
    # Code's real project-directory sanitisation collapses both "/" and "." to "-", which is
    # ambiguous to reverse (see workspace.py's `launch_dir` docstring) — staying dot-free keeps
    # this script's own `sanitize_cwd` (plain "/" -> "-") exactly equivalent to the real one.
    "workspace_root": "/home/keldsynth/workspaces",
    # Mostly opus, some sonnet — deterministic WRR mix, picked once per session.
    "models": [["claude-opus-4-8", 0.65], ["claude-sonnet-4-6", 0.35]],
    # main / a ticket-carrying feature branch / a plain feature branch. WRR per repo.
    "branch_mix": [["main", 0.40], ["ticket", 0.40], ["feature", 0.20]],
    # Bash/Read/Edit/Write/Grep mix for each assistant "turn" (== one API request).
    "tool_mix": [["Bash", 0.35], ["Read", 0.25], ["Edit", 0.20], ["Write", 0.10], ["Grep", 0.10]],
    # Measured on this machine's real blocks (see AGENTS.md's Signal v3 section and README.md):
    "active_run_blocks": [1, 6],        # a run is this many consecutive 20-minute cap blocks
    "idle_bins_range": [3, 12],          # 15-60 minutes of silence, in 5-minute bins (>= IDLE_BINS)
    "session_seconds": [300, 16200],     # 5 minutes to 4.5 hours
    "sessions_per_day": [1, 4],          # informational; --sessions/--days drive the CLI directly
    "turns_per_prompt": [2, 4],          # assistant "turns" (API requests) per human prompt
    "tokens_per_active_minute": [50000, 80000],   # raw (unweighted) token sum, per active minute
}

LANG_FILES = {
    "go": ["internal/agent/blocks/blocks.go", "internal/agent/queue/queue.go",
           "internal/agent/publish/window.go", "internal/agent/daemon/daemon.go",
           "cmd/keld-agent/main.go", "go.mod", "README.md"],
    "python": ["app/analysis/blocks.py", "app/analysis/store.py", "app/analysis/ingest.py",
               "app/main.py", "app/analysis/window.py", "pyproject.toml", "README.md"],
    "typescript": ["src/otel/exporter.ts", "src/otel/index.ts", "src/config.ts",
                   "src/instrumentation.ts", "package.json", "README.md"],
}

BASH_TEMPLATES = {
    "go": ["go test ./...", "go build ./...", "go vet ./...", "git status",
           "git diff --stat", "gofmt -l ."],
    "python": ["pytest -q", "python3 -m pytest app/test_blocks.py", "ruff check .",
              "git status", "git diff --stat"],
    "typescript": ["pnpm test", "pnpm build", "npx tsc --noEmit", "git status",
                   "git diff --stat"],
}

GREP_PATTERNS = ["TODO", "FIXME", "func ", "class ", "import ", "def ", "export "]

BRANCH_SLUGS = ["block-cutter", "retry-policy", "auth-flow", "queue-backpressure",
               "token-accounting", "workspace-resolver", "prompt-index", "telemetry-proxy",
               "schema-migration", "session-store", "settings-poll", "sidecar-client"]


# =================================================================== the fallback text generator

DEV_VERBS = ["fix", "refactor", "add", "wire up", "investigate", "extend", "harden",
            "document", "debug", "optimize", "clean up", "migrate", "test", "review",
            "implement", "simplify", "stabilize"]
DEV_OBJECTS = ["the ingest pipeline", "the block cutter", "the retry policy",
              "the session store", "the auth flow", "the settings poll",
              "the sidecar client", "the queue backpressure", "the token accounting",
              "the workspace resolver", "the prompt index", "the telemetry proxy",
              "the reconnection logic", "the schema migration", "the CLI flag parsing",
              "the readiness gate", "the spool drain"]
DEV_CONDITIONS = ["the queue is full", "the branch has no remote", "tokens exceed the cap",
                  "the store is behind", "a retry storms the sidecar", "the idle timer fires",
                  "two sessions race", "the cache goes cold", "the config is missing a key",
                  "the daemon restarts mid-job"]
PROMPT_TEMPLATES = [
    "Can you {verb} {obj}? {cond_clause}",
    "{obj_cap} is failing when {cond}, please {verb} it.",
    "I need help to {verb} {obj} before the release.",
    "Let's {verb} {obj} — {cond} and it's been bugging me.",
    "Take a look at {obj}. {cond_clause}",
    "{verb_cap} {obj}, then run the tests.",
]
RESPONSE_TEMPLATES = [
    "I'll {verb} {obj} now.",
    "Looking at {obj}, the issue is {cond}. Let me {verb} it.",
    "Done — {obj} now handles the case where {cond}.",
    "On it. {obj_cap} needed a small change to {verb} the path where {cond}.",
    "Checked {obj}: {cond} was the root cause. Applying a fix.",
]


class PromptGenerator:
    """Plausible developer prompt/response text. Never real prompt text — everything here is
    templated from a small fixed vocabulary (optionally flavoured with `faker`, if importable).
    """

    def __init__(self, rng, seed):
        self._rng = rng
        self._faker = None
        if HAVE_FAKER:
            Faker.seed(seed)
            self._faker = Faker()

    def _pieces(self, rng):
        verb = rng.choice(DEV_VERBS)
        obj = rng.choice(DEV_OBJECTS)
        cond = rng.choice(DEV_CONDITIONS)
        return verb, obj, cond

    def human_prompt(self, rng, repo):
        verb, obj, cond = self._pieces(rng)
        tmpl = rng.choice(PROMPT_TEMPLATES)
        text = tmpl.format(verb=verb, obj=obj, cond=cond, obj_cap=obj[0].upper() + obj[1:],
                           verb_cap=verb[0].upper() + verb[1:],
                           cond_clause=f"It looks like {cond}.")
        if self._faker is not None:
            text += " " + self._faker.sentence(nb_words=8)
        return text

    def assistant_note(self, rng, repo, tool_name):
        verb, obj, cond = self._pieces(rng)
        tmpl = rng.choice(RESPONSE_TEMPLATES)
        text = tmpl.format(verb=verb, obj=obj, cond=cond, obj_cap=obj[0].upper() + obj[1:])
        return text

    def synth_file_body(self, rng):
        lines = [f"// synthetic content, seed line {rng.randint(1, 9999)}",
                 "// generated by scripts/blockgen for load-testing only"]
        return "\n".join(lines) + "\n"


def tool_input(rng, tool_name, repo, cwd):
    files = LANG_FILES.get(repo["language"], LANG_FILES["go"])
    if tool_name == "Bash":
        cmd = rng.choice(BASH_TEMPLATES.get(repo["language"], BASH_TEMPLATES["go"]))
        return {"command": cmd}
    if tool_name == "Grep":
        return {"pattern": rng.choice(GREP_PATTERNS), "path": f"{cwd}/{rng.choice(files)}"}
    if tool_name == "Write":
        f = rng.choice(files)
        return {"file_path": f"{cwd}/{f}",
               "content": f"// synthetic content {rng.randint(1, 9999)}\n"}
    if tool_name == "Edit":
        f = rng.choice(files)
        return {"file_path": f"{cwd}/{f}",
               "old_string": "// TODO: tighten this up",
               "new_string": "// tightened per review"}
    # Read
    f = rng.choice(files)
    return {"file_path": f"{cwd}/{f}"}


def marker_read_input(cwd):
    """A Read of a REPO_MARKER file at the checkout root (workspace.py's `REPO_MARKERS`)."""
    return {"file_path": f"{cwd}/README.md"}


def remote_bash_input(repo):
    """A Bash `git remote -v` invocation whose command text also carries the literal
    `https://github.com/<org>/<repo>` URL `workspace.REMOTE_REPO` scans for — the sidecar reads
    only the command STRING and any sibling `text` block, never a fabricated tool result, so the
    URL has to live in the command text itself for `scan_workspace` to see it at all."""
    url = f"https://{repo['remote']}.git"
    return {"command": f"git remote -v\n# origin\t{url} (fetch)\n# origin\t{url} (push)"}


# ============================================================================== determinism kit

class WRR:
    """Deterministic smooth weighted round-robin. `next()` returns an index into `weights`, and
    over any N calls the count for index i is within one of `N * weights[i] / sum(weights)` —
    an exact function of the weights, never a sampling outcome. This is what makes repo/branch/
    model counts "generated, not random" per session, while everything else (timing, text,
    specific ids) still comes from the seeded `random.Random` stream.
    """

    def __init__(self, weights):
        self.weights = list(weights)
        self.current = [0.0] * len(weights)
        self.total = sum(weights)

    def next(self):
        for i, w in enumerate(self.weights):
            self.current[i] += w
        best = max(range(len(self.weights)), key=lambda i: self.current[i])
        self.current[best] -= self.total
        return best


def det_uuid(rng):
    """A uuid4-shaped string drawn from the seeded RNG stream, never `uuid.uuid4()` — the latter
    is unseeded and would break same-seed reproducibility."""
    return str(uuidlib.UUID(int=rng.getrandbits(128), version=4))


def sanitize_cwd(cwd):
    """The watcher's own convention for a `claude_code` project directory: the launch cwd with
    every "/" replaced by "-" (see workspace.py's `launch_dir`). Every synthetic cwd this script
    builds is dot-free (`DEFAULT_PROFILE["workspace_root"]`), so this plain substitution is
    exactly equivalent to Claude Code's real sanitisation (which also folds "." into "-") for
    every path this generator produces."""
    return cwd.replace("/", "-")


def iso(ts):
    dt = datetime.fromtimestamp(ts, tz=timezone.utc)
    return dt.strftime("%Y-%m-%dT%H:%M:%S.") + f"{dt.microsecond // 1000:03d}Z"


def ceil_bin(ts):
    return math.ceil(ts / BIN_SECONDS) * BIN_SECONDS


# ================================================================================ profile loading

def deep_merge(base, overrides):
    for k, v in overrides.items():
        if isinstance(v, dict) and isinstance(base.get(k), dict):
            deep_merge(base[k], v)
        else:
            base[k] = v
    return base


def load_profile(path=None):
    profile = copy.deepcopy(DEFAULT_PROFILE)
    if path:
        with open(path) as f:
            overrides = json.load(f)
        deep_merge(profile, overrides)
    return profile


# =================================================================================== the builder

def pick_branch(rng, repo, branch_wrr, ticket_counters):
    kind = ["main", "ticket", "feature"][branch_wrr.next()]
    if kind == "main":
        return "main"
    slug = rng.choice(BRANCH_SLUGS)
    if kind == "ticket":
        prefix = repo["ticket_prefix"]
        n = ticket_counters.get(prefix, 100)
        ticket_counters[prefix] = n + 1
        return f"feature/{prefix}-{n}-{slug}"
    return f"feature/{slug}"


def build_session_runs(rng, profile):
    """`(runs_seconds, break_bins)` for one session: `runs_seconds[i]` is the i'th active run's
    duration (a multiple of `BIN_SECONDS`), `break_bins[i]` is the number of fully-empty 5-minute
    bins separating run i from run i+1 (always >= IDLE_BINS, so every break the sidecar's cutter
    sees really does read as `idle`). See the module docstring / README for the measured ranges.
    """
    lo, hi = profile["session_seconds"]
    target_total = rng.uniform(lo, hi)
    runs, breaks = [], []
    elapsed = 0.0
    while True:
        remaining = target_total - elapsed
        n_blocks = rng.randint(*profile["active_run_blocks"])
        run_s = float(n_blocks * MAX_BLOCK_MINUTES * 60)
        if run_s > remaining:
            bins = max(1, int(remaining // BIN_SECONDS))
            run_s = float(bins * BIN_SECONDS)
        runs.append(run_s)
        elapsed += run_s
        if elapsed >= target_total:
            break
        # A break belongs strictly BETWEEN two runs — commit one only once a following run is
        # certain, or a trailing break with nothing active after it would inflate the intended
        # break count past what the sidecar's cutter could ever show (there is no block on its
        # far side to form a gap against).
        idle_bins = rng.randint(*profile["idle_bins_range"])
        gap_s = float((idle_bins + 1) * BIN_SECONDS)
        if elapsed + gap_s + BIN_SECONDS > target_total:
            break
        breaks.append(idle_bins)
        elapsed += gap_s
    return runs, breaks


def split_usage(rng, total):
    """`total` raw tokens (unweighted sum) -> (input, cache_read, cache_creation_1h, output),
    proportioned roughly like a real cache-heavy agentic turn (see the real example quoted in
    AGENTS.md / README.md: input ~0%, cache_read ~24%, cache_creation ~76%, output ~0.2%)."""
    fracs = [rng.uniform(0.003, 0.02), rng.uniform(0.45, 0.70),
            rng.uniform(0.20, 0.45), rng.uniform(0.02, 0.08)]
    s = sum(fracs)
    fracs = [f / s for f in fracs]
    input_t = max(0, int(total * fracs[0]))
    cache_read = max(0, int(total * fracs[1]))
    cache_creation = max(0, int(total * fracs[2]))
    output_t = max(1, int(total * fracs[3]))
    return input_t, cache_read, cache_creation, output_t


def usage_dict(rng, total):
    input_t, cache_read, cache_creation, output_t = split_usage(rng, total)
    return {
        "input_tokens": input_t,
        "cache_creation_input_tokens": cache_creation,
        "cache_read_input_tokens": cache_read,
        "output_tokens": output_t,
        "cache_creation": {"ephemeral_1h_input_tokens": cache_creation,
                           "ephemeral_5m_input_tokens": 0},
        "service_tier": "standard",
    }


def build_request(rng, session_id, cwd, branch, model, ts0, prev_uuid, tool_name, in_,
                  note_text, uid_fn, usage, max_ts=None):
    """One API request as TWO assistant lines sharing `requestId` and `usage` — the median shape
    real transcripts carry (see magnitude.py's module docstring). Returns
    `(lines, last_uuid, last_ts)`.

    `max_ts`, when given, is a HARD ceiling on both lines' timestamps: the caller schedules
    events bin-by-bin and must guarantee that nothing spills past the bin's own boundary into
    what is meant to be an empty (idle) bin — a single stray event there would make the sidecar's
    own `blocks.active_bins` see the bin as non-empty and silently fuse two intended active runs
    into one, dropping the break between them.
    """
    if max_ts is not None:
        ts0 = min(ts0, max_ts - 6.0)
    rid = "req_" + uid_fn(rng).replace("-", "")[:24]
    u1 = uid_fn(rng)
    line1 = {
        "parentUuid": prev_uuid, "isSidechain": False,
        "message": {"model": model, "id": "msg_" + u1.replace("-", "")[:24],
                   "type": "message", "role": "assistant",
                   "content": [{"type": "text", "text": note_text}],
                   "stop_reason": None, "stop_sequence": None, "usage": usage},
        "requestId": rid, "type": "assistant", "uuid": u1,
        "timestamp": iso(ts0), "cwd": cwd, "sessionId": session_id,
        "version": VERSION_STRING, "gitBranch": branch,
    }
    ts1 = ts0 + rng.uniform(1.0, 6.0)
    if max_ts is not None:
        ts1 = min(ts1, max_ts)
    u2 = uid_fn(rng)
    line2 = {
        "parentUuid": u1, "isSidechain": False,
        "message": {"model": model, "id": "msg_" + u2.replace("-", "")[:24],
                   "type": "message", "role": "assistant",
                   "content": [{"type": "tool_use", "id": "toolu_" + u2.replace("-", "")[:24],
                               "name": tool_name, "input": in_}],
                   "stop_reason": "tool_use", "stop_sequence": None, "usage": usage},
        "requestId": rid, "type": "assistant", "uuid": u2,
        "timestamp": iso(ts1), "cwd": cwd, "sessionId": session_id,
        "version": VERSION_STRING, "gitBranch": branch,
    }
    return [line1, line2], u2, ts1


def bookkeeping_lines(session_id, title):
    """Untimestamped bookkeeping records real transcripts open with (see AGENTS.md's watch/
    filter notes on `custom-title`/`mode`/`file-history-snapshot`). Deliberately carries no
    `timestamp` key: these are exactly the shapes `transcript.turns_in` skips via its cheap
    substring check before ever calling `json.loads`."""
    return [
        {"type": "mode", "mode": "normal", "sessionId": session_id},
        {"type": "custom-title", "title": title, "sessionId": session_id},
        {"type": "file-history-snapshot", "messageId": "seed",
         "snapshot": {"messageId": "seed", "trackedFileBackups": {}}, "isSnapshotUpdate": False},
    ]


def build_session(rng, uid_fn, text_gen, repo, model_wrr, branch_wrr, ticket_counters,
                  profile, session_start_ts, tool_wrr):
    session_id = uid_fn(rng)
    cwd = f"{profile['workspace_root']}/{repo['workspace']}"
    branch = pick_branch(rng, repo, branch_wrr, ticket_counters)
    model = profile["models"][model_wrr.next()][0]
    runs_s, break_bins = build_session_runs(rng, profile)

    title = text_gen.human_prompt(rng, repo)[:60]
    body = list(bookkeeping_lines(session_id, title))

    prev_uuid = None
    cursor = ceil_bin(session_start_ts)
    manifest_runs, manifest_breaks, prompt_ids = [], [], []
    tool_names = [n for n, _ in profile["tool_mix"]]

    for run_idx, run_s in enumerate(runs_s):
        run_start = cursor
        n_bins = int(run_s // BIN_SECONDS)
        run_rate = rng.uniform(*profile["tokens_per_active_minute"])   # raw tokens / active minute
        for bin_i in range(n_bins):
            bin_start = run_start + bin_i * BIN_SECONDS
            # A HARD ceiling on every event this bin schedules — see build_request's docstring.
            # Without it, a bin with several requests (turns_per_prompt up to 4, each spaced
            # 15-60s) can drift past bin_start + BIN_SECONDS and land a stray event in what was
            # meant to be the first empty (idle) bin of the following break, silently fusing two
            # intended active runs into one and dropping the break between them.
            max_ts = bin_start + BIN_SECONDS - 1.0
            prompt_ts = min(bin_start + rng.uniform(1.0, 40.0), max_ts)
            prompt_id = uid_fn(rng)
            prompt_uuid = uid_fn(rng)
            prompt_ids.append(prompt_id)
            prompt_text = text_gen.human_prompt(rng, repo)
            body.append({
                "parentUuid": prev_uuid, "isSidechain": False, "promptId": prompt_id,
                "type": "user", "message": {"role": "user", "content": prompt_text},
                "isMeta": False, "uuid": prompt_uuid, "timestamp": iso(prompt_ts),
                "userType": "external", "entrypoint": "cli", "cwd": cwd,
                "sessionId": session_id, "version": VERSION_STRING, "gitBranch": branch,
            })
            prev_uuid = prompt_uuid
            n_requests = rng.randint(*profile["turns_per_prompt"])
            usage_total = run_rate * (BIN_SECONDS / 60.0) / max(1, n_requests)
            step_ts = min(prompt_ts + rng.uniform(2.0, 10.0), max_ts)
            for req_i in range(n_requests):
                if run_idx == 0 and bin_i == 0 and req_i == 0:
                    tname, tin = "Read", marker_read_input(cwd)
                elif run_idx == 0 and bin_i == 0 and req_i == 1:
                    tname, tin = "Bash", remote_bash_input(repo)
                else:
                    tname = tool_names[tool_wrr.next()]
                    tin = tool_input(rng, tname, repo, cwd)
                note = text_gen.assistant_note(rng, repo, tname)
                usage = usage_dict(rng, usage_total)
                lines, prev_uuid, step_ts = build_request(
                    rng, session_id, cwd, branch, model, step_ts, prev_uuid, tname, tin,
                    note, uid_fn, usage, max_ts=max_ts)
                body.extend(lines)
                step_ts = min(step_ts + rng.uniform(15.0, 60.0), max_ts)
        manifest_runs.append({
            "start_ts": run_start, "end_ts": run_start + run_s,
            "n_blocks": int(math.ceil(run_s / (MAX_BLOCK_MINUTES * 60))),
        })
        cursor = run_start + run_s
        if run_idx < len(break_bins):
            idle_bins = break_bins[run_idx]
            gap_s = (idle_bins + 1) * BIN_SECONDS
            manifest_breaks.append({
                "start_ts": cursor, "end_ts": cursor + gap_s, "idle_bins": idle_bins,
            })
            cursor += gap_s

    manifest_entry = {
        "session_id": session_id,
        "cwd": cwd,
        "repo_remote": repo["remote"],
        "workspace": repo["workspace"],
        "branch": branch,
        "model": model,
        "runs": manifest_runs,
        "breaks": manifest_breaks,
        "prompt_ids": prompt_ids,
        "sanitized_cwd": sanitize_cwd(cwd),
        "rel_path": f"{sanitize_cwd(cwd)}/{session_id}.jsonl",
    }
    return manifest_entry, body


# =================================================================================== top-level

def generate(profile, sessions, days, seed):
    """Build the full deterministic corpus: `(manifest, files)` where `files` maps a path
    relative to the output directory -> the list of JSON-able line dicts for that transcript.
    Pure — writes nothing to disk. `write_batch`/`write_live` below do the I/O."""
    rng = random.Random(seed)
    uid_fn = det_uuid
    text_gen = PromptGenerator(rng, seed)

    repos = profile["repositories"]
    repo_wrr = WRR([r["weight"] for r in repos])
    model_wrr = WRR([w for _, w in profile["models"]])
    tool_wrr = WRR([w for _, w in profile["tool_mix"]])
    # One branch-mix WRR and one ticket counter PER REPO, so each repo's own branch mix and
    # ticket numbering is independent and reproducible on its own.
    branch_wrrs = {r["remote"]: WRR([w for _, w in profile["branch_mix"]]) for r in repos}
    ticket_counters = {}

    manifest = {"seed": seed, "sessions": sessions, "days": days,
               "generated_with": "blockgen", "schema": 1, "session_list": []}
    files = {}

    for i in range(sessions):
        repo = repos[repo_wrr.next()]
        day_idx = (i * days) // max(1, sessions)
        intraday = rng.uniform(7 * 3600, 21 * 3600)
        session_start_ts = SYNTHETIC_EPOCH + day_idx * 86400 + intraday
        entry, body = build_session(rng, uid_fn, text_gen, repo, model_wrr,
                                    branch_wrrs[repo["remote"]], ticket_counters, profile,
                                    session_start_ts, tool_wrr)
        entry["day"] = day_idx
        manifest["session_list"].append(entry)
        files[entry["rel_path"]] = body

    return manifest, files


def write_batch(out_dir, manifest, files):
    for rel_path, lines in files.items():
        full = os.path.join(out_dir, rel_path)
        os.makedirs(os.path.dirname(full), exist_ok=True)
        with open(full, "w") as f:
            for line in lines:
                f.write(json.dumps(line, separators=(",", ":")) + "\n")
    with open(os.path.join(out_dir, "manifest.json"), "w") as f:
        json.dump(manifest, f, indent=2, sort_keys=True)
        f.write("\n")


def _epoch_of(line):
    ts = line.get("timestamp")
    if not ts:
        return None
    return datetime.fromisoformat(ts.replace("Z", "+00:00")).timestamp()


def write_live(out_dir, manifest, files, speed):
    """Append lines in accelerated wall-clock so a daemon watcher pointed at `out_dir` sees the
    same files grow the way a real Claude Code session would. Headers (untimestamped bookkeeping
    lines) are written immediately so every file exists from the start; the remaining,
    timestamped lines of every session are merged into ONE global chronological stream and
    replayed with real `time.sleep`s scaled by `speed` (simulated-seconds per real-second).

    Not part of the reproducibility contract — real wall-clock time drives the pacing, so this
    mode is for manual daemon/watcher testing, never for the byte-identical acceptance test.
    """
    handles = {}
    events = []  # (epoch, rel_path, line)
    for rel_path, lines in files.items():
        full = os.path.join(out_dir, rel_path)
        os.makedirs(os.path.dirname(full), exist_ok=True)
        header = [ln for ln in lines if _epoch_of(ln) is None]
        body = [ln for ln in lines if _epoch_of(ln) is not None]
        with open(full, "w") as f:
            for ln in header:
                f.write(json.dumps(ln, separators=(",", ":")) + "\n")
        handles[rel_path] = full
        for ln in body:
            events.append((_epoch_of(ln), rel_path, ln))
    events.sort(key=lambda e: e[0])

    prev_ts = None
    for ts, rel_path, line in events:
        if prev_ts is not None:
            delay = max(0.0, (ts - prev_ts) / max(speed, 1e-9))
            if delay > 0:
                time.sleep(delay)
        with open(handles[rel_path], "a") as f:
            f.write(json.dumps(line, separators=(",", ":")) + "\n")
        prev_ts = ts

    with open(os.path.join(out_dir, "manifest.json"), "w") as f:
        json.dump(manifest, f, indent=2, sort_keys=True)
        f.write("\n")


# ========================================================================================= CLI

def parse_args(argv=None):
    p = argparse.ArgumentParser(
        prog="blockgen",
        description="Generate synthetic Claude Code JSONL transcripts for Keld Signal v3.")
    p.add_argument("--out", required=True, help="output directory")
    p.add_argument("--sessions", type=int, default=20, help="total sessions to generate")
    p.add_argument("--days", type=int, default=7, help="span sessions across this many days")
    p.add_argument("--seed", type=int, default=0, help="determinism seed")
    p.add_argument("--profile", default=None, help="JSON file overriding DEFAULT_PROFILE")
    p.add_argument("--live", action="store_true",
                   help="append lines in accelerated wall-clock instead of writing all at once")
    p.add_argument("--speed", type=float, default=60.0,
                   help="live mode: simulated seconds per real second (default 60x)")
    return p.parse_args(argv)


def main(argv=None):
    args = parse_args(argv)
    profile = load_profile(args.profile)
    manifest, files = generate(profile, args.sessions, args.days, args.seed)
    os.makedirs(args.out, exist_ok=True)
    if args.live:
        write_live(args.out, manifest, files, args.speed)
    else:
        write_batch(args.out, manifest, files)
    n_lines = sum(len(v) for v in files.values())
    print(f"blockgen: wrote {len(files)} transcript(s), {n_lines} lines, "
         f"seed={args.seed} -> {args.out}", file=sys.stderr)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
