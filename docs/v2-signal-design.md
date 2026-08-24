# Keld Signal v2 — how it works

For a developer new to this system. It explains what runs, when each thing happens, what is
actually detected, and *why* the design looks the way it does. Reference material lives in
`AGENTS.md`; this is the orientation.

---

## 1. The one invariant, first

**Raw prompt text is read on the engineer's machine and never leaves it.**

Everything else is a consequence of that. Keld wants to tell an organisation what its AI-assisted
engineering work consists of — which projects, which languages, whether sensitive data is being
pasted into prompts — without shipping the prompts anywhere. So the analysis happens locally and
only *derived, masked* signal is published.

Concretely, what crosses to Atlas is: label values from closed vocabularies, masked spans (at most
the last four characters of a detected secret), counts, and shares. What never crosses: prompt text,
assistant text, file contents, and the free-text proper nouns extracted during analysis.

---

## 2. Two lanes that do not touch each other

    TELEMETRY   the coding tool -> Atlas directly.       usage, tokens, cost.
                Configured by `keld signal setup`, which writes an OTLP block into
                each tool's own config. No Keld process is involved at runtime.

    ENRICHMENT  the coding tool -> keld-agent -> Atlas.  what the work WAS.
                A local daemon reads transcripts on disk, derives signal, masks, publishes.

They are joined at Atlas by a correlation id: `enrichment.corr_id == tool_event.prompt_id`. That
join is why prompt ids matter so much later in this document — an enrichment whose id does not match
the telemetry's is orphaned, and silently.

---

## 3. Three processes

    keld              the CLI. Detects installed tools, edits their configs, installs the
                      service, authenticates. Single static Go binary, no runtime deps.

    keld-agent        the daemon. Owns capture, the job queue, the enrichment pipeline,
                      masking, and publishing. Go.

    keld-agent-       the analysis + enrichment SERVICE. Python, FastAPI, loopback only.
    sidecar           Owns the reference store and everything derived from transcript
                      content. GLiNER2 is one capability it CAN load; it is not its identity
                      and is not required for it to run.

The sidecar distinction matters and is easy to get wrong: it used to be thought of as "the GLiNER2
sidecar". It is not — it serves the store, `/analyze`, `/pii` and `/ingest` with no model at all.
But **the model is still used**: seven classification facets call it (see §6d). What v2 changed is
that the *service* no longer depends on it, and the two facets that matter most for privacy —
sensitivity and workstreams — no longer touch it.

---

## 4. What happens, in order, when an engineer submits a prompt

**Two triggers feed one queue.** A *hook* (which the tool invokes) and a *transcript watcher*
(which tails the JSONL files tools write to disk). Either produces a **pointer** — transcript path
plus prompt id, **never text** — and the queue de-duplicates the overlap.

    1  TRIGGER      hook fires, or the watcher notices a transcript grew.
                    A pointer is enqueued. If the daemon is down, the hook writes the
                    pointer to an on-disk spool and the daemon drains it on startup.

    2  INGEST       the watcher separately tells the sidecar "this transcript advanced".
       (parallel)   The sidecar parses ONLY the appended bytes from a stored offset and
                    updates its store. This is off the request path deliberately -- see §5.

    3  RESOLVE      the daemon reads the prompt text from the transcript, locally.

    4  ENRICH       a staged pipeline of independent passes runs over the text and over
                    the window of work around it. Each pass has its own 30s deadline.

    5  MASK         enforced Go-side. A detected secret becomes "…4483"; an email keeps
                    only its domain.

    6  PUBLISH      POST to Atlas. Durable: batched, retried, spooled when unreachable.
                    Atlas de-duplicates on a dedup key, so a late retry is harmless.

**Deadlines are per pass, not per job.** This is load-bearing. A job may issue eight or nine
separate analyses; a job-wide budget meant one slow pass discarded every pass that had already
succeeded, and the whole job was retried — the same work redone and re-discarded until the attempt
budget ran out. Bounded per pass, a slow pass costs exactly one facet: the others commit and the
profile publishes as `pipeline_status: "partial"`. Progress is monotonic.

---

## 5. The reference store — why analysis is a query, not a parse

The naive design re-reads the transcript for every prompt. Measured: a 60-minute window of work
contains a mean of **3.8 prompts** (max 20), so an hour was characterised ~4 times, each costing
**0.79s** on a 90 MB transcript. Nothing persisted, so nothing could ever be compared over time.

v2 ingests each transcript **once, incrementally**, into SQLite at `~/.keld/state/refseries.db`.

    event      the raw facts: (session, timestamp, level, ref, count)
    bin        5-minute rollups of those facts
    ingest     per-file byte offset, size, head hash, and a WATERMARK

