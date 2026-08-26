"""Per-message text embedding, on device — the text half of the training corpus.

`X` for the future-work models has two halves, and the rule
(`docs/superpowers/specs/2026-08-26-signal-embeddings-design.md`) is that neither is ever
serialised into the other's modality: the deterministic digest enters as NUMBERS, the transcript
enters as a TEXT EMBEDDING. This module is the second half, and nothing else in this package
computes one.

⚠️ **Raw text never leaves the device, and it never leaves this process either.** Text is read
here, handed to a child process over a `multiprocessing` queue, and turned into floats. Nothing in
this module returns, stores or logs a message's text: `MessageVector` carries a timestamp, a
stream tag and a vector, and `test_textembed.py` pins that by reflection. Even a child-side
exception is reduced to its CLASS name before it crosses back, because a tokenizer error's `repr`
can quote the input — the same reduction `clientevents/redact.go` applies one layer up.

## THE UNIT OF ENCODING IS THE MESSAGE, NOT THE SHELL

Three reasons, each independently sufficient, and none of them is about tidiness:

1. A 240-minute shell holds hundreds of KB of text. No encoder context holds it, and a bound that
   made it fit would be a mid-clause cut over almost every message in it.
2. The bulk of a transcript is `tool_result` lines, which `transcript.turns_in` **skips unparsed
   by design** — a raw substring check before `json.loads`, and that skip is what keeps a parse
   seconds-long rather than minutes-long. Embedding "the transcript" would mean decoding exactly
   the lines the parser is engineered to avoid. This module reads only `text` and `thinking`
   CONTENT BLOCKS, so a `tool_result` block is never touched even on the lines that do get parsed
   (a line carrying both a `tool_result` and a `tool_use` passes `turns_in`'s filter).
3. Shells overlap across rows: a message in this row's `[0,5m)` is in the next row's `[5,20m)`.
   Per-message encoding means each message is encoded **once, ever**, and every shell of every row
   containing it reuses that vector. Amortised, that is one forward pass per message — ~100/day.

## THREE STREAMS, KEPT SEPARATE, NEVER CONCATENATED

`user` (the human's own words), `asst` (assistant prose, excluding thinking and excluding tool
results), `think` (thinking blocks). They are different registers and pooling them averages the
distinction away, so every derived quantity here is computed per stream — including `novelty`,
whose "any earlier message" means any earlier message OF THE SAME STREAM.

⚠️ **`think` will almost always be EMPTY, and that is a measured fact, not a bug.** `text.py`'s
`think_blocks` records it: every thinking block in a platform-written Claude Code transcript
carries a signature and an EMPTY `thinking` string (9,148 measured there, 7,648 re-measured at
final review, and 9,144 re-measured for THIS module over the 40 largest transcripts on a real
machine — **0 of non-zero length** in all three). The only corpus that ever held real thinking text was a hand-made claude.ai export, which
this system does not read. So the stream is wired, and it reports `skipped:empty` as a STATED
outcome rather than looking identical to a stream that was never asked for. A producer that starts
writing thinking text populates it with no code change.

## ENCODE AT 1024, PUBLISH AT 256

Qwen3-Embedding-0.6B, `transformers.AutoModel` (torch and transformers are already dependencies —
gliner2[local] pulls both; `requirements.txt` is untouched by this module). Last-token pooling with
left padding, which is what this model's contrastive objective was trained for; a mean pool over
its hidden states is a different and worse vector.

Matryoshka truncation is a PREFIX SLICE, so the 256-d published vector costs no second forward
pass. The two numbers are different and conflating them puts the volume estimate out by 4x:
~77 KB/user/active-day at 256 against ~307 KB at full width. ⚠️ It is the one parameter here that
cannot be revised retroactively — a corpus collected at 256 cannot be widened without re-embedding
every machine's history.

The vector is L2-normalised at 1024, sliced, and **re-normalised at 256**. Re-normalising is what
makes an inner product a cosine at the published width; without it every scalar below would be
computed against vectors of varying length and `dispersion` would partly measure norm. All four
derived quantities are computed on the PUBLISHED 256-d vectors, so a consumer at Keld pooling the
message rows Atlas already holds gets the same numbers this client did.

## THE ENCODER RUNS IN ITS OWN CHILD

Not in the FastAPI parent. `worker_manager.parent_reserve_mb()` is a HIGH-WATER LATCH: anything
resident in the parent permanently reduces the inference worker's hard limit, and it is monotone by
design, so a model loaded into the parent can never be un-accounted. spaCy already costs 619 MB
there and the budget is documented as oversubscribed by 444.6 MB before this module exists. A child
is recyclable, idle-unloadable, and returns its heap to the OS by exiting — the only cross-platform
memory reset, which is the identical argument `worker.py` makes for the GLiNER2 worker.

And when the feature is off, the child is never spawned and torch is never imported by this path:
`Encoder.encode` refuses before it reaches a spawn, and this module has no heavy module-level
import (which is also what keeps the spawn re-import cheap).

## MISSING WEIGHTS DEGRADE TO "NO VECTORS"

The weights are provisioned on demand — the daemon fetches them into `~/.keld/models` and hands
the directory over as `KELD_TEXTEMBED_DIR` at spawn, exactly as it already does for GLiNER2 with
`KELD_GLINER2_DIR`. Nothing here downloads at import, and nothing waits at startup. Absent weights
are `degraded:weights_unavailable` and an empty vector list — never a crash, never a stall. The
state is retried after `KELD_TEXTEMBED_RETRY_S` rather than latched, because provisioning is
asynchronous and the weights genuinely may arrive later; retried on a cooldown rather than per call,
because a failed spawn costs seconds.

## THE PUBLISHED-VECTOR TREATMENT

A fixed ORTHOGONAL projection is applied on device before publish. It preserves cosine similarity
and inner products exactly — so nothing about training changes — costs one matmul, and makes
off-the-shelf embedding-inversion tooling (vec2text, ALGEN) useless without the matrix, since those
attacks need the embedding space to be known or alignable. ⚠️ **The matrix is KELD'S, not the
client's**: it is generated deterministically from a seed held in configuration
(`KELD_TEXTEMBED_PROJECTION_SEED`) and issued to the fleet, so the client multiplies by a constant
it does not choose. This is a hardening measure, not a claim of impossibility; it is recorded here
so the reason survives someone later asking why the client multiplies by a constant.
"""
import math
import os
import re

