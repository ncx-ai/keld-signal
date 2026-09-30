# The activity `context` axis — what the work was FOR

**Date:** 2026-09-30
**Branch:** `feat/activity-context`
**Status:** design approved conversationally; tiers A+B to build, tier C deferred (see §6)
**Vocabulary source:** `keld-activity-types-v1.csv` (atv1), 68 rows, `context` column

## 1. What this axis answers, and what it does not

`context` is the **domain of purpose** of a piece of work: who or what the output
is *for*. Generating text **for marketing** against generating text **for finance
reporting** is the distinction, in the repo owner's words, and it is the whole
axis.

It is deliberately **not** three things it is easy to mistake it for:

- **Not the topic.** A block can discuss a legal matter while the work product is
  an engineering change.
- **Not the tool.** A block can write marketing copy into Notion. The system says
  where the output landed; the context says who it was for. `system_categories`
  is therefore a PRIOR here, never the answer.
- **Not the capability.** `activity_class` says what the model had to *do*
  (retrieve, author, verify). `context` says what field the doing served. They are
  independent axes and neither substitutes for the other.

### Vocabulary (11 values, from atv1)

| value | rows | value | rows |
|---|---|---|---|
| `general` | 13 | `medical` | 5 |
| `legal` | 8 | `scientific` | 5 |
| `media` | 8 | `sales` | 5 |
| `financial` | 7 | `support` | 4 |
| `marketing` | 6 | `none` | 4 |
| `operations` | 3 | | |

⚠️ **`none` is not a context. It is a marker that the axis does not apply**, and
reading it as a value to predict is the first way to get this wrong. It is carried
by exactly four verbs — `code.write`, `code.edit`, `embed`, `plan` — which are
most of an engineering organisation's work. The axis is silent there by
construction, not by failure.

## 2. Why this was deferred, and what changed

The context axis was deferred on 2026-09-28 as unvalidatable. That judgement was
correct on the evidence then and is not overturned here. Recorded verbatim in
`docs/notes/2026-09-24-activity-atv1-results.md`:

- **Never measured on real non-engineering work.** Both available corpora are 100%
  software engineering; every gold context is `general` or `none`, so a number from
  them measures nothing. This was "Study 4", never run.
- **The one context number that exists is a warning, not a result.** Arm F
  (flat 68-way scoring) over-assigned non-general contexts — "the strongest
  evidence yet that the context axis over-fires when offered". It is a
  false-positive rate only and says nothing about recall.
- **Two conclusions were reached and never built:** gate context on the VERB rather
  than scoring all 68 ids at once, and use team membership as a prior, which
  measured false domains 11 → 4 (−64%): *"team already worked where it belongs — on
  context."*

**What changed on 2026-09-29:** `system_categories` shipped — a declarative,
deterministic lookup from vendor products to 22 business categories. Its vocabulary
overlaps this axis nearly one-for-one (`legal_contracts`, `finance_billing`,
`crm_sales`, `support`, `marketing`). That is a route to several context values
which did not exist when the axis was deferred, and it is a LOOKUP, so it cannot
over-fire in the way arm F did.

## 3. The decomposition — three tiers, different evidence, different risk

The axis is not one problem. Separating it is what confines the risk.

| tier | contexts reached | evidence | can it be wrong? |
|---|---|---|---|
| **A — structural** | `none` | the verb itself | no — it is a property of the vocabulary |
| **B — deterministic** | `media`, `legal`, `financial`, `sales`, `support`, `marketing`, `medical`, `scientific` | artifact modality + `system_categories` | only if the table is wrong, which is checkable |
| **C — residual** | anything B did not reach; `general` against a real context | message text | yes — this is the only place over-firing can happen |

**Tiers A and B ship. Tier C is deferred (§6).**

### 3.1 Tier A — structural

⚠️ **THIS TIER CANNOT KEY ON atv1's VERBS, AND THE FIRST DRAFT OF THIS SPEC DID.**
atv1 marks `none` on `code.write`, `code.edit`, `embed` and `plan` — but that
17-verb vocabulary **is not published and was deliberately abandoned**: it was
measured at 38.8% falling through to `other` and replaced by the 9-value
`activity_class`. A tier keyed on it would depend on something no block carries.

