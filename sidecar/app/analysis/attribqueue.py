"""The attribution work queue: which blocks are waiting to be encoded, which one is being
encoded now, and which finished answers are waiting to be collected.

⚠️ **THIS EXISTS BECAUSE THE CALLER'S DEADLINE COULD NOT CANCEL THE WORK IT WAS TIMING.**
Until 2026-09-03 `/attribute` encoded the block INSIDE the request. The daemon bounds that call
at `attributeCallTimeout` (2 minutes) and a real block measured **65-110 s** on the two threads
the daemon pinned the encoder to — so the median block squeaked through and a large one did not.
When it did not, three things happened and only the first was intended: the daemon counted an
attempt and re-queued the job; the sidecar **kept encoding to the end** and threw the answer
away, because nothing on this side had ever heard of the deadline; and the next request queued
behind that abandoned encode on the encoder's own lock, so it started late and missed ITS
deadline too. Measured on the machine that wrote this: 79 jobs in the daemon's spool, an encoder
child at 194% CPU for 1 h 48 m, and **two** blocks attributed in fifteen minutes.

That is not a timeout that is too short. It is a timeout on the wrong side of the boundary: the
process holding the deadline could not free the resource, so firing it destroyed a nearly-finished
answer and bought nothing. The repair is to stop timing the work from outside — the request hands
the block over and returns, this queue owns it, and the daemon's `pending` (which by contract
consumes no attempt, see `internal/agent/attrib`) is how it asks again.

⚠️ **LIVENESS IS JUDGED BY PROGRESS, NEVER BY ELAPSED TIME, and that distinction is the whole
reason a watchdog is safe to have here at all.** A wall-clock budget per block cannot tell a slow
block from a hung one — it only knows how long it has been — so it kills healthy work on a loaded
laptop, and the killed job comes straight back and is killed again: exactly the loop this module
replaced, rebuilt one layer down. The encoder feeds the child in batches and gets a reply per
batch (`textembed.Encoder.encode`'s `on_batch`), so `beat()` is real evidence of forward
progress. A block that answers every batch is working, however long it takes in total; only
SILENCE is death. `stalled()` is therefore defined on the heartbeat and never on the start time.

Three more rules, each of which exists to stop a bad block from becoming a bad queue:

  * a killed job goes to the **BACK** (`fail`), so one poisonous block cannot starve everything
    behind it — the failure mode a front-of-queue retry produces is a queue that never advances;
  * attempts are bounded (`MAX_ATTEMPTS`), so a block that genuinely hangs the encoder every time
    is retired rather than replayed forever — the same call `spool.Quarantine` makes Go-side;
  * a result is handed over **once** (`collect` deletes), so a collected block is gone from this
    process entirely and cannot be re-encoded by a re-POST.

No HTTP, no encoder, no file, no environment beyond two tunables read once — the policy is a
plain object over an injected clock, which is what lets every ordering and watchdog rule above be
tested in milliseconds against a fake clock instead of against a 1.2 GB model.
"""
from __future__ import annotations

import os
import threading
from collections import OrderedDict, deque

#: Attempts before a block is retired. Bounds GENUINE FAILURES ONLY — a heartbeat kill or an
#: encoder error. A job that is merely waiting has consumed nothing, which is the same split
#: `internal/agent/attrib` makes between an error and a `pending` answer.
MAX_ATTEMPTS = int(os.environ.get("KELD_ATTRIBUTION_MAX_ATTEMPTS", "4"))

#: How long the running job may go WITHOUT A BATCH before its child is presumed dead.
#: Sized against the batch, not the block: a batch is 8 chunks at ~1.1-1.6 s each, so ~13 s is
#: the honest upper bound for a healthy one and 60 s is ~4x that. It must never be sized against
#: a BLOCK's duration — that is the wall-clock mistake this module's docstring is about.
HEARTBEAT_TIMEOUT_S = float(os.environ.get("KELD_ATTRIBUTION_HEARTBEAT_S", "60"))

#: Finished answers held for collection. The daemon collects on its next sweep (~45 s), so this
#: is only ever a handful — but a daemon that stopped sweeping must not grow this without bound,
#: so the oldest are evicted. Eviction is safe by construction: an uncollected block is still in
#: the daemon's durable spool and is simply re-POSTed and re-encoded.
MAX_RESULTS = int(os.environ.get("KELD_ATTRIBUTION_MAX_RESULTS", "256"))

QUEUED = "queued"
RUNNING = "running"
DONE = "done"
QUARANTINED = "quarantined"
ABSENT = "absent"


class Job:
    """One block waiting to be attributed. Coordinates and counters only — never text."""

    __slots__ = ("key", "path", "start", "end", "dims", "attempts")

    def __init__(self, key, path, start, end, dims=None):
        # `key` is the block's identity, `session@start` — the session is not held separately
        # because nothing here needs it apart from the key it is already inside.
        self.key = key
        self.path = path
        self.start = start
        self.end = end
        self.dims = dims or {}
        self.attempts = 0


