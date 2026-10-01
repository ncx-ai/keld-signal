# The activity `verb` axis — mapping measured capability onto atv1

**Date:** 2026-10-01
**Vocabulary source:** `keld-activity-types-v1.csv` (atv1), 68 rows, `verb` and `family` columns
**Status:** design approved conversationally; spec to be reviewed before planning.
**Revert:** the lookup is additive (one new level, one schema bump); the study is
`rm scripts/verbsplit_*`.

## 1. What this delivers, and the half it does not

atv1's `activity_type_id` decomposes into two axes, and a fact about the CSV's own id scheme
proves the decomposition is the taxonomy's and not ours:

```
id = verb + "." + context      (64 of 68 rows)
id = verb                      (4 rows — exactly those with context "none")
```

Those four are `code.write`, `code.edit`, `embed`, `plan`. **atv1 drops the context suffix
when the axis does not apply**, which is the taxonomy stating in its own structure that context
is silent on most engineering work.

The two axes serve two different goals, and this spec delivers one of them:

| goal | axis | status |
|---|---|---|
| route tasks and agent runs to specific models | **`verb`** | **this spec** |
| understand at a high level what work is FOR, in business terms | `context` | **blocked — six measured negatives** |

⚠️ **This spec does not advance the business-understanding goal and must not be read as
doing so.** `context` has been measured and refused six times: GLiNER2 on prompts (12.7% real),
GLiNER2 on paths (70.7% but one domain only), hand-written keyword lists (17% on real paths),
agent self-report via hook (`SubagentStop` never reaches the model), document-type recognition
(parked), and conversation-domain recognition on WildChat
(`docs/notes/2026-10-01-conversation-domain-results.md`, every arm failed its pre-registered
bar). Tier A's code-artifact rule fires on 60.5% of blocks at ~88% precision and is the only
part of that axis standing. Nothing here changes any of it.

**`family` needs no inference at all.** It is a pure function of `verb` — verified across all
68 rows, every verb maps to exactly one family. It is therefore a 9→4 lookup table shipped as
part of this contract (§5), not a second published level.

## 2. The engine this rides on, and why it is trustworthy

`sidecar/app/analysis/reqclass.py` already assigns one of **nine activity classes** to each
inference request, from the tool call's name and arguments plus the output's shape. No model,
no network, no text inference.

Measured (`docs/notes/2026-09-24-activity-atv1-results.md`): macro **F1 0.920** against its
author's blind labels and **0.815** against an INDEPENDENT labeller's, inter-labeller kappa
**0.885**, with a **1.7% / 1.6%** unclassified residual across two different people's corpora
under identical rules, and a tool-free negative control leaking no tool-derived class across
11,575 WildChat assistant turns.

**This spec adds no new classifier.** It maps an existing, measured output onto atv1's
vocabulary, and measures only the two places where that mapping is not definitional.

## 3. The mapping

### 3a. Definitional — ships on reqclass's existing validation

These are not empirical claims. `verify → review` is a statement about what two words mean;
measuring it would measure the labeller's reading of a dictionary.

| class | verb | discriminator |
|---|---|---|
| `author_code` | `code.write` / `code.edit` | the tool is `Write` vs `Edit` — already in evidence |
| `author_prose` | `text.create` / `text.transform` | same `Write` vs `Edit` split |
| `verify` | `review` | direct |
| `delegate` | `plan` | direct |

### 3b. Excluded — not work, and this is a scope decision, not a gap

| class | share | why excluded |
|---|---|---|
| `operate` | 13.2% eng / 15.0% editorial | state changes — `git push`, `mkdir`, `docker run`, `npm install`. No model choice worth routing, and it says nothing about what the work was for. |
| `acknowledge` | — | a bare "done". No work in it. |
| `unclassified` | 1.7% / 1.6% | an honest abstention, and it stays one. |

⚠️ **atv1 is not missing a verb for `operate`.** atv1 is a taxonomy of model CAPABILITIES —
its own `verb_notes` read *"Richest verb — context drives model choice and price."* Composing
`git push` stresses no capability that routing could act on. Mapping it to `other` was
considered and rejected: it would put running a command in the same bucket as a genuine
abstention and discard a separation reqclass measures at F1 0.920. **Requests in these three
classes publish no verb**, and the distribution says so rather than quietly summing to less
than the work.

