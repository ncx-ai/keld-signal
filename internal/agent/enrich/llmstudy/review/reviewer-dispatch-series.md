# Series reviewer dispatch prompt

Paste everything below the rule, verbatim, into a fresh reviewer. Replace the three
placeholders first — `{{PACKET_FILE}}`, `{{REVIEWER}}`, `{{VERDICT_FILE}}` — and nothing else.
Each packet is dispatched **twice**, to two reviewers who never see each other's work, because
disagreement between them is one of the numbers this round exists to produce. Do not summarise
the packet for the reviewer, do not tell it how many packets there are, do not tell it anything
about how the packets were made, and do not mention that any other review round exists.

---

You are reading back the record of one person's work session. Read the packet at
`{{PACKET_FILE}}`. It contains two things and nothing else: a record counted on the machine for
that session, and the short statements ("beats") that were written about the session as it went,
in the order shown and numbered by that position.

**There is no transcript.** The beats and the record are all there is, and that is the point of
this review: the person who did this work should be able to come back later and follow what they
were doing from these statements alone.

**Read the timeline as a whole.** You are not judging the beats one at a time. A set of
individually accurate statements can still fail to add up to a story, and a story can read well
while one statement in it is wrong. What is under review is the **sequence**.

## The five dimensions

Give each an explicit verdict — `pass` or `fail`, never a score — and evidence for it.

1. **`followable`** — could a reader reconstruct what happened, and in what order, from this
   alone? Fail it if the reader would be left unable to say what the work was or how it moved.
2. **`continuous`** — do consecutive beats connect, or do they read as disjoint snapshots of
   unrelated work? **Name the breaks** by beat number in `beats`. A break is a place where beat
   N+1 does not follow from beat N: it depends on something that never happened, it repeats work
   already reported as done, or it belongs to different work altogether.
3. **`specifics_present`** — are the product, repo, project and client names there, and are they
   used to **place** the work rather than merely listed? Fail it if the names are absent, and also
   fail it if they appear as decoration while the reader still cannot tell which product or which
   client this was about. Do not demand code identifiers: for a month-end close, "the March close
   for the Meridian entity" is fully specific.
4. **`recognisable_week`** — would the person who did this work recognise their own week in it?
   Fail it if they would say "that is not what I was doing", and fail it if it is technically
   right but so generic that it would fit anybody's week.
5. **`no_false_thread`** — does the series imply a progression, a causality or an arc that the
   beats and the record do not support? This is about the sequence, not about one statement: a
   series that arrives at a conclusion nothing led to, or that implies A caused B when the record
   shows them unconnected, fails here. Also fail it when the order itself asserts a chronology the
   content contradicts.

## Evidence is required for every verdict

Every dimension must carry **one or both** of:

- `quote` — a span copied **verbatim** from the packet, plus `quote_source` saying where it came
  from: `"record"` for the counted block, `"series"` for the beats. Copy it character for
  character, keep it short, and do not paraphrase, ellipsise or tidy it. Whitespace is the only
  thing forgiven.
- `absent` — a list of strings you claim appear **nowhere** in the packet, neither in the record
  nor in any beat. Use this when your verdict rests on something missing, which is the honest way
  to evidence a claim that the series never established something.

Both are checked mechanically, including `quote_source`. A quote that is not in the source you
named, an `absent` entry that turns out to be present, and a verdict with neither are each
recorded against you by name. A `pass` needs evidence exactly as much as a `fail` does.

`beats` is a list of the beat **numbers as shown in this packet** that your verdict rests on. Give
it whenever your verdict is about particular beats — for `continuous` and `no_false_thread` that
is almost always. It is not a substitute for a quote.

## The series defect call

Separately from the five dimensions, say whether the timeline as a whole carries a defect, and if
so which kind:

- `order_shuffle` — the beats are not in the order the work happened; the chronology is wrong even
  though the individual statements may each be true.
- `cross_session_contamination` — one or more beats belong to a different piece of work
  altogether, spliced into this timeline.
- `entity_swap` — a product, repo, project or client is named consistently throughout, but it is
  the wrong name: the record or the rest of the evidence names something else.
- `dropped_middle` — the beats where the work turned are missing, leaving a jump the timeline
  never explains.
- `invented_arc` — the series arrives at a conclusion, sign-off or outcome that nothing in it
  reached.
- `other` — a series-level defect that is none of these; say what it is in `why`.

If the timeline is sound, say so: `"claimed": false`, `"class": "none"`. When you do claim a
defect, list the beat numbers that carry it in `beats` and quote the span that shows it.

## Respond with this JSON only

Write it to `{{VERDICT_FILE}}`. No prose before or after it, no markdown fence.

```json
{
  "packet_id": "the id printed at the top of the packet",
  "reviewer": "{{REVIEWER}}",
  "dimensions": {
    "followable": {
      "verdict": "pass",
      "quote": "an exact span from the packet",
      "quote_source": "series",
      "absent": [],
      "beats": [],
      "why": "one or two sentences"
    },
    "continuous": {
      "verdict": "pass", "quote": "", "quote_source": "", "absent": [], "beats": [], "why": ""
    },
    "specifics_present": {
      "verdict": "pass", "quote": "", "quote_source": "", "absent": [], "beats": [], "why": ""
    },
    "recognisable_week": {
      "verdict": "pass", "quote": "", "quote_source": "", "absent": [], "beats": [], "why": ""
    },
    "no_false_thread": {
      "verdict": "pass", "quote": "", "quote_source": "", "absent": [], "beats": [], "why": ""
    }
  },
  "defect": {
    "claimed": false,
    "class": "none",
    "beats": [],
    "quote": "",
    "quote_source": "",
    "why": ""
  }
}
```

Every dimension key must be present. Do not add keys, do not rename them, and do not review any
packet other than the one you were given.

**What you must not assume.** Some timelines are sound. Others carry a defect. You are not told
which this is, and the proportion is not disclosed. Claiming a break you cannot point to is
exactly as bad as missing one that is there; do not go looking for something to say.
