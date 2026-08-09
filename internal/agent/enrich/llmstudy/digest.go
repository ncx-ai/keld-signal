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
	Done     string `json:"done"`
	Happened string `json:"happened"`
	// Structure is the cumulative view of what has been built or established and how
	// the pieces relate — architecture, for engineering work; the shape of the
	// process, for a finance close or a campaign. Named neutrally on purpose: it must
	// read sensibly for an accountant, not only an engineer.
	//
	// It is the section most exposed to both growth and drift, because unlike
	// insights it must be REVISED as understanding changes rather than only appended
	// to. Hence a larger prose cap and an explicit extend-and-revise instruction.
	Structure  string   `json:"structure"`
	Insights   []string `json:"insights"`
	Current    string   `json:"current"`
	Why        string   `json:"why"`
	Next       string   `json:"next"`
	Unresolved []string `json:"unresolved"`
}

// DigestSchema is the JSON schema every digest must satisfy.
func DigestSchema() map[string]any {
	str := map[string]any{"type": "string"}
	list := map[string]any{"type": "array", "items": map[string]any{"type": "string"}}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"done":       str,
			"happened":   str,
			"structure":  str,
			"insights":   list,
			"current":    str,
			"why":        str,
			"next":       str,
			"unresolved": list,
		},
		"required": []string{"done", "happened", "structure", "insights",
			"current", "why", "next", "unresolved"},
		"additionalProperties": false,
	}
}

// digestSections describes each field to the model. Deliberately profession-neutral:
// it names no code, tests or deploys, because the same digest must serve an
// accountant reconciling ledgers and a marketer drafting a campaign.
const digestSections = `  done        What concrete outcomes are now in place? Name them. Do not describe
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
    content in another.
  - The section descriptions above are QUESTIONS to answer, not text to copy. Never
    echo a description back as your answer.
  - Every noun in your report must come from the conversation above. Nothing in
    these instructions is subject matter — do not borrow words from them.
`

// DigestCreatePrompt builds the first-digest prompt.
func DigestCreatePrompt(sessionLabel, turns, facts string) string {
	var b strings.Builder
	b.WriteString("You are writing a short report on a work session, for the person doing the work and for a manager who was not present.\n\n")
	b.WriteString("Session context: ")
	b.WriteString(sessionLabel)
	b.WriteString("\n\nMEASURED COUNTS (authoritative — your report must be consistent with these):\n  ")
	b.WriteString(facts)
	b.WriteString("\n\nCONVERSATION:\n")
	b.WriteString(turns)
	b.WriteString("\nWrite these sections:\n")
	b.WriteString(digestSections)
	b.WriteString(digestRules)
	b.WriteString("\nRespond with JSON only.\n")
	return b.String()
}

// CreateDigest produces the first digest for a session.
func (l *Llama) CreateDigest(sessionLabel, turns, facts string) (Digest, error) {
	var d Digest
	if err := l.call(DigestCreatePrompt(sessionLabel, turns, facts), DigestSchema(), &d); err != nil {
		return Digest{}, err
	}
	return d, nil
}

// DigestJSON renders a digest for embedding in a refine prompt.
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
