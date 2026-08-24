"""Incremental ingest: parse only the bytes a transcript grew by, into the reference store.

Transcripts are append-only JSONL, so a byte offset is a valid resume point and the daemon's
watcher already knows when one grew (task 4 wires the signal). What is NOT automatic is that a
tail parse produces the same events a full parse would — and if it does not, the series is
silently, permanently wrong, because nothing downstream ever re-derives these rows.

## Why a tail parse is not obviously a full parse

Two things in this package are RETROACTIVE — later lines change what earlier lines mean:

1. **`workspace.scan_workspace` is a pre-pass over the whole file.** Its own docstring says why:
   "a repo-level marker may be touched in the last minute and still identifies the root of the
   first". A `CLAUDE.md` read at 17:00 re-resolves the 09:00 turns from "the cwd as given, no
   other signal" [low] to "repo-level marker" [high] — changing `workspace`,
   `workspace_evidence`, `remote`, `repo_mentioned`, and the `root_dir` every path in those turns
   is made relative to. Measured on the real local corpus: a 5-chunk parse of one 13.9 MB
   transcript differed from a single pass by 1,276 `workspace_evidence` rows, and of another by
   4,179 `repo_mentioned` rows.
2. **`reconcile.reconcile` resolves prose paths against every path a tool DECLARED.** A
   declaration in the tail can merge or reattribute a prose path from the head (its "split share"
   and "cross-repo" defects), and `file`/`dir`/`ext`/`lang`/`component` rows come only from it.

Both are handled EXACTLY here, by different means, because they have different costs:

**Reconcile is recomputed whole, every batch.** `pending` is persisted and the tail's entries are
appended to it, then `reconcile` runs over the accumulated list and the result REPLACES the
previous one (`Store.replace_events`, a slot rather than a batch — a retracted row must actually
disappear). This is equal to a full parse by construction, no detection needed, and it is
affordable because `pending` is tiny: measured across twelve real transcripts of 2-47 MB, 104-859
entries, and `reconcile` over the whole of one costs 0-3 ms.

**Workspace evidence is accumulated, and a change in the DERIVED ANSWER forces a reparse.** The
evidence triple itself accumulates exactly (`workspace.scan_tool_use` explains why), but
accumulation cannot make it retroactive. So the distinct cwds seen so far are remembered — 1 to 8
per transcript, measured — and after each batch the workspace resolution and remote selection are
recomputed for all of them under the new evidence. If any answer moved, the stored events for
those turns are stale and the transcript is reparsed from byte 0 (`IngestResult.reparsed`), which
is exact by definition. Detection is on the ANSWER, not on the raw evidence, so the common case —
a new `cd` target that resolves to the same workspace — costs nothing.

Reparsing rather than re-deriving is the deliberate trade. Re-deriving would mean persisting every
turn's cwd/branch and recomputing five levels from it, i.e. a second, partial copy of
`events_for_turns` — and a second implementation of extraction is exactly what
`app/analysis/__init__.py` exists to prevent. A reparse reuses the one implementation and is
self-evidently correct; it costs one full parse, and only when an answer actually moved.

## Measured

Over 284 real local transcripts (0.0-47.1 MB), each ingested in 40 successive chunks and compared
against a single whole-file ingest of the same bytes: **0 files differed and 0 event rows
differed.** Reparses fired on 337 of 7,855 chunks (4.3%), of which 284 are the unavoidable first
ingest of each file — so a retroactive answer-change reparse happens on **0.7%** of appends (53 of
7,571). Cost on the 47.1 MB transcript: 1.82s for the whole 41-chunk lifetime versus 0.76s for one
full parse, i.e. ~44 ms per ingest against the 0.8-1.0s per PROMPT the parse path spends today,
and ~4 prompts land per hour-long window (up to 20). Chunk count barely moves the total (13
chunks: 2.13s; 41 chunks: 2.61s on one 13.9 MB file), which is the O(tail) claim holding.

## What is stored

Nothing this module persists is message text. The events are the store's own contract (see
`store.py`). The carried parse state adds tool-call paths (`marker_dirs`, `cd_targets`,
`pending`), repository names (`remotes`) and the cwds — the same class of value already stored as
`dir`/`file`/`workspace`/`branch` refs, on-device, in a 0600 file. Two qualifications worth
keeping in view, neither introduced here: `term` is drawn from message text (store.py flags it as
the sensitive level), and `remotes` is recovered partly from prose, which is why `remote` and
`repo_mentioned` already are too.
"""
import collections
import hashlib
import os

from app.analysis.analyze import COMPONENT_DEPTH
from app.analysis.levels import events_for_turns
from app.analysis.reconcile import reconcile
# `_order_key` is transcript.py's ordering-only timestamp parser. Imported rather than
# re-derived: the watermark is a comparison between two turn timestamps, which is exactly
# what it is for, and a third timestamp parser in this package is a third thing to drift.
from app.analysis.transcript import _order_key, tool_use_in, turns_in
from app.analysis.workspace import new_evidence, resolve_workspace, scan_tool_use

