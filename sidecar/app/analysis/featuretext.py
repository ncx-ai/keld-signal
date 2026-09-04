"""The TEXT half of a feature row, and THE ONLY THING ON THIS PATH THAT OPENS A TRANSCRIPT.

    a transcript -> per-message text vectors (once, ever) -> the `text` half of a feature row

`features.py` computes `S(t)` out of the reference series and is a QUERY, never a parse — pinned by
`test_features.py`'s `test_features_never_opens_a_transcript`, because `/features` is driven by a
sampling grid and a whole-file parse inside one row would be paid dozens of times per call. The
text half cannot be a query: a message's words are in the file and nowhere else. So it lives here,
behind a seam `features.feature_rows` takes as an argument, and `features.py` keeps its property.

⚠️ **Raw text does not leave this process.** `textembed` reads it, hands it to an encoder child, and
returns vectors; this module holds `MessageVector`s (an instant, a stream tag, a turn id, a vector,
a dropped-character count) and never a `Message`. Nothing here logs, stores or returns text.

## THE CACHE IS THE COST MODEL, NOT AN OPTIMISATION

A message is encoded ONCE, EVER — that is `textembed`'s central claim and the reason its unit is
the message rather than the shell. Nothing enforces it without a cache: shells overlap across rows
(a message in this row's `[0,5m)` is in the next row's `[5,20m)`), the emitter calls this route
repeatedly as its cursor advances, and every call sees the whole session's history because the
outermost shell reaches back to the session's start. Uncached, a session with `n` messages costs
`O(n)` forward passes per CALL at ~766 ms each.

So vectors are memoised per `(session, turn id, stream, block ordinal)` for the process's life. In
steady state a call encodes only the messages that arrived since the last one — a handful. The
FIRST call on an existing session pays for its whole history, once, and that is the honest cost of
turning the toggle on mid-session; it is bounded by the session, not by the corpus.

⚠️ Deliberately **not persisted**. A vector on disk is a vector at rest derived from message text,
and this file is not the place to introduce that class of storage; a re-encode after a restart
costs CPU and nothing else. `KELD_TEXTEMBED_CACHE_SESSIONS` bounds how many sessions are held,
evicting the least recently used, because a machine has many transcripts and the emitter's active
set is one or two.

## ⚠️ ENCODING RUNS OFF THE REQUEST, AND THAT IS FORCED BY A MEASUREMENT

The daemon's sidecar client has a **5-second** HTTP timeout (`daemon.go`, sized for enrichment
inference), and one measured batch of 64 real messages from a 26 MB transcript costs **~92 s** —
plus the child's first model load, measured by `loadtest embed` at **2.8 s with a warm page cache
and ~20 s cold** (this docstring said ~90 s, which was one cold contended reading and is corrected
here; the argument does not turn on it, since even 2.8 s is past the 5 s budget once any encoding
follows it). A synchronous encode could not land inside one
call at ANY batch size worth having: even four messages is ~6 s. And a timed-out POST is a
transport error, which `Client.post` classes as retryable, so the failure mode is not one slow
response but an unbounded retry loop against work that never finishes in time.

So `vectors()` serves what the cache already holds and hands the remainder to a single background
worker. Every call returns in the time it takes to read the transcript (**0.08 s** on the same
file) plus a dictionary walk. The instant of the first message with no vector is returned as the
**FRONTIER**, and `features.feature_rows` emits no row at or after it — so the caller's cursor
never advances past a message whose vector has not been computed, and every row that IS published
has its text half measured over a COMPLETE message history up to its own instant. The frontier
retreats call by call as the worker catches up; catching up a long session is many cheap calls
rather than one that cannot succeed.

`pending:encoding` is the status while that is happening. It is neither `ok` nor `empty` nor
degraded, and a caller without a name for it reads a partial answer as a complete one.

## STATUSES ARE STATED, NEVER INFERRED FROM AN EMPTY LIST

`textembed.STATUSES` is closed and every outcome is named: off, no weights, encoder unavailable, or
a stream that genuinely held nothing. An empty vector list means all four and a caller that cannot
tell them apart reads "the encoder is missing" as "the person said nothing". The statuses ride the
RESPONSE (`text_status`), not the wire rows — the Go side models neither and drops what it does not
model, so this is an operator- and study-facing fact that structurally cannot reach Atlas.
"""
import os
import threading

