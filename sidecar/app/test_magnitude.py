"""Per-turn economic magnitudes: the token weight and the diff magnitude.

  cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_magnitude.py

Three contracts, in order of how badly a break would hurt:

1. **PRIVACY.** `old_string`/`new_string`/`content` are file contents. Nothing in this feature
   may retain a byte of them — see `test_edit_payload_bytes_never_reach_any_serialised_struct`,
   which drives the whole pipeline over a turn carrying a planted secret and then marshals every
   artefact it produced (rows, pending, the store file, the /analyze payload) looking for it.
2. **The weight's MEANING.** A number whose meaning is unclear is worse than none, so the ratios
   are asserted individually rather than only through a total: a regression that swapped cache
   reads to full price would still produce a plausible-looking number.
3. **The store shape.** A magnitude is a number on a TURN, not a level/ref, and the weighted
   rollup must come out in exactly `window.rollup`'s shape so `window.dominant` consumes it
   unchanged.
"""
import json
import os
import sys
import tempfile

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))

from app.analysis import magnitude, window
from app.analysis.levels import events_for_turns, quantize
from app.analysis.store import open_store
from app.analysis.workspace import new_evidence

SESSION = "3f1a9c2b"
T0 = 1755950400.0


# --- the weight's definition -----------------------------------------------------------------

def test_ratios_are_the_published_price_ratios():
    """The four multipliers, each pinned on its own. They are ratios to the model's own base
    input price and are uniform across the current Claude family (cache read 0.1x, 5m cache
    write 1.25x, 1h cache write 2x, output 5x) — which is the whole reason this needs no price
    table and makes no assumption about which model ran."""
    assert magnitude.CACHE_READ == 0.1
    assert magnitude.CACHE_WRITE_5M == 1.25
    assert magnitude.CACHE_WRITE_1H == 2.0
    assert magnitude.OUTPUT == 5.0


def test_a_cache_read_is_not_billed_at_full_price():
    """The defect this signal exists to avoid. Summing the four counts would make these two
    turns identical; they differ in cost by 10x on the read half."""
    read_heavy = magnitude.token_weight(
        {"input_tokens": 0, "output_tokens": 0, "cache_read_input_tokens": 1000,
         "cache_creation_input_tokens": 0})
    fresh_heavy = magnitude.token_weight(
        {"input_tokens": 1000, "output_tokens": 0, "cache_read_input_tokens": 0,
         "cache_creation_input_tokens": 0})
    assert read_heavy == 100.0, read_heavy
    assert fresh_heavy == 1000.0, fresh_heavy


def test_output_tokens_dominate_at_five_to_one():
    assert magnitude.token_weight({"output_tokens": 100}) == 500.0


def test_cache_creation_subobject_separates_5m_from_1h():
    """`cache_creation` carries `ephemeral_5m_input_tokens` and `ephemeral_1h_input_tokens`,
    which bill at 1.25x and 2x. Present on 29,990/29,990 usage-bearing lines of the frozen
    corpus, so this is the normal path, not the exotic one."""
    w = magnitude.token_weight({
        "cache_creation_input_tokens": 300,
        "cache_creation": {"ephemeral_5m_input_tokens": 100,
                           "ephemeral_1h_input_tokens": 200}})
    assert w == 1.25 * 100 + 2.0 * 200, w


def test_flat_cache_creation_field_is_the_fallback():
    """A producer without the sub-object (the committed fixture corpus is one) is billed at the
    5-minute rate — the overwhelmingly common TTL — rather than silently dropped."""
    assert magnitude.token_weight({"cache_creation_input_tokens": 100}) == 125.0


def test_batch_tier_halves_the_weight():
    """`service_tier: "batch"` is a uniform 50% discount, so it is a ratio like the others."""
    base = {"input_tokens": 1000}
    assert magnitude.token_weight(base) == 1000.0
    assert magnitude.token_weight(dict(base, service_tier="batch")) == 500.0
    # Anything else — standard, priority, absent — is left at 1.0. Priority's premium is not a
    # uniform ratio across models, so guessing one would be the unclear number this avoids.
    assert magnitude.token_weight(dict(base, service_tier="standard")) == 1000.0
    assert magnitude.token_weight(dict(base, service_tier="priority")) == 1000.0


def test_missing_or_junk_usage_is_zero_not_an_exception():
    for u in (None, {}, "nope", 7, {"input_tokens": None}, {"input_tokens": "x"}):
        assert magnitude.token_weight(u) == 0.0, u


# --- the diff magnitude ----------------------------------------------------------------------

def test_edit_bytes_is_the_extent_of_the_edit_site():
    """`max(len(old), len(new))`, not the sum: an Edit's `old_string` is an ANCHOR used to locate
    the change, so the larger of the two is the byte extent of file text the model handled."""
    n = magnitude.edit_bytes("Edit", {"old_string": "maxAttempts := 3",
                                      "new_string": "maxAttempts := 8"})
    assert n == 16, n


