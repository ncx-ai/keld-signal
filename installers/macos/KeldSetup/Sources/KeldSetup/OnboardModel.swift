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
