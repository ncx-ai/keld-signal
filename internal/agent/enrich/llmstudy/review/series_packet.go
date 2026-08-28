package review

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// SeriesKind is what a series packet really is. It lives in the answer key and nowhere else.
type SeriesKind string

const (
	KindSeriesClean     SeriesKind = "series_clean"
	KindSeriesPlanted   SeriesKind = "series_planted"
	KindSeriesDuplicate SeriesKind = "series_clean_duplicate"
)

// SeriesPacket is one timeline presented for judgement.
//
// The fields are the whole packet, and the list is deliberately shorter than a beat packet's: the
// measured record and the ordered beats. There is NO conversation window, and that is the
// metric's first requirement rather than an omission — `followable` asks whether a reader can
// reconstruct what happened WITHOUT the transcript, and a packet carrying the transcript cannot
// ask it.
//
// Everything else is provenance and lives only in the answer key: the session, the domain, each
// beat's real ordinal, the window coordinates, the arm, the commit, whether the series was
// mutated and how, and every per-beat verdict round r1 returned.
type SeriesPacket struct {
	ID     string
	Record string
	Beats  []string
}

const seriesPacketIDSalt = "keld-review-series-packet-v1"

// seriesPacketID derives a stable id from provenance under a fixed salt, exactly as the beat
// packets do: sorting the files by id shuffles clean, planted and duplicate together, and the
// same corpus regenerates the same round so a disputed verdict can be re-scored.
func seriesPacketID(kind SeriesKind, sessionTitle, tag string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%s|%s", seriesPacketIDSalt, kind, sessionTitle, tag)))
	return "SER-" + strings.ToUpper(hex.EncodeToString(sum[:4]))
}

// renderSeriesPacket writes the markdown a reviewer is handed.
//
// The beats are numbered by POSITION in the timeline shown, never by their real ordinal, and the
// numbering is what a reviewer names a break with — so it is part of the evidence contract, not
// decoration.
func renderSeriesPacket(p SeriesPacket) string {
	var b strings.Builder
	b.WriteString("# Series review packet " + p.ID + "\n\n")
	b.WriteString("One work session's timeline is under review: the short statements (\"beats\") that were\n")
	b.WriteString("written about it as it went, in the order they are shown, plus the record counted for that\n")
	b.WriteString("session on the machine. There is nothing else to consult — no transcript, and no other\n")
	b.WriteString("session.\n\n")

	b.WriteString("## Evidence 1 — the measured record (counted, not written)\n\n")
	b.WriteString("The counts, the project list and the tool profile are the session totals as counted at the\n")
	b.WriteString("end of the session. The recurring-subject list is every term counted anywhere in the\n")
	b.WriteString("session, alphabetised; its order carries no information.\n\n")
	writeFenced(&b, p.Record)

	b.WriteString("\n## Evidence 2 — the beat timeline, in the order shown\n\n")
	b.WriteString("Beats are numbered by their position in this timeline. Refer to them by that number.\n")
	for i, beat := range p.Beats {
		fmt.Fprintf(&b, "\n### Beat %d\n\n", i+1)
		for _, line := range strings.Split(strings.TrimSpace(beat), "\n") {
			b.WriteString("> " + line + "\n")
		}
	}
	return b.String()
}