Result: **0.79s -> 0.0023s.** Same answer — proven equal on 90 real prompts across 30 transcripts,
0 differing.

Three properties a newcomer will otherwise trip over:

**A tail parse is not automatically a full parse.** Two analyses are retroactive: path
reconciliation resolves prose-mentioned files against accumulated context, and workspace resolution
depends on evidence scattered through the file. Ingesting naively in chunks differed from one pass
by thousands of rows. It is equal *because* the reconciliation state is persisted and recomputed
whole per batch, and a changed workspace answer forces a reparse. Verified across 284 transcripts.

**Never answer past the watermark.** If a window ends after what has been fully ingested, the
service refuses (`503`) and the job retries. Serving a window missing its last few minutes would
publish a confidently wrong attribution, which is worse than publishing nothing.

**Bins are not a fallback for events.** Digests read `event`, not `bin`, because reconciliation must
be re-scoped per window. So pruning old events does not *narrow* a window — it breaks it. Retention
is therefore a 400-day horizon plus a refusal (`410`) rather than a silent shortening.

---

## 6. What is actually detected

### 6a. Workstream dimensions — *what this hour of work was about*

Derived from **tool-call inputs only** (which files were read, which commands ran, which branch was
checked out) — never from model output, never from prose. A closed vocabulary maps each observation
to a level, and each level is rolled up over the window:

    project · branch · model · output_type · language · workflow · tooling

A dimension is **attributed** only if one value holds >= 50% of the window's evidence **and** there
are at least 5 observations. Below that it says so rather than guessing. The evidence floor is
derived, not chosen: take the 50% share as the null hypothesis and unanimity over *n* observations
has probability `0.5**n`, which first clears 5% at n = 5.

Note that floor is about **sample size, not duration** — a shorter window does not get a smaller
floor, because that would make an attribution's significance depend on window length while the
published `value` and `share` look identical.

### 6b. Sensitivity — *was concrete sensitive data pasted into a prompt*

This detects **leaked data, not topic**. A prompt discussing healthcare is not `phi`; a prompt
containing an SSN is. Two deterministic sources, no model:

    credentials   gitleaks rule set, pure Go. API keys, tokens, private keys, JWTs.
                  Plus structural detectors where a parser beats a pattern: a URI
                  userinfo password is extracted with `net/url` and is a credential
                  BY DEFINITION, not by guess.

    personal      Microsoft Presidio, in the sidecar. 25 CHECKSUM-VALIDATED recognizers
                  in three tiers: universal (card via Luhn, email, phone via
                  libphonenumber, IBAN, crypto), a default `us` set (SSN, ABA routing,
                  NPI, DEA licence), and 11 opt-in regions via `KELD_PII_REGIONS`.

Detected entity types roll up to the highest severity present: `ssn`→`phi`,
`credit_card`→`pci`, credential→`secrets`, other identifier→`pii`.

**The gate that makes this usable.** Developer transcripts are full of documentation values.
`4111 1111 1111 1111` passes Luhn; `123-45-6789` is the textbook SSN; `user@example.com` is
RFC-reserved. Without suppressing them, measured precision on real prompts was **~1%, with 24% of
all prompts reporting `pii`**. With the gate, and after dropping free-text name/address detection
(858 spans, approximately zero real names — `JSON` alone accounted for 132), precision is **10/10**
and the false-`pii` rate fell from 240 to 4.5 per thousand prompts.

Two things follow that are easy to misread as gaps: **person and address are not detected** (spaCy's
NER is measurably unfit for this on code-heavy text), and **`proprietary` can never be emitted**
because no detector maps to it.

### 6c. Dynamics — *how the work is changing*

New in v2, and only possible because the store persists history. Each analysis compares a **recent
slice** against a **preceding baseline** and reports, per dimension:

    turnover              share of slice evidence in values absent from the baseline
    decay                 values leaving
    concentration_shift   the dominant value's slice share minus its baseline share
    reading               a STATED conclusion from a closed 7-value vocabulary

The `reading` exists because of a measurement: a 16 KB window document scored **worse than nothing**
for answering questions about the work (-3.3), while a short digest carrying the *same facts* scored
**+36.7** — because the digest stated a conclusion and the document left the reader to divide two
numbers. So the conclusion ships beside the numbers, and the unlabelled remainder does not ship
at all.

**Where the slice boundary goes was decided by experiment, not preference.** A fixed 15-minute slice
was compared against an EWMA of novelty and against `river`'s ADWIN, PageHinkley and KSWIN, scored
against deterministic ground truth (a real transition = the dominant branch changing). The EWMA won
by **+74.6 precision / +27.0 recall**, and `river` does not ship — its defaults structurally cannot
fire inside a 60-observation budget.

