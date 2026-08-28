package review

import (
	"strconv"
	"strings"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich/llmstudy"
)

// The judgement-class heuristics run ONE MORE ROUND here, against the same items the reviewers
// judge, so that "judge versus heuristic" is a measurement rather than an argument. None of
// them is deleted or disabled: findings Part 9 lists them as unreliable, not as known-wrong,
// and retiring them is a decision this comparison informs.
//
// Four are computable from a packet's own content plus the session's earlier statements:
//
//	beat_contradicts_record  T12 — BeatContradictsRecord, including its abstention
//	subject_shifted          the anchor's stand-in trigger — SubjectShifted
//	beat_restates_previous   beatsRestate suppression — BeatSaysNothingNew
//	unverified_specifics     "is this token a specific" — UnverifiedIdentifiers over the
//	                         statement as prose, against window+record as the source
//
// The fifth, ChangedSubject, is NOT recomputed. It is decided on the accumulated series'
// grounded subject terms, and the grounded half comes from the mined Window struct, which a
// rendered window cannot reconstruct faithfully; re-deriving it from the text would compare
// the reader against a lookalike rather than against the flag the run actually set. So the
// document's own "marked SUBJECT CHANGED" annotation is recorded verbatim, and a planted item
// inherits its source's annotation — labelled as inherited, because a mutation can obviously
// change what a subject-change rule would say and this value is not a re-measurement of it.
const (
	heuristicContradicts = "beat_contradicts_record"
	heuristicShifted     = "subject_shifted"
	heuristicRestates    = "beat_restates_previous"
	heuristicSpecifics   = "unverified_specifics"
	heuristicChanged     = "changed_subject_recorded"
)

// HeuristicNames is every heuristic recorded, in report order.
var HeuristicNames = []string{
	heuristicContradicts, heuristicShifted, heuristicRestates, heuristicSpecifics, heuristicChanged,
}

// FlaggingVerdicts are the verdict words that count as "the heuristic objected to this item",
// per heuristic. Everything else is a non-objection, and abstention is neither: it is reported
// as its own column, because collapsing it into "clean" is exactly the defect that made T11's
// and T12's earlier rates unreadable.
var FlaggingVerdicts = map[string]string{
	heuristicContradicts: "flagged",
	heuristicShifted:     "shifted",
	heuristicRestates:    "restates",
	heuristicSpecifics:   "flagged",
	heuristicChanged:     "changed",
}

// AbstainVerdicts are the verdict words that mean the heuristic declined to judge.
var AbstainVerdicts = map[string]bool{"abstained": true, "no_prior_statement": true}

// runHeuristics records what each check says about one statement. prior is the same session's
// earlier GENUINE statements in order — the accumulated series the series-scoped checks are
// defined against.
func runHeuristics(it Item, statement string, prior []Item, changedRecorded, inherited bool) (map[string]string, map[string][]string) {
	verdicts := map[string]string{}
	detail := map[string][]string{}

	rec := recordFromBlock(it.Record)
	terms, checked := llmstudy.BeatContradictsRecord(statement, rec)
	switch {
	case !checked:
		verdicts[heuristicContradicts] = "abstained"
	case len(terms) > 0:
		verdicts[heuristicContradicts] = "flagged"
		detail[heuristicContradicts] = terms
	default:
		verdicts[heuristicContradicts] = "clean"
	}

	if len(prior) == 0 {
		verdicts[heuristicShifted] = "no_prior_statement"
		verdicts[heuristicRestates] = "no_prior_statement"
	} else {
		if llmstudy.SubjectShifted(prior[len(prior)-1].Output, statement) {
			verdicts[heuristicShifted] = "shifted"
		} else {
			verdicts[heuristicShifted] = "continuous"
		}
		beats := make([]llmstudy.Beat, 0, len(prior))
		for i, p := range prior {
			beats = append(beats, llmstudy.Beat{Ordinal: i + 1, Text: p.Output})
		}
		if llmstudy.BeatSaysNothingNew(statement, beats) {
			verdicts[heuristicRestates] = "restates"
		} else {
			verdicts[heuristicRestates] = "new"
		}
	}

	// UnverifiedIdentifiers is a Digest-shaped check; the statement is handed to it as prose,
	// which is what a beat is, and the source is the packet's own evidence. That is the same
	// gate T2 and T13 are computed with, so its flags here are comparable to theirs.
	if bad := llmstudy.UnverifiedIdentifiers(llmstudy.Digest{Synopsis: statement}, it.Window+"\n"+it.Record); len(bad) > 0 {
		verdicts[heuristicSpecifics] = "flagged"
		detail[heuristicSpecifics] = bad
	} else {
		verdicts[heuristicSpecifics] = "clean"
	}

	switch {
	case changedRecorded && inherited:
		verdicts[heuristicChanged] = "changed_inherited_from_source"
	case changedRecorded:
		verdicts[heuristicChanged] = "changed"
	case inherited:
		verdicts[heuristicChanged] = "unchanged_inherited_from_source"
	default:
		verdicts[heuristicChanged] = "unchanged"
	}
	return verdicts, detail
}

// recordFromBlock rebuilds the SessionRecord fields the checks read from the rendered record
// block. Only the fields the checks actually consult are populated — Subjects and Projects for
// BeatContradictsRecord, and the counts because they are cheap and make the entry legible.
// Anything else would be a fabricated measurement, which is the defect Task 5 of the rollup
// found in the compat shims (a record asserting turns=0 corrections=0 that digestRules then
// treated as measured truth).
func recordFromBlock(block string) llmstudy.SessionRecord {
	var r llmstudy.SessionRecord
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "counts:"):
			for _, f := range strings.Fields(strings.TrimPrefix(line, "counts:")) {
				k, v, ok := strings.Cut(f, "=")
				if !ok {
					continue
				}
				n, err := strconv.Atoi(v)
				if err != nil {
					continue
				}
				switch k {
				case "turns":
					r.Turns = n
				case "user_turns":
					r.UserTurns = n
				case "tool_calls":
					r.ToolCalls = n
				case "corrections":
					r.Corrections = n
				}
			}
		case strings.HasPrefix(line, "projects:"):
			r.Projects = splitList(strings.TrimPrefix(line, "projects:"))
		case strings.HasPrefix(line, "recurring subjects:"):
			r.Subjects = splitList(strings.TrimPrefix(line, "recurring subjects:"))
		}
	}
	return r
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
