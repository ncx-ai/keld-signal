# Telemetry Loopback Proxy Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop giving AI tools an Atlas credential — they POST OTLP to the daemon on a fixed loopback port, and the daemon forwards with its own self-healing token, so a token rotation never again requires restarting an editor.

**Architecture:** A new fixed-port loopback listener receives OTLP and hands bodies to `clientevents.Transport` (which already does live-token-getter + bounded spool + retry) pointed at Atlas's `/v1/logs` and `/v1/metrics`. `keld signal setup` writes that loopback endpoint and a *stable* local secret into all three tools instead of Atlas's URL and the org ingest token.

**Tech Stack:** Go (host toolchain). No new dependencies.

**Spec:** `docs/superpowers/specs/2026-08-27-telemetry-loopback-proxy-design.md`

## Global Constraints

- **Fixed port `14318`**, constant with a `KELD_TELEMETRY_PORT` override. Deliberately NOT 4317/4318 (OTLP standards — collide with real collectors).
- **The telemetry secret is STABLE**: generated once, persisted in `agent.json`, **never rotated**. Unlike `agentcfg.NewSecret()`, which is regenerated every run (`daemon.go:656`).
- **The Atlas token is read PER REQUEST** via a `func() string` getter, never captured at drain start.
- **Three failure classes, three answers** (the spec's table, verbatim):
  | failure | drain | re-onboard | the batch |
  |---|---|---|---|
  | Rejected — 401/403 | **stop immediately** | once, single-flighted | **kept** |
  | Unavailable — net, DNS, timeout, 5xx | end this sweep | **never** | kept |
  | Refused — 400/404/422 | **continue to next batch** | no | dropped after one attempt |
- **Delivery is confirmed from the RESPONSE, not the status code alone** (captive portals answer 200 + HTML).
- **No prompt/response text is forwarded**, enforced structurally at the boundary.
- **`go test ./...` before every commit.** Paste output. Never claim a pass without it.
- Do not hand-roll backoff. `internal/retry` + `reauther` already own that.

---

### Task 1: `Rejected` becomes a third failure class in Transport

`DrainSpool` today has exactly two behaviours: transient → stop the sweep and keep the file; permanent → **delete** the file and continue. `retry.IsTransient` classifies 401 as permanent, so an auth rejection currently **deletes the batch** and keeps going — the batch is lost and the drain learns the same rejection once per file.

**Files:**
- Modify: `internal/agent/clientevents/transport.go` (`DrainSpool` ~line 171, `Deliver` ~line 97)
- Test: `internal/agent/clientevents/transport_test.go`

**Interfaces:**
- Produces: `clientevents.IsAuthRejection(err error) bool`, and `Transport.OnAuthRejection(func())` to register the re-onboard hook. `DrainSpool` returns after the first rejection without deleting the file.

- [ ] **Step 1: Write the failing tests**

```go
// A rejection must KEEP the batch and STOP the drain. Deleting it (today's
// behaviour, since retry classifies 401 as permanent) loses data to a condition
// that is fixed by re-onboarding, and walking the rest of the spool learns the
// same rejection once per file — which on a laptop back from a week offline is
// hundreds of pointless requests at the worst possible moment.
func TestDrainStopsAndKeepsOnAuthRejection(t *testing.T) {
	dir := t.TempDir()
	tr := NewTransport("http://atlas.invalid/v1/logs", func() string { return "tok" }, dir)
	var posts int
	tr.post = func(ctx context.Context, body []byte) (int, error) { posts++; return 401, nil }
	var authHits int
	tr.OnAuthRejection(func() { authHits++ })

	for i := 0; i < 5; i++ {
		if err := tr.spool([]byte(`{"n":1}`)); err != nil {
			t.Fatal(err)
		}
	}
	_ = tr.DrainSpool(context.Background())

	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) != 5 {
		t.Fatalf("spool has %d files after an auth rejection, want all 5 kept", len(files))
	}
	if posts != 1 {
		t.Fatalf("posted %d times, want 1 — the drain must stop on the first rejection", posts)
	}
	if authHits != 1 {
		t.Fatalf("auth hook fired %d times, want exactly 1", authHits)
	}
}

// 5xx/network is NOT a rejection: it must never trigger re-onboarding. 401 and
// 503 arrive on the same code path, which is why this is the assertion most
// likely to regress.
func TestUnavailableNeverTriggersReonboard(t *testing.T) {
	dir := t.TempDir()
	tr := NewTransport("http://atlas.invalid/v1/logs", func() string { return "tok" }, dir)
	tr.policy = retry.Policy{Attempts: 1}
	tr.post = func(ctx context.Context, body []byte) (int, error) { return 503, nil }
	var authHits int
	tr.OnAuthRejection(func() { authHits++ })

	if err := tr.spool([]byte(`{"n":1}`)); err != nil {
		t.Fatal(err)
	}
	_ = tr.DrainSpool(context.Background())

	if authHits != 0 {
		t.Fatalf("5xx triggered %d re-onboards, want 0 — nothing is wrong with the credential", authHits)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) != 1 {
		t.Fatalf("5xx dropped the batch (%d files), want it kept", len(files))
	}
}

// A refused payload belongs to that batch, not the connection, so the drain
// CONTINUES. Stopping would let one bad batch block every good one behind it —
// head-of-line blocking that presents as "telemetry silently stopped".
func TestRefusedPayloadDoesNotBlockTheQueueBehindIt(t *testing.T) {
	dir := t.TempDir()
	tr := NewTransport("http://atlas.invalid/v1/logs", func() string { return "tok" }, dir)
	tr.policy = retry.Policy{Attempts: 1}
	var delivered int
	tr.post = func(ctx context.Context, body []byte) (int, error) {
		if bytes.Contains(body, []byte("bad")) {
			return 422, nil
		}
		delivered++
		return 200, nil
	}
	if err := tr.spool([]byte(`{"v":"bad"}`)); err != nil {
		t.Fatal(err)
	}
	if err := tr.spool([]byte(`{"v":"good"}`)); err != nil {
		t.Fatal(err)
	}
	_ = tr.DrainSpool(context.Background())

	if delivered != 1 {
		t.Fatalf("delivered %d good batches, want 1 — a refused batch must not stall the queue", delivered)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) != 0 {
		t.Fatalf("%d files left, want 0 (bad dropped, good delivered)", len(files))
	}
}
```

- [ ] **Step 2: Run and confirm they fail**

Run: `go test ./internal/agent/clientevents/ -run 'TestDrainStops|TestUnavailableNever|TestRefusedPayload' -v`
Expected: FAIL to compile — `tr.OnAuthRejection undefined`.

- [ ] **Step 3: Implement the third class**

In `internal/agent/clientevents/transport.go`, add to the struct and constructor:

```go
	// onAuthRejection is called at most once per drain when Atlas REJECTS the
	// credential. It is how the daemon's reauther learns to refresh without this
	// package importing it.
	onAuthRejection func()
```

```go
// IsAuthRejection reports whether err is Atlas REJECTING the credential (401/403)
// rather than being unavailable or refusing a payload.
//
// ⚠️ THIS IS THE THIRD FAILURE CLASS AND IT DID NOT EXIST. retry.IsTransient
// answers a two-way question — retry or not — and puts 401 in "permanent", which
// DrainSpool implements as "delete the batch and carry on". For an auth rejection
// both halves are wrong: the batch is fine and will deliver after a re-onboard,
// and carrying on means learning the same rejection once per spooled file.
func IsAuthRejection(err error) bool {
	var se *retry.StatusError
	if errors.As(err, &se) {
		return se.Code == http.StatusUnauthorized || se.Code == http.StatusForbidden
	}
	return false
}

// OnAuthRejection registers the callback fired when a drain hits a rejection.
func (t *Transport) OnAuthRejection(fn func()) { t.onAuthRejection = fn }
```

In `DrainSpool`, before the existing transient check:

```go
		postErr := t.postWithRetry(ctx, body)
		if postErr != nil {
			// Rejected: KEEP the file and STOP. Every remaining batch would be
			// told the same thing, and re-sending a batch with a token already
			// known to be rejected is the burst this guards against.
			if IsAuthRejection(postErr) {
				if t.onAuthRejection != nil {
					t.onAuthRejection()
				}
				return nil
			}
			if retry.IsTransient(postErr) {
				return nil
			}
			...unchanged: remove the poison file and continue...
```

And in `Deliver`, spool an auth rejection instead of dropping it:

```go
	if retry.IsTransient(err) || IsAuthRejection(err) || ctx.Err() != nil {
		if spoolErr := t.spool(body); spoolErr != nil {
```

- [ ] **Step 3b: Add the offline-laptop cases**

```go
// Days offline: the spool fills and drop-oldest evicts. The NEWEST events must
// survive — the failure to catch is an eviction policy that keeps the oldest
// and discards what just happened.
func TestSpoolCapKeepsTheNewest(t *testing.T) {
	dir := t.TempDir()
	tr := NewTransport("http://atlas.invalid/v1/logs", func() string { return "tok" }, dir)
	tr.maxSpool = 3
	for i := 0; i < 10; i++ {
		if err := tr.spool([]byte(fmt.Sprintf(`{"n":%d}`, i))); err != nil {
			t.Fatal(err)
		}
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) != 3 {
		t.Fatalf("%d files, want the cap of 3", len(files))
	}
	var newest bool
	for _, f := range files {
		b, _ := os.ReadFile(f)
		if bytes.Contains(b, []byte(`"n":9`)) {
			newest = true
		}
	}
	if !newest {
		t.Fatal("eviction discarded the NEWEST batch — it must drop the oldest")
	}
}

// Kill -9 mid-write leaves a torn file. The next drain must skip it, not choke:
// one corrupt file must not stall every good batch behind it.
func TestATornSpoolFileDoesNotStallTheDrain(t *testing.T) {
	dir := t.TempDir()
	tr := NewTransport("http://atlas.invalid/v1/logs", func() string { return "tok" }, dir)
	var delivered int
	tr.post = func(ctx context.Context, body []byte) (int, error) { delivered++; return 200, nil }

	if err := os.WriteFile(filepath.Join(dir, "1-torn.json"), []byte(`{"resourceL`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := tr.spool([]byte(`{"n":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := tr.DrainSpool(context.Background()); err != nil {
		t.Fatalf("drain errored on a torn file: %v", err)
	}
	if delivered < 1 {
		t.Fatal("the good batch behind the torn file never delivered")
	}
}