# The stream tags. Kept separate, never concatenated — see the module docstring.
USER, ASST, THINK = "user", "asst", "think"
STREAMS = (USER, ASST, THINK)

# stream -> the AUTHOR of the turn it came off, from the published `message` row's closed role
# vocabulary (`enrich.FeatureRoles`, two values). `think` is the assistant's, not a third role: a
# thinking block is written by the assistant and a row published under a third role would be
# dropped whole at the Go decode boundary. Stated as a table because the mapping is a published
# contract and an `if stream == THINK` at the one call site is where it would quietly become one.
ROLE_OF = {USER: "user", ASST: "assistant", THINK: "assistant"}

# Encode width and publish width. Different numbers; see the docstring.
DIM_ENCODE = 1024
DIM_PUBLISH = int(os.environ.get("KELD_TEXTEMBED_DIM", "256"))

# The lookback ladder, in minutes back from the anchor instant, DISJOINT by construction (the
# spec's "shells, not nested windows"). `None` is "to the session's start". A caller that has its
# own ladder passes it in — `shells_for` takes the bounds as an argument precisely so this constant
# and a sibling module's cannot silently disagree while both look right.
SHELL_BOUNDS = ((0, 5), (5, 20), (20, 60), (60, 240), (240, None))

# Closed status vocabulary. Every outcome is STATED: an empty vector list from a stream that held
# nothing and one from a stream whose encoder was missing are different facts, and a consumer that
# cannot tell them apart reads the second as the first.
STATUS_OK = "ok"
STATUS_DISABLED = "skipped:disabled"
STATUS_EMPTY = "skipped:empty"
STATUS_NO_WEIGHTS = "degraded:weights_unavailable"
STATUS_UNAVAILABLE = "degraded:encoder_unavailable"
# ⚠️ A SIXTH OUTCOME, and it is not a degradation. Encoding runs OFF the request (see
# `featuretext.TextSource`: the daemon's sidecar client has a 5-second timeout and one measured
# batch of 64 real messages costs ~92 s, so a synchronous encode could never land), so "the encoder
# is working through this session's backlog" is a real state that is neither `ok` nor `empty` nor
# degraded. Without a name for it a caller reads a partial answer as a complete one.
STATUS_PENDING = "pending:encoding"
STATUSES = (STATUS_OK, STATUS_DISABLED, STATUS_EMPTY, STATUS_NO_WEIGHTS, STATUS_UNAVAILABLE,
            STATUS_PENDING)

DOWN, READY, UNAVAILABLE = "down", "ready", "unavailable"

# Per-chunk character cap. A chunk is packed out of WHOLE SENTENCES up to this many characters and
# the chunk vectors are mean-pooled, so the cap bounds one forward pass without ever bounding a
# sentence. 1200 characters is ~300 tokens of English prose, comfortably inside _MAX_TOKENS below,
# which is the actual memory bound.
_MAX_CHARS = int(os.environ.get("KELD_TEXTEMBED_MAX_CHARS", "1200"))
# The tokenizer's own hard cap. Reached only by a single sentence of code or a pathological token
# run; a chunk that reaches it is dropped by `sentence_chunks` before it gets here, so this is a
# backstop against the tokenizer, not the text bound.
_MAX_TOKENS = int(os.environ.get("KELD_TEXTEMBED_MAX_TOKENS", "512"))
_BATCH = int(os.environ.get("KELD_TEXTEMBED_BATCH", "8"))

