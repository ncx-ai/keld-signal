"""A tick characterises the work no prompt will ever characterise — end to end, on a store.

  cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_analysis_tick.py

`app/test_analysis_coverage.py` proves the PLANNER: which slices are uncovered, and that none of
them can ever be covered by a later prompt. This file proves the SERVICE built on it — that a
planned slice is answered from the reference series by the same `analyze_window` a prompt goes
through, and that the three ways a window can fail to be answerable are each handled the way the
design spec requires rather than papered over:

  no evidence   -> DROPPED. "Idle ticks emit nothing": a quiet machine must not publish empty
                   characterisations forever, and a window whose evidence is 0 is exactly that.
  behind        -> the cursor STOPS at that window and the tick retries. This is the watermark
                   rule, and it is honoured through `analyze_window`'s own `StoreBehind` — the
                   tick must not reach past it into the store, or the guard is only as good as
                   the second copy of it.
  expired       -> DROPPED and COUNTED, and the cursor still advances. Retention is permanent
                   (`WindowExpired`), so stopping the cursor on it would wedge a daemon that had
                   been down longer than the retention horizon: it would retry the same
                   unanswerable window forever and never reach the answerable ones behind it.

The prompt times come from the CALLER, not from the store's `prompt` index, and that is
load-bearing. The index holds every user- and assistant-shaped turn (`ingest.py` indexes
everything `turns_in` yields, deliberately, so an assistant uuid still resolves) — on john's
transcript that is ~260 rows for 14 human prompts. Planning against it would compute a covered
set that swallows the whole session and the tick would never emit anything at all.
`test_the_store_prompt_index_would_hide_every_gap` pins that, because it is the single most
plausible "simplification" a later reader could make here.
"""
import json
import os
import sys
import tempfile
from datetime import datetime, timedelta, timezone

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from app.analysis.analyze import StoreBehind, analyze_window
from app.analysis.dynamics import FixedSizer
from app.analysis.ingest import ingest_file, session_of
from app.analysis.store import open_store
from app.analysis.tick import tick

BASE = datetime(2026, 8, 19, 12, 0, 0, tzinfo=timezone.utc)
PROJDIR = "-workspace-fixture-tick-aurora"
CWD = "/workspace/fixture-tick/aurora"
FILENAME = "7c1d0e42-8b02-4c31-a7de-5f1290abcdef.jsonl"
SPAN = 60.0
HOUR = 3600.0
# 37 seconds off the minute, deliberately: the gap this leaves is 54.6 minutes, so a span
# rounded to whole minutes reaches back INTO the region P2's own look-back already covers.
P2_OFF = 114 * 60 + 37


def _iso(off):
    return (BASE + timedelta(seconds=off)).isoformat().replace("+00:00", "Z")


def _at(off):
    return BASE.timestamp() + off


def _epoch(iso):
    """The payload renders bounds with `isoformat()` (+00:00), the fixture writes them with Z.
    Compare instants, never their spelling."""
    return datetime.fromisoformat(iso.replace("Z", "+00:00")).timestamp()


def _assistant(off, uid, branch="main"):
    return {"type": "assistant", "uuid": uid, "timestamp": _iso(off), "cwd": CWD,
            "gitBranch": branch, "requestId": "req-" + uid,
            "message": {"role": "assistant", "model": "acme-llm-7b-preview",
                        "content": [{"type": "text", "text": "working"},
                                    {"type": "tool_use", "id": f"toolu_{uid}", "name": "Read",
                                     "input": {"file_path": CWD + "/services/api/queue.go"}}],
                        "usage": {"input_tokens": 100, "output_tokens": 20,
                                  "cache_creation_input_tokens": 0,
                                  "cache_read_input_tokens": 0}}}


def _user(off, uid):
    return {"type": "user", "uuid": uid, "timestamp": _iso(off), "cwd": CWD,
            "gitBranch": "main", "message": {"role": "user", "content": "next step"}}