// Disk full: spooling fails, and the daemon must stay up and keep serving. The
// batch is lost and that is stated, but the process must not crash and the
// listener must not wedge.
func TestSpoolFailureIsReportedNotFatal(t *testing.T) {
	tr := NewTransport("http://atlas.invalid/v1/logs", func() string { return "tok" },
		filepath.Join(t.TempDir(), "file-not-a-dir"))
	if err := os.WriteFile(filepath.Join(filepath.Dir(tr.spoolDir), "file-not-a-dir"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	tr.policy = retry.Policy{Attempts: 1}
	tr.post = func(ctx context.Context, body []byte) (int, error) { return 0, errors.New("network down") }
	// Must return an error, never panic.
	if err := tr.Deliver(context.Background(), []byte(`{"n":1}`)); err == nil {
		t.Fatal("an unspoolable failure was reported as success")
	}
}
```

- [ ] **Step 4: Run the package tests**

Run: `go test ./internal/agent/clientevents/ -v`
Expected: PASS, including the six new tests and every pre-existing one.

- [ ] **Step 5: Full suite and commit**

Run: `go test ./...` — paste it.

```bash
git add internal/agent/clientevents/
git commit -m "fix(transport): a rejection is neither transient nor poison — three classes, three answers"
```

---

### Task 2: Confirm delivery from the response, not the status

**Files:**
- Modify: `internal/agent/clientevents/transport.go` (`doPost` ~line 70)
- Test: `internal/agent/clientevents/transport_test.go`

**Interfaces:**
- Consumes: Task 1's classes.
- Produces: a 200 whose body is not Atlas's is treated as transient (kept, retried), not success.

- [ ] **Step 1: Write the failing test**

```go
// ⚠️ Hotel and airport wifi answer EVERY request with 200 and an HTML login
// page. Keying success on the status code alone means the batch is considered
// delivered and DELETED — silent data loss on exactly the networks employee
// laptops sit on. The response must be recognisably Atlas's.
func TestCaptivePortal200IsNotDelivery(t *testing.T) {
	dir := t.TempDir()
	tr := NewTransport("http://atlas.invalid/v1/logs", func() string { return "tok" }, dir)
	tr.policy = retry.Policy{Attempts: 1}
	tr.postBody = func(ctx context.Context, body []byte) (int, []byte, error) {
		return 200, []byte("<!DOCTYPE html><html><body>Sign in to WiFi</body></html>"), nil
	}
	err := tr.Deliver(context.Background(), []byte(`{"n":1}`))
	if err == nil {
		t.Fatal("a captive-portal 200 was accepted as delivery")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) != 1 {
		t.Fatalf("%d files spooled, want 1 — the batch must be KEPT", len(files))
	}
}

// A real Atlas 200 (JSON body, or empty) still counts as delivery.
func TestGenuine200IsDelivery(t *testing.T) {
	dir := t.TempDir()
	tr := NewTransport("http://atlas.invalid/v1/logs", func() string { return "tok" }, dir)
	for _, body := range [][]byte{[]byte(`{"ok":true}`), {}} {
		tr.postBody = func(ctx context.Context, b []byte) (int, []byte, error) { return 200, body, nil }
		if err := tr.Deliver(context.Background(), []byte(`{"n":1}`)); err != nil {
			t.Fatalf("genuine 200 rejected: %v", err)
		}
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) != 0 {
		t.Fatalf("%d files spooled after success, want 0", len(files))
	}
}
```

- [ ] **Step 2: Run and confirm failure**

Run: `go test ./internal/agent/clientevents/ -run 'TestCaptivePortal|TestGenuine200' -v`
Expected: FAIL — `tr.postBody undefined`.

- [ ] **Step 3: Implement**

Widen the injectable seam to carry the body, keeping `post` as a thin wrapper so existing tests are untouched:

```go
	// postBody is the real POST, returning the response body so delivery can be
	// judged on CONTENT and not on the status alone. See notAtlasResponse.
	postBody func(ctx context.Context, body []byte) (int, []byte, error)
```

```go
// notAtlasResponse reports whether a 2xx body is something other than Atlas's.
//
// ⚠️ A captive portal returns 200 with HTML. Treating that as delivery deletes
// the batch, which is why this check exists at all: the cost of being wrong is
// silent loss, and the cost of being over-strict is a retry.
func notAtlasResponse(body []byte) bool {
	b := bytes.TrimSpace(body)
	if len(b) == 0 {
		return false // Atlas answers 2xx with an empty body on some routes
	}
	return b[0] != '{' && b[0] != '['
}
```

In `postWithRetry`, after a 2xx:

```go
		if code < 200 || code >= 300 {
			return retry.HTTPStatus(code)
		}
		if notAtlasResponse(respBody) {
			// Transient on purpose: the laptop will be off this network soon,
			// and the batch must survive until it is.
			return retry.HTTPStatus(http.StatusBadGateway)
		}
		return nil
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/agent/clientevents/ -v`
Expected: PASS.

- [ ] **Step 5: Full suite and commit**

Run: `go test ./...` — paste it.

```bash
git add internal/agent/clientevents/
git commit -m "fix(transport): a captive portal's 200 is not delivery"
```

---

### Task 3: A stable telemetry secret in agent.json

**Files:**
- Modify: `internal/agent/agentcfg/agentcfg.go` (add the field + accessor)
- Test: `internal/agent/agentcfg/agentcfg_test.go`

**Interfaces:**
- Produces: `agentcfg.Info.TelemetrySecret string` (json `telemetry_secret`) and `agentcfg.EnsureTelemetrySecret() (string, error)` — returns the existing secret or generates and persists one.

- [ ] **Step 1: Write the failing test**

```go
// ⚠️ THE WHOLE POINT IS THAT IT DOES NOT CHANGE. The ingress secret is
// regenerated every run (daemon.go's agentcfg.NewSecret()); writing THAT into a
// tool's config file would reintroduce the stale-credential bug this design
// removes, and daily rather than rarely. This secret is written into
// settings.json once and must stay valid forever.
func TestTelemetrySecretIsStableAcrossCalls(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	first, err := EnsureTelemetrySecret()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) < 32 {
		t.Fatalf("secret too short: %d chars", len(first))
	}
	second, err := EnsureTelemetrySecret()
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("secret changed between calls: %q -> %q", first, second)
	}
}

