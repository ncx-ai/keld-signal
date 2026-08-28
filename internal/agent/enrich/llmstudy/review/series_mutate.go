package review

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// SeriesMutationClass is the kind of SERIES-LEVEL defect a planted timeline carries.
//
// None of r1's five classes appears here, and that is the point: a fabricated identifier, an
// invented blocker, an unobservable completion, a drifted subject and a sourceless specific are
// all properties of one beat, and planting them tests a per-beat reader. A series-level defect is
// one that leaves every beat individually true — or nearly so — and breaks the SEQUENCE.
type SeriesMutationClass string

const (
	// OrderShuffle reorders the beats so the chronology is wrong. Every beat stays byte-identical
	// and individually true; only the sequence lies.
	OrderShuffle SeriesMutationClass = "order_shuffle"
	// CrossSessionContamination splices in a beat from a DIFFERENT session's timeline.
	CrossSessionContamination SeriesMutationClass = "cross_session_contamination"
	// EntitySwap renames a repo, product or client consistently throughout to a plausible name
	// that appears nowhere in the evidence.
	EntitySwap SeriesMutationClass = "entity_swap"
	// DroppedMiddle removes the beats where the subject actually turned, leaving an unexplained
	// jump. Which beats those are is decided by the source document's own SUBJECT CHANGED
	// annotation, not by the author of the mutation.
	DroppedMiddle SeriesMutationClass = "dropped_middle"
	// InventedArc replaces the final beat with one asserting a conclusion the series never
	// reached. This is the series-level analogue of the per-beat completion defect, and the class
	// most likely to be invisible beat by beat: the beat is a plausible closing statement, and
	// only the series shows nothing led to it.
	InventedArc SeriesMutationClass = "invented_arc"
)

// SeriesMutationClasses is every class, in report order. The scorer iterates this rather than
// whatever the key happens to contain, so a class missing from a round is reported as a hole in
// the calibration instead of vanishing.
var SeriesMutationClasses = []SeriesMutationClass{
	OrderShuffle, CrossSessionContamination, EntitySwap, DroppedMiddle, InventedArc,
}

// LocationBy is how a reviewer can be credited with LOCATING a plant of this class.
//
// Two of the five classes introduce no text at all — a shuffle and a deletion write nothing — so
// there is no vocabulary to quote and location is by beat POSITION only. That is a real floor on
// the measurement (a reviewer who describes the break without naming a beat number scores as a
// miss), so it is recorded in the answer key per entry rather than left as an assumption.
const (
	LocateByPosition = "position"
	LocateByEither   = "position_or_signature"
)

// SwapPair is one consistent rename applied to every beat of a series.
type SwapPair struct {
	From string
	To   string
}

// SeriesMutation is one planted series-level defect, expressed as a transformation of a REAL
// timeline. Exactly one class's fields may be set; a series carrying two defects cannot tell you
// which one the reviewer saw.
type SeriesMutation struct {
	ID      string
	Class   SeriesMutationClass
	Session string // the host session, as the document titles it

	// OrderShuffle: the presented order, as source beat ordinals. A permutation of the session's
	// own ordinals, differing from chronological.
	Order []int

	// CrossSessionContamination: the donor beat and where it is spliced in (1-based presented
	// position, interior). Foreign names the tokens it brings that the host session cannot
	// support — verified absent here and present there.
	DonorSession string
	DonorOrdinal int
	InsertAt     int
	Foreign      []string

	// EntitySwap: renames applied to every beat, in order.
	Pairs []SwapPair

	// DroppedMiddle: the source ordinals removed. Contiguous, interior, and at least one of them
	// marked SUBJECT CHANGED by the source document.
	Remove []int

	// InventedArc: the statement that replaces the final beat.
	Replacement string

	// Note says what the defect is, in the answer key's words. Never rendered into a packet.
	Note string
}

