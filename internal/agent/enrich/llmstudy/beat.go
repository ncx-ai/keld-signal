package llmstudy

import (
	"os"
	"strconv"
	"strings"
	"unicode"
)

// BeatCap bounds one stored beat, and it is a BUDGET figure rather than a shape figure.
//
// It was measured for prose: at 200 it was not a backstop at all but the shape of the output —
// 46 of 47 beats over three real sessions ended in an ellipsis and the median count of COMPLETE
// sentences was zero, because lengths clustered at 187-199 and every one was guillotined
// mid-clause. 512 was then set from what a two-to-three-sentence standup answer actually costs
// (n=47, unclipped min 110 / median 355 / p90 519 / max 733 runes; the byte the SECOND sentence
// ends on ran median 329 / p95 473 / max 505).
//
// The beat is no longer prose (see BeatPrompt), so the sentence measurement no longer describes
// what is stored. The NUMBER is kept anyway, and deliberately: it is the beat ladder's claim on
// the digest prompt budget — MaxBeatSelection x BeatCap, a discretionary claimant that
// fitDiscretionary (digest_refine.go) shrinks under pressure — and the report tier currently has
// 4 runes of headroom. Changing the beat's shape must not spend that. So a bulleted beat is fit
// to the SAME 512 runes the prose beat claimed, by dropping whole trailing entries with the drop
// marked (fitBeatEvents), never by cutting an entry.
const BeatCap = 512

// BeatMinRunes is the floor below which the answer is degenerate rather than terse.
//
// Restated for the bulleted shape. The old floor (60) was set below the shortest real PROSE beat
// measured, 110 runes; the shortest legitimate bulleted beat is a subject at its schema floor
// plus one entry at its own — 12 + 12 runes plus the "- " and two newlines, i.e. 27 — so a floor
// of 60 would reject an honest thin answer, which is the one answer this design exists to make
// sayable. 40 rejects a pair of stubs while admitting a real one-event window.
const BeatMinRunes = 40

// Beat is one statement of what the work is about and what its window showed happening.
//
// Subject and Events are the model's two fields; Text is what gets stored and read — the two
// rendered together, with any drop marked in it. Text is kept as the canonical rendering rather
// than reassembled by each reader (the review packager, the digest store, RenderBeats) so a beat
// says the same thing everywhere, drop markers included.
//
// ChangedSubject and SubjectTerms are RETAINED AND NO LONGER SIGNALS. ChangedSubject fired on 41
// of 42 refinements and then on 0 of 46 packets, because it measured window adjacency rather
// than subject change; nothing in this design compares a beat to its predecessor. The fields stay
// because the blind review corpus records what the earlier runs decided (review.Item's
// MarkedSubjectChanged), and a struct that cannot represent that could not carry those rounds
// forward. Nothing writes them on the production path.
type Beat struct {
	Ordinal int      `json:"ordinal"`
	Subject string   `json:"subject,omitempty"`
	Events  []string `json:"events,omitempty"`
	Text    string   `json:"text"`

	ChangedSubject bool     `json:"changed_subject,omitempty"`
	SubjectTerms   []string `json:"subject_terms,omitempty"`
}

// BeatGround is the grounded, non-model-authored context one beat was generated from.
//
// Turn is the user turn that prompted the window. It is the one statement of what a window is
// about that no model wrote. It is no longer read by the beat path — the subject-change rule it
// grounded is retired — and is kept because the harnesses record it beside each beat, which is
// what lets a reader see what the window was answering without reading the window.
type BeatGround struct {
	Turn string
}

// GroundOf builds the grounded context for a beat over w. The window's LAST user turn is the turn
// that prompted it — Mine returns exactly one window per user prompt (see its doc), so that turn is
// the question this beat is answering.
func GroundOf(w Window) BeatGround {
	for i := len(w.Turns) - 1; i >= 0; i-- {
		if w.Turns[i].Role == RoleUser {
			return BeatGround{Turn: w.Turns[i].Text}
		}
	}
	return BeatGround{}
}

