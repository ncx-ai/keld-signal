//go:build llmstudy

// Live digest harness. Requires a llama-server.
//
//	DIGEST_URL=http://127.0.0.1:8095 go test -tags llmstudy \
//	  ./internal/agent/enrich/llmstudy/ -run DigestSizing -v -timeout 60m
package llmstudy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich/llmstudy/digeststore"
)

// projectFromPath recovers a readable project name from a Claude Code transcript
// path, whose parent directory encodes the working directory (e.g.
// "-home-dg-keld-keld-signal"). Gives the digest a real "working in" anchor without
// inventing one.
func projectFromPath(p string) string {
	d := filepath.Base(filepath.Dir(p))
	d = strings.TrimPrefix(d, "-")
	parts := strings.Split(d, "-")
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}

// TestDigestSizing is verification test 6: can a budget-fitting model write a usable
// digest at all? Free generation is the capability class where Qwen3-0.6B collapsed
// to a single value, so this must be answered before any prompt tuning — tuning
// against the wrong model is wasted work.
func TestDigestSizing(t *testing.T) {
	url := os.Getenv("DIGEST_URL")
	if url == "" {
		url = "http://127.0.0.1:8095"
	}
	// Spread across projects rather than taking the first N in path order, which drew all
	// 14 sessions from keld-atlas and reported one project's results as a corpus.
	files := StratifiedTranscripts()
	if len(files) == 0 {
		t.Skip("no transcripts")
	}

	o := DefaultMineOpts()
	o.K = 12 // wider than classification uses: a digest needs more material
	l := NewLlama(url)

	tried, ok := 0, 0
	for _, f := range files {
		if tried >= 4 {
			break
		}
		ws, err := Mine(f, o)
		if err != nil || len(ws) < 8 {
			continue
		}
		ocs, err := Outcomes(f, o)
		if err != nil || len(ocs) != len(ws) {
			continue
		}
		idx := len(ws) - 1
		w := ws[idx]
		facts := FactsFrom(Extract(w), ocs[:idx+1]).
			WithWindow(w).
			WithPlace("", "", projectFromPath(f))
		tried++

		d, err := l.CreateDigestWithView("work session", Render(w), RenderSessionView(w), facts.Block())
		if err != nil {
			t.Errorf("[%d] call failed: %v", tried, err)
			continue
		}
		problems := ValidateDigest(d)
		if len(problems) > 0 {
			t.Errorf("[%d] malformed: %v", tried, problems)
			continue
		}
		ok++
		t.Logf("═══ digest %d — %s ═══", tried, filepath.Base(f))
		t.Logf("  FACTS GIVEN: %s", strings.ReplaceAll(strings.TrimSpace(facts.Block()), "\n", " | "))
		t.Logf("  done:       %s", clipLog(d.Done))
		t.Logf("  happened:   %s", clipLog(d.Happened))
		t.Logf("  structure:  %s", clipLog(d.Structure))
		t.Logf("  current:    %s", clipLog(d.Current))
		t.Logf("  why:        %s", clipLog(d.Why))
		t.Logf("  next:       %s", clipLog(d.Next))
		for i, s := range d.Insights {
			t.Logf("  insight[%d]: %s", i, clipLog(s))
		}
		for i, s := range d.Unresolved {
			t.Logf("  unresolved[%d]: %s", i, clipLog(s))
		}
		// Thresholds 2 and 7, per digest.
		if LooksFabricatedUnresolved(d, facts, Render(w)) {
			t.Logf("  ⚠ FABRICATED unresolved (threshold 7)")
		}
		if UsesUnresolvedSentinel(d) {
			t.Logf("  ✓ used the sentinel (nothing open)")
		}
		if leak := LeakedPromptWords(d, Render(w)); len(leak) > 0 {
			t.Logf("  ⚠ PROMPT LEAK: %v (instruction vocabulary absent from the session)", leak)
		}
		if bad := UnverifiedIdentifiers(d, Render(w)); len(bad) > 0 {
			t.Logf("  ⚠ unverified specifics: %v", bad)
		}
	}
	t.Logf("structural validity: %d/%d", ok, tried)
	if tried > 0 && ok != tried {
		t.Errorf("threshold 1 requires 100%% structural validity, got %d/%d", ok, tried)
	}
}

func clipLog(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 260 {
		return s[:260] + "…"
	}
	return s
}

