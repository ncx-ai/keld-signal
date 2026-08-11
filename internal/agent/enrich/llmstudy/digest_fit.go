package llmstudy

import "strings"

// DefaultPromptCharBudget bounds a digest prompt so prompt plus output fits the context.
//
// Derived, not guessed. At ctx 5120 the output can reach ~6500 runes across eight
// sections (five prose caps of 900, structure at 1600, two lists), which is ~1800
// tokens; reserving ~2100 for output and slack leaves ~3000 tokens for the prompt, and
// at the ~3.6 chars/token this corpus measures that is ~10800 characters.
//
// Measured down from 10800 to 9000. At 10800 one session of 14 still truncated on all 5
// attempts, and since the guard already bounded the PROMPT, the overrun had to be the
// output: the reserve was too small for the longest reports, not the prompt too large.
// Trading ~500 tokens of window for generation room closed it.
//
// This exists because retrying could not fix the overflow. One session truncated
// mid-JSON on all 5 attempts at the same step — the prompt was simply too large for the
// window, deterministically, so the only fix is to not send it.
const DefaultPromptCharBudget = 11000

// fitTurns clips a rendered window so a prompt of `overhead` characters plus the turns
// stays inside the budget.
//
// Keeps the TAIL. The newest turns are what "current", "next" and "unresolved" describe,
// and the older ones have already been folded into the prior digest's cumulative
// sections by an earlier refinement — so the head is the part already represented
// elsewhere, and the part cheapest to lose.
func fitTurns(turns string, overhead int) string {
	room := DefaultPromptCharBudget - overhead
	if room < 0 {
		room = 0
	}
	r := []rune(turns)
	if len(r) <= room {
		return turns
	}
	// The notice is part of the prompt, so it comes out of the same room. Omitting it
	// from the arithmetic put the prompt over budget by exactly its own length.
	room -= len([]rune(omittedNotice))
	if room < 0 {
		room = 0
	}
	clipped := string(r[len(r)-room:])
	// Start at a line boundary so the window does not open mid-word — but only when doing
	// so cannot breach MinTurnChars beyond what `room` already promised. The naive version
	// of this trimmed unconditionally, discarding up to one whole line BEYOND `room`: with
	// many short lines that is a small overshoot (measured -25 runes at 35-rune lines,
	// window 1575 against a 1600 floor), but with a single long pasted turn straddling the
	// cut, the first '\n' found is that turn's own terminator deep into `clipped`, and the
	// trim can collapse the window to almost nothing (measured -1589 runes, window 11).
	//
	// `clipped` is exactly `room` runes before any trim, so it already clears the floor
	// whenever `room` does — falling back to it is never worse than trimming, and trimming
	// is applied only when it does not lose that guarantee. If `room` itself is already
	// short of MinTurnChars, the floor was unreachable before this function ran (a finding
	// for whoever owns the budget upstream — see fitDiscretionary's doc — not something a
	// trim choice here can fix), so there is nothing left to protect and the boundary trim
	// is applied unconditionally for cleanliness.
	if i := strings.IndexByte(clipped, '\n'); i >= 0 && i < len(clipped)-1 {
		kept := clipped[i+1:]
		if room < MinTurnChars || len([]rune(kept)) >= MinTurnChars {
			clipped = kept
		}
	}
	return omittedNotice + clipped
}

// omittedNotice tells the model the window was clipped, so it does not read the opening
// of a mid-session window as the start of the work.
const omittedNotice = "[earlier turns omitted to fit the context; they are covered by " +
	"the report's cumulative sections]\n"