// BeatPrompt asks ONE question in ONE inference: what is being worked on, and what this stretch
// shows happening.
//
// ⚠️ THERE IS NO STATUS OR PROGRESS FIELD, AND THAT IS THE WHOLE POINT. The prompt this replaces
// asked a fused question — "what you are working on, and where it has got to" — so every firing
// demanded a progress claim whether or not the window supported one. Blind per-beat review of 46
// packets measured the consequence: `legible_to_a_manager` failed 0 of 36 and
// `domain_neutral_specificity` 0 of 36 — describing the work was never the problem — while
// `not_rubberstamping` failed 22 of 36, and four more beats were lost outright with all five
// ladder attempts rejected for claiming unobservable progress. The model had no way to say that
// this window does not show how far along the work is.
//
// A narrative is a sequence of EVENTS, not of STATES, and an event is observable in the window
// that contains it. Completion may still appear — "committed and pushed, 15/15 tests passing" is
// an event that happened here — but characterising the JOB is not asked for and has nowhere to go.
//
// BULLETS RATHER THAN PROSE, for a reason about the reader as much as the writer. Prose invites
// closure: a truncated story wants an ending, and that pull is what produced "and the
// reconciliation is complete." A list has no ending to write. An honest thin answer also LOOKS
// normal as a list — one entry saying a task was assigned — where the same content in prose reads
// as a failure to answer.
//
// ⚠️ THE EMPTY ANSWER IS MODELLED, NOT PROHIBITED. There is no forbidden-phrase list here and
// there must not be one: when the old stock-opener rule was reworded to NAME the phrasings it
// forbade, those openings went from 2 to 4. A prompt summons what it names. So the second worked
// example IS the nothing-was-finished shape, sitting among the others as an ordinary answer — a
// form to copy instead of a trap to avoid.
func BeatPrompt(record, window string) string {
	var b strings.Builder
	b.WriteString("You are the engineer working in the session below. A colleague asks you at " +
		"standup what you are working on. Name the subject of the work, and list what this " +
		"stretch of the conversation shows happening.\n\n")
	b.WriteString("SESSION RECORD (measured — authoritative):\n")
	b.WriteString(record)
	b.WriteString("\nRECENT CONVERSATION:\n")
	b.WriteString(window)
	b.WriteString(`
Answers look like this, and each of these is a normal answer:
  {"subject": "the CSV export in the Atlas exporter",
   "events": ["the CSV export was added to the exporter",
              "the export came back empty for a date range holding no rows"]}
  {"subject": "the March depreciation review for Meridian",
   "events": ["the depreciation task was assigned, and the register was named as fa-register.csv"]}
  {"subject": "the trial balance for the March close",
   "events": ["the adjusting journal was posted and the ledger reopened",
              "the schedule and the register still disagree on three assets"]}

Rules:
  - subject: one line naming what is being worked on, in the words this session uses for it.
  - events: one entry per thing the conversation shows happening, in order, in the past tense.
    Between one and four entries, one short line each, and fewer when the conversation
    shows fewer.
  - Each entry names what it is about, using the conversation's own words for names.
  - Where the conversation shows something being asked for, started, or discussed and left
    open, that is what the entry says.
  - Use the record to place the work. An action is only meaningful as part of something.
  - Every noun comes from the conversation or the record above. Nothing in these
    instructions is subject matter.
  - Finish every entry.

Respond with JSON only.
`)
	p := b.String()
	// The backstop BeatPrompt never had — see BeatPromptCharBudget. A contiguous beat window is
	// bounded by BeatWindowChars, so this only fires on a caller that assembled one some other
	// way, which is precisely the case that used to overflow ctx in silence.
	assertBeatPromptWithinBudget(p)
	return p
}

// BeatSchema constrains the response to a subject line and a list of events.
//
// There is no third field, and no field may be added that characterises the work as a whole: the
// schema is the only place a status field could come back from once the prompt stops asking for
// one. minLength on both halves is the floor a stub answer fails at, inside the retry loop rather
// than after it.
func BeatSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"subject": map[string]any{"type": "string", "minLength": beatSubjectMinRunes},
			"events": map[string]any{
				"type":     "array",
				"minItems": 1,
				"maxItems": beatEventMaxCount,
				"items":    map[string]any{"type": "string", "minLength": beatEventMinRunes},
			},
		},
		"required":             []string{"subject", "events"},
		"additionalProperties": false,
	}
}