### 3c. Measured — the two real unknowns

| class | verb | share of requests |
|---|---|---|
| `synthesize` | `text.summarize` **or** `research` | 4.4% eng / 13.0% editorial |
| `retrieve` | `research` **or** `extract` | **37.3% eng / 28.4% editorial** |

⚠️ **`retrieve` is the single largest class in both corpora.** The verb carrying the most
routing volume is exactly the one that is not yet validated, so the study in §4 is most of this
spec's value, not a formality attached to it.

### 3d. What the mapping produces

Nine distinct verbs: `code.write`, `code.edit`, `text.create`, `text.transform`,
`text.summarize`, `review`, `extract`, `plan`, `research` — covering four of atv1's seven
families: `code`, `language`, `understanding`, `agentic`.

`media` (image/video/audio create) and `infra` (`embed`) are unreachable, and that is a fact
about the population rather than a limitation of the method: CLI coding agents do not generate
video. `fallback` (`other`) is unreachable because `unclassified` is excluded by §3b.

## 4. The splits, and the one thing that makes them tractable

⚠️ **BOTH DISCRIMINATORS ARE STRUCTURAL, NOT TEXTUAL. This is the whole reason this question
is answerable where `context` was not.** Six routes failed at inferring meaning from text. The
proposals below read tool identity, tool arguments, and request ORDER — evidence of the same
kind reqclass already scores 0.920 on. A proposal that resolved to "read the prose and decide"
would be the seventh attempt at a refuted thing and must be rejected in review.

**`synthesize` → `text.summarize` | `research`.** By construction `synthesize` has NO tool
calls (`route_class` reaches it only when `names` is empty), so its own request carries no tool
evidence at all. The discriminator is therefore **sequence**: a synthesis preceded by
retrieval requests is `research` — the model gathered, then reported; one preceded by none is
`text.summarize` — it condensed what was already in context. The lookback window and the
threshold are what the study measures.

**`retrieve` → `research` | `extract`.** Here the request's own tools are the evidence: a
`Grep` for a specific pattern, or a `Read` with an offset/limit, is pulling a known thing out —
`extract`. A broad `Read` of a whole file, an `ls`, or a `find` is reading to understand —
`research`. The exact partition over `RETRIEVE_TOOLS` and the Bash `RETRIEVE` regex is what the
study measures.

### The bar, pre-registered

- **A split must not degrade its parent.** reqclass measures 0.920 / 0.815 macro F1. A split
  scoring below its parent class's own agreement is not worth shipping.
  ⚠️ **A FAILING SPLIT PUBLISHES NOTHING — there is no unsplit parent to fall back to.**
  `synthesize` and `retrieve` are reqclass CLASS names, not atv1 verbs; atv1 has no value
  meaning "retrieved something, unspecified". So a failed split means those requests publish no
  verb and are declared, exactly as §3b's exclusions are. This asymmetry is deliberate and it
  raises the stakes on §3c's largest class: failing the `retrieve` split costs the verb axis
  37.3% of engineering requests, rather than costing it a little precision.
- **Precision over the requests the split ANSWERS on**, with abstention reported separately and
  counted wrong in the accuracy figure, since the unsplit baseline never abstains. The two
  denominators differ on purpose.
- **The majority-class floor per parent class**, not chance.

### Validation, and the specific circularity to avoid

- **Blind labels committed before the splitter exists**, provable from git. The labeller and
  the splitter's author are different contexts.
- ⚠️ **`is_report()` WAS TUNED ON OUR OWN CORPUS**, and the obvious `synthesize` splitter would
  reuse it. Validating on the same corpus would therefore be partly circular. The holdout is
  **WildChat**, already used in reqclass as a tool-free negative control over 11,575 assistant
  turns.
- **Unlike `context`, `verb` does not need domain diversity.** What the model DID is visible
  whatever field the work served, which is precisely why our engineering-only corpora can
  answer this question and could not answer the other one. State this in the results note: it
  is the load-bearing difference between this study and the six that failed.