// And it survives a rewrite of the file by the daemon's own startup path, which
// regenerates the INGRESS secret every run.
func TestTelemetrySecretSurvivesAnIngressSecretRotation(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	tele, err := EnsureTelemetrySecret()
	if err != nil {
		t.Fatal(err)
	}
	ingress1, err := NewSecret()
	if err != nil {
		t.Fatal(err)
	}
	if err := Write(Info{Port: 1, Secret: ingress1}); err != nil {
		t.Fatal(err)
	}
	after, err := EnsureTelemetrySecret()
	if err != nil {
		t.Fatal(err)
	}
	if after != tele {
		t.Fatalf("telemetry secret was lost when the ingress secret rotated: %q -> %q", tele, after)
	}
}
```

- [ ] **Step 2: Run and confirm failure**

Run: `go test ./internal/agent/agentcfg/ -run TestTelemetrySecret -v`
Expected: FAIL — `EnsureTelemetrySecret undefined`.

- [ ] **Step 3: Implement**

Add the field to `Info` and:

```go
// EnsureTelemetrySecret returns the machine's STABLE local telemetry secret,
// generating and persisting one on first use.
//
// ⚠️ NEVER ROTATED, and that is its entire job. It is written into every AI
// tool's config by `keld signal setup`, and those tools read their config once
// at startup — so a secret that changed would strand every running tool exactly
// as the Atlas ingest token did. Contrast Info.Secret, which is regenerated on
// every daemon start and must never be written into a tool config.
func EnsureTelemetrySecret() (string, error) {
	info, err := Read()
	if err == nil && info.TelemetrySecret != "" {
		return info.TelemetrySecret, nil
	}
	sec, err := NewSecret()
	if err != nil {
		return "", err
	}
	if err == nil {
		info.TelemetrySecret = sec
	}
	if err := Write(info); err != nil {
		return "", err
	}
	return sec, nil
}
```

⚠️ **`Write` must preserve `TelemetrySecret` when other writers rewrite the file.** Check `Write`/`SetSidecarPort`: if either constructs a fresh `Info` rather than merging, the secret is lost on the next daemon start and the test above catches it. Make those paths read-modify-write.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/agent/agentcfg/ -v`
Expected: PASS.

