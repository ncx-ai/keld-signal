//go:build llmstudy

package llmstudy

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
)

// TestBeatRunArtifact renders TestBeatRunDump's JSON as the reviewable markdown artifact, in the
// style of docs/qwen-inputs-and-outputs.md: every model call shown as what went in and what came
// out, inputs labelled by truth status, failures in place with their attempt counts.
//
// Separate from generation for the same reason the report artifact is: how the artifact READS
// must never require re-running inference. It makes no model calls and invents no figures — every
// number printed is a field of the dump or a count over its fields.
//
// It ranks nothing and states no preference. The quality question is scored blind, by readers,
// against a metric this harness does not own.
//
//	BEAT_RUN_DUMP=/path/out.json BEAT_RUN_MD=/path/qwen-beat-inputs-and-outputs.md \
//	  go test -tags llmstudy ./internal/agent/enrich/llmstudy/ -run BeatRunArtifact -v
func TestBeatRunArtifact(t *testing.T) {
	in, out := os.Getenv("BEAT_RUN_DUMP"), os.Getenv("BEAT_RUN_MD")
	if in == "" || out == "" {
		t.Skip("set BEAT_RUN_DUMP and BEAT_RUN_MD")
	}
	blob, err := os.ReadFile(in)
	if err != nil {
		t.Fatal(err)
	}
	var run beatRun
	if err := json.Unmarshal(blob, &run); err != nil {
		t.Fatal(err)
	}

	var b strings.Builder
	writeRunHeader(&b, run)
	for _, s := range run.Sessions {
		writeRunSession(&b, s)
	}
	writeRunTally(&b, run)

	if err := os.WriteFile(out, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	tl := run.tally()
	t.Logf("wrote %s (%d runes over %d beats)", out, runeLen(b.String()), tl.asked)
	for _, line := range tl.lines() {
		t.Log(line)
	}
	for _, split := range []struct {
		name string
		pred func(beatRunSession) bool
	}{
		{"REAL", func(s beatRunSession) bool { return !s.Synthetic }},
		{"SYNTHETIC", func(s beatRunSession) bool { return s.Synthetic }},
	} {
		for _, line := range run.tallyWhere(split.pred).lines() {
			t.Logf("%s  %s", split.name, line)
		}
	}
}

func writeRunHeader(b *strings.Builder, run beatRun) {
	b.WriteString("# Beat generation: inputs and outputs\n\n")
	b.WriteString("Every model call below is shown as **what went in** and **what came out**, " +
		"unedited. One inference per beat: a window and the measured record in, a subject line " +
		"and a list of observed events out.\n\n")
	b.WriteString("**Nothing here judges the output.** The outputs are reviewed blind against a " +
		"metric this artifact does not own, so what it records is what was generated, what was " +
		"dropped, and what failed — and nothing else.\n\n")

	fmt.Fprintf(b, "Model: %s. Server: `%s`\n\n", run.Model, run.ServerFlags)
	b.WriteString("⚠️ **No resource figure is reported anywhere in this artifact, and that is " +
		"deliberate.** The viability case for this model rests on CPU-only measurements at the " +
		"documented flags (`--cache-ram` and `--no-repack` load-bearing). This run is CPU-only " +
		"but at a thread count no laptop deployment would use and without `--no-repack`, so any " +
		"latency or RAM number taken from it would misrepresent the case. That question needs " +
		"its own run at the viability flags.\n\n")

	fmt.Fprintf(b, "Geometry and bounds, as they were set: beat every %d user prompts · beat "+
		"window %d runes, disjoint (stride equals window, holes marked in the window) · stored "+
		"beat capped at %d runes · one entry capped at %d runes, at most %d entries · assembled "+
		"prompt budget %d runes.\n\n", run.BeatTurns, run.WindowChars, run.BeatCap, run.EventCap,
		run.EventMaxCount, run.PromptBudget)
	fmt.Fprintf(b, "Corpus root `%s`; document-frequency table %d sessions, %d distinct terms, "+
		"representative %v. Commit `%s`.\n\n", run.CorpusRoot, run.DFSessions, run.DFTerms,
		run.DFRepresentative, run.Commit)

	var real, synth int
	for _, s := range run.Sessions {
		if s.Synthetic {
			synth++
		} else {
			real++
		}
	}
	fmt.Fprintf(b, "**Corpus: %d real transcripts and %d hand-authored sessions.** The real "+
		"transcripts are the corpus; the two hand-authored non-engineering sessions are a "+
		"labelled minority check, marked SYNTHETIC wherever they appear, and every figure below "+
		"is reported for each population separately as well as together. They are kept because "+
		"the pinned snapshot holds only Claude Code transcripts and they are the only "+
		"non-engineering material there is.\n\n", real, synth)
	b.WriteString("**The corpus is deduplicated on window content, not on session id.** Two of " +
		"the previous sweep's twelve real sessions were the same conversation under two ids — a " +
		"fork or resume, byte-identical over all six of their beat windows — so twelve sessions " +
		"were eleven conversations and two of its three generation failures were one window " +
		"counted twice. Selection now fingerprints the turns each session would show the model " +
		"and skips a repeat.\n\n")
	b.WriteString("**The sessions the prompt's worked examples were read from are held out of " +
		"this corpus**, and the separation is checked mechanically rather than asserted: no " +
		"example's subject line, and no strong identifier or capitalised term any example uses, " +
		"occurs anywhere in any of these sessions' windows or records. The examples are:\n\n")
	for _, ex := range beatExamples {
		fmt.Fprintf(b, "- `%s` — read from `%s`\n", ex.Subject, ex.Source)
	}
	b.WriteString("\n**The prompt is one template, shown once.** It is identical for every beat " +
		"apart from the two inputs it embeds, which are shown per beat below; embedding it again " +
		"under each would repeat every window twice. `{record}` and `{window}` mark where they go.\n\n")
	fenced(b, promptTemplate(run))
	b.WriteString("\n**Two conventions, both stated where they apply.** Nothing read as language " +
		"is cut mid-sentence and no identifier is truncated. Each beat's window is shown once, " +
		"whole. Where the anchoring guard dropped an entry, the entry is printed under the beat " +
		"it was dropped from and the beat itself carries the drop marker — a dropped entry is " +
		"never silent.\n")
}

// promptTemplate reproduces the assembled prompt with its two inputs replaced by markers, taken
// from a real beat so the template is the one that actually ran rather than a re-derivation.
func promptTemplate(run beatRun) string {
	for _, s := range run.Sessions {
		for _, p := range s.Beats {
			if p.Prompt == "" {
				continue
			}
			t := p.Prompt
			if p.Record != "" {
				t = strings.Replace(t, p.Record, "{record}", 1)
			}
			if p.Window != "" {
				t = strings.Replace(t, p.Window, "{window}", 1)
			}
			return t
		}
	}
	return BeatPrompt("{record}", "{window}")
}

func writeRunSession(b *strings.Builder, s beatRunSession) {
	fmt.Fprintf(b, "\n---\n\n# %s\n\n", s.Label)
	fmt.Fprintf(b, "*%s · %d mined windows*\n\n", s.Kind, s.Windows)
	fmt.Fprintf(b, "Transcript: `%s` · project `%s` · walked to window %d\n\n",
		s.Path, s.Project, s.WalkedTo)
	c := s.Coverage
	fmt.Fprintf(b, "Window coverage over the walked prefix: %d turns spanned by the beat "+
		"strides, %d of them read by a window (%.1f%%), %d read by none; %d of %d windows carry "+
		"a hole marker; largest window %d runes.\n",
		c.SpanTurns, c.KeptTurns, c.TurnCoverage, c.UnreadTurns, c.Holed, c.Windows,
		c.LargestRunes)
	if len(s.Beats) == 0 {
		b.WriteString("\nNo beat fired in the walked prefix.\n")
	}
	for _, p := range s.Beats {
		writeRunPoint(b, s, p)
	}
}

func writeRunPoint(b *strings.Builder, s beatRunSession, p beatRunPoint) {
	if p.Failed() {
		fmt.Fprintf(b, "\n---\n\n## Beat at window %d — GENERATION FAILED after %d attempt(s)\n\n",
			p.WindowIndex, p.Attempts)
		if p.Panicked {
			b.WriteString("A recovered panic, not a rejection.\n\n")
		}
		fenced(b, p.Err)
		b.WriteString("\n")
		return
	}
	fmt.Fprintf(b, "\n---\n\n## Beat %d (window %d of %d) · %d attempt(s)\n\n",
		p.Ordinal, p.WindowIndex, s.Windows, p.Attempts)
	fmt.Fprintf(b, "Window geometry: %d turns in the stride since the previous beat, %d kept, "+
		"%d dropped by the character bound%s. %d runes in the window; %d runes in the assembled "+
		"prompt against a %d-rune budget.\n\n",
		p.SpanTurns, p.KeptTurns, p.Dropped,
		map[bool]string{true: " (a hole marker sits where they were)", false: ""}[p.Holed],
		p.TotalRunes, p.PromptRunes, BeatPromptCharBudget)

	b.WriteString("### Input 1 — measured record (counted on device — authoritative)\n\n")
	fenced(b, p.Record)
	b.WriteString("\n")
	fmt.Fprintf(b, "### Input 2 — conversation window (%d runes — evidence)\n\n", runeLen(p.Window))
	fenced(b, p.Window)
	b.WriteString("\n")

	b.WriteString("### Output\n\n")
	b.WriteString("> " + blockquote(p.Text) + "\n\n")

	if len(p.Anchors) > 0 {
		var terms []string
		for _, a := range p.Anchors {
			switch {
			case a.Specifics == 0:
				terms = append(terms, "(names nothing checkable)")
			case !a.InWindow:
				terms = append(terms, a.Term+" (record only)")
			default:
				terms = append(terms, a.Term)
			}
		}
		fmt.Fprintf(b, "What each kept entry was checked on — every specific it names had to occur "+
			"in this window or the record, and an entry naming none is unconstrained: %s.\n\n",
			"`"+strings.Join(terms, "`, `")+"`")
	}
	if p.SubjectRejects > 0 {
		fmt.Fprintf(b, "The subject rule re-requested this beat %d time(s) before the subject "+
			"stood: a subject carrying no term from the evidence is the signature of a copied "+
			"instruction, so it is re-sampled at a wider temperature rather than stored.\n\n",
			p.SubjectRejects)
	}
	if len(p.Unverified) > 0 {
		fmt.Fprintf(b, "Identifiers this beat names that occur nowhere in its evidence: `%s`. "+
			"Recorded, not dropped.\n\n", strings.Join(p.Unverified, "`, `"))
	}
	if echoed := echoedExampleNames(p.Text); len(echoed) > 0 {
		fmt.Fprintf(b, "⚠️ **This beat names %s from the prompt's own worked examples**, which "+
			"belong to a held-out session and cannot have come from this window: `%s`. The "+
			"anchoring guard does not catch it — the entry carrying it also carries an ordinary "+
			"word that does occur in the window, and that is what it anchors on. It is visible "+
			"at all only because the examples are held out of this corpus; against the "+
			"contaminated set it would have read as a correct, anchored beat.\n\n",
			map[bool]string{true: "a term", false: "terms"}[len(echoed) == 1],
			strings.Join(echoed, "`, `"))
	}
	if len(p.Unanchored) > 0 {
		fmt.Fprintf(b, "**Dropped by the anchoring guard (%d):** each of these names a specific — "+
			"an identifier, a number, a proper noun — that occurs in neither this window nor the "+
			"record. The specific is named after the entry, so the decision can be checked rather "+
			"than trusted.\n\n", len(p.Unanchored))
		for i, e := range p.Unanchored {
			b.WriteString("- " + e)
			if i < len(p.Fabricated) && p.Fabricated[i] != "" {
				b.WriteString("  — not in the evidence: `" + p.Fabricated[i] + "`")
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	if len(p.Overflowed) > 0 {
		fmt.Fprintf(b, "**Dropped to fit the %d-rune beat cap (%d):**\n\n", BeatCap,
			len(p.Overflowed))
		for _, e := range p.Overflowed {
			b.WriteString("- " + e + "\n")
		}
		b.WriteString("\n")
	}
}

// runTally counts observations over the whole run. Every field is a count of something recorded.
type runTally struct {
	sessions                    int
	asked, generated            int
	failed, panicked            int
	firstAttempt, retried       int
	attempts                    []int
	entries                     []int
	unanchoredEntries           int
	unanchoredBeats             int
	overflowEntries             int
	overflowBeats               int
	unconstrained               int
	subjectRejects              int
	subjectRetriedBeats         int
	subjectResidualFailures     int
	subjectUnanchored           int
	recordOnly                  int
	exampleEcho                 int
	exampleEchoBeats            int
	unverified                  int
	unverifiedBeats             int
	promptRunes                 []int
	promptOverBudget            int
	spanTurns, keptTurns, holed int
	windows                     int
	largestWindow               int
	beatRunes                   []int
}

// tally counts every session; tallyWhere counts the ones a predicate admits.
//
// ⚠️ THE SPLIT IS THE POINT. Real and synthetic sessions are different material — the synthetic
// pair is short, clean and invented, the real transcripts carry pasted code, tool dumps,
// interruptions and mid-task redirections — so a single figure over both describes neither. The
// previous run's "19 of 19 on the first attempt" was 7 of those 19 beats from hand-authored
// sessions, and nothing in the artifact said so.
func (r beatRun) tally() *runTally { return r.tallyWhere(func(beatRunSession) bool { return true }) }

func (r beatRun) tallyWhere(pred func(beatRunSession) bool) *runTally {
	t := &runTally{}
	for _, s := range r.Sessions {
		if !pred(s) {
			continue
		}
		t.sessions++
		t.spanTurns += s.Coverage.SpanTurns
		t.keptTurns += s.Coverage.KeptTurns
		t.holed += s.Coverage.Holed
		t.windows += s.Coverage.Windows
		if s.Coverage.LargestRunes > t.largestWindow {
			t.largestWindow = s.Coverage.LargestRunes
		}
		for _, p := range s.Beats {
			t.asked++
			t.attempts = append(t.attempts, p.Attempts)
			t.promptRunes = append(t.promptRunes, p.PromptRunes)
			if p.PromptRunes > r.PromptBudget {
				t.promptOverBudget++
			}
			// Counted over every beat asked, not only the ones that survived: a beat the subject
			// rule re-requested four times and then lost is exactly what this figure is for.
			t.subjectRejects += p.SubjectRejects
			if p.SubjectRejects > 0 {
				t.subjectRetriedBeats++
			}
			if p.FailedOnSubject() {
				t.subjectResidualFailures++
			}
			switch {
			case p.Panicked:
				t.panicked++
				continue
			case p.Err != "":
				t.failed++
				continue
			}
			t.generated++
			if p.Attempts <= 1 {
				t.firstAttempt++
			} else {
				t.retried++
			}
			t.entries = append(t.entries, len(p.Events))
			t.beatRunes = append(t.beatRunes, runeLen(p.Text))
			if n := len(p.Unanchored); n > 0 {
				t.unanchoredEntries += n
				t.unanchoredBeats++
			}
			if n := len(p.Overflowed); n > 0 {
				t.overflowEntries += n
				t.overflowBeats++
			}
			if !p.SubjectAnchored {
				t.subjectUnanchored++
			}
			for _, a := range p.Anchors {
				if a.Specifics == 0 {
					t.unconstrained++
				}
			}
			t.recordOnly += p.RecordOnlyAnchors()
			if n := len(echoedExampleNames(p.Text)); n > 0 {
				t.exampleEcho += n
				t.exampleEchoBeats++
			}
			if n := len(p.Unverified); n > 0 {
				t.unverified += n
				t.unverifiedBeats++
			}
		}
	}
	return t
}

func (t *runTally) lines() []string {
	offered := t.unanchoredEntries
	for _, n := range t.entries {
		offered += n
	}
	return []string{
		fmt.Sprintf("sessions %d; beats asked %d, generated %d, failed %d, recovered panics %d",
			t.sessions, t.asked, t.generated, t.failed, t.panicked),
		// The distribution, not just the spread: "first attempt every time" is the figure most
		// likely to move on messier material, and a median cannot show one beat that took five.
		fmt.Sprintf("attempts per beat: %s; distribution %s; first attempt %d, more than one %d",
			spread(t.attempts), histogram(t.attempts), t.firstAttempt, t.retried),
		fmt.Sprintf("entries per stored beat: %s; stored beat runes: %s",
			spread(t.entries), spread(t.beatRunes)),
		fmt.Sprintf("entries dropped by the anchoring guard (a specific occurring in neither the "+
			"window nor the record): %d of %d offered, across %d of %d beats",
			t.unanchoredEntries, offered, t.unanchoredBeats, t.generated),
		fmt.Sprintf("kept entries naming NO specific, so unconstrained by the guard: %d of %d kept",
			t.unconstrained, offered-t.unanchoredEntries),
		fmt.Sprintf("entries dropped to fit the beat cap: %d across %d beats",
			t.overflowEntries, t.overflowBeats),
		fmt.Sprintf("beats re-requested for an unanchored SUBJECT: %d beats, %d attempts; still "+
			"failing after the ladder: %d; stored beats whose subject carries no term from the "+
			"evidence: %d of %d", t.subjectRetriedBeats, t.subjectRejects,
			t.subjectResidualFailures, t.subjectUnanchored, t.generated),
		fmt.Sprintf("entries checked against the record and NOT their own window (the seam signal): "+
			"%d of %d kept", t.recordOnly, offered-t.unanchoredEntries),
		fmt.Sprintf("identifiers named that occur nowhere in the evidence: %d across %d of %d beats",
			t.unverified, t.unverifiedBeats, t.generated),
		fmt.Sprintf("names taken from the prompt's OWN worked examples: %d across %d of %d beats",
			t.exampleEcho, t.exampleEchoBeats, t.generated),
		fmt.Sprintf("turn coverage: %d of %d turns read by a window (%.1f%%), %d read by none; "+
			"%d of %d windows hole-marked", t.keptTurns, t.spanTurns,
			100*float64(t.keptTurns)/float64(max(1, t.spanTurns)), t.spanTurns-t.keptTurns,
			t.holed, t.windows),
		fmt.Sprintf("assembled prompt runes: %s; over budget %d", spread(t.promptRunes),
			t.promptOverBudget),
	}
}

func writeRunTally(b *strings.Builder, run beatRun) {
	b.WriteString("\n---\n\n# What was generated\n\n")
	b.WriteString("Counts of this run's own observations. Nothing here is scored or ranked; the " +
		"quality question belongs to the blind review round.\n\n")
	for _, line := range run.tally().lines() {
		b.WriteString("- " + line + "\n")
	}
	b.WriteString("\n## The same figures, real transcripts and synthetic sessions apart\n\n")
	b.WriteString("The two hand-authored non-engineering sessions are short, clean and invented; " +
		"the real transcripts carry pasted code, tool output, interruptions and mid-task " +
		"redirections. A figure averaged over both describes neither, so both populations are " +
		"counted separately here as well as together above.\n")
	for _, split := range []struct {
		name string
		pred func(beatRunSession) bool
	}{
		{"Real transcripts", func(s beatRunSession) bool { return !s.Synthetic }},
		{"Synthetic sessions", func(s beatRunSession) bool { return s.Synthetic }},
	} {
		fmt.Fprintf(b, "\n**%s**\n\n", split.name)
		for _, line := range run.tallyWhere(split.pred).lines() {
			b.WriteString("- " + line + "\n")
		}
	}
	b.WriteString("\nHow to read the anchoring line: an entry is dropped when a SPECIFIC it " +
		"names — an identifier-shaped token, a number or amount, or a proper noun capitalised " +
		"somewhere other than the start of its sentence — occurs in neither that beat's own " +
		"window nor the measured record. Every specific must hold, not one of them; an entry " +
		"naming no specific is unconstrained and passes, since it cannot fabricate a specific it " +
		"does not have. It is a fact about a string's occurrence, not a judgement about the " +
		"entry — see `beat_anchor.go`. The SUBJECT line is measured by the wider rule (any term " +
		"of four or more runes that is not a function word) and is not dropped but RE-REQUESTED " +
		"at a wider temperature, because a subject carrying nothing from the evidence was, on " +
		"the previous sweep, exactly and only the beats that copied the prompt's worked " +
		"examples.\n")
}

// histogram renders every observed value with its count, ascending: "1 attempt x63, 2 x4".
// Counts, not a summary — the whole point of printing it is that a rare high value stays visible.
func histogram(v []int) string {
	if len(v) == 0 {
		return "none"
	}
	n := map[int]int{}
	keys := []int{}
	for _, x := range v {
		if n[x] == 0 {
			keys = append(keys, x)
		}
		n[x]++
	}
	sort.Ints(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%d x%d", k, n[k]))
	}
	return strings.Join(parts, ", ")
}

// echoedExampleNames returns the names a stored beat took from the prompt's own worked examples.
//
// ⚠️ THIS FACT ONLY EXISTS BECAUSE THE EXAMPLES ARE HELD OUT. Every example is read from a
// session excluded from this corpus, and the hold-out test proves none of their names occurs in
// any window or record here — so a beat carrying one cannot have read it, and instruction copying
// becomes a measurable event rather than an unfalsifiable worry. Against the contaminated set it
// was invisible by construction: an example drawn FROM the corpus supplies names the window also
// has, and a copied beat reads as an anchored one.
//
// The anchoring guard does not and cannot catch this: an entry that copies an example still
// carries ordinary words the window contains, and anchoring is an OR over the entry's terms.
func echoedExampleNames(text string) []string {
	hay := strings.ToLower(text)
	var out []string
	seen := map[string]bool{}
	for _, ex := range beatExamples {
		for _, name := range beatExampleNames(ex) {
			k := strings.ToLower(name)
			if !seen[k] && strings.Contains(hay, k) {
				seen[k] = true
				out = append(out, name)
			}
		}
	}
	return out
}

func spread(v []int) string {
	if len(v) == 0 {
		return "none"
	}
	s := append([]int(nil), v...)
	sort.Ints(s)
	return fmt.Sprintf("min %d / median %d / max %d", s[0], s[len(s)/2], s[len(s)-1])
}