def test_a_pure_deletion_is_not_zero():
    """`len(new_string)` alone would read 0 for deleting 2 KB — the blind spot `max` closes."""
    assert magnitude.edit_bytes("Edit", {"old_string": "x" * 2048, "new_string": ""}) == 2048


def test_authoring_separates_from_a_typo_fix_by_two_orders_of_magnitude():
    typo = magnitude.edit_bytes("Edit", {"old_string": "retries = 3", "new_string": "retries = 8"})
    authoring = magnitude.edit_bytes("Write", {"content": "package main\n" * 700})
    assert authoring > 100 * typo, (typo, authoring)


def test_write_and_notebook_edit_and_multiedit():
    assert magnitude.edit_bytes("Write", {"content": "abcde"}) == 5
    assert magnitude.edit_bytes("NotebookEdit", {"new_source": "abc"}) == 3
    assert magnitude.edit_bytes("MultiEdit", {"edits": [
        {"old_string": "ab", "new_string": "abcd"},
        {"old_string": "xyz", "new_string": ""}]}) == 4 + 3


def test_bytes_not_runes():
    """A byte length, so a non-ASCII edit is not undercounted."""
    assert magnitude.edit_bytes("Write", {"content": "é"}) == 2


def test_a_non_edit_tool_has_no_diff_magnitude():
    for name, inp in (("Read", {"file_path": "/a/b.go"}),
                      ("Bash", {"command": "go test ./..."}),
                      ("Grep", {"pattern": "func"}),
                      (None, {})):
        assert magnitude.edit_bytes(name, inp) == 0, name


def test_edit_bytes_returns_an_int_never_a_string():
    """The structural half of the privacy guarantee: the only function that touches a payload
    returns a length. There is no code path by which it can hand back the text."""
    for args in (("Edit", {"old_string": "a", "new_string": "bb"}),
                 ("Write", {"content": "ccc"}),
                 ("MultiEdit", {"edits": [{"new_string": "d"}]})):
        assert isinstance(magnitude.edit_bytes(*args), int)


# --- what levels.py emits --------------------------------------------------------------------

SECRET_OLD = "SEKRIT-OLD-ZZZQ"
SECRET_NEW = "SEKRIT-NEW-ZZZQ"
SECRET_WRITE = "SEKRIT-CONTENT-ZZZQ"


def _turns():
    """One user turn and two assistant turns: the first carries usage plus an Edit and a Write,
    the second repeats the first's requestId (a streamed continuation) and must not be counted
    twice."""
    def ts(dt):
        import datetime
        return (datetime.datetime.fromtimestamp(T0 + dt, datetime.timezone.utc)
                .isoformat().replace("+00:00", "Z"))
    return [
        {"type": "user", "timestamp": ts(0), "uuid": "u1", "cwd": "/home/x/proj",
         "gitBranch": "main", "message": {"role": "user", "content": "fix the retry cap"}},
        {"type": "assistant", "timestamp": ts(1), "uuid": "a1", "requestId": "req_1",
         "cwd": "/home/x/proj", "gitBranch": "main",
         "message": {"role": "assistant", "model": "claude-opus-5", "content": [
             {"type": "text", "text": "Bumping it."},
             {"type": "tool_use", "id": "t1", "name": "Edit",
              "input": {"file_path": "/home/x/proj/retry.go",
                        "old_string": SECRET_OLD, "new_string": SECRET_NEW}},
             {"type": "tool_use", "id": "t2", "name": "Write",
              "input": {"file_path": "/home/x/proj/notes.md", "content": SECRET_WRITE}},
         ], "usage": {"input_tokens": 100, "output_tokens": 10,
                      "cache_read_input_tokens": 1000,
                      "cache_creation_input_tokens": 40,
                      "cache_creation": {"ephemeral_5m_input_tokens": 40,
                                         "ephemeral_1h_input_tokens": 0}}}},
        {"type": "assistant", "timestamp": ts(2), "uuid": "a2", "requestId": "req_1",
         "cwd": "/home/x/proj", "gitBranch": "main",
         "message": {"role": "assistant", "model": "claude-opus-5",
                     "content": [{"type": "text", "text": "Done."}],
                     "usage": {"input_tokens": 100, "output_tokens": 10,
                               "cache_read_input_tokens": 1000,
                               "cache_creation_input_tokens": 40}}},
    ]


def _extract():
    """`evidence` supplied so nothing opens a file — the point being that these two signals come
    from lines the parse already decoded, so the extraction needs no extra read."""
    return events_for_turns(_turns(), "/home/x/.claude/projects/proj/sess.jsonl",
                            "/home/x/.claude/projects", ("/home/x/proj",), None,
                            evidence=new_evidence(), session=SESSION)


