package daemon

import (
	"context"
	"log"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/clientevents"
	"github.com/ncx-ai/keld-signal/internal/agent/creds"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
	"github.com/ncx-ai/keld-signal/internal/hook"
	"github.com/ncx-ai/keld-signal/internal/paths"
)

// alreadyPaired reports whether hook.json already carries a usable pairing, so
// the "collecting unpaired" line is printed only on a machine that is actually
// waiting. It asks the same question awaitConfig asks, through the same reader
// (env overrides included), so the two can never disagree about one machine.
func alreadyPaired() bool {
	cfg, err := hook.LoadConfig()
	return err == nil && cfg != nil && cfg.Endpoint != "" && cfg.IngestToken != ""
}

// startSenders adopts the pairing and starts the delivery half.
//
// ⚠️ **IT IS THE ONLY THING IN Run THAT WAITED FOR hook.json, AND EVERYTHING
// ELSE USED TO WAIT BEHIND IT.** See pairing.go for what that cost. The senders
// that live here are the ones with a clock of their own — the client-event
// reporter and the org settings poll (which carries the auto-update pin, the
// custom passes and the project vocabulary). Every OTHER sender is constructed
// up in the collect phase against a pairing-resolved endpoint and simply starts
// working when that endpoint stops being "": the enrichment publisher, the
// block emitter, the attributor, the feature reporter and the telemetry proxy.
// That is the difference between "cannot send yet" and "not started", and it is
// what makes anything held while unpaired deliverable without a restart.
//
// cfg is nil on a machine with Send to Atlas OFF, which never waits for a
// pairing at all: there is nothing for an endpoint to be used for, and waiting
// would leave the client-event ring undrained forever on a machine that is
// working exactly as asked.
func startSenders(ctx context.Context, cfg *hook.Config, pr *pairing, tok *creds.Token,
	ra *reauther, set settings.Settings, live *settings.Live, emitter *clientevents.Emitter,
	installID string, flushInterval, pollInterval time.Duration, onRemote func(*settings.Remote)) {

	if cfg != nil {
		// ⚠️ THE ENDPOINT FIRST, THEN THE TOKEN. Every deferred sender treats an
		// empty endpoint as "hold", so the endpoint landing is what unblocks
		// them; a token published first would be read by nobody, while an
		// endpoint published first is at worst one attempt with an empty
		// credential, which spools and retries like any other rejection.
		pr.set(cfg.Endpoint)
		tok.Set(cfg.IngestToken)
		log.Printf("keld-agent: PAIRED with %s — the senders are starting. Everything collected while "+
			"unpaired is delivered from the spool or cursor that held it; no restart is needed.", cfg.Endpoint)
	}
	ra.pairedEndpoint = pr.ingest
	sendersStarted.Store(true)

	// ⚠️ The reporter is the THIRD path that predates the Atlas boundary, and it
	// was still dialling after the worker, the tick and the settings poll were
	// routed through it — found by an end-to-end run, not by a unit test. Same
	// remedy, same reason: it is handed an endpoint it cannot reach rather than
	// a flag it might forget to consult. Operational events about a machine
	// nobody is collecting from have nowhere to go, and spooling them would
	// grow a queue that can never drain.
	clientEventsEndpoint := ""
	if set.AtlasEnabled() && pr.paired() {
		clientEventsEndpoint = signalClientEventsEndpoint(pr.ingest())
	}
	reporter := clientevents.NewReporter(clientEventsEndpoint, tok.Get, installID, emitter.Drain, paths.ClientEventsSpoolDir())
	go reporter.Run(ctx, flushInterval)

	pollSettingsIfOnline(ctx, set.AtlasEnabled(), func(ctx context.Context) {
		pollSettings(ctx, settings.NewDeferredClient(deriveEndpoint(pr.ingest, settingsEndpoint), tok.Get, 10*time.Second),
			live, pollInterval, emitter, onRemote, ra)
	})
}