// ParseSeriesPacket reads a packet file back into its parts.
//
// The scorer needs it to check a quoted span against the source the reviewer NAMED — the record or
// the series — because a check that searched the whole file could not tell the two apart, and
// "quote the record" and "quote the timeline" are different claims about where a fact came from.
func ParseSeriesPacket(body string) (SeriesPacket, error) {
	var p SeriesPacket
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "# Series review packet ") {
			p.ID = strings.TrimSpace(strings.TrimPrefix(line, "# Series review packet "))
			break
		}
	}
	if p.ID == "" {
		return SeriesPacket{}, fmt.Errorf("packet has no series id line")
	}
	var (
		open    string
		current []string
		fences  []string
		beats   []string
		cur     []string
		inBeat  bool
	)
	flushBeat := func() {
		if inBeat {
			beats = append(beats, strings.Join(cur, "\n"))
			cur, inBeat = nil, false
		}
	}
	for _, line := range strings.Split(body, "\n") {
		if open != "" {
			if strings.TrimRight(line, " ") == open {
				fences = append(fences, strings.Join(current, "\n"))
				open, current = "", nil
				continue
			}
			current = append(current, line)
			continue
		}
		if strings.HasPrefix(line, "```") {
			open = strings.TrimRight(line, " ")
			continue
		}
		if strings.HasPrefix(line, "### Beat ") {
			flushBeat()
			inBeat = true
			continue
		}
		if inBeat && strings.HasPrefix(line, ">") {
			cur = append(cur, strings.TrimSpace(strings.TrimPrefix(line, ">")))
		}
	}
	flushBeat()
	if open != "" {
		return SeriesPacket{}, fmt.Errorf("packet ends inside a fence")
	}
	if len(fences) != 1 {
		return SeriesPacket{}, fmt.Errorf("packet has %d fenced evidence blocks, want 1", len(fences))
	}
	if len(beats) < 2 {
		return SeriesPacket{}, fmt.Errorf("packet has %d beats, want at least 2", len(beats))
	}
	for i, beat := range beats {
		if strings.TrimSpace(beat) == "" {
			return SeriesPacket{}, fmt.Errorf("beat %d of the packet is empty", i+1)
		}
	}
	p.Record, p.Beats = fences[0], beats
	return p, nil
}

// SeriesKeyEntry is one series packet's withheld provenance.
type SeriesKeyEntry struct {
	PacketID string     `json:"packet_id"`
	Kind     SeriesKind `json:"kind"`

	SourceSession string `json:"source_session"`
	SourceDomain  string `json:"source_domain"`
	// BeatsPresented is how many beats the packet shows, and PresentedOrdinals is which real beat
	// each position is. Both are provenance: PresentedOrdinals IS the order-shuffle answer.
	BeatsPresented    int   `json:"beats_presented"`
	PresentedOrdinals []int `json:"presented_ordinals"`

	// DuplicateOf is the twin packet id for a clean duplicate. The two files are byte-identical
	// apart from the id line, so a break reported on one and not the other is one reviewer
	// disagreeing with themselves.
	DuplicateOf string `json:"duplicate_of,omitempty"`

	MutationID    string              `json:"mutation_id,omitempty"`
	MutationClass SeriesMutationClass `json:"mutation_class,omitempty"`
	// Positions are the presented beat numbers a reviewer must name to have LOCATED this plant.
	Positions []int `json:"positions,omitempty"`
	// Signature is the vocabulary the mutation introduced, where it introduced any. Order shuffle
	// and dropped middle introduce NO new text, so their signature is empty by construction and
	// they can only be located by position — recorded in LocationBy rather than left implicit.
	Signature  []string `json:"signature,omitempty"`
	LocationBy string   `json:"location_by,omitempty"`
	// RemovedOrdinals / SplicedFrom / SwapPairs / ReplacedBeat are the per-class details, present
	// only for the class that has them.
	RemovedOrdinals []int    `json:"removed_ordinals,omitempty"`
	SplicedFrom     string   `json:"spliced_from_session,omitempty"`
	SwapPairs       []string `json:"swap_pairs,omitempty"`
	ReplacedBeat    string   `json:"replaced_beat,omitempty"`
	MutationNote    string   `json:"mutation_note,omitempty"`

	RecordDerivedFrom int `json:"record_derived_from_blocks"`
	SeriesRunes       int `json:"series_runes"`
}

// SeriesAnswerKey is the withheld half of a series round.
type SeriesAnswerKey struct {
	Round        string           `json:"round"`
	Metric       string           `json:"metric"`
	CorpusSHA256 string           `json:"corpus_sha256"`
	CorpusPath   string           `json:"corpus_path"`
	IDSalt       string           `json:"id_salt"`
	Entries      []SeriesKeyEntry `json:"entries"`
	Counts       SeriesKeyCounts  `json:"counts"`
	Notes        []string         `json:"notes,omitempty"`
}

