// Package checkpoints decides whether one step of a conformance chain worked.
//
// Five checkpoints, in the order the signal travels, one per lane:
//
//	transcript   the tool wrote a transcript under the root we expect
//	pointer      the daemon was told about the prompt and acted on it
//	store_rows   the sidecar ingested that session into the reference series
//	telemetry    the tool's OTLP reached Atlas through the loopback proxy
//	publish      a block or an enrichment reached Atlas
//
// The decision is a PURE function over already-gathered Facts (`Evaluate`), so
// every row is table-testable without a daemon; the gatherers that fill Facts
// from disk and loopback are separate and individually tested.
//
// ⚠️ **An unmet checkpoint that was not expected reads NOT-APPLICABLE, never
// OK.** Codex's store_rows and publish are `expected:false` until its reader and
// its capture land, and a harness that reported them green would be publishing a
// confident negative from a check nobody performed — the failure this codebase
// names in `facets_degraded` one layer down.
package checkpoints

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Names, in chain order. The runner prints them in this order too.
const (
	Transcript = "transcript"
	Pointer    = "pointer"
	StoreRows  = "store_rows"
	Telemetry  = "telemetry"
	Publish    = "publish"
	// ⚠️ **BLOCKS ARE A CHECKPOINT OF THEIR OWN, because `publish` could never
	// hold them to account.** That one passes on blocks OR enrichments, so
	// every run reported "0 block batch(es)" and passed — the signal Atlas
	// actually RENDERS went untested on every tool and every platform, which is
	// the exact shape of the outage this harness exists to prevent.
	Blocks = "blocks"
)

// Facts is everything the gatherers could read. A zero value means "nothing
// found", which is a failed checkpoint; a non-empty Errors entry means the
// gatherer could not look, which is a DIFFERENT failure and is reported as one.
type Facts struct {
	// Transcripts are root-relative paths, never absolute: a CI artifact would
	// otherwise carry the whole isolated HOME path.
	Transcripts []string `json:"transcripts"`
	// PromptIDs are the human-turn ids read out of those transcripts.
	PromptIDs []string `json:"prompt_ids"`

	StorePromptRows int `json:"store_prompt_rows"`
	StoreEventRows  int `json:"store_event_rows"`

	AtlasCounts       map[string]int `json:"atlas_counts"`
	EnrichmentCorrIDs []string       `json:"enrichment_corr_ids"`

	// Errors maps a checkpoint name to why its gatherer could not answer.
	Errors map[string]string `json:"errors,omitempty"`
}

// Expectations says which checkpoints this tool is required to meet at this
// point in the plan. Everything is required for Claude Code today.
type Expectations struct {
	Transcript, Pointer, StoreRows, Telemetry, Publish, Blocks bool
}

// AllRequired is the Claude Code expectation: all six.
func AllRequired() Expectations {
	return Expectations{true, true, true, true, true, true}
}

// Result is one checkpoint's verdict.
type Result struct {
	Name     string `json:"name"`
	OK       bool   `json:"ok"`
	Required bool   `json:"required"`
	// Source names WHERE the fact came from, so a reader can tell a real signal
	// from an interim proxy for one. See the seam note on `pointer`.
	Source string `json:"source"`
	Detail string `json:"detail"`
	Err    string `json:"error,omitempty"`
}

// Report is what `keld-conform check` prints.
type Report struct {
	Tool        string   `json:"tool"`
	Chain       string   `json:"chain"`
	Step        string   `json:"step"`
	Seed        string   `json:"seed"`
	Pass        bool     `json:"pass"`
	Checkpoints []Result `json:"checkpoints"`
	Facts       *Facts   `json:"facts,omitempty"`
}

