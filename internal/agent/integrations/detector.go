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
	// ToolOTLP is the Developer switch, read LIVE per tick for the reason
	// AutoSetup is: a person who has just moved it expects the next poll to
	// act on it, not the next daemon restart. Nil means OFF, matching the
	// setting's own default and `tools.SetupParams`' zero value.
	ToolOTLP func() bool
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

	// Snapshot answers the current state of every row, and Sink publishes what
	// changed. ⚠️ BOTH ARE NEW ON 2026-09-15 AND THE ABSENCE WAS THE BUG:
	// `Reconcile` had no caller anywhere, so `integration.broken` could never
	// fire on any machine and goal G3 — a break reaches us as an event — did
	// not hold. The transition rule and its ten tests were green throughout.
	//
	// The poll is the right home: it already walks the catalogue on a timer,
	// and a state transition is what a timer is for. Nil on either leaves the
	// detector doing exactly what it did before, so a caller that wants only
	// auto-setup is unchanged.
	Snapshot func() []Integration
	Sink     Sink

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
	d.reconcile()
	t := time.NewTicker(d.interval())
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			d.Tick()
			d.reconcile()
		}
	}
}

// reconcile publishes what CHANGED since the last poll, and persists what it
// published so a machine sitting broken for a week sends one event rather than
// one per minute. Silent unless both seams are wired.
func (d *Detector) reconcile() {
	if d.Snapshot == nil || d.Sink == nil {
		return
	}
	prev, err := LoadEmittedStates()
	if err != nil {
		// An unreadable record is "nothing emitted yet", never a reason to skip
		// the poll: the cost of re-announcing a break after a corrupt file is
		// one duplicate event, and the cost of skipping is silence.
		d.logf("integrations: emitted-state record unreadable, treating as empty: %v", err)
		prev = map[string]EmittedState{}
	}
	ems, next := Reconcile(time.Now(), d.Snapshot(), prev, nil, DefaultWindow)
	if len(ems) == 0 {
		return
	}
	Emit(d.Sink, ems)
	if err := SaveEmittedStates(next); err != nil {
		// Publishing already happened. Not persisting means the next poll
		// re-announces, which is noisy but never silent — the safe direction.
		d.logf("integrations: could not persist emitted states: %v", err)
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

// hookCommandBroken reports whether the tool's config already holds a keld hook
// command that cannot execute as written — the one condition under which the
// detector edits a config the manifest records. Reading the CONFIG TEXT is the
// point: the question is what the TOOL will try to run, which is whatever an
// older keld wrote.
func (d *Detector) hookCommandBroken(e Entry) bool {
	adapter, err := d.adapterFor(e.AdapterName)
	if err != nil || adapter == nil {
		return false
	}
	current := tools.ReadConfig(adapter)
	if current == nil {
		return false
	}
	return hookCommandBroken(*current)
}

// wantToolOTLP is the Developer switch's current position; nil means off.
func (d *Detector) wantToolOTLP() bool { return d.ToolOTLP != nil && d.ToolOTLP() }

// otlpDisagrees reports whether this tool's config on disk is out of step with
// the `tool_otlp` switch — it carries keld's OTLP wiring while the switch is
// off, or lacks it while the switch is on.
//
// ⚠️ **IT IS THE ONLY WAY A CONFIG THE MANIFEST ALREADY RECORDS GETS THE BLOCK
// REMOVED.** The detector deliberately never edits a config it has already
// written, so without this a machine an earlier keld configured would keep its
// OTEL block — and the credential in it — for as long as the manifest entry
// lived, however the switch was set. It is the same narrow exception
// `hookCommandBroken` is, and it settles the same way: the answer is read back
// off the file the apply just wrote, so a successful write makes it false and
// the next poll takes the "nothing to do" branch. `TestApplyIsIdempotentInEither
// Position` one package over is what pins that it cannot oscillate.
func (d *Detector) otlpDisagrees(e Entry) bool {
	adapter, err := d.adapterFor(e.AdapterName)
	if err != nil || adapter == nil {
		return false
	}
	return tools.OTLPOnDisk(adapter, nil) != d.wantToolOTLP()
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
	case Configured(e, manifest) && !d.hookCommandBroken(e) && !d.otlpDisagrees(e):
		// The daemon never edits a config the manifest already records —
		// ⚠️ UNLESS what it records cannot execute. An upgrade preserves tool
		// configs by design, so a keld that fixes the hook QUOTING can never
		// reach a machine an older keld configured; without this the fix lands
		// in the binary and the machine stays broken forever. Measured on
		// windows-latest: upgrade completes, configs survive, enrichment dark.
		//
		// This is the narrowest possible exception. It fires only on a command
		// this keld can see is unrunnable as written, it rewrites through the
		// same ApplyEntry path a first-time setup uses, and `attempted` bounds
		// it to one try per daemon life. A healthy row never reaches it,
		// pinned by TestRepairIsIdempotent one package over.
		//
		// ⚠️ AND UNLESS THE CONFIG DISAGREES WITH THE `tool_otlp` SWITCH. That
		// is the second exception, added with the Developer switch, and it is
		// what makes the switch REVERSIBLE rather than one-way: a machine an
		// earlier keld configured keeps its OTEL block otherwise, because the
		// manifest records the tool and this branch returns. See otlpDisagrees.
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
	// ⚠️ A SUCCESSFUL WRITE RELEASES THE ONE-TRY BOUND. `attempted` exists to
	// stop a CONFLICTING tool being retried every minute forever — the person
	// owns that file and the pane's Set up button is how they ask again — and a
	// write that succeeded is not that case. Holding it would make the
	// `tool_otlp` switch move only once per daemon life: flip it on, and
	// flipping it back off before a restart would silently do nothing. Releasing
	// it cannot loop, because every trigger above is read back off the file this
	// write just produced, so a successful apply makes all of them false.
	d.attempted[e.ID] = false

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
