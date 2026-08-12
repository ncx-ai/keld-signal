//go:build llmstudy

package llmstudy

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestBeatArmsArtifact renders TestBeatArmsDump's JSON as the reviewable markdown artifact, in
// the style of docs/qwen-inputs-and-outputs.md: every model call shown as what went in and what
// came out, inputs labelled by truth status, failures in place with their attempt counts.
//
// Separate from generation for the same reason the report artifact is: how the artifact READS
// must never require re-running inference. It makes no model calls and invents no figures —
// every number printed is a field of the dump or a count over its fields.
//
// It does not rank the arms and states no preference. The metric is being built separately and
// the outputs are reviewed blind.
//
//	BEAT_ARMS_DUMP=/path/out.json BEAT_ARMS_MD=docs/qwen-beat-arms-inputs-and-outputs.md \
//	  go test -tags llmstudy ./internal/agent/enrich/llmstudy/ -run BeatArmsArtifact -v
func TestBeatArmsArtifact(t *testing.T) {
	in, out := os.Getenv("BEAT_ARMS_DUMP"), os.Getenv("BEAT_ARMS_MD")
	if in == "" || out == "" {
		t.Skip("set BEAT_ARMS_DUMP and BEAT_ARMS_MD")
	}
	blob, err := os.ReadFile(in)
	if err != nil {
		t.Fatal(err)
	}
	var run beatArmsRun
	if err := json.Unmarshal(blob, &run); err != nil {
		t.Fatal(err)
	}

	var b strings.Builder
	writeArmsHeader(&b, run)
	tally := newArmsTally()
	for _, s := range run.Sessions {
		fmt.Fprintf(&b, "\n---\n\n# Session %d — %s\n\n", s.Index, s.Label)
		fmt.Fprintf(&b, "*%s · project `%s` · %d mined windows, walked to window %d*\n\n",
			s.Kind, s.Project, s.Windows, s.WalkedTo)
		fmt.Fprintf(&b, "Transcript: `%s`\n", s.Path)
		if len(s.Beats) == 0 {
			b.WriteString("\nNo beat fired in the walked prefix.\n")
		}
		for _, p := range s.Beats {
			writeArmsPoint(&b, run, s, p)
			tally.add(p)
		}
	}
	writeArmsTally(&b, tally)

	if err := os.WriteFile(out, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s (%d runes over %d beats)", out, runeLen(b.String()), tally.beats)
	for _, line := range tally.lines() {
		t.Log(line)
	}
}

func writeArmsHeader(b *strings.Builder, run beatArmsRun) {
	fmt.Fprintf(b, "# Beat generation, two arms: inputs and outputs\n\n")
	b.WriteString("Every model call below is shown as **what went in** and **what came out**, " +
		"unedited. Two arms are generated over the SAME beat windows, from the same measured " +
		"record, in a fixed order — control first, then split — so a difference between them " +
		"cannot be an artifact of which one warmed the server.\n\n")
	b.WriteString("- **control** — the current fused prompt, unchanged: *what you are working " +
		"on, and where it has got to*. One inference.\n")
	b.WriteString("- **split** — three narrow passes: **entities** (what is being worked on, " +
		"typed, over a candidate list built on device), **events** (what this window shows " +
		"happened), **composition** (the prose, written from those two and the measured record " +
		"— the conversation window is withheld). Three inferences.\n\n")
	b.WriteString("There is no description-only arm: with no movement at all the series stops " +
		"being a narrative, which is the requirement.\n\n")
	b.WriteString("**Nothing here judges the arms.** The series-level metric is being built " +
		"separately and these outputs are reviewed blind, so this artifact records what was " +
		"generated and what failed, and nothing else.\n\n")

	fmt.Fprintf(b, "Model: %s. Server: `%s`\n\n", run.Model, run.ServerFlags)
	b.WriteString("⚠️ **No resource figure is reported anywhere in this artifact, and that is " +
		"deliberate.** The viability case for this model rests on CPU-only measurements at the " +
		"documented flags (`--cache-ram` and `--no-repack` load-bearing). This run is CPU-only " +
		"but at a thread count no laptop deployment would use and without `--no-repack`, so any " +
		"latency or RAM number taken from it would misrepresent the case — including the real " +
		"question of what going from one inference to three costs a single-flight sidecar. That " +
		"question needs its own run at the viability flags.\n\n")

	fmt.Fprintf(b, "Geometry and bounds, as they were set: beat every %d user prompts · beat "+
		"window %d runes with a %d%% stride overlap · beat cap %d runes · mine K %d · entity "+
		"candidate cap %d.\n\n", run.BeatTurns, run.WindowChars, run.OverlapPct, run.BeatCap,
		run.MineK, run.CandidateCap)
	fmt.Fprintf(b, "Corpus root `%s`; document-frequency table %d sessions, %d distinct terms, "+
		"representative %v. Commit `%s`.\n\n", run.CorpusRoot, run.DFSessions, run.DFTerms,
		run.DFRepresentative, run.Commit)
	b.WriteString("**Two conventions, both stated where they apply.** Nothing read as language " +
		"is cut mid-sentence and no identifier is truncated. Each beat's conversation window is " +
		"shown ONCE, whole; where a prompt embeds that same window, the prompt is shown whole " +
		"with the window replaced by a marked reference to it — an omission, made visible, " +
		"never a silent shortening. The composition prompt is always shown in full, because " +
		"what it does *not* contain is the point.\n")
}

// windowRef is the marker that stands in for a window already shown whole above.
func windowRef(n int) string {
	return "[[ the conversation window shown above, " + strconv.Itoa(n) +
		" runes, included here verbatim ]]"
}

// withoutWindow replaces the embedded window with a reference to the copy shown above. When the
// window cannot be located the prompt is returned untouched, so the artifact never claims an
// omission it did not make.
func withoutWindow(prompt, window string) string {
	if window == "" || !strings.Contains(prompt, window) {
		return prompt
	}
	return strings.Replace(prompt, window, windowRef(runeLen(window)), 1)
}

// verbatim writes one input or output. It reuses the report artifact's `fenced`, which
// grows the fence until the content cannot close it — every window in this corpus contains code
// fences of its own.
func verbatim(b *strings.Builder, s string) {
	fenced(b, s)
	b.WriteString("\n")
}

// says renders generated prose as one block quote, reusing the report artifact's blockquote so a
// multi-line answer cannot escape the quote halfway through.
func says(b *strings.Builder, s string) {
	b.WriteString("> " + blockquote(s) + "\n\n")
}

func writeArmsPoint(b *strings.Builder, run beatArmsRun, s beatArmsSession, p beatArmsPoint) {
	fmt.Fprintf(b, "\n---\n\n## Beat at window %d of %d\n\n", p.WindowIndex, s.Windows)
	fmt.Fprintf(b, "Window geometry: %d turns in the stride since the previous beat, %d kept, "+
		"%d dropped by the character bound, %d turns (%d runes) carried forward from the "+
		"previous beat's stride. %d runes in total%s.\n\n",
		p.SpanTurns, p.KeptTurns, p.Dropped, p.OverlapTurns, p.OverlapRunes, p.TotalRunes,
		map[bool]string{true: ", with an omission notice where the hole falls", false: ""}[p.Clipped])

	b.WriteString("### Input 1 — measured record (counted on device — authoritative)\n\n")
	verbatim(b, p.Record)
	fmt.Fprintf(b, "### Input 2 — conversation window as included (%d runes — evidence)\n\n",
		runeLen(p.Window))
	verbatim(b, p.Window)

	// ---- control
	b.WriteString("### Arm: control — one fused prompt\n\n")
	fmt.Fprintf(b, "#### Prompt (%d runes, window shown above)\n\n", runeLen(p.Control.Prompt))
	verbatim(b, withoutWindow(p.Control.Prompt, p.Window))
	switch {
	case p.Control.Panicked:
		fmt.Fprintf(b, "#### FAILED — recovered panic after %d attempt(s)\n\n", p.Control.Attempts)
	case p.Control.Err != "":
		fmt.Fprintf(b, "#### FAILED after %d attempt(s)\n\n", p.Control.Attempts)
		verbatim(b, p.Control.Err)
	default:
		fmt.Fprintf(b, "#### Output — %d attempt(s)\n\n", p.Control.Attempts)
		says(b, p.Control.Text)
		writeShape(b, p.Control.Text, p.Control.Raw, run.BeatCap, p.Control.ProgressClaims,
			p.Control.Ungrounded)
	}

	// ---- split
	b.WriteString("### Arm: split — three passes\n\n")
	sp := p.Split
	fmt.Fprintf(b, "Candidate terms built on device (%d, document frequency is the salience "+
		"gate): `%s`\n\n", len(sp.Candidates), strings.Join(sp.Candidates, "`, `"))

	if sp.EntityPrompt != "" {
		fmt.Fprintf(b, "#### Pass 1 prompt — entities (%d runes, window shown above)\n\n",
			runeLen(sp.EntityPrompt))
		verbatim(b, withoutWindow(sp.EntityPrompt, p.Window))
	} else {
		b.WriteString("#### Pass 1 — entities: not asked\n\nNo candidate term passed the " +
			"device-side salience gate, so there was nothing to type and no request was issued.\n\n")
	}
	if sp.EntityErr != "" {
		fmt.Fprintf(b, "#### Pass 1 FAILED after %d attempt(s)\n\n", sp.EntityAttempts)
		verbatim(b, sp.EntityErr)
	} else if len(sp.Entities) > 0 {
		fmt.Fprintf(b, "#### Pass 1 output — %d attempt(s)\n\n", sp.EntityAttempts)
		b.WriteString("| term | kind |\n|---|---|\n")
		for _, e := range sp.Entities {
			fmt.Fprintf(b, "| `%s` | %s |\n", e.Name, e.Kind)
		}
		b.WriteString("\n")
		if len(sp.EntityUnjudged) > 0 {
			fmt.Fprintf(b, "Candidates the pass did not answer about: `%s`\n\n",
				strings.Join(sp.EntityUnjudged, "`, `"))
		}
	}

	if sp.EventPrompt != "" {
		fmt.Fprintf(b, "#### Pass 2 prompt — events (%d runes, window shown above)\n\n",
			runeLen(sp.EventPrompt))
		verbatim(b, withoutWindow(sp.EventPrompt, p.Window))
	}
	if sp.EventErr != "" {
		fmt.Fprintf(b, "#### Pass 2 FAILED after %d attempt(s)\n\n", sp.EventAttempts)
		verbatim(b, sp.EventErr)
	} else if len(sp.Events) > 0 {
		fmt.Fprintf(b, "#### Pass 2 output — %d attempt(s)\n\n", sp.EventAttempts)
		for _, e := range sp.Events {
			b.WriteString("- " + e + "\n")
		}
		b.WriteString("\n")
	}

	if sp.ComposePrompt != "" {
		fmt.Fprintf(b, "#### Pass 3 prompt — composition (%d runes, shown in full; "+
			"the conversation window is not in it)\n\n", runeLen(sp.ComposePrompt))
		verbatim(b, sp.ComposePrompt)
	}
	switch {
	case p.SplitPanicked:
		b.WriteString("#### Pass 3 FAILED — recovered panic\n\n")
	case sp.ComposeErr != "":
		fmt.Fprintf(b, "#### Pass 3 FAILED after %d attempt(s)\n\n", sp.ComposeAttempts)
		verbatim(b, sp.ComposeErr)
	case sp.Text != "":
		fmt.Fprintf(b, "#### Pass 3 output — %d attempt(s) (%d across the three passes)\n\n",
			sp.ComposeAttempts, sp.Attempts())
		says(b, sp.Text)
		writeShape(b, sp.Text, sp.Raw, run.BeatCap, sp.ProgressClaims, sp.Ungrounded)
	}
}

// writeShape prints the observations that can be made about one generated beat without judging
// it: its length against the cap, how many complete sentences survived, whether the shape checks
// found anything, and which of its specifics do not occur in what it was written from.
func writeShape(b *strings.Builder, text, raw string, cap int, claims, ungrounded []string) {
	clipped := strings.TrimSpace(raw) != text
	fmt.Fprintf(b, "*%d complete sentence(s), %d runes of %d unclipped, %s.*\n\n",
		countBeatSentences(text), runeLen(text), runeLen(raw),
		map[bool]string{true: "clipped at a sentence boundary", false: "not clipped"}[clipped])
	if len(claims) > 0 {
		fmt.Fprintf(b, "⚠️ Overall-progress claim in a STORED beat, which the shape check should "+
			"have rejected: %s\n\n", strings.Join(claims, "; "))
	}
	if len(ungrounded) > 0 {
		fmt.Fprintf(b, "Specifics in this beat that do not occur in what it was written from: "+
			"`%s`. Occurrence is tested verbatim, so a morphological variant of a term that IS "+
			"present counts here too.\n\n", strings.Join(ungrounded, "`, `"))
	}
}

// armsTally counts observations over the whole run. Every field is a count of something recorded,
// and the two arms are reported side by side without a verdict.
type armsTally struct {
	beats                       int
	cGen, cFail, cPanic, cRetry int
	sGen, sFail, sPanic, sRetry int
	failedPass                  map[string]int
	cSentences, sSentences      map[int]int
	cOpenings, sOpenings        map[string]int
	cRunes, sRunes              []int
	cClaims, sClaims            int
	cUngrounded, sUngrounded    int
	cUngroundedBeats            int
	sUngroundedBeats            int
	kinds                       map[string]int
	// byKind is what each kind was actually assigned to, counted. The histogram of kinds says
	// how often the pass used each label; this says WHICH terms it used them on, which is the
	// only way a reader can see a mistyping without opening every beat.
	byKind   map[string]map[string]int
	events   []int
	cands    []int
	unjudged int
	noise    int
}

func newArmsTally() *armsTally {
	return &armsTally{
		failedPass: map[string]int{}, kinds: map[string]int{},
		byKind:     map[string]map[string]int{},
		cSentences: map[int]int{}, sSentences: map[int]int{},
		cOpenings: map[string]int{}, sOpenings: map[string]int{},
	}
}

func opening(s string) string {
	f := strings.Fields(strings.ToLower(s))
	if len(f) > 4 {
		f = f[:4]
	}
	return strings.Join(f, " ")
}

func (a *armsTally) add(p beatArmsPoint) {
	a.beats++
	switch {
	case p.Control.Panicked:
		a.cPanic++
	case p.Control.Err != "":
		a.cFail++
	default:
		a.cGen++
		a.cSentences[countBeatSentences(p.Control.Text)]++
		a.cOpenings[opening(p.Control.Text)]++
		a.cRunes = append(a.cRunes, runeLen(p.Control.Text))
		a.cClaims += len(p.Control.ProgressClaims)
		a.cUngrounded += len(p.Control.Ungrounded)
		if len(p.Control.Ungrounded) > 0 {
			a.cUngroundedBeats++
		}
	}
	if p.Control.Attempts > 1 {
		a.cRetry++
	}

	sp := p.Split
	a.cands = append(a.cands, len(sp.Candidates))
	for _, e := range sp.Entities {
		a.kinds[string(e.Kind)]++
		if a.byKind[string(e.Kind)] == nil {
			a.byKind[string(e.Kind)] = map[string]int{}
		}
		a.byKind[string(e.Kind)][e.Name]++
		if e.Kind == KindNoise {
			a.noise++
		}
	}
	a.unjudged += len(sp.EntityUnjudged)
	if len(sp.Events) > 0 {
		a.events = append(a.events, len(sp.Events))
	}
	switch {
	case p.SplitPanicked:
		a.sPanic++
	case sp.Failed():
		a.sFail++
		a.failedPass[sp.Which()]++
	default:
		a.sGen++
		a.sSentences[countBeatSentences(sp.Text)]++
		a.sOpenings[opening(sp.Text)]++
		a.sRunes = append(a.sRunes, runeLen(sp.Text))
		a.sClaims += len(sp.ProgressClaims)
		a.sUngrounded += len(sp.Ungrounded)
		if len(sp.Ungrounded) > 0 {
			a.sUngroundedBeats++
		}
	}
	if sp.Attempts() > 3 {
		a.sRetry++
	}
}

func spread(v []int) string {
	if len(v) == 0 {
		return "none"
	}
	s := append([]int(nil), v...)
	sort.Ints(s)
	return fmt.Sprintf("min %d / median %d / max %d", s[0], s[len(s)/2], s[len(s)-1])
}

func distribution(m map[int]int) string {
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%d: %d", k, m[k]))
	}
	return strings.Join(parts, ", ")
}

