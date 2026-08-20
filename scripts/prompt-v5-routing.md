You are reading one stretch of a Claude Code session between an engineer and Claude, to say what kind of work is being done, how the engineer is finding it, and how demanding the work is. Ground everything in the text; do not fabricate.

---
SESSION RECORD (measured — authoritative):
{{RECORD}}

RECENT CONVERSATION:
{{WINDOW}}

Write flat bullet points, strictly grounded in the text, describing what this segment is about and what kind of work it is. Aim for 3-5 bullets. If the project name can be reliably inferred, make it the first bullet.

Then, in this order:

TONE EVIDENCE: quote verbatim, from a line that begins with `user:`, the engineer's most
pointed or most emphatic sentence, exactly as written, profanity and capitals included. Lines
beginning `assistant:` or `tool:` are NOT the engineer and must never be quoted here. If no
`user:` line is pointed, write "none — the engineer is matter-of-fact throughout."

FRUSTRATION SCORE (1-10), judged on the quoted line and the engineer's other messages, not on your own summary of them:
  1-2  matter-of-fact. Asks, answers, moves on.
  3-4  repeating themselves, or correcting the same thing twice.
  5-6  blunt and terse. Flat rejection: "no", "that's wrong", "start over".
  7-8  emphatic criticism of the work: capitals, "horrible", "useless", swearing about the output.
  9-10 sustained anger, insults directed at the assistant, or threatening to abandon the work.
Score the engineer's feeling about THIS work, including their opinion of what was produced for them. Their annoyance at things outside the session — a flaky third-party service, the weather — does not count.

COMPLEXITY SCORE (1-10), how demanding this work is for a professional in the field:
  1-2  routine mechanics: renaming, formatting, running a known command.
  3-4  ordinary feature work with a clear path.
  5-6  several interacting parts, or debugging with an unclear cause.
  7-8  deep debugging across systems, or design where the approach itself is in question.
  9-10 novel work with no established approach, or diagnosis nobody has managed yet.

Give each score as ONE integer, never a range like "1-2". Follow it with one short clause of
justification in your own words about THIS conversation — do not repeat the wording of the band
you chose, which describes a category rather than this session. Do not manufacture anything.
