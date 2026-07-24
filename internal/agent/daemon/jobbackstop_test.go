package daemon

import (
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

// wave1Passes+wave2 issue one inference each; a job makes this many passes.
const worstCasePasses = 8

// The job timeout is a WEDGE BACKSTOP, not the operating bound — passes are
// bounded individually. If it is shorter than the worst case (every pass
// burning its full deadline), the old failure mode returns: the job-wide
// timeout fires, discards every pass that already succeeded, and re-spools —
// the amplification loop that pinned the sidecar in permanent burst and drove
// the RAM oscillation. It must exceed passes x pass-deadline.
func TestJobTimeoutBackstopExceedsWorstCasePassBudget(t *testing.T) {
	worst := worstCasePasses * enrich.DefaultPassTimeout
	if got := jobTimeout(); got <= worst {
		t.Fatalf("jobTimeout() = %s, must exceed worst-case pass budget %s "+
			"(%d passes x %s) so it cannot pre-empt per-pass deadlines",
			got, worst, worstCasePasses, enrich.DefaultPassTimeout)
	}
}