func histogram(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if m[keys[i]] != m[keys[j]] {
			return m[keys[i]] > m[keys[j]]
		}
		return keys[i] < keys[j]
	})
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %d", k, m[k]))
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

func (a *armsTally) lines() []string {
	return []string{
		fmt.Sprintf("beats asked per arm: %d", a.beats),
		fmt.Sprintf("control: generated %d, failed %d, recovered panics %d, more than one attempt %d",
			a.cGen, a.cFail, a.cPanic, a.cRetry),
		fmt.Sprintf("split: generated %d, failed %d %s, recovered panics %d, more than three attempts %d",
			a.sGen, a.sFail, histogram(a.failedPass), a.sPanic, a.sRetry),
		fmt.Sprintf("sentences per beat — control {%s} | split {%s}",
			distribution(a.cSentences), distribution(a.sSentences)),
		fmt.Sprintf("runes per beat — control %s | split %s", spread(a.cRunes), spread(a.sRunes)),
		fmt.Sprintf("distinct openings (first four words) — control %d of %d | split %d of %d",
			len(a.cOpenings), a.cGen, len(a.sOpenings), a.sGen),
		fmt.Sprintf("progress claims surviving into a stored beat — control %d | split %d",
			a.cClaims, a.sClaims),
		fmt.Sprintf("beats naming a specific absent from their own inputs — control %d of %d (%d terms) | split %d of %d (%d terms)",
			a.cUngroundedBeats, a.cGen, a.cUngrounded, a.sUngroundedBeats, a.sGen, a.sUngrounded),
		fmt.Sprintf("entity candidates per beat: %s; kinds assigned: %s; noise %d; unjudged candidates %d",
			spread(a.cands), histogram(a.kinds), a.noise, a.unjudged),
		fmt.Sprintf("events per beat: %s", spread(a.events)),
	}
}

