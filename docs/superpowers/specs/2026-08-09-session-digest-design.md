# Per-session work digest

Status: **design**, 2026-08-09. An LLM-maintained, semi-structured report of what a
session has been about — for the developer in it, and for a non-technical org admin
or project manager who wants to know where things stand.

Sibling work: `2026-08-07-llm-classifier-study-design.md` (the LLM backend study) and
`../plans/2026-08-07-conversational-dimensions-findings.md` (what is measurable).
This spec depends on that study's conclusions and reuses its components.

## The report

Per the project owner: a view of *what we've done, what happened, key thoughts and
insights, what we're doing and why, and where we're going.*

Not a summary. It has causal and forward-looking structure, and it must be readable
by two audiences with different needs: the developer wants the reasoning, the PM
wants the state of play.

## Decisions already taken

**Publication: the prose publishes, behind a per-org opt-in, default OFF.**
Decided by the project owner on 2026-08-09 after the alternative (publish only
structured fields, render narrative in Atlas) was presented. A PM cannot read a file
on a developer's laptop, so serving that audience requires transmission.

⚠️ **This changes a load-bearing sentence in `AGENTS.md`.** Today it reads:

> Raw prompt text is read on-device and must never be transmitted; the daemon
> publishes only masked labels + masked spans.

It must become, verbatim:

> Raw prompt text is read on-device and is never transmitted by the enrichment
> lane; the daemon publishes only masked labels and masked spans. **One exception,
> off by default:** when an org enables `digest_publish`, the session digest — LLM
> prose derived from the transcript — is published as written. Prose cannot be
> masked deterministically, so this is an explicit, org-scoped trade, not a gap.

**No probabilistic redactor.** Unbounded prose behind a hard gate is coherent. An
LLM "redactor" on top would contradict this repo's stated rejection of probabilistic
substitutes and would give false assurance. Gate or no gate — not a fake gate.

**Scope: domain-neutral design, capturable surfaces only.** Ships for
`claude_code`, `codex`, `gemini`. **Cowork is excluded because it cannot be
captured**: its tree on this machine holds **0 readable `.jsonl` files** and
`cowork_vm_node.log` is present — the VM-backed shape `AGENTS.md` documents as
unreadable from the host. Cowork users inherit the feature the day capture exists,
with no redesign.

## Domain neutrality

The digest must serve accountants and marketers as well as engineers.