# A sentence end, and the only place a message is ever cut. Bounded lookbehind so an abbreviation
# or a version number ("v1.23.4", "e.g.") does not read as a sentence end and split an identifier.
_SENTENCE_END = re.compile(r"(?<=[.!?])[\"')\]]*\s+(?=[^\s])")


def enabled():
    """Whether text embedding is switched on. OFF unless set, matching `KELD_CAPTURE`.

    Off is not "compute and discard": nothing is read, no child is spawned, and torch is never
    imported by this path."""
    return os.environ.get("KELD_TEXTEMBED", "0") == "1"


def weights_dir():
    """The local encoder weights, or `None` if they are not provisioned yet.

    `KELD_TEXTEMBED_DIR` is what the daemon sets at spawn once it has fetched them — the same seam
    `KELD_GLINER2_DIR` already is for the inference model. The default path is where the Go side's
    provisioner puts them, resolved through `KELD_HOME` because every Go path is.

    Returns `None` rather than an HF model id: this module must never trigger a download. An
    absent directory is a stated `degraded:weights_unavailable`, not a multi-gigabyte fetch
    started from inside a request."""
    explicit = os.environ.get("KELD_TEXTEMBED_DIR")
    if explicit:
        return explicit if os.path.isdir(explicit) else None
    home = os.environ.get("KELD_HOME") or os.path.join(os.path.expanduser("~"), ".keld")
    d = os.path.join(home, "models", "qwen3-embedding-0.6b")
    return d if os.path.isdir(d) else None


# ---- reading messages ------------------------------------------------------------------------
# This half never leaves the process. It is a module-internal seam, exercised directly by the
# tests; the published shape is MessageVector below, which holds no text.

class Message:
    """One message of one stream: an instant, a stream tag, its text, and the turn's own id.

    ⚠️ INTERNAL. Text lives inside this process and inside the encoder child, and nowhere else.
    No API in this module returns a `Message` to a caller outside it, and none is logged.

    `id` is the transcript turn's `uuid` — AN IDENTIFIER, never a fragment of the message. It is
    here because an instant is not a sufficient message key: the reference series quantizes to
    0.1 s and two turns can collide on one tick, so a published `message` row keyed on its instant
    could upsert its neighbour at Atlas. Last in the slot order and defaulted, so the constructor
    stays call-compatible with the tests that predate it."""

    __slots__ = ("t", "stream", "text", "id")

    def __init__(self, t, stream, text, id=None):     # noqa: A002 — `id` is the wire field's name
        self.t, self.stream, self.text, self.id = t, stream, text, id


def _blocks_of(kind, content):
    """The text of every `kind` content block, in order.

    Block-typed, and that is the whole `tool_result` guarantee: only `text` and `thinking` blocks
    are ever read, so a `tool_result` block riding the same message — which happens whenever a line
    also carries a `tool_use` and so survives `turns_in`'s skip — is structurally unreadable here
    rather than filtered out somewhere a later edit could miss."""
    if isinstance(content, str):
        return [content] if kind == "text" else []
    if not isinstance(content, list):
        return []
    field = "thinking" if kind == "thinking" else "text"
    return [b.get(field) or "" for b in content
            if isinstance(b, dict) and b.get("type") == kind]


def messages_in(turns, epoch_fn):
    """`Message`s from parsed transcript turns, in file order.

    `turns` are what `transcript.turns_in` yields — already filtered of the unparsed `tool_result`
    lines. `epoch_fn` converts a turn's ISO timestamp to an epoch (`capture.epoch`; injected rather
    than imported so this module keeps no opinion about the timezone contract, which that function
    owns and refuses on).

    A `user` turn that is machine text in a user-shaped envelope is dropped — `text.is_command_echo`
    covers slash-command echoes, injected skill files and task notifications. It cost the effort
    signals a 15% machine-text denominator when it was skipped there, and an embedding of a /login
    echo is a vector of the harness talking to itself.

    A turn whose timestamp cannot be read is skipped exactly as `turns_in` skips one: a message
    that cannot be placed in time can be in no shell."""
    from app.analysis.text import is_command_echo

    out = []
    for o in turns:
        role = (o.get("message") or {}).get("role") or o.get("type")
        content = (o.get("message") or {}).get("content")
        if content is None:
            content = o.get("content")
        try:
            t = epoch_fn(o["timestamp"])
        except Exception:                  # noqa: BLE001 — a transcript is another process's data
            continue
        # The turn's own id, carried so a published `message` row has a key that is not its
        # instant. `str()` and not a cast to anything narrower: it is another process's data and
        # its SHAPE is re-checked at the decode boundary, not its type here.
        mid = o.get("uuid")
        mid = str(mid) if mid else None
        if role == "user":
            for body in _blocks_of("text", content):
                body = (body or "").strip()
                if body and not is_command_echo(body):
                    out.append(Message(t, USER, body, mid))
        elif role == "assistant":
            for body in _blocks_of("text", content):
                body = (body or "").strip()
                if body:
                    out.append(Message(t, ASST, body, mid))
            for body in _blocks_of("thinking", content):
                body = (body or "").strip()
                if body:
                    out.append(Message(t, THINK, body, mid))
    return out


