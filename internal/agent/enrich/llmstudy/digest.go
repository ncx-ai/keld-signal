package llmstudy

import (
	"encoding/json"
	"fmt"
	"strings"
)

// DigestSchemaVersion gates the digest's shape. Bump on any field change: stored
// snapshots record it so a refine loop never mixes shapes.
const DigestSchemaVersion = 1

// Digest is a semi-structured report of what a session has been about.
//
// The structure is guaranteed by constrained decoding; the prose inside each field
// is free. That is what "semi-structured" buys: a malformed report is impossible,
// while the writing stays useful.
//
// Unresolved is required for a reason. Rubberstamping — reporting smooth progress on
// work that was corrected and abandoned — thrives when a format has nowhere to put
// failure. A required field the model must address means an all-positive report
// cannot validate, which is a guarantee where "please be honest" is a hope.
type Digest struct {
	// Synopsis leads because the other eight sections are a decomposed status board:
	// answering "what is this work about" from them required a reader to assemble why +
	// structure + done themselves. This is the standalone answer — subject, standing,
	// direction — and it is the one section deliberately allowed to synthesise.
	Synopsis string `json:"synopsis"`
	Done     string `json:"done"`
	Happened string `json:"happened"`
	// Structure is the cumulative view of what has been built or established and how
	// the pieces relate — architecture, for engineering work; the shape of the
	// process, for a finance close or a campaign. Named neutrally on purpose: it must
	// read sensibly for an accountant, not only an engineer.
	//
	// It is the section most exposed to both growth and drift, because unlike
	// insights it must be REVISED as understanding changes rather than only appended
	// to. Hence the explicit extend-and-revise instruction. It carried the largest
	// prose cap too (1,600 runes) until prose clipping was removed — so it is now the
	// section most able to grow without limit, which is why the sweep measures its
	// length per session rather than capping it. See CapSections.
	Structure  string   `json:"structure"`
	Insights   []string `json:"insights"`
	Current    string   `json:"current"`
	Why        string   `json:"why"`
	Next       string   `json:"next"`
	Unresolved []string `json:"unresolved"`
}

// DigestSchema is the JSON schema every digest must satisfy.
// digestMinProse is a floor on required prose sections, low enough that a genuinely
// short honest answer still fits and high enough to exclude "", "n/a" and "none".
const digestMinProse = 12

func DigestSchema() map[string]any {
	list := map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	// minLength is enforced by the grammar, so an empty required section cannot be
	// generated at all. Retrying could not fix it: "next is empty" reproduced through
	// all 5 attempts on the same input, i.e. it was deterministic for that prompt, not
	// a sampling fluke. Prevention beats a retry that cannot converge.
	prose := map[string]any{"type": "string", "minLength": digestMinProse}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"synopsis":   prose,
			"done":       prose,
			"happened":   prose,
			"structure":  prose,
			"insights":   list,
			"current":    prose,
			"why":        prose,
			"next":       prose,
			"unresolved": list,
		},
		"required": []string{"synopsis", "done", "happened", "structure", "insights",
			"current", "why", "next", "unresolved"},
		"additionalProperties": false,
	}
}

// digestSections describes each field to the model. Deliberately profession-neutral:
// it names no code, tests or deploys, because the same digest must serve an
// accountant reconciling ledgers and a marketer drafting a campaign.
const digestSections = `  synopsis    What is this work ABOUT, where does it stand, and where is it going?
              Three or four sentences, readable on its own by someone who reads nothing
              else. Name the subject in the first sentence — not the activity, the
              THING. This is the one section that may draw on the others; it is a
              synthesis, not another status field. Do not restate the purpose sentence
              from "why" — the reader wants the shape of the work, not its justification.
  done        What concrete outcomes are now in place? Name them. Do not describe
              effort or intent here — only what is finished.
  happened    How did it actually go? Include obstacles, wrong turns, reversals, and
              how they were resolved. This is where difficulty belongs.
  structure   How does the thing being worked on fit together? Its parts, what each
              is for, and how they relate. For technical work this is the architecture;
              for other work, the shape of the process. Describe the SUBJECT, not
              the session. Build the fullest picture the conversation supports.
  insights    Key thoughts and learnings. One per entry. Only things a reader could
              not infer from the bare facts.
  current     What single thing is in progress right now? Answer "nothing in
              progress" if the work reached a stopping point.
  why         What purpose does this serve? The goal behind the work — not a
              restatement of what was done.
  next        Where is this going? The direction, plus the concrete immediate steps
              that follow from it.
  unresolved  What is still open, blocked, or was abandoned? One per entry.
              If genuinely NOTHING is open, the entry must be exactly:
                none - the work reached a stopping point
              Do not invent an open item to fill this field. An invented blocker is
              worse than no blocker, because a reader will act on it.
`

