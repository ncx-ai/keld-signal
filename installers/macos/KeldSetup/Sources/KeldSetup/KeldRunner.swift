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
