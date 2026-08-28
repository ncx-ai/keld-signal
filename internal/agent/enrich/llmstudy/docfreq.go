package llmstudy

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Document frequency over the local transcript corpus — what makes a term a SUBJECT.
//
// The rule this replaces accepted a token if it was a strong identifier "or is at least 7
// characters long", and that second clause is the documented mechanical root of four separate
// defects on this branch:
//
//   - SessionRecord.Subjects, a block the prompt labels "measured — authoritative" and tells
//     the model to use to place the work, carried `control`, `question`, `exactly`, `failure`,
//     `padding`, `identity`, `confirm` and a full absolute path. 8 of 12 terms were not
//     subjects.
//   - T12 (beat-vs-record) was unusable: every flag was an accurate beat, because one side of
//     the comparison was gerunds and adverbs (`analyzing`, `ensuring`, `finalizing`,
//     `consistent`) and the two populations never intersected.
//   - T11 (synopsis lag) was near-tautological: an unrelated synopsis matched a recent window
//     on the words `remains` and `whether`, and the check only fires at zero matches.
//   - SubjectShifted fired on 41 of 42 refinements.
//
// A longer stopword list does not fix it, and that was measured too: `failure`, `control`,
// `question` are content words, not function words — generic, not stop — and the pair that
// defeats SynopsisLag (`remains`, `whether`) is in no stopword list at any casing. The
// discriminator needed is SPECIFICITY, and specificity is not a property of a word in isolation.
//
// So: a term that appears in many different sessions is generic; a term concentrated in few
// names that session's subject. That is inverse document frequency, it is the standard answer to
// exactly this problem, and it is computable on device from transcripts already on disk —
// counts only, no text stored and nothing transmitted.
//
// Why this beats a hand-tuned list: it adapts to the material. For an accountant `depreciation`
// and `accruals` are subjects and survive, while an engineering stoplist would never have
// contained them. Same reasoning the design gives for label vocabularies being readable
// descriptions rather than bare ids — the discriminator has to fit the audience.

const (
	// dfMaxFraction is the share of sessions a term may appear in and still count as naming a
	// subject.
	//
	// MEASURED, and the measurement does NOT show two cleanly separated populations — the
	// honest version of the design's expectation. Over 34 sessions of this corpus
	// (TestCorpusDocumentFrequencySeparatesSubjectsFromEnglish prints all of it):
	//
	//	generic   exactly .88  confirm .88  whether .85  changes .85  running .82
	//	          checkout .82 question .79 complete .76 parallel .71 consistent .68
	//	          worktree .65 failure .59  control .50  identity .50 buttons .47
	//	          specifically .41  remains .35  padding .35  synthetic .32  windows .29
	//	          latency .26  password .21  finalizing .18  identified .18
	//	          ensuring .03  analyzing .00
	//	specific  enrichment .56  keld-signal .41  gliner2 .32  atlas.keld.co .26
	//	          daemon.go .21  notarization .12  lenstat .09  build-pkg.sh .09
	//	          northwind .09  keld_oauth .09  globex .06  agentcfg.info .06
	//	          cache-ram .03  llama-server .03  digestschema .03  depreciation .03
	//	          accruals .03  larkin .03  boundretainlist .00  meridian .00
	//
	// The two lists OVERLAP between roughly .12 and .56, so 0.35 is a cut through a contested
	// band and not a gap. What it buys is nonetheless the whole point: every word that caused a
	// recorded defect on the >=7-character rule is now excluded (control, question, exactly,
	// failure, remains, whether, padding, identity), while the accounting vocabulary the
	// audience requirement turns on survives (depreciation, accruals, meridian, larkin, all
	// <=.03) — and most of the residual admissions are strong identifiers anyway, which pass on
	// their own route regardless of frequency.
	//
	// Two costs are real and are recorded rather than tuned away:
	//   - keld-signal (.41) and enrichment (.56) are excluded though a reader would call them
	//     subjects. That is IDF behaving correctly on a corpus where nearly every session is
	//     about them: a term shared by every session cannot distinguish one.
	//   - `analyzing` (.00), `ensuring` (.03), `finalizing` (.18) — the exact terms that made
	//     T12 unusable — are ADMITTED, because DF is measured over TRANSCRIPTS and those are
	//     model-prose gerunds, rare in what a human and a tool actually write. So the design's
	//     expectation that this fixes T12 is not supported by the DF table, and the sweep is
	//     what settles it. Predicted before running it, per the spec's own instruction that "if
	//     they do not move, that is a finding about the checks rather than the tokeniser".
	dfMaxFraction = 0.35

	// dfMinSessions is the cold-start floor, following lenstat's precedent exactly: stay on a
	// narrow rule until enough observations make the estimate representative.
	//
	// The DIRECTION of the fallback is the important part. lenstat falls back to a LIBERAL
	// default because its risk is a memory spike; here the risk is noise in a block labelled
	// authoritative, so the fallback is the NARROW rule — strong identifiers only, precise but
	// incomplete. Falling back to the >=7-character rule would ship the defect to exactly the
	// users least able to notice it: someone on their first session, where DF is meaningless.
	dfMinSessions = 12

	// dfSampleSessions bounds how many transcripts the table is built from. DF is a fraction,
	// so a sample estimates it; 60 sessions costs a few seconds and separates the populations
	// by the margin above. Reading all 567 on this machine would cost ~750 MB of parsing for a
	// threshold decision that a sample already makes unambiguous.
	dfSampleSessions = 60

	// dfMinTermLen is the shortest token DF will judge. The >=7 clause is gone, so short words
	// can now qualify — "Larkin" (6) is exactly the kind of specific a session-spanning record
	// exists to hold — but below 4 runes a token carries too little to be a name.
	dfMinTermLen = 4
)

