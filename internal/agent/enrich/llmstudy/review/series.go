package review

// A SERIES is the second thing this package packages, and it measures something no packet in
// round r1 could: whether a beat timeline can be read back as a narrative.
//
// r1 asks of one beat "is this honest?". That is a guard, and it is per-item by construction.
// The requirement is a property of the SEQUENCE — the owner's words: "we want a developer using
// claude code to be able to look back and follow the narrative of what they were doing and what
// was being worked on, along with references to product, repo, project and other specifics
// relevant to his company or endeavor." No per-item check can see it, and the two metrics are
// INDEPENDENT in both directions: thirty individually honest beats can have no thread, and a
// followable series can contain one defective beat. Nothing here folds a series verdict into a
// beat verdict or the reverse; the scorer reports the cross-tabulation and leaves it a table.

import (
	"fmt"
	"sort"
	"strings"
)

// SeriesBeat is one beat as it appears in a timeline under review.
//
// Ordinal is the source document's beat number and is PROVENANCE: it is never rendered, because
// a reader who can see that the third beat shown is really beat 11 has been handed the answer to
// the order-shuffle and dropped-middle plants. The rendered timeline is numbered by POSITION.
type SeriesBeat struct {
	Ordinal int
	Text    string
}

// SeriesRecord is the measured record for a WHOLE session.
//
// A beat packet carries its own beat's record block verbatim. A series cannot: there are as many
// record blocks as beats, and their counts are cumulative, so showing them would hand the reader
// the chronology in numbers — the order shuffle and the dropped middle would then be caught by
// reading turn counts rather than by reading the narrative, which is not what this metric is
// for. So the series record is derived, and every part of it is measured:
//
//   - Counts, Projects and ToolProfile are the LAST beat's lines, verbatim: the session totals as
//     counted at its end. Cumulative counts make the last block the whole-session block.
//   - Subjects is the UNION of the recurring-subject terms counted across the session,
//     alphabetised. Each term was counted on the machine; the union adds no term that was not.
//     Alphabetised because the per-block order is recency order, which is chronology again.
//
// It is derived from the ORIGINAL session in every case, including for a mutated series: the
// record is measured and a mutation may not touch it. That is also what makes several of the
// series-level plants checkable — an entity swapped throughout the beats still contradicts the
// project and subject terms the machine counted.
type SeriesRecord struct {
	Counts      string   // verbatim "counts: …" line
	Projects    string   // verbatim "projects: …" line
	ToolProfile string   // verbatim "tool profile: …" line; empty when the last block has none
	Subjects    []string // union across the session, alphabetised
	DerivedFrom int      // how many per-beat record blocks it was derived from
}

// Block renders the record as the fenced evidence a reviewer reads. Only measured lines are
// inside the fence; what the lines MEAN — session totals, a union, an order that carries no
// information — is said in the packet's prose, so nothing in the fence is a reformatting of a
// counted line that a reviewer's verbatim quote would then fail against.
func (r SeriesRecord) Block() string {
	var b strings.Builder
	if r.Counts != "" {
		b.WriteString(r.Counts + "\n")
	}
	if r.Projects != "" {
		b.WriteString(r.Projects + "\n")
	}
	if r.ToolProfile != "" {
		b.WriteString(r.ToolProfile + "\n")
	}
	if len(r.Subjects) > 0 {
		b.WriteString("recurring subjects: " + strings.Join(r.Subjects, ", ") + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// Series is one session's ordered beat timeline plus that session's measured record.
type Series struct {
	SessionTitle  string
	SessionDomain string
	Beats         []SeriesBeat
	Record        SeriesRecord
}

// Text is every beat's text joined, for absence checks. Never rendered.
func (s Series) Text() string {
	parts := make([]string, 0, len(s.Beats))
	for _, b := range s.Beats {
		parts = append(parts, b.Text)
	}
	return strings.Join(parts, "\n")
}

// Ordinals is the chronological beat number of each presented position, in presented order.
func (s Series) Ordinals() []int {
	out := make([]int, 0, len(s.Beats))
	for _, b := range s.Beats {
		out = append(out, b.Ordinal)
	}
	return out
}

// BuildSeries turns the parsed corpus into one Series per session, in document order.
//
// A session with fewer than two beats is not a timeline and is rejected rather than emitted as a
// one-item series that every dimension would pass trivially.
func BuildSeries(c Corpus) ([]Series, error) {
	var out []Series
	for _, s := range c.Sessions {
		if len(s.Items) < 2 {
			return nil, fmt.Errorf("session %q has %d beats; a series needs at least two", s.Title, len(s.Items))
		}
		ser := Series{SessionTitle: s.Title, SessionDomain: s.Domain}
		for _, it := range s.Items {
			ser.Beats = append(ser.Beats, SeriesBeat{Ordinal: it.Ordinal, Text: it.Output})
		}
		ser.Record = deriveSeriesRecord(s.Items)
		out = append(out, ser)
	}
	return out, nil
}

// FindSeries returns the series for a session title.
func FindSeries(all []Series, title string) (Series, error) {
	for _, s := range all {
		if s.SessionTitle == title {
			return s, nil
		}
	}
	return Series{}, fmt.Errorf("no series for session %q", title)
}

// deriveSeriesRecord implements the derivation documented on SeriesRecord.
func deriveSeriesRecord(items []Item) SeriesRecord {
	var r SeriesRecord
	seen := map[string]bool{}
	for _, it := range items {
		counts, projects, tools, subjects := recordLines(it.Record)
		if counts != "" || projects != "" || tools != "" || len(subjects) > 0 {
			r.DerivedFrom++
		}
		// Last block wins for the totals: the counts are cumulative.
		if counts != "" {
			r.Counts = counts
		}
		if projects != "" {
			r.Projects = projects
		}
		// Tool profile is cumulative too, but the first block has none, so an empty line must
		// not erase the one already recorded.
		if tools != "" {
			r.ToolProfile = tools
		}
		for _, s := range subjects {
			if !seen[s] {
				seen[s] = true
				r.Subjects = append(r.Subjects, s)
			}
		}
	}
	sort.Slice(r.Subjects, func(i, j int) bool {
		return strings.ToLower(r.Subjects[i]) < strings.ToLower(r.Subjects[j])
	})
	return r
}

// recordLines pulls the four lines a record block can carry, verbatim where they are rendered
// verbatim and split where they are unioned.
func recordLines(block string) (counts, projects, tools string, subjects []string) {
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "counts:"):
			counts = line
		case strings.HasPrefix(line, "projects:"):
			projects = line
		case strings.HasPrefix(line, "tool profile:"):
			tools = line
		case strings.HasPrefix(line, "recurring subjects:"):
			subjects = splitList(strings.TrimPrefix(line, "recurring subjects:"))
		}
	}
	return counts, projects, tools, subjects
}