**The spine is `function_guess`**, which spans 12 business functions and is the most
reproducible dimension measured (**cosine 0.99-1.00 across three architectures**).
Atlas's gallery is already organised by profession (`general / engineering / ai_dev /
content / business / governance`), so the cross-domain structure exists in both
codebases and is reused rather than reinvented.

Two consequences:

- **No section or prompt may reference engineering artifacts.** The schema below
  names no code, tests, or deploys.
- **`Signals.CodeBlocks` and `Signals.CodeLines` must not feed complexity.** They are
  structurally zero for a copywriter, so including them would score non-engineering
  work as trivial. Domain-neutral counts are `turns`, `user_turns`, `corrections` and
  text volume; `tool_calls` is partial (non-engineering users invoke fewer tools) and
  is used only as a relative signal within a session, never across professions.

⚠️ **Neutrality is designed, not validated.** The available corpus is **198/200
`software`, 184/200 `eng`**; Codex adds 14 files of the same developer's work and
Gemini is empty. There is no non-engineering material on this machine to test
against. Any quality figure in the verification plan therefore describes engineering
sessions until non-engineering transcripts exist.

## Digest schema

Structure is a hard guarantee via constrained decoding; prose inside each field is
free. Constrained decoding held **100% validity across every run** in the sibling
study, so this is a safe mechanism to lean on.

| field | type | meaning |
|---|---|---|
| `done` | prose | what has been accomplished |
| `happened` | prose | what actually occurred, including what went wrong |
| `insights` | list of prose | key thoughts and learnings, one per entry |
| `current` | prose | what is being worked on now |
| `why` | prose | the reason for it |
| `next` | prose | where this is going |
| `unresolved` | list of prose | what is still open, blocked, or abandoned |

`required` on all seven, `additionalProperties: false`. A model cannot omit a
section, add one, or return an undifferentiated blob.

**`unresolved` exists to defeat rubberstamping structurally.** Rubberstamping
thrives when a format has nowhere to put failure. A required field the model must
address means an all-positive report cannot validate — a guarantee, where "please be
honest" is a hope. Accepted cost: on a genuinely clean session this forces the model
to name open items, which reads as mild pessimism. That is the safer error for a
PM-facing artifact, and the deterministic `corrections` count lets a reader calibrate.

## Update procedure

Sessions reach **2,636 turns / 228 KB**, far beyond any context we can afford. So the
digest is built by **iterative refinement**:

```
digest(n) = LLM(digest(n-1) + turns since n-1 + deterministic facts)
```

Context stays bounded regardless of session length — the same principle as the
windowed inference in `2026-07-24-agentic-scale-input-bounding.md`.

Refine loops have four known failure modes, each designed against:

| failure | mitigation |
|---|---|
| Telephone-game drift: repeated re-summarising loses facts | `insights` is **append-only** — entries carry forward verbatim, never re-prosed, so the most valuable content stops being the most degraded |
| Unbounded growth: sections swell until context blows | Per-section length caps, enforced after generation, not merely requested |
| Recency bias: newest turns dominate | `done` and `why` are instructed to preserve earlier material unless contradicted |
| Silent contradiction: new content quietly reverses old | The update prompt requires revising in place and stating what changed |

**Deterministic facts are authoritative; prose is subordinate.** Each update receives
computed counts — `turns`, `user_turns`, `corrections`, tool volume, and whether the
most recent exchange was a correction — as facts the digest must be consistent with.
This makes rubberstamping machine-checkable: if `corrections = 5` and the prose says
"smooth progress", the two disagree and the disagreement is detectable. Counts cannot
be flattered.

**Trigger:** every **10 user turns**, plus a final refinement when a session goes
idle. At the measured arrival rate (p50 45 prompts/day) that is ~4-5 updates per
active session-day; at ~15-25 s per update on the 1.7B that is negligible against the
throughput headroom already established (worst observed day = 3.5 h of compute).

## Anti-hallucination

The verbatim substring gate already used for topic terms is extended to the digest:
**identifiers and proper nouns — file names, service names, customer names, versions —
must appear verbatim in the transcript, or they are flagged.**

That gate has receipts. In the sibling study it caught **11 of 11** fabrications by
Qwen3-0.6B, and they were the dangerous kind: plausible values lifted from the
prompt's own examples — "Slack connector" reported for a **Notion** prompt,
"Northwind" and "Acme Corp" for a **Globex** prompt.

⚠️ **Its limit, stated plainly: this bounds fabricated *specifics*, not fabricated
*judgements*.** "The team decided to prioritise X" contains no verifiable token.
Judgement quality is addressed only by the human review in the verification plan.

## Storage

SQLite, following `internal/spool/db.go`, which already encodes hard-won details:
create the file **0600 before `sql.Open`** (WAL/shm sidecars inherit the mode from the
main file at creation), put pragmas **on the DSN** rather than a post-open `Exec`
(`database/sql` silently retires connections and a replacement would come up with
`busy_timeout=0`), `SetMaxOpenConns(1)`, and cache one `*sql.DB` per path rather than
`sync.Once` so a transient open failure is not latched for the process lifetime.
`modernc.org/sqlite` is already a dependency and `CGO_ENABLED=0` is enforced by
`make crosscheck`, so nothing about the static-binary invariant changes.

**Snapshots only, with the consumed range recorded — no separate delta table.**

```sql
CREATE TABLE IF NOT EXISTS digest (
  session_id     TEXT    NOT NULL,
  seq            INTEGER NOT NULL,   -- 1..n, refinement number
  created_ts     INTEGER NOT NULL,
  schema_version INTEGER NOT NULL,
  model          TEXT    NOT NULL,   -- which model produced it
  from_prompt_id TEXT    NOT NULL,   -- delta boundary: first turn consumed
  to_prompt_id   TEXT    NOT NULL,   -- delta boundary: last turn consumed
  turns          INTEGER NOT NULL,
  signals        TEXT    NOT NULL,   -- the deterministic facts given to the model
  body           TEXT    NOT NULL,   -- the digest JSON
  PRIMARY KEY(session_id, seq)
);
CREATE INDEX IF NOT EXISTS ix_digest_session ON digest(session_id, seq DESC);
```

YAGNI on a delta table: the *input* delta is fully described by
`from_prompt_id..to_prompt_id`, and the transcript is already on disk, so replay
needs no duplicate copy of the turns. Storing snapshots plus boundaries gives replay,
audit, and drift measurement at a fraction of the size.

Published rows carry `body` verbatim when `digest_publish` is on; `signals` publishes
regardless (counts only, already safe under the existing redaction rules).

## Verification plan — pre-registered

The design above is a **hypothesis**. Every threshold is stated before measuring, so
a miss is a finding rather than a renegotiation.

| # | property | method | threshold |
|---|---|---|---|
| 1 | Structural validity | share of digests parsing against the schema | **100%** (constrained decoding; anything less is a bug) |
| 2 | Hallucinated identifiers | share of digest identifiers absent from the transcript | **<= 2%** |
| 3 | Rubberstamping | of sessions with `corrections > 0`, share whose digest names no friction in `happened` or `unresolved` | **<= 10%** |
| 4 | Drift / retention | inject known facts, refine over real turns, measure survival to snapshot N+5 | **>= 90%** retained |
| 5 | Usefulness | blind panel: given a digest alone, can a reader answer "what is being worked on / is it going well / what is next"? | **>= 80%** answerable |
| 6 | Model sizing | run 1-5 on Qwen3-1.7B and Qwen3-4B | 1.7B must reach the thresholds, or the budget must be revisited |

Test 3 is the important one and is only possible because `corrected` is harvested
ground truth (base rate **6.9%**, 94 positives over 1,357 turns). It gives
rubberstamping an objective referent, which summarisation tasks usually lack.

Test 6 is the **load-bearing risk**. Everything measured so far is classification and
extraction — short, schema-constrained outputs. A digest is free-form generation, the
capability class where Qwen3-0.6B mode-collapsed outright (100% "moderate" on a graded
scale) and where the 1.7B is least tested. If prose needs the 4B, its **5,192 MB
resting** breaks the stated **<= 3 GB** budget and the product decision changes. This
must be measured before any prompt tuning, because prompt tuning against the wrong
model is wasted work.

Test 5 cannot be automated and is the only subjective gate. It should be run by
someone other than the author of the prompts.

## Out of scope

- Cowork (no capture).
- Cross-session or cross-project rollups.
- Publishing anything derived from the digest beyond `body` and `signals`.
- Any use of the digest as agent context (a plausible second consumer, deliberately
  deferred until the quality numbers exist).

## Risks

| risk | standing |
|---|---|
| Prose needs a model that breaks the RAM budget | **Unmeasured — test 6, load-bearing** |
| Domain neutrality unvalidated | Acknowledged; no non-engineering corpus exists |
| Publishing prose widens the privacy surface | Accepted by the project owner; `AGENTS.md` rewording specified above |
| Refine loop degrades over long sessions | Designed against; test 4 measures it |
| Digest reads as pessimistic due to required `unresolved` | Accepted; safer error for a PM artifact |
| Identifier gate cannot bound fabricated judgements | Stated limit; test 5 is the only check |