WANT_WEIGHT = 100 + 1.25 * 40 + 0.1 * 1000 + 5 * 10


def test_the_rollup_weight_is_on_every_line_of_a_request():
    """`tokens` is the ROLLUP WEIGHT and is deliberately NOT deduped by `requestId`.

    A request is written as several assistant lines each repeating its `usage`, and 72% of all
    `tool_use` blocks (10,827 of 15,066 on the frozen corpus) sit on a line that is not the first
    of its request. Deduping would leave those events weightless, so a weighted rollup would drop
    nearly three quarters of tool-call evidence — which is not a smaller number, it is a different
    and wrong one. Both lines here share `req_1` and both must carry the weight.
    """
    rows, _pending, _n = _extract()
    tokens = [r for r in rows if r[5] == "mag" and r[6] == "tokens"]
    assert len(tokens) == 2, tokens
    assert {r[8] for r in tokens} == {WANT_WEIGHT}, tokens
    assert {r[0] for r in tokens} == {quantize(T0 + 1), quantize(T0 + 2)}, tokens


def test_the_spend_series_is_deduped_by_request():
    """`request_tokens` is the same number recorded ONCE, so a sum over turns is what the window
    actually cost. Without it, summing the weight would multiply a request by its line count."""
    rows, _pending, _n = _extract()
    spend = [r for r in rows if r[5] == "mag" and r[6] == "request_tokens"]
    assert len(spend) == 1, spend
    assert spend[0][8] == WANT_WEIGHT, spend
    weight_total = sum(r[8] for r in rows if r[5] == "mag" and r[6] == "tokens")
    assert weight_total == 2 * WANT_WEIGHT, weight_total   # the trap, made visible


def test_one_edit_bytes_row_per_edit_event():
    rows, _pending, _n = _extract()
    edits = [r for r in rows if r[5] == "mag" and r[6] == "edit_bytes"]
    assert len(edits) == 2, edits
    assert sorted(r[8] for r in edits) == [float(len(SECRET_NEW)), float(len(SECRET_WRITE))]


def test_no_magnitude_row_carries_a_ref():
    """A magnitude is not a reference. Bucketing the number into a `ref` string is exactly what
    would make the one thing it is — a number — the one thing it could not be queried as."""
    rows, _pending, _n = _extract()
    mags = [r for r in rows if r[5] == "mag"]
    assert {r[7] for r in mags} == {""}
    assert {r[6] for r in mags} <= set(magnitude.KINDS), {r[6] for r in mags}


def test_mag_rows_carry_the_turn_base_so_a_window_can_slice_them():
    rows, _pending, _n = _extract()
    mags = [r for r in rows if r[5] == "mag"]
    assert {r[0] for r in mags} == {quantize(T0 + 1), quantize(T0 + 2)}, [r[0] for r in mags]
    assert {r[1] for r in mags} == {SESSION}


def test_a_tool_use_on_a_continuation_line_is_still_weighted():
    """The end-to-end consequence of the two tests above: a tool call on a line whose requestId
    was already seen must still land in a weighted rollup. This is the 72% case."""
    turns = _turns()
    # Move the Edit onto the SECOND line of the request — the shape 72% of real tool calls have.
    edit = turns[1]["message"]["content"].pop(1)
    turns[2]["message"]["content"] = [edit]
    rows, _pending, _n = events_for_turns(
        turns, "/home/x/.claude/projects/proj/sess.jsonl", "/home/x/.claude/projects",
        ("/home/x/proj",), None, evidence=new_evidence(), session=SESSION)
    with tempfile.TemporaryDirectory() as tmp:
        st = _store(tmp)
        st.upsert_events(SESSION, rows, source_line=1)
        rl = st.weighted_rollup_window(SESSION, T0 - 1, T0 + 100, kind="tokens")
        assert ("Edit", WANT_WEIGHT) in rl["tool"], rl["tool"]
        st.close()


def test_ref_rows_are_unchanged_by_the_new_kind():
    """The published payload reads `ref` rows only. Adding a kind must not touch them — this is
    the unit-level companion to the fixture-identity gate."""
    rows, _pending, _n = _extract()
    refs = [r for r in rows if r[5] == "ref"]
    assert any(r[6] == "tool" and r[7] == "Edit" for r in refs)
    assert any(r[6] == "workspace" for r in refs)
    assert all(r[5] in ("ref", "say", "tok", "mag") for r in rows)


# --- the store -------------------------------------------------------------------------------

def _store(tmp):
    return open_store(os.path.join(tmp, "refseries.db"))


def _mag(dt, kind, value, line=1, session=SESSION):
    return (quantize(T0 + dt), session, "keld-signal", "main", False, "mag", kind, "",
            float(value))


