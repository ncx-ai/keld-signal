package review

// SeriesMutations is the series-level calibration set: two planted defects per class, cut from the
// three real timelines the corpus contains.
//
// Two per class is the same minimum r1 used, and for the same reason — it distinguishes "this
// class is never caught" from one reviewer having an off item, and a class that comes back 0 of
// its planted total is reported as a named blind spot rather than averaged away. Every property
// claimed here is verified by ApplySeries against the real timeline and the real record, so a
// wrong entry fails emission instead of becoming an item no reviewer could ever get right.
//
// THE SEVERE LIMIT, stated where the set is defined rather than in a footnote: there are only
// THREE real series. Ten plants over three timelines means each timeline is mutated three or four
// times, so this set cannot separate "the reader catches this class" from "the reader reads this
// particular session well". The accounting session carries three of the ten — over-represented on
// purpose, exactly as in r1, because a reader who can only follow software work is the failure
// mode the audience requirement cares about and it is invisible in a corpus-weighted sample.
var SeriesMutations = []SeriesMutation{
	// ---- order shuffle: chronology wrong, every beat individually true ----
	{
		ID: "s01", Class: OrderShuffle,
		Session: "This session — building the digest",
		Order:   []int{1, 2, 3, 4, 9, 10, 11, 12, 5, 6, 7, 8, 13, 14},
		Note: "A block swap: the beats about the recency check, the rollup design and fixing false " +
			"completion claims are shown BEFORE the beats where the digest's prose gate is still an " +
			"open question. The junction is the only place it shows — beat 8 fixes statements in a " +
			"timeline that beat 9 then treats as an undecided design.",
	},
	{
		ID: "s02", Class: OrderShuffle,
		Session: "Month-end close (hand-authored)",
		Order:   []int{1, 3, 2, 4},
		Note: "The AP-accrual review is shown before the reconciliation it says is already integrated " +
			"(\"with the data from the bank and AR aging already integrated\"), so the series has the " +
			"work depending on a step that has not happened yet. A minimal adjacent swap in the " +
			"shortest series, which is the hardest place to see one.",
	},

	// ---- cross-session contamination: a beat from someone else's timeline ----
	{
		ID: "s03", Class: CrossSessionContamination,
		Session:      "Month-end close (hand-authored)",
		DonorSession: "This session — building the digest",
		DonorOrdinal: 7, InsertAt: 3,
		Foreign: []string{"digests", "2,952", "threshold sweep"},
		Note: "A software beat about a model sweep spliced into the middle of a month-end close. " +
			"Cross-domain, which is the contamination a reader who only tracks 'work happened' will " +
			"wave through.",
	},
	{
		ID: "s04", Class: CrossSessionContamination,
		Session:      "Real engineering session",
		DonorSession: "Month-end close (hand-authored)",
		DonorOrdinal: 4, InsertAt: 7,
		Foreign: []string{"Depreciation", "fa-register.csv", "adjusting journal"},
		Note: "The reverse direction: a depreciation beat spliced into a web-UI session. Both " +
			"directions are planted because a reader biased towards code may accept the code beat in " +
			"the accounting series and reject the accounting beat in the code series for the wrong " +
			"reason.",
	},

	// ---- entity swap: a real name renamed throughout to a plausible absent one ----
	{
		ID: "s05", Class: EntitySwap,
		Session: "Real engineering session",
		Pairs:   []SwapPair{{From: "ConfirmDialog", To: "ConfirmSheet"}},
		Note: "The component fixed in one beat and reused in a later one is renamed in both, so the " +
			"series stays internally consistent and only the measured record disagrees — the record " +
			"counts ConfirmDialog. Internal consistency is what makes a swap invisible per beat.",
	},
	{
		ID: "s06", Class: EntitySwap,
		Session: "This session — building the digest",
		Pairs:   []SwapPair{{From: "GLiNER2", To: "SpanLite"}, {From: "Gliner2", To: "SpanLite"}},
		Note: "The product the whole study is measuring against is renamed in all three beats that " +
			"name it, including the document's own casing variant. The record counts GLiNER2 and " +
			"gliner2; nothing in the corpus contains SpanLite.",
	},

	// ---- dropped middle: the turn is missing, the jump is not explained ----
	{
		ID: "s07", Class: DroppedMiddle,
		Session: "This session — building the digest",
		Remove:  []int{9, 10},
		Note: "Beat 10 is the session's own marked subject change — the turn from the digest to the " +
			"session-level rollup. Removing 9 and 10 puts 'fixing three defects in the digest' " +
			"directly before a beat about beats reading each other's output, a subject the series " +
			"never introduces.",
	},
	{
		ID: "s08", Class: DroppedMiddle,
		Session: "Real engineering session",
		Remove:  []int{4, 5},
		Note: "Removes the pipeline-debugging beat and the KPI-card beat, both marked subject " +
			"changes. The series then goes from adjusting pill styling straight to a spend-card " +
			"toggle that depends on a budget and a run-rate baseline the timeline never mentions.",
	},

	// ---- invented arc: a conclusion the series never reached ----
	{
		ID: "s09", Class: InventedArc,
		Session: "This session — building the digest",
		Replacement: "The gate, baseline, delimiter rule, and stride geometry for the signal " +
			"classification system are all implemented, measured and complete, and the study is " +
			"closed out: the no-regression sweeps came back clean, the encoder is dropped in " +
			"favour of the local model, and the work is shipped.",
		Note: "The real final beat says the measurement phase is still running. The replacement " +
			"asserts a finished study with a decision taken. It is a plausible LAST beat read " +
			"alone, which is exactly why this class needs the series to catch it.",
	},
	{
		ID: "s10", Class: InventedArc,
		Session: "Month-end close (hand-authored)",
		Replacement: "With depreciation posted, the March close for the Meridian entity is complete " +
			"and the ledger is locked for the period, with nothing left outstanding.",
		Note: "The real final beat has the depreciation adjustment ready for posting, not posted, " +
			"and the record counts five corrections. A locked ledger is the ordinary end of a close, " +
			"so it reads as the obvious next sentence rather than as an invention.",
	},
}

// SeriesCleanDuplicates names the clean timelines emitted a SECOND time under a fresh packet id,
// byte-identical apart from that id.
//
// Without them the round measures sensitivity and reports it as accuracy: a reader who reports a
// break in every series catches every plant and looks perfect. r1 made that measurable — its
// duplicates came back 0 of 6 inconsistent, which is the bar here. All three series are duplicated
// because there are only three, so every one of them gets the strict self-consistency test rather
// than a sample of them.
var SeriesCleanDuplicates = []string{
	"This session — building the digest",
	"Month-end close (hand-authored)",
	"Real engineering session",
}