def _deck_shaped():
    """john's shape, in miniature: a prompt, a burst of autonomous work right after it, then
    nearly two hours of silence, then the next prompt. The burst is in NO prompt's look-back —
    the first prompt precedes it and the second's hour reaches only back to 01:54."""
    turns = [_user(0, "P1")]
    turns += [_assistant(30 + i * 20, f"w{i:03d}") for i in range(60)]     # 00:00:30 - 00:20:10
    turns.append(_user(P2_OFF, "P2"))                                      # 01:54:37
    turns.append(_assistant(P2_OFF + 30, "w900"))
    return turns


def _write(tmp, turns):
    d = os.path.join(tmp, "projects", PROJDIR)
    os.makedirs(d, exist_ok=True)
    path = os.path.join(d, FILENAME)
    with open(path, "w") as fh:
        for o in turns:
            fh.write(json.dumps(o, separators=(",", ":")) + "\n")
    return path


def _ingested(tmp, turns):
    path = _write(tmp, turns)
    st = open_store(os.path.join(tmp, "state", "refseries.db"))
    ingest_file(st, path, None)
    return path, st


def _tick(path, st, *, cursor, prompts, now, **kw):
    kw.setdefault("sizer", FixedSizer())
    return tick(st, path, cursor_ts=cursor, prompt_ts=prompts, now=now,
                span_minutes=SPAN, nlp=None, **kw)


# ------------------------------------------------------------------ the hole is characterised

def test_the_uncoverable_burst_is_characterised_by_a_tick():
    """THE POINT OF THE WHOLE TASK. No prompt's look-back reaches this work, so before the tick
    it is characterised by nothing at all."""
    with tempfile.TemporaryDirectory() as tmp:
        path, st = _ingested(tmp, _deck_shaped())
        out = _tick(path, st, cursor=_at(-HOUR), prompts=[_at(0), _at(P2_OFF)],
                    now=_at(116 * 60))
        assert len(out["windows"]) == 1, out
        w = out["windows"][0]
        assert _epoch(w["window_start"]) == _at(0), w["window_start"]
        assert _epoch(w["window_end"]) == _at(P2_OFF - 3600), w["window_end"]
        assert w["evidence"] > 0, w["evidence"]
        st.close()


def test_the_tick_window_carries_the_same_blocks_a_prompt_window_does():
    """A tick row is not a lesser row: same digest, same effort block, same inventory, from the
    same `analyze_window`. If a tick answered from a different code path this would drift."""
    with tempfile.TemporaryDirectory() as tmp:
        path, st = _ingested(tmp, _deck_shaped())
        out = _tick(path, st, cursor=_at(-HOUR), prompts=[_at(0), _at(P2_OFF)],
                    now=_at(116 * 60))
        w = out["windows"][0]
        for key in ("schema", "session", "workstreams", "inventory", "effort", "evidence"):
            assert key in w, (key, sorted(w))
        st.close()


def test_a_tick_window_never_overlaps_the_prompt_window_beside_it():
    with tempfile.TemporaryDirectory() as tmp:
        path, st = _ingested(tmp, _deck_shaped())
        out = _tick(path, st, cursor=_at(-HOUR), prompts=[_at(0), _at(P2_OFF)],
                    now=_at(116 * 60))
        assert _epoch(out["windows"][0]["window_end"]) <= _at(P2_OFF) - HOUR, out
        st.close()


# ------------------------------------------------------------------ idle emits nothing

def test_a_window_with_no_evidence_is_dropped_and_counted():
    """Rule 2. The silent two hours before the transcript starts plan cleanly and answer with
    an evidence of 0; publishing them would be a stream of empty characterisations."""
    with tempfile.TemporaryDirectory() as tmp:
        path, st = _ingested(tmp, _deck_shaped())
        out = _tick(path, st, cursor=_at(-3 * HOUR), prompts=[_at(0), _at(P2_OFF)],
                    now=_at(116 * 60), max_windows=8)
        assert out["empty"] >= 2, out
        assert all(w["evidence"] > 0 for w in out["windows"]), out
        st.close()


