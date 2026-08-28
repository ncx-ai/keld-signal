package llmstudy

import "strings"

// Bounding text at a LOGICAL DELIMITER, never at a rune count that lands mid-clause.
//
// The rule (AGENTS.md, "Never cut text mid-sentence"): any text read as language — a prompt,
// a report, a conversation window handed to a model, a span shown to a person — is cut at a
// sentence end, a line break, a turn boundary or an entry boundary; an identifier is never
// truncated at all, because a path or symbol cut short is a FALSE identifier; and a drop must
// be visible, omittedNotice being the precedent.
//
// This package had five sites cutting on a bare rune count, and they were not cosmetic:
//
//   - clip(v, 80) in toolLine truncated 3,376 of 3,596 shell commands (93.9%) mid-token on
//     this corpus. That is not just ugly. Window text is the verification reference for T2
//     (unverified identifiers) AND the source SessionRecord.Observe extracts Subjects from
//     under a verbatim gate — so a token cut in half enters the record as an authoritative
//     "subject" that never existed, and a real identifier the model correctly names can fail
//     verification against a source that holds only half of it.
//   - clip(text, PerTurnChars) cut 3.3% of turns mid-word. 378 of those 379 turns have a
//     sentence end available inside the cap and the last has a line break, at a mean cost of
//     119 and 334 runes respectively — so compliance here is affordable and NOTHING is dropped
//     at this budget, which had never been measured.
//   - clip(text, digestClip) cut 1,609 turns in the coarse view; at 240 runes 1,445 have a
//     sentence end, 90 have only a line break, and 74 have neither and are dropped.
//   - clipProse at promptOpenItemCap (80) amputated open items the prompt then tells the
//     model to "account for EVERY one" of — measured on this package's own real examples, a
//     133-rune item renders as "The server-side entity storage does not apply redaction,
//     creating a potential…".
//   - clipProse at sessionLabelCap (200) and DefaultListEntryCap (300).
//
// clipProse itself is left in place and still marks its cuts; what changes is which callers
// use it. It prefers a sentence end only at >=sentencePreferPct (92%) of room and otherwise
// cuts at a word boundary, which is exactly the mid-clause cut the rule forbids — its own doc
// records that an earlier sentence-preferring version "measurably deleted evidence", and that
// trade was made for CARRIED PROSE which no longer exists (Part 8). The remaining callers are
// bounded here instead.
//
// What is NOT changed, and why, is as load-bearing as what is:
//
//   - fitTurns already trims the window to a line boundary, but only when the result still
//     clears MinTurnChars; otherwise it keeps the mid-line opening deliberately. Making that
//     unconditional is the AMPLIFIER the round-3 review measured: room 1,599 instead of 1,600
//     threw away up to PerTurnChars (1,200) more runes and panicked 6 of 293 real refine
//     steps. Reserving a line's worth on top of MinTurnChars would need 1,200 runes the budget
//     does not have (+0 at realistic input scale). Recorded as a finding, not worked around.
//   - maxSubjectTermLen already DROPS an over-long term rather than truncating it (both
//     RecentSubjects and SessionRecord.Observe `continue`), which is what the rule requires of
//     an identifier. Its doc says so explicitly. No change was needed.
//   - trimToWindowCap drops whole TURNS and boundRetainList drops whole ENTRIES. Both are
//     already at delimiter granularity.
//   - ClipBeat is the model the rule generalises from: it cuts at a sentence boundary and
//     returns "" rather than store a fragment.