// BeatDraft is one generated beat with everything that happened to it: what the model said, what
// the anchoring guard dropped, what the cap dropped, and the text that would be stored.
//
// The two drop lists are separate because they are different facts. Anchored is a statement about
// the bullet's relationship to the evidence; Overflowed is a statement about the budget. Reporting
// them as one number would make a run look grounded or ungrounded for the wrong reason.
type BeatDraft struct {
	Subject string   `json:"subject"`
	Events  []string `json:"events"`
	// Unanchored are the entries dropped because no term in them occurs in the window or the
	// record. Recorded, always, and rendered into Text as a marker: a guard that drops silently
	// is how T1 reported 100% while discarding 5 of 20 digests.
	Unanchored []string `json:"unanchored,omitempty"`
	// Overflowed are the entries dropped to fit BeatCap, newest-last order preserved.
	Overflowed []string `json:"overflowed,omitempty"`
	// Anchors names, per kept entry, the term that anchored it and which side it was found on —
	// the fact the guard was decided on, so a reader can check the decision rather than trust it.
	// An entry anchored only in the RECORD is the seam signal (see beat_anchor.go).
	Anchors []BeatAnchor `json:"anchors,omitempty"`
	// Unverified are the NAMES the stored beat uses that occur nowhere in the evidence.
	// Recorded, never enforced — the narrow form of the identifier check, kept as an observation
	// because enforcing it is how the 22.6% "unverified identifier" measurement happened.
	Unverified []string `json:"unverified,omitempty"`
	// SubjectAnchored records whether the subject line itself carries a term occurring in the
	// evidence. Recorded, never enforced: dropping a subject would leave no beat, and the design
	// only claims the guard for bullets.
	SubjectAnchored bool `json:"subject_anchored"`
	// Raw is the model's own answer rendered before any dropping, so what the cap and the guard
	// cost is visible rather than inferred.
	Raw string `json:"raw"`
	// Text is what is stored: the subject, the kept entries, and a marker for each drop.
	Text string `json:"text"`
}

// beatSubjectMinRunes / beatSubjectMaxRunes bound the subject line. The floor is the schema's,
// so a one-word subject is re-requested rather than stored. The ceiling keeps one line one line
// and leaves the rest of BeatCap for the events, which are what the window is evidence of: at 80
// runes a subject and three entries at their own cap still fit the stored beat with room to
// spare, so fitBeatEvents never trims an ordinary answer (TestEventCapLetsThreeEntriesFitTheBeatCap).
const (
	beatSubjectMinRunes = 12
	beatSubjectMaxRunes = 80
)

// renderBeat lays out a beat: subject line, one "- " entry per event, then a marker line per
// drop. The markers are part of the text and are charged to BeatCap, for the reason the beat
// window's hole marker is charged to the window bound — a notice that is in the output but in
// nobody's budget is how a bound gets quietly exceeded.
func renderBeat(subject string, events, unanchored, overflowed []string) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(subject))
	b.WriteString("\n")
	for _, e := range events {
		b.WriteString("- " + e + "\n")
	}
	if n := len(unanchored); n > 0 {
		b.WriteString(beatUnanchoredNotice(n))
	}
	if n := len(overflowed); n > 0 {
		b.WriteString(beatOverflowNotice(n))
	}
	return strings.TrimRight(b.String(), "\n")
}

// beatUnanchoredNotice and beatOverflowNotice are the two visible drop markers. They say WHICH
// rule dropped the entry, because "an entry was dropped" and "an entry was dropped for making a
// claim nothing in the evidence carries" are different things to a reader.
func beatUnanchoredNotice(n int) string {
	return "[" + strconv.Itoa(n) + " " + plural(n, "entry", "entries") +
		" dropped: no term in it occurs in this window or the record]\n"
}

