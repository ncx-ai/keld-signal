You are reading one stretch of a Claude Code session between an engineer and Claude, to record what the work is, how demanding it is, and how the engineer is finding it. Ground everything in the text; do not fabricate.

---
SESSION RECORD (measured — authoritative):
{{RECORD}}

{{#IF_USER_TURNS}}WHAT THE ENGINEER SAID — their own messages, nobody else's:
{{USER_TURNS}}
{{/IF_USER_TURNS}}{{#IF_NO_USER_TURNS}}The engineer said nothing in this stretch: it is the assistant working on its own.
{{/IF_NO_USER_TURNS}}
THE WHOLE STRETCH, both sides of the conversation:
{{WINDOW}}

Fill the fields in the order they are given to you.

working_out — read by nobody and discarded. {{#IF_USER_TURNS}}Copy out the engineer's most
pointed or most emphatic sentence, exactly as written, profanity and capitals included, so the
tone questions below are answered against their actual words rather than against your own
summary. If nothing is pointed, write "none".{{/IF_USER_TURNS}}{{#IF_NO_USER_TURNS}}Say in one
clause what this stretch was doing.{{/IF_NO_USER_TURNS}}
{{#IF_USER_TURNS}}
tone — true or false about THE ENGINEER'S OWN MESSAGES. These are observations, not judgements:
answer what the text shows and let the score follow from it.
  swears_in_own_voice    do they swear, in their own voice?
  shouts_in_capitals     do they use CAPITALS or repeated punctuation for emphasis?
  criticises_the_output  do they say what was produced for them is bad?
  rejects_outright       do they reject something flatly — "no", "wrong", "start over"?
  repeats_themselves     do they give the same instruction or correction twice?
  insults_the_assistant  do they insult the assistant — "idiot", "dork", "you fool"?
  threatens_to_stop      do they threaten to abandon the work or the tool?
  quoting_not_speaking   are the charged words QUOTED from somewhere else — another
                         transcript, a log, someone else's message — rather than something
                         the engineer feels right now? Someone reporting that a user elsewhere
                         wrote "HORRIBLE" is describing, not complaining.
Saying that something is broken, failing or wrong is none of these. Reporting a problem plainly
is what a professional does all day, and it is not a feeling.
{{/IF_USER_TURNS}}
project — the repository or product this belongs to, if the record or the text names one,
otherwise null. Never invent one from the subject matter.

bullets — 3 to 5 flat points, strictly grounded, describing what this stretch is about.
Describe the work in neutral terms, as a colleague would summarise it to someone who was not
there. Never reproduce profanity, insults, or a sentence the engineer wrote: this part is kept.

work.domain — the field the work belongs to: software engineering, accounting, marketing, law.
Exactly one, never a list, never two joined by a slash. Not everyone here is an engineer — a
month-end close is accounting, campaign copy is marketing.

work.subjects — 1 to 5 specific things this stretch is actually about, named as the session
names them: files, systems, components, clients, accounts. These distinguish this stretch from
every other stretch in the same domain. Name what the text names; do not generalise them up
into the domain.

work.activity — what was being done to those subjects. Choose one only if the stretch shows it
plainly; a stretch that is mostly discussion, or that moves between several activities, has no
single activity, and null is the right answer. Do not pick the nearest label to fill the field.

demand — true or false about THE WORK, both sides of the conversation. Observations again, not
judgements: point at the text.
  something_went_wrong   does the stretch show something not coming out right — an error, a
                         failure, a number that did not agree, work that was rejected?
  work_was_redone        is something done earlier revisited, reversed, or replaced?
  two_things_reconciled  does it involve making two separate things agree — two records, two
                         systems, a plan and a budget, a draft and a brief?
  spans_separate_parts   does the work reach across parts usually handled separately — an
                         interface and the service behind it, a document and the system it
                         feeds, one team's territory and another's?
  specialist_vocabulary  does it use terms specific to a trade that a general reader would not
                         know — not merely long words?
Answer each on its own. Most stretches are not all five; a stretch where work simply proceeded
as planned is none of them.