- [ ] **Step 5: Full suite and commit**

Run: `go test ./...` — paste it.

```bash
git add internal/agent/agentcfg/
git commit -m "feat(agentcfg): a stable telemetry secret, never rotated"
```

---

### Task 4: The loopback OTLP listener and forwarder

**Files:**
- Create: `internal/agent/teleproxy/proxy.go` — the listener, auth, text guard, forward
- Create: `internal/agent/teleproxy/proxy_test.go`
- Modify: `internal/paths/paths.go` — add `TelemetrySpoolDir()`

**Interfaces:**
- Consumes: `clientevents.NewTransport`, `clientevents.IsAuthRejection`, `agentcfg.EnsureTelemetrySecret`.
- Produces: `teleproxy.New(logsEndpoint, metricsEndpoint string, token func() string, secret string, spoolDir string) *Proxy`; `(*Proxy).Handler() http.Handler`; `(*Proxy).LastForward() time.Time`; `teleproxy.Port() int`; `teleproxy.Addr() string`.

- [ ] **Step 1: Write the failing tests**

```go
func TestRejectsWrongSecret(t *testing.T) {
	p := New("http://a/v1/logs", "http://a/v1/metrics", func() string { return "atlas" }, "s3cret", t.TempDir())
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/logs", strings.NewReader(`{"resourceLogs":[]}`))
	req.Header.Set("x-keld-telemetry-secret", "wrong")
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401 — this route injects billable usage into the org", rr.Code)
	}
}

// The tool must never see an Atlas credential, and the daemon must attach its
// CURRENT one — read per request, so a rotation mid-flight is picked up.
func TestForwardsWithTheCurrentAtlasToken(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Header.Get("x-keld-ingest-token"))
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	tok := "first"
	p := New(srv.URL, srv.URL, func() string { return tok }, "s3cret", t.TempDir())
	post := func() {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/v1/logs", strings.NewReader(`{"resourceLogs":[]}`))
		req.Header.Set("x-keld-telemetry-secret", "s3cret")
		p.Handler().ServeHTTP(rr, req)
	}
	post()
	tok = "rotated"
	post()

	if len(seen) != 2 || seen[0] != "first" || seen[1] != "rotated" {
		t.Fatalf("tokens seen = %v, want [first rotated] — the token must be read per request", seen)
	}
}

// ⚠️ Structural, not a list of today's three tools' settings. The daemon is now
// a conduit for OTLP it did not author; the invariant must not depend on three
// separate defaults staying as they are.
func TestPromptTextIsStrippedBeforeForwarding(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	p := New(srv.URL, srv.URL, func() string { return "t" }, "s", t.TempDir())
	in := `{"resourceLogs":[{"body":{"stringValue":"ok"},"attributes":[` +
		`{"key":"user_prompt","value":{"stringValue":"MY SECRET PROMPT"}},` +
		`{"key":"prompt.text","value":{"stringValue":"ALSO SECRET"}},` +
		`{"key":"model","value":{"stringValue":"claude-opus-5"}}]}]}`
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/logs", strings.NewReader(in))
	req.Header.Set("x-keld-telemetry-secret", "s")
	p.Handler().ServeHTTP(rr, req)

	if bytes.Contains(body, []byte("MY SECRET PROMPT")) || bytes.Contains(body, []byte("ALSO SECRET")) {
		t.Fatalf("prompt text was forwarded: %s", body)
	}
	if !bytes.Contains(body, []byte("claude-opus-5")) {
		t.Fatalf("stripping removed a legitimate attribute: %s", body)
	}
}

// The listener must answer the tool immediately; delivery is the spool's job.
// A slow Atlas must not make the tool block or drop.
func TestListenerAcceptsEvenWhenAtlasIsDown(t *testing.T) {
	dir := t.TempDir()
	p := New("http://127.0.0.1:1/v1/logs", "http://127.0.0.1:1/v1/metrics",
		func() string { return "t" }, "s", dir)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/logs", strings.NewReader(`{"resourceLogs":[]}`))
	req.Header.Set("x-keld-telemetry-secret", "s")
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK && rr.Code != http.StatusAccepted {
		t.Fatalf("code = %d; the tool must not be blocked by Atlas being down", rr.Code)
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) == 0 {
		t.Fatal("nothing spooled — the batch was lost rather than kept")
	}
}

func TestPortDefaultsTo14318AndIsOverridable(t *testing.T) {
	if Port() != 14318 {
		t.Fatalf("default port = %d, want 14318 (deliberately not OTLP's 4317/4318)", Port())
	}
	t.Setenv("KELD_TELEMETRY_PORT", "15999")
	if Port() != 15999 {
		t.Fatalf("override ignored: %d", Port())
	}
}
```