def _ref(dt, level, ref, n=1, line=1, session=SESSION):
    return (quantize(T0 + dt), session, "keld-signal", "main", False, "ref", level, ref,
            float(n))


def test_magnitudes_are_stored_and_ref_rows_still_are_not_polluted():
    with tempfile.TemporaryDirectory() as tmp:
        st = _store(tmp)
        st.upsert_events(SESSION, [_ref(0, "workspace", "a"), _mag(0, "tokens", 250),
                                   _mag(0, "edit_bytes", 40)], source_line=1)
        c = st._conn()
        assert c.execute("SELECT COUNT(*) FROM event").fetchone()[0] == 1
        got = dict(c.execute(
            "SELECT kind, value FROM turn_magnitude WHERE session = ?", (SESSION,)))
        assert got == {"tokens": 250.0, "edit_bytes": 40.0}, got
        st.close()


def test_several_edits_on_one_turn_sum():
    with tempfile.TemporaryDirectory() as tmp:
        st = _store(tmp)
        st.upsert_events(SESSION, [_mag(0, "edit_bytes", 40), _mag(0, "edit_bytes", 2)],
                         source_line=1)
        v = st._conn().execute(
            "SELECT value FROM turn_magnitude WHERE kind = 'edit_bytes'").fetchone()[0]
        assert v == 42.0, v
        st.close()


def test_a_replayed_batch_does_not_inflate_a_magnitude():
    """Ingest replays a tail after a crash. The magnitude must be REPLACED, not added, exactly
    as `event.n` is — same key, same argument."""
    with tempfile.TemporaryDirectory() as tmp:
        st = _store(tmp)
        batch = [_mag(0, "tokens", 250)]
        st.upsert_events(SESSION, batch, source_line=7)
        st.upsert_events(SESSION, batch, source_line=7)
        v = st._conn().execute("SELECT value FROM turn_magnitude").fetchone()[0]
        assert v == 250.0, v
        st.close()


def test_clear_session_takes_the_magnitudes_with_it():
    """A reparse re-reads lines already stored. A magnitude left behind would double every
    window's weight — the same inflation `clear_session` exists to prevent for events."""
    with tempfile.TemporaryDirectory() as tmp:
        st = _store(tmp)
        st.upsert_events(SESSION, [_ref(0, "workspace", "a"), _mag(0, "tokens", 250)],
                         source_line=1)
        st.upsert_events("other", [_mag(0, "tokens", 9)], source_line=1)
        st.clear_session(SESSION)
        rows = st._conn().execute("SELECT session FROM turn_magnitude").fetchall()
        assert rows == [("other",)], rows
        st.close()


def test_weighted_rollup_reduces_to_the_event_rollup_when_weights_are_equal():
    """The property that makes the token-weighted share a GENERALISATION of the published one
    rather than a different metric: give every turn the same weight and the two agree exactly,
    up to the common factor."""
    with tempfile.TemporaryDirectory() as tmp:
        st = _store(tmp)
        rows = []
        for i in range(20):
            dt = i * 30.0
            rows.append(_ref(dt, "workspace", "keld-signal" if i % 3 else "keld-atlas"))
            rows.append(_ref(dt, "tool", ["Bash", "Edit"][i % 2], n=2))
            rows.append(_mag(dt, "tokens", 100))
        st.upsert_events(SESSION, rows, source_line=1)
        lo, hi = T0 - 1, T0 + 10000
        plain = st.rollup_window(SESSION, lo, hi)
        weighted = st.weighted_rollup_window(SESSION, lo, hi, kind="tokens")
        assert set(plain) == set(weighted), (sorted(plain), sorted(weighted))
        for lv in plain:
            assert weighted[lv] == [(r, n * 100.0) for r, n in plain[lv]], lv
        st.close()


def test_weighted_rollup_moves_the_dominant_value_when_weight_disagrees_with_count():
    """The whole point. `keld-atlas` appears on ONE turn and `keld-signal` on nine, so the event
    rollup calls it keld-signal; the one atlas turn burned 100x the tokens, so the weighted
    rollup calls it keld-atlas. If these two could never disagree the signal would be worthless
    and this test would be impossible to write."""
    with tempfile.TemporaryDirectory() as tmp:
        st = _store(tmp)
        rows = []
        for i in range(9):
            rows.append(_ref(i * 30.0, "workspace", "keld-signal"))
            rows.append(_mag(i * 30.0, "tokens", 10))
        rows.append(_ref(500.0, "workspace", "keld-atlas"))
        rows.append(_mag(500.0, "tokens", 10000))
        st.upsert_events(SESSION, rows, source_line=1)
        lo, hi = T0 - 1, T0 + 10000
        plain = st.rollup_window(SESSION, lo, hi)
        weighted = st.weighted_rollup_window(SESSION, lo, hi, kind="tokens")
        assert window.dominant(plain, "workspace")[0] == "keld-signal"
        assert window.dominant(weighted, "workspace")[0] == "keld-atlas"
        st.close()


