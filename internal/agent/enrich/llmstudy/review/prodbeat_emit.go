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

//go:embed reviewer-dispatch-prod.md
var prodDispatchFS embed.FS

// ProdDispatchPrompt is the reviewer prompt, embedded so an emitted round carries a copy of the
// exact rubric that was current when it was cut. A round scored against a different rubric than it
// was reviewed under is a different measurement — and this round's whole purpose is to be scored
// against r1's rubric, so the file is r1's text with one added paragraph about the layout.
func ProdDispatchPrompt() string {
	b, err := prodDispatchFS.ReadFile("reviewer-dispatch-prod.md")
	if err != nil {
		panic("review: embedded production dispatch prompt missing: " + err.Error())
	}
	return string(b)
}

const prodPacketIDSalt = "keld-review-prod-packet-v1"

// prodPacketID derives a stable id from provenance under a fixed salt, exactly as the other two
// rounds do: sorting the emitted files by id shuffles genuine, planted and duplicate together, and
// the same corpus regenerates the same round so a disputed verdict can be re-scored.
func prodPacketID(kind Kind, sessionTitle string, ordinal int, tag string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%s|%d|%s", prodPacketIDSalt, kind, sessionTitle, ordinal, tag)))
	return "PB-" + strings.ToUpper(hex.EncodeToString(sum[:4]))
}

// renderProdPacket writes the markdown a reviewer is handed.
//
// The section markers are r1's, so one parser reads both rounds' packets and the scorer's
// quote-is-in-the-EVIDENCE check needs no second implementation. The one difference is the sentence
// describing the statement's layout, which is necessary — a reader handed a subject line and a list
// with no orientation is being asked to guess the format — and is written to describe the artifact
// in front of them and nothing else. It does not say what produced it, that anything else ever
// produced one, or that any other shape exists to compare it with.
//
// Withheld exactly as in r1, and for the same reasons: the session, the beat number, the window
// coordinates, the population label, whether the item is genuine or planted, how many attempts the
// generation took, and the anchoring guard's drop marker. That last is this round's "marked SUBJECT
// CHANGED": a mechanism's verdict on the item, which showing would ask the reviewer to agree with.
func renderProdPacket(p Packet) string {
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
	b.WriteString("Its first line names the subject of the work. Each line after it, marked with a dash,\n")
	b.WriteString("is one thing the writer says the conversation shows happening.\n\n")
	for _, line := range strings.Split(strings.TrimSpace(p.Output), "\n") {
		b.WriteString("> " + line + "\n")
	}
	return b.String()
}

// ProdEmission is what a production-beat round came out as.
type ProdEmission struct {
	Round      string
	Dir        string
	PacketsDir string
	KeyPath    string
	Manifest   Manifest
	Key        AnswerKey
	Leak       leakReport
	// Sample is the genuine items the round drew, kept so the caller can report coverage without
	// re-deriving it.
	Sample []Item
}

