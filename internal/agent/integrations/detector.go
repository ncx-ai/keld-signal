package integrations

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/ncx-ai/keld-signal/internal/config"
	"github.com/ncx-ai/keld-signal/internal/tools"
)

// DefaultPoll is how often the detector stats the catalogue's config
// directories. AC-3 requires a tool that appears to be LISTED within 60 s, so
// the poll is the budget and not merely a preference.
const DefaultPoll = 60 * time.Second

// PollEnv overrides it (a Go duration).
const PollEnv = "KELD_INTEGRATIONS_POLL"

// EventConfigured is the client-event the detector emits when it configures a
// tool on its own. The other three `integration.*` codes are WS-C2's.
const EventConfigured = "integration.configured"

// Emitter is the narrow slice of the client-events emitter this package needs.
// An interface rather than *clientevents.Emitter so the detector's tests assert
// the event without owning the transport, and so this package does not depend
// on the one that batches and spools.
type Emitter interface {
	Emit(code string, fields map[string]any)
}

// Detector watches for a supported tool's config directory appearing and, when
// auto-setup is on, configures it through the SAME adapters and the SAME write
// path `keld signal setup` uses (tools.CommitPlan). It adds no second setup
// path: one is what makes a backup, a manifest entry and an uninstall that can
// put the machine back all still true of a config the daemon wrote.
type Detector struct {
	// Entries is the catalogue to watch. Injected so a test can narrow it.
	Entries []Entry
	// Poll is the tick interval; zero means DefaultPoll honouring PollEnv.
	Poll time.Duration
	// AutoSetup is read LIVE, per tick, not captured at construction: the page
	// writes the toggle and a person who turns it off expects the next poll to
	// respect that, not the next daemon restart.
	AutoSetup func() bool
	// Params are the telemetry parameters written into a tool's config — the
	// daemon's loopback address and the LOCAL secret, never Atlas's URL and
	// never the org ingest token.
	Params func() (tools.SetupParams, error)
	// Adapter resolves an adapter by ADAPTER NAME (Entry.AdapterName, which is
	// "gemini" where the id is "gemini_cli").
	Adapter func(name string) (tools.Adapter, error)
	// Emit announces a tool the detector configured. Optional.
	Emit Emitter
	// Log is where refusals go. Optional.
	Log func(format string, args ...any)

	// present is the previous tick's answer, so Tick can report what is NEW.
	present map[string]bool
	// attempted bounds retries to one per tool per daemon run. A tool whose
	// adapter reports a CONFLICT is not configured and must not be retried
	// every minute forever: the person owns that file and the pane's Set up
	// button is how they ask again.
	attempted map[string]bool
}

// Run ticks until ctx is done. The first tick happens IMMEDIATELY rather than
// after one interval: a daemon that starts on a machine where a tool was
// installed while it was down would otherwise leave that tool unconfigured for
// a minute for no reason.
func (d *Detector) Run(ctx context.Context) {
	d.Tick()
	t := time.NewTicker(d.interval())
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.Tick()
		}
	}
}

func (d *Detector) interval() time.Duration {
	if d.Poll > 0 {
		return d.Poll
	}
	if raw := os.Getenv(PollEnv); raw != "" {
		if v, err := time.ParseDuration(raw); err == nil && v > 0 {
			return v
		}
	}
	return DefaultPoll
}

// Tick stats every catalogue config directory and returns the ids that are
// present and were NOT present on the previous tick. When auto-setup is on it
// also configures any supported, present, unconfigured tool.
//
// ⚠️ The returned list is "newly present", and the auto-setup decision is NOT
// keyed on it. A tool installed while the daemon was down is present on the
// FIRST tick, so it is never "new" — and it is exactly the machine AC-3 exists
// for. What stops a loop is the manifest (which survives restarts) and
// `attempted` (which bounds a conflicting tool to one attempt per run), not the
// newness of the sighting.
func (d *Detector) Tick() []string {
	if d.present == nil {
		d.present = map[string]bool{}
	}
	if d.attempted == nil {
		d.attempted = map[string]bool{}
	}
	manifest, err := config.LoadManifest()
	if err != nil {
		d.logf("integrations: manifest unreadable, skipping this poll: %v", err)
		return nil
	}

	var appeared []string
	for _, e := range d.Entries {
		here := dirExists(e)
		if here && !d.present[e.ID] {
			appeared = append(appeared, e.ID)
		}
		d.present[e.ID] = here
		if !here {
			continue
		}
		d.maybeConfigure(e, manifest)
	}
	return appeared
}

