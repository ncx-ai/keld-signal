package review

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

//go:embed reviewer-dispatch.md
var dispatchFS embed.FS

// DispatchPrompt is the reviewer prompt, embedded so the emitted round carries a copy of the
// exact text that was current when it was cut. A round scored against a different rubric than
// it was reviewed under is a different measurement.
func DispatchPrompt() string {
	b, err := dispatchFS.ReadFile("reviewer-dispatch.md")
	if err != nil {
		panic("review: embedded dispatch prompt missing: " + err.Error())
	}
	return string(b)
}

// ReviewersPerPacket is how many independent reviewers each packet is dispatched to.
// Disagreement between two readings of the same evidence is a measurement of the instrument,
// and with one reviewer per packet there is nothing to compare.
const ReviewersPerPacket = 2

// ReviewerIDs are the slots, in dispatch order.
var ReviewerIDs = []string{"A", "B"}

// Emission is what a round came out as. Counts carry their denominators because a rate with a
// moving denominator is what made three earlier rounds of this study unreadable.
type Emission struct {
	Round      string
	Dir        string
	PacketsDir string
	KeyPath    string
	Manifest   Manifest
	Key        AnswerKey
	// Leak is the withholding check over the files that were just written: what was grepped
	// for, what was deliberately not grepped for and why, and what was found.
	Leak leakReport
}