The two vocabularies are not translatable either. `activity_class` answers what
CAPABILITY a request stressed; `author_code` is the only value that implies the
context axis does not apply, and `operate`/`verify`/`retrieve` say nothing about
domain — a finance analyst runs queries and checks results too.

So tier A keys on **code artifact evidence**, which is already published and is
the same kind of signal tier B uses: a block whose file evidence
(`file_types`, `lang`) is dominated by code extensions is code work, and its
context is `none`. `activity_class: author_code` is corroboration, never the
trigger on its own.

The correct behaviour is to say `none` out loud rather than stay silent: "the
axis does not apply here" is a different statement from "we could not tell", and
§5's absent rule depends on keeping them apart.

### 3.2 Tier B — deterministic

Two independent evidence lanes, neither of which reads message text:

**Artifact modality → `media`.** Four of `media`'s eight atv1 rows are
`audio.create`, `image.create`, `video.create` and `transcribe` — media by the
nature of the artifact, not by subject. The `file_types` and `artifact` levels
already carry this. A block whose outputs are `.mp4`/`.wav`/`.png` is media work.

⚠️ The remaining media rows (`text.create`/`summarize`/`transform` *about* media)
are NOT reachable this way and fall to tier C. Media coverage is therefore partial
by construction, and must be described as such rather than as "media is handled".

**`system_categories` → the business contexts.** A direct crosswalk:

| system category | context |
|---|---|
| `legal_contracts` | `legal` |
| `finance_billing` | `financial` |
| `crm_sales` | `sales` |
| `support` | `support` |
| `marketing` | `marketing` |
| `medical` (new, §4) | `medical` |
| `scientific` (new, §4) | `scientific` |

⚠️ **`operations` has no lane and is not reachable by A or B.** It is carried by
`extract`, `text.create` and `text.summarize` only, with no modality and no vendor
signature. Stated here rather than discovered later: this design does not publish
`operations` at all.

⚠️ Categories with no context counterpart — `knowledge_base`, `code_hosting`,
`ci_cd`, `cloud_infra`, `data_platform`, `observability`, `security_iam`,
`design`, `analytics_bi`, `scheduling`, `storage_files`, `ecommerce`, `ai_ml`,
`issue_tracking` — contribute NOTHING. They are not evidence of a context; they
are evidence of tooling. Mapping them would be the over-firing failure re-created
by hand.

### 3.3 Tier C — deferred, not designed away

The residual: work whose purpose is only legible in the text. `medical` and
`scientific` prose where no vendor system was touched; `general` against a real
context; writing *about* media; all of `operations`. §6 records why it is not built.

## 4. Vendor table expansion (`sidecar/app/analysis/systems.py`)

Two new categories, **22 → 24**. This is a published-vocabulary change:
`enrich.SchemaVersion` bumps and Atlas needs two display names.

- **`medical`** — epic, cerner, veeva, medidata, athenahealth, doximity, meditech,
  allscripts, nextgen, eclinicalworks, redox, particlehealth
- **`scientific`** — benchling, overleaf, arxiv, pubmed, zenodo, labarchives,
  dataverse, orcid, figshare, protocolsio, scite

Host suffixes where the brand is not the registrable label: `ncbi.nlm.nih.gov` →
pubmed, plus direct hosts for arxiv, zenodo, overleaf, benchling, epic, cerner,
veeva.

Every entry passes the four gates already in `systems.py` and its tests:

1. **Products and services, never general-purpose technology.** `jupyterhub` is
   deliberately EXCLUDED — it is run, not bought, which is the rule that removed
   `postgres`, `docker` and `terraform`.
2. **`AMBIGUOUS`** — `dimensions` (Digital Science) is a real product AND this
   codebase's own core noun; it is host-only.
3. **Product, not company** — keyed by product where a vendor spans categories.
4. **No hyphen or underscore** — both lanes split on them, so such a token is
   unreachable by construction.

