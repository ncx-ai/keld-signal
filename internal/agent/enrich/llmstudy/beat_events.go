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
	// beatEventMaxRunes bounds ONE entry, and it is set against BeatCap rather than taste: a
	// subject at its own cap plus THREE entries at this one still fit the stored beat (480 of
	// 512 runes), so fitBeatEvents never trims an ordinary answer and only fires on a long list.
	// An event needing more than this is a paragraph, and a paragraph is what the fused prose
	// beat was.
	beatEventMaxRunes = 130
	// beatEventMaxCount bounds the list. Five is what the prompt asks for; six is the schema's
	// slack, so a model that adds one is not thrown away.
	beatEventMaxCount = 6
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
