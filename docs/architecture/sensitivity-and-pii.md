# The `sensitivity` facet: its two sources, the region tiers and the test-value gate

> **Provenance — split out of `AGENTS.md` on 2026-09-17** (at `67be5f5`), verbatim.
> The material below was last substantively changed in `AGENTS.md` on **2026-08-23**.
> Every measurement here is stated as it was when written; the split re-verified
> **none** of them. Treat an undated figure as "true when measured, unknown now",
> and re-measure before acting on one.

AGENTS.md → *The sensitivity facet* states the rules. This file carries the
measurements behind them — including the precision run that removed `person`
and `address` from the coverage entirely.

**The sensitivity facet's two sources.** Neither classifies; the class is
a rollup over which entity labels were DETECTED (`sensitivityFromEntities`).
**Neither is GLiNER2** — this facet does not touch `ctx.Model` at all, and a
test asserts its output is identical with a Model present and with none.
1. **`creddetect`** — vendored gitleaks credential rules. Pure Go, no model, no
   network, over the FULL prompt text. Always available; the only source that
   needs nothing at all.
2. **The sidecar's `/pii`** (`enrich.PIIScanner` → `sidecar.Client.DetectPIIIn`
   → `app/pii.py`, presidio-analyzer). **Region-scoped pattern recognizers**,
   every one of them checksum- or algorithm-validated: a universal tier
   (`credit_card`, `email`, `phone`, `iban`, `crypto_wallet`) plus one country
   tier per configured region — `us` by default (`ssn`, `aba_routing`, `us_npi`,
   `medical_license`), with `uk es it pl fi kr in au ng th sg` opt-in. It needs
   **no GLiNER2** — and, since the NER came out, **no spaCy model either**: only
   a blank tokenizer, because every recognizer left is a regex plus a check
   algorithm. It never touches the inference single-flight, so it is wired in
   `ml_backend:"deterministic"` too. It returns **offsets only, never the
   matched value** — the Go side resolves and masks from its own copy of the
   text.

   ⚠️ **Region-scoped is a PRECISION decision, not a cost one** (the full set
   measured **+0.5 ms/prompt**, which is nothing). Almost every national-id
   recognizer is a bare digit run plus ONE check digit, so its false-positive
   floor against arbitrary numbers is 1-in-10 or 1-in-11 — a checksum makes a
   false positive unlikely, not impossible — and the shapes **collide across
   countries**. A valid `us_npi` is ten digits starting 1 or 2, which is exactly
   the `uk_nhs` shape, and `uk_nhs` rolls up to `phi`: enabling `uk` inside a
   US-only org manufactures the most severe class out of provider ids. Verified
   collisions are pinned in `sidecar/app/test_pii_regions.py`. Configure with
   `KELD_PII_REGIONS` (comma-separated, `none` = universal only) or
   `pii_regions` in `~/.keld/agent-config.json`; an Atlas org value
   (`Remote.PIIRegions`) overrides both and takes effect on the **next prompt**,
   because the region list rides each `/pii` request rather than the sidecar's
   startup environment. **Atlas does not serve the key yet** — the client seam
   exists so adopting it is a server change alone.

   ⚠️ **`phi` is deliberately narrow, and two assignments were argued rather
   than assumed.** `us_npi` maps to `pii`, NOT `phi`: an NPI is a public CMS
   provider-registry number, so routing it to the most severe class would
   overstate a lookup as a leak. `aba_routing` maps to `pci` while identifying a
   **bank branch from a published directory**, not an account — kept as a
   reliable marker that banking data is present, not as leaked data itself.
   `it_vat_code`, `kr_brn`, `in_gstin`, `au_abn`, `au_acn` and `sg_uen` are
   **business registration numbers**, included only because each of those
   registers also issues to sole traders; they are the weakest members of their
   (opt-in) regions. See `SensitivityFromEntity`'s comment in `labels.go`.

   ⚠️ **Ten recognizers the design asked for DO NOT EXIST in
   presidio-analyzer 2.2.362**, so those regions are absent entirely rather than
   silently empty: there is **no German recognizer of any kind**, no Swedish, no
   South African, no Turkish, and no UK driving licence.

