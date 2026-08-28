# Comprehensive PII detection via Presidio — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace hand-rolled PII regexes with Microsoft Presidio (86 predefined recognizers) running in the sidecar, so `sensitivity` detects concrete leaked data comprehensively and without GLiNER2.

**Architecture:** Presidio's pattern recognizers run in the sidecar behind a new `/pii` endpoint that needs no GLiNER2 and does not use the inference single-flight. The Go `SensitivityExtractor` calls it beside the existing gitleaks credential layer. The Go hand-rolled detector from `d8ef9ab` is reverted; its well-known/test-value gate is ported to Python, because Presidio does not have one.

**Tech Stack:** Python 3.12 sidecar (FastAPI, standalone test scripts — never pytest), `presidio-analyzer`, Go daemon.

## Global Constraints

- **Precision is the requirement, not recall.** A false `phi` on an org dashboard is worse than a miss.
- **The test/example-value gate is mandatory.** `4111 1111 1111 1111` passes Luhn and Presidio WILL flag it; `123-45-6789` is the textbook SSN; `user@example.com` is RFC 2606 reserved. Without the gate every developer transcript reports `pci`/`phi` continuously and the facet is worse than absent.
- Detected spans use the EXISTING entity vocabulary so `labels.go`'s rollup (`ssn`→phi, `credit_card`→pci, credentials→secrets, `email`/`phone`/`person`/`address`→pii) consumes them unchanged. Do not add vocabulary values without an explicit `SchemaVersion` decision.
- **Must not require GLiNER2** and **must not use the inference single-flight `_dispatch`** — mirror how `/analyze` stays off it.
- Raw prompt text is read on-device and MUST NEVER be transmitted. `/pii` takes text over loopback (like `/classify`, `/extract`, `/entities`); masking stays enforced Go-side before publish.
- **numpy and torch must not move.** The venv has numpy 2.5.0 + torch 2.12.1+cu130; a pip dry-run proposed numpy 2.4.6. A silent downgrade breaks GLiNER2 at runtime, not at install.
- Sidecar tests are standalone scripts with a `__main__` runner, never pytest, run with `~/.keld/sidecar-venv/bin/python`.
- Go gates: `go build ./...`, `go test ./...`, `gofmt -l`.
- Fixtures WHOLLY SYNTHETIC — a fixture containing real PII is itself a defect in this repo.
- Never `git add -A`/`checkout`/`stash`/`clean`. Uncommitted user work must survive: `.gitignore`, `internal/agent/daemon/custom_passes*.go`, a `daemon.go` hunk near lines 761-772, `scripts/context_value.py`, `scripts/prompt-v9.md`, untracked docs under `docs/superpowers/`.

---

### Task 1: Presidio in the sidecar, with the test-value gate

**Files:** Create `sidecar/app/pii.py`, `sidecar/app/wellknown.py`, `sidecar/app/test_pii.py`, `sidecar/app/test_wellknown.py`. Modify `sidecar/requirements.txt`.

**Produces:** `scan(text) -> list[dict]` returning `{"type","start","end","score"}` with `type` in the existing vocabulary; `is_well_known(value, kind) -> bool`.

