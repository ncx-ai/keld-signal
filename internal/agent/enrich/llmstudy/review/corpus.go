// Package review packages generated session statements for blind qualitative review and
// scores the verdicts that come back.
//
// It exists because every string heuristic on this branch that tried to encode a JUDGEMENT
// measured something else instead (findings Part 9, "What in this harness measures a fact,
// and what encodes a judgement"). Unverified identifiers flagged "Key", "Initial" and
// "e.g"; leak detection flagged only the sentinel the model is instructed to emit; plain
// plurals scored as fabrication; T1 reported 100% while silently dropping 5 of 20 digests.
// The judged half of the evaluation therefore moves to a reader — and a reader has to be
// calibrated like any other instrument, which is what the planted-defect and clean-duplicate
// items in this package are for.
//
// Two halves live here, and NEITHER of them performs a review:
//
//   - Packaging (corpus.go, mutate.go, packet.go, emit.go): parse the owner's
//     inputs-and-outputs record, mutate real outputs into planted-defect items, and emit one
//     blind packet per item plus a withheld answer key.
//   - Scoring (verdict.go, score.go): read the verdicts back and report calibration, false
//     positives, inter-reviewer disagreement, judge-versus-heuristic disagreement and
//     unevidenced verdicts — always as a count over its denominator, because moving
//     denominators are what made the earlier rounds unreadable.
package review

import (
	"fmt"
	"strings"
)

// Item is one generated statement together with the whole evidence its writer saw.
//
// Everything on Item that is NOT evidence — the session it came from, its ordinal, the
// window index, the heuristic's SUBJECT CHANGED annotation — is provenance, and provenance
// reaches the answer key only. A packet carries Record, Window and Output and nothing else
// (see renderPacket), because a reviewer who can see that an item is the fourth beat of a
// hand-authored accounting session is not reviewing the item.
type Item struct {
	SessionTitle  string
	SessionDomain string
	// Ordinal is the beat number within its session as the source document numbers it.
	Ordinal int
	// WindowIndex/WindowCount are the mined-window coordinates. They leak session length
	// and generation order, so they are provenance.
	WindowIndex int
	WindowCount int
	// MarkedSubjectChanged is what ChangedSubject decided on this beat in the run that
	// produced the document. Recorded, never recomputed here: it is one of the
	// judgement-class heuristics under comparison.
	MarkedSubjectChanged bool
	// Population is "real" or "synthetic" in the production-beat corpus and empty in the
	// fused-prompt corpus, which had no such split. Provenance: a reviewer told an item is
	// hand-authored reviews it differently, so it reaches the answer key only.
	Population string
	// DroppedEntries are the entries the anchoring guard removed from this beat before it was
	// stored, as the document prints them. Provenance for the same reason
	// MarkedSubjectChanged is: it is a mechanism's verdict on this item, and showing it would
	// ask a reviewer to agree with it.
	DroppedEntries []string

	Record string // the measured record block, verbatim
	Window string // the conversation window, verbatim
	Output string // the statement under review
}

// Session groups the items generated from one conversation.
type Session struct {
	Title  string
	Domain string
	Items  []Item
}

// Corpus is the parsed source document.
type Corpus struct {
	Sessions []Session
}

// Items flattens the corpus in document order.
func (c Corpus) Items() []Item {
	var out []Item
	for _, s := range c.Sessions {
		out = append(out, s.Items...)
	}
	return out
}

// Find returns the item with the given session title and ordinal. Mutations name their
// source that way rather than by index, so inserting a session cannot silently re-point a
// mutation at a different statement.
func (c Corpus) Find(sessionTitle string, ordinal int) (Item, error) {
	for _, s := range c.Sessions {
		if s.Title != sessionTitle {
			continue
		}
		for _, it := range s.Items {
			if it.Ordinal == ordinal {
				return it, nil
			}
		}
		return Item{}, fmt.Errorf("session %q has no beat %d", sessionTitle, ordinal)
	}
	return Item{}, fmt.Errorf("no session titled %q", sessionTitle)
}

// Preceding returns the items of the same session that come before it, in order. The
// beat-level heuristics under comparison (BeatSaysNothingNew, SubjectShifted) are defined
// against the accumulated series, so they cannot be run on a packet in isolation.
func (c Corpus) Preceding(it Item) []Item {
	for _, s := range c.Sessions {
		if s.Title != it.SessionTitle {
			continue
		}
		var out []Item
		for _, p := range s.Items {
			if p.Ordinal >= it.Ordinal {
				break
			}
			out = append(out, p)
		}
		return out
	}
	return nil
}

