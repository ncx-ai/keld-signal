// Command ui-dev is lane D's own static server for the Keld Signal page
// (internal/agent/ui, deliverable D6 of docs/v3/contracts.md). It serves the
// embedded page plus /v1/ledger, /v1/settings and /v1/projects from a
// fixtures directory, so the page can be built and reviewed without a daemon,
// a sidecar or a secret.
//
// This is its own binary rather than a `keld-agent ui-dev` subcommand because
// lane D's brief is scoped to internal/agent/ui/ only and explicitly excludes
// internal/agentcli/, which is where a real subcommand would have to live.
// Wiring `keld-agent ui-dev` as a thin wrapper around ui.DevServer is a
// one-file follow-up for whoever owns agentcli.
package main

import (
	"flag"
	"log"
	"net/http"

	"github.com/ncx-ai/keld-signal/internal/agent/ui"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8899", "address to listen on")
	fixtures := flag.String("fixtures", "internal/agent/ui/fixtures/normal", "directory holding ledger.json, settings.json, projects.json")
	flag.Parse()

	log.Printf("ui-dev: http://%s (fixtures: %s)", *addr, *fixtures)
	log.Fatal(http.ListenAndServe(*addr, ui.DevServer(*fixtures)))
}