⚠️ **`media` is NOT added as a system category.** It is a modality, already carried
by `file_types`/`artifact`, and a `media` system category would collide with
`marketing` on every design tool. Adding it would make two lanes disagree about the
same block.

## 5. What publishes

A new INVENTORY dimension, `activity_contexts`, distribution over the block — the
same shape and the same argument as `activity_classes`: above ~20 requests no unit
is coherent enough for one label.

⚠️ **ABSENT IS NOT `general`, AND THIS IS THE RULE MOST LIKELY TO BE BROKEN BY
SOMEONE BEING HELPFUL.** `general` is a real context meaning the work had no
particular domain. "We could not tell" is a different fact. Defaulting silence to
`general` manufactures the one value that looks like an answer, and a consumer
cannot distinguish it. When no tier fires, the dimension publishes nothing. Same
discipline as `facets_degraded`: never let a check that did not run publish a
confident negative.

**Honest consequence, stated up front:** on engineering work this dimension will be
`none` or absent nearly always. Its coverage in a legal, medical or scientific
organisation is ASSERTED from the vendor table and never measured, exactly as
`system_categories`'s is. A thin answer is unknown coverage, not an org without a
domain.

## 6. Tier C: deferred 2026-09-30, and why

**Nothing about the validation problem changed today.** The routes that failed on
2026-09-28 fail identically now:

- Synthesis manufactures ground truth for an inference, and two generators
  disagreed on 2 of 3 sessions when this was tried.
- Public chat data does not contain the work: 28 candidates in 59,857 real
  conversations, of which ~1 was genuine professional work.
- Neither available corpus contains non-engineering work at all.

Building tier C off-by-default was considered and rejected. The objection is
specific to this repo: **an inert pass whose correctness nobody can check is the
`KELD_DEV_BLOCKS` shape** — that feature was dead for its entire life and said
nothing, while `settings.DevBlocksMode`'s refusal sat in a function with no
callers. Shipping an unvalidatable model pass behind a flag reproduces it.

**The bar for revisiting**, written down now so it is not re-litigated from memory:
a corpus of real, non-engineering work with per-block ground truth, from an
organisation whose domain is known independently of the transcripts. Until one
exists, tier C is not built.

**Candidates already identified for that day**, so the work is not lost:

- Factored scoring — score the VERB, then gate the context pass on the verb.
  Never the flat 68-way arm, which is the one measured to over-fire.
- Team membership as a prior (−64% false domains), plus `system_categories` and
  the tier-B lanes as additional priors. The model chooses among candidates the
  priors already admitted; it never decides alone.
- Model bakeoff, none preferred in advance: GLiNER2 (resident under `auto` only);
  `gliner2.5-base-v1`, 0.77 GB, which went 3/3 on a context smoke test at 0.99
  confidence while failing the verb task, untested live; `convaiinnovations/laya`,
  421M ModernBERT-large, ~808 MB, Apache 2.0, whose `choice` task is this shape
  and which scores 0.950 on AG News 4-label. ⚠️ Laya's evidence is a COMMUNITY
  blog post, not an official card, and its base English checkpoint scores **0.362**
  on typed-decisions against 0.766 fine-tuned — the 0.950 is in-domain and
  fine-tuned, not a zero-shot promise.
- Any such pass must run under `ml_backend:"deterministic"`, because that is what
  `keld-agent install` writes. A GLiNER2-only pass reaches dev machines and
  essentially no real users — the inert-by-construction failure again.

## 7. Follow-on: vendor coverage — the importer premise was WRONG

⚠️ **THIS SECTION ORIGINALLY SPECIFIED AN IMPORTER OVER WIKIDATA AND THE MCP REGISTRY.
BOTH SOURCES WERE MEASURED ON 2026-09-30 AND NEITHER CAN SUPPLY THIS TABLE.** The
original text is replaced rather than amended, because a plan that says "build this"
gets built.