def test_weighted_rollup_is_window_rollup_shaped_and_ordered():
    """`window.dominant`/`attribution`/`workstreams.payload` consume `{level: [(ref, total)]}`
    descending with ties alphabetical. The weighted rollup must be the same shape, and must get
    its order from `window.rollup` itself rather than a second ORDER BY."""
    with tempfile.TemporaryDirectory() as tmp:
        st = _store(tmp)
        rows = [_ref(0, "lang", "Python"), _ref(0, "lang", "Go"), _ref(0, "lang", "Rust", n=3),
                _mag(0, "tokens", 10)]
        st.upsert_events(SESSION, rows, source_line=1)
        rl = st.weighted_rollup_window(SESSION, T0 - 1, T0 + 100, kind="tokens")
        assert rl["lang"] == [("Rust", 30.0), ("Go", 10.0), ("Python", 10.0)], rl["lang"]
        st.close()


def test_weighted_rollup_covers_reconcile_rows_without_plumbing_a_thing():
    """Why the magnitude is a SIBLING TABLE joined on the turn's timestamp rather than a column
    on `event`: `lang`/`file`/`dir`/`component`/`ext` rows come only from `reconcile`, which is
    recomputed wholesale from `pending` into its own slot. A weight column on `event` would have
    to be carried through `pending` — a parse-state change and a forced reparse of every store.
    Joining on `(session, ts)` picks those rows up for free."""
    with tempfile.TemporaryDirectory() as tmp:
        st = _store(tmp)
        st.upsert_events(SESSION, [_mag(0, "tokens", 100)], source_line=4)
        st.replace_events(SESSION, 0, [_ref(0, "lang", "Go")])   # slot 0 == RECONCILE_SLOT
        rl = st.weighted_rollup_window(SESSION, T0 - 1, T0 + 100, kind="tokens",
                                       exclude_slots=())
        assert rl["lang"] == [("Go", 100.0)], rl
        st.close()


def test_weighted_rollup_honours_exclude_slots():
    with tempfile.TemporaryDirectory() as tmp:
        st = _store(tmp)
        st.upsert_events(SESSION, [_mag(0, "tokens", 100)], source_line=4)
        st.replace_events(SESSION, 0, [_ref(0, "lang", "Go")])
        rl = st.weighted_rollup_window(SESSION, T0 - 1, T0 + 100, kind="tokens",
                                       exclude_slots=(0,))
        assert "lang" not in rl, rl
        st.close()


def test_a_zero_magnitude_is_not_stored():
    """A zero magnitude is the ABSENCE of one. Stored, it would make a ref appear in a weighted
    rollup at total 0.0 — present and competing for the window — where an absent magnitude omits
    it, so two rollups could agree on every number and disagree on which keys exist. Measured on
    the frozen corpus: `<synthetic>` model turns carry an all-zero `usage` and produced exactly
    that divergence between the SQL and the study's own arithmetic."""
    with tempfile.TemporaryDirectory() as tmp:
        st = _store(tmp)
        st.upsert_events(SESSION, [_ref(0, "model", "<synthetic>"), _mag(0, "tokens", 0)],
                         source_line=1)
        assert st._conn().execute("SELECT COUNT(*) FROM turn_magnitude").fetchone()[0] == 0
        assert st.weighted_rollup_window(SESSION, T0 - 1, T0 + 100, kind="tokens") == {}
        st.close()


def test_an_all_zero_usage_emits_no_magnitude_row():
    turns = _turns()
    for t in turns[1:]:
        t["message"]["usage"] = {"input_tokens": 0, "output_tokens": 0,
                                 "cache_read_input_tokens": 0,
                                 "cache_creation_input_tokens": 0}
    rows, _p, _n = events_for_turns(
        turns, "/home/x/.claude/projects/proj/sess.jsonl", "/home/x/.claude/projects",
        ("/home/x/proj",), None, evidence=new_evidence(), session=SESSION)
    assert [r for r in rows if r[5] == "mag" and r[6] != "edit_bytes"] == []


def test_a_turn_with_no_magnitude_contributes_nothing_rather_than_erroring():
    with tempfile.TemporaryDirectory() as tmp:
        st = _store(tmp)
        st.upsert_events(SESSION, [_ref(0, "workspace", "a"), _ref(60, "workspace", "b"),
                                   _mag(60, "tokens", 5)], source_line=1)
        rl = st.weighted_rollup_window(SESSION, T0 - 1, T0 + 100, kind="tokens")
        assert rl["workspace"] == [("b", 5.0)], rl
        st.close()