// TestDigestRefineQuality measures thresholds 1-4 and 7 over real sessions, running
// the actual refine loop and persisting snapshots, so the numbers describe the
// shipping path rather than a one-shot call.
func TestDigestRefineQuality(t *testing.T) {
	url := os.Getenv("DIGEST_URL")
	if url == "" {
		url = "http://127.0.0.1:8099"
	}
	// Spread across projects rather than taking the first N in path order, which drew all
	// 14 sessions from keld-atlas and reported one project's results as a corpus. This
	// session's own transcript sat at index 51 of 59 and was never sampled.
	files := StratifiedTranscripts()
	if me := ThisSessionTranscript(); me != "" {
		files = append([]string{me}, files...)
	}

	store, err := digeststore.Open(filepath.Join(t.TempDir(), "digest.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	o := DefaultMineOpts()
	o.K = 12
	l := NewLlama(url)

	var (
		attempted, failed        int
		stale, openItems         int
		completedCurrent         int
		restated                 int
		lagging, lagDen          int
		shifts                   int
		digests, malformed       int
		ids, unverified, leaks   int
		withCorrections, stamped int
		cleanRuns, fabricated    int
		retNum, retDen           int
		sentinelUsed             int
	)
	sessions := 0
	for _, f := range files {
		if sessions >= sessionBudget() {
			break
		}
		ws, e1 := Mine(f, o)
		ocs, e2 := Outcomes(f, o)
		if e1 != nil || e2 != nil || len(ws) < 16 || len(ws) != len(ocs) {
			continue
		}
		sessions++
		sid := filepath.Base(f)

		var cur Digest
		var injected []string
		var firstSrc, prevSrc string
		// The verification reference must be every window consumed so far. Scoring a
		// refined digest against only the newest window counts correct carry-forward
		// as fabrication — which is what an earlier run of this harness did, reporting
		// 53.8% unverified identifiers against a true rate of 1-2 per digest.
		var seenSrc strings.Builder
		for step, idx := range []int{4, 8, 12, 15} {
			w := ws[idx]
			facts := FactsFrom(Extract(w), ocs[:idx+1]).WithWindow(w).
				WithPlace("", "", projectFromPath(f))
			src := Render(w)
			seenSrc.WriteString(src)
			seenSrc.WriteString("\n")
			// The reference must contain EVERYTHING the model was shown, including the
			// coarse whole-session view. Omitting it scored correct carry-through from that
			// view as fabrication and took unverified identifiers from 0.6% to 8.5% — the
			// same defect as scoring a refined digest against only the newest window.
			seenSrc.WriteString(RenderSessionView(w))
			seenSrc.WriteString("\n")
			cumulative := seenSrc.String()

			if step == 0 {
				firstSrc = src
			}
			var d Digest
			var err error
			if step == 0 {
				d, err = l.CreateDigestWithView("work session", src, RenderSessionView(w), facts.Block())
			} else {
				// Stand-in for the production focus-shift trigger, which needs the EWMA
				// focus this harness does not compute. Lets the GATED configuration be
				// measured instead of merely argued for.
				reason := TriggerVolume
				if SubjectShifted(prevSrc, src) {
					reason = TriggerFocusShift
					shifts++
				}
				d, err = l.RefineDigestWithReason(cur, "work session", src, RenderSessionView(w),
					facts.Block(), reason)
			}
			attempted++
			if err != nil {
				failed++
				t.Logf("session %d step %d: %v", sessions, step, err)
				continue
			}
			digests++
			if p := ValidateDigest(d); len(p) > 0 {
				malformed++
				t.Logf("session %d step %d malformed: %v", sessions, step, p)
			}
			ids += len(Identifiers(d))
			// Log the flagged items, not just the count. Both of these gates have
			// reported large numbers that turned out to be ordinary English, and a bare
			// count gives no way to tell a real defect from a measurement artifact.
			if bad := UnverifiedIdentifiers(d, cumulative); len(bad) > 0 {
				unverified += len(bad)
				t.Logf("  UNVERIFIED s%d step%d: %v", sessions, step, bad)
			}
			if lk := LeakedPromptWords(d, cumulative); len(lk) > 0 {
				leaks += len(lk)
				t.Logf("  LEAK s%d step%d: %q", sessions, step, lk)
			}
			if UsesUnresolvedSentinel(d) {
				sentinelUsed++
			}
			// T8: an open item the report itself contradicts. T7 only catches a blocker
			// with no basis at all and scores a stale one as passing, though a reader
			// acting on it wastes the same effort.
			if st := StaleUnresolved(d); len(st) > 0 {
				stale += len(st)
				t.Logf("  STALE s%d step%d: %v", sessions, step, st)
			}
			openItems += len(d.Unresolved)
			// T10: a synthesis section can satisfy its instruction by copying the purpose
			// sentence out of `why`, which would add nothing for the reader it exists for.
			if step > 0 {
				lagDen++
				if rh, eh, lag := SynopsisLag(d, firstSrc, src); lag {
					lagging++
					t.Logf("  SYNOPSIS-LAGS s%d step%d (recent=%d early=%d): %s",
						sessions, step, rh, eh, clipLog(d.Synopsis))
				}
			}
			if SynopsisRestatesAnotherSection(d) {
				restated++
				t.Logf("  SYNOPSIS-RESTATES s%d step%d: %s", sessions, step, clipLog(d.Synopsis))
			}
			if CurrentDescribesCompletion(d) {
				completedCurrent++
				t.Logf("  CURRENT-IS-DONE s%d step%d: %s", sessions, step, clipLog(d.Current))
			}
			if facts.Corrections > 0 || facts.CorrectedTurns > 0 {
				withCorrections++
				if LooksRubberstamped(d, facts) {
					stamped++
					t.Logf("  RUBBERSTAMP s%d step%d (corr=%d): %s",
						sessions, step, facts.Corrections, clipLog(d.Happened))
				}
			} else {
				cleanRuns++
				if LooksFabricatedUnresolved(d, facts, cumulative) {
					fabricated++
					t.Logf("  FABRICATED s%d step%d: %v", sessions, step, d.Unresolved)
				}
			}

			body, _ := json.Marshal(d)
			sig, _ := json.Marshal(facts)
			if err := store.Put(digeststore.Record{
				SessionID: sid, Seq: step + 1, CreatedTS: int64(step),
				SchemaVersion: DigestSchemaVersion, Model: "qwen3-4b",
				Trigger: string(TriggerVolume), FromPromptID: w.PromptID,
				ToPromptID: w.PromptID, Turns: facts.Turns,
				Signals: string(sig), Body: string(body),
			}); err != nil {
				t.Errorf("store: %v", err)
			}

			if step == 0 {
				injected = Identifiers(d)
				if len(injected) > 6 {
					injected = injected[:6]
				}
			}
			cur = d
			prevSrc = src
		}
		if len(injected) > 0 {
			retNum += RetainedFacts(cur, injected)
			retDen += len(injected)
		}
		// The store must actually hold the trail the drift metric replays.
		if h, err := store.History(sid); err != nil || len(h) == 0 {
			t.Errorf("history missing for %s: %v", sid, err)
		}
	}

	pct := func(n, d int) float64 {
		if d == 0 {
			return 0
		}
		return float64(n) / float64(d) * 100
	}
	t.Logf("sessions=%d attempted=%d produced=%d failed=%d", sessions, attempted, digests, failed)
	// T1 counts SUCCEEDED/ATTEMPTED, not valid/produced. An earlier version measured
	// the latter and reported 100%% while silently dropping 5 of 20 attempts to
	// truncated JSON — a dropped digest is worse than a malformed one, not exempt.
	t.Logf("T1 usable digests        %.1f%% of %d attempts  (want 100%%)", pct(digests-malformed, attempted), attempted)
	t.Logf("T2 unverified identifiers %.1f%% of %d  (want <=2%%)", pct(unverified, ids), ids)
	t.Logf("T3 rubberstamped          %.1f%% of %d correction-bearing  (want <=10%%)", pct(stamped, withCorrections), withCorrections)
	t.Logf("T4 retention to final     %.1f%% of %d  (want >=90%%)", pct(retNum, retDen), retDen)
	t.Logf("T7 fabricated unresolved  %.1f%% of %d clean runs  (want <=10%%)", pct(fabricated, cleanRuns), cleanRuns)
	t.Logf("T8 stale open items       %.1f%% of %d open items  (want <=2%%)", pct(stale, openItems), openItems)
	t.Logf("T9 current-is-completed   %.1f%% of %d  (want <=5%%)", pct(completedCurrent, digests), digests)
	t.Logf("T10 synopsis restates      %.1f%% of %d  (want <=5%%)", pct(restated, digests), digests)
	t.Logf("T11 synopsis lags          %.1f%% of %d refinements  (want <=10%%)", pct(lagging, lagDen), lagDen)
	t.Logf("    anchor fired on %d of %d refinements (measured subject shift)", shifts, lagDen)
	t.Logf("   prompt leaks %d; sentinel used %d/%d", leaks, sentinelUsed, digests)
}

// sessionBudget sets how many sessions the sweep covers. Default 5 is a fast
// iteration loop; the reportable number needs more, because T4's denominator is
// 6 injected facts per session and at 30 a three-fact swing spans most of the
// plausible range — three configs scored 100%, 96.7% and 86.7% with overlapping
// Wilson intervals, i.e. indistinguishable.
func sessionBudget() int {
	if v := os.Getenv("DIGEST_SESSIONS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 5
}
