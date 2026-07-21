package promptlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Identity is the Anthropic account identity the CLI's OTEL attributes telemetry
// to. For Cowork it is recovered host-side from the session directory layout and
// metadata so watched telemetry attributes to the same account as the CLI's
// native telemetry (not keld's login). user.id / user.account_id are not present
// in Cowork's local metadata and are therefore left empty.
type Identity struct {
	Email       string // user.email
	AccountUUID string // user.account_uuid
	OrgID       string // organization.id
}

// coworkIdentity extracts identity for a Cowork transcript path of the form
//
//	…/local-agent-mode-sessions/<accountUUID>/<orgUUID>/local_<id>/.claude/projects/…
//
// The account/org UUIDs are the two path segments under local-agent-mode-sessions;
// the email is read from the session's <accountUUID>/<orgUUID>/local_<id>.json
// (emailAddress). Returns a zero Identity if the path doesn't match or metadata is
// unreadable.
func coworkIdentity(transcriptPath string) Identity {
	const marker = "local-agent-mode-sessions"
	i := strings.Index(transcriptPath, marker)
	if i < 0 {
		return Identity{}
	}
	rest := strings.TrimPrefix(transcriptPath[i+len(marker):], string(filepath.Separator))
	segs := strings.Split(rest, string(filepath.Separator))
	if len(segs) < 3 {
		return Identity{}
	}
	id := Identity{AccountUUID: segs[0], OrgID: segs[1]}
	// segs[2] is the local_<id> session dir; its metadata sits alongside it as
	// <accountUUID>/<orgUUID>/local_<id>.json.
	if strings.HasPrefix(segs[2], "local_") {
		metaPath := filepath.Join(transcriptPath[:i+len(marker)], segs[0], segs[1], segs[2]+".json")
		if b, err := os.ReadFile(metaPath); err == nil {
			var m struct {
				EmailAddress string `json:"emailAddress"`
			}
			if json.Unmarshal(b, &m) == nil {
				id.Email = m.EmailAddress
			}
		}
	}
	return id
}

// identityCache memoizes identity lookups per transcript path (metadata reads are
// filesystem hits; a transcript's identity never changes).
type identityCache struct {
	mu sync.Mutex
	m  map[string]Identity
}

func newIdentityCache() *identityCache { return &identityCache{m: map[string]Identity{}} }

// forCowork returns the cached identity for a Cowork transcript path, computing
// and caching it on first use.
func (c *identityCache) forCowork(transcriptPath string) Identity {
	c.mu.Lock()
	defer c.mu.Unlock()
	if id, ok := c.m[transcriptPath]; ok {
		return id
	}
	id := coworkIdentity(transcriptPath)
	c.m[transcriptPath] = id
	return id
}
