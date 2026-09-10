import Foundation

public struct HostInfo: Codable, Hashable, Sendable {
    public let name: String
    public let version: String
    public let `protocol`: Int
    public let roots: [String]
    public let user: String

    public init(name: String, version: String, protocol: Int, roots: [String], user: String) {
        self.name = name; self.version = version; self.protocol = `protocol`; self.roots = roots; self.user = user
    }
}

public enum HostReachability: String, Codable, Sendable { case reachable, offline, forbidden }

public struct DiscoveredHost: Identifiable, Hashable, Sendable {
    public let id: String
    public var name: String
    public var address: String
    public var status: HostReachability
    public var info: HostInfo?
    public var forbiddenMessage: String?
    public var reachable: Bool { status == .reachable }

    public init(id: String? = nil, name: String, address: String, status: HostReachability = .offline, info: HostInfo? = nil, forbiddenMessage: String? = nil) {
        self.id = id ?? address; self.name = name; self.address = address; self.status = status; self.info = info; self.forbiddenMessage = forbiddenMessage
    }
}

public enum RemoteEntryType: String, Codable, Sendable { case file, dir, symlink, other }

public struct RemoteEntry: Codable, Identifiable, Hashable, Sendable {
    public var id: String { name }
    public let name: String
    public let type: RemoteEntryType
    public let size: Int64
    public let mtime: Date
    public let mode: String
    public let linkType: RemoteEntryType?

    enum CodingKeys: String, CodingKey { case name, type, size, mtime, mode; case linkType = "link_type" }
    public init(name: String, type: RemoteEntryType, size: Int64, mtime: Date, mode: String, linkType: RemoteEntryType? = nil) {
        self.name = name; self.type = type; self.size = size; self.mtime = mtime; self.mode = mode; self.linkType = linkType
    }
}

public struct DirectoryListing: Codable, Hashable, Sendable {
    public let path: String
    public let entries: [RemoteEntry]
    public let truncated: Bool?
    public init(path: String, entries: [RemoteEntry], truncated: Bool? = nil) { self.path = path; self.entries = entries; self.truncated = truncated }
}

public struct PartialWrite: Codable, Sendable { public let partial: Bool; public let size: Int64 }

public enum HostError: Error, LocalizedError, Equatable, Sendable {
    case notFound(String), forbidden(String), exists(String), invalidPath(String), io(String), badRequest(String)
    case incompatibleProtocol(Int), invalidResponse, transport(String)

    public var errorDescription: String? {
        switch self {
        case let .notFound(v), let .forbidden(v), let .exists(v), let .invalidPath(v), let .io(v), let .badRequest(v), let .transport(v): v
        case let .incompatibleProtocol(v): "Unsupported Dead Drop protocol \(v)"
        case .invalidResponse: "The host returned an invalid response."
        }
    }
}

struct ErrorEnvelope: Codable { struct Payload: Codable { let code: String; let message: String }; let error: Payload }

extension JSONDecoder {
    static var deadDrop: JSONDecoder { let d = JSONDecoder(); d.dateDecodingStrategy = .iso8601; return d }
}
