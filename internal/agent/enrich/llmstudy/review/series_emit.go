package review

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

//go:embed reviewer-dispatch-series.md
var seriesDispatchFS embed.FS

// SeriesDispatchPrompt is the series reviewer prompt, embedded so an emitted round carries a copy
// of the exact rubric that was current when it was cut. A round scored against a different rubric
// than it was reviewed under is a different measurement.
func SeriesDispatchPrompt() string {
	b, err := seriesDispatchFS.ReadFile("reviewer-dispatch-series.md")
	if err != nil {
		panic("review: embedded series dispatch prompt missing: " + err.Error())
	}
	return string(b)
}

// SeriesEmission is what a series round came out as.
type SeriesEmission struct {
	Round      string
	Dir        string
	PacketsDir string
	KeyPath    string
	Manifest   Manifest
	Key        SeriesAnswerKey
	Leak       leakReport
}

// EmitSeries cuts a series round: it plants every series mutation, duplicates the clean controls,
// writes one packet per timeline plus a manifest, writes the answer key OUTSIDE the packets
// directory, copies the reviewer prompt, and greps the packets it just wrote for every answer-key
// value it can legitimately look for.
//
// Deterministic given a corpus: ids are salted hashes of provenance and files are written in id
// order, so the same corpus produces the same round byte for byte and a disputed verdict can be
// re-scored against a regenerated round.
func EmitSeries(dir, round string, c Corpus, corpusPath, corpusSHA string, corpusSkipped int) (SeriesEmission, error) {
	if round == "" {
		round = "s1"
	}
	all, err := BuildSeries(c)
	if err != nil {
		return SeriesEmission{}, err
	}
	packetsDir := filepath.Join(dir, "packets")
	withheldDir := filepath.Join(dir, "withheld")
	verdictsDir := filepath.Join(dir, "verdicts")
	for _, d := range []string{packetsDir, withheldDir, verdictsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return SeriesEmission{}, err
		}
	}

	type built struct {
		packet SeriesPacket
		entry  SeriesKeyEntry
	}
	var items []built
	bySession := map[string]int{}
	add := func(p SeriesPacket, e SeriesKeyEntry) {
		items = append(items, built{p, e})
		bySession[e.SourceSession]++
	}

	// Clean series first, so a duplicate can point at its twin's id.
	cleanID := map[string]string{}
	for _, s := range all {
		id := seriesPacketID(KindSeriesClean, s.SessionTitle, "")
		cleanID[s.SessionTitle] = id
		add(seriesPacketOf(id, s), seriesEntry(id, KindSeriesClean, s))
	}

	classCount := map[string]int{}
	for _, m := range SeriesMutations {
		p, err := ApplySeries(c, all, m)
		if err != nil {
			return SeriesEmission{}, err
		}
		id := seriesPacketID(KindSeriesPlanted, m.Session, m.ID)
		e := seriesEntry(id, KindSeriesPlanted, p.Series)
		e.MutationID, e.MutationClass, e.MutationNote = m.ID, m.Class, m.Note
		e.Positions, e.Signature, e.LocationBy = p.Positions, p.Signature, p.LocationBy
		e.RemovedOrdinals, e.ReplacedBeat = p.Removed, p.Replaced
		if m.Class == CrossSessionContamination {
			e.SplicedFrom = m.DonorSession
		}
		for _, pair := range m.Pairs {
			e.SwapPairs = append(e.SwapPairs, pair.From+" -> "+pair.To)
		}
		classCount[string(m.Class)]++
		add(seriesPacketOf(id, p.Series), e)
	}

	for _, title := range SeriesCleanDuplicates {
		s, err := FindSeries(all, title)
		if err != nil {
			return SeriesEmission{}, fmt.Errorf("series clean duplicate: %w", err)
		}
		id := seriesPacketID(KindSeriesDuplicate, title, "dup")
		e := seriesEntry(id, KindSeriesDuplicate, s)
		e.DuplicateOf = cleanID[title]
		add(seriesPacketOf(id, s), e)
	}

	// Sorting by id is what makes the directory listing uninformative.
	sort.Slice(items, func(i, j int) bool { return items[i].packet.ID < items[j].packet.ID })
	seen := map[string]bool{}
	for _, b := range items {
		if seen[b.packet.ID] {
			return SeriesEmission{}, fmt.Errorf("duplicate series packet id %s — the id salt is colliding", b.packet.ID)
		}
		seen[b.packet.ID] = true
	}

	em := SeriesEmission{
		Round: round, Dir: dir, PacketsDir: packetsDir,
		KeyPath:  filepath.Join(withheldDir, "answer-key.json"),
		Manifest: Manifest{Round: round},
		Key: SeriesAnswerKey{
			Round: round, Metric: "series_followability", CorpusSHA256: corpusSHA,
			CorpusPath: corpusPath, IDSalt: seriesPacketIDSalt,
			Notes: []string{
				"A packet carries the derived session record and the ordered beats, and NO conversation " +
					"window: followable asks whether a reader can reconstruct the work without the transcript.",
				"The record's counts, projects and tool profile are the session's last measured block " +
					"verbatim (the counts are cumulative). The recurring-subject line is the union of every " +
					"term counted across the session, alphabetised, so it carries no chronology.",
				"Beats are numbered by POSITION in the timeline shown. presented_ordinals is the real beat " +
					"number at each position and is the order-shuffle answer.",
				"order_shuffle and dropped_middle introduce no text, so location is by beat position only " +
					"(location_by). A reviewer who describes the break without naming a beat scores as a miss: " +
					"a floor on the measurement, not on the reader.",
				"Clean series are NOT certified thread-free. r1 found reviewers claiming a defect on 21 of " +
					"30 genuine beats, mostly completion claims the beats really do make, so a break " +
					"reported on a clean series may be a real finding this harness never planted.",
			},
		},
	}

	bodies := map[string]SeriesPacket{}
	for _, b := range items {
		body := renderSeriesPacket(b.packet)
		bodies[b.packet.ID] = b.packet
		name := b.packet.ID + ".md"
		if err := os.WriteFile(filepath.Join(packetsDir, name), []byte(body), 0o644); err != nil {
			return SeriesEmission{}, err
		}
		sum := sha256.Sum256([]byte(body))
		em.Manifest.Packets = append(em.Manifest.Packets, ManifestEntry{
			ID: b.packet.ID, File: name, SHA256: hex.EncodeToString(sum[:]),
		})
		em.Key.Entries = append(em.Key.Entries, b.entry)
	}
	em.Manifest.Count = len(em.Manifest.Packets)
	em.Key.Counts = SeriesKeyCounts{
		Packets: len(items), Clean: len(all), Planted: len(SeriesMutations),
		CleanDuplicates: len(SeriesCleanDuplicates), PlantedByClass: classCount,
		SourceSeries: len(all), SeriesBySession: bySession,
		CorpusItems: len(c.Items()), CorpusSkipped: corpusSkipped,
	}

	if err := writeJSON(filepath.Join(packetsDir, "manifest.json"), em.Manifest); err != nil {
		return SeriesEmission{}, err
	}
	if err := writeJSON(em.KeyPath, em.Key); err != nil {
		return SeriesEmission{}, err
	}
	if err := os.WriteFile(filepath.Join(withheldDir, "README.md"), []byte(withheldReadme), 0o644); err != nil {
		return SeriesEmission{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, "reviewer-dispatch-series.md"), []byte(SeriesDispatchPrompt()), 0o644); err != nil {
		return SeriesEmission{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, "dispatch-plan.tsv"), []byte(seriesDispatchPlan(em)), 0o644); err != nil {
		return SeriesEmission{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(seriesRoundReadme), 0o644); err != nil {
		return SeriesEmission{}, err
	}

	rep, err := checkNoSeriesLeak(packetsDir, em.Key, bodies)
	if err != nil {
		return SeriesEmission{}, err
	}
	em.Leak = rep
	if err := writeJSON(filepath.Join(withheldDir, "leak-check.json"), rep); err != nil {
		return SeriesEmission{}, err
	}
	if len(rep.Hits) > 0 || len(rep.Structural) > 0 {
		return em, fmt.Errorf("withholding check failed, round must not be dispatched: hits=%v structural=%v", rep.Hits, rep.Structural)
	}
	return em, nil
}