- [ ] **Step 2: Run and confirm failure**

Run: `go test ./internal/agent/teleproxy/ -v`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Implement `internal/agent/teleproxy/proxy.go`**

Route `/v1/logs` and `/v1/metrics` to their own `clientevents.Transport` (separate spool subdirectories so a poison metrics batch cannot block logs). Authenticate on `x-keld-telemetry-secret` with `subtle.ConstantTimeCompare`. Strip text, hand to `Deliver` on a goroutine, and answer `202` immediately. Cap the body at 4 MiB with `http.MaxBytesReader`. `stripText` walks the decoded JSON and deletes any attribute whose key matches a **shape** — contains `prompt`, `completion`, `response.text`, `input.text`, `output.text` — rather than an allow-list of today's names.

- [ ] **Step 3b: Stamp the path, and prove concurrency is safe**

```go
// ⚠️ Proxied and direct-push machines must stay DISTINGUISHABLE in Atlas, or a
// debugging session cannot tell which path a machine is on — and during the
// migration both populations exist at once.
func TestForwardedBatchesAreStampedAsProxied(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("x-keld-telemetry-path")
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	p := New(srv.URL, srv.URL, func() string { return "t" }, "s", t.TempDir())
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/logs", strings.NewReader(`{"resourceLogs":[]}`))
	req.Header.Set("x-keld-telemetry-secret", "s")
	p.Handler().ServeHTTP(rr, req)
	if got != "proxy" {
		t.Fatalf("stamp = %q, want \"proxy\"", got)
	}
}

// Claude Code, Codex and Gemini open at once all POST to the same port. Run with
// -race; the failure this catches is a shared buffer or an unsynchronised spool.
func TestConcurrentToolsDoNotCorruptTheSpool(t *testing.T) {
	dir := t.TempDir()
	p := New("http://127.0.0.1:1/v1/logs", "http://127.0.0.1:1/v1/metrics",
		func() string { return "t" }, "s", dir)
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/logs",
				strings.NewReader(fmt.Sprintf(`{"resourceLogs":[{"n":%d}]}`, i)))
			req.Header.Set("x-keld-telemetry-secret", "s")
			p.Handler().ServeHTTP(rr, req)
		}(i)
	}
	wg.Wait()
	p.WaitIdle()
	for _, f := range mustGlob(t, filepath.Join(dir, "**", "*.json")) {
		b, _ := os.ReadFile(f)
		var v any
		if json.Unmarshal(b, &v) != nil {
			t.Fatalf("spooled a corrupt body: %s", b)
		}
	}
}
```

