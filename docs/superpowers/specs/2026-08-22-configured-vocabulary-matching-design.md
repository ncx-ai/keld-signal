# Configured-vocabulary matching — design

> **Revised.** The first version of this spec placed the matcher Go-side. Three of its four
> arguments for that did not survive checking, and they are recorded below rather than quietly
> dropped, because a spec that silently loses a refuted claim is the same defect one level up.

Attribution coverage is **36.9%**, and the model's measured contribution to it is **0.0%** over 130
classifier-windows. The gap is not a modelling problem. In the one non-engineering session in the
corpus the customer (**ACME**), the suppliers (**UnityPredict**, **Bedrock**, **Together.ai**,
**Vertex**), the model under evaluation (**Magenta**) and the initiative (**Developer Preview** ->
**Exchange Alpha**) are all stated in the message text, and every one of the deterministic
reference levels reads tool-call inputs only.

This spec covers the half of the fix that needs no model: matching prompt text against a
vocabulary the org has already declared.

## What it is

A sidecar pass that scans resolved prompt text for terms an org configured in Atlas, and publishes
**the identifier of the bucket that matched** — never a span of the text.

    prompt:    "...Customer is ACME, Agent 1 has three steps... Magenta is send all steps..."
    published: {"customer": "acme", "product": "magenta"}

## Where it runs, and why that changed

**In the sidecar, as `POST /match`, bypassing the single-flight runner.**

The original rationale for Go-side was tested and mostly failed:

| claim | verdict |
|---|---|
| "It would queue behind inference on the single-flight runner" | **False.** Only `/entities`, `/classify` and `/extract` go through `_dispatch` -> `runner.submit`. `/health` and `/metrics` already bypass it, and `/match` would too. |
| "It must sit off the readiness gate" | **Weak.** The gate is a daemon scheduling choice, not physics; a pass can be exempted from it wherever it runs. |
| "Vocabulary would drift across worker recycles" | **Mostly false.** The FastAPI parent survives recycles and deliberately holds no model, so the vocabulary lives there safely. |
| "The sidecar may be absent" | **Holds** — see the trade-off below. |

What decided it is the other direction: **every measurement behind this work is Python.** The
term level, the reference levels, the digest, spaCy, bashlex — none has a Go counterpart. A Go
matcher would be a reimplementation whose behaviour is unvalidated, with normalisation and
matching semantics living in two places and two bugs to fix instead of one. `creddetect` is Go
because it runs on every prompt with no dependency; that reasoning does not transfer to a pass
meant to sit beside spaCy.

