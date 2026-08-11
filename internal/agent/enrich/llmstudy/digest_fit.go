package llmstudy

import (
	"fmt"
	"strings"
)

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

// sessionLabelCap bounds SessionLabel/sessionLabel as embedded in EITHER prompt path —
// task-7b fix round 3 (minor G): the label was written verbatim with no cap at all, and
// it sits ahead of everything else both paths budget around. A label is meant to be a
// short descriptor ("finance / invoicing", "work session") — real callers never
// approach this — but nothing stopped a caller from handing over an entire paragraph;
// measured (DigestCreatePrompt, an otherwise-tiny turns/facts), a 12,000-rune label
// alone produced a 15,954-rune prompt. 200 is generous
// against any real label while bounding the pathological one.
const sessionLabelCap = 200

// assertPromptWithinBudget is the backstop task-7b's fix rounds kept discovering they
// needed: FOUR rounds so far have each fixed a named, measured leak — the retain-list,
// open-item count, TurningPoints, SessionLabel, the omitted-turns notice, the header
// pairs on both prompt paths — and every review since has found the round after had
// missed one. That pattern (fix an instance, find another instance) does not end by
// finding one more instance; it ends by asserting the FINISHED PRODUCT, once, after
// everything else has been assembled, so that whichever leak has not yet been found
// fails HERE — loudly — instead of shipping a prompt that silently truncates mid-JSON
// and drops the digest, the worst-consequence failure mode this package has (already
// paid for once on this branch: five of twenty digests lost that way before diagnosis).
//
// Checks the two invariants that actually matter architecturally, not every constant
// individually: the WHOLE prompt fits DefaultPromptCharBudget, and the recent-turns
// window — the only evidence current/why/next/unresolved are written from — still holds
// at least MinTurnChars. Panics rather than returning an error: both
// DigestCreatePromptWithView and DigestUpdatePromptFrom are pure string builders with no
// error channel and ~20 call sites (production and test) that assume a bare string back,
// so widening their signature to thread an error through is the "patch one more
// instance" move this backstop exists to stop needing. A violation here means a
// programming error in THIS package's own budgeting, not a bad but valid caller input —
// exactly the shape Go reserves panic for. windowMarker/tailMarker are supplied by the
// caller because the two prompt paths use different literal headings around their
// window (createWindowHeader/createSectionsMarker vs windowHeader/updateSectionsMarker);
// the check itself is otherwise identical, which is why it lives here once rather than
// as two near-duplicate checks in digest.go and digest_refine.go.
func assertPromptWithinBudget(p, windowMarker, tailMarker string) {
	if err := promptBudgetViolation(p, windowMarker, tailMarker); err != nil {
		panic("llmstudy: " + err.Error())
	}
}

// promptBudgetViolation is assertPromptWithinBudget's check, factored out so a test can
// assert "the backstop fires" by inspecting a returned error directly rather than only
// via recover — and so it can be exercised on a hand-built prompt string too, independent
// of how a violating one might arise.
//
// The window floor is checked ONLY when fitTurns actually clipped (the window starts
// with omittedNotice) — caught live by TestSmallWindowIsNotClipped, whose entire point
// is that a genuinely short conversation ("user: hello\n", 12 runes) must pass through
// untouched. MinTurnChars is a floor on how much room fitTurns is GUARANTEED when it
// has to clip a conversation that does not fit, not a padding requirement on every
// window regardless of how little conversation there actually is — a short session is
// not a starvation bug, and flagging one would make this backstop fire constantly on
// completely healthy prompts, which is exactly the "fires so often it gets ignored"
// failure a backstop must not have.
func promptBudgetViolation(p, windowMarker, tailMarker string) error {
	if total := len([]rune(p)); total > DefaultPromptCharBudget {
		return fmt.Errorf("assembled prompt is %d runes, over the %d-rune budget", total, DefaultPromptCharBudget)
	}
	start := strings.Index(p, windowMarker)
	if start < 0 {
		return fmt.Errorf("assembled prompt is missing its window marker %q", windowMarker)
	}
	start += len(windowMarker)
	end := strings.Index(p[start:], tailMarker)
	if end < 0 {
		return fmt.Errorf("assembled prompt is missing its tail marker %q", tailMarker)
	}
	window := p[start : start+end]
	if strings.HasPrefix(window, omittedNotice) {
		if n := len([]rune(window)); n < MinTurnChars {
			return fmt.Errorf("assembled prompt's conversation window was clipped to %d runes, below the %d-rune floor", n, MinTurnChars)
		}
	}
	return nil
}