# ---- bounding a message ----------------------------------------------------------------------

def sentence_chunks(text, cap=None):
    """`(chunks, dropped_chars)` — whole sentences packed into chunks of at most `cap` characters.

    ⚠️ **Never cut text mid-sentence** (AGENTS.md). A long message is SPLIT at sentence boundaries
    and its chunk vectors mean-pooled by the caller; it is never truncated mid-clause. The measured
    cost of getting this wrong is on record: beats generated under a 200-rune cap came out
    mid-clause 46 times in 47, with a median of zero complete sentences — unusable output from a
    correct model, caused entirely by the cut.

    A single sentence longer than the cap is **dropped whole**, not cut. That is the same rule one
    level down: a cut identifier is a FALSE identifier, and a sentence of code or a pasted path
    list is exactly what exceeds this cap. **And the drop is declared** — `dropped_chars` is
    returned, `omittedNotice`'s rule applied one level up — so a message that lost a paragraph is
    distinguishable from one that never had it.

    Returns `([], 0)` for empty text, and `([], n)` for a message that was all over-long sentences:
    no vector, and a stated reason for there being none."""
    cap = _MAX_CHARS if cap is None else cap
    text = " ".join((text or "").split())
    if not text:
        return [], 0
    if len(text) <= cap:
        return [text], 0
    chunks, cur, dropped = [], "", 0
    for sentence in _SENTENCE_END.split(text):
        if not sentence:
            continue
        if len(sentence) > cap:
            # Whole, or not at all. See the docstring.
            dropped += len(sentence)
            continue
        if not cur:
            cur = sentence
        elif len(cur) + 1 + len(sentence) <= cap:
            cur = cur + " " + sentence
        else:
            chunks.append(cur)
            cur = sentence
    if cur:
        chunks.append(cur)
    return chunks, dropped


# ---- vectors ----------------------------------------------------------------------------------

class MessageVector:
    """The published per-message row: an instant, a stream tag, a vector, and what was dropped.

    ⚠️ No text field, and `test_textembed.py` asserts that by reflection rather than by reading.
    `dropped_chars` is a COUNT — it says a paragraph was too long to encode, never what it said.
    `id` is the turn's `uuid`, carried through from `Message` — an identifier, and bounded and
    whitespace-checked again at the Go decode boundary (`enrich.ValidAnchorID`)."""

    __slots__ = ("t", "stream", "vector", "chunks", "dropped_chars", "id")

    def __init__(self, t, stream, vector, chunks, dropped_chars, id=None):  # noqa: A002
        self.t, self.stream, self.vector = t, stream, vector
        self.chunks, self.dropped_chars = chunks, dropped_chars
        self.id = id


def normalize(v):
    """L2-normalise, or return the zero vector unchanged. A zero vector has no direction, and
    inventing one by dividing by an epsilon would give it a cosine of 1 with itself and an
    arbitrary one with everything else."""
    n = math.sqrt(sum(x * x for x in v))
    return list(v) if n == 0.0 else [x / n for x in v]


def truncate(v, dim=None):
    """MRL truncation: the first `dim` components, re-normalised.

    A prefix slice — no second forward pass. Re-normalised because every scalar below is a cosine,
    and a slice of a unit vector is not a unit vector; without this, `dispersion` would partly be
    measuring how much of a message's norm survived the slice."""
    dim = DIM_PUBLISH if dim is None else dim
    return normalize(v[:dim])


def cosine(a, b):
    """Cosine of two vectors this module produced, i.e. two unit vectors — so, their inner
    product. Falls back to the full computation for a zero vector, which is 0.0 by definition
    here rather than a division by zero."""
    na = math.sqrt(sum(x * x for x in a))
    nb = math.sqrt(sum(x * x for x in b))
    if na == 0.0 or nb == 0.0:
        return 0.0
    return sum(x * y for x, y in zip(a, b)) / (na * nb)


def mean_pool(vectors):
    """The mean of a set of vectors, re-normalised. The pooling of a message's chunk vectors and
    of a shell's message vectors alike — one function, because they are the same operation and
    Keld re-pools the published message rows with it."""
    if not vectors:
        return []
    dim = len(vectors[0])
    acc = [0.0] * dim
    for v in vectors:
        for i in range(dim):
            acc[i] += v[i]
    return normalize([x / len(vectors) for x in acc])


# ---- the fixed orthogonal projection ------------------------------------------------------------

_PROJECTIONS = {}