def test_a_session_with_nothing_new_plans_and_emits_nothing():
    with tempfile.TemporaryDirectory() as tmp:
        path, st = _ingested(tmp, _deck_shaped())
        first = _tick(path, st, cursor=_at(-HOUR), prompts=[_at(0), _at(P2_OFF)],
                      now=_at(300 * 60), max_windows=64)
        again = _tick(path, st, cursor=first["cursor"], prompts=[_at(0), _at(P2_OFF)],
                      now=_at(300 * 60), max_windows=64)
        assert again["windows"] == [] and again["planned"] == 0, again
        assert again["cursor"] == first["cursor"], (again["cursor"], first["cursor"])
        st.close()


def test_one_tick_never_exceeds_its_batch_bound_and_the_next_one_resumes():
    """A daemon down for a week has a week of gaps. Unbounded, one tick puts all of them on the
    wire at once; bounded, the cursor stops at the last window of the batch and nothing is
    skipped. Both halves asserted — a cap that dropped the remainder instead of deferring it
    would pass the first assertion alone."""
    with tempfile.TemporaryDirectory() as tmp:
        path, st = _ingested(tmp, _deck_shaped())
        first = _tick(path, st, cursor=_at(-20 * HOUR), prompts=[_at(0), _at(P2_OFF)],
                      now=_at(116 * 60), max_windows=3)
        assert first["planned"] == 3, first["planned"]
        assert len(first["windows"]) + first["empty"] + first["expired"] == 3, first
        second = _tick(path, st, cursor=first["cursor"], prompts=[_at(0), _at(P2_OFF)],
                       now=_at(116 * 60), max_windows=3)
        assert second["cursor"] > first["cursor"], (first["cursor"], second["cursor"])
        assert second["planned"] == 3, second["planned"]
        st.close()


# ------------------------------------------------------------------ the two refusals

def test_a_window_past_the_watermark_stops_the_cursor_rather_than_being_served():
    """The watermark rule, and it must fire through `analyze_window`'s own StoreBehind. The
    store is deliberately left behind the file: half the transcript is ingested, the rest is
    appended after."""
    with tempfile.TemporaryDirectory() as tmp:
        turns = _deck_shaped()
        path = _write(tmp, turns[:30])
        st = open_store(os.path.join(tmp, "state", "refseries.db"))
        ingest_file(st, path, None)
        _write(tmp, turns)                              # the file grows; the store does not
        behind = _tick(path, st, cursor=_at(-HOUR), prompts=[_at(0), _at(P2_OFF)],
                       now=_at(116 * 60))
        assert behind["behind"] is True, behind
        assert behind["windows"] == [], behind
        # The cursor may cross ground a prompt already covers, but never the window it refused:
        # the work must still be there to characterise once the store catches up.
        assert behind["cursor"] <= _at(0), behind["cursor"]
        ingest_file(st, path, None)                     # the store catches up
        after = _tick(path, st, cursor=behind["cursor"], prompts=[_at(0), _at(P2_OFF)],
                      now=_at(116 * 60))
        assert after["behind"] is False, after
        assert len(after["windows"]) == 1, after
        assert _epoch(after["windows"][0]["window_start"]) == _at(0), after["windows"][0]
        st.close()


def test_an_expired_window_is_dropped_and_the_cursor_still_advances():
    """Retention is permanent, so a tick that stopped on it would retry the same unanswerable
    window forever and never reach the answerable ones behind it."""
    with tempfile.TemporaryDirectory() as tmp:
        path, st = _ingested(tmp, _deck_shaped())
        st.note_pruned("event", _at(60 * 60), rows=1)   # everything before 01:00 is gone
        out = _tick(path, st, cursor=_at(-HOUR), prompts=[_at(0), _at(P2_OFF)],
                    now=_at(116 * 60), max_windows=8)
        assert out["expired"] >= 1, out
        assert out["behind"] is False, out
        # PAST the window it refused, not merely past where it started. A cursor parked on a
        # permanently-unanswerable window retries it forever and never reaches what is behind it.
        assert out["cursor"] > _at(0), out["cursor"]
        st.close()


