You are reading one stretch of a Claude Code session between an engineer and Claude, to say what kind of work is being done, how the engineer is finding it, and how demanding the work is. Ground everything in the text; do not fabricate.

---
SESSION RECORD (measured — authoritative):
{{RECORD}}

{{#IF_USER_TURNS}}WHAT THE ENGINEER SAID — their own messages, nobody else's:
{{USER_TURNS}}
{{/IF_USER_TURNS}}{{#IF_NO_USER_TURNS}}The engineer said nothing in this stretch: it is the assistant working on its own.
{{/IF_NO_USER_TURNS}}
THE WHOLE STRETCH, both sides of the conversation:
{{WINDOW}}

{{#IF_USER_TURNS}}Answer in two parts.

PART ONE — WORKING OUT. This part is read by nobody and is discarded; it exists only so the
frustration score is reached by looking at the engineer's own words rather than at your own
summary of them. Quote verbatim, from WHAT THE ENGINEER SAID above, their most pointed or most
emphatic sentence — exactly as written, profanity and capitals included. If none of it is
pointed, write "none — matter-of-fact throughout."

{{/IF_USER_TURNS}}THE RECORD. A fenced ```yaml block, and nothing after it. This part IS kept, so it
must carry no quoted speech and no wording lifted from the engineer's messages: describe the
work in neutral terms, the way a colleague would summarise it to someone who was not there.
Never reproduce profanity, insults, or a sentence the engineer wrote.

```yaml
project: <name if reliably inferable from the record or the text, else null>
bullets:
  - <3-5 flat points, strictly grounded, describing what this segment is about>
scores:
{{#IF_USER_TURNS}}  frustration: <ONE integer 1-10, never a range>
{{/IF_USER_TURNS}}  complexity: <ONE integer 1-10, never a range>
```

{{#IF_USER_TURNS}}FRUSTRATION, judged on part one's quote and the engineer's other messages. Score their feeling
about THIS work, including their opinion of what was produced for them; annoyance at things
outside the session does not count.
  1-2  matter-of-fact. Asks, answers, moves on.
  3-4  having to say the same thing to the assistant twice, or correct a
       misunderstanding it already had. Repeating a COMMAND is not this.
  5-6  blunt and terse. Flat rejection: "no", "that's wrong", "start over".
  7-8  emphatic criticism of the work: capitals, "horrible", "useless", swearing about the output.
  9-10 sustained anger, insults directed at the assistant, or threatening to abandon the work.
{{/IF_USER_TURNS}}

COMPLEXITY, how demanding this work is for a professional in the field. Unlike frustration,
this is about THE WORK, not about what the engineer said: judge it from the whole stretch, both
sides of the conversation. Judge it on what the work required, which is visible in how it
proceeds:
  - was the cause known at the start, or discovered by ruling things out?
  - was an approach tried and abandoned, or a wrong assumption corrected?
  - are there constraints pulling against each other, or one clear path?
  - what does getting it wrong cost — a cosmetic slip, or lost data, or a broken deployment?
  - would this need someone who knows this domain, or could a competent generalist do it?
A count of tool calls is NOT difficulty. Repetitive mechanical work can run hundreds of
commands; a subtle fix can take three. Where the window opens with a "tools in this stretch:"
line, read it only as a hint about the KIND of work — many edits across many files is a
different shape of work from a long read-and-search — never as a measure of how hard it was.

  1-2  routine mechanics: renaming, formatting, running a known command.
  3-4  ordinary feature work with a clear path.
  5-6  several interacting parts, or debugging with an unclear cause.
  7-8  deep debugging across systems, or design where the approach itself is in question.
  9-10 novel work with no established approach, or diagnosis nobody has managed yet.