# The `source_line` slot the reconcile-derived rows occupy. Line ordinals are 1-based, so 0 can
# never collide with a batch of turns. Reconcile output is not a batch at all — it is a single
# recomputed set that each ingest replaces wholesale — so it gets a slot, not a marker.
RECONCILE_SLOT = 0

# Bytes fingerprinted to detect rotation (a new file at the same path). A transcript's first
# lines carry the session id and the launch metadata, so the prefix is distinctive well inside
# this; hashing more would read a megabyte per ingest for nothing.
HEAD_BYTES = 4096


class IngestResult:
    """`new_lines` is the number of transcript lines PARSED — i.e. speech turns; `turns_in`
    discards everything else by a substring check before JSON decoding, so a tail of
    `tool_result` lines advances the byte offset and parses nothing, which is what it should
    report. `watermark_ts` is the timestamp through which the store may be trusted. `reparsed`
    says the whole file was re-read and the session's events replaced.
    """

    __slots__ = ("new_lines", "watermark_ts", "reparsed")

    def __init__(self, new_lines, watermark_ts, reparsed):
        self.new_lines = new_lines
        self.watermark_ts = watermark_ts
        self.reparsed = reparsed

    def __repr__(self):
        return (f"IngestResult(new_lines={self.new_lines}, "
                f"watermark_ts={self.watermark_ts!r}, reparsed={self.reparsed})")

    def __eq__(self, other):
        return (isinstance(other, IngestResult)
                and (self.new_lines, self.watermark_ts, self.reparsed)
                == (other.new_lines, other.watermark_ts, other.reparsed))


def session_of(path):
    """The session id, the same way `levels.events_for_turns` and `analyze_window` derive it."""
    return os.path.basename(path)[:8]


def _scope(path):
    """`(root, projdir)`, derived exactly as `analyze.analyze_window` derives them, and for the
    reasons documented there: a transcript's path is `<root>/<projdir>/<session>.jsonl`, so the
    collection root is recovered by two `dirname`s rather than by a second convention. Like
    `analyze_window`, this layer passes `repo_root=()` — it has no configured filesystem
    repo-root list to confirm a candidate checkout against, and needs none to resolve a
    workspace from transcript evidence alone.
    """
    return os.path.dirname(os.path.dirname(path)), os.path.basename(os.path.dirname(path))


def _head_fingerprint(path, nbytes=HEAD_BYTES):
    """`"<n>:<sha256>"` over the first `n` bytes actually present.

    The byte count is part of the value because a growing file changes how much there is to
    hash. Comparison recomputes over the count the PREVIOUS fingerprint used, so an append —
    which never alters a prefix — always matches, while a rewrite at the same path never does.
    """
    with open(path, "rb") as fh:
        head = fh.read(nbytes)
    return f"{len(head)}:{hashlib.sha256(head).hexdigest()}"


def _head_matches(path, stored):
    if not stored or ":" not in stored:
        return False
    n, _, _rest = stored.partition(":")
    try:
        return _head_fingerprint(path, int(n)) == stored
    except ValueError:
        return False


def _read_complete_lines(path, offset, size):
    """The complete lines in `[offset, size)`, and the offset just past the last one.

    A trailing fragment with no newline is NOT consumed. The watcher can signal mid-write, and
    advancing the checkpoint over a half-written record would drop it permanently — the same
    rule `AGENTS.md` states for text generally, applied to the one delimiter JSONL has.
    """
    if size <= offset:
        return [], offset
    with open(path, "rb") as fh:
        fh.seek(offset)
        buf = fh.read(size - offset)
    cut = buf.rfind(b"\n")
    if cut < 0:
        return [], offset
    text = buf[:cut + 1].decode("utf-8", errors="replace")
    return text.splitlines(True), offset + cut + 1


def _remote_choice(remotes, repo):
    """The `remote` and `repo_mentioned` refs `events_for_turns` would emit for `repo`.

    Mirrors that function's two loops exactly (first remote whose basename IS the workspace;
    the top three that are not), so a change here is detected as a change there. It is a
    *derived answer*, which is what the reparse decision keys on — a remote merely gaining a
    count changes the Counter but usually not this.
    """
    if not repo:
        return None, ()
    own = None
    for rr, _n in remotes.most_common():
        if rr.rsplit("/", 1)[-1] == repo:
            own = rr
            break
    mentioned = tuple(rr for rr, _n in remotes.most_common(3)
                      if rr.rsplit("/", 1)[-1] != repo)
    return own, mentioned


def _answers(cwds, projdir, evidence):
    """Every evidence-dependent answer for the cwds seen so far. Compared before and after a
    batch's evidence is folded in; any difference means already-stored turns would now resolve
    differently, and only a reparse can fix that."""
    marker_dirs, cd_targets, remotes = evidence
    out = {}
    for cwd in cwds:
        resolved = resolve_workspace(cwd, projdir, marker_dirs, cd_targets, ())
        out[cwd] = (resolved, _remote_choice(remotes, resolved[1]))
    return out


