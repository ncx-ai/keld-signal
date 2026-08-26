> # ⚠️ SUPERSEDED — do not build from this file
>
> Atlas's side of the block model now lives IN THE ATLAS REPO, written against that code rather
> than against assumptions made from here:
> `keld-atlas:docs/superpowers/specs/2026-08-25-block-model-atlas-design.md`, with its plan at
> `keld-atlas:docs/superpowers/plans/2026-08-25-workstreams-v2-roadmap.md`.
>
> This file is kept for its history and is WRONG in at least three ways:
>
> 1. **`covers` is DELETED** (`9d47e71`). Below it is a `jsonb` column and a schema line.
> 2. **Its central claim about `covers` is refuted.** It says *"Atlas cannot derive it, since only
>    the daemon…"*. Atlas can: it has `ToolEvent.session_id` and `event_ts`, the table is
>    RANGE-partitioned on `event_ts`, and it must run that time join anyway for COST — a turn
>    spanning several blocks would double-count spend through `covers`. Display and cost share one
>    join. Anyone reading that sentence would rebuild the thing we removed.
> 3. **It reads as though blocks replace the activity feed.** They do not: telemetry is a separate
>    lane already delivering raw activity live, and a block rail can only ever show SETTLED blocks,
>    so a blocks-only page is permanently blind exactly where the user is looking.
>
> It also lists "Signal Phases 0-4 … `covers`, the wire, the tick" — that sequencing died too: the
> tick's gap-finding is obsolete under blocks, because blocks tile active time and leave no gaps.

# Atlas's side of the block model — and how v1 comes out cleanly

Consumes `2026-08-25-block-model-contract-design.md`. Executed in `keld-atlas`; specified here
because it is one design set with the contract and Signal's side.

Two requirements, and the second shapes everything:

1. **Existing v1 customers keep working, unchanged.** A fleet upgrades machine by machine, so a
   single org will hold v1 and v2 rows simultaneously — possibly for months.
2. **v1 support must UNPLUG, not untangle.** When support ends, removing it should be deleting
   files and one registration, never editing a dozen read paths that grew `if v1` branches.

## The good news: no constraint change, no migration of existing rows

`uq_enrichment_corr` is `(org_id, source_id, corr_scheme, corr_id)` — **the scheme is already part
of the key** — and `CorrelationIn.scheme` is a free-form string on the wire. So a block row under
its own scheme coexists with prompt rows today, with no DDL on the constraint and no rewrite of
anything already stored. v1 rows are never touched, read differently, or migrated.

That was the blocker I expected to dominate this work and it does not exist.

## ⚠️ The blast radius is wider than the activity page

The activity feed is the visible part. The structural part is aggregation:

- **`TelemetryRollup`'s PRIMARY KEY includes `function_guess` and `activity_type`.** Both are
  model-backed and absent under v2. The dashboard's day-grain rollup is not merely fed by v1
  facets, it is *keyed* on them.
- **`workstream_usage.BUILTIN_USAGE_FIELDS`** is `(task_type, sensitivity, domain, activity_type,
  function_guess, subcategory)` — five of six model-backed. The Classifications/Dimensions pages
  are v1-shaped end to end.
- Ten files reference `corr_scheme`.

**The productive framing: v2 produces DIFFERENT dimensions, not fewer.** v1 aggregated on
model *guesses*; v2 aggregates on counted *facts* — `repo`, `language`, `output_type`, `skill`,
`tooling` — each with an evidence count behind it and an honest absent state. `TelemetryRollup`
already has a `repo` primary-key column, so the pivot has somewhere to land. The rollup does not
lose its purpose; it gets better columns.

## The principle: v1 is a PATH, not a parameter

This is the whole compatibility design, and everything below is a consequence.

**Anything that joins `Enrichment` on `corr_scheme == 'prompt_id'` is v1 code and lives in a v1
file.** Anything that joins on the block scheme is v2 code and lives in a v2 file. Nothing joins
on both.

Named anti-patterns, because each is the thing that turns removal into archaeology:

