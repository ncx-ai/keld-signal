package usage

import (
	"os"
	"strings"
)

// EnabledFromEnv reports whether the per-request count runs: the live recorder
// and the one-time backfill. On by default; KELD_USAGE in {off,0,false}
// (case-insensitive) switches both off, leaving the Atlas mirror untouched and
// the rows already in the table where they are. The same shape as
// watch.EnabledFromEnv.
func EnabledFromEnv() bool {
	switch strings.ToLower(os.Getenv("KELD_USAGE")) {
	case "off", "0", "false":
		return false
	default:
		return true
	}
}