// EmitProd cuts a production-beat round.
//
// The shape is r1's — sample the genuine items, plant every mutation, duplicate the clean controls,
// write one packet per item plus a manifest, write the answer key OUTSIDE the packets directory,
// copy the reviewer prompt, and grep the packets just written for every answer-key value that can
// legitimately be looked for. Two things differ:
//
//   - the genuine items are SAMPLED rather than exhaustive (see prodbeat_sample.go), and the sample
//     and its session coverage are recorded in the key, because a conclusion from this round is
//     bounded by how many distinct conversations it saw;
//   - NO heuristic verdict is recorded. r1 ran the retired string checks one more round to measure
//     them against a reader; that comparison has been made, this design deletes the checks it
//     compared, and re-running them over a bulleted statement would be one more measure of ordinary
//     English. The key's Heuristics map is left empty and the scorer prints no heuristic table
//     rather than a table of zeros.
func EmitProd(dir, round string, p ProdCorpus, corpusPath, corpusSHA string) (ProdEmission, error) {
	if round == "" {
		round = "p1"
	}
	sample, err := SampleProdGenuine(p, ProdGenuineReal, ProdGenuineSynthetic)
	if err != nil {
		return ProdEmission{}, err
	}
	inSample := map[string]bool{}
	for _, it := range sample {
		inSample[itemKey(it)] = true
	}

	packetsDir := filepath.Join(dir, "packets")
	withheldDir := filepath.Join(dir, "withheld")
	verdictsDir := filepath.Join(dir, "verdicts")
	for _, d := range []string{packetsDir, withheldDir, verdictsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return ProdEmission{}, err
		}
	}

	type built struct {
		packet Packet
		entry  KeyEntry
	}
	var all []built
	add := func(pk Packet, e KeyEntry) { all = append(all, built{pk, e}) }

	genuineID := map[string]string{}
	for _, it := range sample {
		id := prodPacketID(KindGenuine, it.SessionTitle, it.Ordinal, "")
		genuineID[itemKey(it)] = id
		add(Packet{ID: id, Record: it.Record, Window: it.Window, Output: it.Output},
			prodKeyEntry(id, KindGenuine, it))
	}

	classCount := map[string]int{}
	for _, m := range ProdMutations {
		if inSample[itemKey(Item{SessionTitle: m.Session, Ordinal: m.Ordinal})] {
			return ProdEmission{}, fmt.Errorf("mutation %s is cut from %s#%d, which is also emitted unmutated — the same statement would be in the round twice",
				m.ID, m.Session, m.Ordinal)
		}
		pl, err := ApplyProd(p.Corpus, m)
		if err != nil {
			return ProdEmission{}, err
		}
		id := prodPacketID(KindPlanted, m.Session, m.Ordinal, m.ID)
		e := prodKeyEntry(id, KindPlanted, pl.Item)
		e.MutationID, e.MutationClass, e.MutationNote = m.ID, m.Class, m.Note
		e.MutatedSpan, e.ReplacedSpan = m.Replacement, m.Original
		e.SpanRunes = []int{pl.SpanStart, pl.SpanEnd}
		e.AbsentTokens, e.Signature = m.Absent, pl.Signature
		classCount[string(m.Class)]++
		add(Packet{ID: id, Record: pl.Item.Record, Window: pl.Item.Window, Output: pl.Item.Output}, e)
	}

	for _, d := range ProdCleanDuplicates {
		it, err := p.Corpus.Find(d.Session, d.Ordinal)
		if err != nil {
			return ProdEmission{}, fmt.Errorf("clean duplicate: %w", err)
		}
		twin, ok := genuineID[itemKey(it)]
		if !ok {
			return ProdEmission{}, fmt.Errorf("clean duplicate %s#%d is not in the genuine sample, so it has no twin to be a duplicate OF", d.Session, d.Ordinal)
		}
		id := prodPacketID(KindCleanDuplicate, d.Session, d.Ordinal, "dup")
		e := prodKeyEntry(id, KindCleanDuplicate, it)
		e.DuplicateOf = twin
		add(Packet{ID: id, Record: it.Record, Window: it.Window, Output: it.Output}, e)
	}

	// Sorting by id is what makes the directory listing uninformative: a running counter would have
	// put every planted item in one block at the end.
	sort.Slice(all, func(i, j int) bool { return all[i].packet.ID < all[j].packet.ID })
	seen := map[string]bool{}
	for _, b := range all {
		if seen[b.packet.ID] {
			return ProdEmission{}, fmt.Errorf("duplicate packet id %s — the id salt is colliding", b.packet.ID)
		}
		seen[b.packet.ID] = true
	}

	realSessions, synthSessions := ProdSampleCoverage(p, sample)
	em := ProdEmission{
		Round: round, Dir: dir, PacketsDir: packetsDir, Sample: sample,
		KeyPath:  filepath.Join(withheldDir, "answer-key.json"),
		Manifest: Manifest{Round: round},
		Key: AnswerKey{
			Round: round, CorpusSHA256: corpusSHA, CorpusPath: corpusPath, IDSalt: prodPacketIDSalt,
			HeuristicNotes: prodKeyNotes(p, sample, realSessions, synthSessions),
		},
	}

	bodies := map[string]Packet{}
	for _, b := range all {
		body := renderProdPacket(b.packet)
		bodies[b.packet.ID] = b.packet
		name := b.packet.ID + ".md"
		if err := os.WriteFile(filepath.Join(packetsDir, name), []byte(body), 0o644); err != nil {
			return ProdEmission{}, err
		}
		sum := sha256.Sum256([]byte(body))
		em.Manifest.Packets = append(em.Manifest.Packets, ManifestEntry{
			ID: b.packet.ID, File: name, SHA256: hex.EncodeToString(sum[:]),
		})
		em.Key.Entries = append(em.Key.Entries, b.entry)
	}
	em.Manifest.Count = len(em.Manifest.Packets)
	em.Key.Counts = KeyCounts{
		Packets: len(all), Genuine: len(sample), Planted: len(ProdMutations),
		CleanDuplicates: len(ProdCleanDuplicates), PlantedByClass: classCount,
		CorpusItems: len(p.Corpus.Items()), CorpusSkipped: len(p.Failures),
	}

	if err := writeJSON(filepath.Join(packetsDir, "manifest.json"), em.Manifest); err != nil {
		return ProdEmission{}, err
	}
	if err := writeJSON(em.KeyPath, em.Key); err != nil {
		return ProdEmission{}, err
	}
	if err := writeJSON(filepath.Join(withheldDir, "run-facts.json"), prodRunFacts(p, sample)); err != nil {
		return ProdEmission{}, err
	}
	if err := os.WriteFile(filepath.Join(withheldDir, "README.md"), []byte(withheldReadme), 0o644); err != nil {
		return ProdEmission{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, "reviewer-dispatch-prod.md"), []byte(ProdDispatchPrompt()), 0o644); err != nil {
		return ProdEmission{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, "dispatch-plan.tsv"), []byte(prodDispatchPlan(em)), 0o644); err != nil {
		return ProdEmission{}, err
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(prodRoundReadme(p, sample, realSessions, synthSessions)), 0o644); err != nil {
		return ProdEmission{}, err
	}

	rep, err := checkNoProdLeak(packetsDir, em.Key, bodies)
	if err != nil {
		return ProdEmission{}, err
	}
	em.Leak = rep
	if err := writeJSON(filepath.Join(withheldDir, "leak-check.json"), rep); err != nil {
		return ProdEmission{}, err
	}
	if len(rep.Hits) > 0 || len(rep.Structural) > 0 {
		return em, fmt.Errorf("withholding check failed, round must not be dispatched: hits=%v structural=%v", rep.Hits, rep.Structural)
	}
	return em, nil
}

