import Foundation
import Darwin
#if canImport(FoundationNetworking)
import FoundationNetworking
#endif

public protocol HostClientProtocol: Sendable {
    func list(path: String) async throws -> DirectoryListing
    func stat(path: String) async throws -> RemoteEntry
    func mkdir(path: String) async throws -> RemoteEntry
    func partialSize(path: String) async throws -> Int64
    func uploadChunk(_ data: Data, path: String, offset: Int64, expectedSize: Int64, overwrite: Bool) async throws
    func download(path: String, offset: Int64) async throws -> URL
    func cancelPartial(path: String) async throws
}

public extension HostClientProtocol {
    func uploadFile(_ fileURL: URL, path: String, offset: Int64, expectedSize: Int64, overwrite: Bool, progress: @escaping @Sendable (Int64) -> Void) async throws {
        let data = try Data(contentsOf: fileURL); progress(Int64(data.count))
        try await uploadChunk(data, path: path, offset: offset, expectedSize: expectedSize, overwrite: overwrite)
    }
    func download(path: String, offset: Int64, length: Int64?, progress: @escaping @Sendable (Int64) -> Void) async throws -> URL {
        let url = try await download(path: path, offset: offset)
        let size = (try? url.resourceValues(forKeys: [.fileSizeKey]).fileSize).map(Int64.init) ?? 0
        progress(size); return url
    }
}