// clipAllowancePct lets a delimiter-respecting cut reach FORWARD to the next boundary instead of
// retreating to the previous one, when the next one is within half a budget again.
//
// This is the difference between the rule being free and the rule being expensive, and it was
// measured rather than reasoned. Retreating to the last sentence end inside the budget throws
// away everything between it and the cut, and at a small budget that is most of the text: over
// the sweep's own 14 sessions, retreating cost 40.3% of the coarse session view's content
// (146,426 runes) and 10.4% of the mined turns'. Reaching forward instead costs +2.5% and +7.7%
// — i.e. the delimiter-respecting cut carries slightly MORE text than the rune-count cut it
// replaces, while landing on a full stop rather than mid-word.
//
// That 40% mattered. The first measured pair of sweeps under the retreating rule regressed four
// thresholds in the anchor-ON arm (T2 0.9%->1.2%, T4 58.2%->53.8%, T7 4.5%->6.8%,
// T11 0.0%->3.6%), and the coarse view is what the synopsis is framed from. Honouring a
// convention at a 40% content cost is the trade this study exists to refuse; honouring it at zero
// cost is just correctness.
//
// Overshooting is safe because these are SHAPING budgets, not hard bounds: a mined turn is
// bounded downstream by trimToWindowCap and fitTurns, a view turn by clipSessionViewFor and
// SessionViewCap, a beat window by tailTurnsWithin measuring what it actually holds. The real
// context bounds (DefaultPromptCharBudget, BeatPromptCharBudget) are enforced by assertions that
// measure the assembled product, not by these per-turn numbers.
//
// 50%, not more: at +100% the view grows 21.6% and the reach becomes "find a sentence end
// eventually" rather than "the boundary is nearby".
const clipAllowancePct = 50

// elisionMark is the one marker this package uses to say "text was removed here". Shared with
// clipProse (which appends the identical rune) so a reader meets one convention, and so a
// re-clip can recognise and strip a previous mark instead of accumulating them.
const elisionMark = "…"

// clipTurn bounds one turn's text at a sentence end, falling back to a line break, and drops
// the text entirely rather than cut mid-clause when neither exists inside the budget.
//
// The sentence rule is lastSentenceStop (beat.go), NOT lastSentenceEnd (digest_insights.go).
// That is not a stylistic preference — lastSentenceEnd counts every '.', so it ends a sentence
// inside "turn-row.tsx", "2.9 GB" and "atlas.keld.co", which would make this function
// MANUFACTURE the false identifier it exists to prevent. Caught by this file's own test: with
// lastSentenceEnd, line-structured text ending "Read foo.go" was cut to "Read foo." — a file
// that does not exist. lastSentenceStop requires whitespace or end-of-text after the
// terminator and knows the abbreviation cases, which is exactly the property needed here.
//
// Dropping to a bare marker looks drastic and is the correct end of the trade: text with no
// sentence terminator and no newline in its first n runes is not prose being shortened, it is
// an unbroken blob (a minified payload, a base64 line, a single enormous token), and half of a
// blob is a false specific that this package's own record then extracts as a verified subject.
// Measured on the corpus: at PerTurnChars (1,200) this branch is never reached — all 379
// over-length turns have a sentence end or a line break — and at digestClip (240) it is
// reached by 74 of 1,609, which is the population it exists for.
func clipTurn(s string, n int) string {
	if n <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	room := n - runeLen(elisionMark)
	if room <= 0 {
		return elisionMark
	}
	// Forward first: the nearest sentence end at or beyond the budget, inside the allowance.
	// Preferred over the retreat below because it keeps the text between the two boundaries,
	// which at a small budget is most of the turn — see clipAllowancePct.
	if end := nextSentenceStop(r, room, room+room*clipAllowancePct/100); end > 0 {
		return strings.TrimSpace(string(r[:end])) + elisionMark
	}
	head := r[:room]
	if end := lastSentenceStop(head); end > 0 {
		return strings.TrimSpace(string(head[:end])) + elisionMark
	}
	if i := lastRune(head, '\n'); i > 0 {
		return strings.TrimRight(string(head[:i]), " \t\n") + elisionMark
	}
	return elisionMark
}

// nextSentenceStop is the first sentence end at or after `from`, no later than `limit`, or -1.
func nextSentenceStop(r []rune, from, limit int) int {
	if limit > len(r) {
		limit = len(r)
	}
	if from >= limit {
		return -1
	}
	for _, e := range sentenceStops(r[:limit]) {
		if e >= from {
			return e
		}
	}
	return -1
}

// lastRune is the index of the last occurrence of c in r, or -1.
func lastRune(r []rune, c rune) int {
	for i := len(r) - 1; i >= 0; i-- {
		if r[i] == c {
			return i
		}
	}
	return -1
}

