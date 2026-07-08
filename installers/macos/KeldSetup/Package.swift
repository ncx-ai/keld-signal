// swift-tools-version:5.7
import PackageDescription

let package = Package(
    name: "KeldSetup",
    platforms: [.macOS(.v13)],
    targets: [
        .executableTarget(name: "KeldSetup", path: "Sources/KeldSetup")
    ]
)
