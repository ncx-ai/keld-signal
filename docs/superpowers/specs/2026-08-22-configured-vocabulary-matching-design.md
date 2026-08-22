# Configured-vocabulary matching — design

Attribution coverage is **36.9%**, and the model's measured contribution to it is **0.0%** over 130
classifier-windows. The gap is not a modelling problem. In the one non-engineering session in the
corpus the customer (**ACME**), the suppliers (**UnityPredict**, **Bedrock**, **Together.ai**,
**Vertex**), the model under evaluation (**Magenta**) and the initiative (**Developer Preview** ->
**Exchange Alpha**) are all stated in the message text, and **all 18 deterministic reference levels
read tool-call inputs only**. Nothing in the pipeline looks at what was said.

This spec covers the half of the fix that needs no model: matching prompt text against a
vocabulary the org has already declared.

## What it is

A Go-side pass that scans resolved prompt text for terms an org configured in Atlas, and publishes
**the identifier of the bucket that matched** — never a span of the text.

    prompt:    "...Customer is ACME, Agent 1 has three steps... Magenta is send all steps..."
    published: {"customer": "acme", "product": "magenta"}

## Why this shape

**Publish IDs, not spans.** `customEntityExtractor` already handles org-configured entities by
calling GLiNER2 and masking every span (`custom.go:239`). `Mask("customer", "UnityPredict")`
returns `…dict`, which tells a report that *a supplier was mentioned* but not which one — presence
without identity, and you cannot bucket spend by `…dict`. Publishing the matched id is what makes
it an attribution rather than an observation, and it means **no prompt text crosses the wire at
all**, so there is nothing to mask.

**Four properties fall out of the mechanism rather than being engineered:**

* It cannot miss a name that is present — it is a string match.
* It cannot invent one. Compare the enum that produced `ENG-4521`, `Sharp 25` and `Northwind` on
  windows where nothing was true, at 0% honest declines.
* It abstains by construction. No match, no claim. Seven attempts to buy abstention with a label
  all failed; GLiNER2 declined on **0 of 12** inapplicable windows at 0.956-0.998 confidence, in
  the mode designed to allow declining. Removing the question is the only thing that has ever
  worked, and a matcher with nothing to match asks nothing.
* People cannot appear. No org lists an employee as a customer, and only matched ids publish.

**It runs Go-side, off the readiness gate.** Every existing enrichment pass queues behind the
sidecar; a machine that has not yet downloaded 1.9 GB of weights produces nothing. This produces
attribution on the first prompt after install. `internal/agent/enrich/creddetect` is the precedent:
354 lines running gitleaks rules Go-side over the full text, already in the pipeline.

## Schema

A new pass kind, `vocabulary`, served by the existing `GET /v1/enrichment-settings` poll:

```json
{"key": "customer", "kind": "vocabulary", "title": "Customer",
 "labels": [{"id": "acme",      "text": "ACME Corp", "regex": "ACME|Acme Corp"},
            {"id": "northwind", "text": "Northwind", "regex": "Northwind"}]}
```

`RemoteLabel` already carries `ID`, `Text`, `Description` and **`Regex`** — and `Regex` is
currently parsed and then **never read by any code**. The wire contract already has the slot.

**Why a new kind rather than `entity` + `Regex`.** Reusing `entity` needs no Atlas change, which is
tempting. It is rejected because the two behave oppositely on the things that matter: `entity`
publishes masked spans, runs on the sidecar, and is naturally multi-valued; `vocabulary` publishes
ids, runs Go-side, and must be single-valued to serve as a cost bucket. One kind meaning both would
make the contract unreadable. The cost is one coordinated Atlas change.

## Matching

* **Case-insensitive, anchored at word boundaries.** Substring matching would fire `ACME` on
  `acmesoft`. Multi-word terms are matched whole.