// docFreq is a term -> number-of-sessions-containing-it table.
type docFreq struct {
	// sessions is how many transcripts were folded in, the denominator of every fraction.
	sessions int
	count    map[string]int
}

// fraction is the share of sampled sessions using this term.
//
// Lowercases here rather than trusting the caller. The table's keys are lowercase by
// construction, so a mixed-case lookup would return 0 — i.e. "in no session", i.e. maximally
// DISTINCTIVE — and admit exactly the ordinary capitalised English this rule exists to reject.
// A silent failure in the flattering direction, which is the shape this branch keeps finding.
func (d *docFreq) fraction(term string) float64 {
	if d == nil || d.sessions == 0 {
		return 1 // no evidence: treat as maximally generic, i.e. admit nothing on this route
	}
	return float64(d.count[strings.ToLower(term)]) / float64(d.sessions)
}

// representative reports whether the table holds enough sessions for a fraction to mean
// anything. See dfMinSessions.
func (d *docFreq) representative() bool { return d != nil && d.sessions >= dfMinSessions }

var (
	dfBuilt *docFreq
	// dfSet, when non-nil, replaces the corpus-derived table. Tests use it so a unit test never
	// depends on what happens to be in the running machine's transcript directory — which is
	// the same reason the eval harness's corpus tests are tagged and these are not.
	dfSet *docFreq
	dfMu  sync.RWMutex
)

// documentFrequency returns the table every distinctiveness decision reads.
//
// UNINITIALISED BY DEFAULT, and that is deliberate rather than lazy. An auto-scanning table
// would make every unit test in this package depend on whatever transcripts happen to be on the
// machine running it — the class of test this study has already been burned by six times, where
// a green suite certifies a property the code does not have. Uninitialised means "no corpus
// evidence", which the cold-start rule already handles in the conservative direction: strong
// identifiers only. A caller that has a corpus opts in with InitDocFreqFromCorpus.
func documentFrequency() *docFreq {
	dfMu.RLock()
	defer dfMu.RUnlock()
	if dfSet != nil {
		return dfSet
	}
	return dfBuilt
}

// InitDocFreqFromCorpus builds the table from the local transcripts, once.
//
// Idempotent and safe to call from every harness entry point. Returns the table so a caller can
// report what it got — "34 sessions, 22,604 distinct terms" is the difference between the DF rule
// being live and the cold-start rule being live, and a measurement that cannot tell those apart
// is not a measurement of either.
func InitDocFreqFromCorpus() *docFreq {
	dfMu.Lock()
	defer dfMu.Unlock()
	if dfBuilt == nil {
		dfBuilt = buildDocFreq(corpusRoot(), dfSampleSessions)
	}
	return dfBuilt
}