// prodKeyLeakFields is KeyLeakFields plus the session id with its ".jsonl" suffix stripped.
//
// It exists because the stored session title is `<uuid>.jsonl` and a hand grep over the emitted
// packets found the BARE uuid in four of them. Each occurrence turned out to be inside the
// conversation window — the engineer's own `/tmp/claude-1000/<uuid>/scratchpad` paths, which the
// beat writer saw exactly as the reviewer does — so it is a coincidence and not a leak. But the
// emitter's grep had not looked for it at all, and a withholding check that cannot see the form the
// value actually takes in a packet is a check that would miss a real one. The suffixed and bare
// forms are now both searched, and the bare form's occurrences are recorded as coincidences with
// their packets named, rather than being invisible.
func prodKeyLeakFields(key AnswerKey) (checked []string, excluded []LeakField) {
	checked, excluded = KeyLeakFields(key)
	seen := map[string]bool{}
	for _, v := range checked {
		seen[v] = true
	}
	for _, e := range key.Entries {
		stem := strings.TrimSuffix(e.SourceSession, ".jsonl")
		if stem == "" || stem == e.SourceSession || seen[stem] || utf8.RuneCountInString(stem) < minLeakToken {
			continue
		}
		seen[stem] = true
		checked = append(checked, stem)
	}
	sort.Strings(checked)
	return checked, excluded
}

// prodKeyEntry fills the provenance every entry carries. Heuristics is left empty deliberately —
// see EmitProd — and is an empty map rather than nil so a reader of the key sees the field and its
// emptiness rather than a missing key they might read as an omission.
func prodKeyEntry(id string, kind Kind, it Item) KeyEntry {
	return KeyEntry{
		PacketID: id, Kind: kind,
		SourceSession: it.SessionTitle, SourceOrdinal: it.Ordinal,
		SourceWindow: fmt.Sprintf("window %d of %d", it.WindowIndex, it.WindowCount),
		SourceDomain: it.Population,
		Heuristics:   map[string]string{},
		OutputRunes:  utf8.RuneCountInString(it.Output),
	}
}

