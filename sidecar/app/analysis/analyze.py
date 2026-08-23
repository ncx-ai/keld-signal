"""One window of a transcript, analysed into the workstream payload.

Takes COORDINATES, never text — the same rule as `spool.Pointer`. The window ENDS at the prompt
and looks back, because the question a cost report asks is "what was this hour of work about",
and the work that produced a prompt precedes it. That is also why a caller needs nothing but a
transcript path and a prompt id: no timestamp bookkeeping of its own.
"""
import os
from datetime import datetime, timedelta

from app.analysis import SCHEMA
from app.analysis import window, workstreams
from app.analysis.levels import events_for_turns
from app.analysis.reconcile import reconcile
from app.analysis.transcript import iter_turns, turns_between

# Matches refseries.py's own `--component-depth` default. `reconcile.reconcile` needs it to
# truncate the "component" level (e.g. depth 3 -> "internal/agent/daemon", not a full file path);
# analyze_window has no caller-supplied value to plumb through, so it inherits the study's default
# rather than inventing a second one.
COMPONENT_DEPTH = 3


class PromptNotFound(Exception):
    """The prompt id is not in this transcript. Distinct from an empty window: one is a
    resolution failure, the other is a fact about the work."""


def _prompt_time(path, prompt_id):
    """The target prompt's own timestamp, by a first pass over the transcript.

    `iter_turns` already skips everything that cannot be a user/assistant speech turn (see its
    docstring), so this is one cheap linear scan, not a second full parse.
    """
    for o in iter_turns(path):
        if o.get("uuid") == prompt_id:
            return o["timestamp"]
    raise PromptNotFound(prompt_id)


def analyze_window(path, prompt_id, span_minutes=60, nlp=None):
    """The `span_minutes` of work ending at `prompt_id` -> the workstream + inventory payload.

    Raises `PromptNotFound` if `prompt_id` is not in this transcript, rather than returning an
    empty window — resolution failure and "nothing was happening" must stay distinguishable to a
    caller (and, downstream, to whoever reads the published enrichment).
    """
    end_iso = _prompt_time(path, prompt_id)
    end = datetime.fromisoformat(end_iso.replace("Z", "+00:00"))
    start = end - timedelta(minutes=span_minutes)
    turns = turns_between(path, start.isoformat(), end.isoformat())

    # `root` is reconcile.py's machine-scope key: in production a transcript's path is
    # `<root>/<projdir>/<session>.jsonl` for one of `--roots` (refseries.py), so the collection
    # root is recovered the same way here rather than inventing a second meaning for it.
    # `repo_root=()` is resolve_workspace's own no-fixture default (levels.py's comment on the
    # `repo_root or ()` line): this layer has no configured filesystem repo-root list to confirm
    # a candidate checkout against, and doesn't need one to resolve a workspace from transcript
    # evidence alone.
    root = os.path.dirname(os.path.dirname(path))
    rows, pending, _n_lines = events_for_turns(turns, path, root, (), nlp)
    # `pending` is reconciled prose paths, not optional decoration: `file`/`dir`/`ext`/`lang`/
    # `component` rows are ONLY ever produced by reconcile() (see its module docstring), so
    # skipping this step would leave the "language" workstream permanently unattributed.
    recon_rows, _stats = reconcile(pending, COMPONENT_DEPTH)
    rl = window.rollup(rows + recon_rows)

    out = workstreams.payload(rl)
    out.update(
        schema=SCHEMA,
        session=os.path.basename(path)[:8],
        window_start=start.isoformat(),
        window_end=end.isoformat(),
        evidence=int(sum(n for items in rl.values() for _, n in items)),
    )
    return out
