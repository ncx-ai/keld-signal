# Effort Signals (Part A) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Publish three signals the analysis already computes and withholds — the price-weighted spend over a block, and the median and 90th-percentile inter-turn gap.

**Architecture:** Additive fields on the existing `effort` block. The percentiles are a new pure function in `analysis/latency.py`; the spend is a second call to the store accessor `analyze.py` already uses for edit bytes. `effort` has ONE formatter (`_effort(auth, tmp)`) and TWO gatherers — `_effort_from_store` and the parse-path ORACLE `_effort_from_rows`. The formatter gains two parameters; each gatherer computes them. An existing test asserts the two paths agree, so both gatherers must supply the new inputs or that equality breaks.

**Tech Stack:** Python 3.12 sidecar (`~/.keld/sidecar-venv/bin/python`, standalone test scripts, no pytest); Go daemon for the wire.

## Global Constraints

- Spec: `docs/superpowers/specs/2026-08-25-work-shift-detection-design.md`, Part A. **Three fields: `request_tokens`, `gap_p50_s`, `gap_p90_s`.**
- ⚠️ **NEVER sum `magnitude.TOKENS`.** It carries a request's cost on every line of that request, so summing multiplies a request by its line count (median 2, up to 12 measured). `magnitude.REQUEST_TOKENS` is the spend series — once per `requestId`, already price-weighted by `magnitude.token_weight`. Use `REQUEST_TOKENS` and nothing else.
- ⚠️ **A missing measurement is `None`, never `0`.** `authored_bytes` and `fast_share` already follow this and `analyze.py` documents why: "a sum of no terms IS zero once we know we looked, while 0/0 never is."
- The percentiles abstain on the SAME evidence rule as `tempo`: `None` when fewer than `latency.MIN_GAPS` gaps. All three timing fields must agree about whether the window had enough timing evidence.
- ⚠️ **The oracle must stay equal.** `analyze_window_by_parse` is retained as the equivalence oracle and never as a fallback; a test asserts 0 of 90 real prompts differ. Both effort paths gain the fields or that test fails.
- Sidecar `SCHEMA` bumps (the payload gains keys) and `enrich.SchemaVersion` bumps. Both, and check the current values rather than assuming.
- Never `git add -A` / `git add .` / `git checkout` / `git stash` / `git clean`. Stage only the paths you edit.
- These carry uncommitted owner work and must NOT be touched: `.gitignore`, `internal/agent/daemon/custom_passes.go`, `custom_passes_test.go`, `daemon.go`, `scripts/context_value.py`, `scripts/prompt-v9.md`, `internal/agent/publish/report.go`, `internal/agent/publish/report_show_test.go`.
- Do not start the keld daemon. Never a broad `pkill -f`.

## File Structure

| File | Change |
|---|---|
| `sidecar/app/analysis/latency.py` | new `percentiles(times, min_gaps=MIN_GAPS)` |
| `sidecar/app/test_latency.py` | its tests (this file is a MUTATION AUDIT script — keep that style) |
| `sidecar/app/analysis/analyze.py` | `_effort` formats three more; both gatherers supply them |
| `sidecar/app/analysis/__init__.py` | `SCHEMA` bump + changelog entry |
| `sidecar/app/test_analysis_analyze.py` | payload + oracle-equality coverage |
| `internal/agent/enrich/effort.go` | `Effort` gains three fields |
| `internal/agent/enrich/sidecar/*.go` | decode them |
| `internal/agent/enrich/labels.go` | `SchemaVersion` bump + changelog |
| `internal/agent/publish/window_test.go` | the exhaustive allowlist + `sampleWindow()` |

---

### Task 1: Gap percentiles

**Files:**
- Modify: `sidecar/app/analysis/latency.py`
- Modify: `sidecar/app/test_latency.py`

**Interfaces:**
- Consumes: `latency.gaps(times) -> [float]`, `latency.MIN_GAPS`.
- Produces: `latency.percentiles(times, min_gaps=MIN_GAPS) -> Percentiles(p50, p90, n_gaps)`, a namedtuple. `p50`/`p90` are `None` together when `n_gaps < min_gaps`.

- [ ] **Step 1: Write the failing tests**

