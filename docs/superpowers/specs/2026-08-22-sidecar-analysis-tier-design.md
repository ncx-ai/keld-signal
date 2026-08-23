# The sidecar becomes the analysis tier

A decision, and the shape it commits us to.

## The decision

The sidecar stops being only an inference server and becomes the place **all on-device text and
transcript analysis runs**. The Go daemon keeps orchestration, durability, the settings poll, and
— unchanged and more important than before — **enforcement of what leaves the machine**.

## Why

Everything of value measured recently is Python and has **no Go counterpart**: the reference levels
extracted from tool inputs, the named-term level (spaCy), shell command parsing (bashlex), the
digest, the workstream dimensions. Building any of it Go-side means reimplementing measured
behaviour as unvalidated behaviour, with normalisation and parsing semantics living in two places.

The counter-argument that held us Go-side was mostly wrong (see
`2026-08-22-configured-vocabulary-matching-design.md`): a new endpoint does not queue behind
inference, the readiness gate is a daemon scheduling choice, and the FastAPI parent survives worker
recycles. What remains is one accepted trade-off, restated below.

## Process topology

Today: **FastAPI parent** (holds no model, RSS flat regardless of uptime) + **inference worker**
(holds GLiNER2, recycled on RSS ceiling / memory pressure / idle / hung job / crash).

Adding a third: a **long-lived analysis worker**.

    parent (flat, no model)
      ├── inference worker   GLiNER2, ~2.7 GB, recycled aggressively
      └── analysis worker    spaCy + parsers, small and stable, NOT on the model's recycle schedule

Not in the parent: the "parent holds no model" invariant is what keeps its RSS flat, and spaCy plus
a word-frequency table is a model by that definition even at a fraction of the size.

Not in the inference worker: analysis must work **when the model does not**. A machine that has not
finished downloading 1.9 GB of weights should still attribute work, and coupling analysis to a
process that is killed on memory pressure would forfeit exactly that.

Its own process, because its memory profile is the opposite of the inference worker's — small,
stable, no transient activation spikes — so the recycle machinery built for a transformer is the
wrong regime for it.

## Interface: coordinates, not text

The daemon sends **window coordinates** — `(session, transcript path, turn range, promptIds)` — and
the analysis worker reads the transcript itself. Same rule as `spool.Pointer`, and the same rule
the deferred-pass design already states: *never store or ship prompt text when a pointer will do.*

This is a change in kind and should be stated plainly: **the sidecar gains filesystem access to the
transcript directories.** It is the same machine, the same user, and the same bytes the daemon
already reads — so no new exposure — but the sidecar stops being "text in, labels out" and becomes
a reader of on-device state. Anything that assumed the sidecar sees only what it is handed needs
revisiting.

It also removes a real loss. Sending text meant windowing it first, and `window_text`'s 400-char
per-turn clip was measured to drop **58% of entity mentions**, taking `Together.ai` and `Vertex`
from four mentions each to zero. Reading the transcript directly, analysis sees all of it.

## The privacy invariant is unchanged, and load-bearing

**The sidecar never publishes. The daemon decides what leaves.**

This matters more now, not less, because the analysis tier produces rich local structure that must
not be transmitted: the term level contains person names (`Federico`, `Daniel` appeared in one
window). The rule:

* analysis output is **local by default**;
* only what the daemon selects crosses the wire — masked labels, masked spans, and matched
  vocabulary **ids**;
* unmatched named terms never leave the machine.

`enrich/mask.go` stays Go-side and stays the enforcement point. An analysis tier that could publish
would make the invariant unauditable.

## Scope, in phases

**Phase 1 — vocabulary matching.** `POST /match`, no new heavy dependencies, immediate attribution
value. Spec'd separately. This is the phase that pays for itself first: attribution coverage is
36.9% and the model contributes 0.0%.

**Phase 2 — reference levels and named terms.** `POST /analyze` over coordinates, returning the
per-window levels (workspace, branch, artifact, lang, action, exe, service, skill, term, …) that
back the built-in workstreams. Adds spaCy, bashlex, wordfreq. Extraction is essentially
pandas-free today — one timestamp call — so this phase does **not** require the frame machinery.

**Phase 3 — baselines and lift.** `characterize` is where pandas lives, because lift needs history:
a reference's share now against its share across the machine's past. That means **persisted state**,
and it is the expensive phase. The precedent is `lenstat` (streaming mean/variance of prompt
lengths, `~/.keld/state/`); a level-baseline store is the same idea at much larger cardinality.
Without it you still get counts and shares — you lose *distinctiveness*, which is what makes
`Together.ai` outrank `Magenta` and stops `API` taking every slot.

**Phase 4 — digest prose.** Only if a consumer exists. Production today has none: the workstream
dimensions publish as structured values, not sentences. Do not build it speculatively.

## What stays in Go

Ingress and the loopback `/enrich` intake; the bounded queue and spool; transcript **resolution**
for the per-prompt path; the settings poll and vocabulary compilation; publish; masking;
client-events; and `creddetect`, which is Go because it runs on every prompt with no dependency —
a rationale that does **not** generalise to passes meant to sit beside spaCy.

## Costs, stated rather than assumed

**The sidecar-absent machine.** The macOS pkg ships without the sidecar; `onboard.command` fetches
it separately and failure is explicitly non-fatal ("telemetry still works, enrichment jobs spool").
Every analysis capability now depends on a component that is allowed to be missing. Accepted, and
it raises the priority of making that fetch reliable.

**Python regex backtracks.** Org-supplied patterns were harmless under Go's RE2 and are a
denial-of-service risk under Python's `re`. Compile at poll time, run under a wall-clock budget,
degrade the key rather than the job.

**Memory.** A third process against a 4096 MB budget that already holds a ~2.7 GB model. spaCy's
model is 15 MB on disk; resident cost with the runtime and a word-frequency table is **unmeasured**,
and measuring it is a merge gate, not a follow-up. If it does not fit, the model budget moves before
the analysis tier does.

**Packaging.** spaCy freezes cleanly under PyInstaller — verified end to end, official
`hook-spacy` plus `collect_all("en_core_web_sm")` for the model package, `load=0.15s` in the frozen
binary. `wordfreq` carries data files and is **untested** under freeze. bashlex is pure Python.

## Open questions

1. **Does the analysis worker need the frames at all in production?** The study builds parquet
   frames because it re-slices a corpus repeatedly. A daemon analysing one window at a time may
   need only counts, making phase 2 much lighter than the study code suggests.
2. **Per-window or per-session grain.** `lang: Markdown` for a docs commit in a Go repo is right
   about the bytes and wrong about the work. Dominant-per-session probably beats per-window for
   Language and Project. Unmeasured.
3. **What recycles the analysis worker, if anything.** It should not follow the model's schedule,
   but "never" is not a policy either. A crash-only restart is probably right; an RSS ceiling may
   be unnecessary if the profile is genuinely flat, which is a thing to measure rather than assert.
4. **Whether the study code ports or is rewritten.** `refseries.py` is 2600 lines carrying the
   corpus-slicing machinery a daemon does not need. Porting it wholesale would import a study's
   shape into production.