// ParseCorpus reads the inputs-and-outputs document.
//
// The parser is FENCE-AWARE and that is not a detail: real conversation windows contain
// lines beginning "## " and "# " (an assistant answer with headings is quoted verbatim
// inside the fence), so a scanner that keys on markdown headings alone splits a window in
// half and hands a reviewer a truncated one. Every structural marker is therefore only
// honoured outside a ``` fence.
//
// A beat with no output is SKIPPED, not carried as an empty item: the document records one
// generation failure ("GENERATION FAILED") and an item with nothing to review is not an
// item. Skipping is visible — the count is returned in Skipped.
func ParseCorpus(doc string) (Corpus, int, error) {
	var (
		c        Corpus
		skipped  int
		inFence  bool
		fenced   []string // lines of the fence currently open
		fences   [][]string
		quoted   []string
		cur      *Item
		curTitle string
		curDom   string
	)

	// flushItem closes the beat being read. It resets the accumulated fences and blockquote
	// UNCONDITIONALLY, including when there is no beat open: the document contains a
	// GENERATION FAILED block that carries its own fence and no beat, and leaving that fence
	// in the buffer made it the NEXT beat's measured record — a packet whose evidence belonged
	// to a different item. The fixture reproduces that shape and TestParseCorpusIsNotDerailed
	// pins it.
	flushItem := func() error {
		defer func() { cur, fences, quoted = nil, nil, nil }()
		if cur == nil {
			return nil
		}
		out := strings.TrimSpace(strings.Join(quoted, " "))
		if out == "" {
			skipped++
			return nil
		}
		if len(fences) < 2 {
			return fmt.Errorf("beat %d of %q: want 2 fenced inputs, got %d", cur.Ordinal, cur.SessionTitle, len(fences))
		}
		cur.Record = strings.Join(fences[0], "\n")
		cur.Window = strings.Join(fences[1], "\n")
		cur.Output = out
		if len(c.Sessions) == 0 {
			return fmt.Errorf("beat %d appears before any session heading", cur.Ordinal)
		}
		s := &c.Sessions[len(c.Sessions)-1]
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
			if err := flushItem(); err != nil {
				return Corpus{}, skipped, err
			}
			curTitle, curDom = strings.TrimSpace(strings.TrimPrefix(line, "# ")), ""
			// The document's own title precedes every session heading and owns no beats;
			// a session is opened lazily on its first beat instead of eagerly here.
		case strings.HasPrefix(line, "## "):
			if err := flushItem(); err != nil {
				return Corpus{}, skipped, err
			}
			h := strings.TrimSpace(strings.TrimPrefix(line, "## "))
			ord, wi, wc, changed, ok := parseBeatHeading(h)
			if !ok {
				// "Beat at window 20 — GENERATION FAILED" and anything else non-beat.
				if strings.HasPrefix(h, "Beat") {
					skipped++
				}
				continue
			}
			if len(c.Sessions) == 0 || c.Sessions[len(c.Sessions)-1].Title != curTitle {
				c.Sessions = append(c.Sessions, Session{Title: curTitle, Domain: curDom})
			}
			cur = &Item{
				SessionTitle:         curTitle,
				SessionDomain:        c.Sessions[len(c.Sessions)-1].Domain,
				Ordinal:              ord,
				WindowIndex:          wi,
				WindowCount:          wc,
				MarkedSubjectChanged: changed,
			}
		case strings.HasPrefix(line, "*") && curDom == "" && cur == nil:
			// "*Software · 72 mined windows*" — the domain label under a session heading.
			if d, ok := parseDomainLine(line); ok {
				curDom = d
			}
		case strings.HasPrefix(line, ">"):
			quoted = append(quoted, strings.TrimSpace(strings.TrimPrefix(line, ">")))
		}
	}
	if inFence {
		return Corpus{}, skipped, fmt.Errorf("document ends inside a ``` fence")
	}
	if err := flushItem(); err != nil {
		return Corpus{}, skipped, err
	}
	if len(c.Sessions) == 0 {
		return Corpus{}, skipped, fmt.Errorf("no sessions parsed")
	}
	return c, skipped, nil
}

// parseBeatHeading reads "Beat 3 (window 10 of 72) · marked SUBJECT CHANGED".
func parseBeatHeading(h string) (ordinal, windowIndex, windowCount int, changed bool, ok bool) {
	if !strings.HasPrefix(h, "Beat ") {
		return 0, 0, 0, false, false
	}
	changed = strings.Contains(h, "SUBJECT CHANGED")
	rest := strings.TrimPrefix(h, "Beat ")
	open := strings.Index(rest, "(")
	if open < 0 {
		return 0, 0, 0, false, false
	}
	if _, err := fmt.Sscanf(strings.TrimSpace(rest[:open]), "%d", &ordinal); err != nil {
		return 0, 0, 0, false, false
	}
	shut := strings.Index(rest, ")")
	if shut < open {
		return 0, 0, 0, false, false
	}
	if _, err := fmt.Sscanf(rest[open+1:shut], "window %d of %d", &windowIndex, &windowCount); err != nil {
		return 0, 0, 0, false, false
	}
	return ordinal, windowIndex, windowCount, changed, true
}

// parseDomainLine reads "*Software · 72 mined windows*" and returns "Software".
func parseDomainLine(line string) (string, bool) {
	s := strings.Trim(strings.TrimSpace(line), "*")
	if !strings.Contains(s, "mined windows") {
		return "", false
	}
	if i := strings.Index(s, "·"); i > 0 {
		return strings.TrimSpace(s[:i]), true
	}
	return "", false
}
