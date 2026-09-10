import Combine
import Foundation
import AppKit
import Network

public enum DiscoveryState: Equatable, Sendable {
    case idle, refreshing, ready, tailscaleUnavailable(String)
}

public struct TailscaleStatusParser: Sendable {
    public init() {}

    public func parse(_ data: Data) throws -> [DiscoveredHost] {
        struct Status: Decodable { let BackendState: String?; let Peer: [String: Peer]? }
        struct Peer: Decodable {
            let HostName: String?
            let DNSName: String?
            let TailscaleIPs: [String]?
            let OS: String?
            let Online: Bool?
        }
        let status = try JSONDecoder().decode(Status.self, from: data)
        if let backendState = status.BackendState, backendState.lowercased() != "running" {
            throw HostError.transport("Tailscale is \(backendState.lowercased()).")
        }
        return (status.Peer ?? [:]).values.compactMap { peer in
            guard peer.OS?.lowercased() == "linux" else { return nil }
            let addresses = peer.TailscaleIPs ?? []
            guard let address = addresses.first(where: { $0.contains(".") && HostClient.isAllowedTailnetAddress($0) })
                    ?? addresses.first(where: { $0.contains(":") && HostClient.isAllowedTailnetAddress($0) }) else { return nil }
            let dns = peer.DNSName?.trimmingCharacters(in: CharacterSet(charactersIn: "."))
            let name = dns?.split(separator: ".").first.map(String.init) ?? peer.HostName ?? address
            return DiscoveredHost(name: name, address: address, status: peer.Online == true ? .daemonUnavailable : .offline)
        }.sorted { $0.name.localizedCaseInsensitiveCompare($1.name) == .orderedAscending }
    }
}

@MainActor
public final class TailscaleDiscovery: ObservableObject {
    @Published public private(set) var hosts: [DiscoveredHost] = []
    @Published public private(set) var state: DiscoveryState = .idle
    public var error: String? { if case let .tailscaleUnavailable(value) = state { value } else { nil } }

    private let binaryURL: URL?
    private let parser = TailscaleStatusParser()
    private let session: URLSession?
    private let statusLoader: (@Sendable () async throws -> Data)?
    private let hostProbe: (@Sendable (DiscoveredHost) async -> DiscoveredHost)?
    private var pollingTask: Task<Void, Never>?
    private var pathMonitor: NWPathMonitor?
    private var wakeObserver: NSObjectProtocol?
    private var manualHosts: [DiscoveredHost] = []
    private var refreshGeneration = 0

    public init(binaryURL: URL? = nil, session: URLSession? = nil) {
        self.binaryURL = binaryURL ?? Self.findTailscaleBinary()
        self.session = session
        self.statusLoader = nil
        self.hostProbe = nil
    }

    init(statusLoader: @escaping @Sendable () async throws -> Data, hostProbe: @escaping @Sendable (DiscoveredHost) async -> DiscoveredHost) {
        self.binaryURL = nil; self.session = nil; self.statusLoader = statusLoader; self.hostProbe = hostProbe
    }

    public func addManualHost(_ address: String, name: String? = nil) {
        guard !address.isEmpty else { return }
        let host = DiscoveredHost(name: name ?? address, address: address, status: .daemonUnavailable)
        if let index = manualHosts.firstIndex(where: { $0.id == host.id }) { manualHosts[index] = host } else { manualHosts.append(host) }
        Task { await refresh() }
    }

