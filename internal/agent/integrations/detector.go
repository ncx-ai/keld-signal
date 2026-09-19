package integrations

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/ncx-ai/keld-signal/internal/config"
	"github.com/ncx-ai/keld-signal/internal/telemetry"
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
	// Probe asks the RUNNING proxy whether it accepts a credential, and is the
	// gate on the repair path alone — see repairVerified. Nil means
	// telemetry.ProbeSecret, the same request `keld signal setup` makes after
	// writing every tool's config.
	Probe func(endpoint, secret string) (telemetry.ProbeOutcome, int)

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
	// reported bounds the REFUSALS the same way. A machine whose proxy cannot
	// confirm the credential is refused on every poll, and a line a minute is a
	// line nobody reads — the `budget_shortfall_mb` rule one subsystem over,
	// which logs per worker generation rather than per poll for exactly this.
	reported map[string]bool
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
	if d.reported == nil {
		d.reported = map[string]bool{}
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

// params resolves the setup parameters for an apply, with the `tool_otlp`
// position stamped from THIS detector; paramsNow is the same thing resolved
// immediately, for the readers that build an expected plan.
//
// ⚠️ ONE AUTHORITY FOR THE SWITCH, BECAUSE TWO READS CAN DISAGREE. The
// position reaches an apply twice: `otlpDisagrees` asks `d.ToolOTLP` whether
// the file is out of step, and the adapter asks `SetupParams.ToolOTLP` what to
// write. In production both resolve from `settings.Load()`, so they agree; but
// nothing made them, and the first symptom was a repair that rewrote a config
// with the OTLP block MISSING while the detector believed the switch was on --
// the apply and the reason for it disagreeing about one fact. Stamping it here
// means no caller can hand in a third answer.
func (d *Detector) params() func() (tools.SetupParams, error) {
	if d.Params == nil {
		return nil
	}
	return d.paramsNow
}

func (d *Detector) paramsNow() (tools.SetupParams, error) {
	p, err := d.Params()
	if err != nil {
		return p, err
	}
	p.ToolOTLP = d.wantToolOTLP()
	return p, nil
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
	}
	reason, ok := d.reasonToApply(e, manifest)
	if !ok || d.attempted[e.ID] {
		return
	}

	// ⚠️ DO NOT REPAIR WHAT YOU CANNOT VERIFY. A REPAIR overwrites a value that
	// is already on disk, unattended, from a background poll; writing one the
	// running proxy has not accepted would replace a broken config with a
	// differently broken config and announce it as fixed. `keld signal setup`
	// treats "nothing answered" as a pass because a human is reading its output
	// and because the daemon is started AFTER it runs — neither is true here.
	//
	// A first-time setup is deliberately NOT gated: there is no working value
	// to lose, the tool's telemetry spools until onboarding completes, and
	// refusing would leave a machine with the daemon down permanently
	// unconfigured — the wedge `deterministicBackend` refuses one subsystem
	// over.
	if reason != ReasonFirstSetup && d.writesACredential(reason) && !d.repairVerified(e) {
		return
	}
	d.attempted[e.ID] = true

	res, err := ApplyEntry(e, d.adapterFor, d.params(), manifest)
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
	// ⚠️ A SUCCESSFUL SWITCH APPLY RELEASES THE ONE-TRY BOUND; A SUCCESSFUL
	// REPAIR DOES NOT. The two reasons want opposite things from `attempted`
	// and the difference is who is acting.
	//
	// The `tool_otlp` switch is a PERSON flipping a control, and it can be
	// flipped twice in one daemon life. Holding the bound would make the second
	// flip silently do nothing: on, then off before a restart, and the block
	// stays. So the switch releases it -- and so does a first setup, or the
	// FIRST thing a fresh daemon does would use up the tool's only attempt and
	// nothing could be repaired or switched until a restart.
	//
	// A REPAIR is this daemon disagreeing with whatever wrote the file. If the
	// drift comes straight back, something else is writing it -- on 2026-09-18
	// that was a keld 3.0.0-rc.3 still on PATH -- and repairing every minute
	// would be two binaries fighting over a person's config, with a backup
	// written each round. One try per daemon life is the bound for that, and
	// `keld signal doctor` is what names the other writer.
	//
	// A conflicting tool stays bounded either way: `attempted` is set BEFORE
	// the apply, and the conflict path returns above this line.
	if reason == ReasonOTLPSwitch || reason == ReasonFirstSetup {
		d.attempted[e.ID] = false
	}

	if reason != ReasonFirstSetup {
		// The row has to be able to say WHAT keld did, or a person watching a
		// config get rewritten under them reads `restart_required` with no
		// explanation — which is what `broken · otel` was on 2026-09-18, only
		// quieter. The route is computed from scratch per request, so this is
		// the only way the answer survives to reach it.
		if err := RecordRepair(e.ID, reason, time.Now()); err != nil {
			// The repair HAPPENED. Not recording it costs the sentence beside
			// the restart instruction, never the fix.
			d.logf("integrations: %s repaired but the note could not be recorded: %v", e.ID, err)
		}
	}
	if d.Emit != nil {
		d.Emit.Emit(EventConfigured, map[string]any{
			"source":     e.ID,
			"auto_setup": true,
			"backup":     res.Backup != "",
			// ⚠️ `reason` DISTINGUISHES A REPAIR FROM A FIRST SETUP, and until
			// it existed a fleet view could not: both arrived as
			// `integration.configured` and only one of them means a machine
			// was found broken. The set is closed (ReasonFirstSetup /
			// ReasonHookCommand / ReasonTelemetryDrift / ReasonOTLPSwitch).
			"reason": reason,
		})
	}
	if reason == ReasonFirstSetup {
		d.logf("integrations: configured %s automatically (backup %q) — restart it to finish", e.ID, res.Backup)
		return
	}
	d.logf("integrations: repaired %s (%s, backup %q) — restart it to finish", e.ID, reason, res.Backup)
}