`WaitIdle()` blocks until in-flight forwards finish; it exists for tests and for
clean shutdown, and must not be used on the request path.

- [ ] **Step 4: Run tests**

Run: `go test -race ./internal/agent/teleproxy/ -v`
Expected: PASS.

- [ ] **Step 5: Full suite and commit**

Run: `go test ./...` — paste it.

```bash
git add internal/agent/teleproxy/ internal/paths/
git commit -m "feat(teleproxy): the daemon receives OTLP on loopback and forwards with its own token"
```

---

### Task 5: Start the listener in the daemon and wire re-onboarding

**Files:**
- Modify: `internal/agent/daemon/daemon.go` (near the ingress listener ~line 729, and the reauther wiring)
- Test: `internal/agent/daemon/teleproxy_test.go`

**Interfaces:**
- Consumes: `teleproxy.New/Addr/Port`, `clientevents.Transport.OnAuthRejection`.
- Produces: the listener running for the daemon's lifetime; `LastForward()` reachable by `keld-agent status`.

- [ ] **Step 1: Write the failing test**

```go
// ⚠️ A second daemon must FAIL LOUDLY, not silently steal or silently lose
// telemetry. A developer running `keld-agent run` beside the service is normal
// on an engineer's laptop, and this is the failure mode the fixed port
// introduces that the ephemeral one never had.
func TestASecondDaemonReportsThePortRatherThanStealingIt(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	t.Setenv("KELD_TELEMETRY_PORT", port)

	_, err = startTelemetryProxy(context.Background(), nil, "http://a", func() string { return "t" }, nil)
	if err == nil {
		t.Fatal("a taken port was accepted; the daemon would point tools at a listener it does not own")
	}
}
```