⚠️ **`person` and `address` are NOT DETECTED. The coverage is four types, not
six.** They came from presidio's `SpacyRecognizer`, and on **2,000 real
developer prompts** that recognizer produced **998 of 1,090 spans with zero
confirmed names and zero addresses** — `JSON` ×132, `Docker`, `YAGNI` ×27,
exported Go identifiers, hex colour literals, and a bare `❌` at **0.85**, the
same score a real name gets. Overall precision was **~1%**, and **24% of prompts
published `sensitivity: pii`**. No threshold separates a common noun from a name,
so the recognizer was removed rather than tuned; the same measurement rerun
against the current detector publishes `pii` on **0.45%** of prompts at 100%
precision, and `pci`/`phi` never fire. Both dropped types mapped to `pii`, the
LOWEST severity class, so nothing severe was lost — but free-form personal names
in prose are now **undetectable**, and that is a real narrowing, not a tuning.
`SensitivityFromEntity` still knows both names so a future detector needs no
schema change. Full before/after:
`~/keld/refseries-context/pii-precision/RESULTS.md` (reproduce with
`scripts/pii_precision.py --port N --regions us|all`). The same 2,000-prompt run
against the **widened** detector with **every region enabled** produced **zero**
spans of any new type — the only raw presidio output was three readings of one
digit run inside a URL path, all removed by the fragment gate.
There is deliberately **no third, GLiNER2 source**. `/entities` over a
`SensitiveEntityLabels` vocabulary used to be one, and it was redundant:
presidio produced every mapped type the GLiNER2 NER did (`person`/`address` then
came from its `SpacyRecognizer`, which is spaCy, not GLiNER2 — both have since
been dropped as measured noise, see above), so the NER added no type of its
own while needing a corroboration rule to keep its confident, perfectly-shaped
documentation constants off the wire. That rule, the label vocabulary, and the
call are all gone. Re-admitting a model here is a deliberate decision with its
own evidence, not a rewiring.

⚠️ **The published-test-value gate lives in ONE place: `sidecar/app/wellknown.py`.**
`4111 1111 1111 1111` passes Luhn, `123-45-6789` is the textbook SSN,
`user@example.com` is RFC 2606 — developer transcripts are saturated with them,
and ungated the facet reports `pci`/`phi` continuously and is worse than absent.
The gate is applied **at source**, inside `scan()` before it answers, so nothing
it suppresses can reach Go by another route — there is no second detector to
route around it. Two rules there are **measured, not structural**: a no-reply /
machine local-part denylist (66 of 78 `email` spans in the corpus were one
`noreply@` address quoted out of a `Co-Authored-By` git trailer — an unattended
sink is not personal data), and a rejection of Go-module-version paths
(`host.tld/pkg@v1.23.4` parses as local-part-at-domain and scored 1.0). A
companion **numeric-fragment gate** lives in `pii.py`, not here, because it needs
the surrounding text: a match preceded by `[0-9.,:/-]` is the tail of a longer
token, which is how 13 digits after a decimal point published **`pci` at 1.0**.
It is asymmetric on purpose — the right-hand side rejects only a continuation
(digit, or `.,-` then a digit), because "…4470, then email" is prose and the
whitespace-token rule would have cost real detections. With **no scan available
the four personal-data types simply have no source**; only credentials remain, and the loss is declared (see
`facets_degraded` below) rather than papered over. Do not add a Go copy of the
list, and do not add a second detector that would need one.

**`facets_degraded` for sensitivity turns on the SCAN.** The scan is the sole
source for every personal-data type, so a whole scan leaves nothing uncovered
and a Model's absence costs nothing.
Scan absent / failed / **`truncated`** ⇒ degraded, unless the answer already
reached the ceiling of the severity order (`phi`), which no missing evidence
could raise.
