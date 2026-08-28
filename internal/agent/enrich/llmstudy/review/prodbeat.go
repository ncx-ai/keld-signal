package review

// The production beat is a different OUTPUT SHAPE from the one round r1 judged, and everything in
// this file exists because of that one difference.
//
// r1's statement was prose: two or three sentences, one blockquote. The production beat is a
// SUBJECT line naming the work plus a list of observed EVENTS, one per line. The evidence either
// side of it is unchanged — the same measured record, the same conversation window — so the
// packaging, the calibration classes, the evidence requirement and the five rubric dimensions are
// r1's, reused rather than reinvented. A round scored on a different rubric than r1 cannot be put
// beside r1, and putting it beside r1 is the only reason this round exists.
//
// What is NOT carried over: the retired judgement-class string heuristics. r1 ran them one more
// round to measure them against a reader; that comparison has been made and the design under
// review deletes the checks it compared. Running them again over a bulleted statement would be a
// ninth measure of ordinary English, so the production round records no heuristic verdict at all
// and its scorer prints no heuristic table rather than a table of zeros.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// DefaultProdCorpusPath is the production-beat inputs-and-outputs record.
//
// Unlike r1's source document this one is TRACKED, so it can be required rather than tolerated —
// but the entry points still skip when it is missing rather than fail, so a checkout without it is
// diagnosable instead of merely red.
const DefaultProdCorpusPath = "docs/qwen-beat-inputs-and-outputs.md"

// ProdCorpusPathFromEnv resolves the source document. REVIEW_PROD_CORPUS overrides.
func ProdCorpusPathFromEnv(repoRoot string) string {
	if p := os.Getenv("REVIEW_PROD_CORPUS"); p != "" {
		return p
	}
	return repoRoot + "/" + DefaultProdCorpusPath
}

// LoadProdCorpus parses the document at path and returns it with its content digest. The digest is
// recorded in the answer key so a scored round is tied to the exact revision it was cut from.
func LoadProdCorpus(path string) (ProdCorpus, string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return ProdCorpus{}, "", err
	}
	sum := sha256.Sum256(b)
	p, err := ParseProdCorpus(string(b))
	if err != nil {
		return ProdCorpus{}, "", fmt.Errorf("%s: %w", path, err)
	}
	return p, hex.EncodeToString(sum[:]), nil
}

// ProdPopulation separates the real transcripts from the hand-authored pair.
//
// The document marks the two hand-authored sessions SYNTHETIC on every heading and reports every
// figure for both populations apart, because a figure averaged over both describes neither. The
// label is PROVENANCE here: it reaches the answer key and never a packet, since a reviewer told an
// item is invented reviews it differently.
type ProdPopulation string

const (
	PopulationReal      ProdPopulation = "real"
	PopulationSynthetic ProdPopulation = "synthetic"
)

// ProdFailure is a window that produced no beat at all.
//
// These are ABSENCES, and a round that scores only what exists cannot see them. They are parsed,
// counted and printed in the round's own README and in the score, because the design's enforcement
// converted a class of stored fabrication into lost beats and the trade is only visible if both
// halves are on the page.
type ProdFailure struct {
	Session     string         `json:"session"`
	Population  ProdPopulation `json:"population"`
	WindowIndex int            `json:"window_index"`
	Attempts    int            `json:"attempts"`
	// Reason is the harness's own error text, verbatim.
	Reason string `json:"reason"`
	// Rule is the failure classified: "subject_unanchored" when the subject-anchoring ladder ran
	// out, "entry_cap" when a single entry ran past the entry cap, "other" otherwise. Derived from
	// Reason by exact substrings the harness emits, and the derivation is checked against the
	// document's own tally (see ProdRunCounts).
	Rule string `json:"rule"`
}

const (
	FailureSubjectUnanchored = "subject_unanchored"
	FailureEntryCap          = "entry_cap"
	FailureOther             = "other"
)

