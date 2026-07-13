# macOS "Keld Setup" onboarding app — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship a small native macOS app, auto-launched by the `.pkg`, that walks the user through login + tool setup by driving `keld login --json` / `keld signal setup --json`.

**Architecture:** A SwiftUI executable (Swift Package) whose parsing/runner layers are thin wrappers over the tested `keld --json` NDJSON contract. `build-app.sh` compiles it and wraps it into `KeldSetup.app`; `build-pkg.sh` stages + signs it into the pkg payload; `postinstall` registers the service headless and `open`s the app.

**Tech Stack:** Swift 5.7 / SwiftUI (macOS 13+), bash packaging scripts, Apple `pkgbuild`/`productbuild`/`codesign`/`notarytool` (CI, macOS runners).

## Global Constraints

- **No Swift toolchain on the dev box (Linux).** Swift is compiled only on the macOS CI runners; UX is human-verified on a Mac. Do not claim the macOS UX works from this environment.
- Do not change the `keld --json` contract (shipped): `login` emits `device_code`→`authorized`/`error`; `setup` emits `tool`→`done`/`error`.
- The app is **best-effort**: it never blocks/breaks the install; the LaunchAgent is registered independently of it.
- Keep the SwiftUI App entry file **not** named `main.swift` (the `@main` attribute conflicts with a `main.swift` top-level-code file in an SPM executable).
- End commit messages with `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.

---

### Task 1: KeldSetup Swift package + app source

**Files (all create):**
- `installers/macos/KeldSetup/Package.swift`
- `installers/macos/KeldSetup/Sources/KeldSetup/OnboardEvent.swift`
- `installers/macos/KeldSetup/Sources/KeldSetup/KeldRunner.swift`
- `installers/macos/KeldSetup/Sources/KeldSetup/OnboardModel.swift`
- `installers/macos/KeldSetup/Sources/KeldSetup/ContentView.swift`
- `installers/macos/KeldSetup/Sources/KeldSetup/KeldSetupApp.swift`
- `installers/macos/Info.plist`

**Interfaces:**
- Produces the `KeldSetup` executable target (built by Task 2) and the bundle `Info.plist` (used by Task 2).

- [ ] **Step 1: `Package.swift`**

```swift
// swift-tools-version:5.7
import PackageDescription

let package = Package(
    name: "KeldSetup",
    platforms: [.macOS(.v13)],
    targets: [
        .executableTarget(name: "KeldSetup", path: "Sources/KeldSetup")
    ]
)
```

- [ ] **Step 2: `OnboardEvent.swift` (wire model + pure decoder)**

```swift
import Foundation

// OnboardEvent mirrors the keld --json NDJSON events (login + signal setup).
enum OnboardEvent {
    case deviceCode(url: String, code: String)
    case authorized(principal: String, org: String)
    case tool(name: String, display: String, action: String)
    case done(configured: Int)
    case error(message: String)
}

// decodeEvent parses one NDJSON line into an OnboardEvent, or nil if the line is
// blank, malformed, or an unknown event kind (forward-compatible).
func decodeEvent(_ line: String) -> OnboardEvent? {
    let trimmed = line.trimmingCharacters(in: .whitespacesAndNewlines)
    guard !trimmed.isEmpty,
          let data = trimmed.data(using: .utf8),
          let obj = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any],
          let event = obj["event"] as? String else { return nil }
    func s(_ k: String) -> String { obj[k] as? String ?? "" }
    switch event {
    case "device_code": return .deviceCode(url: s("verification_url"), code: s("user_code"))
    case "authorized":  return .authorized(principal: s("principal"), org: s("org"))
    case "tool":        return .tool(name: s("name"), display: s("display"), action: s("action"))
    case "done":        return .done(configured: obj["configured"] as? Int ?? 0)
    case "error":       return .error(message: s("message"))
    default:            return nil
    }
}
```

- [ ] **Step 3: `KeldRunner.swift` (spawn keld, stream events)**

```swift
import Foundation

