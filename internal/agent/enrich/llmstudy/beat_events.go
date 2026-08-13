package llmstudy

import (
	"fmt"
	"strconv"
	"strings"
)

// The event list is the beat's answer to "what does this window show HAPPENING", and that
// question is answerable from a window in a way "how far along is this" is not. See BeatPrompt
// for the measurement that retired the second question; what lives here is the shape of the
// answer to the first.
//
// It used to be a separate inference — the middle pass of a three-pass split whose composition
// pass existed only to keep a prose writer blind to the window. With a bulleted output there is
// nothing to compose, so the split collapsed to the single pass in beat.go and these are its
// entry-level rules.

const (
	// beatEventMinRunes is short enough for a real one-clause event ("the export was rerun")
	// and long enough to reject a fragment that says nothing.
	beatEventMinRunes = 12
	// beatEventMaxRunes bounds ONE entry, and it is MEASURED rather than chosen — twice, in
	// opposite directions.
	//
	// It was first set at 130 against BeatCap, so that a subject and three entries always fit the
	// stored beat. The first pilot then lost a whole beat to it: all five ladder attempts were
	// rejected for one 179-rune entry ("The Sub-CA selection for Apple Developer certificates was
	// discussed and confirmed to be G2 ..."), which is the exact failure this design exists to
	// stop — a bound that discards the answer rather than bounding it.
	//
	// 200 is above the longest entry the pilot produced with margin, so a rejection here now means
	// a paragraph rather than a long sentence. The TOTAL is bounded where it can be bounded
	// without losing the generation: fitBeatEvents drops whole trailing entries and MARKS the
	// drop, so an over-long list costs one visible entry instead of the beat.
	//
	// ⚠️ IT STILL LOSES WHOLE BEATS ON REAL MATERIAL. Every one of the 3 generation failures over
	// the rebalanced 14-session sweep was this bound, all 3 on real transcripts, all 3 after five
	// ladder attempts: a 235-rune entry twice and a 219-rune entry once, each a single sentence
	// that ran long rather than a paragraph. Raising it further is no longer paid for at BeatCap
	// (that trade is gone — see below), but it is still paid for in the report, since BeatCap must
	// rise with it to keep the three numbers consistent. Left where it is: the fix for a 235-rune
	// sentence is a shorter entry, which is a prompt question, not a constant.
	beatEventMaxRunes = 200
	// beatEventMaxCount bounds the list, and it is the SCHEMA's bound rather than a rule the
	// prompt hopes for: a constrained decode cannot emit a fifth entry, so nothing is generated
	// that would then be thrown away.
	//
	// Four is measured. At five, the first full sweep dropped 11 of 70 entries to BeatCap across
	// 7 of 19 beats — real observed events, lost to a budget number after the model had already
	// paid to write them.
	//
	// ⚠️ THE COUNT IS NOW WHAT BeatCap IS SIZED FROM, RATHER THAN A HOPE ABOUT WHAT WILL FIT.
	// "Four entries fit the stored beat" was true of that first half-hand-authored sweep, where
	// entries are short, and false of real material: over the rebalanced 14-session corpus the cap
	// dropped 68 of 274 offered entries across 47 of 69 beats — 67 of 248 across 46 of 62 on the
	// real transcripts alone — because four entries at beatEventMaxRunes beside a subject at its
	// own cap renders to 892 runes against a 512-rune BeatCap. That was arithmetic, not luck, and
	// the fix was the cap rather than the count: the model offered the full four entries on 67 of
	// 69 beats, so asking for three would have discarded a real observed event by construction on
	// nearly every beat. TestBeatCapHoldsTheAnswerTheSchemaAdmits pins the three numbers against
	// each other; what the larger cap costs the report is measured in
	// TestBeatCapTradesBeatsInTheReportRatherThanTrippingTheBackstop.
	beatEventMaxCount = 4
)

// passProblem names the pass a validation failure belongs to.
//
// Retry behaviour is identical to firstProblem's and does not come from here — callValid wraps any
// validator error in sampleErr — but it names the pass instead of borrowing the digest's prefix.
// That mattered in a real recorded failure, which read "invalid digest: event ... is 350 runes,
// over the cap of 300": the artifact's job is to say what happened, and that sentence named the
// wrong thing.
func passProblem(pass, problem string) error {
	return fmt.Errorf("invalid %s: %s", pass, problem)
}

// checkBeatEvents validates SHAPE only, and deliberately nothing else.
//
// There is no check here that the events are true, and there cannot be one at this level: the
// only thing to compare against is the window itself, which is what the blind judges read. What
// it does enforce is that each entry is a whole statement of usable length and that the list does
// not repeat itself — a duplicated event would be counted twice by anything reading the series.
//
// The one substantive gate, verbatim anchoring, runs after this and is a separate function on
// purpose: it is a fact about a string's occurrence in the evidence, not a rule about the answer's
// shape, and the two must not be readable as one number (see beat_anchor.go).
func checkBeatEvents(raw []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, e := range raw {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if n := runeLen(e); n < beatEventMinRunes {
			return nil, passProblem("beat", "entry "+strconv.Quote(e)+" is "+
				strconv.Itoa(n)+" runes, under the floor of "+strconv.Itoa(beatEventMinRunes))
		} else if n > beatEventMaxRunes {
			return nil, passProblem("beat", "entry "+strconv.Quote(e)+" is "+
				strconv.Itoa(n)+" runes, over the cap of "+strconv.Itoa(beatEventMaxRunes))
		}
		if strings.ContainsAny(e, "\n\r") {
			return nil, passProblem("beat", "entry "+strconv.Quote(e)+" spans more than one line")
		}
		k := strings.ToLower(e)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, e)
	}
	if len(out) == 0 {
		return nil, passProblem("beat", "events is empty")
	}
	return out, nil
}