// ProdRunCounts are the two figures from the document's own tally that MUST ride into the report as
// caveats rather than be discovered later.
//
// Both are limits on what any quality number from this corpus can mean:
//
//   - UnconstrainedEntries of KeptEntries name no specific at all, so the anchoring guard had
//     nothing to check on them. A low drop count is not evidence of grounding, and the guard's
//     reach is the denominator that says so.
//   - SubjectLadderLosses windows produced no beat because subject anchoring could not be
//     satisfied. Absences.
//
// They are PARSED from the document rather than typed in here, so they cannot go stale against the
// artifact they describe, and parsing fails loudly when the lines are not found.
type ProdRunCounts struct {
	KeptEntries           int `json:"kept_entries"`
	UnconstrainedEntries  int `json:"entries_naming_no_specific"`
	SubjectLadderLosses   int `json:"windows_lost_to_subject_anchoring"`
	BeatsAsked            int `json:"beats_asked"`
	BeatsGenerated        int `json:"beats_generated"`
	BeatsFailed           int `json:"beats_failed"`
	EntriesDroppedByGuard int `json:"entries_dropped_by_the_anchoring_guard"`
}

// ProdCorpus is the parsed production-beat document.
type ProdCorpus struct {
	Corpus
	// Failures are the windows that produced no beat, in document order.
	Failures []ProdFailure
	// Counts are the document's own tally lines.
	Counts ProdRunCounts
	// Population is each session's population, by session title.
	Population map[string]ProdPopulation
}

// SessionsBy returns the session titles of one population, in document order.
func (p ProdCorpus) SessionsBy(pop ProdPopulation) []string {
	var out []string
	for _, s := range p.Corpus.Sessions {
		if p.Population[s.Title] == pop {
			out = append(out, s.Title)
		}
	}
	return out
}

// ItemsBy returns the beats of one population, in document order.
func (p ProdCorpus) ItemsBy(pop ProdPopulation) []Item {
	var out []Item
	for _, it := range p.Corpus.Items() {
		if ProdPopulation(it.Population) == pop {
			out = append(out, it)
		}
	}
	return out
}

// FailuresBy returns the lost windows of one rule.
func (p ProdCorpus) FailuresBy(rule string) []ProdFailure {
	var out []ProdFailure
	for _, f := range p.Failures {
		if f.Rule == rule {
			out = append(out, f)
		}
	}
	return out
}

// ProdSubject and ProdEvents split a stored statement back into its two parts.
//
// The statement is held on Item.Output as the subject line followed by one "- " line per event,
// which is exactly how the document prints it and exactly how a packet renders it. Splitting is
// needed by the register check, which has to know that a mutation left the shape alone.
func ProdSubject(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) == 0 {
		return ""
	}
	return strings.TrimSpace(lines[0])
}

