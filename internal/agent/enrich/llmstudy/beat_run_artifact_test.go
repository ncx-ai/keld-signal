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

	b.WriteString("**The prompt is one template, shown once.** It is identical for every beat " +
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
		fmt.Fprintf(b, "Anchoring term each entry was kept on: `%s`.%s\n\n",
			strings.Join(p.Anchors, "`, `"),
			map[bool]string{
				true:  "",
				false: " The subject line carries no term occurring in the evidence.",
			}[p.SubjectAnchored])
	}
	if len(p.Unanchored) > 0 {
		fmt.Fprintf(b, "**Dropped by the anchoring guard (%d):** no term in these occurs "+
			"verbatim in this window or the record.\n\n", len(p.Unanchored))
		for _, e := range p.Unanchored {
			b.WriteString("- " + e + "\n")
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
	subjectUnanchored           int
	promptRunes                 []int
	promptOverBudget            int
	spanTurns, keptTurns, holed int
	windows                     int
	largestWindow               int
	beatRunes                   []int
}

func (r beatRun) tally() *runTally {
	t := &runTally{}
	for _, s := range r.Sessions {
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
		fmt.Sprintf("attempts per beat: %s; first attempt %d, more than one %d",
			spread(t.attempts), t.firstAttempt, t.retried),
		fmt.Sprintf("entries per stored beat: %s; stored beat runes: %s",
			spread(t.entries), spread(t.beatRunes)),
		fmt.Sprintf("entries dropped by the anchoring guard: %d of %d offered, across %d of %d beats",
			t.unanchoredEntries, offered, t.unanchoredBeats, t.generated),
		fmt.Sprintf("entries dropped to fit the beat cap: %d across %d beats",
			t.overflowEntries, t.overflowBeats),
		fmt.Sprintf("beats whose SUBJECT carries no term occurring in the evidence: %d of %d",
			t.subjectUnanchored, t.generated),
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
	b.WriteString("\nHow to read the anchoring line: an entry is dropped when no term in it " +
		"occurs verbatim in that beat's own window or in the measured record. It is a fact about " +
		"a string's occurrence, not a judgement about the entry — see `beat_anchor.go` for what " +
		"counts as a term. The subject line is measured the same way but never dropped, since " +
		"dropping it would leave no beat.\n")
}

func spread(v []int) string {
	if len(v) == 0 {
		return "none"
	}
	s := append([]int(nil), v...)
	sort.Ints(s)
	return fmt.Sprintf("min %d / median %d / max %d", s[0], s[len(s)/2], s[len(s)-1])
}
