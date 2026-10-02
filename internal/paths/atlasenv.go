package paths

import (
	"encoding/json"
	"net/url"
	"os"
	"strings"
)

// AtlasEnv is a named Atlas a developer can point this machine at with
// `keld signal env <name>`, which writes `atlas_env` into agent-config.json.
// A file, not an environment variable, because the installed service carries
// no environment (LaunchAgent, systemd unit and Windows task alike), so a
// variable can never reach it; `ml_backend` and `blocks` are set the same way.
type AtlasEnv struct {
	Name string
	API  string // where the CLI and the daemon call Atlas
	Web  string // where the browser sign-in's authorize page lives
}

// AtlasEnvs is the closed set. Named, never a free URL, so the two-port local
// Atlas needs nothing remembered and a typo cannot point a machine somewhere
// strange: an unknown name reads as production.
var AtlasEnvs = []AtlasEnv{
	{Name: "prod", API: DefaultAPIURL, Web: DefaultAPIURL},
	{Name: "dev", API: "https://atlas-dev.keld.co", Web: "https://atlas-dev.keld.co"},
	{Name: "local", API: "http://localhost:8000", Web: "http://localhost:3000"},
}

// LookupAtlasEnv returns the named environment.
func LookupAtlasEnv(name string) (AtlasEnv, bool) {
	for _, e := range AtlasEnvs {
		if e.Name == strings.TrimSpace(strings.ToLower(name)) {
			return e, true
		}
	}
	return AtlasEnv{}, false
}

// configuredAtlasEnv is the environment agent-config.json names, or prod for
// a missing file, a missing key, an unreadable file or an unknown name.
func configuredAtlasEnv() AtlasEnv {
	data, err := os.ReadFile(AgentConfigPath())
	if err == nil {
		var cfg struct {
			AtlasEnv string `json:"atlas_env"`
		}
		if json.Unmarshal(data, &cfg) == nil {
			if e, ok := LookupAtlasEnv(cfg.AtlasEnv); ok {
				return e
			}
		}
	}
	return AtlasEnvs[0]
}

// CurrentAtlasEnv is the Atlas this process will use, with its name: one of
// AtlasEnvs, or "custom" when KELD_API_URL / KELD_ATLAS_WEB_URL or a CLI
// --api-url decides instead (tests and hand-started daemons).
func CurrentAtlasEnv() AtlasEnv {
	if apiOverrideSet || os.Getenv("KELD_API_URL") != "" || os.Getenv("KELD_ATLAS_WEB_URL") != "" {
		return AtlasEnv{Name: "custom", API: APIBase(), Web: AtlasWebBase()}
	}
	return configuredAtlasEnv()
}

// SendingAtlasEnv is the Atlas this machine's data goes to. A paired daemon
// publishes to the endpoint it paired with (hook.json, or KELD_CTX_ENDPOINT)
// until it is unpaired, whatever `keld signal env` says since: that setting
// moves sign-in and the CLI, never a pairing. So when pairedEndpoint is set
// this names it — one of AtlasEnvs by scheme and host, else "custom" — and
// only an unpaired machine reports the configured environment. Anything that
// tells a person where their data goes (the top bar, status, doctor) uses
// this, not CurrentAtlasEnv.
func SendingAtlasEnv(pairedEndpoint string) AtlasEnv {
	if strings.TrimSpace(pairedEndpoint) == "" {
		return CurrentAtlasEnv()
	}
	u, err := url.Parse(strings.TrimSpace(pairedEndpoint))
	if err != nil || u.Host == "" {
		return AtlasEnv{Name: "custom", API: pairedEndpoint}
	}
	for _, e := range AtlasEnvs {
		if a, err := url.Parse(e.API); err == nil && strings.EqualFold(a.Scheme, u.Scheme) && strings.EqualFold(a.Host, u.Host) {
			return e
		}
	}
	origin := u.Scheme + "://" + u.Host
	return AtlasEnv{Name: "custom", API: origin, Web: origin}
}