    public func refresh() async {
        refreshGeneration += 1
        let generation = refreshGeneration
        state = .refreshing
        var candidates = manualHosts
        if let statusLoader {
            do { candidates += try parser.parse(try await statusLoader()); guard generation == refreshGeneration else { return } }
            catch { guard generation == refreshGeneration else { return }; if manualHosts.isEmpty { state = .tailscaleUnavailable(error.localizedDescription); return } }
        } else if let binaryURL {
            do {
                candidates += try parser.parse(try await Self.runStatus(binaryURL))
                guard generation == refreshGeneration else { return }
            } catch {
                guard generation == refreshGeneration else { return }
                if manualHosts.isEmpty { state = .tailscaleUnavailable(error.localizedDescription); hosts = hosts.map { var h = $0; h.status = .offline; return h }; return }
            }
        } else if manualHosts.isEmpty {
            state = .tailscaleUnavailable("The Tailscale command-line tool could not be found."); return
        }

        let prior = Dictionary(hosts.map { ($0.id, $0) }, uniquingKeysWith: { first, _ in first })
        let mergedCandidates = candidates.map { candidate -> DiscoveredHost in
            var value = candidate
            if let old = prior[candidate.id] { value.info = old.info; value.forbiddenMessage = old.forbiddenMessage }
            return value
        }
        let unique = Dictionary(mergedCandidates.map { ($0.id, $0) }, uniquingKeysWith: { _, last in last })
        let probeCandidates = unique.values.filter { $0.status != .offline }
        let customProbe = hostProbe
        let probeSession = session
        let probed = await withTaskGroup(of: DiscoveredHost.self) { group in
            var iterator = probeCandidates.makeIterator()
            for _ in 0..<min(8, probeCandidates.count) { if let host = iterator.next() { group.addTask { if let customProbe { return await customProbe(host) }; return await Self.probe(host, session: probeSession) } } }
            var result: [DiscoveredHost] = []
            while let host = await group.next() { result.append(host); if let next = iterator.next() { group.addTask { if let customProbe { return await customProbe(next) }; return await Self.probe(next, session: probeSession) } } }
            return result
        }
        guard generation == refreshGeneration else { return }
        let current = Dictionary((probed + unique.values.filter { $0.status == .offline }).map { ($0.id, $0) }, uniquingKeysWith: { first, _ in first })
        hosts = Set(prior.keys).union(current.keys).compactMap { current[$0] ?? prior[$0].map { var h = $0; h.status = .offline; return h } }
            .sorted { $0.name.localizedCaseInsensitiveCompare($1.name) == .orderedAscending }
        state = .ready
    }

    public func start(interval: TimeInterval = 30) {
        stop()
        let monitor = NWPathMonitor()
        monitor.pathUpdateHandler = { [weak self] _ in Task { @MainActor in await self?.refresh() } }
        monitor.start(queue: DispatchQueue(label: "dev.proteanlabs.deaddrop.network"))
        pathMonitor = monitor
        wakeObserver = NSWorkspace.shared.notificationCenter.addObserver(forName: NSWorkspace.didWakeNotification, object: nil, queue: .main) { [weak self] _ in
            Task { @MainActor in await self?.refresh() }
        }
        pollingTask = Task { [weak self] in
            while !Task.isCancelled { await self?.refresh(); try? await Task.sleep(for: .seconds(max(1, interval))) }
        }
    }

    public func stop() {
        pollingTask?.cancel(); pollingTask = nil
        pathMonitor?.cancel(); pathMonitor = nil
        if let wakeObserver { NSWorkspace.shared.notificationCenter.removeObserver(wakeObserver); self.wakeObserver = nil }
    }
    deinit { pollingTask?.cancel(); pathMonitor?.cancel() }

    public static func findTailscaleBinary(fileManager: FileManager = .default) -> URL? {
        ["/usr/local/bin/tailscale", "/opt/homebrew/bin/tailscale", "/Applications/Tailscale.app/Contents/MacOS/Tailscale"].map(URL.init(fileURLWithPath:)).first { fileManager.isExecutableFile(atPath: $0.path) }
    }

    nonisolated private static func runStatus(_ binary: URL) async throws -> Data {
        try await Task.detached { try runStatusBlocking(binary) }.value
    }

    nonisolated private static func runStatusBlocking(_ binary: URL) throws -> Data {
        let process = Process(); let finished = DispatchSemaphore(value: 0)
        let outputURL = FileManager.default.temporaryDirectory.appendingPathComponent("deaddrop-status-\(UUID().uuidString)")
        FileManager.default.createFile(atPath: outputURL.path, contents: nil)
        let output = try FileHandle(forWritingTo: outputURL); defer { try? output.close(); try? FileManager.default.removeItem(at: outputURL) }
        process.executableURL = binary; process.arguments = ["status", "--json"]; process.standardOutput = output; process.standardError = output
        process.terminationHandler = { _ in finished.signal() }
        try process.run()
        guard finished.wait(timeout: .now() + 5) == .success else { process.terminate(); throw HostError.transport("tailscale status timed out") }
        try output.synchronize()
        let data = try Data(contentsOf: outputURL)
        guard process.terminationStatus == 0 else { throw HostError.transport(String(data: data, encoding: .utf8) ?? "tailscale status failed") }
        return data
    }

    nonisolated static func probe(_ host: DiscoveredHost, session: URLSession?) async -> DiscoveredHost {
        var result = host
        do {
            let client = HostClient(host: host, session: session)
            result.info = try await withThrowingTaskGroup(of: HostInfo.self) { group in
                group.addTask { try await client.info() }
                group.addTask { try await Task.sleep(for: .milliseconds(1500)); throw HostError.transport("Daemon probe timed out") }
                defer { group.cancelAll() }
                return try await group.next()!
            }
            result.status = .reachable
        } catch let HostError.forbidden(message) { result.status = .forbidden; result.forbiddenMessage = message }
        catch { result.status = .daemonUnavailable }
        return result
    }
}
