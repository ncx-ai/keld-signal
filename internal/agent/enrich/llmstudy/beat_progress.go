package llmstudy

import "strings"

// A beat answers "what are you working on, and where has it got to" from ONE window. It can see
// what that window shows. It cannot see how much of the job is left, and the two beats below are
// what happens when it guesses anyway — both from the accounting session, both read as fact:
//
//	[1] generated at window 0, before any work had happened:
//	    "The books are in ~/finance/meridian and the work is nearly complete, with only
//	     final reconciliations pending."
//	[4] generated while revenue cutoff and the trial-balance review were still ahead:
//	    "The work is complete and integrated into the adjusting journal"
//
// A reader ACTS on that — the same harm StaleUnresolved exists for (a closed blocker wastes
// exactly the effort an invented one does), except here the invented thing is the state of the
// whole engagement.
//
// The defect is NOT past tense, and that distinction is the whole difficulty. "Adding bottom
// padding to the activity rows has been completed" is a beat naming a specific thing the window
// shows finished, and it is correct. What is unobservable is a characterisation of the SESSION'S
// overall progress — that the work as a whole is done, nearly done, or has only some small
// remainder left. So the rule keys on the SUBJECT of the completion claim, not on the tense of
// its verb:
//
//   - a generic whole-of-work subject ("the work", "the project", "everything", ...) reaching a
//     completion word within a few tokens, which is a claim about the session; a named subject
//     ("the card logic", "the CSV export") is not matched at all, because a specific thing
//     finishing is exactly what a beat is allowed to report;
//   - a DEGREE-of-completion or remaining-work phrase ("nearly complete", "only X pending",
//     "all that remains"), which is unobservable whatever its subject is: a fraction-remaining is
//     a statement about the whole job even when it is attached to one part of it.
//
// Matching is over word SEQUENCES, never substrings. "complete" inside "completeness" and
// "pending" inside "depending" are the word-level over-detection this package has paid for
// repeatedly (unverified identifiers at 22.6%, leakage at ~100 per sweep, plurals scored as
// fabrications).
//
// Measured over both recorded beat sets — the 27 shipped beats and the 47 from before the
// framing fix, 74 in total — the rule fires on exactly the two beats above and on nothing else.

// wholeWorkSubjects are the noun phrases that stand for the session's work as a whole rather than
// for anything the window names. Deliberately a short, closed list of GENERIC subjects: the test
// has to separate "the work is complete" from "the CSV export is finished", and it can only do
// that by refusing to match a specific.
var wholeWorkSubjects = [][]string{
	{"the", "work"}, {"this", "work"}, {"all", "the", "work"}, {"the", "whole", "thing"},
	{"the", "task"}, {"the", "project"}, {"the", "session"}, {"the", "effort"},
	{"the", "close"}, {"everything"},
}

// completionWords are what a whole-work subject must reach to be a completion claim.
var completionWords = map[string]bool{
	"complete": true, "completed": true, "done": true, "finished": true,
	"finalised": true, "finalized": true, "wrapped": true,
}

// completionReach is how many tokens after a whole-work subject still count as its predicate —
// enough for "the work is now fully complete", short enough that a completion word belonging to a
// later clause is not attributed to it.
const completionReach = 5

// degreeClaims are the fraction-remaining phrases. Unobservable regardless of subject: a beat
// cannot see what is left, so saying how little is left is a guess whatever it is attached to.
var degreeClaims = [][]string{
	{"nearly", "complete"}, {"nearly", "completed"}, {"nearly", "done"}, {"nearly", "finished"},
	{"almost", "complete"}, {"almost", "done"}, {"almost", "finished"},
	{"mostly", "complete"}, {"mostly", "done"}, {"largely", "complete"}, {"largely", "done"},
	{"essentially", "complete"}, {"essentially", "done"}, {"practically", "done"},
	{"close", "to", "complete"}, {"close", "to", "done"}, {"wrapping", "up"},
	{"final", "stretch"}, {"home", "stretch"},
	{"all", "that", "remains"}, {"all", "that", "is", "left"}, {"all", "that", "s", "left"},
	{"little", "remains"}, {"nothing", "remains"}, {"nothing", "else", "remains"},
	{"the", "last", "step"}, {"the", "final", "step"}, {"the", "only", "thing", "left"},
}

// remainderWords are what "only"/"just" must reach to be a remaining-work claim ("with only final
// reconciliations pending", "just the trial balance left").
var remainderWords = map[string]bool{
	"remains": true, "remain": true, "remaining": true,
	"pending": true, "left": true, "outstanding": true,
}

// remainderReach bounds how far after "only"/"just" a remainder word still belongs to it. Small,
// because "only" is a common intensifier and the claim being caught is a short one.
const remainderReach = 5

// beatProgressClaims returns the overall-progress claims s makes, empty when it makes none. The
// matched shapes are returned rather than a bool so the validation failure can name what it
// rejected, and so the same detector can be run over the evidence (see
// BeatClaimsUnobservableProgress).
func beatProgressClaims(s string) []string {
	w := progressWords(s)
	var out []string
	for _, seq := range degreeClaims {
		if indexOfSequence(w, seq) >= 0 {
			out = append(out, strings.Join(seq, " "))
		}
	}
	for i, tok := range w {
		if tok != "only" && tok != "just" {
			continue
		}
		for j := i + 1; j < len(w) && j <= i+remainderReach; j++ {
			if remainderWords[w[j]] {
				out = append(out, tok+" ... "+w[j])
				break
			}
		}
	}
	for _, seq := range wholeWorkSubjects {
		i := indexOfSequence(w, seq)
		if i < 0 {
			continue
		}
		for j := i + len(seq); j < len(w) && j <= i+len(seq)+completionReach; j++ {
			if completionWords[w[j]] {
				out = append(out, strings.Join(seq, " ")+" ... "+w[j])
				break
			}
		}
	}
	return out
}

// progressWords lowercases and splits on anything that is not a letter or digit, so a claim is
// matched as words and an apostrophe does not hide one ("the work's complete" -> the, work, s,
// complete).
func progressWords(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	})
}

// indexOfSequence returns where seq occurs in w, or -1.
func indexOfSequence(w, seq []string) int {
	for i := 0; i+len(seq) <= len(w); i++ {
		match := true
		for j := range seq {
			if w[i+j] != seq[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// BeatClaimsUnobservableProgress reports whether a beat characterises overall progress that the
// evidence it was written from does not state.
//
// The evidence check is the refinement CurrentDescribesCompletion needed and got: that test
// over-counted by half until an in-progress guard made it abstain where the prose did describe a
// live state, because "a phrase's presence is not a statement's meaning". The same discipline
// applies here, but the guard has to be a different one — a beat is not asked for live state, so
// there is nothing in the beat itself that could license a completion claim. What licenses it is
// the CONVERSATION saying so. So the detector is run over the evidence too (the window plus the
// measured record, exactly what the beat was written from), and a claim the evidence also makes
// is a claim the beat is entitled to repeat.
//
// Where the evidence is empty this is the beat-only test, which is the strict reading and the
// right default: an unsupported progress claim is the defect.
func BeatClaimsUnobservableProgress(text, evidence string) bool {
	claims := beatProgressClaims(text)
	if len(claims) == 0 {
		return false
	}
	return len(beatProgressClaims(evidence)) == 0
}