from app.analysis import textembed as te
from app.analysis.capture import epoch
from app.analysis.ingest import session_of
from app.analysis.transcript import iter_turns

# How many sessions' vectors are held. The emitter's active set is one or two transcripts on a real
# machine (its own sweep is activity-driven for exactly that reason), so this is generous; it exists
# so a backfill across a 496-transcript corpus cannot grow without bound.
_CACHE_SESSIONS = int(os.environ.get("KELD_TEXTEMBED_CACHE_SESSIONS", "4"))

# How many messages ONE BACKGROUND PASS encodes, OLDEST FIRST.
#
# ⚠️ It is not a request-latency bound — encoding is off the request entirely (see the module
# docstring). It bounds how long the worker holds the encoder child resident and busy between
# re-reads of the transcript, so a session that is still being appended to gets its new messages
# reasonably soon rather than behind a whole backlog. Measured on a real 26 MB Claude Code
# transcript (1,646 messages, 2,174 sentence chunks, 2 threads, real Qwen3-Embedding-0.6B weights):
# ~92 s per 64 messages, i.e. ~1.44 s/message, against the module's own 766 ms/message figure
# measured on shorter inputs. A whole such session is ~40 minutes of encoding, once, and that is
# the honest cost of turning the toggle on mid-session.
#
# ⚠️ The ~1.44 s is the figure that REPLICATED, and 766 ms is the one that did not: `loadtest embed`
# measured **1,119-1,635 ms/message** across runs, over 104-134 messages of sustained encoding on a
# real 14.4 MB transcript. Size any per-message cost off ~1.1-1.6 s (see textembed._load).
_MAX_ENCODE = int(os.environ.get("KELD_TEXTEMBED_MAX_ENCODE", "64"))

# The encoder's published identity. The model name is read off the weights DIRECTORY rather than
# hardcoded, because the directory is what was actually loaded — a mismatch between the two is
# precisely the version skew `EncoderRef` exists to make visible.
_DEFAULT_MODEL = "qwen3-embedding-0.6b"

# Two distinct cache states, and conflating them is what wedges the cursor. `_NO_VECTOR` means the
# encoder RAN on this message and there was nothing to encode; `_NOT_TRIED` is a cache miss.
_NO_VECTOR = object()
_NOT_TRIED = object()


def encoder_ref(weights=None):
    """`{"model", "width", "projection"}` — the identity every published text vector needs.

    ⚠️ **`width` is the PUBLISHED width, not the encode width.** The encoder runs at 1,024 and the
    row carries a 256-prefix; conflating the two puts the volume estimate out by 4x, so the row
    states the one that is actually on the wire.

    `projection` names the fixed orthogonal change of basis applied before publish. It is the SEED,
    not the matrix: the matrix is Keld's, issued to the fleet, and the client multiplies by a
    constant it did not choose. Named so a rotation is detectable rather than silent — two corpora
    under different seeds are not poolable and nothing about the numbers says so.
    """
    d = te.weights_dir() if weights is None else weights
    return {"model": os.path.basename(str(d).rstrip("/")) if d else _DEFAULT_MODEL,
            "width": te.DIM_PUBLISH,
            "projection": "orth-%d" % int(
                os.environ.get("KELD_TEXTEMBED_PROJECTION_SEED", "0"))}