// PlantedSeries is a mutated timeline with everything the answer key and the scorer need.
type PlantedSeries struct {
	Series     Series
	Source     Series
	Mutation   SeriesMutation
	Positions  []int
	Signature  []string
	LocationBy string
	// Removed / Replaced are recorded for the key so a missed plant can be reported with the
	// thing that was missed rather than only its class.
	Removed  []int
	Replaced string
}

// ApplySeries plants one series-level mutation, verifying every property its class claims.
//
// It fails loudly rather than emitting a plausible-looking timeline, for the same reason r1's
// Apply does: a plant whose claimed property does not hold is not a plant, it is a clean item
// mislabelled in the answer key, and it would score as a reviewer failure forever. The checks are
// per class and are described at each branch. Two hold for every class:
//
//   - the measured record is NEVER touched — it is taken from the original session — so a
//     mutation cannot make the record agree with it;
//   - exactly one class's fields are set.
func ApplySeries(c Corpus, all []Series, m SeriesMutation) (PlantedSeries, error) {
	src, err := FindSeries(all, m.Session)
	if err != nil {
		return PlantedSeries{}, fmt.Errorf("series mutation %s: %w", m.ID, err)
	}
	if m.Note == "" {
		return PlantedSeries{}, fmt.Errorf("series mutation %s: a note is required", m.ID)
	}
	if !seriesClassKnown(m.Class) {
		return PlantedSeries{}, fmt.Errorf("series mutation %s: unknown class %q", m.ID, m.Class)
	}
	if err := onlyOneClassConfigured(m); err != nil {
		return PlantedSeries{}, err
	}

	out := PlantedSeries{Source: src, Mutation: m}
	mut := Series{SessionTitle: src.SessionTitle, SessionDomain: src.SessionDomain, Record: src.Record}
	evidence := strings.ToLower(src.Text() + "\n" + src.Record.Block())
	sessionAll, err := sessionEvidence(c, m.Session)
	if err != nil {
		return PlantedSeries{}, fmt.Errorf("series mutation %s: %w", m.ID, err)
	}

	switch m.Class {
	case OrderShuffle:
		beats, positions, err := shuffleBeats(src, m)
		if err != nil {
			return PlantedSeries{}, err
		}
		mut.Beats = beats
		out.Positions, out.LocationBy = positions, LocateByPosition

	case CrossSessionContamination:
		beats, positions, err := spliceBeat(c, all, src, m, sessionAll)
		if err != nil {
			return PlantedSeries{}, err
		}
		mut.Beats = beats
		out.Positions, out.Signature, out.LocationBy = positions, m.Foreign, LocateByEither

	case EntitySwap:
		beats, positions, sig, err := swapEntity(c, src, m)
		if err != nil {
			return PlantedSeries{}, err
		}
		mut.Beats = beats
		out.Positions, out.Signature, out.LocationBy = positions, sig, LocateByEither

	case DroppedMiddle:
		beats, positions, removed, err := dropMiddle(c, src, m)
		if err != nil {
			return PlantedSeries{}, err
		}
		mut.Beats = beats
		out.Positions, out.Removed, out.LocationBy = positions, removed, LocateByPosition

	case InventedArc:
		beats, positions, sig, replaced, err := inventArc(src, m, evidence, sessionAll)
		if err != nil {
			return PlantedSeries{}, err
		}
		mut.Beats = beats
		out.Positions, out.Signature, out.Replaced, out.LocationBy = positions, sig, replaced, LocateByEither
	}

	if len(mut.Beats) < 3 {
		return PlantedSeries{}, fmt.Errorf("series mutation %s: leaves %d beats, too few to read as a timeline", m.ID, len(mut.Beats))
	}
	if len(out.Positions) == 0 {
		return PlantedSeries{}, fmt.Errorf("series mutation %s: names no beat position a reviewer could be credited for", m.ID)
	}
	for _, p := range out.Positions {
		if p < 1 || p > len(mut.Beats) {
			return PlantedSeries{}, fmt.Errorf("series mutation %s: position %d is outside the %d beats shown", m.ID, p, len(mut.Beats))
		}
	}
	out.Signature = distinctiveSignature(out.Signature)
	if out.LocationBy == LocateByEither && len(out.Signature) == 0 {
		return PlantedSeries{}, fmt.Errorf("series mutation %s: claims a quotable signature and has none left after the distinctiveness filter — write a replacement that introduces a name or number, not ordinary English", m.ID)
	}
	if mut.Record.Block() != src.Record.Block() {
		return PlantedSeries{}, fmt.Errorf("series mutation %s: the measured record changed, which no mutation may do", m.ID)
	}
	out.Series = mut
	return out, nil
}

