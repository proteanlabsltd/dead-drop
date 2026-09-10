import AppKit
import Combine
import DeadDropKit
import Network
import UserNotifications

@MainActor
final class AppModel: ObservableObject {
    @Published var selectedHost: DiscoveredHost?
    @Published var path = "/"
    @Published var entries: [RemoteEntry] = []
    @Published var filter = ""
    @Published var error: ErrorState?
    @Published var isLoading = false
    @Published var showTransfers = false
    @Published var pendingUploads: [URL] = []
    @Published var showOverwritePrompt = false

    let discovery: TailscaleDiscovery
    let transfers = TransferManager()
    private let pathMonitor = NWPathMonitor()
    private let monitorQueue = DispatchQueue(label: "dev.proteanlabs.deaddrop.network")
    private var cancellables: Set<AnyCancellable> = []
    private var notifiedTransfers: Set<UUID> = []

    struct ErrorState: Identifiable {
        let id = UUID()
        let kind: Kind
        let message: String
        enum Kind { case forbidden, unavailable, general }
    }

    var visibleEntries: [RemoteEntry] {
        filter.isEmpty ? entries : entries.filter { $0.name.localizedCaseInsensitiveContains(filter) }
    }

    var parentPaths: [String] {
        guard path != "/" else { return [] }
        let boundary = selectedHost?.info?.roots
            .filter { path == $0 || path.hasPrefix($0.hasSuffix("/") ? $0 : $0 + "/") }
            .max(by: { $0.count < $1.count }) ?? "/"
        guard path != boundary else { return [] }
        var parents: [String] = []
        var candidate = (path as NSString).deletingLastPathComponent
        while !candidate.isEmpty {
            parents.append(candidate)
            if candidate == boundary { break }
            candidate = (candidate as NSString).deletingLastPathComponent
        }
        return parents
    }