// ProdEvents returns the event lines with their "- " markers stripped.
func ProdEvents(output string) []string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	var out []string
	for _, l := range lines[min(1, len(lines)):] {
		out = append(out, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "- ")))
	}
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ParseProdCorpus reads the production-beat inputs-and-outputs document.
//
// It is a SEPARATE parser from ParseCorpus rather than a flag on it, because four things differ and
// each of them would be a silent corruption if the wrong one ran:
//
//  1. the statement is a subject line plus bullets and must be joined on NEWLINES. ParseCorpus
//     joins a blockquote with spaces, which is right for prose and would flatten a list into one
//     unreadable line here;
//  2. the blockquote can carry a DROP MARKER — "[1 entry dropped: …]" — which is the anchoring
//     guard's verdict on the beat. It is the exact analogue of r1's "marked SUBJECT CHANGED"
//     annotation and is withheld for the same reason: it is a mechanism under comparison, and
//     showing it would ask a reviewer to agree with it;
//  3. a window that produced no beat is recorded as a heading plus one fence holding the harness's
//     error, and it carries no record and no window. It is a FAILURE, not a skipped beat, and it is
//     captured rather than counted;
//  4. sessions carry a population label, and the prose that follows a beat's output (what each
//     entry was checked on, which identifiers were recorded, which entries were dropped) is the
//     harness's own annotation and is not part of the statement.
//
// Fence-awareness is not a detail here either: a conversation window contains lines beginning "## "
// and "# " — assistant answers with headings are quoted verbatim inside the fence — so every
// structural marker is honoured only outside a ``` fence.
func ParseProdCorpus(doc string) (ProdCorpus, error) {
	p := ProdCorpus{Population: map[string]ProdPopulation{}}

	var (
		inFence   bool
		fenced    []string
		fences    [][]string
		quoted    []string
		dropped   []string
		cur       *Item
		curTitle  string
		curPop    ProdPopulation
		curFail   *ProdFailure
		inOutput  bool
		sawRecord bool
		sawWindow bool
	)

	flush := func() error {
		defer func() {
			cur, curFail, fences, quoted, dropped = nil, nil, nil, nil, nil
			inOutput, sawRecord, sawWindow = false, false, false
		}()
		if curFail != nil {
			if len(fences) != 1 {
				return fmt.Errorf("%s: generation failure at window %d has %d fenced blocks, want 1",
					curFail.Session, curFail.WindowIndex, len(fences))
			}
			curFail.Reason = strings.TrimSpace(strings.Join(fences[0], "\n"))
			curFail.Rule = classifyFailure(curFail.Reason)
			p.Failures = append(p.Failures, *curFail)
			return nil
		}
		if cur == nil {
			return nil
		}
		if !sawRecord || !sawWindow || len(fences) != 2 {
			return fmt.Errorf("beat %d of %q: want a record fence and a window fence, got %d fences (record=%v window=%v)",
				cur.Ordinal, cur.SessionTitle, len(fences), sawRecord, sawWindow)
		}
		if len(quoted) == 0 {
			return fmt.Errorf("beat %d of %q: no output", cur.Ordinal, cur.SessionTitle)
		}
		cur.Record = strings.Join(fences[0], "\n")
		cur.Window = strings.Join(fences[1], "\n")
		cur.Output = strings.Join(quoted, "\n")
		cur.DroppedEntries = dropped
		if len(p.Corpus.Sessions) == 0 {
			return fmt.Errorf("beat %d appears before any session heading", cur.Ordinal)
		}
		s := &p.Corpus.Sessions[len(p.Corpus.Sessions)-1]
		s.Items = append(s.Items, *cur)
		return nil
	}

	for _, line := range strings.Split(doc, "\n") {
		if strings.HasPrefix(line, "```") {
			if inFence {
				fences = append(fences, fenced)
				fenced, inFence = nil, false
			} else {
				inFence = true
			}
			continue
		}
		if inFence {
			fenced = append(fenced, line)
			continue
		}

		switch {
		case strings.HasPrefix(line, "# "):
			if err := flush(); err != nil {
				return ProdCorpus{}, err
			}
			curTitle, curPop = strings.TrimSpace(strings.TrimPrefix(line, "# ")), ""

		case strings.HasPrefix(line, "## "):
			if err := flush(); err != nil {
				return ProdCorpus{}, err
			}
			h := strings.TrimSpace(strings.TrimPrefix(line, "## "))
			if f, ok := parseProdFailureHeading(h); ok {
				f.Session, f.Population = curTitle, curPop
				curFail = &f
				continue
			}
			ord, wi, wc, ok := parseProdBeatHeading(h)
			if !ok {
				continue
			}
			if len(p.Corpus.Sessions) == 0 || p.Corpus.Sessions[len(p.Corpus.Sessions)-1].Title != curTitle {
				p.Corpus.Sessions = append(p.Corpus.Sessions, Session{Title: curTitle, Domain: string(curPop)})
			}
			cur = &Item{
				SessionTitle: curTitle, SessionDomain: string(curPop),
				Ordinal: ord, WindowIndex: wi, WindowCount: wc,
				Population: string(curPop),
			}

		case strings.HasPrefix(line, "### "):
			h := strings.TrimSpace(strings.TrimPrefix(line, "### "))
			switch {
			case strings.HasPrefix(h, "Input 1"):
				sawRecord = true
			case strings.HasPrefix(h, "Input 2"):
				sawWindow = true
			case h == "Output":
				inOutput = true
			}

		case strings.HasPrefix(line, "*") && curPop == "" && cur == nil && curFail == nil:
			if pop, ok := parseProdPopulationLine(line); ok {
				curPop = pop
				p.Population[curTitle] = pop
			}

		case strings.HasPrefix(line, ">"):
			if !inOutput {
				continue
			}
			body := strings.TrimSpace(strings.TrimPrefix(line, ">"))
			if body == "" {
				continue
			}
			// The drop marker is the anchoring guard's verdict on this beat, not part of the
			// statement. Recorded on the item and withheld from the packet.
			if strings.HasPrefix(body, "[") && strings.Contains(body, "dropped") {
				dropped = append(dropped, body)
				continue
			}
			quoted = append(quoted, body)
		}
	}
	if inFence {
		return ProdCorpus{}, fmt.Errorf("document ends inside a ``` fence")
	}
	if err := flush(); err != nil {
		return ProdCorpus{}, err
	}
	if len(p.Corpus.Sessions) == 0 {
		return ProdCorpus{}, fmt.Errorf("no sessions parsed")
	}

	counts, err := parseProdRunCounts(doc)
	if err != nil {
		return ProdCorpus{}, err
	}
	p.Counts = counts
	if err := p.checkAgainstItsOwnTally(); err != nil {
		return ProdCorpus{}, err
	}
	return p, nil
}