const digestRules = `
Rules:
  - Report only what the conversation supports. If something is not stated, do not
    assert it. Absence of evidence is itself worth reporting.
  - The COUNTS above are measured facts and your report must be consistent with
    them. If corrections occurred, the work did not go smoothly and the report must
    say so.
  - Name specifics (files, systems, people, amounts) only when they appear in the
    conversation. Do not invent plausible ones.
  - unresolved must be addressed, but only with items the conversation actually
    supports. If the work was verified and nothing remains, use the exact "none -"
    entry above. Never speculate that something "needs further testing" or "is not
    fully understood" unless the conversation says so.
  - Write about the WORK, not about the assistant or the conversation. Describe what
    changed and what state things are in, never who typed what. The reader cares
    what happened to the work, not who did it.
  - Each section must add something the others do not. Do not restate one section's
    content in another. The single exception is synopsis, whose job IS to synthesise —
    but it must still be written, not assembled by copying sentences from elsewhere.
  - The section descriptions above are QUESTIONS to answer, not text to copy. Never
    echo a description back as your answer.
  - Every noun in your report must come from the conversation above. Nothing in
    these instructions is subject matter — do not borrow words from them.
`

// createSectionsMarker is the literal line DigestCreatePromptWithView writes immediately
// after the window (i.e. where the window ENDS) — named so createTailLen, the assembly
// below, and the backstop's window-extraction (assertPromptWithinBudget in digest_fit.go)
// all reference the identical string rather than three independently-typed copies that
// could drift apart the way the header constants already had to be unified for (task-7b
// finding (c)).
const createSectionsMarker = "\nWrite these sections:\n"

// createTailLen is the size of everything appended after the turns, so fitTurns budgets
// against the whole prompt rather than only the part built so far.
//
// RUNES, not bytes: task-7b fix round 3 (minor G) — len() on a string literal containing
// any multi-byte rune (this package's prose leans on em dashes throughout) counts bytes,
// while every budget this figure is compared against (DefaultPromptCharBudget,
// MinTurnChars) is a RUNE count, measured elsewhere with runeLen. The mismatch was
// described here as "conservative, never a floor or budget breach". Half of that was wrong,
// and the wrong half is the one that mattered: over-estimating overhead is conservative for
// the BUDGET and hostile to the FLOOR, because every rune of phantom overhead comes straight
// out of the window's room. Applied to the assembly's own prefix (b.Len(), bytes) the same
// mistake starved the floor on 2% of real mined transcripts — see runeLen in digest_fit.go.
func createTailLen() int {
	return runeLen(createSectionsMarker) + runeLen(digestSections) +
		runeLen(digestRules) + runeLen("\nRespond with JSON only.\n")
}

// createViewHeader and createWindowHeader are the literal headings
// DigestCreatePromptWithView writes immediately before the whole-session view and the
// recent-turns window, respectively. Defined once and used by BOTH the real assembly below
// and the room computation handed to clipSessionViewFor, so a wording change cannot
// silently reopen the header-omission bug fixed on the refine path in 1531ef0 (see
// beatsHeader, viewHeader and windowHeader in digest_refine.go for the same pattern — that
// fix accounts for all THREE of its headers, not just the view's, for exactly the reason
// createWindowHeader is included here too). This path needed its own constants rather than
// reusing the refine path's viewHeader/windowHeader because the wording differs ("so far"
// and a leading blank line the refine path's viewHeader does not have; "MOST RECENT PART OF
// THE CONVERSATION, in detail" instead of "NEW PART OF THE CONVERSATION (evidence)").
//
// clipSessionViewFor already reserves MinTurnChars for the turns that follow the view, but
// that reservation is only correct if it is a floor on the TURNS CONTENT alone — any header
// written between the view and the turns is overhead on top of it, the same distinction
// fitDiscretionary's doc draws for windowHeader. createWindowHeader is written between the
// view and turns just like windowHeader is on the refine path, so it needs the same
// accounting; omitting it (as the code did before this fix) left the window able to land up
// to len(createWindowHeader) below MinTurnChars even with createViewHeader fixed.
const createViewHeader = "\n\nWHOLE SESSION so far, sampled from start to now (coarse — for the shape" +
	" of the work, not its detail):\n"
const createWindowHeader = "\n\nMOST RECENT PART OF THE CONVERSATION, in detail:\n"

// DigestCreatePrompt builds the first-digest prompt with no whole-session view.
func DigestCreatePrompt(sessionLabel, turns, facts string) string {
	return DigestCreatePromptWithView(sessionLabel, turns, "", facts)
}

// DigestCreatePromptWithView is DigestCreatePrompt plus a coarse view of the WHOLE session.
//
// The miner has always built this view — SessionDigest samples six turns across the session
// — and it was never given to the digest prompt, which saw only the recent window. A
// synopsis written from the recent window alone can only describe the last thing discussed:
// a month-end close would be summarised as "clearing the suspense account". The view is what
// makes the leading question answerable.
func DigestCreatePromptWithView(sessionLabel, turns, sessionView, facts string) string {
	p, _ := createPromptAndWindow(sessionLabel, turns, sessionView, facts)
	return p
}

