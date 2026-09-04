"""`KELD_DEV_BLOCKS` -- an alternate cut for `POST /blocks`, for a LOCAL developer's own health
page. NEVER for what Atlas receives: `docs/v3/contracts.md` refuses `dev_blocks` while
`send_to_atlas` is on, and nothing here changes what the default ("") path publishes.

Three cuts beside the shipped one (`blocks.cut`, which this module never calls and which stays
BYTE-IDENTICAL when `KELD_DEV_BLOCKS` is unset -- see `main.py`'s `_blocks_blocking`):

  * `prompt` -- one block per HUMAN PROMPT. The boundary is a `promptId`, and it is read directly
    off the transcript rather than off the store's `prompt` INDEX, because that index cannot
    answer the question: `Store.upsert_prompts` writes BOTH the per-line `uuid` and, when present,
    the `promptId` into the SAME table with no column saying which is which (see the `prompt`
    table's own comment in `store.py` and the seam note in AGENTS.md /
    `test_prompt_id_seam.py`). A `promptId` is written only on `user`-type lines and is SHARED by
    every follow-on line of one human turn; an assistant line has none at all
    (`test_prompt_time_still_resolves_by_uuid`). So "the human prompts, in order" is recoverable
    only by reading the file -- a bounded scan, acceptable here because this mode is dev-gated
    (never the emitter's own timer, never reachable while Atlas is being sent to) and the cost
    lands on a developer's own request rather than on production.
  * `bin` -- one block per non-empty 5-minute bin, off the REAL store's own bins.
  * `minute` -- one block per non-empty 60-SECOND bin, off a SEPARATE store
    (`refseries-dev.db`, `Store(bin_seconds=60)`) ingested from the same transcript. A second
    store, not a second query: bin WIDTH is fixed in the `bin` table at the moment a row is
    written, so a 60-second answer needs 60-second rows, and writing them into the REAL store
    would give every other consumer of it -- `/analyze`'s dynamics, the shipped cutter's own
    `active_segments` -- two disagreeing populations of `bin` rows for one session. See
    `Store.bin_seconds` in `store.py`.

Every mode is digested through the SAME `blockdigest.digest` / `blockdigest.is_closed` the shipped
cutter uses, so the WIRE SHAPE (`workstreams`, `inventory`, `effort`, `tokens`, `requests`,
`dynamics`, `prior`, ...) is identical regardless of `KELD_DEV_BLOCKS` -- only the SPAN and the
boundary reasons differ. Reasons are deliberately NOT drawn from `blocks.REASONS`: that tuple is
"what the shipped cutter can emit", and a dev-mode block is cut by something else entirely, so
each mode names its own reason (`"prompt"`, `"bin"`, `"minute"`).
"""
import os
import time

from app.analysis import blockdigest
from app.analysis import blocks as blocks_mod
from app.analysis import transcript
from app.analysis.blocks import Block
from app.analysis.ingest import session_of
from app.analysis.levels import _epoch, quantize
from app.analysis.store import Store, default_path
from app.analysis.transcript import _order_key

# The closed set `KELD_DEV_BLOCKS` may name. `""` is the shipped cutter and is never handled by
# this module -- `main.py` keeps that branch on the untouched original code path.
MODES = ("", "prompt", "bin", "minute")


def mode_from_env(env=None):
    """`KELD_DEV_BLOCKS`, validated against `MODES`. Read PER REQUEST, not latched, matching
    every other `KELD_*` toggle this package re-reads live (`capture_mode`, `textembed.enabled`).

    An unrecognised value reads as the DEFAULT rather than raising: this is consulted on the
    `/blocks` request path, and a typo in an environment variable must not take the whole route
    down with it -- the same tolerance `store._env_float` extends a malformed retention setting.
    """
    env = os.environ if env is None else env
    v = (env.get("KELD_DEV_BLOCKS") or "").strip()
    return v if v in MODES else ""


def dev_store_path():
    """`refseries-dev.db`, beside `refseries.db` under the same `KELD_HOME`. Its own file: mixing
    a 60-second ingest of one session into the real store would corrupt every other consumer's
    `bin` rows for that session (see the module docstring)."""
    return os.path.join(os.path.dirname(default_path()), "refseries-dev.db")


def open_dev_store(path=None):
    """The 60-second-binned store `minute` mode reads (and ingests) from. Opened lazily by the
    caller, exactly like the real store in `main.py` -- never at import."""
    return Store(path or dev_store_path(), bin_seconds=60)