func beatOverflowNotice(n int) string {
	return "[" + strconv.Itoa(n) + " " + plural(n, "entry", "entries") +
		" dropped to fit the beat cap]\n"
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// fitBeatEvents drops WHOLE trailing entries until the rendered beat fits cap, and returns what
// it dropped so the drop can be marked.
//
// Whole entries, never a cut inside one: a half-entry is the mid-clause truncation AGENTS.md
// forbids and the defect BeatCap was raised to fix. Trailing rather than leading, because the
// entries are in the order the window shows them and a list that starts mid-story is harder to
// read than one that stops early with the stop marked.
//
// The marker's own length is charged, which is why this searches downward over the rendered
// length rather than accumulating: whether a marker is needed at all depends on how many entries
// are kept, so the two cannot be computed in one pass.
func fitBeatEvents(subject string, events, unanchored []string, cap int) (kept, dropped []string) {
	if cap <= 0 {
		return events, nil
	}
	for n := len(events); n > 0; n-- {
		kept, dropped = events[:n], events[n:]
		if runeLen(renderBeat(subject, kept, unanchored, dropped)) <= cap {
			return kept, dropped
		}
	}
	return nil, events
}

// fitBeatText is the same bound applied to an already-rendered beat, which is what AppendBeat
// holds: whole LINES are dropped from the end and the drop is marked. It is the second gate, and
// on the production path it never fires — generateBeat fits before it returns — so its job is to
// stop a caller that assembled beat text some other way from putting an over-cap beat into the
// series silently.
func fitBeatText(s string, cap int) string {
	s = strings.TrimRight(strings.TrimSpace(s), "\n")
	if cap <= 0 || runeLen(s) <= cap {
		return s
	}
	lines := strings.Split(s, "\n")
	for n := len(lines) - 1; n >= 1; n-- {
		out := strings.Join(lines[:n], "\n") + "\n" + beatOverflowNotice(len(lines)-n)
		out = strings.TrimRight(out, "\n")
		if runeLen(out) <= cap {
			return out
		}
	}
	return s
}

// ClipBeat bounds PROSE at a sentence boundary, dropping any trailing incomplete sentence, and
// returns "" when no complete sentence fits.
//
// ⚠️ IT IS NO LONGER ON THE BEAT PATH. A bulleted beat holds no sentence terminators at all —
// "the export was rerun" is a complete entry and ends on a letter — so running this over one
// returns "" and loses every beat. Bounding is fitBeatEvents instead, at entry granularity.
//
// It stays because it is the model clipbound.go's rule generalises from (see its doc), and
// because the sentence machinery below it is that package's, not this one's.
func ClipBeat(s string, cap int) string {
	s = strings.TrimSpace(s)
	// Text may arrive already carrying clipProse's marker (a re-clip, or a model copying one
	// out of the record). It is the marker of the defect being fixed here, so it is removed
	// along with the fragment it marks.
	for strings.HasSuffix(s, "…") {
		s = strings.TrimSpace(strings.TrimSuffix(s, "…"))
	}
	r := []rune(s)
	if cap > 0 && len(r) > cap {
		r = r[:cap]
	}
	end := lastSentenceStop(r)
	if end <= 0 {
		return ""
	}
	return strings.TrimSpace(string(r[:end]))
}

// lastSentenceStop returns the rune index just past the terminator that ends the last COMPLETE
// sentence in r, or -1 when r holds none.
//
// Stricter than lastSentenceEnd (digest_insights.go), which counts every '.', '!' and '?'. That
// is safe for the word-boundary clip it serves, but as the rule that decides where prose ENDS
// it would cut inside the identifiers this study is full of: a bare "any period" rule ends a
// sentence in the middle of "turn-row.tsx" or "2.9 GB" or "atlas.keld.co". So a terminator
// counts only when what follows it is whitespace or the end of the string — which is what makes
// a period a sentence end rather than punctuation inside a token — and closing quotes and
// brackets are stepped over so a sentence ending in a quoted term is not missed. A short
// abbreviation list covers the remaining case, "e.g." and friends, where a period IS followed by
// a space mid-sentence.
func lastSentenceStop(r []rune) int {
	stops := sentenceStops(r)
	if len(stops) == 0 {
		return -1
	}
	return stops[len(stops)-1]
}

// sentenceStops returns the rune index just past every sentence-ending terminator in r. Callers
// that need only the last one use lastSentenceStop; the full list is what clipbound.go's
// boundary search walks.
func sentenceStops(r []rune) []int {
	var out []int
	for i := 0; i < len(r); i++ {
		if !sentenceTerminator(r[i]) || abbreviationBefore(r, i) {
			continue
		}
		j := i + 1
		for j < len(r) && (sentenceTerminator(r[j]) || sentenceCloser(r[j])) {
			j++
		}
		if j == len(r) || unicode.IsSpace(r[j]) {
			out = append(out, j)
			i = j - 1
		}
	}
	return out
}

func sentenceTerminator(c rune) bool { return c == '.' || c == '!' || c == '?' }

// sentenceCloser is the punctuation allowed to sit between a terminator and the space after it,
// so a sentence ending on a quoted or parenthesised term still ends. Straight and curly alike.
func sentenceCloser(c rune) bool {
	switch c {
	case '"', '\'', ')', ']', '’', '”', '»':
		return true
	}
	return false
}

// beatAbbreviations are the tokens whose trailing period is not a sentence end even though a
// space follows it. Short by design: every entry is a false sentence break that would cut text
// mid-clause, and a wrong entry silently costs a real sentence end.
var beatAbbreviations = map[string]bool{
	"e.g": true, "i.e": true, "eg": true, "ie": true, "etc": true, "vs": true,
	"cf": true, "al": true, "fig": true, "approx": true, "no": true,
	"dr": true, "mr": true, "mrs": true, "ms": true, "prof": true,
}

// abbreviationBefore reports whether the terminator at i closes a known abbreviation. The token
// is read back over letters, digits and interior periods so "e.g" arrives whole rather than as
// its final letter.
func abbreviationBefore(r []rune, i int) bool {
	j := i - 1
	for j >= 0 && (unicode.IsLetter(r[j]) || unicode.IsDigit(r[j]) || r[j] == '.') {
		j--
	}
	return beatAbbreviations[strings.ToLower(string(r[j+1:i]))]
}

// GenerateBeat produces one beat's stored text, or an error.
func (l *Llama) GenerateBeat(record, window string) (string, error) {
	d, err := l.generateBeat(record, window)
	return d.Text, err
}

// beatSampling is the beat path's retry schedule: greedy first, then a widening temperature at a
// fixed seed.
//
// ⚠️ A RETRY THAT CANNOT DIFFER IS NOT A RETRY. At temperature 0 a re-request returns the
// BYTE-IDENTICAL string, so a rejected generation failed five times on the same bytes and the
// window contributed no beat — 2 of 42 asked, identically in all six sweeps of one round, and
// recorded in no artifact but a concerns list. The split experiment's reading passes then
// repeated the mistake (callValid, sample == nil) and cost a pass five identical attempts. Every
// generation on this path varies its retries.
//
// Attempt 0 is temperature 0 with no seed field at all, so the request is byte-identical to every
// other caller's and a beat that succeeds first time is unaffected. Each retry then raises the
// temperature by beatRetryTempStep — enough for the sampler to leave a degenerate mode, far below
// the level at which a 4B instruct model starts inventing. The seed is the attempt index, not a
// clock or a random value: a re-run of the same sweep re-requests with the same seed and gets the
// same recovery, which is what keeps a repeated sweep's figures comparable.
func beatSampling(attempt int) sampling {
	if attempt <= 0 {
		return sampling{}
	}
	return sampling{Temp: float64(attempt) * beatRetryTempStep, Seed: attempt}
}

// beatRetryTempStep is how much each retry widens sampling. With retry.DefaultPolicy's 5
// attempts the schedule is 0, 0.2, 0.4, 0.6, 0.8 — the last of those is still a conservative
// temperature for an instruct model, and the shape gates apply unchanged to every attempt, so a
// wilder sample is rejected rather than stored.
const beatRetryTempStep = 0.2

// generateBeat is the whole beat pass: one inference, shape-checked and re-requested inside the
// retry loop, then anchored and fitted.
//
// What is validated is SHAPE — the entries are whole statements of usable length, the list does
// not repeat itself, the subject is one line — plus the one substantive condition that makes a
// beat a beat: at least one entry survives the anchoring guard. Nothing here judges whether the
// beat is a good answer; that is what the blind review round is for, and every string heuristic
// on this branch that tried to encode a judgement measured ordinary English instead.
//
// Note what is deliberately absent: a max_tokens bound. The response is JSON-schema-constrained,
// so a token limit cuts the string mid-value and the object never closes; brevity is asked for in
// the prompt and enforced here by shape, never by truncating the decode.
func (l *Llama) generateBeat(record, window string) (BeatDraft, error) {
	var d BeatDraft
	var out struct {
		Subject string   `json:"subject"`
		Events  []string `json:"events"`
	}
	evidence := window + "\n" + record
	err := l.callValidSampled(BeatPrompt(record, window), BeatSchema(), &out, func() error {
		d = BeatDraft{}
		subject := strings.Join(strings.Fields(out.Subject), " ")
		switch {
		case runeLen(subject) < beatSubjectMinRunes:
			return passProblem("beat", "subject is "+strconv.Itoa(runeLen(subject))+
				" runes, under the floor of "+strconv.Itoa(beatSubjectMinRunes))
		case runeLen(subject) > beatSubjectMaxRunes:
			return passProblem("beat", "subject is "+strconv.Itoa(runeLen(subject))+
				" runes, over the cap of "+strconv.Itoa(beatSubjectMaxRunes))
		}
		events, err := checkBeatEvents(out.Events)
		if err != nil {
			return err
		}
		d.Subject, d.Raw = subject, renderBeat(subject, events, nil, nil)
		d.SubjectAnchored = beatAnchorIn(subject, window, record).Term != ""
		anchored, unanchored, anchors := anchorBeatEvents(events, window, record)
		if len(anchored) == 0 {
			// Every entry unanchored is not a drop, it is a generation with nothing in it that
			// the window or the record carries — the one case where losing the beat is the
			// honest outcome. Re-requested at a wider temperature like any other rejection.
			return passProblem("beat", "no entry carries a term occurring in the window or "+
				"the record: "+strings.Join(quoteAll(unanchored), "; "))
		}
		kept, overflowed := fitBeatEvents(subject, anchored, unanchored, BeatCap)
		d.Events, d.Unanchored, d.Overflowed, d.Anchors = kept, unanchored, overflowed, anchors[:len(kept)]
		d.Text = renderBeat(subject, kept, unanchored, overflowed)
		d.Unverified = unverifiedSpecifics(d.Text, evidence)
		if runeLen(d.Text) < BeatMinRunes {
			return passProblem("beat", "beat is "+strconv.Itoa(runeLen(d.Text))+
				" runes, under the floor of "+strconv.Itoa(BeatMinRunes))
		}
		return nil
	}, beatSampling)
	if err != nil {
		return d, err
	}
	return d, nil
}

// quoteAll renders a list for an error message, so a rejection names what it rejected.
func quoteAll(v []string) []string {
	out := make([]string, 0, len(v))
	for _, s := range v {
		out = append(out, strconv.Quote(s))
	}
	return out
}

// BeatSaysNothingNew reports a beat that restates the most recent one.
//
// ⚠️ RETIRED AS A SIGNAL, KEPT AS A FUNCTION. It discarded 0 of 70 beats in the round that
// measured it, which is the whole of its evidence: it is inert. AppendBeat no longer consults it,
// so a near-duplicate beat is now stored — a window that genuinely repeated itself is a fact about
// the session, and suppressing it silently deleted history to hide a repetition the reader could
// have seen. It stays defined because the blind review harness lists it among the heuristics under
// comparison (review/heuristics.go) and scoring an earlier round requires being able to run it.
func BeatSaysNothingNew(text string, prev []Beat) bool {
	if len(prev) == 0 {
		return false
	}
	return beatsRestate(text, prev[len(prev)-1].Text)
}

// BeatTurnsFromEnv reads KELD_DIGEST_BEAT_TURNS, defaulting to 5 user turns.
//
// Three was too often, and repetitiveness was the symptom: over three user turns there is
// frequently nothing new to say, so the model restates the same subject in slightly different
// words. Five spaces the question far enough apart that there is usually something to answer.
func BeatTurnsFromEnv() int {
	if v := os.Getenv("KELD_DIGEST_BEAT_TURNS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 5
}