// Emit cuts a round: it plants every mutation, duplicates the clean controls, writes one packet
// per item plus a manifest, writes the answer key OUTSIDE the packets directory, copies the
// reviewer prompt, and greps the packets it just wrote for every answer-key value it can
// legitimately look for.
//
// Emission is deterministic given a corpus: the ids are salted hashes of provenance and the
// files are written in id order, so the same corpus produces the same round byte for byte, and
// a disputed verdict can be re-scored against a regenerated round.
func Emit(dir, round string, c Corpus, corpusPath, corpusSHA string, corpusSkipped int) (Emission, error) {
	if round == "" {
		round = "r1"
	}
	packetsDir := filepath.Join(dir, "packets")
	withheldDir := filepath.Join(dir, "withheld")
	verdictsDir := filepath.Join(dir, "verdicts")
	for _, d := range []string{packetsDir, withheldDir, verdictsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return Emission{}, err
		}
	}

	type built struct {
		packet Packet
		entry  KeyEntry
	}
	var all []built
	add := func(p Packet, e KeyEntry) { all = append(all, built{p, e}) }

	// Genuine items first, so a duplicate can point at its twin's id.
	genuineID := map[string]string{}
	for _, it := range c.Items() {
		id := packetID(KindGenuine, it.SessionTitle, it.Ordinal, "")
		genuineID[itemKey(it)] = id
		verdicts, detail := runHeuristics(it, it.Output, c.Preceding(it), it.MarkedSubjectChanged, false)
		add(Packet{ID: id, Record: it.Record, Window: it.Window, Output: it.Output},
			KeyEntry{
				PacketID: id, Kind: KindGenuine,
				SourceSession: it.SessionTitle, SourceOrdinal: it.Ordinal,
				SourceWindow: fmt.Sprintf("window %d of %d", it.WindowIndex, it.WindowCount),
				SourceDomain: it.SessionDomain,
				Heuristics:   verdicts, HeuristicDetail: detail,
				OutputRunes: utf8.RuneCountInString(it.Output),
			})
	}

	classCount := map[string]int{}
	for _, m := range Mutations {
		p, err := Apply(c, m)
		if err != nil {
			return Emission{}, err
		}
		id := packetID(KindPlanted, m.Session, m.Ordinal, m.ID)
		verdicts, detail := runHeuristics(p.Item, p.Item.Output, c.Preceding(p.Item), p.Source.MarkedSubjectChanged, true)
		classCount[string(m.Class)]++
		add(Packet{ID: id, Record: p.Item.Record, Window: p.Item.Window, Output: p.Item.Output},
			KeyEntry{
				PacketID: id, Kind: KindPlanted,
				SourceSession: m.Session, SourceOrdinal: m.Ordinal,
				SourceWindow: fmt.Sprintf("window %d of %d", p.Source.WindowIndex, p.Source.WindowCount),
				SourceDomain: p.Source.SessionDomain,
				MutationID:   m.ID, MutationClass: m.Class,
				MutatedSpan: m.Replacement, ReplacedSpan: m.Original,
				SpanRunes:    []int{p.SpanStart, p.SpanEnd},
				AbsentTokens: m.Absent, Signature: p.Signature, MutationNote: m.Note,
				Heuristics: verdicts, HeuristicDetail: detail,
				OutputRunes: utf8.RuneCountInString(p.Item.Output),
			})
	}

	for _, d := range CleanDuplicates {
		it, err := c.Find(d.Session, d.Ordinal)
		if err != nil {
			return Emission{}, fmt.Errorf("clean duplicate: %w", err)
		}
		id := packetID(KindCleanDuplicate, d.Session, d.Ordinal, "dup")
		verdicts, detail := runHeuristics(it, it.Output, c.Preceding(it), it.MarkedSubjectChanged, false)
		add(Packet{ID: id, Record: it.Record, Window: it.Window, Output: it.Output},
			KeyEntry{
				PacketID: id, Kind: KindCleanDuplicate,
				SourceSession: it.SessionTitle, SourceOrdinal: it.Ordinal,
				SourceWindow: fmt.Sprintf("window %d of %d", it.WindowIndex, it.WindowCount),
				SourceDomain: it.SessionDomain,
				DuplicateOf:  genuineID[itemKey(it)],
				Heuristics:   verdicts, HeuristicDetail: detail,
				OutputRunes: utf8.RuneCountInString(it.Output),
			})
	}

	// Sorting by id is what makes the directory listing uninformative: a running counter would
	// have put every planted item in one block at the end.
	sort.Slice(all, func(i, j int) bool { return all[i].packet.ID < all[j].packet.ID })
	seen := map[string]bool{}
	for _, b := range all {
		if seen[b.packet.ID] {
			return Emission{}, fmt.Errorf("duplicate packet id %s — the id salt is colliding", b.packet.ID)
		}
		seen[b.packet.ID] = true
	}

	em := Emission{
		Round: round, Dir: dir, PacketsDir: packetsDir,
		KeyPath:  filepath.Join(withheldDir, "answer-key.json"),
		Manifest: Manifest{Round: round},
		Key: AnswerKey{
			Round: round, CorpusSHA256: corpusSHA, CorpusPath: corpusPath, IDSalt: packetIDSalt,
			HeuristicNotes: []string{
				"changed_subject_recorded is the source document's own annotation, not a re-derivation: " +
					"ChangedSubject is decided on the mined Window's grounded subject terms, which a rendered " +
					"window cannot reconstruct. A planted item inherits its source item's annotation and is " +
					"labelled inherited, so it is not evidence about the mutated statement.",
				"The other four heuristics are recomputed on the statement in the packet, so for a planted " +
					"item they judge the MUTATED text.",
				"beat_contradicts_record abstains when the record holds fewer than three subjects; " +
					"abstention is reported as its own column and never folded into a pass.",
			},
		},
	}

	bodies := map[string]Packet{}
	for _, b := range all {
		body := renderPacket(b.packet)
		bodies[b.packet.ID] = b.packet
		name := b.packet.ID + ".md"
		if err := os.WriteFile(filepath.Join(packetsDir, name), []byte(body), 0o644); err != nil {
			return Emission{}, err
		}
		sum := sha256.Sum256([]byte(body))
		em.Manifest.Packets = append(em.Manifest.Packets, ManifestEntry{
			ID: b.packet.ID, File: name, SHA256: hex.EncodeToString(sum[:]),
		})
		em.Key.Entries = append(em.Key.Entries, b.entry)
	}
	em.Manifest.Count = len(em.Manifest.Packets)
	em.Key.Counts = KeyCounts{
		Packets: len(all), Genuine: len(c.Items()), Planted: len(Mutations),
		CleanDuplicates: len(CleanDuplicates), PlantedByClass: classCount,
		CorpusItems: len(c.Items()), CorpusSkipped: corpusSkipped,
	}

	if err := writeJSON(filepath.Join(packetsDir, "manifest.json"), em.Manifest); err != nil {
		return Emission{}, err
	}
	if err := writeJSON(em.KeyPath, em.Key); err != nil {
		return Emission{}, err
	}
	if err := os.WriteFile(filepath.Join(withheldDir, "README.md"), []byte(withheldReadme), 0o644); err != nil {
		return Emission{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, "reviewer-dispatch.md"), []byte(DispatchPrompt()), 0o644); err != nil {
		return Emission{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, "dispatch-plan.tsv"), []byte(dispatchPlan(em)), 0o644); err != nil {
		return Emission{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(roundReadme), 0o644); err != nil {
		return Emission{}, err
	}

	rep, err := checkNoLeak(packetsDir, em.Key, bodies)
	if err != nil {
		return Emission{}, err
	}
	em.Leak = rep
	if err := writeJSON(filepath.Join(withheldDir, "leak-check.json"), rep); err != nil {
		return Emission{}, err
	}
	if len(rep.Hits) > 0 || len(rep.Structural) > 0 {
		return em, fmt.Errorf("withholding check failed, round must not be dispatched: hits=%v structural=%v", rep.Hits, rep.Structural)
	}
	return em, nil
}

func itemKey(it Item) string { return fmt.Sprintf("%s#%d", it.SessionTitle, it.Ordinal) }