// reasonToApply answers WHY this entry should be written now, or that it should
// not be.
//
// ⚠️ THE REFUSAL IT NARROWS IS RIGHT AND STAYS RIGHT: the daemon never edits the
// PERSON'S OWN telemetry section of a config the manifest already records.
// Overwriting that unasked from a background poll is the one edit no backup
// makes acceptable. What it does not cover is KELD'S OWN BLOCK, which keld wrote
// and owns — and leaving that unrepairable is what made 2026-09-18 a support
// problem rather than a self-healing one: agent.json held telemetry secret
// 26908e20… while ~/.codex/config.toml and ~/.claude/settings.json both held
// a5629e92…; the daemon could see both the whole time, and the pane said
// `broken · otel` while the repair waited on a human who had to know to re-run
// setup.
//
// The two exceptions are the same shape and are both bounded the same way:
// they fire only on something this keld can SEE is wrong inside its own block,
// they rewrite through the one ApplyEntry path a first-time setup uses (backup,
// manifest record and `configured_at` included), and `attempted` bounds them to
// one try per daemon life.
func (d *Detector) reasonToApply(e Entry, manifest *config.Manifest) (string, bool) {
	if !Configured(e, manifest) {
		return ReasonFirstSetup, true
	}
	// An upgrade preserves tool configs by design, so a keld that fixes the hook
	// QUOTING can never reach a machine an older keld configured; without this
	// the fix lands in the binary and the machine stays broken forever. Measured
	// on windows-latest: upgrade completes, configs survive, enrichment dark.
	// A healthy row never reaches it, pinned by TestRepairIsIdempotent one
	// package over.
	if d.hookCommandBroken(e) {
		return ReasonHookCommand, true
	}
	// ⚠️ THE SWITCH IS ASKED BEFORE DRIFT, AND THE ORDER IS LOAD-BEARING.
	// Turning `tool_otlp` ON leaves a config with no credential where one
	// belongs, which the drift reader also sees -- so with drift asked first,
	// flipping the switch on was reported as a REPAIR. That is the wrong
	// sentence in the client-event and the pane, and it had a second effect
	// that broke the feature: a repair deliberately holds the one-try bound
	// (an older keld writing the value back must not start a fight), so the
	// switch could be moved once per daemon life and flipping it back off
	// silently did nothing. A difference the switch explains is the switch's,
	// and only what is left over is drift.
	if d.otlpDisagrees(e) {
		return ReasonOTLPSwitch, true
	}
	if d.telemetryDrifted(e) {
		return ReasonTelemetryDrift, true
	}
	return "", false
}

