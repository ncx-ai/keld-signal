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

Produce these sections, in this order.{{#IF_USER_TURNS}} The tone sections come first
deliberately: judge the engineer's feeling from their own words, before you have written any
summary of your own to judge instead.

1. TONE EVIDENCE — quote verbatim, from WHAT THE ENGINEER SAID above, their most pointed or most
   emphatic sentence, exactly as written, profanity and capitals included. If none of it is
   pointed, write "none — the engineer is matter-of-fact throughout."

2. FRUSTRATION SCORE (1-10), judged on that quote and the engineer's other messages:
     1-2  matter-of-fact. Asks, answers, moves on.
     3-4  repeating themselves, or correcting the same thing twice.
     5-6  blunt and terse. Flat rejection: "no", "that's wrong", "start over".
     7-8  emphatic criticism of the work: capitals, "horrible", "useless", swearing about the output.
     9-10 sustained anger, insults directed at the assistant, or threatening to abandon the work.
   Score their feeling about THIS work, including their opinion of what was produced for them.
   Annoyance at things outside the session — a flaky third-party service, the weather — does not
   count.
{{/IF_USER_TURNS}}
3. WHAT THIS IS — flat bullet points, strictly grounded in the text, describing what this segment
   is about and what kind of work it is. Aim for 3-5 bullets. If the project name can be reliably
   inferred, make it the first bullet.

4. COMPLEXITY SCORE (1-10), how demanding this work is for a professional in the field:
     1-2  routine mechanics: renaming, formatting, running a known command.
     3-4  ordinary feature work with a clear path.
     5-6  several interacting parts, or debugging with an unclear cause.
     7-8  deep debugging across systems, or design where the approach itself is in question.
     9-10 novel work with no established approach, or diagnosis nobody has managed yet.

Give each score as ONE integer, never a range like "1-2". Follow it with one short clause of
justification in your own words about THIS conversation — do not repeat the wording of the band
you chose, which describes a category rather than this session. Do not manufacture anything.