// shuffleBeats reorders the timeline and returns the positions of every adjacent inversion.
//
// The positions are DERIVED from the permutation, not authored: a reviewer is credited for naming
// a beat at a junction where the presented order runs backwards, which is the only place the
// shuffle is visible without the transcript.
func shuffleBeats(src Series, m SeriesMutation) ([]SeriesBeat, []int, error) {
	if len(m.Order) != len(src.Beats) {
		return nil, nil, fmt.Errorf("series mutation %s: order has %d entries for %d beats", m.ID, len(m.Order), len(src.Beats))
	}
	byOrdinal := map[int]SeriesBeat{}
	for _, b := range src.Beats {
		byOrdinal[b.Ordinal] = b
	}
	seen := map[int]bool{}
	beats := make([]SeriesBeat, 0, len(m.Order))
	for _, ord := range m.Order {
		b, ok := byOrdinal[ord]
		if !ok {
			return nil, nil, fmt.Errorf("series mutation %s: beat %d is not in session %q", m.ID, ord, m.Session)
		}
		if seen[ord] {
			return nil, nil, fmt.Errorf("series mutation %s: beat %d appears twice; a shuffle is a permutation, nothing added and nothing lost", m.ID, ord)
		}
		seen[ord] = true
		beats = append(beats, b)
	}
	chronological := src.Ordinals()
	if sameInts(m.Order, chronological) {
		return nil, nil, fmt.Errorf("series mutation %s: the order is unchanged, so nothing is planted", m.ID)
	}
	var positions []int
	for i := 0; i+1 < len(m.Order); i++ {
		if m.Order[i] > m.Order[i+1] {
			positions = append(positions, i+1, i+2)
		}
	}
	if len(positions) == 0 {
		return nil, nil, fmt.Errorf("series mutation %s: the order differs but has no adjacent inversion, so there is no junction to name", m.ID)
	}
	return beats, dedupInts(positions), nil
}

// spliceBeat inserts a beat from another session and verifies it really is foreign: every token
// it is planted for is absent from EVERYTHING the host session produced, and present in the donor
// session. That is what separates contamination from a beat the host could plausibly have
// written.
func spliceBeat(c Corpus, all []Series, src Series, m SeriesMutation, hostEvidence string) ([]SeriesBeat, []int, error) {
	if m.DonorSession == m.Session {
		return nil, nil, fmt.Errorf("series mutation %s: contamination must come from a DIFFERENT session", m.ID)
	}
	donor, err := c.Find(m.DonorSession, m.DonorOrdinal)
	if err != nil {
		return nil, nil, fmt.Errorf("series mutation %s: %w", m.ID, err)
	}
	if m.InsertAt < 2 || m.InsertAt > len(src.Beats) {
		return nil, nil, fmt.Errorf("series mutation %s: insert position %d is not interior to %d beats", m.ID, m.InsertAt, len(src.Beats))
	}
	if len(m.Foreign) == 0 {
		return nil, nil, fmt.Errorf("series mutation %s: contamination is DEFINED by what the host cannot support, so it must name those tokens", m.ID)
	}
	donorEvidence, err := sessionEvidence(c, m.DonorSession)
	if err != nil {
		return nil, nil, fmt.Errorf("series mutation %s: %w", m.ID, err)
	}
	for _, tok := range m.Foreign {
		low := strings.ToLower(tok)
		if !strings.Contains(strings.ToLower(donor.Output), low) {
			return nil, nil, fmt.Errorf("series mutation %s: %q is not in the spliced beat, so the beat does not carry it", m.ID, tok)
		}
		if strings.Contains(hostEvidence, low) {
			return nil, nil, fmt.Errorf("series mutation %s: %q occurs in session %q itself, so the spliced beat is not foreign to it", m.ID, tok, m.Session)
		}
		if !strings.Contains(donorEvidence, low) {
			return nil, nil, fmt.Errorf("series mutation %s: %q is not in %q's evidence either", m.ID, tok, m.DonorSession)
		}
	}
	beats := make([]SeriesBeat, 0, len(src.Beats)+1)
	beats = append(beats, src.Beats[:m.InsertAt-1]...)
	beats = append(beats, SeriesBeat{Ordinal: -donor.Ordinal, Text: donor.Output})
	beats = append(beats, src.Beats[m.InsertAt-1:]...)
	return beats, []int{m.InsertAt}, nil
}