enum KeldRunner {
    // binaryPath resolves the keld CLI: the postinstall symlink first, then the
    // payload location.
    static func binaryPath() -> String {
        for c in ["/usr/local/bin/keld", "/usr/local/keld/keld"]
        where FileManager.default.isExecutableFile(atPath: c) { return c }
        return "/usr/local/bin/keld"
    }

    // run executes `keld <args>`, invoking onEvent for each decoded NDJSON line and
    // onExit with the termination status. Callbacks are delivered on the main queue.
    static func run(_ args: [String],
                    onEvent: @escaping (OnboardEvent) -> Void,
                    onExit: @escaping (Int32) -> Void) {
        let proc = Process()
        proc.executableURL = URL(fileURLWithPath: binaryPath())
        proc.arguments = args
        let pipe = Pipe()
        proc.standardOutput = pipe
        proc.standardError = FileHandle.nullDevice

        let handle = pipe.fileHandleForReading
        var buffer = Data()
        handle.readabilityHandler = { h in
            let chunk = h.availableData
            if chunk.isEmpty { return }
            buffer.append(chunk)
            while let nl = buffer.firstIndex(of: 0x0a) {
                let lineData = buffer.subdata(in: buffer.startIndex..<nl)
                buffer.removeSubrange(buffer.startIndex...nl)
                if let line = String(data: lineData, encoding: .utf8),
                   let ev = decodeEvent(line) {
                    DispatchQueue.main.async { onEvent(ev) }
                }
            }
        }
        proc.terminationHandler = { p in
            handle.readabilityHandler = nil
            DispatchQueue.main.async { onExit(p.terminationStatus) }
        }
        do {
            try proc.run()
        } catch {
            DispatchQueue.main.async {
                onEvent(.error(message: "could not launch keld: \(error.localizedDescription)"))
                onExit(1)
            }
        }
    }
}
```

- [ ] **Step 4: `OnboardModel.swift` (state machine)**

```swift
import Foundation
import AppKit

@MainActor
final class OnboardModel: ObservableObject {
    enum Phase { case authorizing, awaitingApproval, settingUp, done, failed }

    @Published var phase: Phase = .authorizing
    @Published var userCode = ""
    @Published var verificationURL = ""
    @Published var principal = ""
    @Published var tools: [ToolRow] = []
    @Published var configured = 0
    @Published var errorMessage = ""

    struct ToolRow: Identifiable { let id = UUID(); let display: String; let action: String }

    func start() {
        phase = .authorizing
        tools = []
        errorMessage = ""
        KeldRunner.run(["login", "--json", "--no-browser"],
                       onEvent: { [weak self] in self?.handleLogin($0) },
                       onExit: { [weak self] in self?.loginExited($0) })
    }

    private func handleLogin(_ ev: OnboardEvent) {
        switch ev {
        case .deviceCode(let url, let code):
            verificationURL = url; userCode = code; phase = .awaitingApproval
        case .authorized(let p, _):
            principal = p; startSetup()
        case .error(let m):
            errorMessage = m; phase = .failed
        default: break
        }
    }

    private func loginExited(_ code: Int32) {
        if code != 0 && phase != .settingUp && phase != .done {
            if errorMessage.isEmpty { errorMessage = "login failed" }
            phase = .failed
        }
    }

    private func startSetup() {
        phase = .settingUp
        KeldRunner.run(["signal", "setup", "--json"],
                       onEvent: { [weak self] in self?.handleSetup($0) },
                       onExit: { [weak self] in self?.setupExited($0) })
    }

    private func handleSetup(_ ev: OnboardEvent) {
        switch ev {
        case .tool(_, let display, let action):
            tools.append(ToolRow(display: display, action: action))
        case .done(let n):
            configured = n; phase = .done
        case .error(let m):
            errorMessage = m; phase = .failed
        default: break
        }
    }

    private func setupExited(_ code: Int32) {
        if code != 0 && phase != .done {
            if errorMessage.isEmpty { errorMessage = "setup failed" }
            phase = .failed
        }
    }