// SeriesKeyCounts is the round's composition with the denominators the scorer reports against.
type SeriesKeyCounts struct {
	Packets         int            `json:"packets"`
	Clean           int            `json:"clean"`
	Planted         int            `json:"planted"`
	CleanDuplicates int            `json:"clean_duplicates"`
	PlantedByClass  map[string]int `json:"planted_by_class"`
	// SourceSeries is how many REAL timelines the whole round was cut from. It is small, and the
	// scorer prints it beside every conclusion for that reason.
	SourceSeries    int            `json:"source_series"`
	SeriesBySession map[string]int `json:"packets_by_session"`
	CorpusItems     int            `json:"corpus_items"`
	CorpusSkipped   int            `json:"corpus_skipped"`
}

// SeriesKeyLeakFields returns the key values every emitted series packet is grepped for, and the
// values deliberately NOT grepped for, with a reason each.
//
// The exclusions are the same four kinds as the beat round's, for the same reasons: a packet
// prints its own id; a planted series CONTAINS its own introduced vocabulary by construction, so
// grepping for it would fail on every plant and prove nothing about withholding; the replaced
// beat is the clean series' own text; and anything under minLeakToken runes is ordinary English
// whose presence is noise. What guards those is the structural check — a packet body must equal a
// render over the record and the beats alone, which holds for every field including ones added
// later.
func SeriesKeyLeakFields(k SeriesAnswerKey) (checked []string, excluded []LeakField) {
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		if utf8.RuneCountInString(s) < minLeakToken {
			excluded = append(excluded, LeakField{s, "shorter than the leak-token floor: a hit would be ordinary text"})
			return
		}
		checked = append(checked, s)
	}
	skip := func(s, reason string) {
		if s = strings.TrimSpace(s); s != "" {
			excluded = append(excluded, LeakField{s, reason})
		}
	}
	add(k.CorpusPath)
	add(k.IDSalt)
	add(k.Metric)
	for _, e := range k.Entries {
		add(string(e.Kind))
		add(e.SourceSession)
		add(e.SourceDomain)
		add(e.MutationID)
		add(string(e.MutationClass))
		add(e.MutationNote)
		add(e.SplicedFrom)
		// LocationBy is "position" / "position_or_signature", and the packet's own instructions tell
		// the reviewer to refer to beats "by that number" — its position. Grepping for it flagged
		// every packet in the round on first run: ordinary English in the frame, not a leak. It is
		// excluded for the same reason r1 excludes heuristic verdict words, and the structural check
		// is what actually guarantees the field is not in the file.
		skip(e.LocationBy, "a mechanism word that also occurs in the packet's own instructions")
		skip(e.PacketID, "a packet prints its own id by design")
		skip(e.DuplicateOf, "the twin packet prints that id by design")
		skip(e.ReplacedBeat, "the clean series IS this text")
		for _, t := range e.Signature {
			skip(t, "introduced into the planted series on purpose")
		}
		for _, p := range e.SwapPairs {
			skip(p, "one half of the pair is the planted name, the other is the real one")
		}
		// The presented ordinals and the located-by positions are numbers, and a two-digit number
		// occurs in a transcript for a hundred innocent reasons. They are excluded as values and
		// guarded structurally instead: the packet is a render over the record and the beats, so
		// no ordinal can be in it.
		skip(intsToString(e.PresentedOrdinals), "the shuffle answer, guarded structurally: a bare number search would be noise")
		skip(intsToString(e.Positions), "the located-by answer, guarded structurally for the same reason")
		skip(intsToString(e.RemovedOrdinals), "the dropped-middle answer, guarded structurally for the same reason")
	}
	sort.Strings(checked)
	sort.Slice(excluded, func(i, j int) bool { return excluded[i].Value < excluded[j].Value })
	return dedup(checked), dedupFields(excluded)
}

func intsToString(in []int) string {
	if len(in) == 0 {
		return ""
	}
	parts := make([]string, 0, len(in))
	for _, n := range in {
		parts = append(parts, strconv.Itoa(n))
	}
	return strings.Join(parts, ",")
}