def test_magnitudes_do_not_survive_the_retention_floor():
    """Events below the serving floor are gone; a magnitude that outlived them would be a
    growing table of numbers nothing can ever join to."""
    with tempfile.TemporaryDirectory() as tmp:
        st = _store(tmp)
        old = T0 - 500 * 86400.0
        st.upsert_events(SESSION, [(round(old, 1), SESSION, "r", "main", False, "ref",
                                    "workspace", "a", 1.0),
                                   (round(old, 1), SESSION, "r", "main", False, "mag",
                                    "tokens", "", 5.0)], source_line=1)
        st.upsert_events(SESSION, [_ref(0, "workspace", "b"), _mag(0, "tokens", 7)],
                         source_line=2)
        out = st.enforce_retention(now=T0, force=True)
        assert out["event_pruned"] >= 1, out
        assert out["magnitude_pruned"] == 1, out
        left = st._conn().execute("SELECT value FROM turn_magnitude").fetchall()
        assert left == [(7.0,)], left
        st.close()


def test_store_schema_version_is_six_and_additive():
    """v5 -> v6 adds a table and nothing else, so `CREATE TABLE IF NOT EXISTS` upgrades a v5
    file in place — no discard, no re-ingest. A v5 store simply has no magnitudes until its next
    ingest, which is the correct initial state."""
    import sqlite3
    from app.analysis import store as store_mod
    assert store_mod.SCHEMA_VERSION == 6
    with tempfile.TemporaryDirectory() as tmp:
        path = os.path.join(tmp, "refseries.db")
        st = _store(tmp)
        st.upsert_events(SESSION, [_ref(0, "workspace", "a")], source_line=1)
        st.close()
        # Pretend it was written by v5: stamp the old version, drop the new table.
        conn = sqlite3.connect(path)
        conn.execute("DROP TABLE turn_magnitude")
        conn.execute("PRAGMA user_version = 5")
        conn.commit(); conn.close()
        st = _store(tmp)
        # The v5 rows SURVIVE — this is the additive path, not the v4->v5 discard.
        assert st._conn().execute("SELECT COUNT(*) FROM event").fetchone()[0] == 1
        assert st._conn().execute("SELECT COUNT(*) FROM turn_magnitude").fetchone()[0] == 0
        st.upsert_events(SESSION, [_mag(0, "tokens", 3)], source_line=1)
        assert st._conn().execute("SELECT COUNT(*) FROM turn_magnitude").fetchone()[0] == 1
        st.close()


# --- the window rollup: a magnitude is a TOTAL, and its gate is a COUNT ----------------------

def test_authored_sums_the_windows_edit_turns_and_counts_them():
    """The published pair. The sum alone cannot separate one 22 KB authoring from fifty 400 B
    fixes, and the study's whole finding was that windows indistinguishable under `edit >= 5`
    differ by two orders of magnitude in bytes — so both numbers ship, never one."""
    a = magnitude.authored([16.0, 22187.0, 400.0], recorded=True)
    assert a.nbytes == 22603
    assert a.turns == 3
    assert a.status == "attributed"


def test_the_gate_is_the_COUNT_of_edit_turns_never_the_byte_SUM():
    """The token-weight artefact, named. `MIN_EVIDENCE` is a COUNT threshold; a byte sum in the
    thousands clears a floor of 5 unconditionally, which DELETES the floor -- exactly the shape
    that produced apparent +187/+123 attributions collapsing to ~0 once the gate was
    count-derived. So no significance floor is applied to the sum at all: a sum is a total, not
    an estimate from a sample, and there is no coin for it to have come from. One 22 KB edit is
    exactly 22 KB of authoring and is reported as such."""
    one = magnitude.authored([22187.0], recorded=True)
    assert one.status == "attributed", "a single edit is a real total, not a thin sample"
    assert (one.nbytes, one.turns) == (22187, 1)
    # And the count is what a reader gates on, which is only possible because it is published.
    assert one.turns < window.MIN_EVIDENCE


def test_no_magnitude_recorded_at_all_is_absent_not_zero_bytes():
    """A window whose turns carry no magnitude of ANY kind cannot be said to have authored zero:
    a v5 store upgraded in place holds no magnitudes until its next ingest (see
    `store.SCHEMA_VERSION`'s 5 -> 6 note), and a window of nothing but user turns has none
    either. `0` there would be a plausible wrong number; `absent` is the honest one."""
    a = magnitude.authored([], recorded=False)
    assert a.nbytes is None
    assert a.turns == 0
    assert a.status == "absent"


def test_a_recorded_window_with_no_edits_really_did_author_zero():
    """The other half, and the reason `recorded` exists. Turns whose cost WAS recorded and none
    of which edited anything genuinely authored 0 bytes -- unlike `fast_share`, a sum of no terms
    is unambiguous, so this is a number rather than an abstention."""
    a = magnitude.authored([], recorded=True)
    assert (a.nbytes, a.turns, a.status) == (0, 0, "attributed")