- ❌ A function gaining an `is_v1` / `scheme=` / `legacy=` parameter.
- ❌ A query with a scheme conditional in its `where`.
- ❌ A component branching on which shape it received.
- ❌ A shared "enrichment summary" type that both paths populate differently.
- ✅ Two sibling modules, one switch, at one call site.

### The package boundary

All v1-only server code moves to **`services/api/app/v1compat/`**, all v1-only client code to
**`services/web/components/activity/v1/`**. Every file in both opens with:

    # SIGNAL V1 COMPAT — delete this package when v1 support ends.
    # See docs/.../2026-08-25-atlas-block-activity-design.md § Unplugging.

Unplugging is then `rm -r` on two directories plus removing their imports. A CI check that greps
for `corr_scheme.*prompt_id` outside `v1compat/` keeps it honest — if that pattern appears
elsewhere, the boundary has leaked and the grep fails the build.

### What moves into v1compat, and what does not

**Into `v1compat/`** — the Enrichment join and everything downstream of it:
`turn_enrichment_summaries`, `aggregate_turn_enrichment`, `enrichment_for_event`,
`resolved_prompt_chars_map`, `rollup.py`'s enrichment laterals, `workstream_usage.py`,
`custom_pass_dim.py`, `labels.py`'s enrichment joins, `job_cost`/`job_crosstab`'s.
Client: `aggregated-enrichment-pills.tsx`, `classification-ledger.tsx`, `EnrichmentPillData`.

**Stays forever, untouched** — everything keyed on `ToolEvent`, which v2 does not change:
`turns.py`'s `assign_turn_key`/`build_turn`/`list_turns` grouping, the whole telemetry
aggregation, every facet and filter (`source, model, user, team, repo, work, agent`), the
websocket feed, `activity-grid.ts`. **The turn feed itself is not v1.** Only its enrichment
decoration is.

That distinction is the one to get right, and it is narrower than it sounds: **`turns.py` is 449
lines, of which 11 mention `Enrichment`.** Only those 11 move. Anyone reading "the turn feed is
v1" and deleting the module would take the entire telemetry aggregation with it.

## Additive schema

Nullable columns on `enrichments`, left NULL by every v1 row:

    start_ts        timestamptz  -- block span start
    end_ts          timestamptz  -- block span end
    start_reason    text         -- budget | idle | session_start | session_end
                                 -- NOTE: no `detected`. The change detector was ablated after
                                 -- measurement; see the contract. Do not add a column value or a
                                 -- render branch for it.
    end_reason      text
    covers          jsonb        -- [{prompt_id, from, to, complete}]

Plus an index for the temporal join: `(org_id, corr_scheme, start_ts, end_ts)`.

And **`corr_session_id` finally gets read.** It is written on every row today, echoed by one
endpoint, never joined, never indexed — the natural hook for a session-scoped row, half-built and
inert. The rail groups by it, so it gets an index.

## The v2 read path

New `services/api/app/services/blocks.py`, which knows nothing about prompts:

- `list_blocks(org, filters, range)` — blocks for the filtered scope, ordered by
  `(corr_session_id, start_ts)`. **Session first, time second**, because blocks tile within a
  session and overlap freely across them: sorting purely by time produces a merge in which
  adjacency implies a continuity that does not exist.
- `block_turns(org, block)` — the turns inside one block, by
  `ToolEvent.event_ts BETWEEN start_ts AND end_ts` scoped to the session. This is the temporal
  join, and it is *cheaper* than the string equality it replaces because `tool_events` is
  range-partitioned on `event_ts`.
- `covers` is used directly rather than recomputed — Atlas cannot derive it, since only the daemon
  can tell a human prompt from an agent turn.

**Pagination:** the rail is keyset on `(corr_session_id, start_ts)`, its own cursor. It does not
interleave into the existing `min(tool_events.id)` cursor and must not try to — that was the
constraint that made Direction A expensive, and a separate rail avoids it entirely.

## The page — Direction C

A block rail, and a detail pane.

