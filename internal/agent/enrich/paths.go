package enrich

// PathCount is one entry of a window's FILE-PATH inventories — `files`,
// `directories` and `components` (see Profile.Files/Directories/Components) —
// which all share this shape.
//
// It is a COUNT, not a share, for the same reason Act is: these are inventories
// (what was touched), not allocations (what owns the window), so there is no
// denominator to divide by — an hour that edits three files across two
// directories is all of that at once.
//
// UNLIKE Act, the vocabulary here is OPEN: a file path is not a member of a
// closed table, so there is no KnownX gate at the decode boundary. What stands
// in its place is a STRUCTURAL invariant instead of a vocabulary one: every
// value reaching this type has already been resolved workspace-relative by the
// sidecar's `reconcile()` — verified over all 500 real corpus transcripts plus
// John's (zero absolute paths, zero `~`/`/Users`/`/home` paths, zero `../`
// escapes, zero URLs, zero Windows drive paths) — and
// sidecar.convertPathInventory drops, per entry, anything that does not look
// workspace-relative. That is defence in depth against a sidecar that ever
// stopped honouring the invariant, mirroring the per-entry drop convertActs
// already applies to Act for a different reason (vocabulary membership rather
// than shape).
type PathCount struct {
	// Value is a workspace-relative path: a file (`files`), a directory
	// (`directories`), or a directory prefix truncated to
	// app.analysis.COMPONENT_DEPTH (`components`). Never absolute, never
	// `~`-relative, never a drive letter, never a `../` escape. Never empty: a
	// count attached to no path is a count attached to nothing.
	Value string `json:"value"`
	// N is how many times the window referenced it. Always stated.
	N int `json:"n"`
}