def projection(dim=None, seed=None):
    """The fixed orthogonal matrix applied before publish, as a list of rows.

    Deterministic from a seed held in CONFIGURATION, not chosen by the client:
    `KELD_TEXTEMBED_PROJECTION_SEED`. ⚠️ The matrix is Keld's — generated once per deployment,
    issued to the fleet, and held at Keld. A client multiplies by a constant it does not know the
    provenance of, which is the point: an inversion attack needs the embedding space, and an
    orthogonal change of basis withholds it while preserving cosine and inner products EXACTLY
    (`Qx · Qy == x · y` for orthogonal `Q`), so training is unaffected.

    Built by QR of a seeded Gaussian with the Mezzadri sign correction (`Q * sign(diag(R))`),
    which is what makes the factorisation's sign convention — the one part of QR that is not
    unique — reproducible rather than a property of whichever LAPACK is installed.

    Cached per `(dim, seed)`: it is a 256x256 constant, and regenerating it per message would cost
    a QR per row for nothing."""
    import numpy as np

    dim = DIM_PUBLISH if dim is None else dim
    if seed is None:
        seed = int(os.environ.get("KELD_TEXTEMBED_PROJECTION_SEED", "0"))
    key = (dim, seed)
    hit = _PROJECTIONS.get(key)
    if hit is not None:
        return hit
    rng = np.random.default_rng(seed)
    q, r = np.linalg.qr(rng.standard_normal((dim, dim)))
    d = np.diagonal(r)
    q = q * (d / np.abs(np.where(d == 0, 1.0, d)))
    m = [[float(x) for x in row] for row in q]
    _PROJECTIONS[key] = m
    return m


def project(v, matrix=None):
    """Apply the projection to one published-width vector."""
    matrix = projection(len(v)) if matrix is None else matrix
    return [sum(row[i] * v[i] for i in range(len(v))) for row in matrix]


# ---- the encoder child --------------------------------------------------------------------------

def _load(spec):
    """Load the encoder. CHILD SIDE ONLY — torch and transformers are imported here so the parent
    never pulls them in and the spawn re-import of this module stays cheap.

    ⚠️ **bfloat16, and the default is MEASURED — on BOTH axes, because the cost argument for it
    was only half true.** Measured on 200 real messages from the 40 largest transcripts on this
    host, 2 threads:

        float32   3113 MB after load, 3126 MB peak,  804.0 ms/message
        bfloat16  1673 MB after load, 1813 MB peak,  766.2 ms/message

    So bf16 is not a latency trade at all here; it is 1313 MB cheaper and marginally faster. fp32
    at 3.1 GB is the same order as the GLiNER2 worker this child is supposed to be cheap beside,
    and it buys nothing the published vector can carry: 256 dimensions of a unit direction.
    `KELD_TEXTEMBED_DTYPE=float32` restores the wider load for anyone who wants to re-check that
    rather than take it. (Both runs shared the host with a live sidecar at 131% CPU, so the
    latencies are upper bounds; the RSS figures are not affected.)

    bf16 rather than fp16 because this runs on CPU: torch's CPU kernels cover bfloat16, and fp16
    on CPU is emulated where it exists at all."""
    import torch
    from transformers import AutoModel, AutoTokenizer

    torch.set_num_threads(int(spec.get("threads") or 2))
    d = spec["dir"]
    dtype = getattr(torch, os.environ.get("KELD_TEXTEMBED_DTYPE", "bfloat16"), torch.bfloat16)
    tok = AutoTokenizer.from_pretrained(d, padding_side="left")
    model = AutoModel.from_pretrained(d, dtype=dtype)
    model.eval()
    return tok, model


def _encode_batch(tok, model, texts, max_tokens):
    """Last-token pooling with left padding, L2-normalised, at the ENCODE width.

    Last-token, not mean: this model's contrastive objective puts the sentence representation in
    the final position, and left padding is what keeps that position real for every row of a
    padded batch.

    Cast to float32 BEFORE normalising. Under the bf16 load (see `_load`) the hidden state has
    ~3 significant decimal digits, and a unit vector quantised at that width would put the noise
    floor of every cosine below at ~1e-2 — the scale the three scalars themselves live on. The
    forward pass is bf16; the arithmetic that turns it into a published direction is not."""
    import torch

    batch = tok(texts, padding=True, truncation=True, max_length=max_tokens, return_tensors="pt")
    with torch.no_grad():
        hidden = model(**batch).last_hidden_state[:, -1].float()
        hidden = torch.nn.functional.normalize(hidden, p=2, dim=1)
    return [[float(x) for x in row] for row in hidden]


def _release_memory():
    """Hand the forward pass's transient activations back to the OS. `worker.py`'s function
    verbatim in intent: gc first, then a glibc trim, both best-effort and neither allowed to
    raise (malloc_trim is Linux-only)."""
    import gc
    try:
        gc.collect()
    except Exception:
        pass
    try:
        import ctypes
        ctypes.CDLL("libc.so.6").malloc_trim(0)
    except Exception:
        pass