// swapEntity renames throughout and verifies both halves of "plausible but absent": the new name
// occurs NOWHERE in the corpus, and the old one occurs in the session's own measured record — so
// the swap contradicts something counted, not merely something remembered.
func swapEntity(c Corpus, src Series, m SeriesMutation) ([]SeriesBeat, []int, []string, error) {
	if len(m.Pairs) == 0 {
		return nil, nil, nil, fmt.Errorf("series mutation %s: an entity swap must name at least one rename", m.ID)
	}
	corpusAll := strings.ToLower(corpusEvidence(c))
	record := strings.ToLower(src.Record.Block())
	inRecord := false
	occurrences := 0
	for _, p := range m.Pairs {
		if p.From == "" || p.To == "" || p.From == p.To {
			return nil, nil, nil, fmt.Errorf("series mutation %s: a rename needs two different non-empty names", m.ID)
		}
		if strings.Contains(corpusAll, strings.ToLower(p.To)) {
			return nil, nil, nil, fmt.Errorf("series mutation %s: the substituted name %q occurs in the corpus, so it is not an absent name", m.ID, p.To)
		}
		for _, b := range src.Beats {
			occurrences += strings.Count(b.Text, p.From)
		}
		if strings.Contains(record, strings.ToLower(p.From)) {
			inRecord = true
		}
	}
	if occurrences < 2 {
		return nil, nil, nil, fmt.Errorf("series mutation %s: the name occurs %d time(s) in the beats; a CONSISTENT rename needs at least two", m.ID, occurrences)
	}
	if !inRecord {
		return nil, nil, nil, fmt.Errorf("series mutation %s: none of the renamed names is in the measured record, so the swap contradicts nothing counted and no reviewer could evidence it", m.ID)
	}
	var (
		beats     []SeriesBeat
		positions []int
		sig       []string
		seenSig   = map[string]bool{}
	)
	for i, b := range src.Beats {
		text := b.Text
		for _, p := range m.Pairs {
			text = strings.ReplaceAll(text, p.From, p.To)
		}
		if text != b.Text {
			positions = append(positions, i+1)
			if err := checkRegister(m.ID, b.Text, text); err != nil {
				return nil, nil, nil, err
			}
			for _, p := range m.Pairs {
				if strings.Contains(text, p.To) && !seenSig[p.To] {
					seenSig[p.To] = true
					sig = append(sig, p.To)
				}
			}
		}
		beats = append(beats, SeriesBeat{Ordinal: b.Ordinal, Text: text})
	}
	if len(positions) < 2 {
		return nil, nil, nil, fmt.Errorf("series mutation %s: the rename touched %d beat(s); a series-level swap must run through the timeline", m.ID, len(positions))
	}
	sort.Strings(sig)
	return beats, positions, sig, nil
}