```python
def test_percentiles_are_none_below_the_gap_floor():
    """The same abstention rule `tempo` uses, and for the same reason: three timing fields that
    disagree about whether the window had enough evidence would be unreadable together."""
    p = latency.percentiles([0.0, 10.0])          # 1 gap, MIN_GAPS is 5
    assert p.n_gaps == 1, p
    assert p.p50 is None and p.p90 is None, p

def test_percentiles_over_enough_gaps():
    # gaps: 1,2,3,4,100 -> p50 = 3, p90 near the tail
    p = latency.percentiles([0.0, 1.0, 3.0, 6.0, 10.0, 110.0])
    assert p.n_gaps == 5, p
    assert p.p50 == 3.0, p.p50
    assert p.p90 > 50.0, p.p90

def test_percentiles_use_the_same_deduped_gaps_as_tempo():
    """`gaps()` sorts and dedupes because stored timestamps are quantised to 0.1s; two turns in
    one bucket are ONE instant. Percentiles must not re-derive gaps and reintroduce zeros."""
    times = [5.0, 0.0, 1.0, 1.0, 2.0, 3.0, 4.0]       # unsorted, one duplicate
    assert latency.percentiles(times).n_gaps == len(latency.gaps(times))

def test_percentiles_tail_separates_two_windows_fast_share_cannot():
    """The reason this field exists. Steady 30s turns and alternating 2s/5m turns both sit at the
    same side of the 5s threshold for most gaps, so `fast_share` alone cannot tell them apart."""
    steady = latency.percentiles([0.0, 30, 60, 90, 120, 150])
    spiky = latency.percentiles([0.0, 2, 302, 304, 604, 606])
    assert steady.p90 < spiky.p90, (steady, spiky)
```

- [ ] **Step 2: Run to verify they fail**

Run: `cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_latency.py`
Expected: FAIL — `percentiles` not defined.

- [ ] **Step 3: Implement**

```python
Percentiles = collections.namedtuple("Percentiles", "p50 p90 n_gaps")


def percentiles(times, min_gaps=MIN_GAPS):
    """Turn timestamps -> the median and 90th-percentile inter-turn gap.

    `fast_share` collapses the whole distribution to one side of a 5-second threshold, so a
    window of steady 30-second turns and one alternating between 2s and 5m are indistinguishable
    by it. The tail is where "stopped to think" lives, and it is the half that decides whether a
    stretch was continuous work or a series of restarts.

    Reuses `gaps()` rather than re-deriving, so the sorting and the 0.1s-resolution dedupe apply
    identically -- a second derivation is a second place for the storage resolution to
    manufacture a zero-second gap.

    Abstains as `tempo` does, on the same `min_gaps`: both are None below it. Three timing fields
    that disagreed about whether the window had enough evidence would be unreadable together.
    """
    g = gaps(times)
    if len(g) < min_gaps:
        return Percentiles(None, None, len(g))
    return Percentiles(round(_pct(g, 0.50), 3), round(_pct(g, 0.90), 3), len(g))


def _pct(sorted_or_not, q):
    """Linear-interpolated quantile. `statistics.quantiles` needs n>=2 and takes a different
    convention per method; one explicit definition is cheaper to reason about than remembering
    which."""
    xs = sorted(sorted_or_not)
    if len(xs) == 1:
        return float(xs[0])
    i = q * (len(xs) - 1)
    lo = int(i)
    hi = min(lo + 1, len(xs) - 1)
    return float(xs[lo] + (xs[hi] - xs[lo]) * (i - lo))
```

- [ ] **Step 4: Run to verify they pass**

Run: `cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_latency.py`
Expected: PASS, and the file's MUTATION AUDIT still reports OK.

- [ ] **Step 5: Commit**

```bash
git add sidecar/app/analysis/latency.py sidecar/app/test_latency.py
git commit -m "feat(latency): the gap distribution, not just which side of 5s it fell on"
```

---

### Task 2: Both effort paths gain the three fields

**Files:**
- Modify: `sidecar/app/analysis/analyze.py`
- Modify: `sidecar/app/analysis/__init__.py`
- Modify: `sidecar/app/test_analysis_analyze.py`

**Interfaces:**
- Consumes: `latency.percentiles` from Task 1; `store.turn_magnitudes(session, start, end, kind=magnitude.REQUEST_TOKENS) -> [(ts, value)]`; `magnitude.REQUEST_TOKENS`.
- Produces: `effort` payload keys `request_tokens` (int or None), `gap_p50_s` (float or None), `gap_p90_s` (float or None).