def _serve(req_q, resp_q, spec):
    """Child entrypoint: load, signal ready, encode one batch at a time until a `None` sentinel.

    ⚠️ An error crosses back as its exception CLASS NAME and nothing else. `repr(e)` is what
    `worker.py` sends, and it is safe there because that worker's inputs are already the caller's
    own text; here the input is a transcript message, and a tokenizer error's repr can quote it."""
    try:
        tok, model = _load(spec)
    except Exception as e:                 # noqa: BLE001 — absent weights are a normal state
        resp_q.put({"ready": False, "error": type(e).__name__})
        return
    resp_q.put({"ready": True})
    while True:
        req = req_q.get()
        if req is None:
            return
        try:
            resp_q.put({"ok": True, "vectors": _encode_batch(
                tok, model, req["texts"], req.get("max_tokens") or _MAX_TOKENS)})
        except Exception as e:             # noqa: BLE001 — one bad batch must not kill the child
            resp_q.put({"ok": False, "error": type(e).__name__})
        _release_memory()


def _default_spawn(spec):
    import multiprocessing as mp
    ctx = mp.get_context("spawn")
    req_q, resp_q = ctx.Queue(), ctx.Queue()
    proc = ctx.Process(target=_serve, args=(req_q, resp_q, spec), daemon=True)
    proc.start()
    return proc, req_q, resp_q


def _default_rss(pid):
    import psutil
    try:
        return psutil.Process(pid).memory_info().rss / (1024.0 * 1024.0)
    except Exception:
        return 0.0


class Encoder:
    """Parent-side handle to the encoder child. Spawned lazily, killed on idle, never resident in
    the FastAPI parent.

    Dependencies are injected (`spawn_fn`/`rss_fn`/`clock`) so the policy is testable against a
    stub encoder with no torch and no real process — the same shape `WorkerManager` uses, and the
    reason the tests below run in milliseconds."""

    def __init__(self, *, spawn_fn=None, rss_fn=None, clock=None,
                 idle_timeout_s=None, spawn_timeout_s=None, retry_s=None, weights=None):
        import threading
        import time
        self._spawn_fn = spawn_fn or _default_spawn
        self._rss_fn = rss_fn or _default_rss
        self._clock = clock or time.monotonic
        self._idle = float(os.environ.get("KELD_TEXTEMBED_IDLE_UNLOAD_S", "300")) \
            if idle_timeout_s is None else idle_timeout_s
        self._spawn_timeout = float(os.environ.get("KELD_TEXTEMBED_SPAWN_TIMEOUT_S", "180")) \
            if spawn_timeout_s is None else spawn_timeout_s
        self._retry = float(os.environ.get("KELD_TEXTEMBED_RETRY_S", "300")) \
            if retry_s is None else retry_s
        self._weights = weights            # test seam; None means resolve from the environment
        self._lock = threading.RLock()
        self._proc = self._req = self._resp = None
        self.state = DOWN
        self.status = STATUS_OK
        self._unavailable_at = None
        self._last_activity = self._clock()
        self.counts = {"spawns": 0, "kills_idle": 0, "failures": 0, "batches": 0}

    # -- lifecycle --
    def _mark_unavailable(self, status):
        self.state = UNAVAILABLE
        self.status = status
        self._unavailable_at = self._clock()
        self.counts["failures"] += 1

    def _may_retry(self):
        """A failed encoder is retried on a COOLDOWN, not latched and not per call.

        Not latched, because the weights are provisioned asynchronously and genuinely may arrive
        during this process's life — latching would mean a daemon restart to pick up a download
        that already finished. Not per call, because a failed spawn costs seconds and every job
        would pay it."""
        return self._unavailable_at is None or \
            (self._clock() - self._unavailable_at) >= self._retry

    def _ensure_up(self):
        if self.state == READY:
            return True
        if self.state == UNAVAILABLE and not self._may_retry():
            return False
        d = self._weights if self._weights is not None else weights_dir()
        if not d:
            self._mark_unavailable(STATUS_NO_WEIGHTS)
            return False
        try:
            self._proc, self._req, self._resp = self._spawn_fn(
                {"dir": d, "threads": int(os.environ.get("KELD_SIDECAR_MAX_THREADS", "2"))})
            self.counts["spawns"] += 1
            msg = self._resp.get(timeout=self._spawn_timeout)
        except Exception:              # noqa: BLE001 — every spawn failure is the same outcome
            self._kill()
            self._mark_unavailable(STATUS_UNAVAILABLE)
            return False
        if not (isinstance(msg, dict) and msg.get("ready")):
            self._kill()
            # A child that loaded nothing is a weights problem far more often than a torch one,
            # and the two are not distinguishable from here without shipping the child's message
            # text across — which is exactly what must not cross.
            self._mark_unavailable(STATUS_NO_WEIGHTS)
            return False
        self.state = READY
        self.status = STATUS_OK
        self._unavailable_at = None
        self._last_activity = self._clock()
        return True

    def _kill(self):
        proc = self._proc
        if proc is not None:
            try:
                proc.kill()
                proc.join(timeout=5)
            except Exception:
                pass
        self._proc = self._req = self._resp = None
        if self.state == READY:
            self.state = DOWN

    def maybe_unload(self):
        """Kill an idle child. The encoder's duty cycle is ~100 messages a day; holding ~1.4 GB
        between them is the cost this child exists to avoid."""
        with self._lock:
            if self.state == READY and self._idle > 0 and \
                    (self._clock() - self._last_activity) >= self._idle:
                self._kill()
                self.counts["kills_idle"] += 1
                return True
        return False

    def shutdown(self):
        with self._lock:
            if self._req is not None:
                try:
                    self._req.put(None)
                except Exception:
                    pass
            self._kill()
            self.state = DOWN

    def rss_mb(self):
        p = self._proc
        return self._rss_fn(p.pid) if p else 0.0

    # -- dispatch --
    def encode(self, texts):
        """`(vectors, status)` at the ENCODE width, or `([], status)` with the reason stated.

        Never raises for an unavailable encoder: absent weights, a failed spawn and a failed batch
        are all "no vectors, and here is why". This is the degrade the module contract requires —
        a missing model must cost the text half of the row, never the row and never the daemon."""
        if not enabled():
            return [], STATUS_DISABLED
        if not texts:
            return [], STATUS_EMPTY
        with self._lock:
            if not self._ensure_up():
                return [], self.status
            out = []
            for i in range(0, len(texts), _BATCH):
                self._req.put({"texts": list(texts[i:i + _BATCH]), "max_tokens": _MAX_TOKENS})
                try:
                    resp = self._resp.get(timeout=self._spawn_timeout)
                except Exception:          # noqa: BLE001 — a hung child is an unavailable one
                    self._kill()
                    self._mark_unavailable(STATUS_UNAVAILABLE)
                    return [], self.status
                if not (isinstance(resp, dict) and resp.get("ok")):
                    self._kill()
                    self._mark_unavailable(STATUS_UNAVAILABLE)
                    return [], self.status
                out.extend(resp["vectors"])
                self.counts["batches"] += 1
            self._last_activity = self._clock()
            return out, STATUS_OK


