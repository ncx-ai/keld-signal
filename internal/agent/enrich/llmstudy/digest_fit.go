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
const DefaultPromptCharBudget = 9000

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
	// Start at a line boundary so the window does not open mid-word.
	if i := strings.IndexByte(clipped, '\n'); i >= 0 && i < len(clipped)-1 {
		clipped = clipped[i+1:]
	}
	return omittedNotice + clipped
}

// omittedNotice tells the model the window was clipped, so it does not read the opening
// of a mid-session window as the start of the work.
const omittedNotice = "[earlier turns omitted to fit the context; they are covered by " +
	"the report's cumulative sections]\n"
