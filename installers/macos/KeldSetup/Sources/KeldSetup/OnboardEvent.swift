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
