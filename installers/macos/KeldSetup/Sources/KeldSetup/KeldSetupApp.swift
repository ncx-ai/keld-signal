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