- [ ] **Step 2: Run and confirm failure**

Run: `go test ./internal/agent/daemon/ -run TestASecondDaemon -v`
Expected: FAIL — `startTelemetryProxy` undefined.

- [ ] **Step 3: Implement**

Add `startTelemetryProxy` beside the other wiring: `net.Listen("tcp", teleproxy.Addr())`, returning the error rather than swallowing it; log one line per run on failure. Register `tp.OnAuthRejection(func() { reauth.refresh(ctx) })` so a rejection routes through the existing single-flight cooldown rather than a new loop.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/agent/daemon/ -v`
Expected: PASS.

- [ ] **Step 5: Full suite and commit**

Run: `go test ./...` — paste it.

```bash
git add internal/agent/daemon/
git commit -m "feat(daemon): serve the telemetry proxy, and fail loudly on a taken port"
```

---

### Task 6: Setup writes the loopback endpoint, not an Atlas credential

**Files:**
- Modify: `internal/cli/setup.go:301-305` (the `SetupParams` construction)
- Test: `internal/telemetry/telemetry_test.go`

**Interfaces:**
- Consumes: `teleproxy.Addr()`, `agentcfg.EnsureTelemetrySecret()`.
- Produces: all three tool writers emitting the loopback URL + local secret.

- [ ] **Step 1: Write the failing test**

```go
// THE REGRESSION TEST FOR THE ORIGINAL BUG. A tool config that contains an Atlas
// ingest token is a tool that goes stale the moment that token rotates, and the
// user cannot fix it without restarting their editor.
func TestNoToolConfigCarriesAnAtlasCredential(t *testing.T) {
	p := SetupParams{Endpoint: "http://127.0.0.1:14318", IngestToken: "LOCAL-SECRET", BinPath: "/usr/bin/keld"}
	claude := fmt.Sprint(ClaudeEnv(p))
	codex := CodexBlockBody(p, "codex")
	gem := fmt.Sprint(GeminiTelemetry(p))

	for name, out := range map[string]string{"claude": claude, "codex": codex, "gemini": gem} {
		if strings.Contains(out, "atlas.keld.co") {
			t.Errorf("%s config points at Atlas directly: %s", name, out)
		}
		if !strings.Contains(out, "127.0.0.1:14318") {
			t.Errorf("%s config does not point at the loopback proxy: %s", name, out)
		}
	}
}
```

- [ ] **Step 2: Run and confirm it fails**

Run: `go test ./internal/telemetry/ -run TestNoToolConfig -v`
Expected: FAIL — Gemini embeds the token in the URL and the endpoints are Atlas's.

- [ ] **Step 3: Implement**

In `setup.go`, populate from the proxy rather than Atlas:

```go
			secret, err := agentcfg.EnsureTelemetrySecret()
			if err != nil {
				return fmt.Errorf("telemetry secret: %w", err)
			}
			// ⚠️ NOT ob.Endpoint / ob.IngestToken. A tool that holds an Atlas
			// credential goes stale the moment that credential rotates, and only
			// restarting the tool fixes it — see the spec. The tool now talks to
			// the daemon, which owns the Atlas credential and self-heals it.
			p := tools.SetupParams{
				Endpoint:    "http://" + teleproxy.Addr(),
				IngestToken: secret,
				BinPath:     keldBinaryPath(),
			}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/telemetry/ ./internal/cli/ -v`
Expected: PASS.

- [ ] **Step 5: Full suite and commit**

Run: `go test ./...` — paste it.

```bash
git add internal/cli/setup.go internal/telemetry/
git commit -m "feat(setup): tools point at the daemon, and hold no Atlas credential"
```

---

### Task 7: The doctor check

**Files:**
- Modify: `internal/cli/status.go` (`newDoctorCmd` ~line 169)
- Create: `internal/localagent/telemetry.go` — the pure state function, mirroring `ModelState`
- Test: `internal/localagent/telemetry_test.go`

**Interfaces:**
- Consumes: the proxy's `LastForward()`, exposed via `keld-agent status`'s existing JSON.
- Produces: `localagent.TelemetryState{Known, Configured, LastForward, HookWritten}` with `ProblemLine() string`.

- [ ] **Step 1: Write the failing tests**

```go
// Configured, and silent since well before the credential was last rewritten.
func TestReportsWhenConfiguredAndSilent(t *testing.T) {
	now := time.Date(2026, 8, 27, 13, 0, 0, 0, time.UTC)
	s := TelemetryState{
		Known: true, Configured: true,
		HookWritten: now.Add(-30 * time.Minute),
		LastForward: now.Add(-90 * time.Minute),
		Now:         now,
	}
	if s.ProblemLine() == "" {
		t.Fatal("silent-since-before-the-rotation produced no finding")
	}
}