**Rail:** grouped by session, blocks in time order within each group. Boundary reasons rendered
distinctly — the set is FOUR, not five, and none of them is a claim that the work changed:
`budget` is only "cut for length" (48.5% of blocks), `idle` means the person stopped (18.5%), and
`session_start`/`session_end` are the transcript's own edges. Showing `budget` and `idle` alike
turns a lunch break into a work transition. **Idle spans are genuine GAPS — no block covers them —
and must render as gaps rather than as unlabelled blocks**; that is what makes the unit work at all
(a cap without the idle rule attributes 29.8% of blocks against 95.3% with it, because three
quarters of its blocks were empty tiles over silence).

**Pane:** the selected block's facets — the 8 allocation dimensions, the 9 inventories, `dynamics`
readings, `prior`, `effort` — then its turns. This is the only surface with room for a block's own
facts, which is why C won over the banded feed.

⚠️ **Every allocation dimension arrives with a `status`, and rendering one alike regardless is a
misreport.** Each carries `(value, share, evidence, status)` with `status` in
`attributed | thin | no_majority | tie | absent`. Measured shares of the 12,016 dimension-slots:
53.2% attributed, 38.7% absent, **7.7% thin**, 0.4% tie/no_majority. A `thin` value is real evidence
under the significance floor — 198 slots held four observations, one short — so it must be shown,
visibly distinguished, never promoted to a plain value and never dropped. `absent` means no data at
all and is the only status with a null value.

The reason the producer no longer filters these is that it now sends `evidence`, and the floor's
own justification was that it could not. The consumer owns the threshold; the contract owns the
label.

Two things the pane should say that nothing in the product says today:

- **Stated negatives.** `Go absent — usually 50% of this repo`, when the salience block ships. A
  reader currently cannot learn that something expected is missing.
- **Concurrency.** Two people in the same repo on different branches at the same time is a fact
  worth surfacing, not noise. It is invisible today.

**The switch, in one place:** `has_blocks(org, filters)`. True → rail + pane. False → today's turn
feed, rendered by unmodified v1 components. One conditional, at the router, and it is the
conditional that gets deleted at unplug time.

## Mixed fleet — say so, don't hide it

An org mid-upgrade has both. The rail lists **sessions**, so a v1 session appears in it with no
blocks and an explicit label — `rmoss · Signal v1 · no blocks` — linking to the turn view.

Silently omitting v1 sessions from the rail would read as "this person did no work", which is the
same class of error as an unattributed dimension publishing an empty string. Absence is stated.

## Unplugging (the checklist this spec exists to make possible)

1. `rm -r services/api/app/v1compat/ services/web/components/activity/v1/`
2. Delete the `has_blocks` switch; the router calls the block path unconditionally.
3. Drop the `corr_scheme = 'prompt_id'` rows, or leave them — they are inert once nothing reads
   them, and the unique key means they cannot collide with block rows.
4. Remove the CI grep guard.
5. `TelemetryRollup`: drop `function_guess`/`activity_type` from the PK, having already added the
   v2 dimension columns alongside them in Phase 2 below.

No step edits a shared file. That is the test of whether this design held.

## Sequencing

⚠️ **Atlas ships before Signal flips its default.** Signal's spec makes
`ml_backend:"deterministic"` the last phase for this reason: the Context column renders exactly
`function_guess`, `subcategory` and `activity_type`, all three model-backed, so flipping first
blanks it on every row.

1. **Atlas Phase 1** — schema columns, `blocks.py`, the rail + pane, the `has_blocks` switch, the
   v1compat move. Renders nothing new until block rows arrive; ships safely at any time.
2. **Signal Phases 0–4** — measurement, block cutting, `covers`, the wire, the tick.
3. **Atlas Phase 2** — the rollup pivot to v2 dimensions, added alongside the v1 columns.
4. **Signal Phase 5** — the default flip. Only now does v1's facet set stop being produced.
5. Later, on a schedule that is a business decision rather than a technical one: unplug.

## Out of scope

- Org-wide baselines. The salience block is one machine's history; an org baseline is an Atlas
  design of its own.
- Retiring the turn feed. It stays as the v2 detail pane's content and is not v1 code.
- Routing. The block is an input to a routing decision, not the decision.