def test_an_anchored_window_is_refused_when_the_watermark_is_short_of_it():
    """The NARROWER of the two staleness guards: every byte is accounted for, so `is_current`
    is satisfied, but the checkpoint's watermark stops short of the window's end. That is the
    guard `analyze_window` applies to a prompt window, and an anchored window must not be a way
    around it. Asserted on `analyze_window` directly, because the tick clamps its own frontier to
    the watermark first and so never reaches the guard — belt and braces, and this is the brace:
    the day a caller anchors a window some other way, the refusal is still there."""
    with tempfile.TemporaryDirectory() as tmp:
        path, st = _ingested(tmp, _deck_shaped())
        was = st.ingest_state(path)
        st.record_ingest(path, was["offset"], was["size"], was["head_sha"], was["mtime"],
                         watermark_ts=_iso(5 * 60))          # fully read, watermark at 00:05
        try:
            analyze_window(path, None, 10.0, None, store=st, refresh=False,
                           end_ts=_iso(20 * 60))             # ends 15 minutes past it
        except StoreBehind:
            pass
        else:
            raise AssertionError("an anchored window past the watermark was served")
        st.close()


# ------------------------------------------------------------------ the prompt-source trap

def test_the_store_prompt_index_would_hide_every_gap():
    """Planning against the store's own `prompt` index instead of the caller's human prompts
    computes a covered set that swallows the session. Asserted as a CONTRAST so the tick's
    result is visibly not an accident of the fixture."""
    with tempfile.TemporaryDirectory() as tmp:
        path, st = _ingested(tmp, _deck_shaped())
        session = session_of(path)
        indexed = [datetime.fromisoformat(ts.replace("Z", "+00:00")).timestamp()
                   for (ts,) in st._conn().execute(
                       "SELECT ts FROM prompt WHERE session = ?", (session,))]
        assert len(indexed) > 50, len(indexed)          # every turn, not the two human prompts
        burst = _at(10 * 60)                            # inside the autonomous work

        def covers(res, t):
            return any(_epoch(w["window_start"]) <= t < _epoch(w["window_end"])
                       for w in res["windows"])

        human = _tick(path, st, cursor=_at(-HOUR), prompts=[_at(0), _at(P2_OFF)],
                      now=_at(116 * 60))
        wrong = _tick(path, st, cursor=_at(-HOUR), prompts=indexed, now=_at(116 * 60))
        assert covers(human, burst), human["windows"]
        # Every indexed turn is treated as a prompt, so the burst sits inside its own "covered"
        # set and the one thing the tick exists to characterise is the one thing it misses.
        assert not covers(wrong, burst), wrong["windows"]
        st.close()


# ------------------------------------------------------------------ the anchored analyze

def test_analyze_window_anchored_by_time_agrees_with_the_same_window_by_prompt():
    """`end_ts` is a second way to say WHERE the window ends and must not be a second way to
    compute WHAT is in it. Anchored at the prompt's own instant it has to give the prompt's own
    answer, field for field."""
    with tempfile.TemporaryDirectory() as tmp:
        path, st = _ingested(tmp, _deck_shaped())
        by_prompt = analyze_window(path, "P2", 60, None, store=st, refresh=False)
        by_time = analyze_window(path, None, 60, None, store=st, refresh=False,
                                 end_ts=_iso(P2_OFF))
        assert by_prompt == by_time, (by_prompt, by_time)
        st.close()


def test_a_fractional_span_is_honoured_exactly():
    """A closed gap's remainder is not a whole number of minutes, and rounding it up would
    reach back into the prompt-covered region the planner just excluded."""
    with tempfile.TemporaryDirectory() as tmp:
        path, st = _ingested(tmp, _deck_shaped())
        out = analyze_window(path, None, 54.0, None, store=st, refresh=False,
                             end_ts=_iso(P2_OFF - 3600))
        assert _epoch(out["window_start"]) == _at(P2_OFF - 3600 - 54 * 60), out["window_start"]
        st.close()


if __name__ == "__main__":
    fns = [(n, f) for n, f in sorted(globals().items()) if n.startswith("test_")]
    bad = 0
    for n, f in fns:
        try:
            f(); print(f"PASS {n}")
        except AssertionError as e:
            bad += 1; print(f"FAIL {n}: {e}")
    print(f"\n{len(fns)-bad}/{len(fns)} passed")
    sys.exit(1 if bad else 0)