⚠️ **The shape is one formatter, two gatherers**, which makes this easier than it looks:
`_effort(auth, tmp)` at `analyze.py:256` formats; `_effort_from_store(store, session, lo, hi)` at
:304 and `_effort_from_rows(rows)` at :284 gather. Change the formatter's signature to
`_effort(auth, tmp, spend, pcts)` and have EACH gatherer compute `spend` and `pcts` its own way —
the store one from `turn_magnitudes(kind=REQUEST_TOKENS)`, the oracle one by summing the
`mag`/`request_tokens` rows `events_for_turns` emitted, grouped as it already groups edit bytes.
One formatter means the two paths cannot format differently; the equality test then only has to
police the gathering.

- [ ] **Step 1: Write the failing tests**

```python
def test_effort_carries_the_spend_and_the_gap_distribution():
    with tempfile.TemporaryDirectory() as tmp:
        path, st, nlp = _write(tmp), _store(tmp), _FakeNlp()
        out = analyze_window(path, PROMPT_ID, store=st, nlp=nlp)
        e = out["effort"]
        for k in ("request_tokens", "gap_p50_s", "gap_p90_s"):
            assert k in e, (k, sorted(e))

def test_effort_abstains_rather_than_reporting_zero():
    """A missing measurement is None, never 0 — the rule authored_bytes and fast_share follow."""
    with tempfile.TemporaryDirectory() as tmp:
        path, st, nlp = _write(tmp, turns=TURNS[:2]), _store(tmp), _FakeNlp()
        e = analyze_window(path, PROMPT_ID, store=st, nlp=nlp)["effort"]
        assert e["gap_p50_s"] is None and e["gap_p90_s"] is None, e

def test_the_oracle_still_agrees_on_effort():
    """analyze_window_by_parse is the equivalence ORACLE and never a fallback. Adding a field to
    one path and not the other is exactly the silent divergence it exists to catch."""
    with tempfile.TemporaryDirectory() as tmp:
        path, st, nlp = _write(tmp), _store(tmp), _FakeNlp()
        served = analyze_window(path, PROMPT_ID, store=st, nlp=nlp)["effort"]
        oracle = analyze_window_by_parse(path, PROMPT_ID, nlp=nlp)["effort"]
        assert served == oracle, f"served={served} oracle={oracle}"
```

- [ ] **Step 2: Run to verify they fail**

Run: `cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_analysis_analyze.py`
Expected: FAIL — the keys are absent.

- [ ] **Step 3: Implement**

In `_effort(auth, tmp, spend, pcts)`, extend the returned dict:

```python
    return {
        "authored_bytes": auth.nbytes,
        "authoring_turns": auth.turns,
        "authored_status": auth.status,
        "fast_share": None if tmp.fast_share is None else round(tmp.fast_share, 3),
        "gaps": tmp.n_gaps,
        "tempo": tmp.reading,
        "tempo_status": tmp.status,
        # The SPEND series: magnitude.REQUEST_TOKENS is token_weight once per requestId, so it
        # sums to what the window cost. magnitude.TOKENS must never be summed here -- it carries
        # a request's cost on every line of that request (median 2 lines, up to 12), and a
        # 2x-over-counted total that looks like spend is the plausible-wrong-number failure the
        # two constants exist to prevent.
        "request_tokens": spend,
        "gap_p50_s": pcts.p50,
        "gap_p90_s": pcts.p90,
    }
```

In `_effort_from_store`, beside the existing `turn_magnitudes(..., kind=magnitude.EDIT_BYTES)` call, add:

```python
    spend_vals = [v for _ts, v in store.turn_magnitudes(session, lo, hi,
                                                        kind=magnitude.REQUEST_TOKENS)]
    spend = int(round(sum(spend_vals))) if spend_vals else None
    pcts = latency.percentiles(turn_times)
```

`turn_times` is the same instant sequence `tempo` is already given — reuse it, do not re-query.

Mirror both in `_effort_from_rows`: sum its `mag`/`request_tokens` rows and take the turn instants
the same way that function already does for `tempo`. Then pass both into `_effort`.