// checkAgainstItsOwnTally is what stops this parser from silently disagreeing with the artifact it
// is reading. The document counts its own beats and its own losses; if the parse disagrees, one of
// the two is wrong and neither may be reported.
func (p ProdCorpus) checkAgainstItsOwnTally() error {
	if got, want := len(p.Corpus.Items()), p.Counts.BeatsGenerated; got != want {
		return fmt.Errorf("parsed %d beats but the document's own tally says %d generated", got, want)
	}
	if got, want := len(p.Failures), p.Counts.BeatsFailed; got != want {
		return fmt.Errorf("parsed %d generation failures but the document's own tally says %d", got, want)
	}
	if got, want := len(p.FailuresBy(FailureSubjectUnanchored)), p.Counts.SubjectLadderLosses; got != want {
		return fmt.Errorf("parsed %d windows lost to subject anchoring but the tally says %d", got, want)
	}
	if p.Counts.KeptEntries == 0 || p.Counts.UnconstrainedEntries == 0 {
		return fmt.Errorf("the guard-reach caveat parsed as %d of %d, which cannot be right",
			p.Counts.UnconstrainedEntries, p.Counts.KeptEntries)
	}
	return nil
}

// parseProdBeatHeading reads "Beat 3 (window 14 of 34) · 1 attempt(s)".
func parseProdBeatHeading(h string) (ordinal, windowIndex, windowCount int, ok bool) {
	if !strings.HasPrefix(h, "Beat ") {
		return 0, 0, 0, false
	}
	rest := strings.TrimPrefix(h, "Beat ")
	open := strings.Index(rest, "(")
	if open < 0 {
		return 0, 0, 0, false
	}
	if _, err := fmt.Sscanf(strings.TrimSpace(rest[:open]), "%d", &ordinal); err != nil {
		return 0, 0, 0, false
	}
	shut := strings.Index(rest, ")")
	if shut < open {
		return 0, 0, 0, false
	}
	if _, err := fmt.Sscanf(rest[open+1:shut], "window %d of %d", &windowIndex, &windowCount); err != nil {
		return 0, 0, 0, false
	}
	return ordinal, windowIndex, windowCount, true
}

// parseProdFailureHeading reads "Beat at window 4 — GENERATION FAILED after 5 attempt(s)".
func parseProdFailureHeading(h string) (ProdFailure, bool) {
	if !strings.HasPrefix(h, "Beat at window ") || !strings.Contains(h, "GENERATION FAILED") {
		return ProdFailure{}, false
	}
	var f ProdFailure
	rest := strings.TrimPrefix(h, "Beat at window ")
	numEnd := strings.IndexFunc(rest, func(r rune) bool { return r < '0' || r > '9' })
	if numEnd <= 0 {
		return ProdFailure{}, false
	}
	n, err := strconv.Atoi(rest[:numEnd])
	if err != nil {
		return ProdFailure{}, false
	}
	f.WindowIndex = n
	if i := strings.Index(h, "after "); i >= 0 {
		fmt.Sscanf(h[i:], "after %d attempt", &f.Attempts)
	}
	return f, true
}