class TextSource:
    """The seam `features.feature_rows` takes: "the text vectors of this transcript's messages".

    One per process. `encoder` and `reader` are injectable so the policy is testable against a stub
    encoder with no torch and a fixture with no file — the shape `textembed.Encoder` itself uses,
    and the reason its tests run in milliseconds.
    """

    def __init__(self, encoder=None, reader=None, max_sessions=None, background=True):
        self._encoder = encoder if encoder is not None else te.Encoder()
        self._reader = reader if reader is not None else iter_turns
        self._max_sessions = _CACHE_SESSIONS if max_sessions is None else int(max_sessions)
        # `background=False` runs the encode INLINE. It is the study and test seam, not a second
        # production path: the arithmetic is identical either way — same batch, same order, same
        # cache — so a test that drains the worker and a test that runs it inline are asserting the
        # same function. Production never passes it.
        self._background = bool(background)
        self._lock = threading.RLock()
        # session -> {key: MessageVector}, insertion-ordered so the oldest entry is the LRU victim.
        self._cache = {}
        self._worker = None
        # The encoder's own last word, so a status can be reported by a call that did no encoding.
        self._last_status = None
        # Un-encoded messages seen by the LAST call — the backlog behind the frontier, which is
        # what a caller's cursor is actually waiting on. Recorded rather than recomputed because
        # deriving it costs a transcript read, and /metrics must not open one.
        self._pending = 0
        self.counts = {"encoded": 0, "reused": 0, "reads": 0, "passes": 0}

    def poll(self):
        """The periodic lifecycle+observability tick, called off the event loop.

        Two things, in this order, for the reason `worker_manager.poll` does the same: the RSS
        sample must happen whether or not the policy below fires, and it must happen before a kill
        clears the child's peak. Keeping them in one method is what stops a caller from wiring the
        policy and silently dropping the sampling (see `textembed.Encoder.observe_rss`)."""
        self._encoder.observe_rss()
        return self.maybe_unload()

    def observe_rss(self):
        return self._encoder.observe_rss()

    def maybe_unload(self):
        """Let the encoder child go if it has been idle. Called from the same poll loop that
        recycles the inference worker; the encoder's duty cycle is ~100 messages a day and holding
        ~1.7 GB between them is the cost the child exists to avoid."""
        return self._encoder.maybe_unload()

    def shutdown(self):
        self.drain(timeout=30.0)
        self._encoder.shutdown()

    def drain(self, timeout=None):
        """Wait for the current background pass, if any. The test and shutdown seam; nothing on the
        request path calls it, because waiting is the thing this design exists not to do."""
        w = self._worker
        if w is not None and w is not threading.current_thread():
            w.join(timeout)
        return self._worker is None

    def rss_mb(self):
        return self._encoder.rss_mb()

    @property
    def state(self):
        return self._encoder.state

    @property
    def encoder(self):
        """The encoder child handle, for a SECOND consumer of the same child.

        ⚠️ There is one encoder child per process and there must stay one: it is
        1.7-2.4 GB resident (measured, bf16, real weights) against a budget
        AGENTS.md already documents as oversubscribed, and it is idle-unloaded
        and shut down through THIS object's `poll`/`shutdown` — which the app's
        poll loop and lifespan already call. So a second consumer (`/attribute`,
        which embeds a block span against the declared projects) takes this
        handle rather than constructing an `Encoder` of its own, which would
        double the memory and leave the second child with nothing to release it.

        Exposed read-only and deliberately not an `encode()` passthrough: this
        class's own contract is the per-message CACHE and the frontier, and a
        block span is neither. A caller wanting raw vectors is asking the child,
        not this source, and should say so."""
        return self._encoder

    def stats(self, block):
        """Fill `block` (from `embed_stats`) with this source's live figures.

        Takes the skeleton rather than building one so the block has ONE shape whether or not a
        source exists — a /metrics consumer must not have to handle two.

        `peak_rss_mb` is the high-water for the current child generation and `rss_mb` the
        instantaneous reading; both, never one. See `textembed.Encoder.observe_rss` for why the
        instantaneous one alone is not observability.
        """
        e = self._encoder
        w = self._worker
        with self._lock:
            sessions = len(self._cache)
            # Cached ENTRIES, not vectors: a message the encoder ran on and produced nothing for
            # is a real entry (`_NO_VECTOR`) and is exactly what stops the frontier latching, so
            # counting only vectors would under-report the work already done.
            cached = sum(len(v) for v in self._cache.values())
            pending, status = self._pending, self._last_status
        block.update({
            "state": e.state,
            "status": status or e.status,
            "rss_mb": round(e.rss_mb(), 1),
            "peak_rss_mb": round(e.peak_rss_mb, 1),
            "pending_messages": pending,
            # A background pass in flight. `pending_messages` above is a backlog, this is whether
            # anything is working through it — flat backlog with `encoding: false` is the shape of
            # a wedged encoder, and neither field says it alone.
            "encoding": bool(w is not None and w.is_alive()),
            "cached_sessions": sessions,
            "cached_messages": cached,
            # `batch_ms_total` is a running sum, kept in `counts` so it accumulates with the
            # count it is divided by; it is dropped from the published block because a total in
            # milliseconds is not a number anyone reads — the mean beside the count is.
            "counts": {k: v for k, v in dict(self.counts, **e.counts).items()
                       if k != "batch_ms_total"},
            # The child's own report, not a re-derivation: see `Encoder.dtype`. Absent until a
            # child has come up, which is why the default block above states it as None.
            "dtype": e.dtype,
            "last_batch_ms": round(e.last_batch_ms, 1),
            "mean_batch_ms": round(e.counts["batch_ms_total"] / e.counts["batches"], 1)
            if e.counts.get("batches") else 0.0,
            "last_encode_ms": round(e.last_encode_ms, 1),
        })
        return block

    def _touch(self, session):
        """Move `session` to the most-recently-used end and evict down to the bound."""
        entry = self._cache.pop(session, None)
        if entry is None:
            entry = {}
        self._cache[session] = entry
        while len(self._cache) > max(1, self._max_sessions):
            self._cache.pop(next(iter(self._cache)))
        return entry

    def vectors(self, path, max_encode=None):
        """`(vectors, statuses, encoder_ref, frontier)` for the messages of this transcript.

        `vectors` are `textembed.MessageVector`s in file order — an instant, a stream tag, the
        turn's id and a 256-d published vector. `statuses` is the per-stream closed status.
        `encoder_ref` is `None` wherever there is no vector at all, so a row can never carry a text
        vector without the identity it must be pooled under.

        `frontier` is the instant of the first message this call could not encode, or `None` when
        every message has a vector. ⚠️ **A CALLER MUST NOT EMIT A ROW AT OR AFTER IT** — see
        `_MAX_ENCODE`: past the frontier the message history is incomplete, so a text scalar
        computed there would be a confident number measured over a fraction of the words. It is
        returned rather than enforced here because this module knows nothing about anchors.

        Returns `([], statuses, None, None)` and never raises: absent weights, a failed spawn, a
        hung child and an unreadable transcript are all "no text half, and here is why". A missing
        model must cost the text half of a row, never the row and never the daemon. `frontier` is
        `None` in those cases and not 0.0, because nothing was partial — the whole half is out, and
        `statuses` says so.
        """
        if not te.enabled():
            return [], {s: te.STATUS_DISABLED for s in te.STREAMS}, None, None
        bound = _MAX_ENCODE if max_encode is None else int(max_encode)
        session = session_of(path)
        try:
            messages = te.messages_in(self._reader(path), epoch)
        except OSError:
            # The transcript is gone or unreadable. The SERIES still holds everything ingested from
            # it, so the structured half of every row is unaffected — this is the text half
            # degrading alone, which is exactly the split the module contract requires.
            return [], {s: te.STATUS_UNAVAILABLE for s in te.STREAMS}, None, None
        self.counts["reads"] += 1
        keys = _keys(messages)

        with self._lock:
            cache = self._touch(session)
            pending = [(k, m) for k, m in zip(keys, messages)
                       if k is not None and k not in cache]
            self.counts["reused"] += len(messages) - len(pending)
            self._pending = len(pending)
            # OLDEST FIRST, and the bound cuts the TAIL. `messages_in` yields file order, so the
            # un-encoded remainder is contiguous and its first member is the frontier — which is
            # what lets the caller's cursor stop cleanly rather than straddle a gap.
            batch = pending[:max(0, bound)]
            start = bool(batch) and (self._worker is None or not self._worker.is_alive())

        if start and not self._background:
            self._pass(session, batch)
        elif start:
            # ONE worker at a time, and it is a daemon thread: the encoder child is single-flight
            # anyway (one `Encoder`, one lock), so a second thread would only queue behind the
            # first while holding a second copy of the batch. Fire and forget — the frontier is how
            # the caller learns how far it has got, so there is nothing to await.
            w = threading.Thread(target=self._pass, args=(session, batch),
                                 name="keld-textembed", daemon=True)
            self._worker = w
            w.start()

        with self._lock:
            cache = self._cache.get(session) or {}
            out, present, frontier = [], set(), None
            for k, m in zip(keys, messages):
                v = cache.get(k, _NOT_TRIED) if k is not None else _NOT_TRIED
                if v is _NOT_TRIED:
                    if frontier is None:
                        frontier = float(m.t)     # the first message with no vector yet
                    continue
                if v is _NO_VECTOR:
                    continue          # the encoder ran and there was nothing to encode
                out.append(v)
                present.add(m.stream)
            last = self._last_status

        # ⚠️ A DEGRADED ENCODER DROPS THE TEXT HALF WHOLE AND RETURNS NO FRONTIER. A frontier says
        # "the history is complete up to here and incomplete after"; with the encoder down that is
        # not what happened, and returning one would hold every row back on a failure that is
        # supposed to cost only the text half. Cached vectors are dropped for this call too, because
        # publishing rows over a prefix of the history while the rest is unreachable is exactly the
        # confident-number-over-a-fraction failure the frontier exists to prevent.
        if str(last or "").startswith("degraded:"):
            present_any = {m.stream for m in messages}
            return [], {s: (last if s in present_any else te.STATUS_EMPTY)
                        for s in te.STREAMS}, None, None

        statuses = {}
        for s in te.STREAMS:
            if s in present:
                statuses[s] = te.STATUS_OK
            elif frontier is not None and any(m.stream == s for m in messages):
                # The stream HAS messages and none has a vector yet — that is neither empty nor
                # degraded, and a caller without this name reads a partial answer as a complete one.
                statuses[s] = te.STATUS_PENDING
            else:
                statuses[s] = te.STATUS_EMPTY
        return out, statuses, (encoder_ref() if out else None), frontier

    def _pass(self, session, batch):
        """Encode one batch into the cache. Runs on the background worker (or inline under
        `background=False`), NEVER holding the cache lock across the forward pass.

        ⚠️ **A MESSAGE THE ENCODER RAN ON AND PRODUCED NOTHING FOR IS CACHED AS `_NO_VECTOR`, and
        without that the frontier LATCHES AND THE CURSOR WEDGES FOREVER.** A message whose every
        sentence exceeds the chunk cap is dropped WHOLE (`sentence_chunks` never cuts one), so it
        will never have a vector however many times it is retried; left uncached it would fix the
        frontier at its own instant and no row after it could ever be emitted again. That is kept
        distinct from a DEGRADED encoder, which genuinely must be retried — absent weights arrive
        asynchronously — by asking whether the encoder ran at all.
        """
        try:
            new, statuses = te.embed([m for _k, m in batch], self._encoder)
            attempted = not any(str(v).startswith("degraded:") for v in statuses.values())
            degraded = next((v for v in statuses.values() if str(v).startswith("degraded:")), None)
            aligned = _aligned([m for _k, m in batch], new)
            with self._lock:
                self._last_status = degraded
                if attempted:
                    cache = self._cache.setdefault(session, {})
                    for (k, _m), v in zip(batch, aligned):
                        if k is None:
                            continue
                        cache[k] = v if v is not None else _NO_VECTOR
                self.counts["encoded"] += len(new)
                self.counts["passes"] += 1
        finally:
            self._worker = None