// Evaluate decides all five checkpoints from gathered facts.
func Evaluate(f Facts, e Expectations) []Result {
	logs := f.AtlasCounts["/v1/logs"] + f.AtlasCounts["/v1/metrics"]
	blocks := f.AtlasCounts["/v1/signal/blocks"]
	enrich := f.AtlasCounts["/v1/enrichments"]

	return []Result{
		f.result(Transcript, e.Transcript, "transcript-root", len(f.Transcripts) > 0,
			fmt.Sprintf("%d transcript(s) under the expected root: %s",
				len(f.Transcripts), strings.Join(f.Transcripts, ", "))),

		// ⚠️ SEAM. "The daemon received a pointer" has NO observable outside the
		// daemon's own queue today: there is no /metrics route on the daemon
		// (the plan assumed one) and the only pointer log lines are failures. So
		// this is measured one hop downstream — an enrichment at the mock Atlas
		// whose corr_id is a prompt id from THIS run's transcript, which the
		// daemon can only produce from a pointer it was given. Replace the body
		// of this fact with /v1/integrations' hook/watcher `last_seen` when
		// WS-C1 lands; the checkpoint's meaning does not change, only its
		// source, which is why Source is reported.
		f.result(Pointer, e.Pointer, "mockatlas:enrichments(corr_id)", f.matchedPointer(),
			fmt.Sprintf("%d enrichment corr_id(s) against %d prompt id(s) in the transcript",
				len(f.EnrichmentCorrIDs), len(f.PromptIDs))),

		f.result(StoreRows, e.StoreRows, "refseries.db",
			f.StorePromptRows > 0 && f.StoreEventRows > 0,
			fmt.Sprintf("%d prompt row(s), %d event row(s) for this session",
				f.StorePromptRows, f.StoreEventRows)),

		f.result(Telemetry, e.Telemetry, "mockatlas:counts", logs > 0,
			fmt.Sprintf("%d OTLP request(s) forwarded (/v1/logs + /v1/metrics)", logs)),

		// ⚠️ ENRICHMENTS ONLY. This used to accept blocks OR enrichments, which
		// made `publish` pass on either and left NEITHER separately required —
		// so "0 block batch(es)" rode along green on every run ever recorded.
		// One fact per checkpoint is what lets a failure name its own cause,
		// and TestEachMissingFactFailsExactlyItsOwnCheckpoint pins it.
		f.result(Publish, e.Publish, "mockatlas:counts", enrich > 0,
			fmt.Sprintf("%d enrichment(s)", enrich)),

		// ⚠️ A BLOCK IS WHAT ATLAS RENDERS, and it is cut from the sidecar's
		// store — so this can only pass for a source the store can read, and
		// only once the run produces a CLOSED block. The shipped cutter closes
		// on 20 minutes or 15 of silence, which a seconds-long chain never
		// reaches; the harness sets KELD_DEV_BLOCKS=prompt so one human prompt
		// closes one block. That granularity is refused against a real Atlas
		// and admissible here only because this run publishes to a loopback
		// mock.
		f.result(Blocks, e.Blocks, "mockatlas:counts", blocks > 0,
			fmt.Sprintf("%d block batch(es)", blocks)),
	}
}

// matchedPointer is true when an enrichment corr_id equals a prompt id read
// from this run's transcript — not merely that some enrichment arrived, which a
// leftover from an earlier step would also satisfy.
func (f Facts) matchedPointer() bool {
	if len(f.PromptIDs) == 0 || len(f.EnrichmentCorrIDs) == 0 {
		return false
	}
	want := make(map[string]bool, len(f.PromptIDs))
	for _, p := range f.PromptIDs {
		want[p] = true
	}
	for _, c := range f.EnrichmentCorrIDs {
		if want[c] {
			return true
		}
	}
	return false
}

// result assembles one verdict. ⚠️ A gatherer error FORCES ok to false: "could
// not look" and "looked and found nothing" are different facts, and only the
// first has an Err to say so — but neither may report OK.
func (f Facts) result(name string, required bool, source string, ok bool, detail string) Result {
	e := f.Errors[name]
	if e != "" {
		ok = false
	}
	return Result{Name: name, OK: ok, Required: required, Source: source, Detail: detail, Err: e}
}

// Pass is true when every REQUIRED checkpoint is OK.
func Pass(rs []Result) bool {
	for _, r := range rs {
		if r.Required && !r.OK {
			return false
		}
	}
	return true
}

// FailureLine is the one-line JSON a runner reads with `tail -1`: the five
// fields naming the FIRST failed required checkpoint. Empty on a pass.
func (r Report) FailureLine() string {
	for _, c := range r.Checkpoints {
		if c.Required && !c.OK {
			raw, _ := json.Marshal(map[string]string{
				"tool": r.Tool, "chain": r.Chain, "step": r.Step,
				"checkpoint": c.Name, "seed": r.Seed,
			})
			return string(raw)
		}
	}
	return ""
}

// Summary renders the report as the lines a person reads in a terminal or in a
// GitHub step summary.
func (r Report) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "conformance: %s chain %s step %s (seed %s)\n", r.Tool, r.Chain, r.Step, r.Seed)
	for _, c := range r.Checkpoints {
		mark := "FAIL"
		switch {
		case c.OK:
			mark = "PASS"
		case !c.Required:
			mark = "n/a "
		}
		fmt.Fprintf(&b, "  [%s] %-11s %s\n", mark, c.Name, c.Detail)
		if c.Err != "" {
			fmt.Fprintf(&b, "        error: %s\n", c.Err)
		}
	}
	return b.String()
}
