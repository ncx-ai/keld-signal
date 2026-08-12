package review

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// Kind is what a packet really is. It lives in the answer key and nowhere else.
type Kind string

const (
	KindGenuine        Kind = "genuine"
	KindPlanted        Kind = "planted"
	KindCleanDuplicate Kind = "clean_duplicate"
)

// Packet is one item presented for judgement.
//
// The fields are the whole packet: the evidence the writer had, and the statement. Anything
// else a reviewer could key on — which session, which beat, which arm, which commit, whether
// the item is real — is provenance and lives only in the answer key. Two of those omissions
// are worth naming because the source document displays them prominently: the "window 25 of
// 72" coordinates (which leak session length and generation order) and the "marked SUBJECT
// CHANGED" annotation (which is one of the heuristic verdicts under comparison, so showing it
// would ask the reviewer to agree with it).
type Packet struct {
	ID     string
	Record string
	Window string
	Output string
}

// PacketIDs are derived from provenance under a salt, so they are stable across re-emissions
// of the same round and carry no order: sorting the emitted files by id shuffles genuine,
// planted and duplicate items together, which a running counter would not.
//
// The salt is a constant rather than random because a round that cannot be regenerated
// identically cannot be re-scored, and re-scoring is how a disputed verdict gets settled.
const packetIDSalt = "keld-review-packet-v1"

func packetID(kind Kind, sessionTitle string, ordinal int, tag string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%s|%d|%s", packetIDSalt, kind, sessionTitle, ordinal, tag)))
	return "PKT-" + strings.ToUpper(hex.EncodeToString(sum[:4]))
}

// renderPacket writes the markdown a reviewer is handed.
//
// The evidence is fenced verbatim, at a fence long enough to survive content that contains
// fences of its own — the windows are quoted transcript and one of them contains a markdown
// table, headings and a horizontal rule already.
func renderPacket(p Packet) string {
	var b strings.Builder
	b.WriteString("# Review packet " + p.ID + "\n\n")
	b.WriteString("One statement about one work session is under review. Below it is all the evidence\n")
	b.WriteString("its writer had: a measured record counted on the machine, and the slice of the\n")
	b.WriteString("conversation the writer was shown. There is nothing else to consult.\n\n")

	b.WriteString("## Evidence 1 — the measured record (counted, not written)\n\n")
	writeFenced(&b, p.Record)
	b.WriteString("\n## Evidence 2 — the conversation window, verbatim\n\n")
	writeFenced(&b, p.Window)
	b.WriteString("\n## The statement under review\n\n")
	for _, line := range strings.Split(strings.TrimSpace(p.Output), "\n") {
		b.WriteString("> " + line + "\n")
	}
	return b.String()
}

// ParsePacket reads a packet file back into its three parts.
//
// The scorer needs this to check that a quoted span really is in the EVIDENCE rather than in the
// statement — a reviewer who evidences a verdict by quoting the statement back has evidenced
// nothing, and a check that searched the whole file would accept it. Round-tripped by
// TestAPacketFileParsesBackToItsThreeParts.
func ParsePacket(body string) (Packet, error) {
	var p Packet
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "# Review packet ") {
			p.ID = strings.TrimSpace(strings.TrimPrefix(line, "# Review packet "))
			break
		}
	}
	if p.ID == "" {
		return Packet{}, fmt.Errorf("packet has no id line")
	}
	fences, statement, err := packetSections(body)
	if err != nil {
		return Packet{}, err
	}
	if len(fences) != 2 {
		return Packet{}, fmt.Errorf("packet has %d fenced evidence blocks, want 2", len(fences))
	}
	p.Record, p.Window, p.Output = fences[0], fences[1], statement
	return p, nil
}

// packetSections splits a rendered packet, honouring the variable-length fence renderPacket
// uses when the evidence itself contains a fence.
func packetSections(body string) (fences []string, statement string, err error) {
	var (
		open    string
		current []string
		inStmt  bool
		stmt    []string
	)
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
		if strings.HasPrefix(line, "## The statement under review") {
			inStmt = true
			continue
		}
		if inStmt && strings.HasPrefix(line, ">") {
			stmt = append(stmt, strings.TrimSpace(strings.TrimPrefix(line, ">")))
		}
	}
	if open != "" {
		return nil, "", fmt.Errorf("packet ends inside a fence")
	}
	if len(stmt) == 0 {
		return nil, "", fmt.Errorf("packet has no statement")
	}
	return fences, strings.Join(stmt, "\n"), nil
}

func writeFenced(b *strings.Builder, body string) {
	fence := "```"
	for strings.Contains(body, fence) {
		fence += "`"
	}
	b.WriteString(fence + "\n")
	b.WriteString(strings.TrimRight(body, "\n"))
	b.WriteString("\n" + fence + "\n")
}

// ManifestEntry is what the packets directory advertises about a packet: its id, its file and
// its digest. Deliberately not its kind, its source or its length band — the manifest sits
// beside the packets, so anything provenance-shaped in it is provenance a reviewer can read.
type ManifestEntry struct {
	ID     string `json:"id"`
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
}

// Manifest is the packets directory's index.
type Manifest struct {
	Round   string          `json:"round"`
	Packets []ManifestEntry `json:"packets"`
	Count   int             `json:"count"`
}

