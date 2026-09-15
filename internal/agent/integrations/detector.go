package integrations

import (
	"context"
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

	adapter, err := d.adapterFor(e.AdapterName)
	if err != nil {
		d.logf("integrations: no adapter %q for %s: %v", e.AdapterName, e.ID, err)
		return
	}
	if d.Params == nil {
		return
	}
	p, err := d.Params()
	if err != nil {
		d.logf("integrations: telemetry parameters unavailable, %s not configured: %v", e.ID, err)
		// Not a permanent refusal — the secret may simply not exist yet on a
		// daemon that has not finished onboarding.
		d.attempted[e.ID] = false
		return
	}

	plan := adapter.Apply(tools.ReadConfig(adapter), p, false)
	if plan.Conflict != "" {
		// ⚠️ NEVER `replace` UNASKED. A conflict means the person's own
		// telemetry section is in that file; overwriting it silently from a
		// background poll is the one edit no backup makes acceptable. The pane
		// shows Set up, which is a human asking.
		d.logf("integrations: %s not configured (conflict: %s) — Set up on the pane to resolve", e.ID, plan.Conflict)
		return
	}
	if !plan.Changed {
		return
	}

	backup, err := tools.CommitPlan(adapter, plan)
	if err != nil {
		d.logf("integrations: writing %s config failed: %v", e.ID, err)
		d.attempted[e.ID] = false
		return
	}

	// MERGE into the manifest; never rebuild it. runSetup writes a fresh
	// manifest because it configures every tool in one pass and owns the
	// result; this runs one tool at a time against a machine that is already
	// configured, and rebuilding would erase the others.
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
		d.logf("integrations: saving the manifest after configuring %s failed: %v", e.ID, err)
		return
	}

	if d.Emit != nil {
		d.Emit.Emit(EventConfigured, map[string]any{
			"source":     e.ID,
			"auto_setup": true,
			"backup":     backup != "",
		})
	}
	d.logf("integrations: configured %s automatically (backup %q) — restart it to finish", e.ID, backup)
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