// histogramTop is histogram bounded to the n most-used terms, with the remainder counted rather
// than dropped.
func histogramTop(m map[string]int, n int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if m[keys[i]] != m[keys[j]] {
			return m[keys[i]] > m[keys[j]]
		}
		return keys[i] < keys[j]
	})
	rest := 0
	if len(keys) > n {
		for _, k := range keys[n:] {
			rest += m[k]
		}
		keys = keys[:n]
	}
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("`%s` %d", k, m[k]))
	}
	if rest > 0 {
		parts = append(parts, fmt.Sprintf("and %d more assignment(s)", rest))
	}
	return strings.Join(parts, ", ")
}

func writeArmsTally(b *strings.Builder, a *armsTally) {
	b.WriteString("\n---\n\n# What was generated\n\n")
	b.WriteString("Counts of this run's own observations. No arm is scored, ranked or preferred " +
		"here; the series-level metric belongs to a separate review.\n\n")
	for _, line := range a.lines() {
		b.WriteString("- " + line + "\n")
	}
	b.WriteString("\n## What each kind was assigned to\n\n")
	b.WriteString("The terms each kind was used on, most-used first, so a mistyping is visible " +
		"without opening every beat. Recorded, not corrected.\n\n")
	kinds := make([]string, 0, len(a.byKind))
	for k := range a.byKind {
		kinds = append(kinds, k)
	}
	sort.Slice(kinds, func(i, j int) bool { return a.kinds[kinds[i]] > a.kinds[kinds[j]] })
	for _, k := range kinds {
		fmt.Fprintf(b, "- **%s** (%d): %s\n", k, a.kinds[k], histogramTop(a.byKind[k], 10))
	}
	b.WriteString("\nTwo notes on how to read the last two lines. *Progress claims surviving " +
		"into a stored beat* should be zero on both arms — the check runs inside the retry " +
		"loop, so a non-zero count means an offending generation reached the series anyway. " +
		"*Specifics absent from their own inputs* is not symmetric between the arms and cannot " +
		"be compared as if it were: the control's inputs include the whole conversation, so " +
		"almost anything it writes occurs somewhere in them, while the split arm's inputs are " +
		"the two passes and the record, which is a far smaller haystack. It measures the " +
		"constraint, not the accuracy.\n")
}