// writesACredential reports whether an apply for this reason PUTS a telemetry
// credential into the tool's config, which is the only thing the proxy probe
// protects.
//
// ⚠️ TURNING THE SWITCH OFF MUST NOT WAIT ON THE PROXY. Off is the product
// default and it REMOVES the block, so there is no credential to confirm;
// gating it would leave a machine whose daemon cannot reach its own loopback
// stuck with a lane it was told to stop using. Turning it on writes one, so it
// is gated like any other repair.
func (d *Detector) writesACredential(reason string) bool {
	if reason == ReasonOTLPSwitch {
		return d.wantToolOTLP()
	}
	return true
}

// telemetryDrifted reports whether keld's own block in this tool's config names
// a different endpoint, or carries a different credential, from the one this
// machine is paired with — the pair `Params` resolves, which is the daemon's own
// loopback address and the LOCAL secret.
//
// It compares the VALUES keld wrote against the values keld would write now
// (the adapter's own plan), never the files: the tools rewrite their own config
// files, measured 2026-09-18, and a whole-file comparison would rewrite a
// healthy config every time one of them did.
func (d *Detector) telemetryDrifted(e Entry) bool {
	if d.Params == nil {
		return false
	}
	adapter, err := d.adapterFor(e.AdapterName)
	if err != nil || adapter == nil {
		return false
	}
	current := tools.ReadConfig(adapter)
	if current == nil {
		return false
	}
	p, err := d.paramsNow()
	if err != nil {
		// No telemetry secret yet is a normal state on a daemon that has not
		// finished onboarding. It is not evidence about the file.
		return false
	}
	plan := adapter.Apply(current, p, false)
	if plan.Conflict != "" {
		// The person's own telemetry section is in that file. Whatever keld's
		// block says, the adapter cannot write without replacing something keld
		// does not own — so this is not drift keld may act on, and the pane's
		// Set up button stays the path.
		return false
	}
	return telemetryDrifted(e.AdapterName, *current, plan.AfterText)
}

// repairVerified asks the running proxy whether it accepts the credential the
// repair is about to write, and reports rather than rewrites when it will not
// say yes.
//
// Both refusals HOLD the work rather than quarantine it: `attempted` is not
// consumed, so the next poll repairs the moment the proxy answers. That is the
// reading `/attribute`'s route-unsupported and the enrichment gate already take
// — the work becomes doable, so waiting is right.
func (d *Detector) repairVerified(e Entry) bool {
	if d.Params == nil {
		return false
	}
	p, err := d.paramsNow()
	if err != nil {
		return false
	}
	probe := d.Probe
	if probe == nil {
		probe = telemetry.ProbeSecret
	}
	switch outcome, code := probe(p.Endpoint, p.IngestToken); outcome {
	case telemetry.ProbeOK:
		return true
	case telemetry.ProbeRejected:
		d.reportOnce(e, "integrations: %s needs its telemetry settings repaired, but the running proxy REJECTED (%d) the credential to write — not rewriting", e.ID, code)
	default:
		d.reportOnce(e, "integrations: %s needs its telemetry settings repaired, but nothing answered on the loopback proxy to confirm the credential — not rewriting", e.ID)
	}
	return false
}

// reportOnce logs a refusal at most once per tool per daemon life.
func (d *Detector) reportOnce(e Entry, format string, args ...any) {
	if d.reported[e.ID] {
		return
	}
	d.reported[e.ID] = true
	d.logf(format, args...)
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
	// ⚠️ `configured_at` IS STAMPED HERE, WITH THE WRITE, and it is the only
	// record of when keld configured this tool. The tools rewrite their own
	// config files — measured 2026-09-18, Codex writing `hooks.state` at session
	// start and Claude Code rewriting settings.json unprompted — so the file's
	// mtime answers a different question, and answering the restart question
	// with it told people to restart tools nobody had touched.
	wroteAt := time.Now().UTC()
	manifest.Tools[adapter.Name()] = config.ToolManifest{
		Name:         adapter.Name(),
		ConfigPath:   plan.ConfigPath,
		Managed:      plan.Managed,
		BackupPath:   backupPtr,
		ConfiguredAt: &wroteAt,
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