- [ ] **Step 1: Prove the dependency is safe BEFORE adopting it.** Record `numpy.__version__` and `torch.__version__`, install `presidio-analyzer` pinned into the sidecar venv, re-record both, and confirm `import torch` and a real GLiNER2 `/classify` still work. If numpy moves, STOP and report — do not proceed by pinning around it silently.
- [ ] **Step 2: Write the failing tests.** Positive cases per entity type using structurally valid but NOT well-known values (construct them; explain how). Negative cases: every standard test card brand, `123-45-6789`, `078-05-1120`, RFC 2606/6761 reserved domains, version strings, ports, git SHAs, and long digit runs that are order ids.
- [ ] **Step 3: Run them and watch them fail** — `cd sidecar && PYTHONPATH=. ~/.keld/sidecar-venv/bin/python app/test_pii.py`
- [ ] **Step 4: Implement.** Map Presidio entity names onto the existing vocabulary (`US_SSN`→`ssn`, `CREDIT_CARD`→`credit_card`, `EMAIL_ADDRESS`→`email`, `PHONE_NUMBER`→`phone`, `PERSON`→`person`, `LOCATION`→`address`, and decide what to do with the rest — several map to `pii` and several have no home). **Decide which recognizers to ENABLE**: all 86 is not obviously right; `Date`, `Url`, `Ip`, `MacAddress` are not sensitive data in a developer transcript and would fire constantly. Justify the enabled set. Apply the gate to every result.
- [ ] **Step 5: Run.** All sidecar suites pass.
- [ ] **Step 6: Commit.**

---

### Task 2: The `/pii` endpoint

**Files:** Modify `sidecar/app/main.py`; test in `sidecar/app/test_main.py`.

- [ ] **Step 1: Write the failing test** — `/pii` returns spans for a synthetic SSN, and the GLiNER2 worker stays `down` across the call (the load-bearing property; assert it as `test_the_service_answers_analyze_with_gliner2_never_loaded` does).
- [ ] **Step 2: Run and watch it fail.**
- [ ] **Step 3: Implement.** Off the event loop via the executor, like `/analyze`. Not through `_dispatch`. No worker.
- [ ] **Step 4: Run every sidecar suite.**
- [ ] **Step 5: Commit.**

---

### Task 3: Wire Go to it, and revert the hand-rolled detector

**Files:** Modify `internal/agent/enrich/sidecar/client.go`, `internal/agent/enrich/extractors.go`, `internal/agent/enrich/piidetect_pass.go`; delete `internal/agent/enrich/piidetect/`.

- [ ] **Step 1: Write the failing test** — `SensitivityExtractor` with a fake PII backend returning an `ssn` span rolls up to `"phi"`; credential detection still works with no backend at all.
- [ ] **Step 2: Run and watch it fail.**
- [ ] **Step 3: Implement.** Add the client method; call it beside `CredentialSpans`; union and dedup with the NER's spans when a model is present. Revert `d8ef9ab`'s Go detector (`piidetect/`), keeping gitleaks credentials untouched. **When the service is unavailable, PII detection is genuinely absent** — that must show in `facets_degraded` (added in `e0543e9`), not be silently dropped. Re-examine `SensitivityExtractor.Degraded`: it was narrowed on the assumption that ssn/credit_card/email were covered in-process, which stops being true.
- [ ] **Step 4: Run** `go test ./...`, `go build ./...`, `gofmt -l internal/agent/enrich/`.
- [ ] **Step 5: Commit**, with a `SchemaVersion` decision and its justification.

---

### Task 4: Measure precision on the real corpus before trusting it

**Files:** Create `scripts/pii_precision.py`. Durable output to `~/keld/refseries-context/pii-precision/`.

Precision is the stated requirement, so it must be measured, not assumed — this project's standing rule is that roughly twenty defects surfaced as plausible wrong numbers that no aggregate table caught.

- [ ] **Step 1:** Run `/pii` over a sample of real transcript prompts from `~/keld/refseries-context/frozen-corpus/`.
- [ ] **Step 2:** Report the rate of each entity type per 1,000 prompts, and **render the actual matched strings** (masked to first/last 2 chars) for manual inspection — not just counts. A `phi` rate above a few per thousand in a developer corpus is a false-positive signal, not a discovery.
- [ ] **Step 3:** List every distinct false positive found and what gate change would kill it.
- [ ] **Step 4:** Write `RESULTS.md`; commit the script.

## Not in this plan

- Moving analysis/spaCy into a recycled worker child (deferred separately).
- The incremental reference-series store.
- Presidio's own NER recognizers as a GLiNER2 replacement for `person`/`address` — measure first.