def embed_stats(source):
    """The `embed` block of /metrics: the text encoder child, its memory, and its backlog.

    ⚠️ **This block exists because an unobserved ~1.7-2.3 GB child is the mistake this codebase has
    already paid for.** `worker_manager` reports `peak_rss_mb` for exactly one reason — an
    instantaneous sample made a worker oscillating 2715 -> 5692 MB against a 3409 MB ceiling look
    healthy — and this child is of the same order and rides the same budget. So the peak is
    reported beside the live reading, never instead of it.

    `source` is `None` when no `TextSource` has been built: the toggle is off, or it is on and no
    `/features` call has needed one yet. Those are reported as `state: "down"` with the
    environment-level facts still stated, NOT as a null block — "the encoder is not running" is a
    correct and useful answer, and a null one is indistinguishable from a broken poll.

    ⚠️ **Pure reads only.** A metrics poll must never build a `TextSource`, spawn the child, load
    weights, open a transcript or wait behind the encode lock; every field here is a counter, a
    cached number, an environment read or a lock-free RSS sample. `pending_messages` is the
    backlog recorded by the last `/features` call for that reason — recomputing it means a
    transcript read.
    """
    enc = te.enabled()
    block = {
        # The toggle, and the child's own state, kept separate: enabled-but-down is the normal
        # state of a machine between sessions (idle-unloaded), and enabled-but-unavailable is a
        # failure. One field cannot say both.
        "enabled": enc,
        "state": te.DOWN,
        "status": None,
        # Whether the weights are on disk at all. The single most common reason for
        # `degraded:weights_unavailable`, and the one an operator can act on: provisioning is
        # asynchronous, so "not yet" is a normal early state rather than a defect.
        "weights_present": te.weights_dir() is not None,
        # The identity every published vector must be pooled under, stated even with the child
        # down: `width` is the PUBLISHED width (256), which is the one parameter that cannot be
        # revised retroactively, and `projection` is the seed, so a fleet-wide rotation is visible.
        "encoder": encoder_ref(),
        # ⚠️ **BESIDE `encoder`, DELIBERATELY NOT INSIDE IT.** `encoder_ref` is the POOLABILITY
        # identity — model, published width, projection seed — and a field inside it reads as
        # "two corpora under different values are not poolable". Measured, two dtypes are: the
        # same three chunks encode to cosine 0.99983-0.99990 across bf16 and fp32 at both widths,
        # against an attribution MARGIN of 0.08. So the dtype belongs in the operability block,
        # where "which arm did THIS host pick" is the question, and not in the identity, where it
        # would invent an incompatibility the numbers do not show.
        "dtype": None,
        # The encode width (1024) beside it, because the two differ by 4x and conflating them puts
        # any volume estimate out by the same factor.
        "encode_width": te.DIM_ENCODE,
        "rss_mb": 0.0,
        "peak_rss_mb": 0.0,
        "pending_messages": 0,
        "encoding": False,
        "cached_sessions": 0,
        "cached_messages": 0,
        "counts": {"encoded": 0, "reused": 0, "reads": 0, "passes": 0,
                   "spawns": 0, "batches": 0, "failures": 0, "kills_idle": 0,
                   "kills_stalled": 0},
        # ⚠️ **THE COST OF A BATCH IS THE NUMBER THIS SUBSYSTEM IS ACTUALLY TUNED BY, and it was
        # not reported until 2026-09-03.** Everything sized against this child — the thread
        # ceiling, the attribution heartbeat window, whether a block can be attributed at all —
        # rests on "~1.1-1.6 s per message", a figure from a load test on one machine. A batch is
        # 8 chunks, so `mean_batch_ms` is that figure times eight on THIS machine, measured
        # continuously and for free: the count was already kept, so the total is one addition.
        # `last_encode_ms` is the whole-block cost beside it, which is what the daemon's deadline
        # used to be compared against.
        "last_batch_ms": 0.0,
        "mean_batch_ms": 0.0,
        "last_encode_ms": 0.0,
    }
    if source is None:
        return block
    return source.stats(block)