Then bump `SCHEMA` in `app/analysis/__init__.py` and add a changelog entry in the established
style, naming the three fields and stating that `TOKENS` is deliberately not among them.

- [ ] **Step 4: Run to verify they pass**

Run: `cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_analysis_analyze.py`
Then the whole sidecar suite:
```bash
cd sidecar && for f in app/test_*.py; do PYTHONPATH=. ~/.keld/sidecar-venv/bin/python "$f" >/dev/null || echo "FAIL $f"; done
```
Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add sidecar/app/analysis/analyze.py sidecar/app/analysis/__init__.py sidecar/app/test_analysis_analyze.py
git commit -m "feat(effort): the block's spend and its gap distribution reach the payload"
```

---

### Task 3: The wire

**Files:**
- Modify: `internal/agent/enrich/effort.go`
- Modify: `internal/agent/enrich/sidecar/` (wherever `EffortBlock` is decoded)
- Modify: `internal/agent/enrich/labels.go`, `labels_test.go`
- Modify: `internal/agent/publish/window_test.go`

**Interfaces:**
- Consumes: the sidecar payload's `effort.request_tokens` / `gap_p50_s` / `gap_p90_s`.
- Produces: `enrich.Effort` fields `RequestTokens *int64`, `GapP50S *float64`, `GapP90S *float64` with json tags `request_tokens,omitempty`, `gap_p50_s,omitempty`, `gap_p90_s,omitempty`.

Pointers, not values, for the same reason `AuthoredBytes` and `FastShare` are pointers: `omitempty`
on a plain `int64` cannot distinguish "no evidence" from a measured zero.

- [ ] **Step 1: Write the failing test**

```go
func TestEffortCarriesTheSpendAndGapDistribution(t *testing.T) {
	// A payload whose effort block has all three; assert they round-trip to Profile and to the
	// published Enrichment, and that absent fields decode to nil rather than 0.
}
```
Write it concretely against whatever fixture the existing effort decode test uses — find it with
`grep -rn "fast_share" internal/agent/enrich/sidecar/*_test.go`. Mirror that test's construction.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/agent/enrich/... -run Effort`
Expected: FAIL — fields do not exist.

- [ ] **Step 3: Implement**

Add the three pointer fields to `enrich.Effort` with doc comments stating: `request_tokens` is
window-scoped AND price-weighted, so it is NOT the raw per-event token counts Atlas already
receives from telemetry, and a consumer that adds them double-counts. Decode them in the sidecar
client's effort conversion. Bump `enrich.SchemaVersion` with a changelog entry.

- [ ] **Step 4: Run to verify it passes**

```bash
go test ./... && gofmt -l internal/ cmd/
```
Expected: 40 packages ok. `internal/paths/paths.go` is ALREADY unformatted on this branch —
pre-existing and unrelated, leave it.

⚠️ `internal/agent/publish/window_test.go` has an exhaustive wire-shape allowlist and a
`sampleWindow()` fixture. `effort` is a nested object so the allowlist may not trip, but the
fixture must carry the new fields or any assertion about them passes vacuously. Check both.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/enrich/effort.go internal/agent/enrich/sidecar internal/agent/enrich/labels.go internal/agent/enrich/labels_test.go internal/agent/publish/window_test.go
git commit -m "feat(publish): the block's spend and gap distribution reach Atlas"
```

---

## Self-Review

**Spec coverage.** Part A names exactly three fields; Tasks 1–3 deliver `gap_p50_s`/`gap_p90_s`
(Task 1 computes, Task 2 publishes) and `request_tokens` (Task 2). The spec's `TOKENS`-is-not-a-sum
warning is carried into Task 2's code comment. The telemetry-duplication warning is carried into
Task 3's doc comment. The abstention rule is Task 1's and Task 2's tests.

**Placeholders.** Task 3 Step 1 deliberately says "write it against whatever fixture the existing
effort decode test uses" rather than inventing a fixture I have not read — the grep is given so the
implementer finds it in one command. Every other step carries its code.

**Type consistency.** `Percentiles(p50, p90, n_gaps)` is produced in Task 1 and consumed as
`pcts.p50`/`pcts.p90` in Task 2. Payload keys `request_tokens`/`gap_p50_s`/`gap_p90_s` are identical
in Tasks 2 and 3. Go fields are pointers throughout, matching `AuthoredBytes`/`FastShare`.
