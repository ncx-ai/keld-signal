# Proposal — Signal reads the conjunctive Workstream rules Atlas now serves

**Status: PROPOSAL, not agreed. Written 2026-09-21** against keld-signal `e9f96d8` and
keld-atlas `feat/workstream-conjunctive-rules`. Nothing here is scheduled, and no Signal
code has changed. It is written now, ahead of the work, because someone is actively editing
this area and the Atlas half is landing first — so the constraints are worth having before
they are discovered.

Handing this over: the Atlas side ships **inert** for Signal. Everything below can wait
indefinitely without anything breaking. Read *What must not break* before touching
`internal/agent/projects/`, whether or not you take this on.

## What Atlas is changing, and what reaches this repo

Atlas is replacing a Workstream's flat matcher list with **rules**: a rule is an AND of
conditions, a Workstream is an OR of rules, and at most one Workstream per Group wins. That
lets one repository be a condition of two different rules in the same Group — the thing the
UI currently forbids. Spec:
`keld-atlas/docs/superpowers/specs/2026-09-18-workstream-conjunctive-rules-design.md`.

**Exactly one thing reaches Signal**, on `GET /v1/enrichment-settings`' `projects` key
(Atlas's `wire_projects`). It gains one additive field:

```jsonc
{
  "id": "...", "title": "...", "description": "...", "team": "...",
  "keywords": ["acme/web", "docs", "Launch week"],   // unchanged
  "rules": [["repo: acme/web", "mention: docs"]]     // NEW, inert
}
```

`settings.RemoteProject` does not declare `rules` and nothing in `settings/` or `agentcfg/`
sets `DisallowUnknownFields`, so today's client ignores it. **Current Signal behaviour is
unchanged, by construction**: Atlas keeps any value that still carries `tags` on its
original flattening path byte for byte (see *What must not break*, item 1).

## What Signal does today

`projects.Attribute` (`internal/agent/projects/attribute.go:534`) is **not a weaker version
of the new model — it is a different shape**. It is a hard-coded precedence ladder:

1. an attributed `repo` dim against `EffectiveRepos(p)` — an OR,
2. else a Jira-style ticket key parsed out of the `branch` dim against `p.TicketKey`,
3. else the optional embedding `Vector`.

There is no AND anywhere, and no `mention`. `Project` carries `Repos []string` +
`TicketKey string`.

⚠️ **And Atlas has never sent repositories.** `wire_projects` emits only `keywords`, so
`FromRemoteProjects` (`attribute.go:450-456`) recovers them by running `RepoLike` — a
regex — over the keyword bag. `RepoLike`'s own comment says it is "a loose, shape-only
filter… never worth SHOWING as a fact", which is why `Rules()` (`attribute.go:289`) holds
an unmatched candidate back from the project card until a real block confirms it.

**That is the actual prize here.** A rule's conditions are *typed*. Reading them lets Signal
stop inferring repositories from shape, and retires a workaround the code already documents.

## The proposal, in three pieces

### 1. Decode the rules, prefer them, keep the guess as fallback

- `settings.RemoteProject` gains `Rules [][]string \`json:"rules,omitempty"\``.
- `projects.Project` gains a rules field; `FromRemoteProjects` uses it **when non-empty** and
  falls back to today's `RepoLike` sweep when absent — an org on an older Atlas, or a value
  nothing has re-served yet, must keep working exactly as it does.
- `Document`'s byte-compatibility with `KELD_PROJECTS_FILE` is pinned by
  `TestProjectsFileByteCompatibleWithKeldProjectsFile` — keep it green; the file and the wire
  are the same shape on purpose.

### 2. Evaluate AND/OR, and feed it `named_terms`

`Attribute` and `MatchesFor` take `dims map[string]enrich.Labeled` today. `mention`
conditions need the ninth inventory, `WindowAnalysis.NamedTerms`.

**Both call sites already hold the whole `WindowAnalysis`** and pass only `.Workstreams`:
`daemon/v3blocks.go:322` (`r`) and `daemon/projectmatches.go:64` (`b.Analysis`). So widening
the parameter is local — no plumbing, no new sidecar call.

Rules to carry over from the Atlas side, each of which cost something there:

- **An empty rule matches nothing.** `all([])` is true; a half-filled editor row would
  otherwise match every block.
- **A condition on an unattributed facet fails the WHOLE rule** — it does not quietly reduce
  to the remaining conditions. This is the same discipline `attributedValue` already applies.
- **`mention` matches set-wise and case-folded** against `named_terms` values.
- **Most conditions wins; a tie is reported as a tie**, not broken by array position.

### 3. The pane

`ingress/projects.go:303` already serialises `projectView{Project; Rules []string}` — the key
is already called `rules`, so the name does not move; the shape becomes nested. The card wants
conditions joined by `+` within a rule and rules listed as alternatives.

⚠️ **Keep `Rules()`' discipline.** It deliberately shows `DeclaredRepos` always and a
`MatchedCandidate` only once a real block has confirmed it. Typed conditions are declared by
definition, so they show — but the `RepoLike` fallback path still produces guesses, and those
must stay behind the same gate. Do not collapse the two into one list.

## ⚠️ The thing that must be fixed in the same change

`projects.Attribute` returns `ReasonConflict` and **no attribution** for two or more matches
(`attribute.go:549`). That was right for the one-Group world it was written in. Under the new
model a block legitimately matches **one Workstream per Group**, so N Groups means N matches
means a **permanent conflict** on the local pane and in the ledger.

So adopting rules without fixing this makes the pane *worse*. The fix is to resolve per Group
rather than globally — which needs the Group a project belongs to, and ⚠️ **Atlas does not
serve one**: `RemoteProject` has no group field, and `FromRemoteProjects` reconstructs a
bucket from `team` (which carries the Workstream's *name* when a value has no owning team —
see `Project.Team`'s comment and `WorkstreamKey`). Deciding whether that proxy is good enough,
or whether Atlas should serve an explicit group id, is **the one open question in this
proposal** and is worth settling before any code.

`MatchesFor` is unaffected — it already returns every match, which is why Atlas-side
multi-group attribution needed no Signal change at all.

Claims: `attribute-conflicts-on-multi-match`, `matchesfor-reports-every-match`. Run
`claimlock diff <id>` before relying on either; both were verified 2026-09-17 and this
proposal did not re-check them.

## What must not break

1. ⚠️ **The keyword list's ORDER is load-bearing and nothing in this repo says so.** The
   sidecar builds a project's embedding document as `"Keywords: " + ", ".join(keywords)`
   (`sidecar/app/analysis/attribution.py:71`) and `attribution.Offsets` keys the centring
   baseline on that document's **text**. So merely reordering the list reads as a *reworded
   project*: new embedding, and the baseline reset to uncentred for the next 50 messages
   (`KELD_ATTRIBUTION_MIN_BACKGROUND`). Atlas's Task 7 preserves legacy order deliberately.
   If you rebuild `Keywords` from rules on this side, preserve it here too.
2. **`FromRemoteProjects` dedupes at the import boundary on purpose** — storage and display
   read the same list, so cleaning once is what stops them disagreeing. Any rules path must
   land inside that same boundary, not beside it.
3. **`MergeCandidates` is the overlay** — a local edit to an Atlas value shares its id, and
   concatenating instead of merging makes `Attribute` report a block as conflicting with
   itself. A rules field must union through that merge, not replace.
4. **A project that exists in Atlas is never changed from Signal** (decided 2026-09-05).
   Rules authored in Atlas are read-only here.
5. **Nothing in this package may read prompt text, a span or an offset.** `named_terms` is
   already-published inventory data — a term and a count — and stays that way.

## Suggested sequencing

Each step is independently shippable and leaves the pane working:

| # | Step | Ships alone? |
|---|---|---|
| 1 | Decode `rules`; prefer over `RepoLike`; keep the fallback | Yes — no behaviour change where Atlas sends no rules |
| 2 | Settle the Group question (above), then per-Group resolution in `Attribute` | Yes — fixes today's conflict bug independently of rules |
| 3 | AND/OR evaluation + `named_terms`, behind the decoded rules | Yes |
| 4 | Pane renders nested rules | Yes |

Doing **2 before 3** is the recommendation: it is the standing defect, it is smaller, and it
does not depend on the Atlas release having reached anyone.

## Not in scope

Signal's own local rule *authoring* (the bundle/suggestion flow in `suggest.go` / `edit.go`).
A person declares repo and ticket-key rules there today; whether they should be able to author
a conjunction locally is a product question nobody has asked, and the answer is not implied by
anything above.