class Queue:
    """FIFO of blocks to encode, one running at a time, plus the finished answers.

    Every method is safe to call from any thread. The watchdog in particular calls `stalled()`
    from the sidecar's 1 s poll loop while the worker thread is inside an encode — this lock is
    held only for the bookkeeping, never across the encode itself, so that is never a wait.
    """

    def __init__(self, *, clock=None, heartbeat_timeout_s=None, max_attempts=None,
                 max_results=None):
        import time
        self._clock = clock or time.monotonic
        self._hb = HEARTBEAT_TIMEOUT_S if heartbeat_timeout_s is None else heartbeat_timeout_s
        self._max_attempts = MAX_ATTEMPTS if max_attempts is None else max_attempts
        self._max_results = MAX_RESULTS if max_results is None else max_results
        self._lock = threading.Lock()
        self._waiting = deque()               # Job, in arrival order
        self._index = {}                      # key -> Job, for the O(1) dedupe
        self._running = None                  # Job
        self._beat_at = None                  # monotonic instant of the running job's last batch
        self._results = OrderedDict()          # key -> answer dict, oldest first
        self._quarantined = set()
        self.counts = {"submitted": 0, "completed": 0, "failed": 0, "quarantined": 0,
                       "collected": 0, "evicted": 0, "heartbeat_kills": 0}

    # -- submission ---------------------------------------------------------------------------
    def state(self, key):
        """What this process already knows about `key`, without changing anything.

        The route calls this BEFORE reading the transcript, so re-asking about a block already in
        flight costs no file I/O — which is what makes a 45 s sweep over 24 jobs free."""
        with self._lock:
            return self._state_locked(key)

    def _state_locked(self, key):
        if key in self._results:
            return DONE
        if self._running is not None and self._running.key == key:
            return RUNNING
        if key in self._index:
            return QUEUED
        if key in self._quarantined:
            return QUARANTINED
        return ABSENT

    def submit(self, job):
        """Enqueue `job` unless this process already has it. Returns the resulting state.

        ⚠️ The dedupe is the point, not an optimisation: the daemon re-POSTs a `pending` block on
        every sweep, so without it a block would be enqueued once per 45 s and the queue would
        grow faster than it drains — the pile-up this module replaced, rebuilt in a new place."""
        with self._lock:
            st = self._state_locked(job.key)
            if st != ABSENT:
                return st
            self._waiting.append(job)
            self._index[job.key] = job
            self.counts["submitted"] += 1
            return QUEUED

    # -- the worker's end ---------------------------------------------------------------------
    def take(self):
        """The next job to encode, or None. Marks it running and starts its heartbeat.

        Refuses while a job is already running: the encoder is one child with one lock, so a
        second concurrent job could only ever wait on the first while looking like progress."""
        with self._lock:
            if self._running is not None or not self._waiting:
                return None
            job = self._waiting.popleft()
            del self._index[job.key]
            self._running = job
            self._beat_at = self._clock()
            return job

    def beat(self, key=None):
        """Report forward progress on the running job — called once per encoded batch.

        `key` is accepted and checked so a beat from a stale worker (one whose job was already
        killed and re-queued) cannot revive the watchdog's clock for whatever is running now."""
        with self._lock:
            if self._running is None:
                return False
            if key is not None and self._running.key != key:
                return False
            self._beat_at = self._clock()
            return True

    def finish(self, key, result):
        """Store `key`'s answer for collection and free the encoder for the next job."""
        with self._lock:
            if self._running is None or self._running.key != key:
                # A late answer from a job the watchdog already killed and re-queued. Dropped
                # rather than stored: the re-queued copy is the live one, and storing this would
                # hand the daemon an answer for a block that is about to be encoded again.
                return False
            self._running = None
            self._beat_at = None
            self._results[key] = result
            self._results.move_to_end(key)
            while len(self._results) > self._max_results:
                self._results.popitem(last=False)
                self.counts["evicted"] += 1
            self.counts["completed"] += 1
            return True

    def fail(self, key, reason="error"):
        """The running job did not produce an answer. Back of the queue, one attempt spent.

        Returns `queued` or `quarantined`. ⚠️ **Back, not front.** A front-of-queue retry is how a
        single bad block stops every other block from ever being attributed — the queue would
        spend its whole life on the one job that cannot finish, which is indistinguishable from
        being stuck."""
        with self._lock:
            job = self._running
            if job is None or job.key != key:
                return ABSENT
            self._running = None
            self._beat_at = None
            job.attempts += 1
            self.counts["failed"] += 1
            if reason == "heartbeat":
                self.counts["heartbeat_kills"] += 1
            if job.attempts >= self._max_attempts:
                self._quarantined.add(job.key)
                self.counts["quarantined"] += 1
                return QUARANTINED
            self._waiting.append(job)
            self._index[job.key] = job
            return QUEUED

    # -- the watchdog's end -------------------------------------------------------------------
    def stalled(self):
        """The running job's key if it has gone silent past the window, else None.

        ⚠️ Silence, not duration. A job beating every batch is never named here however long it
        has run; a job that has stopped answering is named after one window. That is the entire
        difference between killing a hung child and killing a slow one."""
        with self._lock:
            if self._running is None or self._beat_at is None:
                return None
            if (self._clock() - self._beat_at) >= self._hb:
                return self._running.key
            return None

    # -- the route's end ----------------------------------------------------------------------
    def collect(self, key):
        """The stored answer for `key`, removed. None if there is none.

        ⚠️ Removed, so the handover happens exactly once. The daemon deletes its durable job on a
        terminal answer, so a second copy here could only ever be re-delivered to a block that no
        longer exists — and holding it would make an evicted result and a collected one
        indistinguishable."""
        with self._lock:
            result = self._results.pop(key, None)
            if result is not None:
                self.counts["collected"] += 1
            return result

    def stats(self):
        """The `/metrics` view. Counters and depths only — no keys, no paths, no text."""
        with self._lock:
            return {
                "waiting": len(self._waiting),
                "running": self._running is not None,
                "running_attempts": self._running.attempts if self._running else 0,
                "results_held": len(self._results),
                "quarantined": len(self._quarantined),
                "heartbeat_timeout_s": self._hb,
                "max_attempts": self._max_attempts,
                "counts": dict(self.counts),
            }