def _keys(messages):
    """One cache key per message, `None` where the message cannot be keyed.

    ⚠️ **`(turn id, stream)` IS NOT UNIQUE and a cache keyed on it would serve the wrong vector.**
    One assistant turn can hold several `text` blocks, and prose plus thinking; `messages_in` emits
    one message per BLOCK, in file order. So the key carries the block's ORDINAL within its
    `(turn, stream)` pair — exact, stable across calls because a transcript is append-only, and a
    number rather than anything derived from the text.

    A message with no turn id is not cacheable, because nothing identifies it; it is re-encoded
    every call. Measured on the frozen corpus every user/assistant line carries a `uuid`, so that
    branch is defensive rather than routine.
    """
    seen, out = {}, []
    for m in messages:
        if not m.id:
            out.append(None)
            continue
        pair = (m.id, m.stream)
        n = seen.get(pair, 0)
        seen[pair] = n + 1
        out.append((m.id, m.stream, n))
    return out


def _aligned(messages, vectors):
    """`vectors` re-aligned to `messages`, `None` where a message produced none.

    `textembed.embed` returns one row per message that produced a vector, IN THE ORDER GIVEN, and
    drops the rest — an all-over-long message, or one whose every sentence exceeded the chunk cap.
    So the two lists are not index-parallel and a positional zip would shift every vector after the
    first drop onto the wrong message, which is a silent mis-attribution of one person's words to
    another's turn.

    A two-pointer walk rather than a dict: `(instant, stream, id)` is not unique either (see
    `_keys`), and order is the one thing `embed` does guarantee.
    """
    out, i = [], 0
    for m in messages:
        if i < len(vectors) and vectors[i].t == m.t and vectors[i].stream == m.stream \
                and vectors[i].id == m.id:
            out.append(vectors[i])
            i += 1
        else:
            out.append(None)
    return out
