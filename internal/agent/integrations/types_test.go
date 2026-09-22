package integrations

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"
)

// declaredConsts returns, in SOURCE order, the value of every constant declared
// in types.go whose explicit type is typeName.
//
// The vocabularies are closed sets published on the wire, and the pane refuses a
// value outside them, so a constant that exists in Go and not in the exported
// slice is a value the server can emit and no consumer will render. Reading the
// source is the only check that catches that; a hand-written list in the test
// would be a third copy to forget.
func declaredConsts(t *testing.T, typeName string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "types.go", nil, 0)
	if err != nil {
		t.Fatalf("parse types.go: %v", err)
	}
	var out []string
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			id, ok := vs.Type.(*ast.Ident)
			if !ok || id.Name != typeName {
				continue
			}
			for _, v := range vs.Values {
				lit, ok := v.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					t.Fatalf("%s constant is not a string literal: %v", typeName, v)
				}
				s, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatalf("unquote %s: %v", lit.Value, err)
				}
				out = append(out, s)
			}
		}
	}
	if len(out) == 0 {
		t.Fatalf("no %s constants found in types.go", typeName)
	}
	return out
}

func TestVocabularyIsClosedAndOrdered(t *testing.T) {
	// The order AC-1 documents, and the order docs/signal-integrations-wire.md
	// repeats. It is part of the contract: the pane lists the vocabulary.
	documented := []State{
		NotInstalled, NotConfigured, RestartRequired, ApprovalRequired,
		Idle, Working, Broken, Unsupported,
	}
	if len(States) != len(documented) {
		t.Fatalf("States has %d entries, documented order has %d", len(States), len(documented))
	}
	for i := range documented {
		if States[i] != documented[i] {
			t.Errorf("States[%d] = %q, want %q", i, States[i], documented[i])
		}
	}

	// Every State constant declared in types.go appears exactly once in States,
	// in declaration order — so States cannot fall behind the constants.
	decl := declaredConsts(t, "State")
	if len(decl) != len(States) {
		t.Fatalf("types.go declares %d State constants, States carries %d: %v vs %v", len(decl), len(States), decl, States)
	}
	seen := map[string]int{}
	for i, s := range States {
		if s == "" {
			t.Errorf("States[%d] is empty", i)
		}
		seen[string(s)]++
		if decl[i] != string(s) {
			t.Errorf("States[%d] = %q, but types.go declares %q in that position", i, s, decl[i])
		}
	}
	for s, n := range seen {
		if n != 1 {
			t.Errorf("state %q appears %d times in States, want exactly once", s, n)
		}
	}

	// waiting_on is a closed set too, and "" is a member of it: a surface that
	// is waiting on nothing says so with the empty string, never by omission of
	// the vocabulary entry.
	wantWaiting := []WaitingOn{WaitingOnNothing, WaitingOnRestart, WaitingOnApproval, WaitingOnReader}
	if len(WaitingOns) != len(wantWaiting) {
		t.Fatalf("WaitingOns = %v, want %v", WaitingOns, wantWaiting)
	}
	for i := range wantWaiting {
		if WaitingOns[i] != wantWaiting[i] {
			t.Errorf("WaitingOns[%d] = %q, want %q", i, WaitingOns[i], wantWaiting[i])
		}
	}
	declWaiting := declaredConsts(t, "WaitingOn")
	if len(declWaiting) != len(WaitingOns) {
		t.Fatalf("types.go declares %d WaitingOn constants, WaitingOns carries %d", len(declWaiting), len(WaitingOns))
	}
	for i, w := range WaitingOns {
		if declWaiting[i] != string(w) {
			t.Errorf("WaitingOns[%d] = %q, but types.go declares %q in that position", i, w, declWaiting[i])
		}
	}

	// Surface kinds likewise.
	wantKinds := []SurfaceKind{SurfaceHook, SurfaceOTel, SurfaceWatcher, SurfaceExtension, SurfaceReader}
	if len(SurfaceKinds) != len(wantKinds) {
		t.Fatalf("SurfaceKinds = %v, want %v", SurfaceKinds, wantKinds)
	}
	for i := range wantKinds {
		if SurfaceKinds[i] != wantKinds[i] {
			t.Errorf("SurfaceKinds[%d] = %q, want %q", i, SurfaceKinds[i], wantKinds[i])
		}
	}
	declKinds := declaredConsts(t, "SurfaceKind")
	if len(declKinds) != len(SurfaceKinds) {
		t.Fatalf("types.go declares %d SurfaceKind constants, SurfaceKinds carries %d", len(declKinds), len(SurfaceKinds))
	}
	for i, k := range SurfaceKinds {
		if k == "" {
			t.Errorf("SurfaceKinds[%d] is empty", i)
		}
		if declKinds[i] != string(k) {
			t.Errorf("SurfaceKinds[%d] = %q, but types.go declares %q in that position", i, k, declKinds[i])
		}
	}

	// Every waiting_on that is not "" carries an instruction sentence, because
	// a row that says it is waiting and cannot say for what is the thing the
	// pane exists to prevent.
	for _, w := range WaitingOns {
		if w == WaitingOnNothing {
			if Instructions[w] != "" {
				t.Errorf("waiting on nothing carries an instruction: %q", Instructions[w])
			}
			continue
		}
		if Instructions[w] == "" {
			t.Errorf("waiting_on %q has no instruction sentence", w)
		}
	}
	// AC-9 quotes this one verbatim. It is the contract, not a paraphrase.
	const codexApproval = "Open Codex, run /hooks, approve the two keld hooks. Signal confirms here within a minute."
	if Instructions[WaitingOnApproval] != codexApproval {
		t.Errorf("approval instruction = %q, want AC-9's sentence %q", Instructions[WaitingOnApproval], codexApproval)
	}
}

