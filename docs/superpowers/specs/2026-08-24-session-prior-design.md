# The session prior — a frame of reference, never a fallback

**Status:** design, awaiting review.

## What this is for

A window's characterisation is computed in isolation, so a value near the attribution floor is
indistinguishable from a value that is genuinely the whole story. The session it sits in is a
stable, cheap frame of reference that makes the difference visible.

Three failures already measured that this addresses:

- **A window reported `language: Python 0.576`** — barely over the 0.50 floor, because a planning
  hour touched two languages. The session was overwhelmingly TypeScript. That window is not "Python
  work", it is an EXCURSION, and neither number alone says so.
- **`MIN_EVIDENCE = 5` leaves 10.9% of 5-minute `project` slots unattributed** and `tooling` is
  **77.8% absent** at that scale. The session usually has a clear answer for both.
- **The `language` floor is doing real work** and must keep doing it — the answer is not to lower it.

## The rule that defines this design

**Contrast, never fallback.** The prior is reported ALONGSIDE the window's own answer. It never
supplies an answer the window did not have.

The rejected alternative — a thin window inheriting the session's value — buys coverage by
laundering "we do not know" into something that looks confident. That is precisely the defect
`MIN_EVIDENCE` was introduced to prevent, and this project has already paid twice for facets that
answered confidently when they should not have (`activity_type`'s `transform`: predicted 36 times,
right zero; `speech_act`'s `statement`: 22 times, right zero).

So: **an unattributed window stays unattributed.** The prior tells a reader what the session looked
like around it; it does not fill the hole.

## What it computes

For each allocation dimension, over the whole session ingested so far:

    value        the session-dominant value, under the SAME 0.50 floor and MIN_EVIDENCE
    share        its share of session evidence
    status       attributed / no_majority / thin / absent  (window.attribution's REASONS)

A session with no dominant `language` reports `no_majority`, which is itself informative — it says
the window's ambiguity is the session's ambiguity, not a thin-slice artefact.

Then, per window, the contrast:

    agrees       window value == session value
    departure    window share − session share for the window's value
    novel        the window's value does not appear in the session prior at all

`novel` is the interesting one: a value that is dominant in this window and absent from the session
is a genuine change of subject, and it is exactly what a per-window view cannot see.

## Why this is nearly free

The store already holds every event keyed by session. A session prior is `rollup_window` with wider
bounds — the same query the digest already runs. The tick (`c28092a`) provides the cadence; a prior
recomputed per tick is current to within one interval, and the interval was measured to be latency
rather than correctness.

**Recompute, do not accumulate.** An incrementally-updated prior would drift from the stored events
and could not be checked against them; recomputation is cheap and self-verifying, which is the same
reasoning that made reconcile's `pending` recomputed-whole-per-batch rather than merged.

## The boundary question, and the honest answer

**A session is not a fixed thing.** It grows. A prior computed at hour 1 differs from the same
prior at hour 7, and a window near the start is contrasted against less evidence than one near the
end. Two options:

  A. **Prior over the session so far** (causal). Matches what the daemon could have known at the
     time, but the same window contrasts differently depending on when it is computed.
  B. **Prior over the whole session** (retrospective). Stable and reproducible, but not available
     live, and a window enriched at hour 1 could never have used it.

**Recommend A**, and record the evidence count in the payload so a reader can see how much session
the prior rests on. B is reproducible in a way that is misleading: it describes a session the
daemon had not yet seen.

## What must be measured before shipping

1. **How often does the window agree with its session prior?** If agreement is near-total the
   contrast carries nothing and this should not ship — the same bar that dropped three dynamics and
   four of six transcript signals.
2. **How often is a window `novel`?** That is the signal's real yield.
3. **Does the prior's own coverage justify it?** A prior that is itself `no_majority` most of the
   time is not a frame of reference.
4. **Correlation with log window volume < 0.5.** The most reliably informative test across five
   studies — almost every attractive candidate turned out to be a size bucket wearing a label.

## Out of scope

- Cross-session or cross-machine priors. The store is per-machine, per-session-file.
- Using the prior to route, gate, or weight anything. It is reported; consumers decide.
- Any change to `MIN_EVIDENCE` or the 0.50 share floor.