// dropMiddle removes an interior, contiguous run of beats and verifies that the run is where the
// subject actually TURNED, using the source document's own SUBJECT CHANGED annotation. That
// annotation is recorded provenance from the run that produced the document, so "the beats where
// the subject turned" is not the mutation author's opinion.
func dropMiddle(c Corpus, src Series, m SeriesMutation) ([]SeriesBeat, []int, []int, error) {
	if len(m.Remove) == 0 {
		return nil, nil, nil, fmt.Errorf("series mutation %s: nothing to remove", m.ID)
	}
	remove := append([]int(nil), m.Remove...)
	sort.Ints(remove)
	for i := 1; i < len(remove); i++ {
		if remove[i] != remove[i-1]+1 {
			return nil, nil, nil, fmt.Errorf("series mutation %s: removed beats %v are not contiguous, so the series would have two gaps and a located verdict could not say which one was seen", m.ID, remove)
		}
	}
	first, last := src.Beats[0].Ordinal, src.Beats[len(src.Beats)-1].Ordinal
	turned := false
	for _, ord := range remove {
		if ord == first || ord == last {
			return nil, nil, nil, fmt.Errorf("series mutation %s: beat %d is the first or last of the session; a dropped MIDDLE is interior", m.ID, ord)
		}
		it, err := c.Find(m.Session, ord)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("series mutation %s: %w", m.ID, err)
		}
		if it.MarkedSubjectChanged {
			turned = true
		}
	}
	if !turned {
		return nil, nil, nil, fmt.Errorf("series mutation %s: none of the removed beats %v is marked SUBJECT CHANGED, so this removes a stretch of the same work rather than the turn", m.ID, remove)
	}
	dropped := map[int]bool{}
	for _, ord := range remove {
		dropped[ord] = true
	}
	var beats []SeriesBeat
	gapAfter := 0
	for _, b := range src.Beats {
		if dropped[b.Ordinal] {
			if gapAfter == 0 {
				gapAfter = len(beats) // presented position of the last beat before the gap
			}
			continue
		}
		beats = append(beats, b)
	}
	if gapAfter < 1 || gapAfter >= len(beats)+1 {
		return nil, nil, nil, fmt.Errorf("series mutation %s: the gap is not interior after removal", m.ID)
	}
	return beats, []int{gapAfter, gapAfter + 1}, remove, nil
}

// inventArc replaces the final beat with an asserted conclusion, and verifies that it IS one: the
// replacement uses the completion vocabulary the beat prompt itself forbids, introduces at least
// one word the session never produced anywhere (so a reviewer has something to quote), and stays
// in the length band and delimiter rule of the beat it replaces.
func inventArc(src Series, m SeriesMutation, evidence, sessionAll string) ([]SeriesBeat, []int, []string, string, error) {
	if strings.TrimSpace(m.Replacement) == "" {
		return nil, nil, nil, "", fmt.Errorf("series mutation %s: an invented arc needs a replacement final beat", m.ID)
	}
	last := src.Beats[len(src.Beats)-1]
	if m.Replacement == last.Text {
		return nil, nil, nil, "", fmt.Errorf("series mutation %s: the replacement is the original final beat", m.ID)
	}
	if !containsAny(m.Replacement, completionWords) {
		return nil, nil, nil, "", fmt.Errorf("series mutation %s: an invented arc must assert a conclusion (one of %v)", m.ID, completionWords)
	}
	if err := checkRegister(m.ID, last.Text, m.Replacement); err != nil {
		return nil, nil, nil, "", err
	}
	var sig []string
	for _, w := range signatureOf(src.Text(), m.Replacement) {
		if strings.Contains(evidence, w) || strings.Contains(sessionAll, w) {
			continue
		}
		sig = append(sig, w)
	}
	if len(sig) == 0 {
		return nil, nil, nil, "", fmt.Errorf("series mutation %s: the replacement introduces no word absent from the session, so the asserted conclusion is already in the evidence somewhere", m.ID)
	}
	beats := append([]SeriesBeat(nil), src.Beats[:len(src.Beats)-1]...)
	beats = append(beats, SeriesBeat{Ordinal: last.Ordinal, Text: m.Replacement})
	sort.Strings(sig)
	return beats, []int{len(beats)}, sig, last.Text, nil
}

