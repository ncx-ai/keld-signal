package review

// Mutations is the calibration set: two planted defects per class, cut from ten different
// real statements across all three sessions.
//
// Two per class is the minimum that can distinguish "this class is never caught" from one
// reviewer having an off item, and a class that comes back 0 of its planted total is reported
// as a named blind spot rather than averaged away. They span the domains deliberately — three
// of the ten are the accounting session, which is 4 of the 30 genuine items — because a
// reviewer who only reads code is the failure mode the audience requirement cares about, and
// it is invisible in a corpus-weighted sample.
//
// Every property claimed here is verified by Apply against the item's real evidence, so a
// wrong entry fails emission instead of silently becoming an item no reviewer could ever get
// right. In particular the Absent tokens are checked against the window AND the record.
var Mutations = []Mutation{
	// ---- fabricated identifier: a real symbol swapped for a plausible neighbour ----
	{
		ID: "m01", Class: FabricatedIdentifier,
		Session: "Real engineering session", Ordinal: 2,
		Original:    "`turn-row.tsx`",
		Replacement: "`activity-row.tsx`",
		Absent:      []string{"activity-row"},
		Note:        "The file edited in the window is turn-row.tsx; activity-row.tsx does not exist in the evidence. Plausible because the window talks about the activity table throughout.",
	},
	{
		ID: "m02", Class: FabricatedIdentifier,
		Session: "Month-end close (hand-authored)", Ordinal: 4,
		Original:    "fa-register.csv",
		Replacement: "fixed-assets-mar.csv",
		Absent:      []string{"fixed-assets-mar"},
		Note:        "The register the user names is fa-register.csv. The substitute follows the session's own mar-suffixed naming convention, so it is wrong without looking wrong.",
	},

	// ---- invented blocker: an obstacle the evidence does not support ----
	{
		ID: "m03", Class: InventedBlocker,
		Session: "Month-end close (hand-authored)", Ordinal: 3,
		Original:    "The review has identified the outstanding amounts and is now preparing the adjustment entries",
		Replacement: "The Halberd accrual cannot be posted until the vendor confirms a disputed balance, which is holding up the adjustment entries",
		Absent:      []string{"disputed"},
		Note:        "Nothing in the window is disputed or waiting on a vendor; the user simply asks for the AP accruals next. An accrual blocked on a vendor is an ordinary accounting obstacle, which is what makes it credible.",
	},
	{
		ID: "m04", Class: InventedBlocker,
		Session: "This session — building the digest", Ordinal: 7,
		Original:    "It's at 5 of 20 digests completed, taking about 146 seconds per digest with memory stable at 2,952 MB.",
		Replacement: "It's at 5 of 20 digests completed at about 146 seconds each, and is now stalled waiting on the store, with memory stable at 2,952 MB.",
		Absent:      []string{"stalled"},
		Note:        "The window shows a sweep running normally. A stall waiting on the store is invented, and it is the shape of blocker this harness really does hit, so it reads true.",
	},

	// ---- unobservable completion: progress the window cannot show ----
	{
		ID: "m05", Class: UnobservableCompletion,
		Session: "This session — building the digest", Ordinal: 13,
		Original:    "The design is grounded in measurement showing that 18 of every 30 turns were previously invisible to the beat system.",
		Replacement: "Only the stride constant is left to settle, and the geometry work is otherwise nearly done.",
		Note:        "Trades a measured claim for a claim about the whole job. The window is one slice of the session and cannot show how much of the geometry work is left — the beat prompt forbids exactly these phrasings.",
	},
	{
		ID: "m06", Class: UnobservableCompletion,
		Session: "Real engineering session", Ordinal: 9,
		Original:    "All changes are live on dev stack and pass tests.",
		Replacement: "All changes are live on dev stack and pass tests, which leaves the layout work essentially finished with only tidying up to do.",
		Note:        "The live-and-passing half is supported; the appended claim about the layout work as a whole is not, and introduces no new specific, so it can only be caught as a judgement.",
	},

	// ---- subject drift: the right kind of work, the wrong subject, taken from another session ----
	{
		ID: "m07", Class: SubjectDrift,
		Session: "Real engineering session", Ordinal: 3,
		Original:    "the enrichment pills and compliance flag",
		Replacement: "the Meridian close checklist rows",
		Absent:      []string{"Meridian"},
		DrawnFrom:   "Month-end close (hand-authored)",
		Note:        "Styling work is real in this window; the thing being styled belongs to the accounting session. Cross-domain drift, which is the drift a domain-blind reviewer misses.",
	},
	{
		ID: "m08", Class: SubjectDrift,
		Session: "This session — building the digest", Ordinal: 11,
		Original:    "the signal classification task structure",
		Replacement: "the ConfirmDialog trigger structure",
		Absent:      []string{"ConfirmDialog"},
		DrawnFrom:   "Real engineering session",
		Note:        "Attributes the restructuring work to a component from the other engineering session. Same domain, adjacent register — the harder of the two drift items.",
	},

	// ---- sourceless specificity: a number or proper noun from nowhere ----
	{
		ID: "m09", Class: SourcelessSpecificity,
		Session: "Month-end close (hand-authored)", Ordinal: 4,
		Original:    "The adjustment has been calculated and is now ready for posting",
		Replacement: "The adjustment comes to 4,318.00 and is now ready for posting",
		Absent:      []string{"4,318"},
		Note:        "The session is dense with real amounts, none of which is this one. A reviewer has to check the number against the evidence rather than accept it because it looks like the others.",
	},
	{
		ID: "m10", Class: SourcelessSpecificity,
		Session: "This session — building the digest", Ordinal: 3,
		Original:    "each window takes 40–60 seconds",
		Replacement: "each window takes 40–60 seconds on the Ardent bench host",
		Absent:      []string{"Ardent"},
		Note:        "A proper noun with no source at all. Named rather than numeric, because a reviewer scanning for unsupported digits will not necessarily scan for unsupported names.",
	},
}

// CleanDuplicates names the genuine items that are emitted a SECOND time under a fresh
// packet id, byte-identical.
//
// Without them the round measures sensitivity and reports it as accuracy: a reviewer who
// flags something in every item catches every planted defect and looks perfect. Six of them
// against ten planted items, chosen to span both domains and the corpus's length range (the
// shortest genuine statement is 87 runes and the longest 503) so a false positive cannot be
// blamed on the sample being uniform.
var CleanDuplicates = []struct {
	Session string
	Ordinal int
}{
	{"This session — building the digest", 1}, // 192 runes, single subject
	{"This session — building the digest", 6}, // 435 runes, dense and hedged
	{"Month-end close (hand-authored)", 2},    // 419 runes, accounting, many real amounts
	{"Month-end close (hand-authored)", 4},    // 171 runes, accounting, short
	{"Real engineering session", 1},           // 87 runes, the shortest in the corpus
	{"Real engineering session", 12},          // 136 runes, says the work is NOT complete
}