def test_a_zero_valued_edit_turn_is_not_counted_as_an_edit_turn():
    """`_aggregate_mag` never stores a zero (a zero magnitude is the ABSENCE of one), so the
    parse path must drop them too or the two paths would disagree on `turns` while agreeing on
    every byte."""
    a = magnitude.authored([0.0, 113.0, 0], recorded=True)
    assert (a.nbytes, a.turns) == (113, 1)


def test_authored_bytes_is_an_int_and_the_statuses_are_closed():
    a = magnitude.authored([113.5], recorded=True)
    assert isinstance(a.nbytes, int), "a byte count is an integer, not a float"
    assert set(magnitude.AUTHORED_STATUSES) <= set(window.REASONS)
    assert magnitude.AUTHORED_STATUSES == ("attributed", "absent")
    for vals, rec in (([], False), ([], True), ([1.0], True), ([0.0], False)):
        assert magnitude.authored(vals, recorded=rec).status in magnitude.AUTHORED_STATUSES


def test_authored_reports_no_reading_because_no_cut_point_was_measured():
    """Deliberate asymmetry with `latency.tempo`, which DOES state a conclusion. The tempo
    reading flips at a floor already in the package (0.50); a byte sum has no such floor -- the
    study reports a p10->p90 spread of 22x-87x and no cut point anywhere in it. Inventing
    `small`/`large` thresholds would be the fabricated vocabulary this package keeps paying for,
    so the block ships the labelled numbers and states nothing it did not measure."""
    assert not hasattr(magnitude, "AUTHORED_READINGS")
    assert set(magnitude.authored([1.0], recorded=True)._fields) == {"nbytes", "turns", "status"}


# --- privacy ---------------------------------------------------------------------------------

def test_edit_payload_bytes_never_reach_any_serialised_struct():
    """The marshal-level gate. Drive the real pipeline over a turn whose `old_string`,
    `new_string` and `content` are planted secrets, then serialise EVERY artefact the feature
    produces — the extraction rows, the reconcile `pending` list, the whole store file byte for
    byte, and the /analyze payload — and assert none of them contains a secret.

    Byte-for-byte on the store file, not `json.dumps` of a query: a column added to the wrong
    table, or an index on the wrong column, would be invisible to a query that did not think to
    ask for it, and this feature's failure mode is exactly a payload byte reaching a place
    nobody looked.
    """
    secrets = (SECRET_OLD, SECRET_NEW, SECRET_WRITE)
    rows, pending, _n = _extract()

    blob = json.dumps(rows, default=str)
    for s in secrets:
        assert s not in blob, f"{s} in the extraction rows"
    blob = json.dumps(pending, default=str)
    for s in secrets:
        assert s not in blob, f"{s} in the reconcile pending list"

    with tempfile.TemporaryDirectory() as tmp:
        path = os.path.join(tmp, "refseries.db")
        st = open_store(path)
        st.upsert_events(SESSION, rows, source_line=1)
        st.upsert_prompts(SESSION, [("u1", "2026-08-24T00:00:00Z")])
        lo = min(r[0] for r in rows) - 1
        hi = max(r[0] for r in rows) + 1
        payload = json.dumps({
            "plain": st.rollup_window(SESSION, lo, hi),
            "weighted_tokens": st.weighted_rollup_window(SESSION, lo, hi, kind="tokens"),
            "weighted_edits": st.weighted_rollup_window(SESSION, lo, hi, kind="edit_bytes"),
            "stats": st.store_stats(ttl=0),
        }, default=str)
        for s in secrets:
            assert s not in payload, f"{s} in a rollup / stats payload"
        st.close()

        raw = open(path, "rb").read()
        for s in secrets:
            assert s.encode() not in raw, f"{s} in the store FILE"

    # And the planted secrets really were in the input — otherwise every assertion above is
    # vacuous, which is the failure mode this branch keeps finding.
    src = json.dumps(_turns())
    for s in secrets:
        assert s in src, f"{s} never reached the input; the whole test proves nothing"