    func openBrowser() {
        guard let url = URL(string: verificationURL) else { return }
        NSWorkspace.shared.open(url)
    }
}
```

- [ ] **Step 5: `ContentView.swift`**

```swift
import SwiftUI
import AppKit

struct ContentView: View {
    @ObservedObject var model: OnboardModel

    var body: some View {
        VStack(spacing: 20) {
            Text("Welcome to Keld Signal").font(.title2).bold()

            switch model.phase {
            case .authorizing:
                ProgressView("Starting sign-in…")

            case .awaitingApproval:
                VStack(spacing: 12) {
                    Text("Authorize this device").font(.headline)
                    Text(model.userCode)
                        .font(.system(.largeTitle, design: .monospaced)).bold()
                        .textSelection(.enabled)
                    Button("Open browser to approve") { model.openBrowser() }
                    ProgressView("Waiting for approval…")
                }

            case .settingUp:
                VStack(alignment: .leading, spacing: 8) {
                    Text("Signed in as \(model.principal)").foregroundStyle(.secondary)
                    Text("Configuring your tools…").font(.headline)
                    ForEach(model.tools) { t in
                        Text("• \(t.display) — \(t.action)")
                    }
                }

            case .done:
                VStack(spacing: 10) {
                    Text("You're all set 🎉").font(.headline)
                    Text("Configured \(model.configured) tool(s).").foregroundStyle(.secondary)
                    Button("Close") { NSApplication.shared.terminate(nil) }
                }

            case .failed:
                VStack(spacing: 10) {
                    Text("Something went wrong").font(.headline)
                    Text(model.errorMessage)
                        .foregroundStyle(.secondary).multilineTextAlignment(.center)
                    Button("Retry") { model.start() }
                }
            }
        }
        .padding(30)
        .frame(width: 460, height: 360)
    }
}
```

- [ ] **Step 6: `KeldSetupApp.swift` (entry — NOT main.swift)**

```swift
import SwiftUI

@main
struct KeldSetupApp: App {
    @StateObject private var model = OnboardModel()

    var body: some Scene {
        Window("Keld Setup", id: "main") {
            ContentView(model: model)
                .onAppear { model.start() }
        }
        .windowResizability(.contentSize)
    }
}
```

- [ ] **Step 7: `installers/macos/Info.plist` (bundle template)**

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key><string>Keld Setup</string>
  <key>CFBundleDisplayName</key><string>Keld Setup</string>
  <key>CFBundleIdentifier</key><string>co.keld.setup</string>
  <key>CFBundleExecutable</key><string>KeldSetup</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>1.0</string>
  <key>CFBundleVersion</key><string>1</string>
  <key>LSMinimumSystemVersion</key><string>13.0</string>
  <key>NSHighResolutionCapable</key><true/>
</dict>
</plist>
```

- [ ] **Step 8: Local validation (no Swift here)**

Run:
```bash
python3 -c "import plistlib; plistlib.load(open('installers/macos/Info.plist','rb')); print('plist OK')"
test -f installers/macos/KeldSetup/Package.swift && echo "package present"
# Guard the main.swift pitfall:
! test -f installers/macos/KeldSetup/Sources/KeldSetup/main.swift && echo "no main.swift (good)"
```
Expected: `plist OK`, `package present`, `no main.swift (good)`. (Swift compilation is CI-only.)

- [ ] **Step 9: Commit**

```bash
git add installers/macos/KeldSetup installers/macos/Info.plist
git commit -m "feat(macos): KeldSetup SwiftUI onboarding app source

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: `build-app.sh` — compile + wrap into `KeldSetup.app`

**Files:**
- Create: `installers/macos/build-app.sh`

**Interfaces:**
- Consumes: the Swift package (Task 1) + `Info.plist` (Task 1).
- Produces: `<stage-dir>/KeldSetup.app` (consumed by Task 3).

- [ ] **Step 1: Write the script**

```bash
#!/usr/bin/env bash
# Build KeldSetup.app into <stage-dir>. macOS-only (needs the Swift toolchain that
# ships with Xcode on the CI runners). No-op with a message if swift is absent, so
# a binaries-only pkg can still be built.
set -euo pipefail
STAGE="${1:?stage dir}"
ROOT="$(cd "$(dirname "$0")" && pwd)"
PKG="$ROOT/KeldSetup"