// createPromptAndWindow is DigestCreatePromptWithView's body, returning the conversation
// window fitTurns produced alongside the prompt.
//
// The window is returned rather than left to be recovered by landmark because landmark
// recovery is defeated by content quoting a heading — the defect fix round 4 removed from
// the backstop (see assertPromptWithinBudget's doc). The backstop already needed the real
// value; a MEASUREMENT needs it for the same reason, and the real-corpus probe measures
// window margins over transcripts of this harness's own development, in which every
// literal this assembly writes actually appears. Same seam on the refine path
// (updatePromptAndWindow).
func createPromptAndWindow(sessionLabel, turns, sessionView, facts string) (prompt, window string) {
	var b strings.Builder
	b.WriteString("You are writing a short report on a work session, for the person doing the work and for a manager who was not present.\n\n")
	b.WriteString("Session context: ")
	// Bounded here, not by mutating the caller's label — task-7b fix round 3 (minor G):
	// SessionLabel had no cap at all, and a pathological label (a caller passing a whole
	// paragraph, or worse) is fixed overhead ahead of everything else this function
	// budgets around; measured (DigestCreatePrompt, an otherwise-tiny turns/facts), a
	// 12,000-rune label alone produced a 15,954-rune prompt.
	// sessionLabelCap lives in digest_fit.go, shared with the refine path's identical fix.
	b.WriteString(clipProse(sessionLabel, sessionLabelCap))
	b.WriteString("\n\nMEASURED COUNTS (authoritative — your report must be consistent with these):\n  ")
	b.WriteString(facts)
	viewOverhead := runeLen(b.String()) + createTailLen() +
		runeLen(createViewHeader) + runeLen(createWindowHeader)
	if v := clipSessionViewFor(sessionView, viewOverhead); v != "" {
		b.WriteString(createViewHeader)
		b.WriteString(v)
	}
	b.WriteString(createWindowHeader)
	// Held in a variable so the backstop below measures the window fitTurns produced
	// instead of re-locating it by landmark — see assertPromptWithinBudget's doc, and the
	// identical line in DigestUpdatePromptFrom.
	window = fitTurns(turns, runeLen(b.String())+createTailLen())
	b.WriteString(window)
	b.WriteString(createSectionsMarker)
	b.WriteString(digestSections)
	b.WriteString(digestRules)
	b.WriteString("\nRespond with JSON only.\n")
	p := b.String()
	// The backstop (task-7b fix round 3, finding A): every prior round fixed a NAMED
	// leak, and every review since has found another one the fix rounds had not — the
	// retain-list, open-item count, TurningPoints, SessionLabel, omittedNotice. This is
	// what stops that pattern: whatever leak is still unfound, THIS is where it fails,
	// loudly, on the actually-assembled prompt, instead of shipping a prompt that
	// truncates mid-JSON and silently drops the digest. See assertPromptWithinBudget's
	// doc in digest_fit.go.
	assertPromptWithinBudget(p, window)
	return p, window
}

// CreateDigest produces the first digest for a session.
func (l *Llama) CreateDigest(sessionLabel, turns, facts string) (Digest, error) {
	return l.CreateDigestWithView(sessionLabel, turns, "", facts)
}

// CreateDigestWithView is CreateDigest given the coarse whole-session view.
func (l *Llama) CreateDigestWithView(sessionLabel, turns, sessionView, facts string) (Digest, error) {
	var d Digest
	if err := l.callValid(DigestCreatePromptWithView(sessionLabel, turns, sessionView, facts), DigestSchema(), &d,
		func() error { return firstProblem(ValidateDigest(d)) }); err != nil {
		return Digest{}, err
	}
	return d, nil
}

// DigestJSON renders a digest as JSON.
//
// It has NO callers. Its name and its former doc ("for embedding in a refine prompt") are the
// last trace of the scheme where a refinement was shown the previous report verbatim; nothing
// embeds a digest in a prompt now (see DigestUpdatePromptFrom), and no prompt builder in this
// package reads a Digest's prose at all — which is what makes prose length a question about
// the report's reader rather than about the context. Kept as a debugging/dump convenience,
// with the claim corrected rather than left to mislead the next reader of it.
func DigestJSON(d Digest) string {
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return fmt.Sprintf("{\"error\":%q}", err.Error())
	}
	return string(b)
}

// ValidateDigest reports structural problems a schema cannot express: empty prose
// where prose is required, and an unaddressed unresolved list.
//
// The unresolved check accepts an explicit "nothing is open" entry but rejects an
// empty list, because an empty list is exactly what a rubberstamping model produces.
func ValidateDigest(d Digest) []string {
	var p []string
	for _, f := range []struct {
		name, val string
	}{
		{"synopsis", d.Synopsis},
		{"done", d.Done}, {"happened", d.Happened}, {"structure", d.Structure},
		{"current", d.Current}, {"why", d.Why}, {"next", d.Next},
	} {
		if strings.TrimSpace(f.val) == "" {
			p = append(p, f.name+" is empty")
		}
	}
	if len(d.Unresolved) == 0 {
		p = append(p, "unresolved is empty — it must be addressed explicitly")
	}
	return p
}

// firstProblem turns ValidateDigest's report into a retryable error, so a digest that
// parses but is structurally unusable is re-requested rather than counted as a defect.
func firstProblem(problems []string) error {
	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("invalid digest: %s", strings.Join(problems, "; "))
}
