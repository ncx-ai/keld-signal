package daemon

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/attrib"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/sidecar"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
)

// The attribution path must ship OFF, mirroring blocks.Enabled's shape
// exactly: neither KELD_ATTRIBUTION nor the `attribution` config key set.
func TestTheAttributorIsOffByDefault(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	t.Setenv(attrib.EnvEnabled, "")
	if got := startAttributor(context.Background(), stubDigester{}, &fakeAttribClient{},
		"https://a/v1/x", func() string { return "tok" }, "actor", nil, false); got != nil {
		t.Fatal("the attributor started with KELD_ATTRIBUTION unset and no config key")
	}
}

// The installer's path: agent-config.json says attribution:true, no
// environment variable set anywhere — the same reason blocks.Enabled needs a
// config key at all (no service definition on any OS carries an environment
// block).
func TestTheAttributorStartsFromTheConfigKeyAlone(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	t.Setenv(attrib.EnvEnabled, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if got := startAttributor(ctx, stubDigester{}, &fakeAttribClient{},
		"https://a/v1/x", func() string { return "tok" }, "actor", nil, true); got == nil {
		t.Fatal("the attributor stayed off with attribution:true in agent-config.json")
	}
}

// KELD_ATTRIBUTION=0 must override a true config key either way.
func TestKeldAttributionZeroOverridesTheConfigKey(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	t.Setenv(attrib.EnvEnabled, "0")
	if got := startAttributor(context.Background(), stubDigester{}, &fakeAttribClient{},
		"https://a/v1/x", func() string { return "tok" }, "actor", nil, true); got != nil {
		t.Fatal("KELD_ATTRIBUTION=0 did not override attribution:true in agent-config.json")
	}
}

// With no digester or no attribute client there is nothing to schedule
// against or ask, so the path stays off rather than starting a loop that can
// only fail.
func TestTheAttributorNeedsADigesterAndClient(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	t.Setenv(attrib.EnvEnabled, "1")
	if got := startAttributor(context.Background(), nil, &fakeAttribClient{},
		"https://a/v1/x", func() string { return "tok" }, "actor", nil, false); got != nil {
		t.Fatal("the attributor started with no digester")
	}
	if got := startAttributor(context.Background(), stubDigester{}, nil,
		"https://a/v1/x", func() string { return "tok" }, "actor", nil, false); got != nil {
		t.Fatal("the attributor started with no attribute client")
	}
}

type fakeAttribClient struct{}

func (fakeAttribClient) Attribute(path, sessionID string, start, end float64, dims map[string]string) (sidecar.AttributeResult, bool) {
	return sidecar.AttributeResult{}, false
}

// The REAL sidecar client must satisfy both new capabilities, or facetsFor
// would silently leave them nil and the path would never start.
func TestTheAttributionCapabilitiesAreServiceFacetsOfTheRealClient(t *testing.T) {
	var _ attributionClient = (*sidecar.Client)(nil)
	var _ projectsPoster = (*sidecar.Client)(nil)
	var _ attrib.AttributeClient = (*sidecar.Client)(nil)
	if f := facetsFor(nil, nil); f.Attribution != nil || f.PostProjects != nil {
		t.Error("no client means no attribution capability")
	}
	c := sidecar.New("http://127.0.0.1:1", time.Second)
	f := facetsFor(c, nil)
	if f.Attribution == nil {
		t.Error("the real client must advertise the attribution client")
	}
	if f.PostProjects == nil {
		t.Error("the real client must advertise PostProjects")
	}
}

// KELD_PROJECTS_FILE wins over the remote settings key.
func TestResolveProjectsPrecedence(t *testing.T) {
	t.Run("env file wins", func(t *testing.T) {
		dir := t.TempDir()
		p := dir + "/projects.json"
		if err := writeFile(p, `[{"id":"proj_env","title":"Env"}]`); err != nil {
			t.Fatal(err)
		}
		t.Setenv(settings.EnvProjectsFile, p)
		remote := &settings.Remote{Projects: &[]settings.RemoteProject{{ID: "proj_remote"}}}
		got := resolveProjects(remote)
		if len(got) != 1 || got[0].ID != "proj_env" {
			t.Fatalf("got %+v, want the env file's project", got)
		}
	})
	t.Run("remote key when no env file", func(t *testing.T) {
		t.Setenv(settings.EnvProjectsFile, "")
		remote := &settings.Remote{Projects: &[]settings.RemoteProject{{ID: "proj_remote"}}}
		got := resolveProjects(remote)
		if len(got) != 1 || got[0].ID != "proj_remote" {
			t.Fatalf("got %+v, want the remote project", got)
		}
	})
	t.Run("none when neither is set", func(t *testing.T) {
		t.Setenv(settings.EnvProjectsFile, "")
		if got := resolveProjects(nil); got != nil {
			t.Fatalf("got %+v, want nil", got)
		}
		if got := resolveProjects(&settings.Remote{}); got != nil {
			t.Fatalf("got %+v, want nil (remote.Projects unset)", got)
		}
	})
	t.Run("unreadable env file yields no projects, not the remote list", func(t *testing.T) {
		t.Setenv(settings.EnvProjectsFile, "/does/not/exist.json")
		remote := &settings.Remote{Projects: &[]settings.RemoteProject{{ID: "proj_remote"}}}
		if got := resolveProjects(remote); got != nil {
			t.Fatalf("got %+v, want nil rather than falling through to the remote list", got)
		}
	})
}

func TestProjectsChanged(t *testing.T) {
	a := []settings.RemoteProject{{ID: "p1"}}
	b := []settings.RemoteProject{{ID: "p1"}}
	c := []settings.RemoteProject{{ID: "p2"}}
	if projectsChanged(a, b) {
		t.Fatal("identical lists must not read as changed")
	}
	if !projectsChanged(a, c) {
		t.Fatal("different lists must read as changed")
	}
	if !projectsChanged(nil, a) {
		t.Fatal("nil -> non-nil must read as changed")
	}
	if projectsChanged(nil, nil) {
		t.Fatal("nil -> nil must not read as changed")
	}
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

// C4: lastProjects moved from a bare var to projectsState because it is now
// written by TWO goroutines — the initial startup POST (moved off the
// synchronous path) and onRemote on the poll goroutine. This pins its two
// contracts directly: concurrent access must not race, and a FAILED post
// must leave the held value stale (never latch a failure as success).
func TestProjectsStateIsConcurrencySafe(t *testing.T) {
	ps := &projectsState{}
	done := make(chan struct{})
	go func() {
		for i := 0; i < 200; i++ {
			ps.set([]settings.RemoteProject{{ID: "from-goroutine-a"}})
		}
		close(done)
	}()
	for i := 0; i < 200; i++ {
		ps.changed([]settings.RemoteProject{{ID: "from-main"}})
	}
	<-done
}

func TestProjectsStateSetOnlyOnSuccess(t *testing.T) {
	ps := &projectsState{}
	p := []settings.RemoteProject{{ID: "proj_a"}}
	if !ps.changed(p) {
		t.Fatal("an empty state must read a non-nil list as changed")
	}
	// Simulate a failed post: never call set. The value must still read as
	// changed on the next attempt, not silently accepted as already-current.
	if !ps.changed(p) {
		t.Fatal("a failed post must not be remembered as success — the same list must still read as changed")
	}
	ps.set(p)
	if ps.changed(p) {
		t.Fatal("after a successful set, the identical list must read as unchanged")
	}
}