def test_the_only_string_reader_is_the_length_function():
    """`levels.py` must hand edit payloads to `magnitude.edit_bytes` and nowhere else. A future
    `add("ref", "diff", inp["new_string"], 1)` would pass every test above except this one.

    Asserted over the AST, not the source text: a comment or docstring may name a payload key
    (they do, and should), but no *expression* in `levels.py` may. Every way of reading one --
    `inp["old_string"]`, `inp.get("new_string")`, a dict of keys, a loop over a tuple of them --
    puts that key in the module as a string constant, so the absence of the constant is the
    absence of the read.
    """
    import ast
    path = os.path.join(os.path.dirname(os.path.abspath(__file__)), "analysis", "levels.py")
    tree = ast.parse(open(path).read())
    consts = {n.value for n in ast.walk(tree)
              if isinstance(n, ast.Constant) and isinstance(n.value, str)}
    for key in ("old_string", "new_string", "new_source", "edits"):
        assert key not in consts, (f"levels.py has the string constant {key!r}; edit payloads "
                                   "must be read only by magnitude.edit_bytes")
    # And the read really does happen, via the one permitted call — otherwise this test would
    # also pass on a levels.py that had simply dropped the feature.
    calls = {ast.unparse(n.func) for n in ast.walk(tree) if isinstance(n, ast.Call)}
    assert "magnitude.edit_bytes" in calls, calls


# --- mutation audit for the window rollup ----------------------------------------------------

def mutations():
    """One wrong `authored` per behaviour above; assert the suite fails. Every work unit on this
    branch found a test passing vacuously, so the gate is that each rule BITES."""
    fns = [f for n, f in sorted(globals().items()) if n.startswith("test_")]

    def run():
        for f in fns:
            f()

    orig = magnitude.authored
    orig_statuses = magnitude.AUTHORED_STATUSES

    def restore():
        magnitude.authored = orig
        magnitude.AUTHORED_STATUSES = orig_statuses

    def min_evidence_on_the_sum(values, recorded=False):
        """M1 -- THE artefact: the count floor compared against the byte sum, which a sum in the
        thousands clears unconditionally. Vacuous where it matters and wrong where it bites."""
        a = orig(values, recorded)
        if a.nbytes is not None and a.nbytes < window.MIN_EVIDENCE:
            return magnitude.Authored(None, a.turns, "absent")
        return a

    def min_evidence_on_the_count(values, recorded=False):
        """M2 -- the floor moved to the turn count: a genuine 22 KB single-turn authoring is
        discarded as a thin sample."""
        a = orig(values, recorded)
        if a.turns < window.MIN_EVIDENCE:
            return magnitude.Authored(None, a.turns, "absent")
        return a

    def unrecorded_as_zero(values, recorded=False):
        """M3 -- the abstention collapsed into a truthful-looking 0."""
        a = orig(values, recorded)
        return magnitude.Authored(0, 0, "attributed") if a.nbytes is None else a

    def recorded_zero_as_absent(values, recorded=False):
        """M4 -- the mirror: a real zero turned into "we did not look"."""
        a = orig(values, recorded)
        if a.nbytes == 0:
            return magnitude.Authored(None, 0, "absent")
        return a

    def count_zeros(values, recorded=False):
        """M5 -- zero-valued turns counted as edit turns, so `turns` disagrees with the store."""
        vals = [float(v) for v in values]
        if not vals and not recorded:
            return magnitude.Authored(None, 0, "absent")
        return magnitude.Authored(int(sum(vals)), len(vals), "attributed")

    def sum_only(values, recorded=False):
        """M6 -- the count dropped, leaving the bare number the study says is misread."""
        a = orig(values, recorded)
        return magnitude.Authored(a.nbytes, 0, a.status)

    cases = [
        ("M1 MIN_EVIDENCE applied to the byte sum", min_evidence_on_the_sum),
        ("M2 MIN_EVIDENCE applied to the turn count", min_evidence_on_the_count),
        ("M3 no record published as zero bytes", unrecorded_as_zero),
        ("M4 a real zero published as absent", recorded_zero_as_absent),
        ("M5 zero-valued turns counted", count_zeros),
        ("M6 the turn count dropped", sum_only),
    ]
    caught = 0
    for name, fn in cases:
        magnitude.authored = fn
        try:
            run(); print(f"MISSED  {name}")
        except Exception:
            caught += 1; print(f"CAUGHT  {name}")
        finally:
            restore()
    magnitude.AUTHORED_STATUSES = ("attributed", "empty")
    try:
        run(); print("MISSED  M7 the status vocabulary leaves window.REASONS")
    except Exception:
        caught += 1; print("CAUGHT  M7 the status vocabulary leaves window.REASONS")
    finally:
        restore()
    total = len(cases) + 1
    print(f"\nmutation audit: {caught} of {total} caught")
    return caught == total


if __name__ == "__main__":
    fns = [(n, f) for n, f in sorted(globals().items()) if n.startswith("test_")]
    bad = 0
    for n, f in fns:
        try:
            f(); print(f"PASS {n}")
        except AssertionError as e:
            bad += 1; print(f"FAIL {n}: {e}")
    print(f"\n{len(fns)-bad}/{len(fns)} passed\n")
    ok = mutations()
    print("MUTATION AUDIT " + ("OK" if ok else "INCOMPLETE"))
    sys.exit(1 if bad or not ok else 0)