- ⚠️ **Do not put aggregate label distributions in commit messages.** The conversation-domain
  study's "blind" labelling was compromised exactly that way — the arm author read the
  distribution out of `git show`. Aggregates belong in the results note, written after scoring.

## 5. Published shape

**`activity_verbs`** — an INVENTORY level, a sibling of `activity_classes`, emitted at the same
site in `sidecar/app/analysis/levels.py` from the same per-request record.

- **INVENTORY, not ALLOCATION**, for the reason already written into `dimensions.py`: above ~20
  requests no unit — session, block or subagent run — is coherent enough for a single label,
  while the DISTRIBUTION stays distinctive at every size. An ALLOCATION floor (winning share
  ≥ 0.50 and ≥ `MIN_EVIDENCE`) would publish `no_majority` on most units and discard the signal.
- **Cap 9 — the whole closed vocabulary**, so the level can never be truncated and
  `inventory_omitted` can never name it. A cut distribution is a wrong one, not a shorter one.

**`activity_verb_tokens`** — the same nine values weighted by OUTPUT TOKENS rather than counted.
⚠️ **Two denominators are needed because they DISAGREE, and not slightly:** on one real block
`author_prose` is 9.4% of calls and 25.8% of output tokens while `retrieve` is 18.9% of calls
and 5.5% of tokens. A consumer holding only the call count would report that block as
retrieval-dominated when prose authoring consumed the output. Neither denominator is wrong;
publishing one is. It rides the ordinary `n` field because `window.rollup` SUMS n per
`(level, ref)`. ⚠️ **Not a cost figure** — output is 10-14% of modelled cost, the rest being
cache reads.

**No `activity_family` level.** Family is a 9→4 rollup Atlas applies from this fixed table,
which is part of the contract:

```
code.write, code.edit                      -> code
text.create, text.transform, text.summarize -> language
review, extract                            -> understanding
plan, research                             -> agentic
```

`system_categories` earns its own level because it is strictly LESS identifying than its
neighbour `external_systems`; family has no comparable independent justification, and a level
costs a schema bump, a fixture rebaseline and an Atlas column. Both columns remain renderable.

**Schema:** `enrich.SchemaVersion` 26 → 27 (contract-affecting vocabulary change), and the eval
re-run that a vocabulary change requires. The fixture identity baseline
(`sidecar/app/analysis/testdata/fixture-identity-baseline.json`) needs rebaselining, and the
rebaseline must be **verified additive** — new levels only, no existing level's rows, total or
sha moving. A rebaseline that also altered an existing level would hide a regression behind a
legitimate schema addition.

## 6. Scope

**In:** the §3a lookup, the §3c study, one published level plus its token sibling, the family
table, the schema bump.

**Out:** `context` in any form. Any new model or model-backed classifier. Any change to
`reqclass.route_class`'s nine-value output — this maps that output, it does not revise it.
`media`/`infra` family coverage. An atv1 vocabulary extension for `operate`.

## 7. The gate

- **Both splits PASS** → ship nine verbs.
- **One passes** → ship that one split and the unsplit parent for the other. A partial result
  is a real result here, not a failure: `review`, `plan` and the four authoring verbs are
  unaffected either way.
- **Neither passes** → ship §3a alone: **six verbs** (`code.write`, `code.edit`, `text.create`,
  `text.transform`, `review`, `plan`) still covering **all four reachable families** — `code`,
  `language`, `understanding` (via `review`) and `agentic` (via `plan`). Record the outcome
  beside the six context negatives. The lookup is unaffected and stands on reqclass's own
  validation.

## 8. Open question, dated

**Whether `research` reached via two different paths is one verb or two.** It is produced by
both splits — from `synthesize` (gathered, then reported) and from `retrieve` (read broadly to
understand). atv1 has one `research` verb and this spec publishes one. If the study finds the
two populations behave differently for routing, that is an atv1 question, not a Signal one.
**Re-check when a routing consumer exists to care** — as of 2026-10-01 none does.