**What went wrong in the original reasoning:** I verified both sources were REACHABLE
— 4,071 Wikidata rows, a live registry — and never checked whether they were USABLE.
Confirming a thing exists is not confirming it answers the question.

**Wikidata `P452` is the wrong axis.** It records what industry a COMPANY OPERATES
IN, not what kind of tool a product IS. Measured on the 4,071 software items that
have both a website and an industry:

- The flagship B2B products mostly have **no industry property at all** — Jira,
  Figma, Snowflake and Asana are each blank.
- Where it exists it is multi-valued and mostly noise: Salesforce carries eight
  values, of which `customer relationship management` is usable and `automation`,
  `cloud computing`, `analytics`, `software industry`, `intelligent agent` and
  `software as a service` are not.
- The population is consumer/media heavy, not B2B SaaS: e-commerce 593, software
  industry 321, retail 199, journalism 156, **pornography 151**, tourism 69,
  Internet dating 48.

`issue_tracking` is not derivable from `software industry`. No crosswalk fixes that,
because the mismatch is in the axis rather than in the labels.

**The MCP registry is the wrong namespace.** Its `name` field is a reverse-DNS
PUBLISHING identifier — `ac.inference.sh/mcp`, `ae.brainy/grocery-prices`,
`agency.goji/goji` — not the brand token a transcript yields. Our resolver sees
`mcp__<uuid>__notion-fetch` and takes `notion`; the registry namespace never appears
there. The listing is also long-tail unknown publishers rather than the vendors an
enterprise runs on.

⚠️ **AND RUNTIME LOOKUP REMAINS REFUSED** for the reason it always was: it would send
vendor names off-device per block and put a network call in the enrichment path.

### What replaces it: the `unrecognized` bucket IS the gap report

The mechanism already ships and needs no new source. `system_categories` publishes
`unrecognized` whenever a registrable host or connector brand is seen and the table
cannot name it, and `integrations` / `mcp_servers` publish the raw names beside it.
So the fleet itself reports precisely which vendors are missing, ranked by how often
real people hit them.

That is strictly better than any importer for this problem: it is drawn from observed
usage rather than from someone's idea of what a company uses, it prioritises itself by
frequency, and it costs nothing to run because the evidence already crosses.

**The owed work is an Atlas-side view** — "top `unrecognized` vendors this month" —
and a habit of adding what it names. Hand curation is not a stopgap here; with a
frequency-ranked worklist it is the correct method, and the 659 entries audited on
2026-09-30 are its starting point rather than its ceiling.

⚠️ Coverage of this table is therefore ASSERTED, never measured, until such a view
exists and a real fleet feeds it — the same honest limit `system_categories` already
carries, and the same one §1 states for the whole axis.

## 8. Testing

- **Tier A** — a block whose file evidence is dominated by code extensions yields
  `none`; one with no code evidence does not. ⚠️ And a test asserting the trigger is
  NOT atv1's verb list, which is unpublished and abandoned — the mistake this spec
  made in its own first draft.
- **Tier B** — the crosswalk is total and one-way; every mapped system category has
  exactly one context; the 14 unmapped categories contribute nothing (asserted by
  name, so adding a category cannot silently start publishing a context).
- **Vendor table** — the four gates, as `test_analysis_systems.py` already applies
  them, extended to the new entries; `jupyterhub` asserted ABSENT with the reason.
- **End to end** — extend `test_systems_business_domains.py`: medical and
  scientific sessions through the real ingest → store → analyze path, reaching
  their category AND their context, over-firing into no other domain.
- **The absent/`general` rule** — a block with no firing tier publishes NO context;
  asserted directly, because this is the rule a future change is most likely to
  break by being helpful.

## 9. Open questions

- **`operations` is unreachable** and this design does not publish it. Is dropping
  it from the published vocabulary better than carrying a value that never appears?
  Deferred to the tier C day, when it would become reachable.
- **Which unit does routing want?** The repo owner previously said per-request for
  activity; this axis is designed per-block. If per-request context is wanted
  later, tier B is already per-reference and could be re-aggregated; tier A is per
  dominant verb and would need restating.