// ProdRunFacts is what the round records about the RUN that produced its material, as opposed to
// the round itself. It sits in the withheld directory beside the key because it is scorer input,
// and every field of it lands in the report as a caveat.
type ProdRunFacts struct {
	Counts ProdRunCounts `json:"run_counts"`
	// Failures are the windows that produced no beat. Absences: a round that scores only what
	// exists cannot see them, so they are carried explicitly.
	Failures []ProdFailure `json:"generation_failures"`
	// SampledSessions / CorpusSessions are the distinct conversations behind the round and behind
	// the corpus. The README prints them above every table.
	SampledSessionsReal      int `json:"sampled_sessions_real"`
	SampledSessionsSynthetic int `json:"sampled_sessions_synthetic"`
	CorpusSessionsReal       int `json:"corpus_sessions_real"`
	CorpusSessionsSynthetic  int `json:"corpus_sessions_synthetic"`
	SampleReal               int `json:"sampled_beats_real"`
	SampleSynthetic          int `json:"sampled_beats_synthetic"`
}

func prodRunFacts(p ProdCorpus, sample []Item) ProdRunFacts {
	r, s := ProdSampleCoverage(p, sample)
	f := ProdRunFacts{
		Counts: p.Counts, Failures: p.Failures,
		SampledSessionsReal: r, SampledSessionsSynthetic: s,
		CorpusSessionsReal:      len(p.SessionsBy(PopulationReal)),
		CorpusSessionsSynthetic: len(p.SessionsBy(PopulationSynthetic)),
	}
	for _, it := range sample {
		if ProdPopulation(it.Population) == PopulationSynthetic {
			f.SampleSynthetic++
		} else {
			f.SampleReal++
		}
	}
	return f
}

func prodKeyNotes(p ProdCorpus, sample []Item, realSessions, synthSessions int) []string {
	return []string{
		fmt.Sprintf("The genuine items are a SAMPLE of %d of the corpus's %d beats, drawn by the documented "+
			"rotation over sessions, and they cover %d real and %d hand-authored conversations.",
			len(sample), len(p.Corpus.Items()), realSessions, synthSessions),
		"No heuristic verdict is recorded on any entry. The retired string checks were measured against a " +
			"reader in round r1; this design deletes them, and re-running them over a bulleted statement " +
			"would be one more measure of ordinary English rather than a comparison.",
		fmt.Sprintf("%d of %d kept entries in the run behind this corpus name no checkable specific at all, "+
			"so the run's own anchoring guard had nothing to check on %d%% of them. A low drop count is not "+
			"evidence of grounding.", p.Counts.UnconstrainedEntries, p.Counts.KeptEntries,
			pct(p.Counts.UnconstrainedEntries, p.Counts.KeptEntries)),
		fmt.Sprintf("%d windows produced NO beat because subject anchoring could not be satisfied by the "+
			"temperature ladder, and %d more failed on the entry cap. Those are absences and no packet in this "+
			"round represents them.", p.Counts.SubjectLadderLosses, len(p.FailuresBy(FailureEntryCap))),
		"ONE defect is planted per class, not two. A class that comes back uncaught therefore cannot be told " +
			"apart from one reviewer having an off item; r1 planted two per class precisely to make that " +
			"distinction, and this round trades it for breadth of genuine material.",
	}
}

func pct(n, of int) int {
	if of == 0 {
		return 0
	}
	return (n*100 + of/2) / of
}