// seriesSignatureFloor and seriesSignatureBanned keep ordinary English out of the located-the-defect
// signature.
//
// This is the exact defect this branch has paid for four times: unverified identifiers flagged
// "Key" and "e.g", plurals scored as fabrication. Here it would appear as a reviewer being CREDITED
// with locating a plant because they used the word "left" or "complete" — words the rubric itself
// puts in their mouth. So a signature token must be five runes or carry a digit or a separator (a
// name, a path, an amount), and the words a reviewer of a completion defect will inevitably write
// are excluded by name. A mutation left with no signature at all fails emission, which forces the
// author to introduce something quotable rather than letting the scorer credit noise.
const seriesSignatureFloor = 5

var seriesSignatureBanned = map[string]bool{
	"complete": true, "completed": true, "completion": true, "finished": true, "closed": true,
	"signed": true, "wrong": true, "missing": true, "unclear": true, "order": true, "series": true,
	"beats": true, "timeline": true, "jumps": true, "before": true, "after": true, "still": true,
	"which": true, "there": true, "their": true, "these": true, "those": true, "about": true,
	"would": true, "could": true, "should": true, "other": true, "being": true, "because": true,
}

func distinctiveSignature(in []string) []string {
	out := make([]string, 0, len(in))
	for _, tok := range in {
		t := strings.ToLower(strings.TrimSpace(tok))
		if t == "" || seriesSignatureBanned[t] {
			continue
		}
		if utf8.RuneCountInString(t) >= seriesSignatureFloor || strings.ContainsAny(t, "0123456789./-_") {
			out = append(out, tok)
		}
	}
	return out
}

// onlyOneClassConfigured refuses a mutation that sets fields belonging to another class. Two
// defects in one series cannot tell you which one the reviewer saw, and a stray field is the way
// that happens by accident.
func onlyOneClassConfigured(m SeriesMutation) error {
	set := map[SeriesMutationClass]bool{
		OrderShuffle:              len(m.Order) > 0,
		CrossSessionContamination: m.DonorSession != "" || m.DonorOrdinal != 0 || m.InsertAt != 0 || len(m.Foreign) > 0,
		EntitySwap:                len(m.Pairs) > 0,
		DroppedMiddle:             len(m.Remove) > 0,
		InventedArc:               m.Replacement != "",
	}
	if !set[m.Class] {
		return fmt.Errorf("series mutation %s: class %s has none of its own fields set", m.ID, m.Class)
	}
	for class, isSet := range set {
		if isSet && class != m.Class {
			return fmt.Errorf("series mutation %s: class is %s but the fields of %s are also set", m.ID, m.Class, class)
		}
	}
	return nil
}

func seriesClassKnown(c SeriesMutationClass) bool {
	for _, k := range SeriesMutationClasses {
		if k == c {
			return true
		}
	}
	return false
}

// corpusEvidence is every session's record, window and output, for the "absent from the corpus"
// half of an entity swap.
func corpusEvidence(c Corpus) string {
	var b strings.Builder
	for _, s := range c.Sessions {
		b.WriteString(s.Title)
		b.WriteByte('\n')
		for _, it := range s.Items {
			b.WriteString(it.Record)
			b.WriteByte('\n')
			b.WriteString(it.Window)
			b.WriteByte('\n')
			b.WriteString(it.Output)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func sameInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func dedupInts(in []int) []int {
	sort.Ints(in)
	out := in[:0:0]
	for i, n := range in {
		if i == 0 || in[i-1] != n {
			out = append(out, n)
		}
	}
	return out
}