// withDocFreq installs a table for the duration of a test, returning the restore func.
func withDocFreq(d *docFreq) func() {
	dfMu.Lock()
	prev := dfSet
	dfSet = d
	dfMu.Unlock()
	return func() {
		dfMu.Lock()
		dfSet = prev
		dfMu.Unlock()
	}
}

// newDocFreq builds a table from explicit per-session term sets, for tests and for callers that
// already hold the corpus.
func newDocFreq(sessions [][]string) *docFreq {
	d := &docFreq{count: map[string]int{}}
	for _, terms := range sessions {
		d.sessions++
		seen := map[string]bool{}
		for _, t := range terms {
			k := strings.ToLower(t)
			if seen[k] {
				continue
			}
			seen[k] = true
			d.count[k]++
		}
	}
	return d
}

// corpusRoot is where the transcripts live. Overridable so a run can be pointed at a pinned
// snapshot — which is what makes a measurement reproducible when the live directory is growing
// underneath it.
func corpusRoot() string {
	if v := os.Getenv("KELD_STUDY_CORPUS_ROOT"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "projects")
}

// buildDocFreq counts, per term, how many of up to `max` transcripts contain it.
//
// Sessions, not occurrences: a term repeated fifty times in one session is one session's worth
// of evidence that it is generic, and counting occurrences would make any long session's
// vocabulary look universal. That distinction is the whole of why this works.
//
// Deterministic selection — paths sorted, spread across projects round-robin the way
// StratifiedTranscripts is — because a table that differs run to run would make every downstream
// figure unreproducible, and reproducibility is what retired "is it variance?" for this study.
func buildDocFreq(root string, max int) *docFreq {
	if root == "" {
		return &docFreq{count: map[string]int{}}
	}
	byProject := map[string][]string{}
	filepath.WalkDir(root, func(p string, e os.DirEntry, err error) error {
		if err == nil && !e.IsDir() && strings.HasSuffix(p, ".jsonl") {
			proj := filepath.Base(filepath.Dir(p))
			byProject[proj] = append(byProject[proj], p)
		}
		return nil
	})
	projects := make([]string, 0, len(byProject))
	for k := range byProject {
		sort.Strings(byProject[k])
		projects = append(projects, k)
	}
	sort.Slice(projects, func(i, j int) bool {
		if len(byProject[projects[i]]) != len(byProject[projects[j]]) {
			return len(byProject[projects[i]]) < len(byProject[projects[j]])
		}
		return projects[i] < projects[j]
	})
	var files []string
	for round := 0; len(files) < max; round++ {
		added := false
		for _, proj := range projects {
			if round < len(byProject[proj]) {
				files = append(files, byProject[proj][round])
				added = true
				if len(files) == max {
					break
				}
			}
		}
		if !added {
			break
		}
	}

	d := &docFreq{count: map[string]int{}}
	for _, f := range files {
		terms := sessionTermSet(f)
		if len(terms) == 0 {
			continue
		}
		d.sessions++
		for t := range terms {
			d.count[t]++
		}
	}
	return d
}

// sessionTermSet is the SET of candidate terms one transcript contains, tokenised exactly the way
// the distinctiveness test tokenises — subjectTokens, trimmed, lowercased.
//
// Identical tokenisation on both sides is load-bearing, not tidiness: a table built by a
// different splitter would answer questions about words the caller never asks about, and the
// caller's own tokens would all read as DF 0, i.e. maximally distinctive. That failure would be
// silent and would look like the fix working.
func sessionTermSet(path string) map[string]bool {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	out := map[string]bool{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 16<<20)
	for sc.Scan() {
		var l line
		if json.Unmarshal(sc.Bytes(), &l) != nil {
			continue
		}
		if l.Type != "user" && l.Type != "assistant" {
			continue
		}
		if l.IsSidechain || l.IsMeta || l.IsCompactSummary {
			continue
		}
		for _, r := range parseRecord(l) {
			for _, tok := range subjectTokens(r.text) {
				tok = trimTermPunct(tok)
				if runeLen(tok) < dfMinTermLen || runeLen(tok) > maxSubjectTermLen {
					continue
				}
				out[strings.ToLower(tok)] = true
			}
		}
	}
	return out
}