// clipUnits bounds text at a whitespace-delimited UNIT boundary, keeping whole units.
//
// For a shell command, a search pattern or a URL the unit is the argument: it is the entry
// boundary the rule names, and it is the only cut that cannot manufacture a token that was
// never in the transcript. A quoted regex arrives as one unit and is therefore dropped whole
// rather than sliced — which is the point, since a sliced one reads as a real symbol.
//
// The kept units are re-joined with single spaces, so a clipped value is normalised where the
// original was not. That only happens on the clipped path (an argument that fits is returned
// byte-identical), and it is preferable to preserving the exact spacing of a value that is
// already known to be incomplete.
func clipUnits(s string, n int) string {
	if n <= 0 || runeLen(s) <= n {
		return s
	}
	// Room for the units themselves, once the " …" that marks the drop is paid for — plus the
	// same forward allowance clipTurn uses, so a unit that straddles the budget is kept whole
	// rather than the budget's last 20 runes being thrown away to avoid it. Measured over the
	// sweep's corpus: retreating strictly cost 22.7% of the rendered tool-argument text.
	room := n - runeLen(elisionMark) - 1
	limit := room + room*clipAllowancePct/100
	var b strings.Builder
	used := 0
	for _, f := range strings.Fields(s) {
		add := runeLen(f)
		if used > 0 {
			add++
		}
		// A unit is admitted if it fits the room, or if it straddles the room and still lands
		// inside the allowance. Either way it is a WHOLE unit: no token is ever halved.
		if used+add > limit || (used > 0 && used+add > room) {
			break
		}
		if used > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(f)
		used += add
	}
	if used == 0 {
		return elisionMark
	}
	return b.String() + " " + elisionMark
}

// viewOmittedNotice is the visible half of clipLines, mirroring omittedNotice's job for the
// coarse whole-session view: a shorter view that says nothing about being shorter is the
// silent-drop failure the rule exists to prevent, one level up from a mid-sentence cut.
const viewOmittedNotice = "[later lines of this view omitted to fit the context]\n"

// clipLines bounds line-structured text at a LINE boundary, keeping whole lines from the head
// and saying that it did.
//
// The head is kept because the whole-session view is sampled start-to-now and its opening is
// the session's goal statement, which is what the synopsis is written from; the tail it gives
// up is the recent material the conversation window carries in full anyway. That is also what
// clipProse did here, so only the granularity changes.
//
// The result is always <= n runes including the notice, so every reservation upstream
// (clipSessionViewFor, fitDiscretionary) keeps its arithmetic unchanged.
func clipLines(s string, n int) string {
	if n <= 0 || runeLen(s) <= n {
		return s
	}
	room := n - runeLen(viewOmittedNotice)
	if room <= 0 {
		return ""
	}
	var b strings.Builder
	used := 0
	for _, ln := range strings.SplitAfter(s, "\n") {
		if ln == "" {
			continue
		}
		add := runeLen(ln)
		if used+add > room {
			break
		}
		b.WriteString(ln)
		used += add
	}
	if used == 0 {
		return ""
	}
	out := b.String()
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return out + viewOmittedNotice
}

// clipEntry bounds one stored list entry (an insight, an open item) at a sentence end, and
// keeps an over-long SINGLE sentence whole rather than amputate it.
//
// No forward allowance here, unlike clipTurn and clipUnits, and for a measured reason rather
// than an oversight: an entry is one or two sentences by construction, so the retreat either
// finds a sentence end (losing the second sentence, which is the intent of a length cap on a
// list entry) or finds none and keeps the entry whole. There is no 40%-of-the-text loss to
// recover, so there is nothing to buy with an overshoot.
//
// Keeping it whole means DefaultListEntryCap is advisory for that one shape, and that is the
// deliberate choice rather than an oversight. The cap governs only what a PERSON reads in the
// stored report — the assembled prompt is measured insensitive to it
// (TestRefinePromptIsInsensitiveToStoredProseLength, and promptOpenItemCap intercepts every
// item the prompt embeds) — so nothing downstream can overflow, while an entry cut mid-clause
// is damage to the one reader the cap exists for. An entry that is genuinely a section in the
// wrong field still shows up in the sweep's REPORT LENGTH table, which is where that finding
// belongs.
func clipEntry(s string, n int) string {
	if n <= 0 || runeLen(s) <= n {
		return s
	}
	room := n - runeLen(elisionMark)
	if room <= 0 {
		return s
	}
	head := []rune(s)[:room]
	if end := lastSentenceStop(head); end > 0 {
		return strings.TrimSpace(string(head[:end])) + elisionMark
	}
	return s
}