def _prompt_boundaries(path):
    """`[(ts, prompt_id), ...]` ascending: the FIRST instant of every distinct human prompt in
    this transcript.

    `user`-type lines only -- an assistant line carries no `promptId` at all
    (`test_prompt_id_seam.py`) -- and deduplicated to first occurrence, the same rule
    `Store.upsert_prompts`' `ON CONFLICT DO NOTHING` enforces for the mixed index: a `promptId`
    shared by several continuation lines resolves to the HUMAN PROMPT's own instant, never a
    continuation's.
    """
    seen = set()
    out = []
    for o in transcript.iter_turns(path):
        if o.get("type") != "user":
            continue
        pid = o.get("promptId")
        ts = o.get("timestamp")
        if not pid or not ts or pid in seen:
            continue
        seen.add(pid)
        out.append((quantize(_epoch(ts)), pid))
    out.sort()
    return out


def prompt_cut(store, session, path):
    """One `Block` per human prompt in `path`, `"prompt"`/`"prompt"` for both boundary reasons.

    The last prompt's block runs to the session's own active end (`blockdigest.span_of`'s `hi`),
    exactly as the shipped cutter's trailing block does. `[]` when the transcript has no active
    bin at all, or no human prompt in it (an all-agentic subagent transcript, say) -- an honest
    empty answer, not an error.
    """
    span = blockdigest.span_of(store, session)
    if span is None:
        return []
    _lo, hi = span
    bounds = _prompt_boundaries(path)
    if not bounds:
        return []
    out = []
    for i, (start, _pid) in enumerate(bounds):
        end = bounds[i + 1][0] if i + 1 < len(bounds) else hi
        if end <= start:
            continue
        out.append(Block(start, end, "prompt", "prompt"))
    return out


def bin_cut(store, session, reason="bin"):
    """One `Block` per non-empty bin of `store`'s OWN width (`store.bin_seconds`) --
    `"bin"` mode on the real (300s) store, `"minute"` mode on the 60s dev store. Not
    `blocks.cut`: there is no cap and no idle terminator here, only "was there a bin".

    `reason` names the boundary reason both fields share -- `"bin"` or `"minute"` -- so the two
    modes, which otherwise run the identical function over two different stores, still publish
    the mode they were asked for rather than always reading `"bin"`.
    """
    width = store.bin_seconds
    return [Block(float(b), float(b + width), reason, reason)
           for b in blocks_mod.active_bins(store, session)]


def digest_dev_blocks(store, path, mode, since_ts=None, now=None,
                      max_blocks=blockdigest.DEFAULT_MAX_BLOCKS, current=True, floor=None,
                      sizer=None, prior=True):
    """`digest_blocks`'s shape (`{"blocks": [...], "watermark": ...}`), cut by `mode` -- one of
    `MODES` other than `""` -- instead of `blocks.cut`. `session` is derived from `path` exactly
    as `digest_blocks` derives it, so a caller cannot pass a mismatched pair.

    Closedness is asked with the SAME `blockdigest.is_closed` the shipped cutter uses: that
    function only reads `cut[i].end`, the list's position and `current`, so it applies unchanged
    to a `prompt`/`bin`/`minute` cut list -- "is there a later block, or has enough silence
    settled the trailing one" means the same thing whatever cut the list.
    """
    if mode not in ("prompt", "bin", "minute"):
        raise ValueError(f"not a dev block mode: {mode!r}")
    now = time.time() if now is None else float(now)
    session = session_of(path)
    wm_iso = store.watermark(path)
    watermark = None if wm_iso is None else _order_key(wm_iso).timestamp()
    out = {"blocks": [], "watermark": watermark}
    if watermark is None:
        return out

    cut = (prompt_cut(store, session, path) if mode == "prompt"
          else bin_cut(store, session, reason=mode))
    if not cut:
        return out

    floor = store.serving_floor() if floor is None else floor
    for i, b in enumerate(cut):
        if len(out["blocks"]) >= max_blocks:
            break
        if since_ts is not None and float(b.start) < float(since_ts):
            continue
        if not blockdigest.is_closed(cut, i, watermark, now, current=current):
            continue
        if floor is not None and quantize(float(b.start)) < float(floor):
            continue
        out["blocks"].append(
            blockdigest.digest(store, session, b, path, floor=floor, sizer=sizer, prior=prior))
    return out