# ---- the per-message pass -----------------------------------------------------------------------

def embed(messages, encoder, matrix=None, dim=None, cap=None):
    """`(vectors, statuses)` — one `MessageVector` per message that produced one, and a per-stream
    status.

    The whole cost model of this module is here: every message is chunked at sentence boundaries,
    every chunk of every message goes into ONE batched call, and the chunk vectors of a message are
    mean-pooled back into that message's single vector. So a message is encoded once and a shell
    re-uses it; the batch is flat rather than per-message because a per-message call would pay the
    queue round-trip ~100 times a day for nothing.

    Chunks are pooled at the ENCODE width and the message vector is sliced once, in that order:
    slicing first would pool 256-d fragments of a space whose geometry is 1024-d.

    The projection is applied LAST, to the published-width vector, because it is a change of basis
    of the published space and nothing downstream ever sees the pre-projection one."""
    dim = DIM_PUBLISH if dim is None else dim
    statuses = {}
    present = {m.stream for m in messages}
    for s in STREAMS:
        if s not in present:
            statuses[s] = STATUS_EMPTY

    flat, owners, dropped = [], [], []
    for m in messages:
        chunks, drop = sentence_chunks(m.text, cap)
        dropped.append(drop)
        for c in chunks:
            flat.append(c)
            owners.append(len(dropped) - 1)

    if not flat:
        for s in present:
            statuses[s] = STATUS_EMPTY
        return [], statuses

    raw, status = encoder.encode(flat)
    if not raw:
        for s in present:
            statuses[s] = status
        return [], statuses

    matrix = projection(dim) if matrix is None else matrix
    per_message = {}
    for vec, owner in zip(raw, owners):
        per_message.setdefault(owner, []).append(vec)

    out = []
    for i, m in enumerate(messages):
        chunks = per_message.get(i)
        if not chunks:
            continue
        pooled = mean_pool(chunks)                 # at the encode width
        published = project(truncate(pooled, dim), matrix)
        out.append(MessageVector(m.t, m.stream, published, len(chunks), dropped[i], m.id))
    for s in present:
        statuses[s] = STATUS_OK if any(v.stream == s for v in out) else STATUS_EMPTY
    return out, statuses


# ---- shells and the derived scalars --------------------------------------------------------------

def shells_for(anchor_t, vectors, bounds=None):
    """Partition message vectors into the DISJOINT lookback ladder ending at `anchor_t`.

    Disjoint, not nested: `[0,5m)`, `[5,20m)`, `[20,60m)`, `[60,240m)`, `[240m, session start)`.
    Bounds are an argument so a sibling module's ladder can be passed in rather than duplicated
    here — two constants that must agree and are written twice is how they stop agreeing.

    A vector at or after the anchor belongs to no shell: the ladder looks BACK."""
    bounds = SHELL_BOUNDS if bounds is None else bounds
    shells = [[] for _ in bounds]
    for v in vectors:
        age = (anchor_t - v.t) / 60.0
        if age < 0:
            continue
        for i, (lo, hi) in enumerate(bounds):
            if age >= lo and (hi is None or age < hi):
                shells[i].append(v)
                break
    return shells