// maybeConfigure applies one entry's adapter, subject to every refusal.
func (d *Detector) maybeConfigure(e Entry, manifest *config.Manifest) {
	switch {
	case !e.Supported, e.AdapterName == "":
		// An unsupported row, or a tool configured as part of something else.
		// It is listed; there is nothing to apply.
		return
	case d.AutoSetup == nil || !d.AutoSetup():
		return
	case Configured(e, manifest):
		// The daemon never edits a config the manifest already records.
		return
	case d.attempted[e.ID]:
		return
	}
	d.attempted[e.ID] = true

	res, err := ApplyEntry(e, d.adapterFor, d.Params, manifest)
	if err != nil {
		d.logf("integrations: %s not configured: %v", e.ID, err)
		// Not a permanent refusal — the telemetry secret may simply not exist
		// yet on a daemon that has not finished onboarding, and the manifest
		// may be writable on the next poll.
		d.attempted[e.ID] = false
		return
	}
	if res.Conflict != "" {
		// ⚠️ NEVER `replace` UNASKED. A conflict means the person's own
		// telemetry section is in that file; overwriting it silently from a
		// background poll is the one edit no backup makes acceptable. The
		// pane's Set up button is a human asking, and that is the path.
		d.logf("integrations: %s not configured (conflict: %s) — Set up on the pane to resolve", e.ID, res.Conflict)
		return
	}
	if !res.RestartRequired {
		return // nothing changed; the adapter had nothing to write
	}

	if d.Emit != nil {
		d.Emit.Emit(EventConfigured, map[string]any{
			"source":     e.ID,
			"auto_setup": true,
			"backup":     res.Backup != "",
		})
	}
	d.logf("integrations: configured %s automatically (backup %q) — restart it to finish", e.ID, res.Backup)
}

// ApplyEntry configures ONE catalogue entry through the adapter, the write
// path and the manifest record `keld signal setup` uses. It is what both the
// detector's automatic poll and the pane's Set up button call, so the two
// cannot produce differently-configured machines.
//
// It never resolves a conflict: the answer carries the conflict string and the
// file is left exactly as it was.
//
// manifest is UPDATED AND SAVED on a successful write — merged, never rebuilt.
// runSetup writes a fresh manifest because it configures every tool in one
// pass and owns the result; this runs one tool at a time against a machine
// that is already configured, and rebuilding would erase the others.
func ApplyEntry(e Entry, adapterFor func(string) (tools.Adapter, error), params func() (tools.SetupParams, error), manifest *config.Manifest) (SetupResult, error) {
	if !e.Supported || e.AdapterName == "" {
		return SetupResult{}, fmt.Errorf("%s has no adapter to apply", e.ID)
	}
	if adapterFor == nil {
		adapterFor = tools.Get
	}
	adapter, err := adapterFor(e.AdapterName)
	if err != nil {
		return SetupResult{}, err
	}
	if params == nil {
		return SetupResult{}, errors.New("no telemetry parameters")
	}
	p, err := params()
	if err != nil {
		return SetupResult{}, err
	}

	plan := adapter.Apply(tools.ReadConfig(adapter), p, false)
	if plan.Conflict != "" {
		return SetupResult{Conflict: plan.Conflict}, nil
	}
	if !plan.Changed {
		// Already exactly what the adapter would write. Not an error and not a
		// restart notice: nothing moved.
		return SetupResult{}, nil
	}

	backup, err := tools.CommitPlan(adapter, plan)
	if err != nil {
		return SetupResult{}, err
	}
	if manifest == nil {
		if manifest, err = config.LoadManifest(); err != nil {
			return SetupResult{}, err
		}
	}
	if manifest.Tools == nil {
		manifest.Tools = map[string]config.ToolManifest{}
	}
	var backupPtr *string
	if backup != "" {
		backupPtr = &backup
	}
	manifest.Tools[adapter.Name()] = config.ToolManifest{
		Name:       adapter.Name(),
		ConfigPath: plan.ConfigPath,
		Managed:    plan.Managed,
		BackupPath: backupPtr,
	}
	if err := manifest.Save(); err != nil {
		return SetupResult{}, err
	}
	return SetupResult{Backup: backup, RestartRequired: true}, nil
}

func (d *Detector) adapterFor(name string) (tools.Adapter, error) {
	if d.Adapter != nil {
		return d.Adapter(name)
	}
	return tools.Get(name)
}

func (d *Detector) logf(format string, args ...any) {
	if d.Log != nil {
		d.Log(format, args...)
		return
	}
	log.Printf(format, args...)
}

func dirExists(e Entry) bool {
	if e.ConfigDir == nil {
		return false
	}
	info, err := os.Stat(e.ConfigDir())
	return err == nil && info.IsDir()
}