// leakReport is the outcome of checking the emitted files against the key.
type leakReport struct {
	Checked  []string    `json:"checked_values"`
	Excluded []LeakField `json:"excluded_values"`
	// Hits are real: an answer-key value present in a packet but NOT present in that packet's
	// own evidence or statement, so it can only have come from the packaging.
	Hits []string `json:"hits"`
	// Coincidences are the same values found INSIDE the evidence the writer saw. Some key
	// fields are ordinary English — the domain label "Software", the kind "genuine" — and a
	// transcript that happens to use the word is not a leak. They are listed rather than
	// suppressed, because a check that silently drops its awkward cases is the failure this
	// harness is replacing; the structural check below is what makes them safe.
	Coincidences []string `json:"coincidences"`
	// Structural are packets whose file is not byte-identical to a render over the evidence
	// and statement alone. Any entry here is fatal and makes every other line meaningless.
	Structural []string `json:"structural_mismatches"`
	Scanned    int      `json:"packets_scanned"`
}

// checkNoLeak verifies withholding two ways over the FILES ON DISK, because the files are what
// gets dispatched.
//
//  1. STRUCTURALLY: each packet must equal renderPacket over its record, window and statement
//     and nothing else. This is the guarantee; it holds for every field, including ones added
//     later and ones no substring search could look for.
//  2. BY GREP, as instructed: every answer-key value that can be searched for is searched for
//     in every packet. Because (1) holds, a value found in a packet is either inside the
//     evidence the writer legitimately saw (a coincidence — recorded) or it came from the
//     packaging (a hit — fatal).
func checkNoLeak(packetsDir string, key AnswerKey, want map[string]Packet) (leakReport, error) {
	var rep leakReport
	rep.Checked, rep.Excluded = KeyLeakFields(key)
	entries, err := os.ReadDir(packetsDir)
	if err != nil {
		return rep, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(packetsDir, e.Name()))
		if err != nil {
			return rep, err
		}
		rep.Scanned++
		body := string(b)
		id := strings.TrimSuffix(e.Name(), ".md")
		p, ok := want[id]
		if !ok {
			rep.Structural = append(rep.Structural, e.Name()+": not in the round")
			continue
		}
		if body != renderPacket(p) {
			rep.Structural = append(rep.Structural, e.Name()+": file is not a render over the evidence and statement alone")
		}
		evidence := p.Record + "\n" + p.Window + "\n" + p.Output
		for _, v := range rep.Checked {
			if !strings.Contains(body, v) {
				continue
			}
			if strings.Contains(evidence, v) {
				rep.Coincidences = append(rep.Coincidences, fmt.Sprintf("%s: %q occurs in the evidence itself", e.Name(), v))
				continue
			}
			rep.Hits = append(rep.Hits, fmt.Sprintf("%s contains %q", e.Name(), v))
		}
	}
	sort.Strings(rep.Hits)
	sort.Strings(rep.Coincidences)
	sort.Strings(rep.Structural)
	return rep, nil
}

// dispatchPlan is the coordinator's worklist: every packet twice, with the exact verdict path
// each reviewer writes to. Tab-separated so it can be read by eye or by a loop.
func dispatchPlan(em Emission) string {
	var b strings.Builder
	b.WriteString("# packet_id\treviewer\tpacket_file\tverdict_file\n")
	for _, p := range em.Manifest.Packets {
		for _, r := range ReviewerIDs {
			fmt.Fprintf(&b, "%s\t%s\tpackets/%s\tverdicts/%s.%s.json\n", p.ID, r, p.File, p.ID, r)
		}
	}
	return b.String()
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// roundReadme is the coordinator's guide. It deliberately carries NO composition counts — how
// many of the packets are planted is a base rate, and a reviewer who wandered in here and read it
// would review differently. Those counts live in the withheld key and in the design doc.
const roundReadme = `# Qualitative review round

    packets/             one packet per item, plus manifest.json. A packet is the evidence and
                         the statement, and nothing else.
    reviewer-dispatch.md the prompt to paste into a reviewer, verbatim, with three placeholders
                         substituted per dispatch.
    dispatch-plan.tsv    every packet twice, with the verdict path each reviewer writes to.
    verdicts/            where verdict JSON lands, named <PKT-ID>.<reviewer>.json.
    withheld/            the answer key. NOT for reviewers. See its own README.

To dispatch: for each row of dispatch-plan.tsv, paste reviewer-dispatch.md with {{PACKET_FILE}},
{{REVIEWER}} and {{VERDICT_FILE}} replaced by that row's columns. Two reviewers per packet, each
told nothing about the other and nothing about how the packets were made.

To score, from the repository root:

    REVIEW_SCORE_DIR=<this directory> go test ./internal/agent/enrich/llmstudy/review/ \
      -run TestScoreRound -v

which writes score.md and score.json here.
`

const withheldReadme = `# Withheld — scorer only

Nothing in this directory may be read by a reviewer, and nothing in it may be pasted into a
reviewer dispatch. It holds the answer key: which packets are genuine, which carry a planted
defect and of what class, the exact span mutated in each, and what the retired string
heuristics say about every item.

A reviewer that has seen this file has produced an unusable verdict, and the round it belongs
to cannot be re-run against the same corpus, because the same packet ids are derived from the
same provenance under a fixed salt.

` + "`leak-check.json`" + ` records the answer-key values the emitted packets were grepped for,
the values deliberately not grepped for with the reason for each, and any hit. A round with a
hit must not be dispatched.
`