    init(settings: SettingsStore) {
        if settings.tailscalePath.isEmpty {
            discovery = TailscaleDiscovery()
        } else {
            discovery = TailscaleDiscovery(binaryURL: URL(fileURLWithPath: settings.tailscalePath))
        }
        discovery.objectWillChange.sink { [weak self] in self?.objectWillChange.send() }.store(in: &cancellables)
        transfers.objectWillChange.sink { [weak self] in
            self?.objectWillChange.send()
            Task { @MainActor in await Task.yield(); self?.notifyFinishedTransfers() }
        }.store(in: &cancellables)
        discovery.$hosts.sink { [weak self] hosts in self?.updateSelection(hosts) }.store(in: &cancellables)
        NSWorkspace.shared.notificationCenter.publisher(for: NSWorkspace.didWakeNotification)
            .sink { [weak self] _ in Task { await self?.refreshHosts() } }.store(in: &cancellables)
        pathMonitor.pathUpdateHandler = { [weak self] _ in Task { @MainActor in await self?.refreshHosts() } }
        pathMonitor.start(queue: monitorQueue)
        UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound]) { _, _ in }
        for host in settings.manualHosts { discovery.addManualHost(host) }
        discovery.start(interval: settings.refreshInterval)
        settings.$refreshInterval.dropFirst().sink { [weak self] interval in self?.discovery.start(interval: interval) }.store(in: &cancellables)
    }

    deinit { pathMonitor.cancel() }

    func refreshHosts() async { await discovery.refresh() }

    func select(_ host: DiscoveredHost) {
        selectedHost = host
        UserDefaults.standard.set(host.id, forKey: "selectedHost")
        path = host.info?.roots.first ?? "/"
        Task { await load() }
    }

    func load() async {
        guard let host = selectedHost, host.reachable else { entries = []; return }
        isLoading = true
        defer { isLoading = false }
        do {
            let listing = try await HostClient(host: host).list(path: path)
            entries = listing.entries
            path = listing.path
            error = nil
        } catch let hostError as HostError {
            if case .forbidden(let message) = hostError { error = .init(kind: .forbidden, message: message) }
            else { error = .init(kind: .general, message: hostError.localizedDescription) }
        } catch { self.error = .init(kind: .general, message: error.localizedDescription) }
    }

    func open(_ entry: RemoteEntry) {
        guard entry.type == .dir || (entry.type == .symlink && entry.linkType == .dir) else { return }
        path = Self.join(path, entry.name)
        Task { await load() }
    }

    func goUp() {
        guard path != "/" else { return }
        path = (path as NSString).deletingLastPathComponent
        if path.isEmpty { path = "/" }
        Task { await load() }
    }

    func go(to newPath: String) { path = newPath; Task { await load() } }

    @discardableResult
    func addManualHost(_ address: String) -> Bool {
        let value = address.trimmingCharacters(in: .whitespacesAndNewlines)
        guard Self.isAllowedManualAddress(value) else {
            error = .init(kind: .general, message: "Enter a Tailscale IPv4 or IPv6 address, a MagicDNS host name, or a .ts.net name.")
            return false
        }
        var saved = UserDefaults.standard.stringArray(forKey: "manualHosts") ?? []
        if !saved.contains(value) { saved.append(value); UserDefaults.standard.set(saved, forKey: "manualHosts") }
        discovery.addManualHost(value)
        return true
    }

    func prepareUpload(_ urls: [URL]) {
        guard !urls.isEmpty, let host = selectedHost else { return }
        pendingUploads = urls
        Task {
            let client = HostClient(host: host)
            for (url, remotePath) in uploadCandidates(urls) {
                var isDirectory: ObjCBool = false
                guard FileManager.default.fileExists(atPath: url.path, isDirectory: &isDirectory), !isDirectory.boolValue else { continue }
                do {
                    _ = try await client.stat(path: remotePath)
                    showOverwritePrompt = true
                    return
                } catch HostError.notFound { continue }
                catch { continue }
            }
            finishUpload(overwrite: false)
        }
    }

    func finishUpload(overwrite: Bool) {
        guard let host = selectedHost else { return }
        transfers.enqueueUpload(urls: pendingUploads, host: host, remoteDirectory: path, overwrite: overwrite)
        pendingUploads = []
        showTransfers = true
    }

    func download(_ entry: RemoteEntry, to directory: URL) {
        guard let host = selectedHost else { return }
        transfers.enqueueDownload(entry: entry, host: host, remotePath: Self.join(path, entry.name), destination: directory)
        showTransfers = true
    }

    func createFolder(named name: String) async {
        guard let host = selectedHost, !name.isEmpty else { return }
        do { _ = try await HostClient(host: host).mkdir(path: Self.join(path, name)); await load() }
        catch { self.error = .init(kind: .general, message: error.localizedDescription) }
    }

    private func updateSelection(_ hosts: [DiscoveredHost]) {
        if let selectedHost, let updated = hosts.first(where: { $0.id == selectedHost.id }) {
            self.selectedHost = updated
            if updated.status == .forbidden {
                error = .init(kind: .forbidden, message: updated.forbiddenMessage ?? "Your Tailscale login is not listed in allow_users on this host.")
            } else if error?.kind == .forbidden {
                error = nil
            }
            if path == "/", let root = updated.info?.roots.first { path = root }
            if updated.reachable { Task { await load() } }
            return
        }
        let saved = UserDefaults.standard.string(forKey: "selectedHost")
        if let host = hosts.first(where: { $0.id == saved }) ?? hosts.first(where: { $0.reachable }) { select(host) }
    }

    private func notifyFinishedTransfers() {
        for transfer in transfers.transfers where transfer.state == .completed && notifiedTransfers.insert(transfer.id).inserted {
            let content = UNMutableNotificationContent()
            content.title = transfer.direction == .upload ? "Upload complete" : "Download complete"
            content.body = transfer.localURL.lastPathComponent
            UNUserNotificationCenter.current().add(UNNotificationRequest(identifier: transfer.id.uuidString, content: content, trigger: nil))
        }
    }

    private func uploadCandidates(_ urls: [URL]) -> [(URL, String)] {
        var result: [(URL, String)] = []
        for url in urls {
            let destination = Self.join(path, url.lastPathComponent)
            var isDirectory: ObjCBool = false
            if FileManager.default.fileExists(atPath: url.path, isDirectory: &isDirectory), isDirectory.boolValue,
               let enumerator = FileManager.default.enumerator(at: url, includingPropertiesForKeys: [.isDirectoryKey]) {
                for case let child as URL in enumerator {
                    let relative = child.path.dropFirst(url.path.count).trimmingCharacters(in: CharacterSet(charactersIn: "/"))
                    result.append((child, Self.join(destination, relative)))
                }
            } else {
                result.append((url, destination))
            }
        }
        return result
    }

    static func join(_ directory: String, _ name: String) -> String {
        directory == "/" ? "/\(name)" : directory + (directory.hasSuffix("/") ? "" : "/") + name
    }

    private static func isAllowedManualAddress(_ value: String) -> Bool {
        guard !value.isEmpty, !value.contains("/"), !value.contains("@") else { return false }
        let host: String
        if value.hasPrefix("["), let close = value.firstIndex(of: "]") {
            host = String(value[value.index(after: value.startIndex)..<close])
            let remainder = value[value.index(after: close)...]
            if !remainder.isEmpty && !(remainder.first == ":" && validPort(remainder.dropFirst())) { return false }
        } else if value.filter({ $0 == ":" }).count == 1, let colon = value.lastIndex(of: ":"), validPort(value[value.index(after: colon)...]) {
            host = String(value[..<colon])
        } else {
            host = value
        }

        if let ipv4 = ipv4Value(host) { return ipv4 & 0xffc0_0000 == 0x6440_0000 }
        if host.contains(":") { return host.lowercased().hasPrefix("fd7a:115c:a1e0:") }
        let dns = host.lowercased().trimmingCharacters(in: CharacterSet(charactersIn: "."))
        return !dns.isEmpty && (!dns.contains(".") || dns.hasSuffix(".ts.net")) && dns.allSatisfy { $0.isLetter || $0.isNumber || $0 == "-" || $0 == "." }
    }

    private static func validPort<S: StringProtocol>(_ value: S) -> Bool {
        guard let port = Int(value) else { return false }
        return (1...65_535).contains(port)
    }

    private static func ipv4Value(_ host: String) -> UInt32? {
        let parts = host.split(separator: ".", omittingEmptySubsequences: false)
        guard parts.count == 4 else { return nil }
        var result: UInt32 = 0
        for part in parts {
            guard let byte = UInt8(part), String(byte) == part else { return nil }
            result = (result << 8) | UInt32(byte)
        }
        return result
    }
}