func lanes(t *testing.T, id string, l SupportLevel) []SurfaceKind {
	t.Helper()
	e, ok := Get(id)
	if !ok {
		t.Fatalf("catalogue has no entry %q", id)
	}
	return e.ExpectedLanes(l)
}

func sameLanes(got, want []SurfaceKind) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestCatalogueExpectedLanesFollowSupportLevel(t *testing.T) {
	// A lane a tool cannot feed is not expected, so it can never make the tool
	// broken. ⚠️ THIS ASSERTION HAS FLIPPED. It was written while Codex had no
	// reader and said so: "flip this and the row below together". The reader
	// landed on 2026-09-15, this test failed loudly at that moment — which is
	// what it was for — and both halves moved in the same commit.
	codex, ok := Get("codex")
	if !ok {
		t.Fatal("catalogue has no codex entry")
	}
	if !codex.ReaderAvailable {
		t.Fatal("codex.ReaderAvailable is false: readers/codex.py exists, and a false flag hides a broken reader as `idle`")
	}
	if got, want := codex.ExpectedLanes(codex.SupportLevel()), []SurfaceKind{SurfaceHook, SurfaceOTel, SurfaceWatcher, SurfaceReader}; !sameLanes(got, want) {
		t.Errorf("codex expected lanes = %v, want %v", got, want)
	}
	// ...and the same table still says what a reader-less Codex would expect, so
	// the rule stays readable rather than collapsing into today's answer.
	noReader := SupportLevel{Supported: true, ReaderAvailable: false}
	if got, want := codex.ExpectedLanes(noReader), []SurfaceKind{SurfaceHook, SurfaceOTel}; !sameLanes(got, want) {
		t.Errorf("codex expected lanes without a reader = %v, want %v", got, want)
	}

	if got, want := lanes(t, "claude_code", SupportLevel{Supported: true, ReaderAvailable: true}), []SurfaceKind{SurfaceHook, SurfaceOTel, SurfaceWatcher, SurfaceReader}; !sameLanes(got, want) {
		t.Errorf("claude_code expected lanes = %v, want %v", got, want)
	}
	if got, want := lanes(t, "gemini_cli", SupportLevel{Supported: true}), []SurfaceKind{SurfaceOTel, SurfaceWatcher}; !sameLanes(got, want) {
		t.Errorf("gemini_cli expected lanes = %v, want %v", got, want)
	}
	// Cowork runs in a VM: no hook can reach the host daemon and its egress to
	// Atlas is blocked by design, so the watcher is the only lane.
	if got, want := lanes(t, "cowork", SupportLevel{Supported: true, ReaderAvailable: true}), []SurfaceKind{SurfaceWatcher}; !sameLanes(got, want) {
		t.Errorf("cowork expected lanes = %v, want %v", got, want)
	}

	// An unsupported entry expects nothing, whatever else is true of it, so it
	// can never be broken — it is a catalogue row with a storage class.
	for _, id := range []string{"pi", "antigravity", "cursor"} {
		e, ok := Get(id)
		if !ok {
			t.Fatalf("catalogue has no entry %q", id)
		}
		if e.Supported {
			t.Errorf("%s is marked supported; the spec lists it as a catalogue row only", id)
		}
		if got := e.ExpectedLanes(e.SupportLevel()); len(got) != 0 {
			t.Errorf("%s expects lanes %v; an unsupported entry expects none", id, got)
		}
		// Even asked as if it were supported, nothing is expected until its
		// surfaces say so.
		if got := e.ExpectedLanes(SupportLevel{Supported: true, ReaderAvailable: true}); len(got) != 0 && id != "pi" {
			t.Errorf("%s expects lanes %v at a supported level", id, got)
		}
	}
	pi, _ := Get("pi")
	var ext bool
	for _, s := range pi.Surfaces {
		if s.Kind == SurfaceExtension && s.Documented {
			ext = true
		}
	}
	if !ext {
		t.Error("pi has no documented extension surface")
	}

	// Every catalogue entry is well formed: the ids the wire doc lists, a
	// display name, a config dir resolved through HOME, and a known storage
	// class.
	wantIDs := []string{"claude_code", "codex", "gemini_cli", "cowork", "pi", "antigravity", "cursor"}
	if len(Catalogue) != len(wantIDs) {
		t.Fatalf("catalogue has %d entries, want %d", len(Catalogue), len(wantIDs))
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	for i, e := range Catalogue {
		if e.ID != wantIDs[i] {
			t.Errorf("Catalogue[%d].ID = %q, want %q", i, e.ID, wantIDs[i])
		}
		if e.DisplayName == "" {
			t.Errorf("%s has no display name", e.ID)
		}
		switch e.StorageClass {
		case StorageJSONLTail, StorageDBPoll, StorageRPC:
		default:
			t.Errorf("%s has unknown storage class %q", e.ID, e.StorageClass)
		}
		if e.ConfigDir == nil {
			t.Fatalf("%s has no ConfigDir", e.ID)
		}
		if dir := e.ConfigDir(); dir == "" {
			t.Errorf("%s ConfigDir() is empty", e.ID)
		} else if !filepathHasPrefix(dir, home) {
			t.Errorf("%s ConfigDir() = %q, which does not resolve through HOME %q", e.ID, dir, home)
		}
		for _, s := range e.Surfaces {
			if !knownKind(s.Kind) {
				t.Errorf("%s has surface of unknown kind %q", e.ID, s.Kind)
			}
			if s.ExpectedWhen == nil {
				t.Errorf("%s surface %q has no ExpectedWhen", e.ID, s.Kind)
			}
		}
	}
}

func knownKind(k SurfaceKind) bool {
	for _, known := range SurfaceKinds {
		if k == known {
			return true
		}
	}
	return false
}

func filepathHasPrefix(path, prefix string) bool {
	return len(path) >= len(prefix) && path[:len(prefix)] == prefix
}