// checkNoProdLeak verifies withholding two ways over the FILES ON DISK, because the files are what
// gets dispatched: structurally, that each packet equals a render over its record, window and
// statement and nothing else; and by grep, that no answer-key value reached a packet from anywhere
// but the evidence the writer legitimately saw.
func checkNoProdLeak(packetsDir string, key AnswerKey, want map[string]Packet) (leakReport, error) {
	var rep leakReport
	rep.Checked, rep.Excluded = prodKeyLeakFields(key)
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
		if body != renderProdPacket(p) {
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

func prodDispatchPlan(em ProdEmission) string {
	var b strings.Builder
	b.WriteString("# packet_id\treviewer\tpacket_file\tverdict_file\n")
	for _, p := range em.Manifest.Packets {
		for _, r := range ReviewerIDs {
			fmt.Fprintf(&b, "%s\t%s\tpackets/%s\tverdicts/%s.%s.json\n", p.ID, r, p.File, p.ID, r)
		}
	}
	return b.String()
}

// prodRoundReadme is the coordinator's guide. Like r1's it carries NO composition counts — how many
// packets are planted is a base rate, and a reviewer who wandered in here and read it would review
// differently. What it DOES carry, above everything else, is how many distinct conversations the
// material comes from and what the run behind it could not measure, because those two facts bound
// every number the round can produce and neither is visible from a score table.
func prodRoundReadme(p ProdCorpus, sample []Item, realSessions, synthSessions int) string {
	var b strings.Builder
	b.WriteString("# Production-beat review round\n\n")
	fmt.Fprintf(&b, "**The material is %d beats drawn from %d distinct real conversations and %d hand-authored\n"+
		"sessions.** The corpus behind it holds %d beats over %d real conversations (deduplicated on window\n"+
		"content, so a session id is not counted twice for a fork or resume) and the same %d hand-authored\n"+
		"pair. This is a small number of sources and it is printed here and above every table in the score\n"+
		"for that reason: no count below can separate \"the reader judges this kind of statement so\" from\n"+
		"\"the reader reads these particular conversations so\".\n\n",
		len(sample), realSessions, synthSessions, len(p.Corpus.Items()),
		len(p.SessionsBy(PopulationReal)), len(p.SessionsBy(PopulationSynthetic)))

	b.WriteString("**Two facts about the run that produced this material are limits on what the round can say,\n")
	b.WriteString("and neither is visible from any verdict:**\n\n")
	fmt.Fprintf(&b, "1. **%d of %d kept entries (%d%%) name nothing checkable.** The run's anchoring guard is a\n"+
		"   verbatim-occurrence check over specifics, and an entry carrying no specific is unconstrained by\n"+
		"   construction. Its low drop count is therefore not evidence of grounding on more than a quarter\n"+
		"   of the entries in this material.\n",
		p.Counts.UnconstrainedEntries, p.Counts.KeptEntries, pct(p.Counts.UnconstrainedEntries, p.Counts.KeptEntries))
	fmt.Fprintf(&b, "2. **%d windows produced no beat at all**, because subject anchoring could not be satisfied by\n"+
		"   the temperature ladder (%d further windows were lost to the entry cap, for %d generation failures\n"+
		"   in all). Those windows are ABSENCES: no packet in this round represents them, and a round that\n"+
		"   scores only what exists will not see them.\n\n",
		p.Counts.SubjectLadderLosses, len(p.FailuresBy(FailureEntryCap)), len(p.Failures))

	b.WriteString("    packets/                  one packet per item, plus manifest.json. A packet is the evidence\n")
	b.WriteString("                              and the statement, and nothing else.\n")
	b.WriteString("    reviewer-dispatch-prod.md the prompt to paste into a reviewer, verbatim, with three\n")
	b.WriteString("                              placeholders substituted per dispatch.\n")
	b.WriteString("    dispatch-plan.tsv         every packet twice, with the verdict path each reviewer writes to.\n")
	b.WriteString("    verdicts/                 where verdict JSON lands, named <PB-ID>.<reviewer>.json.\n")
	b.WriteString("    withheld/                 the answer key and the run facts. NOT for reviewers.\n\n")

	b.WriteString("To dispatch: for each row of dispatch-plan.tsv, paste reviewer-dispatch-prod.md with\n")
	b.WriteString("{{PACKET_FILE}}, {{REVIEWER}} and {{VERDICT_FILE}} replaced by that row's columns. Two reviewers\n")
	b.WriteString("per packet, each told nothing about the other and nothing about how the packets were made.\n\n")

	b.WriteString("To score, from the repository root:\n\n")
	b.WriteString("    REVIEW_PROD_DIR=\"$PWD/<this directory>\" \\\n")
	b.WriteString("    REVIEW_R1_SCORE=\"$PWD/.superpowers/sdd/2026-08-11-qualitative-review/score.json\" \\\n")
	b.WriteString("      go test ./internal/agent/enrich/llmstudy/review/ -run TestScoreProdRound -v\n\n")
	b.WriteString("⚠️ USE ABSOLUTE PATHS. `go test` runs each test with its working directory set to the PACKAGE\n")
	b.WriteString("directory, so a relative path is resolved against internal/agent/enrich/llmstudy/review/ and\n")
	b.WriteString("not against your shell's cwd. Round r1 lost a scoring run to exactly that. The resolver\n")
	b.WriteString("accepts an absolute path, a path relative to your cwd, or a path relative to the repository\n")
	b.WriteString("root, and says which it used — but an absolute path is the one that cannot be misread.\n\n")
	b.WriteString("REVIEW_R1_SCORE is what produces the dimension-by-dimension comparison against round r1.\n")
	b.WriteString("Without it that table is omitted rather than guessed at, and the comparison is the reason\n")
	b.WriteString("this round exists.\n")
	return b.String()
}