if ! command -v swift >/dev/null 2>&1; then
  echo "build-app.sh: swift not found — skipping KeldSetup.app"
  exit 0
fi

echo "build-app.sh: building KeldSetup ($(swift --version 2>/dev/null | head -1))"
( cd "$PKG" && swift build -c release )
BIN="$PKG/.build/release/KeldSetup"
[ -x "$BIN" ] || { echo "build-app.sh: no binary at $BIN"; exit 1; }

APP="$STAGE/KeldSetup.app"
rm -rf "$APP"
mkdir -p "$APP/Contents/MacOS"
cp "$BIN" "$APP/Contents/MacOS/KeldSetup"
cp "$ROOT/Info.plist" "$APP/Contents/Info.plist"
echo "build-app.sh: wrapped $APP"
```

- [ ] **Step 2: Make executable + syntax check**

Run:
```bash
chmod +x installers/macos/build-app.sh
bash -n installers/macos/build-app.sh && echo "syntax OK"
```
Expected: `syntax OK`.

- [ ] **Step 3: Commit**

```bash
git add installers/macos/build-app.sh
git commit -m "feat(macos): build-app.sh compiles + wraps KeldSetup.app

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Wire the app into `build-pkg.sh`

**Files:**
- Modify: `installers/macos/build-pkg.sh`

**Interfaces:**
- Consumes: `build-app.sh` (Task 2).
- Produces: a pkg payload containing `KeldSetup.app` (signed when creds present).

- [ ] **Step 1: Build the app into the payload (before signing)**

In `installers/macos/build-pkg.sh`, immediately after the `ROOT="$(cd "$(dirname "$0")" && pwd)"` line, add:

```bash

# Build the onboarding app into the payload (no-op if swift is unavailable).
bash "$ROOT/build-app.sh" "$STAGE"
```

- [ ] **Step 2: Sign the app bundle in the codesign block**

Replace:

```bash
if [ -n "${APPLE_DEVELOPER_ID_APP:-}" ]; then
  for b in keld keld-agent keld-agent-sidecar/keld-agent-sidecar; do
    codesign --force --options runtime --timestamp --sign "$APPLE_DEVELOPER_ID_APP" "$STAGE/$b" || true
  done
fi
```

with:

```bash
if [ -n "${APPLE_DEVELOPER_ID_APP:-}" ]; then
  for b in keld keld-agent keld-agent-sidecar/keld-agent-sidecar; do
    codesign --force --options runtime --timestamp --sign "$APPLE_DEVELOPER_ID_APP" "$STAGE/$b" || true
  done
  if [ -d "$STAGE/KeldSetup.app" ]; then
    codesign --force --options runtime --timestamp --deep --sign "$APPLE_DEVELOPER_ID_APP" "$STAGE/KeldSetup.app" || true
  fi
fi
```

- [ ] **Step 3: Syntax check**

Run: `bash -n installers/macos/build-pkg.sh && echo "syntax OK"`
Expected: `syntax OK`.

- [ ] **Step 4: Commit**

```bash
git add installers/macos/build-pkg.sh
git commit -m "feat(macos): stage + sign KeldSetup.app in the pkg payload

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: postinstall — headless service install + launch the app

**Files:**
- Modify: `installers/macos/scripts/postinstall`

- [ ] **Step 1: Rewrite postinstall**

Replace the whole file with:

```bash
#!/bin/bash
set -e
# Payload staged to /usr/local/keld; symlink the CLIs onto PATH, keep the sidecar
# and KeldSetup.app beside keld-agent so sidecarBinPath()/the app resolve them.
PREFIX="/usr/local/keld"
ln -sf "$PREFIX/keld" /usr/local/bin/keld
ln -sf "$PREFIX/keld-agent" /usr/local/bin/keld-agent