* **Longest match wins** where two labels overlap, so `Magenta model` beats `Magenta`.
* **Whitespace collapsed** before matching, so a name broken across a line still matches.
* **Never truncated.** A partial identifier is a false identifier (AGENTS.md); a term matches whole
  or not at all.
* **Regex is org-supplied.** Go's RE2 has no backtracking, so a pathological pattern cannot cause
  catastrophic backtracking. A pattern that fails to compile is a `CustomReject` reported once per
  distinct reject set, exactly as `rejectReporter` already does for malformed passes — not once per
  poll, which turned one bad config into ~288 client events per machine per day.

## Output and the single-value rule

Per key the pass emits the matched id, a count, and the alternates:

```go
Labeled{Value: "acme", Confidence: 1.0, Producer: "customer-vocab-<version>"}
```

Confidence is `1.0` and means *the string was present*, not *this is the right customer*.

**Multi-label breaks cost allocation** — `[Engineering, Operations]` double-counts. So when several
labels of one key match:

* the label with the most mentions wins;
* the others are recorded as alternates, not as values;
* **a tie emits `ambiguous`**, never an arbitrary pick. A silently chosen winner is the
  plausible-wrong-number failure this study has hit roughly twenty times.

## Precedence

Ask the facts first. A `vocabulary` match is authoritative for its key and **suppresses any model
pass with the same key** — the same rule that absorbed `work_function` into `known` once an A/B
showed it was following the team hint rather than the content. Provenance is published alongside
(`matched:vocabulary` vs `known:cwd` vs `inferred:model`), because a financial number gets audited
and those are different epistemic objects.

## Where it runs

A **Wave 0** extractor, before the sidecar waves. It needs no `Model`, no pass deadline and no
job-context binding: it is a regex over text already resident in the daemon. It must not be able to
fail a job — a panic or a bad pattern degrades that key to absent, never the whole profile.

## Bounds

* Vocabulary size is capped and the cap is enforced daemon-side, not trusted from Atlas.
* Patterns compile **once per settings poll**, not per prompt, and hot-swap like custom passes.
* Matching cost is linear in text length and vocabulary size; it is measured before merge, not
  estimated, and reported per prompt.

## What this deliberately does not do

* **No discovery.** Names nobody configured stay invisible. In john's session `Keld`,
  `Together.ai`, `Bedrock` and `Vertex` are all present and would not publish. The honest limit is
  that **an org with no list gets nothing from this.**
* **No transmission of unmatched terms.** The local spaCy term level may contain person names;
  nothing unmatched leaves the machine. Surfacing candidates for an admin to promote is a separate
  feature with its own privacy design, and it is out of scope here.
* **No inference.** If a prompt says "the customer" without naming one, this pass says nothing.

## Testing

TDD, and every test asserts a behaviour that has been measured to matter:

* a present name matches; an absent one produces **no key at all**, not an empty value;
* `ACME` does not match `acmesoft` (boundary);
* `Magenta model` beats `Magenta` (longest wins);
* two labels tied on count emit `ambiguous`;
* a vocabulary match suppresses a model pass with the same key;
* an uncompilable regex is rejected once, and the other labels of that pass still work;
* the published payload contains **no span, no offset and no prompt text** — asserted on the wire
  shape, since that is the invariant, not an implementation detail.

## Open questions

1. **Aliases and case.** `Acme`, `ACME`, `Acme Corp.` — the regex handles it, but who writes the
   regex? An admin typing a name should not have to. A `match: [...]` list compiled into a pattern
   daemon-side is probably the better UX; that is an Atlas surface decision.
2. **Per prompt or per session.** A customer named once in message 1 attributes one prompt and not
   the next twelve. The state-carries rule says resolve at session scope and carry it forward.
   Unmeasured, and it materially changes coverage.
3. **Whether `text` alone is enough.** Tool inputs contain names too — `keld-acme-routing-scenarios.pptx`
   is a filename. Matching over tool inputs as well as message text is a cheap extension, and
   untested.