public actor HostClient: HostClientProtocol {
    public let baseURL: URL
    private let session: URLSession
    private let addressError: HostError?
    private let requestedHost: String
    private let requestedPort: Int
    private var pinnedBaseURL: URL?

    public init(host: DiscoveredHost, port: Int? = nil, session: URLSession? = nil) {
        self.init(address: host.address, port: port, session: session)
    }

    public init(address: String, port: Int? = nil, session: URLSession? = nil) {
        let parsed = URLComponents(string: "http://\(address)")
        let rawHost = parsed?.host ?? address
        let host = rawHost.hasPrefix("[") && rawHost.hasSuffix("]") ? String(rawHost.dropFirst().dropLast()) : rawHost
        let escaped = host.contains(":") ? "[\(host)]" : host
        let effectivePort = port ?? parsed?.port ?? 7477
        let constructed = URL(string: "http://\(escaped):\(effectivePort)")
        self.baseURL = constructed ?? URL(string: "http://invalid.invalid:7477")!
        self.requestedHost = host
        self.requestedPort = effectivePort
        self.addressError = constructed == nil || host.isEmpty ? .invalidPath("Invalid host address: \(address)") : nil
        self.session = session ?? Self.tailnetSession()
    }

    public init(baseURL: URL, session: URLSession? = nil) {
        self.baseURL = baseURL; self.requestedHost = baseURL.host ?? ""; self.requestedPort = baseURL.port ?? 7477
        self.addressError = baseURL.host == nil ? .invalidPath("Invalid host address") : nil
        self.session = session ?? Self.tailnetSession()
    }

    private static func tailnetSession() -> URLSession {
        let configuration = URLSessionConfiguration.ephemeral
        configuration.connectionProxyDictionary = [:]
        configuration.urlCache = nil
        configuration.requestCachePolicy = .reloadIgnoringLocalCacheData
        return URLSession(configuration: configuration, delegate: RedirectRejectingDelegate.shared, delegateQueue: nil)
    }

    public func info() async throws -> HostInfo {
        let info: HostInfo = try await json(method: "GET", path: "/v1/info")
        guard info.protocol == 1 else { throw HostError.incompatibleProtocol(info.protocol) }
        return info
    }

    public func list(path: String) async throws -> DirectoryListing { try await json(method: "GET", path: "/v1/ls", query: ["path": path]) }
    public func stat(path: String) async throws -> RemoteEntry { try await json(method: "GET", path: "/v1/stat", query: ["path": path]) }
    public func mkdir(path: String) async throws -> RemoteEntry {
        try await json(method: "POST", path: "/v1/mkdir", body: try JSONEncoder().encode(["path": path]))
    }

    public func partialSize(path: String) async throws -> Int64 {
        let (_, response) = try await request(method: "HEAD", path: "/v1/write", query: ["path": path])
        guard let value = response.value(forHTTPHeaderField: "X-Partial-Size"), let size = Int64(value) else { return 0 }
        return size
    }

    public func uploadChunk(_ data: Data, path: String, offset: Int64, expectedSize: Int64, overwrite: Bool) async throws {
        var req = try await makeRequest(method: "PUT", path: "/v1/write", query: ["path": path, "offset": String(offset), "overwrite": overwrite ? "1" : "0"])
        req.setValue(String(expectedSize), forHTTPHeaderField: "X-Expected-Size")
        req.setValue("application/octet-stream", forHTTPHeaderField: "Content-Type")
        let (responseData, response) = try await session.upload(for: req, from: data)
        try validate(response: response, data: responseData)
    }

    public func uploadFile(_ fileURL: URL, path: String, offset: Int64, expectedSize: Int64, overwrite: Bool, progress: @escaping @Sendable (Int64) -> Void) async throws {
        var req = try await makeRequest(method: "PUT", path: "/v1/write", query: ["path": path, "offset": String(offset), "overwrite": overwrite ? "1" : "0"])
        req.setValue(String(expectedSize), forHTTPHeaderField: "X-Expected-Size")
        req.setValue("application/octet-stream", forHTTPHeaderField: "Content-Type")
        let delegate = TransferProgressDelegate(upload: progress)
        do {
            let (data, response) = try await session.upload(for: req, fromFile: fileURL, delegate: delegate)
            try validate(response: response, data: data)
        } catch let error as HostError { throw error }
        catch { throw HostError.transport(error.localizedDescription) }
    }

    public func download(path: String, offset: Int64 = 0) async throws -> URL {
        try await download(path: path, offset: offset, length: nil, progress: { _ in })
    }

    public func download(path: String, offset: Int64, length: Int64?, progress: @escaping @Sendable (Int64) -> Void) async throws -> URL {
        var req = try await makeRequest(method: "GET", path: "/v1/read", query: ["path": path])
        if offset > 0 || length != nil {
            let end = length.map { offset + max(0, $0 - 1) }
            req.setValue(end.map { "bytes=\(offset)-\($0)" } ?? "bytes=\(offset)-", forHTTPHeaderField: "Range")
        }
        do {
            let delegate = TransferProgressDelegate(download: progress)
            let (url, response) = try await session.download(for: req, delegate: delegate)
            try validate(response: response, data: Data(), allowed: offset > 0 || length != nil ? [206] : [200])
            let stable = FileManager.default.temporaryDirectory.appendingPathComponent("deaddrop-\(UUID().uuidString)")
            try FileManager.default.moveItem(at: url, to: stable)
            return stable
        } catch let error as HostError { throw error }
        catch { throw HostError.transport(error.localizedDescription) }
    }

    public func cancelPartial(path: String) async throws {
        _ = try await request(method: "DELETE", path: "/v1/write", query: ["path": path])
    }

    private func json<T: Decodable>(method: String, path: String, query: [String: String] = [:], body: Data? = nil) async throws -> T {
        let (data, _) = try await request(method: method, path: path, query: query, body: body)
        do { return try JSONDecoder.deadDrop.decode(T.self, from: data) } catch { throw HostError.invalidResponse }
    }

    private func request(method: String, path: String, query: [String: String] = [:], body: Data? = nil) async throws -> (Data, HTTPURLResponse) {
        var req = try await makeRequest(method: method, path: path, query: query); req.httpBody = body
        if body != nil { req.setValue("application/json", forHTTPHeaderField: "Content-Type") }
        do {
            let (data, raw) = try await session.data(for: req)
            guard let response = raw as? HTTPURLResponse else { throw HostError.invalidResponse }
            try validate(response: response, data: data)
            return (data, response)
        } catch let error as HostError { throw error }
        catch { throw HostError.transport(error.localizedDescription) }
    }

    private func makeRequest(method: String, path: String, query: [String: String]) async throws -> URLRequest {
        if let addressError { throw addressError }
        let pinned = try await resolvedBaseURL()
        guard var parts = URLComponents(url: pinned.appendingPathComponent(path), resolvingAgainstBaseURL: false) else { throw HostError.invalidResponse }
        parts.queryItems = query.sorted { $0.key < $1.key }.map(URLQueryItem.init)
        guard let url = parts.url else { throw HostError.invalidResponse }
        var req = URLRequest(url: url); req.httpMethod = method; req.timeoutInterval = 30
        return req
    }

    private func resolvedBaseURL() async throws -> URL {
        if let pinnedBaseURL { return pinnedBaseURL }
        let host = requestedHost.trimmingCharacters(in: CharacterSet(charactersIn: "[]"))
        let numeric: String
        if Self.isIPAddress(host) {
            guard Self.isAllowedTailnetAddress(host) else { throw HostError.invalidPath("Host is outside the Tailscale address ranges: \(host)") }
            numeric = host
        } else {
            let lowered = host.lowercased().trimmingCharacters(in: CharacterSet(charactersIn: "."))
            guard !lowered.contains(".") || lowered.hasSuffix(".ts.net") else { throw HostError.invalidPath("Only MagicDNS or .ts.net host names are allowed.") }
            numeric = try await Task.detached { try Self.resolveAllowedAddress(lowered) }.value
        }
        let escaped = numeric.contains(":") ? "[\(numeric)]" : numeric
        guard let url = URL(string: "http://\(escaped):\(requestedPort)") else { throw HostError.invalidPath("Invalid resolved host address") }
        pinnedBaseURL = url
        return url
    }

    static func isIPAddress(_ value: String) -> Bool {
        var v4 = in_addr(); var v6 = in6_addr()
        return value.withCString { inet_pton(AF_INET, $0, &v4) == 1 || inet_pton(AF_INET6, $0, &v6) == 1 }
    }

    static func isAllowedTailnetAddress(_ value: String) -> Bool {
        var v4 = in_addr()
        if value.withCString({ inet_pton(AF_INET, $0, &v4) }) == 1 {
            let n = UInt32(bigEndian: v4.s_addr)
            return (n & 0xffc00000) == 0x64400000 || (n & 0xff000000) == 0x7f000000
        }
        var v6 = in6_addr()
        if value.withCString({ inet_pton(AF_INET6, $0, &v6) }) == 1 {
            let bytes = withUnsafeBytes(of: &v6) { Array($0) }
            return (bytes[0...5].elementsEqual([0xfd, 0x7a, 0x11, 0x5c, 0xa1, 0xe0])) || value == "::1"
        }
        return false
    }

    nonisolated private static func resolveAllowedAddress(_ host: String) throws -> String {
        var hints = addrinfo(ai_flags: AI_ADDRCONFIG, ai_family: AF_UNSPEC, ai_socktype: SOCK_STREAM, ai_protocol: IPPROTO_TCP, ai_addrlen: 0, ai_canonname: nil, ai_addr: nil, ai_next: nil)
        var result: UnsafeMutablePointer<addrinfo>?
        guard getaddrinfo(host, nil, &hints, &result) == 0, let first = result else { throw HostError.transport("Could not resolve MagicDNS host \(host).") }
        defer { freeaddrinfo(first) }
        var allowed: [String] = []; var cursor: UnsafeMutablePointer<addrinfo>? = first
        while let item = cursor {
            var buffer = [CChar](repeating: 0, count: Int(INET6_ADDRSTRLEN))
            let address: String?
            if item.pointee.ai_family == AF_INET {
                let sin = item.pointee.ai_addr.withMemoryRebound(to: sockaddr_in.self, capacity: 1) { $0.pointee }
                var raw = sin.sin_addr; address = inet_ntop(AF_INET, &raw, &buffer, socklen_t(buffer.count)).map { String(cString: $0) }
            } else if item.pointee.ai_family == AF_INET6 {
                let sin = item.pointee.ai_addr.withMemoryRebound(to: sockaddr_in6.self, capacity: 1) { $0.pointee }
                var raw = sin.sin6_addr; address = inet_ntop(AF_INET6, &raw, &buffer, socklen_t(buffer.count)).map { String(cString: $0) }
            } else { address = nil }
            if let address, isAllowedTailnetAddress(address) { allowed.append(address) }
            cursor = item.pointee.ai_next
        }
        guard let selected = allowed.sorted(by: { ($0.contains(":" ) ? 1 : 0) < ($1.contains(":" ) ? 1 : 0) }).first else { throw HostError.invalidPath("MagicDNS host resolved outside the Tailscale address ranges.") }
        return selected
    }

    private func validate(response raw: URLResponse, data: Data, allowed: Set<Int> = Set(200..<300)) throws {
        guard let response = raw as? HTTPURLResponse else { throw HostError.invalidResponse }
        guard allowed.contains(response.statusCode) else {
            let payload = try? JSONDecoder().decode(ErrorEnvelope.self, from: data).error
            let message = payload?.message ?? HTTPURLResponse.localizedString(forStatusCode: response.statusCode)
            switch payload?.code {
            case "not_found": throw HostError.notFound(message)
            case "forbidden": throw HostError.forbidden(message)
            case "exists": throw HostError.exists(message)
            case "invalid_path": throw HostError.invalidPath(message)
            case "io": throw HostError.io(message)
            case "bad_request": throw HostError.badRequest(message)
            default: throw response.statusCode == 403 ? HostError.forbidden(message) : HostError.transport(message)
            }
        }
    }
}