def _load_state(store, path):
    """The carried parse state as live Python objects, or an empty one."""
    raw = store.parse_state(path) or {}
    evidence = (dict(raw.get("markers") or {}),
                set(raw.get("cd") or ()),
                collections.Counter())
    # Rebuilt in the recorded order so `Counter.most_common` breaks ties the same way a
    # single pass would -- the tie-break is insertion order, so the order is part of the state.
    for name, n in raw.get("remotes") or ():
        evidence[2][name] += n
    pending = [(tuple(b), rel, bool(fi), rt) for b, rel, fi, rt in raw.get("pending") or ()]
    return evidence, pending, list(raw.get("cwds") or ()), int(raw.get("lines") or 0)


def _dump_state(evidence, pending, cwds, lines):
    marker_dirs, cd_targets, remotes = evidence
    return {"markers": marker_dirs,
            "cd": sorted(cd_targets),
            "remotes": [[k, v] for k, v in remotes.most_common()],
            "pending": [[list(b), rel, fi, rt] for b, rel, fi, rt in pending],
            "cwds": cwds,
            "lines": lines}


def _latest(a, b):
    """The later of two ISO timestamps, either of which may be None."""
    if a is None:
        return b
    if b is None:
        return a
    return a if _order_key(a) >= _order_key(b) else b


def ingest_file(store, path, nlp=None):
    """Ingest whatever `path` has grown by (or the whole file) into `store`.

    Raises `FileNotFoundError` if the transcript is gone — a caller that signalled about a file
    that has since been deleted needs to know that, and it is not the same as "nothing new".

    `nlp` is passed through to the `term` level exactly as `analyze_window` passes it. It must be
    the SAME pipeline for the life of a store: `term` rows ingested with a loaded spaCy model and
    rows ingested without one are not comparable, and unlike every other level they are never
    re-derived.
    """
    size = os.path.getsize(path)                      # raises FileNotFoundError, deliberately
    state = store.ingest_state(path)
    reparse = (state is None
               or size < state["offset"]
               or not _head_matches(path, state["head_sha"])
               # A checkpoint with no carried parse state: a store written before `parse_state`
               # existed (SCHEMA_VERSION 1), or one whose state was pruned. Resuming from the
               # offset would tail-parse with EMPTY workspace evidence and an empty `pending` --
               # the exact inequality this module exists to prevent, and silent, because the
               # offset looks perfectly valid. Reparse instead; it happens at most once.
               or store.parse_state(path) is None)
    result = _ingest_from(store, path, size, 0 if reparse else state["offset"],
                          None if reparse else state["watermark_ts"], reparse, nlp)
    if result is not None:
        return result
    # A batch's own evidence re-resolved turns that are already stored. Only a full re-read can
    # correct them; it is exact by definition, and `_ingest_from` cannot recurse again because
    # a whole-file parse has no earlier state to invalidate.
    return _ingest_from(store, path, size, 0, None, True, nlp)


def _ingest_from(store, path, size, offset, watermark_ts, reparse, nlp):
    """One pass from `offset`. Returns None to mean "this must be redone as a reparse"."""
    session, (root, projdir) = session_of(path), _scope(path)
    lines, end_offset = _read_complete_lines(path, offset, size)

    evidence, pending, cwds, prev_lines = ((new_evidence(), [], [], 0) if reparse
                                           else _load_state(store, path))
    before = _answers(cwds, projdir, evidence)
    scan_tool_use(tool_use_in(lines), into=evidence)
    if not reparse and _answers(cwds, projdir, evidence) != before:
        return None

    turns = list(turns_in(lines))
    rows, new_pending, _n = events_for_turns(turns, path, root, (), nlp, evidence=evidence)
    pending += new_pending
    for o in turns:
        cwd = o.get("cwd") or ""
        if cwd not in cwds:
            cwds.append(cwd)
    for o in turns:
        watermark_ts = _latest(watermark_ts, o.get("timestamp"))

    # `source_line` is the absolute 1-based ordinal of the LAST transcript line this batch read
    # through -- a real position in the file, not a synthetic counter, so it stays meaningful
    # across restarts and is monotonic across batches. Carried in the parse state rather than
    # recounted, because recounting means reading the whole prefix and this path exists to be
    # O(tail); the reparse guard above makes the state's presence certain whenever `prev_lines`
    # is used. It is NOT per-ROW -- `events_for_turns` returns rows with no ordinal, and calling
    # it once per turn would break its cross-turn `requestId` dedup. See the task report.
    n_lines = prev_lines + len(lines)
    batch_line = n_lines if lines else 0

    with store.transaction():
        if reparse:
            store.clear_session(session)
        if rows:
            store.upsert_events(session, rows, source_line=batch_line)
        recon_rows, _stats = reconcile(pending, COMPONENT_DEPTH)
        store.replace_events(session, RECONCILE_SLOT, recon_rows)
        store.set_parse_state(path, _dump_state(evidence, pending, cwds, n_lines))
        store.record_ingest(path, end_offset, size, _head_fingerprint(path),
                            os.path.getmtime(path), watermark_ts)
    return IngestResult(len(turns), watermark_ts, reparse)