func seriesPacketOf(id string, s Series) SeriesPacket {
	p := SeriesPacket{ID: id, Record: s.Record.Block()}
	for _, b := range s.Beats {
		p.Beats = append(p.Beats, b.Text)
	}
	return p
}

func seriesEntry(id string, kind SeriesKind, s Series) SeriesKeyEntry {
	return SeriesKeyEntry{
		PacketID: id, Kind: kind,
		SourceSession: s.SessionTitle, SourceDomain: s.SessionDomain,
		BeatsPresented: len(s.Beats), PresentedOrdinals: s.Ordinals(),
		RecordDerivedFrom: s.Record.DerivedFrom,
		SeriesRunes:       utf8.RuneCountInString(s.Text()),
	}
}

// checkNoSeriesLeak verifies withholding two ways over the FILES ON DISK, because the files are
// what gets dispatched — structurally first, because that is the guarantee that holds for fields no
// substring search would know to look for.
func checkNoSeriesLeak(packetsDir string, key SeriesAnswerKey, want map[string]SeriesPacket) (leakReport, error) {
	var rep leakReport
	rep.Checked, rep.Excluded = SeriesKeyLeakFields(key)
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
		if body != renderSeriesPacket(p) {
			rep.Structural = append(rep.Structural, e.Name()+": file is not a render over the record and the beats alone")
		}
		evidence := p.Record + "\n" + strings.Join(p.Beats, "\n")
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

func seriesDispatchPlan(em SeriesEmission) string {
	var b strings.Builder
	b.WriteString("# packet_id\treviewer\tpacket_file\tverdict_file\n")
	for _, p := range em.Manifest.Packets {
		for _, r := range ReviewerIDs {
			fmt.Fprintf(&b, "%s\t%s\tpackets/%s\tverdicts/%s.%s.json\n", p.ID, r, p.File, p.ID, r)
		}
	}
	return b.String()
}

// seriesRoundReadme is the coordinator's guide. Like r1's it carries NO composition counts — how
// many of the packets are planted is a base rate, and a reviewer who wandered in here and read it
// would review differently.
const seriesRoundReadme = `# Series review round — can the timeline be read back as a narrative?

    packets/                    one packet per timeline, plus manifest.json. A packet is the
                                measured session record and the ordered beats, and nothing else.
                                There is deliberately NO conversation window.
    reviewer-dispatch-series.md the prompt to paste into a reviewer, verbatim, with three
                                placeholders substituted per dispatch.
    dispatch-plan.tsv           every packet twice, with the verdict path each reviewer writes to.
    verdicts/                   where verdict JSON lands, named <SER-ID>.<reviewer>.json.
    withheld/                   the answer key. NOT for reviewers. See its own README.

This round measures a SERIES property. It is independent of the per-beat round in both
directions: a set of individually honest beats can still have no thread, and a followable series
can contain a defective beat. Do not merge the two, and do not tell a series reviewer anything
about the beat round.

To dispatch: for each row of dispatch-plan.tsv, paste reviewer-dispatch-series.md with
{{PACKET_FILE}}, {{REVIEWER}} and {{VERDICT_FILE}} replaced by that row's columns. Two reviewers
per packet, each told nothing about the other and nothing about how the packets were made.

To score, from the repository root:

    REVIEW_SERIES_DIR="$PWD/<this directory>" \
      go test ./internal/agent/enrich/llmstudy/review/ -run TestScoreSeriesRound -v

⚠️ USE AN ABSOLUTE PATH. ` + "`go test`" + ` runs each test with its working directory set to the
PACKAGE directory, so a relative path is resolved against
internal/agent/enrich/llmstudy/review/ and not against your shell's cwd. Round r1 hit this: the
scorer silently failed to find the answer key. The resolver now accepts an absolute path, a path
relative to your cwd, or a path relative to the repository root, and it says which one it used —
but an absolute path is the one that cannot be misread.

Add REVIEW_BEAT_DIR=<the r1 round directory, also absolute> to get the series-versus-beat
cross-tabulation. It is reported as its own table and is never folded into either metric.
`