The control is what makes that believable: relocating every transition to a random position collapses
the EWMA from 86.4% to 24.1%, while every *fixed* sizer barely moves (11.9 -> 10.9) — a constant
offset was never a detector — and ADWIN scores *better* on shuffled truth than real, i.e. carries no
signal at all.

Half the candidate dynamics were then **dropped on their distributions**: `project` was identically
zero across all 2,180 comparable windows (a transcript is scoped to one project directory), and
`model` reported a change **0 times in 2,702 windows**.

### 6d. Classification facets — model-backed, and unmeasured since

Seven facets classify the prompt text against a closed vocabulary using GLiNER2 in the sidecar:

    task_type · domain · activity_type · personal · function_guess · speech_act · subcategory

These are the reason `ml_backend:"auto"` still provisions a ~1.8 GB model. They are also the part of
the system with the weakest evidence behind it — see §9. Classifiers score against readable label
DESCRIPTIONS, not bare ids, because the bi-encoder keys on token overlap; the label wording is
load-bearing.

In `ml_backend:"deterministic"` these seven do not run at all, and the profile publishes the
model-free facets with the others named in `facets_skipped`.

---

## 7. Three operating modes

`ml_backend`, local and startup-only:

    "auto" (default)   everything above, and the GLiNER2 model loads lazily when a
                       model-backed facet is requested. CORRECTION (measured): the
                       Go pipeline still requests one -- 6 inferences per prompt
                       (task_type, domain, activity_type, personal, function_guess,
                       subcategory, speech_act). See
                       enrich.TestBuiltInPipelineStillDemandsAModel, which fails the
                       day that reaches zero. The weights are fetched ON DEMAND, by
                       the first attempted inference, never at daemon start.
    "deterministic"    enrichment runs; the model is never asked for, so never loads.
                       The service still starts -- it is needed for analysis and PII.
    "off"              no enrichment at all; the intake endpoint accepts and discards.

Enrichment never silently degrades. If the service cannot answer, jobs queue and spool until it can
— they are never handed to a lower-fidelity substitute. The one exception is deliberate: if no
sidecar binary is *installed*, that is "this capability does not exist here" rather than "not ready
yet", so enrichment runs without window analysis instead of wedging forever.

---

## 8. The method — why the code reads the way it does

Nearly every constant in this system has a measurement attached, and the comments record it. That is
not decoration; it is because the failures here are almost never crashes.

**The characteristic failure is a plausible wrong number.** Roughly twenty defects surfaced during
v2 development and essentially none was caught by reading an aggregate table: a folded vocabulary
flattering a half-life, `pdf 54%` for a slide deck built from ten `pdftoppm` calls, a model answering
`2659` to "which ticket?" (it was the window's own event count). The habits that follow:

- **Render specific identifiers, not aggregates.** Name the session, show the matched value.
- **Measure on the real corpus, not the fixture.** Every unmeasured precision claim in v2 was wrong.
  A URI-password detector's first version produced 43 findings across 42 prompts and *one* distinct
  value.
- **Fixtures built from published examples measure the gate, not the detector.** This bit three
  times. Recall read as 0.14 and 0.42 when the true numbers were 1.00 and 0.71.
- **Inject a mutation and confirm the suite bites.** Vacuously-passing tests were found in nearly
  every work unit — including assertions about masking that ran over empty span lists.
- **Pre-register the decision rule** before an experiment, and report the null result when it comes.
  `river` was rejected this way; so was GLiNER2 as a classifier (37.8% against a 67.8% majority
  baseline — 30 points *worse* than a constant guess, and degenerate at 91% one class).

---

## 9. Deliberately not done

- **GLiNER2 is unused by the WORKSTREAM and SENSITIVITY facets** — measured unfit as a classifier of
  activity and unnecessary for detection. It is **not** unused by the pipeline: the semantic facets
  (§ the "auto" note above) still issue 6 inferences per prompt, so `auto` genuinely needs the
  weights. What changed is *when* they arrive: provisioning is triggered by the first attempted
  inference rather than by daemon start, so a machine that never enriches never downloads them.
- **Person and address detection**, per §6b.
- **Enrichment cadence is per prompt.** A tick-based cadence (characterise every N minutes and
  attach to all activity in the slice) is specified but deferred — the store removed its efficiency
  justification, leaving a product decision.
- **Codex gets telemetry but no enrichment.** Both capture paths are dead: the watcher requires an
  `ordinal` field real Codex transcripts do not emit (0 of 26 prompts captured across 14 sessions),
  and its hook events are not prompt submissions. The fix is known but the prompt id must match what
  Codex's own telemetry emits or the Atlas join orphans every row — unresolved.
- **Cowork** is supported in code but its transcripts moved inside a VM the host cannot read.