// KeyEntry is one packet's withheld provenance.
type KeyEntry struct {
	PacketID string `json:"packet_id"`
	Kind     Kind   `json:"kind"`

	SourceSession string `json:"source_session"`
	SourceOrdinal int    `json:"source_ordinal"`
	SourceWindow  string `json:"source_window_coords"`
	SourceDomain  string `json:"source_domain"`

	// DuplicateOf is the packet id carrying the same statement, for a clean duplicate. Both
	// packets are byte-identical apart from their id line, which is the point: a defect
	// claimed on one and not the other is one reviewer disagreeing with themselves.
	DuplicateOf string `json:"duplicate_of,omitempty"`

	MutationID    string        `json:"mutation_id,omitempty"`
	MutationClass MutationClass `json:"mutation_class,omitempty"`
	MutatedSpan   string        `json:"mutated_span,omitempty"`
	ReplacedSpan  string        `json:"replaced_span,omitempty"`
	// SpanRunes is the rune offset range of the planted span, present only for a planted item —
	// a fixed-size array would have written a misleading "0, 0" onto every clean entry.
	SpanRunes    []int    `json:"span_runes,omitempty"`
	AbsentTokens []string `json:"absent_tokens,omitempty"`
	Signature    []string `json:"signature,omitempty"`
	MutationNote string   `json:"mutation_note,omitempty"`

	// Heuristics is what the retired judgement-class string checks say about THIS packet's
	// statement, recorded at emission so the scorer can report where reader and heuristic
	// disagree. It is in the key rather than the packet for the obvious reason.
	Heuristics map[string]string `json:"heuristics"`
	// HeuristicDetail is what each flagging heuristic flagged. Kept because "look at the
	// actual flagged items before reporting a count" is the discipline this branch paid for:
	// every one of T12's flags turned out to be an accurate beat, and the rate alone could
	// never have said so.
	HeuristicDetail map[string][]string `json:"heuristic_detail,omitempty"`

	OutputRunes int `json:"output_runes"`
}

// AnswerKey is the withheld half of a round.
type AnswerKey struct {
	Round        string     `json:"round"`
	CorpusSHA256 string     `json:"corpus_sha256"`
	CorpusPath   string     `json:"corpus_path"`
	IDSalt       string     `json:"id_salt"`
	Entries      []KeyEntry `json:"entries"`

	// Counts is the round's composition, by kind and by mutation class, with the
	// denominators the scorer must report against.
	Counts KeyCounts `json:"counts"`

	// HeuristicNotes records what could and could not be recomputed, so a comparison is never
	// read as stronger than the derivation behind it.
	HeuristicNotes []string `json:"heuristic_notes,omitempty"`
}

// KeyCounts is the composition of a round.
type KeyCounts struct {
	Packets         int            `json:"packets"`
	Genuine         int            `json:"genuine"`
	Planted         int            `json:"planted"`
	CleanDuplicates int            `json:"clean_duplicates"`
	PlantedByClass  map[string]int `json:"planted_by_class"`
	CorpusItems     int            `json:"corpus_items"`
	CorpusSkipped   int            `json:"corpus_skipped"`
}

// LeakField is a key value excluded from the grep, with the reason. Dropping is visible here
// for the same reason omittedNotice exists: a check that silently narrows what it looks for is
// how "leak detection flagged only the sentinel the model is instructed to emit" happened.
type LeakField struct {
	Value  string `json:"value"`
	Reason string `json:"reason"`
}

// KeyLeakFields returns the key values every emitted packet is grepped for, and the values
// deliberately NOT grepped for, with reasons.
//
// Four kinds of value are excluded, and each exclusion is a real limit on the check:
//
//   - A packet id, including a duplicate's twin id: a packet prints its own id by design.
//   - The mutated span, its absent tokens and its signature: the planted statement contains
//     these BY CONSTRUCTION. That is the mutation. Grepping for them would fail on every
//     planted packet and prove nothing about withholding.
//   - The replaced (genuine) span: it is the genuine item's own statement.
//   - Heuristic verdict words ("flagged", "clean", "abstained") and anything under
//     minLeakToken runes: ordinary English that occurs in real transcripts, so a hit would be
//     noise rather than a leak. What guards those is the structural check instead — a packet
//     body must equal renderPacket over the record, window and statement alone, so no other
//     field can be in it whatever it says.
func KeyLeakFields(k AnswerKey) (checked []string, excluded []LeakField) {
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
	for _, e := range k.Entries {
		add(string(e.Kind))
		add(e.SourceSession)
		add(e.SourceWindow)
		add(e.SourceDomain)
		add(e.MutationID)
		add(string(e.MutationClass))
		add(e.MutationNote)
		skip(e.PacketID, "a packet prints its own id by design")
		skip(e.DuplicateOf, "the twin packet prints that id by design")
		skip(e.MutatedSpan, "the planted statement IS this span")
		skip(e.ReplacedSpan, "the genuine statement IS this span")
		for _, t := range e.AbsentTokens {
			skip(t, "introduced into the planted statement on purpose")
		}
		for _, t := range e.Signature {
			skip(t, "vocabulary of the planted statement")
		}
		for name, verdict := range e.Heuristics {
			add(name)
			skip(verdict, "a verdict word that also occurs in ordinary transcript text")
		}
		for _, toks := range e.HeuristicDetail {
			for _, t := range toks {
				skip(t, "a token the heuristic flagged, which it read OUT of the statement")
			}
		}
	}
	sort.Strings(checked)
	sort.Slice(excluded, func(i, j int) bool { return excluded[i].Value < excluded[j].Value })
	return dedup(checked), dedupFields(excluded)
}

// minLeakToken is the shortest string the leak check will look for.
const minLeakToken = 4

func dedupFields(in []LeakField) []LeakField {
	out := in[:0:0]
	seen := map[string]bool{}
	for _, f := range in {
		if !seen[f.Value] {
			seen[f.Value] = true
			out = append(out, f)
		}
	}
	return out
}

func dedup(in []string) []string {
	out := in[:0:0]
	seen := map[string]bool{}
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