**The trade-off being accepted.** The macOS pkg ships *without* the sidecar — `onboard.command`
fetches it separately and failure is explicitly non-fatal ("telemetry still works, enrichment jobs
spool"). On a machine where that fetch failed, a sidecar-side matcher produces nothing, where a
Go-side one would have produced attribution from the first prompt. This is a product decision
about how much that machine matters, and it is recorded here rather than resolved.

## Why publish IDs, not spans

`customEntityExtractor` already handles org-configured entities by calling GLiNER2 and masking
every span (`custom.go:239`). `Mask("customer", "UnityPredict")` returns `…dict`, which tells a
report that *a supplier was mentioned* but not which one — presence without identity, and you
cannot bucket spend by `…dict`. Publishing the matched id is what makes it an attribution rather
than an observation, and it means **no prompt text crosses the wire at all**, so there is nothing
to mask.

Four properties follow from the mechanism rather than being engineered:

* It cannot miss a name that is present — it is a string match.
* It cannot invent one. Compare the enum that produced `ENG-4521`, `Sharp 25` and `Northwind` on
  windows where nothing was true, at 0% honest declines.
* It abstains by construction. No match, no claim. Seven attempts to buy abstention with a label
  all failed; GLiNER2 declined on **0 of 12** inapplicable windows at 0.956-0.998 confidence, in
  the mode designed to allow declining, and its relation extraction fired on 47% of windows where
  the relation could not exist. Removing the question is the only thing that has ever worked, and
  a matcher with nothing to match asks nothing.
* People cannot appear. No org lists an employee as a customer, and only matched ids publish.

## Schema and distribution

A new pass kind, `vocabulary`, served by the existing `GET /v1/enrichment-settings` poll:

```json
{"key": "customer", "kind": "vocabulary", "title": "Customer",
 "labels": [{"id": "acme",      "text": "ACME Corp", "regex": "ACME|Acme Corp"},
            {"id": "northwind", "text": "Northwind", "regex": "Northwind"}]}
```

`RemoteLabel` already carries `ID`, `Text`, `Description` and **`Regex`** — and `Regex` is
currently parsed and then **never read by any code**. The wire contract already has the slot.

The daemon owns the settings poll, so it **pushes the compiled vocabulary to the sidecar** on
every change (`POST /vocabulary`), hot-swapping it exactly as custom passes hot-swap today. It
lands in the FastAPI parent, which survives worker recycles, so a recycle never silently empties
it. A daemon restart re-pushes; that is the floor we want.

## Matching

* **Case-insensitive, anchored at word boundaries.** Substring matching would fire `ACME` on
  `acmesoft`. Multi-word terms match whole.
* **Longest match wins** where two labels overlap, so `Magenta model` beats `Magenta`.
* **Whitespace collapsed** first, so a name broken across a line still matches.
* **Never truncated.** A partial identifier is a false identifier (AGENTS.md); a term matches
  whole or not at all.
* **No token budget.** Unlike every other sidecar call, this one has no `max_len`: matching is a
  regex, its cost is linear in text length, and nothing here is a transformer. The 768-token
  ceiling that governs `/classify` does not apply, so the full prompt is scanned.
* **Regex is org-supplied.** Python's `re` backtracks, unlike Go's RE2, so a pathological pattern
  IS a denial-of-service risk here where it was not Go-side. Patterns are therefore compiled
  daemon-side at poll time and rejected on failure, matching is run under a wall-clock budget, and
  the budget expiring degrades that key to absent rather than failing the job. This is a real cost
  of the sidecar decision and is called out rather than assumed away.
* A pattern that fails to compile is a `CustomReject`, reported once per distinct reject set as
  `rejectReporter` already does — not once per poll, which turned one bad config into ~288 client
  events per machine per day.

## Output and the single-value rule

Per key the pass emits the matched id, a count, and the alternates:

```go
Labeled{Value: "acme", Confidence: 1.0, Producer: "customer-vocab-<version>"}
```

Confidence is `1.0` and means *the string was present*, not *this is the right customer*.

**Multi-label breaks cost allocation** — `[Engineering, Operations]` double-counts. So when several
labels of one key match:

* the label with the most mentions wins;
* the others are recorded as alternates, not values;
* **a tie emits `ambiguous`**, never an arbitrary pick. A silently chosen winner is the
  plausible-wrong-number failure this study has hit roughly twenty times.

## Precedence

Ask the facts first. A `vocabulary` match is authoritative for its key and **suppresses any model
pass with the same key** — the rule that absorbed `work_function` into `known` once an A/B showed
it was following the team hint rather than the content. Provenance publishes alongside
(`matched:vocabulary` vs `known:cwd` vs `inferred:model`), because a financial number gets audited
and those are different epistemic objects.

## What this deliberately does not do

* **No discovery.** Names nobody configured stay invisible. In john's session `Keld`,
  `Together.ai`, `Bedrock` and `Vertex` are all present and would not publish. The honest limit is
  that **an org with no list gets nothing from this.**
* **No transmission of unmatched terms.** The local spaCy term level may contain person names;
  nothing unmatched leaves the machine. Surfacing candidates for an admin to promote is a separate
  feature with its own privacy design.
* **No inference.** If a prompt says "the customer" without naming one, this pass says nothing.

## Testing

Sidecar tests are standalone scripts, no pytest (AGENTS.md). Every case asserts a behaviour that
has been measured to matter:

* a present name matches; an absent one produces **no key at all**, not an empty value;
* `ACME` does not match `acmesoft` (boundary);
* `Magenta model` beats `Magenta` (longest wins);
* two labels tied on count emit `ambiguous`;
* a vocabulary match suppresses a model pass with the same key;
* an uncompilable regex is rejected once and the pass's other labels still work;
* a catastrophic-backtracking pattern hits the wall-clock budget and degrades that key, without
  taking the request or the job with it;
* `/match` returns while an inference is in flight — proving it does not touch the runner;
* the published payload contains **no span, no offset and no prompt text**, asserted on the wire
  shape, because that is the invariant rather than an implementation detail.

## Open questions

1. **Aliases and case.** `Acme`, `ACME`, `Acme Corp.` — an admin typing a name should not have to
   write a regex. A `match: [...]` list compiled into a pattern is the better UX; that is an Atlas
   surface decision.
2. **Per prompt or per session.** A customer named once in message 1 attributes one prompt and not
   the next twelve. The state-carries rule says resolve at session scope and carry it forward.
   Unmeasured, and it materially changes coverage.
3. **Whether message text alone is enough.** Tool inputs carry names too —
   `keld-acme-routing-scenarios.pptx` is a filename. Matching over tool inputs as well is a cheap
   extension, and untested.
4. **Whether this is the first of a Python analysis stage.** The term level, the reference levels
   and the digest are all Python with no Go counterpart. If they ship, this pass belongs beside
   them and the sidecar becomes the analysis tier rather than only the inference tier — which is a
   larger architectural commitment than this one pass, and should be decided deliberately.