def novelty_of(vector, earlier):
    """`1 − max cos(vector, any earlier vector)`. `None` when there is no earlier message: a first
    message is not maximally novel, it is unmeasured, and returning 1.0 would publish the strongest
    possible reading of no evidence at all — the CONTRAST-NEVER-FALLBACK rule `prior.py` states one
    level up.

    ⚠️ **The max is taken in numpy, and that is a COST fix rather than a preference.** This is the
    only quadratic quantity in the module — every message of a shell against every message before
    it — and `/features` computes a ladder per anchor row, up to 96 in one call. Measured on a real
    session: the pure-Python double loop cost ~4 s for ONE row at 500 message vectors (0.08 * n^2
    cosines of 256 components each), i.e. minutes per call; the same arithmetic in numpy is
    milliseconds. The formula is unchanged — norms are divided out exactly as `cosine` does, so a
    non-unit vector reads the same — and a zero-norm row contributes 0.0, matching `cosine`'s own
    definition here rather than dividing by zero.
    """
    if not earlier:
        return None
    import numpy as np

    v = np.asarray(vector.vector, dtype=np.float64)
    m = np.asarray([e.vector for e in earlier], dtype=np.float64)
    nv = math.sqrt(float(v @ v))
    nm = np.sqrt((m * m).sum(axis=1))
    if nv == 0.0:
        return max(0.0, 1.0 - 0.0)
    cos = np.where(nm == 0.0, 0.0, (m @ v) / np.where(nm == 0.0, 1.0, nm) / nv)
    return max(0.0, 1.0 - float(cos.max()))


def shell_stats(shell, stream, prev_centroid=None, earlier=None):
    """The per-shell, per-stream derivation: `centroid` and the three scalars.

    - `dispersion` — `1 − mean cos(message, centroid)`. How varied the talk was.
    - `drift` — `1 − cos(this centroid, the previous shell's)`. The text analogue of
      `dynamics.turnover`.
    - `novelty` — the mean over this shell's messages of `1 − max cos(message, any earlier message
      of the SAME STREAM in the session)`. A mean, matching `dispersion`, so the two read on the
      same scale; `None` when no message in the shell had anything earlier to be novel against.

    Every scalar is `None` where it cannot be computed, never 0.0. A drift of 0.0 says the work did
    not move; an absent previous shell says nothing was compared, and rendering the second as the
    first is the single misreading the evidence-floor work across this package exists to prevent.
    `status` states which case this is.

    ⚠️ The CENTROID is returned but is deliberately NOT published on a `bin`/`block` row — it is an
    exact pooling of message vectors Atlas already holds, so publishing it would triple the payload
    to say nothing new and would freeze one pooling function into the wire. It is returned because
    the NEXT shell's `drift` needs it, which is the same reason the three scalars publish and the
    centroid does not."""
    vecs = [v for v in shell if v.stream == stream]
    if not vecs:
        return {"stream": stream, "n": 0, "status": STATUS_EMPTY, "centroid": None,
                "dispersion": None, "drift": None, "novelty": None}
    centroid = mean_pool([v.vector for v in vecs])
    # max(0.0, ...) on every scalar: `1 - cos` is non-negative for real vectors, but a cosine
    # computed as a sum of 256 floats lands a hair above 1.0 for a vector against itself, which
    # publishes -0.0 for a single-message shell. That is arithmetic noise wearing the shape of a
    # sign, and a sign is exactly what a reader would try to interpret.
    dispersion = max(0.0, 1.0 - (sum(cosine(v.vector, centroid) for v in vecs) / len(vecs)))
    drift = None if not prev_centroid else max(0.0, 1.0 - cosine(centroid, prev_centroid))
    novelties = [n for n in (novelty_of(v, earlier or []) for v in vecs) if n is not None]
    novelty = (sum(novelties) / len(novelties)) if novelties else None
    return {"stream": stream, "n": len(vecs), "status": STATUS_OK, "centroid": centroid,
            "dispersion": dispersion, "drift": drift, "novelty": novelty}


def ladder(anchor_t, vectors, bounds=None):
    """The full per-shell, per-stream derivation for one row.

    Shells are walked OLDEST FIRST for `drift`, because "the previous shell" means the one further
    back in time, and the ladder is written newest-first. `earlier` for `novelty` is every message
    of that stream strictly before the shell's own oldest member — "earlier in the SESSION", which
    includes messages outside the ladder entirely."""
    bounds = SHELL_BOUNDS if bounds is None else bounds
    shells = shells_for(anchor_t, vectors, bounds)
    ordered = list(reversed(list(enumerate(shells))))
    prev = {s: None for s in STREAMS}
    out = [None] * len(shells)
    for idx, shell in ordered:
        row = {}
        for s in STREAMS:
            members = [v for v in shell if v.stream == s]
            cutoff = min((v.t for v in members), default=None)
            earlier = [v for v in vectors
                       if v.stream == s and cutoff is not None and v.t < cutoff]
            row[s] = shell_stats(shell, s, prev[s], earlier)
            if row[s]["centroid"]:
                prev[s] = row[s]["centroid"]
        out[idx] = {"bounds": bounds[idx], "streams": row}
    return out