// classifyFailure reads the harness's own error text. The two substrings are the harness's, not
// this package's paraphrase, and the classification is cross-checked against the document's tally.
func classifyFailure(reason string) string {
	switch {
	case strings.Contains(reason, "subject line carries no term"):
		return FailureSubjectUnanchored
	case strings.Contains(reason, "over the cap of"):
		return FailureEntryCap
	default:
		return FailureOther
	}
}

// parseProdPopulationLine reads "*real transcript (engineering) · 34 mined windows*" and
// "*SYNTHETIC — hand-authored non-engineering · 20 mined windows*".
func parseProdPopulationLine(line string) (ProdPopulation, bool) {
	s := strings.Trim(strings.TrimSpace(line), "*")
	if !strings.Contains(s, "mined windows") {
		return "", false
	}
	if strings.HasPrefix(s, "SYNTHETIC") {
		return PopulationSynthetic, true
	}
	if strings.HasPrefix(s, "real transcript") {
		return PopulationReal, true
	}
	return "", false
}

// parseProdRunCounts reads the document's own "What was generated" tally.
//
// Only the OVERALL block is read — the document repeats every figure for each population, and the
// first occurrence of each line is the overall one. Each line is required: a caveat that silently
// went missing would be a caveat this round stopped carrying.
func parseProdRunCounts(doc string) (ProdRunCounts, error) {
	var c ProdRunCounts
	var haveKept, haveLadder, haveSessions, haveGuard bool
	for _, line := range strings.Split(doc, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case !haveKept && strings.HasPrefix(line, "- kept entries naming NO specific"):
			if _, err := fmt.Sscanf(afterColon(line), "%d of %d kept", &c.UnconstrainedEntries, &c.KeptEntries); err != nil {
				return c, fmt.Errorf("the guard-reach line does not parse: %q", line)
			}
			haveKept = true
		case !haveLadder && strings.HasPrefix(line, "- beats re-requested for an unanchored SUBJECT"):
			i := strings.Index(line, "still failing after the ladder:")
			if i < 0 {
				return c, fmt.Errorf("the subject-ladder line names no loss count: %q", line)
			}
			if _, err := fmt.Sscanf(line[i:], "still failing after the ladder: %d", &c.SubjectLadderLosses); err != nil {
				return c, fmt.Errorf("the subject-ladder loss count does not parse: %q", line)
			}
			haveLadder = true
		case !haveSessions && strings.HasPrefix(line, "- sessions "):
			if _, err := fmt.Sscanf(line, "- sessions %d; beats asked %d, generated %d, failed %d",
				new(int), &c.BeatsAsked, &c.BeatsGenerated, &c.BeatsFailed); err != nil {
				return c, fmt.Errorf("the beats-asked line does not parse: %q", line)
			}
			haveSessions = true
		case !haveGuard && strings.HasPrefix(line, "- entries dropped by the anchoring guard"):
			if _, err := fmt.Sscanf(afterColon(line), "%d of", &c.EntriesDroppedByGuard); err != nil {
				return c, fmt.Errorf("the guard-drop line does not parse: %q", line)
			}
			haveGuard = true
		}
	}
	if !haveKept || !haveLadder || !haveSessions || !haveGuard {
		return c, fmt.Errorf("the document's own tally is missing a required line (kept=%v ladder=%v sessions=%v guard=%v)",
			haveKept, haveLadder, haveSessions, haveGuard)
	}
	return c, nil
}

func afterColon(line string) string {
	if i := strings.LastIndex(line, ": "); i >= 0 {
		return strings.TrimSpace(line[i+2:])
	}
	return line
}