# Register the LaunchAgent in the logged-in user's GUI session (postinstall runs as
# root). keld-agent install has no TTY here, so the shipped TTY guard registers the
# service only and skips the interactive login/setup.
uid=$(stat -f %u /dev/console)
user=$(id -un "$uid")
launchctl asuser "$uid" sudo -u "$user" "$PREFIX/keld-agent" install || true

# Launch the onboarding app in the user's GUI session (best-effort — the service is
# already registered regardless). It drives `keld login --json` / `keld signal
# setup --json` to walk the user through auth + tool setup.
if [ -d "$PREFIX/KeldSetup.app" ]; then
  launchctl asuser "$uid" sudo -u "$user" open "$PREFIX/KeldSetup.app" || true
fi

exit 0
```

- [ ] **Step 2: Syntax check**

Run: `bash -n installers/macos/scripts/postinstall && echo "syntax OK"`
Expected: `syntax OK`.

- [ ] **Step 3: Commit**

```bash
git add installers/macos/scripts/postinstall
git commit -m "feat(macos): postinstall registers service headless + launches KeldSetup.app

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Docs — record the macOS onboarding flow

**Files:**
- Modify: `README.md` (macOS install section)
- Modify: `AGENTS.md` (installers note)

- [ ] **Step 1: README — note the auto-launched app**

In `README.md`, in the "### macOS — `.pkg` installer" section, after the paragraph ending "…which the `.pkg`'s install scripts wire up.", add:

```markdown

After install, a small **Keld Setup** app opens automatically to walk you through
sign-in and tool configuration (it drives `keld login` / `keld signal setup` for
you). You can close it and run those two commands yourself later — the background
agent is registered either way.
```

- [ ] **Step 2: AGENTS.md — note the installer app + build path**

In `AGENTS.md`, under "## Repo layout", in the `installers/` area (add a line near the macOS installer references) or in the CLI/installer prose, add a sentence:

```markdown
- **macOS onboarding UI:** `installers/macos/KeldSetup/` (SwiftUI app) is compiled
  by `installers/macos/build-app.sh`, wrapped into `KeldSetup.app`, staged + signed
  by `build-pkg.sh`, and launched by the pkg `postinstall`. It drives the
  `keld --json` interface. Swift builds only on the macOS CI runners.
```

- [ ] **Step 3: Commit**

```bash
git add README.md AGENTS.md
git commit -m "docs: macOS Keld Setup onboarding app

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review

**Spec coverage:**
- SwiftUI app, state machine, `keld --json` driver, browser open, defensive parsing → Task 1.
- Swift Package (no xcodeproj), build + wrap → Tasks 1–2.
- Payload staging + deep codesign → Task 3.
- postinstall: headless service install + `open` app → Task 4.
- Docs → Task 5.
- Verification constraint (no local Swift; CI compile via build-pkg.sh; human smoke) → honored: only local shell/plist checks are claimed; `swift build` runs in the existing CI macOS job.

**Placeholder scan:** none — every file's full content is given.

**Type consistency:** `OnboardEvent` cases (Task 1 Step 2) are produced by `decodeEvent` and consumed in `OnboardModel.handleLogin/handleSetup` (Step 4) with matching associated values. `KeldRunner.run(_:onEvent:onExit:)` signature (Step 3) matches both call sites in `OnboardModel` (Step 4). `ContentView` reads `model.phase/userCode/principal/tools/configured/errorMessage` — all declared `@Published` in `OnboardModel`. `ToolRow` is `Identifiable` for the `ForEach`. `build-app.sh` writes `KeldSetup.app` where `build-pkg.sh` signs it and `postinstall` opens it (`/usr/local/keld/KeldSetup.app`).

**Known risk (flagged, not claimable here):** SwiftUI `@main`/`Window` scene as an SPM executable is compiled only in CI; if the runner's Swift/macOS SDK rejects the `Window` scene or the SPM-executable `@main`, CI will surface it — I cannot pre-verify locally.
