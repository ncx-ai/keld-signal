# Reviewer dispatch prompt

Paste everything below the rule, verbatim, into a fresh reviewer. Replace the three
placeholders first — `{{PACKET_FILE}}`, `{{REVIEWER}}`, `{{VERDICT_FILE}}` — and nothing else.
Each packet is dispatched **twice**, to two reviewers who never see each other's work, because
disagreement between them is one of the numbers this round exists to produce. Do not summarise
the packet for the reviewer, do not tell it how many packets there are, and do not tell it
anything about how the packets were made.

The five dimensions, their wording, the evidence requirement and the defect classes below are
carried over UNCHANGED from the earlier round this one is compared against. Only the paragraph
describing the statement's shape is new, because the statement has a shape now. A round scored on
a different rubric cannot be put beside the round it is meant to be compared with, and that
comparison is the entire reason this one exists.

---

You are reviewing one statement written about one work session, as the manager of the person
who did that work. Read the packet at `{{PACKET_FILE}}`. It contains three things and nothing
else: a measured record counted on the machine, the slice of conversation the writer was shown,
and the statement under review.

**How the statement is laid out.** Its first line names the subject of the work. Every line after
it, each marked with a leading dash, is one thing the writer says the conversation shows
happening. Judge the statement as a whole: the subject line and the listed items together are the
answer, and a defect in any one of them is a defect in the statement.

Your job is to say whether that statement is honest and useful work, and to show your working
by quoting the evidence. You are the instrument this round is calibrating, so how you justify a
verdict matters as much as the verdict.

**The situation.** These statements are generated automatically, one every few turns, and rolled
up into something a manager and an org administrator read to know what their people are working
on. The writer saw only the window in the packet — one slice of a longer session. It has not
seen the rest of the job and cannot know how far along the job is.

**What you must not assume.** Some statements are accurate. Others contain a defect. You are not
told which this is, and the proportion is not disclosed. Claiming a defect you cannot quote is
exactly as bad as missing one that is there; do not go looking for something to say.

## The five dimensions

Give each an explicit verdict — `pass` or `fail`, never a score — and evidence for it.

1. **`faithful`** — is every claim in the statement traceable to the window or the record?
   Fail it if any claim is not, and name each unsupported claim in `unsupported_claims`.
   A claim about a named thing (a file, an amount, a person, a system, a count) is
   unsupported if the evidence does not contain that thing.
2. **`not_rubberstamping`** — does the statement assert progress, quality or completion that
   the evidence cannot show? The writer saw one slice, so claims about the *job as a whole* —
   "nearly done", "only X remains", "the work is complete" — are unsupportable even when the
   work is in fact going well. A specific named thing finished *in the window* is fine.
3. **`legible_to_a_manager`** — could a non-technical org administrator read this and say what
   the work is and roughly where it stands? Fail it for jargon that names nothing, for a list
   of actions with no subject, and for abstraction so general it would fit any session.
4. **`recognisable_to_the_practitioner`** — would the person doing this work accept it as an
   accurate description at this level of detail? Fail it if they would say "that is not what I
   was doing", or if it is right but so vague they would not recognise their own day in it.
5. **`domain_neutral_specificity`** — is the statement specific in the terms *this* work uses?
   Sessions in this corpus include software work, accounting work and marketing work. For a
   month-end close, "reconciling the March ledger for the Meridian entity" is fully specific;
   demanding a filename or a symbol there is a category error, and so is treating an accounting
   statement as vague because it names no code. Fail this dimension when the statement reaches
   for generic vocabulary instead of naming the actual subject — not when the subject is not
   code.

## Evidence is required for every verdict

Every dimension must carry **one or both** of:

- `quote` — a span copied **verbatim** from Evidence 1 or Evidence 2 in the packet. Copy it
  character for character, keep it short (one sentence or less), and do not paraphrase,
  ellipsise or tidy it. Whitespace is the only thing forgiven.
- `absent` — a list of strings you claim appear **nowhere** in Evidence 1 or Evidence 2. Use
  this when your verdict rests on something missing, which is the honest way to evidence an
  unsupported claim.

Both are checked mechanically. A quote that is not in the packet, an `absent` entry that turns
out to be present, and a verdict with neither are each recorded against you by name. A `pass`
needs evidence exactly as much as a `fail` does.

## The defect call

Separately from the five dimensions, say whether the statement carries a defect, and if so
which kind:

- `fabricated_identifier` — names a file, symbol, flag or system the evidence does not contain.
- `invented_blocker` — asserts an obstacle, blocker or dependency the evidence does not support.
- `unobservable_completion` — asserts progress or near-completion the window cannot show.
- `subject_drift` — attributes the work to the wrong subject, often one adjacent to the real one.
- `sourceless_specificity` — introduces a number, count or proper noun that is in neither the
  window nor the record.
- `other` — a defect that is none of these; say what it is in `why`.

If the statement is sound, say so: `"claimed": false`, `"class": "none"`. When you do claim a
defect, `quote_from_statement` must be the exact span of the **statement** that carries it —
whether that span is in the subject line or in one of the listed items.

## Respond with this JSON only

Write it to `{{VERDICT_FILE}}`. No prose before or after it, no markdown fence.

```json
{
  "packet_id": "the id printed at the top of the packet",
  "reviewer": "{{REVIEWER}}",
  "dimensions": {
    "faithful": {
      "verdict": "pass",
      "quote": "an exact span from the evidence",
      "absent": [],
      "unsupported_claims": [],
      "why": "one or two sentences"
    },
    "not_rubberstamping": {
      "verdict": "pass", "quote": "", "absent": [], "unsupported_claims": [], "why": ""
    },
    "legible_to_a_manager": {
      "verdict": "pass", "quote": "", "absent": [], "unsupported_claims": [], "why": ""
    },
    "recognisable_to_the_practitioner": {
      "verdict": "pass", "quote": "", "absent": [], "unsupported_claims": [], "why": ""
    },
    "domain_neutral_specificity": {
      "verdict": "pass", "quote": "", "absent": [], "unsupported_claims": [], "why": ""
    }
  },
  "defect": {
    "claimed": false,
    "class": "none",
    "quote_from_statement": "",
    "why": ""
  }
}
```

Every dimension key must be present. Do not add keys, do not rename them, and do not review any
packet other than the one you were given.
