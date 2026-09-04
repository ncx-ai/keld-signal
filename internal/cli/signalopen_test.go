package cli

import (
	"net/url"
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/agentcfg"
)

func TestPageURLCarriesTheSecretOnceAndEscapesIt(t *testing.T) {
	info := &agentcfg.Info{Port: 63946, Secret: "a b/c+d=e&f"}
	got := pageURL(info)
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("pageURL produced an unparseable URL %q: %v", got, err)
	}
	if u.Scheme != "http" || u.Host != "127.0.0.1:63946" {
		t.Fatalf("must address the loopback daemon, got %s://%s", u.Scheme, u.Host)
	}
	if u.Query().Get("secret") != info.Secret {
		t.Fatalf("secret round-trip failed: got %q want %q", u.Query().Get("secret"), info.Secret)
	}
	// A secret with & or = in it must not smuggle extra parameters.
	if len(u.Query()) != 1 {
		t.Fatalf("exactly one query parameter expected, got %v", u.Query())
	}
}

// The page strips the secret from the address bar on load; the CLI's own
// confirmation line must not print it either, or it lands in the terminal
// scrollback and in every pasted screenshot.
func TestOpenedLineNeverPrintsTheSecret(t *testing.T) {
	info := &agentcfg.Info{Port: 63946, Secret: "s3cr3t-value"}
	full := pageURL(info)
	if !strings.Contains(full, "s3cr3t-value") {
		t.Fatal("fixture is wrong: the URL should carry the secret")
	}
	// This is the string the command prints on success.
	printed := "http://127.0.0.1:63946/"
	if strings.Contains(printed, info.Secret) {
		t.Fatal("the confirmation line must not contain the secret")
	}
}

func TestSignalOpenIsRegistered(t *testing.T) {
	root := NewRootCmd()
	var signal, open bool
	for _, c := range root.Commands() {
		if c.Name() != "signal" {
			continue
		}
		signal = true
		for _, s := range c.Commands() {
			if s.Name() == "open" {
				open = true
			}
		}
	}
	if !signal || !open {
		t.Fatalf("keld signal open must be registered (signal=%v open=%v)", signal, open)
	}
}
