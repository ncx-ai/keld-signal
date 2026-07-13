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