// ⚠️ A laptop that wakes with a skewed clock must not make doctor LIE. If the
// timestamps are unordered the honest answer is "cannot tell" — the same rule
// localagent.ModelState follows: never report a problem from an inconclusive check.
func TestClockSkewProducesNoFinding(t *testing.T) {
	now := time.Date(2026, 8, 27, 13, 0, 0, 0, time.UTC)
	s := TelemetryState{
		Known: true, Configured: true,
		HookWritten: now.Add(2 * time.Hour), // in the future: clock jumped
		LastForward: now.Add(-5 * time.Minute),
		Now:         now,
	}
	if p := s.ProblemLine(); p != "" {
		t.Fatalf("clock skew produced a confident finding: %q", p)
	}
}

// A freshly installed machine is silent and fine.
func TestFreshInstallProducesNoFinding(t *testing.T) {
	now := time.Date(2026, 8, 27, 13, 0, 0, 0, time.UTC)
	s := TelemetryState{
		Known: true, Configured: true,
		HookWritten: now.Add(-2 * time.Minute),
		LastForward: time.Time{},
		Now:         now,
	}
	if p := s.ProblemLine(); p != "" {
		t.Fatalf("a two-minute-old install was reported as broken: %q", p)
	}
}

// Unknown (daemon unreachable, or a direct-push machine the proxy never sees)
// must never produce a finding.
func TestUnknownProducesNoFinding(t *testing.T) {
	if p := (TelemetryState{Known: false}).ProblemLine(); p != "" {
		t.Fatalf("an inconclusive check produced a finding: %q", p)
	}
}
```

- [ ] **Step 2: Run and confirm failure**

Run: `go test ./internal/localagent/ -run 'TestReportsWhen|TestClockSkew|TestFreshInstall|TestUnknown' -v`
Expected: FAIL — `TelemetryState` undefined.

- [ ] **Step 3: Implement**

`ProblemLine` returns "" unless: `Known && Configured`, both timestamps are sane and ordered (`HookWritten <= Now`), the settling window (15 min) has passed, and `LastForward` precedes `HookWritten`. Then it names the fix: *"telemetry is configured but has not reached Atlas since <t>. Restart your AI tools — they read their config at startup and are still using the previous credential."*

- [ ] **Step 4: Wire it into doctor**

Append beside the model lines in `newDoctorCmd`, exactly as `modelStates()` is used.

- [ ] **Step 5: Run tests, full suite, commit**

Run: `go test ./...` — paste it.

```bash
git add internal/localagent/ internal/cli/status.go
git commit -m "feat(doctor): report telemetry configured but not flowing, and never from an inconclusive check"
```

---

### Task 8: Documentation

**Files:**
- Modify: `AGENTS.md` (the two-lanes description and the repo layout)

- [ ] **Step 1: Correct the two-lanes claim**

AGENTS.md says "**Telemetry (push):** the hook posts usage telemetry straight to Atlas. No daemon involvement." That is no longer true for a machine that has re-run setup. Replace with the loopback description, the reason (a tool holding a credential goes stale and only a restart fixes it), the fixed port and stable secret, and the fact that telemetry now depends on the daemon with the spool as the mitigation.

- [ ] **Step 2: Add teleproxy to the repo layout**

- [ ] **Step 3: Commit**

```bash
git add AGENTS.md
git commit -m "docs: telemetry goes through the daemon, and why the tool must hold no credential"
```

---

## Final verification

- [ ] `go test ./...` — paste it.
- [ ] Sidecar suite unchanged: `cd sidecar && for f in app/test_*.py; do PYTHONPATH=. ~/.keld/sidecar-venv/bin/python "$f"; done`
- [ ] **Live**: restart the daemon, confirm `curl -s -o /dev/null -w '%{http_code}' -X POST -H 'x-keld-telemetry-secret: <secret>' -d '{"resourceLogs":[]}' http://127.0.0.1:14318/v1/logs` returns 202, and that Atlas's `tool_events` advances after a real tool pushes.
- [ ] **State the gap**: no automated test proves a real Claude Code reads a loopback endpoint from settings.json and pushes to it. That is one human run per tool and belongs in the release checklist.
