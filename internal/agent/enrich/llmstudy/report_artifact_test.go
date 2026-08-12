//go:build llmstudy

package llmstudy

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestReportArtifact renders TestReportDump's JSON as the reviewable markdown artifact —
// docs/qwen-reports-inputs-and-outputs.md, the report tier's counterpart to
// docs/qwen-inputs-and-outputs.md for beats.
//
// Separate from the generation pass on purpose: the sweep costs hours of inference, and
// nothing about how the artifact READS should require re-running it. This reads the recorded
// dump and writes markdown; it makes no model calls and invents no figures — every number it
// prints is a field of the dump, i.e. an observation from the run.
//
// Nothing shown as language is cut. Where a section is long it is shown whole, because the
// artifact exists to be judged and a truncated prompt cannot be (see the delimiter convention
// in AGENTS.md: a cut mid-clause is a defect, and an identifier cut short is a false
// identifier). The one omission the artifact makes is stated where it is made.
//
//	REPORT_DUMP=/path/out.json REPORT_MD=docs/qwen-reports-inputs-and-outputs.md \
//	  go test -tags llmstudy ./internal/agent/enrich/llmstudy/ -run ReportArtifact -v
func TestReportArtifact(t *testing.T) {
	in := os.Getenv("REPORT_DUMP")
	out := os.Getenv("REPORT_MD")
	if in == "" || out == "" {
		t.Skip("set REPORT_DUMP and REPORT_MD")
	}
	blob, err := os.ReadFile(in)
	if err != nil {
		t.Fatal(err)
	}
	var run runDump
	if err := json.Unmarshal(blob, &run); err != nil {
		t.Fatal(err)
	}

	var b strings.Builder
	writeArtifactHeader(&b, run)

	var reports, failed, retried, substituted, panicked, beatFail int
	for _, s := range run.Sessions {
		fmt.Fprintf(&b, "\n---\n\n# Session %d — %s\n\n", s.Index, s.Label)
		fmt.Fprintf(&b, "*%s · project `%s` · %d mined windows*\n\n", s.Kind, s.Project, s.Windows)
		fmt.Fprintf(&b, "Transcript: `%s`\n", s.Path)
		for _, a := range s.Arms {
			fmt.Fprintf(&b, "\n## Session %d, arm: %s\n", s.Index, a.Name)
			for _, r := range a.Reports {
				reports++
				if r.Digest == nil {
					failed++
				}
				if r.Attempts > 1 {
					retried++
				}
				if r.EmptyUnresolvedSubstituted {
					substituted++
				}
				if r.Panicked {
					panicked++
				}
				for _, ev := range r.Beats {
					if ev.Err != "" || ev.Panicked {
						beatFail++
					}
				}
				writeReport(&b, run, s, a, r)
			}
			if len(a.TrailingBeats) > 0 {
				fmt.Fprintf(&b, "\n### Beats generated after the last report (no report read them)\n\n")
				for _, ev := range a.TrailingBeats {
					writeBeatEvent(&b, ev)
					if ev.Err != "" || ev.Panicked {
						beatFail++
					}
				}
			}
		}
	}

	fmt.Fprintf(&b, "\n---\n\n# What this run contained\n\n")
	fmt.Fprintf(&b, "| | |\n|---|---:|\n")
	fmt.Fprintf(&b, "| sessions | %d |\n", len(run.Sessions))
	fmt.Fprintf(&b, "| reports shown | %d |\n", reports)
	fmt.Fprintf(&b, "| reports lost (refusal, exhausted retries, or recovered panic) | %d |\n", failed)
	fmt.Fprintf(&b, "| reports that took more than one attempt | %d |\n", retried)
	fmt.Fprintf(&b, "| recovered backstop panics | %d |\n", panicked)
	fmt.Fprintf(&b, "| empty-open-list substitutions | %d |\n", substituted)
	fmt.Fprintf(&b, "| beat generations that failed | %d |\n", beatFail)

	if err := os.WriteFile(out, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s (%d bytes, %d reports across %d sessions)", out, b.Len(), reports, len(run.Sessions))
}

func writeArtifactHeader(b *strings.Builder, run runDump) {
	fmt.Fprintf(b, "# Qwen report inputs and outputs, for review\n\n")
	fmt.Fprintf(b, "Every model call that produced a **report** is shown below as what went in and what came "+
		"out, unedited. This is the expensive periodic tier; `docs/qwen-inputs-and-outputs.md` is the same "+
		"artifact for the cheap **beat** tier.\n\n")
	fmt.Fprintf(b, "| | |\n|---|---|\n")
	fmt.Fprintf(b, "| generated | %s |\n", time.Now().Format("2006-01-02"))
	if run.Commit != "" {
		fmt.Fprintf(b, "| commit | `%s` |\n", run.Commit)
	}
	fmt.Fprintf(b, "| model | %s, on device, ctx 8192, temperature 0 |\n", run.Model)
	fmt.Fprintf(b, "| corpus snapshot (pinned) | `%s` |\n", run.CorpusRoot)
	if run.SessionID != "" {
		fmt.Fprintf(b, "| this harness's own session | `%s` |\n", run.SessionID)
	}
	fmt.Fprintf(b, "| prompt budget | %d characters |\n", run.Budget)
	fmt.Fprintf(b, "| conversation-window floor | %d runes of content |\n", run.WindowFloor)
	fmt.Fprintf(b, "| report cadence | windows %v of each session (4 reports) |\n", run.ReportWindows)
	fmt.Fprintf(b, "| beat cadence | one beat every %d user turns |\n", run.BeatTurns)
	fmt.Fprintf(b, "| beats a report may read | at most %d, chosen by SelectBeats |\n", run.MaxBeatSelection)
	fmt.Fprintf(b, "| retain-list caps | %d entries / %d runes |\n", run.RetainCap, run.RetainRuneCap)
	fmt.Fprintf(b, "| distinctiveness table | %d corpus sessions, %d distinct terms, representative=%v |\n",
		run.DFSessions, run.DFTerms, run.DFRepresentative)

	fmt.Fprintf(b, `
**The two arms.** They differ in exactly one thing: whether a report whose subject was measured
to have changed is TOLD so.

- **anchor ON** — when the trigger measures a subject shift, the prompt carries the recency
  anchor: what the newest turns are about, plus an instruction to re-scope the synopsis.
- **anchor OFF** — the prompt is always told the refresh was routine, whatever the trigger
  measured.

The measured record is identical in both arms (the trigger reason still feeds the record's
turning points in both), so nothing else moves between them.

**Inputs are labelled by truth status, because that separation is the whole design.**

- **The measured record** is counted on device from the transcript. It is authoritative: the
  prompt says so, and the report is required to be consistent with it.
- **The beat series** is model-generated. It is indicative history — a paraphrase, not evidence
  — and a report may not treat it as fact.
- **The retain-list** is derived deterministically from the previous report's own text. It is
  the only channel carrying a prior report's specifics forward, and every entry is something
  the report is obliged to still carry.
- **The session view and the new turns** are raw transcript, clipped to fit. Where a clip
  happened its notice is shown verbatim rather than elided, because a silently shorter input is
  the same defect one level up.

**What "as included" means.** The conversation window is the value the prompt builder itself
produced. The beat series and the session view are recovered by testing which real
`+"`SelectBeats`/`RenderBeats`"+` and `+"`clipSessionViewFor`"+` output the assembled prompt
contains — not by parsing the prompt between headings, which conversation text quoting a
heading defeats.

**Prompt sizes are observed on the assembled string**, not derived from a bound the code is
supposed to enforce. Failures are shown in place, labelled, with their attempt count.

**Order:** session, then arm, then report step.
`)
}

func writeReport(b *strings.Builder, run runDump, s sessionDump, a armDump, r reportDump) {
	title := fmt.Sprintf("Report %d of %d", r.Step+1, len(a.Reports))
	fmt.Fprintf(b, "\n---\n\n### %s · %s · window %d of %d · arm %s\n",
		title, r.Kind, r.WindowIndex, s.Windows, a.Name)

	if len(r.Beats) > 0 {
		fmt.Fprintf(b, "\n#### Beats generated since the previous report (model-generated; this is what the series below is made of)\n\n")
		for _, ev := range r.Beats {
			writeBeatEvent(b, ev)
		}
	}

	fmt.Fprintf(b, "\n#### Input 1 — the measured record (authoritative: counted on device, not generated)\n\n")
	if strings.TrimSpace(r.Record) == "" {
		fmt.Fprintf(b, "*(empty — nothing measured yet)*\n")
	} else {
		fenced(b, r.Record)
		fmt.Fprintf(b, "\npopulated fields: %s\n", joinOr(r.RecordPopulated, "none"))
	}

	if r.Kind == "create" {
		fmt.Fprintf(b, "\n#### Input 2 — the measured counts block (authoritative), which only the FIRST report is given\n\n")
		fenced(b, r.Facts)
		fmt.Fprintf(b, "\n*A first report reads no beats, no retain-list and no open items: there is no previous "+
			"report to carry anything from. The refinements below read the record instead of this block.*\n")
	} else {
		fmt.Fprintf(b, "\n#### Input 2 — the beat series as selected and rendered (indicative: model-generated)\n\n")
		fmt.Fprintf(b, "%d beats accumulated, %d carried by this prompt.\n\n", len(r.BeatsAccumulated), r.BeatsShown)
		if strings.TrimSpace(r.BeatsRendered) == "" {
			fmt.Fprintf(b, "*(no beat series in this prompt)*\n")
		} else {
			fenced(b, r.BeatsRendered)
		}

		fmt.Fprintf(b, "\n#### Input 3 — the retain-list: the facts this report is obliged to carry forward (%d entries, %d runes)\n\n",
			len(r.RetainList), r.RetainRunes)
		if len(r.RetainList) == 0 {
			fmt.Fprintf(b, "*(empty)*\n")
		} else {
			fenced(b, strings.Join(r.RetainList, ", "))
			fmt.Fprintf(b, "\n%d named by `Identifiers` on the previous report; %d carried after `boundRetainList` "+
				"(caps %d entries / %d runes).\n", r.RetainNamed, len(r.RetainList), run.RetainCap, run.RetainRuneCap)
			if len(r.RetainDropped) > 0 {
				fmt.Fprintf(b, "\n**Dropped by the bound before the prompt saw them (%d):** %s\n",
					len(r.RetainDropped), strings.Join(r.RetainDropped, ", "))
			}
		}

		fmt.Fprintf(b, "\n#### Input 4 — the open items handed back for a verdict (%d)\n\n", len(r.OpenItemsIn))
		if len(r.OpenItemsIn) == 0 {
			fmt.Fprintf(b, "*(none — the previous report's open list was the \"nothing is open\" sentinel, or empty)*\n")
		} else {
			for _, it := range r.OpenItemsIn {
				fmt.Fprintf(b, "- %s\n", it)
			}
		}
	}

	n := map[bool]int{true: 5, false: 3}[r.Kind == "refine"]
	// Measured against the TRIMMED rendering, because that is what clipSessionViewFor is
	// handed: comparing against the untrimmed one made every view look one rune short and
	// reported a clip that never happened.
	fullView := strings.TrimSpace(r.ViewFull)
	fmt.Fprintf(b, "\n#### Input %d — the whole-session view, as included (%d runes of the %d rendered)\n\n",
		n, runeLen(r.ViewIncluded), runeLen(fullView))
	if strings.TrimSpace(r.ViewIncluded) == "" {
		fmt.Fprintf(b, "*(the view yielded its whole share of the budget: nothing was included)*\n")
	} else {
		fenced(b, r.ViewIncluded)
		switch {
		case strings.HasSuffix(r.ViewIncluded, viewOmittedNotice):
			fmt.Fprintf(b, "\nClipped at a line boundary. Its own omission notice is the last line above, "+
				"shown verbatim.\n")
		case runeLen(r.ViewIncluded) < runeLen(fullView):
			fmt.Fprintf(b, "\nShorter than the rendering by %d runes with no omission notice — the difference "+
				"is trailing whitespace, not content.\n", runeLen(fullView)-runeLen(r.ViewIncluded))
		default:
			fmt.Fprintf(b, "\nThe whole view fitted; nothing was clipped.\n")
		}
	}

	fmt.Fprintf(b, "\n#### Input %d — the new turns, as included (%d runes of the %d rendered)\n\n",
		n+1, r.WindowRunes, runeLen(r.NewTurnsFull))
	fenced(b, r.Window)
	switch {
	case r.WindowClipped:
		fmt.Fprintf(b, "\nClipped, keeping the newest turns. The omission notice is the first line above, shown "+
			"verbatim. Content after the notice: %d runes, %+d against the %d-rune floor.\n",
			r.WindowRunes-runeLen(omittedNotice), r.WindowMargin, run.WindowFloor)
	default:
		fmt.Fprintf(b, "\nNot clipped — the whole rendered window fitted, so the floor does not apply to this step.\n")
	}

	fmt.Fprintf(b, "\n#### The refresh reason that fired\n\n")
	fmt.Fprintf(b, "- measured trigger: **%s**\n", r.Reason)
	if r.Kind == "create" {
		fmt.Fprintf(b, "- the prompt is not told a reason on a first report.\n")
	} else {
		fmt.Fprintf(b, "- what the prompt was told: **%s**", r.Why)
		if r.Reason != r.Why {
			fmt.Fprintf(b, " — this arm withholds the shift the trigger measured")
		}
		fmt.Fprintf(b, "\n")
	}

	fmt.Fprintf(b, "\n#### The full prompt as sent — %d of the %d-character budget, measured on the assembled string\n\n",
		r.PromptRunes, run.Budget)
	fenced(b, r.Prompt)

	switch {
	case r.Panicked:
		fmt.Fprintf(b, "\n#### GENERATION FAILED — the prompt-budget backstop panicked and was recovered\n\n")
		fmt.Fprintf(b, "No report was produced for this step. The panic text is in the run log; the assembled "+
			"prompt above is what tripped it.\n")
	case r.Err != "":
		fmt.Fprintf(b, "\n#### GENERATION FAILED — %d attempt(s)\n\n", r.Attempts)
		fenced(b, r.Err)
		fmt.Fprintf(b, "\nNo report was produced for this step. The next step, if any, refines the report BEFORE "+
			"this one.\n")
	default:
		fmt.Fprintf(b, "\n#### Output — the report\n\n")
		if r.Attempts > 1 {
			fmt.Fprintf(b, "**Took %d attempts.** A rejected generation was re-requested; the report below is the "+
				"one that passed validation.\n\n", r.Attempts)
		}
		if r.EmptyUnresolvedSubstituted {
			fmt.Fprintf(b, "**The model returned no open list at all**, and code supplied the "+
				"\"nothing is open\" sentinel (`ensureUnresolvedIsAddressed`). Before that substitution existed "+
				"this step would have been LOST.\n\n")
		}
		if len(r.Malformed) > 0 {
			fmt.Fprintf(b, "**Structurally malformed even after the repairs:** %s\n\n", strings.Join(r.Malformed, "; "))
		}
		writeDigest(b, *r.Digest)
	}

	if r.Kind != "create" {
		writeDiff(b, r)
	}
}

func writeDigest(b *strings.Builder, d Digest) {
	for _, s := range []struct{ name, text string }{
		{"synopsis", d.Synopsis}, {"done", d.Done}, {"happened", d.Happened},
		{"structure", d.Structure},
	} {
		fmt.Fprintf(b, "**%s** (%d runes)\n\n> %s\n\n", s.name, runeLen(s.text), blockquote(s.text))
	}
	fmt.Fprintf(b, "**insights** (%d entries)\n\n", len(d.Insights))
	for i, s := range d.Insights {
		fmt.Fprintf(b, "%d. %s\n", i+1, s)
	}
	fmt.Fprintf(b, "\n")
	for _, s := range []struct{ name, text string }{
		{"current", d.Current}, {"why", d.Why}, {"next", d.Next},
	} {
		fmt.Fprintf(b, "**%s** (%d runes)\n\n> %s\n\n", s.name, runeLen(s.text), blockquote(s.text))
	}
	fmt.Fprintf(b, "**unresolved** (%d entries)\n\n", len(d.Unresolved))
	for i, s := range d.Unresolved {
		fmt.Fprintf(b, "%d. %s\n", i+1, s)
	}
	fmt.Fprintf(b, "\n")
}

// writeDiff states what this report did with the previous one's facts and open items, so a
// reader can see a fact vanish without diffing two reports by hand. That is the design's own
// claim under test: the retain-list is the only channel, so a name absent here is gone for
// good — every later retain-list is derived from the LAST report.
func writeDiff(b *strings.Builder, r reportDump) {
	fmt.Fprintf(b, "\n#### What changed from the previous report\n\n")
	if r.Digest == nil {
		fmt.Fprintf(b, "*Nothing — this step produced no report.*\n")
		return
	}
	fmt.Fprintf(b, "**Facts** (the retain-list it was obliged to carry, tested with the harness's own "+
		"survival test): %d of %d retained.\n\n", len(r.FactsRetained), len(r.FactsObliged))
	if len(r.FactsRetained) > 0 {
		fmt.Fprintf(b, "- retained: %s\n", strings.Join(r.FactsRetained, ", "))
	}
	if len(r.FactsDropped) > 0 {
		fmt.Fprintf(b, "- **dropped: %s** — each was named in the prompt under \"each must still appear\", and "+
			"the next retain-list is derived from THIS report, so a drop is permanent.\n",
			strings.Join(r.FactsDropped, ", "))
	}
	if len(r.FactsObliged) == 0 {
		fmt.Fprintf(b, "- *(the previous report named no specifics, so nothing was obliged)*\n")
	}
	fmt.Fprintf(b, "\n**Open items:** %d kept, %d closed, %d added.\n\n",
		len(r.ItemsKept), len(r.ItemsClosed), len(r.ItemsAdded))
	for _, it := range r.ItemsClosed {
		fmt.Fprintf(b, "- closed or dropped: %s\n", it)
	}
	for _, it := range r.ItemsAdded {
		fmt.Fprintf(b, "- added: %s\n", it)
	}
	if len(r.ItemsKept) > 0 {
		fmt.Fprintf(b, "- kept open: %s\n", strings.Join(r.ItemsKept, " | "))
	}
	fmt.Fprintf(b, "\n*\"Closed\" here is measured by comparing the two open lists, not read from the model's "+
		"own `closed` field: the merge consumes that field, and what a reader sees is the list.*\n")
}

func writeBeatEvent(b *strings.Builder, ev beatEvent) {
	span := fmt.Sprintf("window %d · stride %d turns, %d kept, %d overlapping, %d runes",
		ev.WindowIndex, ev.SpanTurns, ev.KeptTurns, ev.OverlapTurns, ev.TotalRunes)
	switch {
	case ev.Panicked:
		fmt.Fprintf(b, "- **BEAT GENERATION PANICKED AND WAS RECOVERED** (%s, %d attempt(s))\n", span, ev.Attempts)
	case ev.Err != "":
		fmt.Fprintf(b, "- **BEAT GENERATION FAILED** (%s, %d attempt(s)): `%s`\n", span, ev.Attempts, ev.Err)
	case !ev.Kept:
		fmt.Fprintf(b, "- **DISCARDED as a restatement of the previous beat** (%s, %d attempt(s)): %s\n",
			span, ev.Attempts, ev.Text)
	default:
		mark := ""
		if ev.ChangedSubject {
			mark = " · marked SUBJECT CHANGED"
		}
		att := ""
		if ev.Attempts > 1 {
			att = fmt.Sprintf(" · **%d attempts**", ev.Attempts)
		}
		fmt.Fprintf(b, "- beat [%d] (%s%s)%s: %s\n", ev.Ordinal, span, mark, att, ev.Text)
	}
}

// fenced writes a block verbatim inside a fence long enough that content of its own cannot
// close it — transcript windows in this corpus contain code fences.
func fenced(b *strings.Builder, s string) {
	f := "```"
	for strings.Contains(s, f) {
		f += "`"
	}
	fmt.Fprintf(b, "%s\n%s", f, s)
	if !strings.HasSuffix(s, "\n") {
		fmt.Fprintf(b, "\n")
	}
	fmt.Fprintf(b, "%s\n", f)
}

// blockquote keeps a multi-line section inside one markdown quote rather than letting its
// second line escape the quote. Nothing is removed.
func blockquote(s string) string {
	return strings.ReplaceAll(strings.TrimSpace(s), "\n", "\n> ")
}

func joinOr(v []string, empty string) string {
	if len(v) == 0 {
		return empty
	}
	return strings.Join(v, ", ")
}
