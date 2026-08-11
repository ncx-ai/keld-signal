package llmstudy

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// DefaultPromptCharBudget bounds a digest prompt so prompt plus output fits the context.
//
// It exists because retrying could not fix an overflow. One session truncated mid-JSON on
// all 5 attempts at the same step — the prompt was simply too large for the window,
// deterministically — so the only fix is to not send it. Five of twenty digests were lost
// that way on this branch before diagnosis.
//
// **14,000 at `ctx` 8192.** The context is the lever and it was MEASURED, not estimated,
// CPU-only after plateau (`--no-repack -ctk q8_0 -ctv q8_0 --cache-ram 0`):
//
//	ctx    baseline RSS
//	4096   2855 MB
//	5120   2932 MB
//	6144   3008 MB
//	8192   3161 MB   <- the study now runs here
//
// The earlier rounds read the ~3 GB figure as a hard ceiling and so treated 5120/6144 as
// the only admissible windows, which is what forced the prompt budget to absorb every
// subsequent finding. It is an eyeball target with roughly +/-100 MB of tolerance, and
// 3,161 MB is inside that: DECIDED, and not to be optimised back down.
//
// Prompt plus output at this size: 14,000 characters is ~3,889 tokens at the ~3.6
// chars/token this corpus measures, and a nine-section report runs 2,000-2,400 output
// tokens (~7,200-8,600 runes), so the worst case is ~6,289 of 8,192 — about 1,900 tokens
// (23%) of margin. At the old 11,000/5120 pairing the same output sat at ~5,456 of 5,120,
// i.e. over, which is why the budget had to keep shrinking to hold T1.
//
// Superseded reasoning, recorded so it is not re-derived: the budget was previously
// justified as "~10,800 characters at ctx 5120, measured down to 9,000 because one session
// of 14 still truncated on all 5 attempts", then back up to 11,000. That whole line of
// argument is obsolete — it was arithmetic against a context this study no longer uses,
// and squeezing content to fit it is what made MinTurnChars unreachable at REALISTIC input
// scale (12 path-shaped record Subjects, or a path-heavy recency anchor, or a large facts
// block on the create path: each starved the window below its floor, which the backstop
// then correctly panics on — aborting a measurement sweep rather than losing one digest).
// Raising the context was the right lever because the floor and the retain-list are the
// two things the design is judged on, and both are content.
const DefaultPromptCharBudget = 14000

// runeLen is the ONE unit every figure in the prompt-budget arithmetic is measured in.
//
// This existed as scattered `len([]rune(x))` calls and, at four sites, as `b.Len()` — and
// that mixture was the round-3 review's CRITICAL finding, the one that panicked on 2% of
// real mined transcripts. Both budgets are rune counts (DefaultPromptCharBudget is compared
// against len([]rune(p)), MinTurnChars against the window's rune length) and fitTurns slices
// runes, but the two assemblies charged their prefix in BYTES: `b.Len()`. On ASCII the two
// agree exactly, so every synthetic test certified the arithmetic as sound; on real
// transcript text — em dashes, arrows, box-drawing, emoji — the assembled prefix carried
// 8-18 bytes of multi-byte excess, so `room` landed that many runes BELOW what
// fitDiscretionary and clipSessionViewFor had reserved for the window, and the floor broke.
//
// RUNES is the direction the reconciliation had to go, not bytes. The floor is a promise
// about how much CONVERSATION the window holds, and a rune is the unit a reader and a
// tokenizer both count in; MinTurnChars bytes of multi-byte text is fewer than MinTurnChars
// characters of it, so a byte-denominated floor would silently shrink on exactly the
// non-English or symbol-heavy content it most needs to hold. Bytes are an encoding detail
// that no budget in this package is expressed in.
//
// Named rather than inlined so the unit is visible at every site and a `b.Len()` reappearing
// in this arithmetic reads as the anomaly it is. utf8.RuneCountInString, not len([]rune(s)),
// so measuring a prompt does not allocate a copy of it.
func runeLen(s string) int { return utf8.RuneCountInString(s) }

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
	room -= runeLen(omittedNotice)
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
// exactly the shape Go reserves panic for.
//
// `window` is fitTurns' OWN RETURN VALUE, passed in by the assembly — not re-derived from
// `p`. Task-7b fix round 4: an earlier version located the window by strings.Index-ing the
// literal headings that surround it, which any CONTENT quoting one of those headings
// defeats, in both directions.
//
//   - A quoted windowHeader (reachable: prev.Unresolved is model output, and
//     LeakedPromptWords exists in this package precisely because this model echoes prompt
//     headings back) moves the measured start EARLIER, into the open-items block. The
//     measured span then does not begin with omittedNotice, so the floor check is skipped
//     entirely: measured 1,717 while the real window was 757 against a 1,600 floor, total
//     in budget, backstop silent — a starved window shipping unnoticed, which is the exact
//     failure this backstop exists to prevent.
//   - A quoted tail marker inside the conversation moves the measured END earlier, making a
//     HEALTHY prompt panic: measured 867 against a real window of 5,102. Self-inflictable,
//     since this harness mines transcripts of its own development and that literal gets
//     dumped into them.
//
// Measure the quantity you produced. The assembly HAS fitTurns' output — it just wrote it —
// so there is no reason to go looking for it again in a string that also contains
// arbitrary conversation text. (Tests that hold only a finished prompt still recover the
// window by landmark; see promptWindow's doc for why that is sound there and not here.)
func assertPromptWithinBudget(p, window string) {
	if err := promptBudgetViolation(p, window); err != nil {
		panic("llmstudy: " + err.Error())
	}
}

// promptBudgetViolation is assertPromptWithinBudget's check, factored out so a test can
// assert "the backstop fires" by inspecting a returned error directly rather than only
// via recover — and so it can be exercised on hand-built strings too, independent of how a
// violating prompt might arise.
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
func promptBudgetViolation(p, window string) error {
	if total := len([]rune(p)); total > DefaultPromptCharBudget {
		return fmt.Errorf("assembled prompt is %d runes, over the %d-rune budget", total, DefaultPromptCharBudget)
	}
	if strings.HasPrefix(window, omittedNotice) {
		if n := len([]rune(window)); n < MinTurnChars {
			return fmt.Errorf("assembled prompt's conversation window was clipped to %d runes, below the %d-rune floor", n, MinTurnChars)
		}
	}
	return nil
}