private final class RedirectRejectingDelegate: NSObject, URLSessionTaskDelegate, @unchecked Sendable {
    static let shared = RedirectRejectingDelegate()
    func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse, newRequest request: URLRequest, completionHandler: @escaping (URLRequest?) -> Void) { completionHandler(nil) }
}

private final class TransferProgressDelegate: NSObject, URLSessionTaskDelegate, URLSessionDownloadDelegate, @unchecked Sendable {
    private let upload: (@Sendable (Int64) -> Void)?
    private let download: (@Sendable (Int64) -> Void)?
    init(upload: (@Sendable (Int64) -> Void)? = nil, download: (@Sendable (Int64) -> Void)? = nil) { self.upload = upload; self.download = download }
    func urlSession(_ session: URLSession, task: URLSessionTask, didSendBodyData bytesSent: Int64, totalBytesSent: Int64, totalBytesExpectedToSend: Int64) { upload?(totalBytesSent) }
    func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse, newRequest request: URLRequest, completionHandler: @escaping (URLRequest?) -> Void) { completionHandler(nil) }
    func urlSession(_ session: URLSession, downloadTask: URLSessionDownloadTask, didWriteData bytesWritten: Int64, totalBytesWritten: Int64, totalBytesExpectedToWrite: Int64) { download?(totalBytesWritten) }
    func urlSession(_ session: URLSession, downloadTask: URLSessionDownloadTask, didFinishDownloadingTo location: URL) {}
}
