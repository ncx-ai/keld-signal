# Task 9 Report — HF Fetcher, Pinned Manifest, Streaming Hash, Daemon Provisioning

## Status: GREEN

---

## Pieces

### 1. Streaming hash (provision.go) — GREEN
`fileSHA` replaced: was `os.ReadFile` → `sha256.Sum256(b)` (loads whole file). Now opens with `os.Open`, streams via `io.Copy(h, f)`. Added `io` import. Behavior identical — all three existing provision tests pass.

### 2. Manifest constants (provision/manifest.go) — GREEN
New file. Constants `ModelRepo`, `ModelRevision`, `ModelSHA256` with pinned values verbatim. New `manifest_test.go` asserts exact pinned values.

### 3. HF Fetcher (enrich/sidecar/hf.go) — GREEN
`HFFetcher` struct with `repo`, `rev`, `baseURL`, `hc` fields. `NewHFFetcher` defaults `baseURL` to `https://huggingface.co`, 30m timeout. `Fetch(ctx, destDir)`:
1. GET `{baseURL}/api/models/{repo}/revision/{rev}` → JSON siblings list
2. For each rfilename: GET `{baseURL}/{repo}/resolve/{rev}/{rfilename}`, stream to temp file, rename atomically. `os.MkdirAll` for subdirs. Ctx-aware on every request.

### 4. CI-safe fetcher test (hf_test.go) — GREEN
`hfStub` httptest server stubs both revision endpoint (returns 2-file siblings) and resolve endpoints (returns canned bytes). Three tests: downloads all files with correct content; 404 on revision propagates error; 500 on resolve propagates error. No real network. Runs in normal CI.

### 5. Live test (hf_live_test.go) — AUTHORED (not run)
Build-tagged `//go:build hf_live`. Calls `provision.EnsureModel` with real `HFFetcher` pointing at pinned manifest constants. Excluded from `go test ./...`.

### 6. Daemon provisioning wiring (daemon.go) — GREEN
- Added `paths.ModelsDir(model)` helper to `internal/paths/paths.go` -> `~/.keld/models/{model}`.
- `mlBackend` now computes `modelDir = paths.ModelsDir("gliner2-large-v1")` and calls `mlBackendWithOpts`.
- `mlBackendWithOpts(ctx, mlBackendOpts)` — new testable core:
  - `provisionFailed atomic.Bool` declared.
  - Goroutine: `provision.EnsureModel(ctx, opts.modelDir, opts.modelSHA, opts.fetcher)`; on success `go opts.sup.Start(ctx)`; on failure logs and sets `provisionFailed`.
  - `cmd.Env = append(os.Environ(), "KELD_GLINER2_DIR="+modelDir)` set on spawn func in production path.
  - Gate: `sup.Ready() || sup.FellBack() || provisionFailed.Load()` — opens on provision fail.
  - Governor goroutine kept as-is.

### 7. daemon_test.go additions — GREEN
- `TestMLBackendProvisionSuccessPublishesViaSidecar`: pre-creates valid model dir, injects `fakeFetcherOK` (EnsureModel no-ops), httptest sidecar stub healthy -> worker publishes via sidecar router within 5s.
- `TestMLBackendProvisionFailurePublishesViaDeterministic`: `fakeFetcherErr` -> provisioning fails -> `provisionFailed` opens gate -> worker publishes via deterministic. Existing tests unchanged.

---

## Test run summary

```
go test ./... -race  ->  all packages PASS (29 packages, 0 failures, 0 races)
go vet ./...         ->  clean
go build ./...       ->  clean
```

---

## Files changed/created

- `internal/agent/provision/provision.go` — streaming fileSHA, added `io` import
- `internal/agent/provision/manifest.go` — new: pinned manifest constants
- `internal/agent/provision/manifest_test.go` — new: exact-value assertions
- `internal/agent/enrich/sidecar/hf.go` — new: HFFetcher
- `internal/agent/enrich/sidecar/hf_test.go` — new: CI-safe stub tests
- `internal/agent/enrich/sidecar/hf_live_test.go` — new: hf_live-tagged live test
- `internal/agent/daemon/daemon.go` — mlBackendOpts seam, mlBackendWithOpts, provision-before-spawn, KELD_GLINER2_DIR env, atomic gate
- `internal/agent/daemon/daemon_test.go` — provision success + failure tests, sha256Hex helper
- `internal/paths/paths.go` — ModelsDir helper

---

## Self-review

- **Streaming hash behavior-preserving**: yes — identical SHA output, identical early-return and mismatch semantics, all 3 existing tests pass.
- **Provision-before-spawn**: yes — `go opts.sup.Start(ctx)` is called only from inside the provisioning goroutine's success branch.
- **Gate opens on provision failure**: yes — `provisionFailed.Load()` is third disjunct in gate func.
- **KELD_GLINER2_DIR set**: yes — production spawn func appends env var; test seam injects a different model dir so tests are isolated.
- **No regression**: all 9 existing daemon tests + 4 supervisor tests pass.

## Concerns

None. The `mlBackendOpts` seam is minimal (unexported, test-only use). The `atomic.Bool` is accessed directly (Go 1.19+, well within Go 1.26). Live test authored but not run — correct per spec.
