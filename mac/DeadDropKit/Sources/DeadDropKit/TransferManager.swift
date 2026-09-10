import Combine
import Foundation

public enum TransferDirection: String, Codable, Sendable { case upload, download }
public enum TransferState: Equatable, Sendable {
    case queued, running, paused, completed, cancelled, failed(String)
}

public struct Transfer: Identifiable, Sendable {
    public let id: UUID
    public let direction: TransferDirection
    public let host: DiscoveredHost
    public let localURL: URL
    public let remotePath: String
    public let size: Int64
    public var transferred: Int64
    public var state: TransferState
    public let overwrite: Bool
    let sourceModificationDate: Date?
    public var progress: Double { size == 0 ? (state == .completed ? 1 : 0) : min(1, Double(transferred) / Double(size)) }
}

@MainActor
public final class TransferManager: ObservableObject {
    public typealias ClientFactory = @Sendable (DiscoveredHost) -> any HostClientProtocol
    @Published public private(set) var transfers: [Transfer] = []
    private let clientFactory: ClientFactory
    private let maxConcurrentPerHost: Int
    private let chunkSize: Int
    private var tasks: [UUID: Task<Void, Never>] = [:]
    private var generations: [UUID: Int] = [:]
    private var cleanupPending: Set<UUID> = []

    public init(maxConcurrentPerHost: Int = 3, chunkSize: Int = 4 * 1024 * 1024, clientFactory: @escaping ClientFactory = { HostClient(host: $0) }) {
        self.maxConcurrentPerHost = max(1, maxConcurrentPerHost); self.chunkSize = max(64 * 1024, chunkSize); self.clientFactory = clientFactory
    }

    public func enqueueUpload(urls: [URL], host: DiscoveredHost, remoteDirectory: String, overwrite: Bool = false) {
        for url in urls { enqueueUploadTree(url: url, host: host, remotePath: Self.join(remoteDirectory, url.lastPathComponent), overwrite: overwrite) }
        schedule()
    }

    public func enqueueDownload(entry: RemoteEntry, host: DiscoveredHost, remotePath: String, destination: URL) {
        if entry.type == .dir {
            Task { await enqueueDownloadDirectory(host: host, remotePath: remotePath, destination: destination.appendingPathComponent(entry.name, isDirectory: true)) }
        } else {
            append(direction: .download, host: host, localURL: destination.appendingPathComponent(entry.name), remotePath: remotePath, size: entry.size, overwrite: false, sourceDate: entry.mtime); schedule()
        }
    }

    public func pause(_ id: UUID) { guard let i = index(id), transfers[i].state == .running || transfers[i].state == .queued else { return }; transfers[i].state = .paused; tasks[id]?.cancel(); if tasks[id] == nil { schedule() } }
    public func resume(_ id: UUID) { guard let i = index(id), transfers[i].state == .paused else { return }; transfers[i].state = .queued; schedule() }
    public func cancel(_ id: UUID) {
        guard let i = index(id) else { return }; transfers[i].state = .cancelled; let oldTask = tasks[id]; oldTask?.cancel()
        let transfer = transfers[i]
        if transfer.direction == .upload {
            cleanupPending.insert(id)
            Task { [weak self] in _ = await oldTask?.value; await self?.cleanupCancelledUpload(id) }
        } else {
            Task { _ = await oldTask?.value; try? FileManager.default.removeItem(at: transfer.localURL.appendingPathExtension("deaddrop-part")) }
        }
        if oldTask == nil { schedule() }
    }
    public func retry(_ id: UUID) {
        guard let i = index(id), case .failed = transfers[i].state else { return }
        if cleanupPending.contains(id) { transfers[i].state = .cancelled; Task { [weak self] in await self?.cleanupCancelledUpload(id) } }
        else { transfers[i].state = .queued; schedule() }
    }

    private func cleanupCancelledUpload(_ id: UUID) async {
        guard cleanupPending.contains(id), let i = index(id) else { return }
        let transfer = transfers[i]; var lastError: Error?
        for attempt in 0..<5 {
            do {
                try await clientFactory(transfer.host).cancelPartial(path: transfer.remotePath)
                cleanupPending.remove(id)
                if let i = index(id) { transfers[i].state = .cancelled }
                return
            } catch {
                lastError = error
                if attempt < 4 { try? await Task.sleep(for: .seconds(min(30, 1 << (attempt + 1)))) }
            }
        }
        if let i = index(id) { transfers[i].state = .failed("Cancel cleanup failed: \(lastError?.localizedDescription ?? "unknown error")") }
    }

    private func enqueueUploadTree(url: URL, host: DiscoveredHost, remotePath: String, overwrite: Bool) {
        var isDirectory: ObjCBool = false
        guard FileManager.default.fileExists(atPath: url.path, isDirectory: &isDirectory) else { return }
        if isDirectory.boolValue {
            guard (try? url.resourceValues(forKeys: [.isSymbolicLinkKey]).isSymbolicLink) != true else {
                appendFailure(direction: .upload, host: host, localURL: url, remotePath: remotePath, message: "Symbolic links are not transferred."); return
            }
            Task {
                do {
                    let client = clientFactory(host)
                    do { _ = try await client.mkdir(path: remotePath) } catch HostError.exists { }
                    let children = try FileManager.default.contentsOfDirectory(at: url, includingPropertiesForKeys: nil)
                    for child in children { enqueueUploadTree(url: child, host: host, remotePath: Self.join(remotePath, child.lastPathComponent), overwrite: overwrite) }
                    schedule()
                } catch { appendFailure(direction: .upload, host: host, localURL: url, remotePath: remotePath, message: error.localizedDescription) }
            }
        } else {
            let values = try? url.resourceValues(forKeys: [.fileSizeKey, .fileAllocatedSizeKey])
            append(direction: .upload, host: host, localURL: url, remotePath: remotePath, size: Int64(values?.fileSize ?? 0), overwrite: overwrite)
        }
    }

    private func enqueueDownloadDirectory(host: DiscoveredHost, remotePath: String, destination: URL) async {
        do {
            try FileManager.default.createDirectory(at: destination, withIntermediateDirectories: true)
            let listing = try await clientFactory(host).list(path: remotePath)
            guard listing.truncated != true else { throw HostError.io("Directory listing was truncated; refusing an incomplete directory download.") }
            for entry in listing.entries where Self.isSafeComponent(entry.name) {
                let childRemote = Self.join(remotePath, entry.name)
                if entry.type == .dir { await enqueueDownloadDirectory(host: host, remotePath: childRemote, destination: destination.appendingPathComponent(entry.name, isDirectory: true)) }
                else if entry.type == .file { append(direction: .download, host: host, localURL: destination.appendingPathComponent(entry.name), remotePath: childRemote, size: entry.size, overwrite: false, sourceDate: entry.mtime) }
            }
            schedule()
        } catch { appendFailure(direction: .download, host: host, localURL: destination, remotePath: remotePath, message: error.localizedDescription) }
    }

    private func append(direction: TransferDirection, host: DiscoveredHost, localURL: URL, remotePath: String, size: Int64, overwrite: Bool, sourceDate: Date? = nil) {
        let modificationDate = direction == .upload ? (try? localURL.resourceValues(forKeys: [.contentModificationDateKey]).contentModificationDate) : sourceDate
        transfers.append(Transfer(id: UUID(), direction: direction, host: host, localURL: localURL, remotePath: remotePath, size: size, transferred: 0, state: .queued, overwrite: overwrite, sourceModificationDate: modificationDate))
    }

    private func appendFailure(direction: TransferDirection, host: DiscoveredHost, localURL: URL, remotePath: String, message: String) {
        transfers.append(Transfer(id: UUID(), direction: direction, host: host, localURL: localURL, remotePath: remotePath, size: 0, transferred: 0, state: .failed(message), overwrite: false, sourceModificationDate: nil))
    }

    private func schedule() {
        let runningByHost = Dictionary(grouping: transfers.filter { $0.state == .running }, by: { $0.host.id }).mapValues(\.count)
        var starting: [String: Int] = [:]
        for transfer in transfers where transfer.state == .queued {
            guard tasks[transfer.id] == nil else { continue }
            let active = (runningByHost[transfer.host.id] ?? 0) + (starting[transfer.host.id] ?? 0)
            guard active < maxConcurrentPerHost else { continue }
            starting[transfer.host.id, default: 0] += 1
            if let i = index(transfer.id) { transfers[i].state = .running }
            let generation = (generations[transfer.id] ?? 0) + 1
            generations[transfer.id] = generation
            tasks[transfer.id] = Task { [weak self] in await self?.run(id: transfer.id, generation: generation) }
        }
    }

    private func run(id: UUID, generation: Int) async {
        guard let i = index(id) else { return }; let transfer = transfers[i]
        do {
            if transfer.direction == .upload { try await upload(transfer, generation: generation) } else { try await download(transfer, generation: generation) }
            if generations[id] == generation, let i = index(id), transfers[i].state == .running { transfers[i].state = .completed; transfers[i].transferred = transfers[i].size }
        } catch is CancellationError { }
        catch HostError.exists where transfer.direction == .upload && !transfer.overwrite {
            if generations[id] == generation, let i = index(id), transfers[i].state == .running { transfers[i].state = .completed; transfers[i].transferred = transfers[i].size }
        }
        catch { if generations[id] == generation, let i = index(id), transfers[i].state == .running { transfers[i].state = .failed(error.localizedDescription) } }
        if generations[id] == generation { tasks[id] = nil; schedule() }
    }

    private func upload(_ transfer: Transfer, generation: Int) async throws {
        let client = clientFactory(transfer.host)
        var offset = (try? await client.partialSize(path: transfer.remotePath)) ?? 0
        guard offset <= transfer.size else { throw HostError.io("Remote partial file is larger than the local file.") }
        if offset == transfer.size { try await client.uploadChunk(Data(), path: transfer.remotePath, offset: offset, expectedSize: transfer.size, overwrite: transfer.overwrite); return }
        let handle = try FileHandle(forReadingFrom: transfer.localURL); defer { try? handle.close() }; try handle.seek(toOffset: UInt64(offset)); update(transfer.id, generation: generation, bytes: offset)
        if transfer.size == 0 { try await client.uploadChunk(Data(), path: transfer.remotePath, offset: 0, expectedSize: 0, overwrite: transfer.overwrite); return }
        var retries = 0
        while offset < transfer.size {
            try Task.checkCancellation()
            let current = try transfer.localURL.resourceValues(forKeys: [.fileSizeKey, .contentModificationDateKey])
            guard Int64(current.fileSize ?? -1) == transfer.size, current.contentModificationDate == transfer.sourceModificationDate else { throw HostError.io("The local file changed during upload.") }
            let data = try handle.read(upToCount: min(chunkSize, Int(transfer.size - offset))) ?? Data()
            guard !data.isEmpty else { throw HostError.io("Local file ended before its reported size.") }
            let chunkURL = FileManager.default.temporaryDirectory.appendingPathComponent("deaddrop-upload-\(UUID().uuidString)")
            try data.write(to: chunkURL, options: .atomic)
            defer { try? FileManager.default.removeItem(at: chunkURL) }
            do {
                let chunkOffset = offset
                try await client.uploadFile(chunkURL, path: transfer.remotePath, offset: offset, expectedSize: transfer.size, overwrite: transfer.overwrite) { [weak self] sent in
                    Task { @MainActor in self?.update(transfer.id, generation: generation, bytes: chunkOffset + sent) }
                }
                offset += Int64(data.count); retries = 0; update(transfer.id, generation: generation, bytes: offset)
            } catch HostError.exists where !transfer.overwrite {
                return
            } catch {
                retries += 1; guard retries <= 5 else { throw error }
                try await Task.sleep(for: .seconds(min(30, 1 << retries)))
                offset = try await client.partialSize(path: transfer.remotePath); try handle.seek(toOffset: UInt64(offset)); update(transfer.id, generation: generation, bytes: offset)
            }
        }
    }

    private func download(_ transfer: Transfer, generation: Int) async throws {
        let fm = FileManager.default; try fm.createDirectory(at: transfer.localURL.deletingLastPathComponent(), withIntermediateDirectories: true)
        let initial = try await clientFactory(transfer.host).stat(path: transfer.remotePath)
        guard initial.size == transfer.size, initial.mtime == transfer.sourceModificationDate else { throw HostError.io("The remote file changed before download.") }
        let partial = transfer.localURL.appendingPathExtension("deaddrop-part")
        if !fm.fileExists(atPath: partial.path) { fm.createFile(atPath: partial.path, contents: nil) }
        let handle = try FileHandle(forWritingTo: partial); defer { try? handle.close() }
        let offset = (try? handle.seekToEnd()) ?? 0; update(transfer.id, generation: generation, bytes: Int64(offset))
        var retries = 0
        while Int64(try handle.offset()) < transfer.size {
            try Task.checkCancellation()
            do {
                let revision = try await clientFactory(transfer.host).stat(path: transfer.remotePath)
                guard revision.size == transfer.size, revision.mtime == transfer.sourceModificationDate else { throw HostError.io("The remote file changed during download.") }
                let currentOffset = Int64(try handle.offset())
                let requested = min(Int64(chunkSize), transfer.size - currentOffset)
                let temp = try await clientFactory(transfer.host).download(path: transfer.remotePath, offset: currentOffset, length: requested) { [weak self] received in
                    Task { @MainActor in self?.update(transfer.id, generation: generation, bytes: currentOffset + received) }
                }
                defer { try? fm.removeItem(at: temp) }
                let returnedSize = Int64((try temp.resourceValues(forKeys: [.fileSizeKey])).fileSize ?? -1)
                guard returnedSize > 0, returnedSize <= requested else { throw HostError.invalidResponse }
                let input = try FileHandle(forReadingFrom: temp); defer { try? input.close() }
                while let data = try input.read(upToCount: chunkSize), !data.isEmpty { try Task.checkCancellation(); try handle.write(contentsOf: data); update(transfer.id, generation: generation, bytes: Int64(try handle.offset())) }
                retries = 0
            } catch is CancellationError { throw CancellationError() }
            catch { retries += 1; guard retries <= 5 else { throw error }; try await Task.sleep(for: .seconds(min(30, 1 << retries))) }
        }
        try handle.synchronize(); try handle.close()
        let final = Self.deduplicated(transfer.localURL, fileManager: fm)
        try fm.moveItem(at: partial, to: final)
    }

    private func update(_ id: UUID, generation: Int, bytes: Int64) { if generations[id] == generation, let i = index(id) { transfers[i].transferred = bytes } }
    private func index(_ id: UUID) -> Int? { transfers.firstIndex { $0.id == id } }
    private static func join(_ directory: String, _ name: String) -> String { directory.hasSuffix("/") ? directory + name : directory + "/" + name }
    private static func isSafeComponent(_ name: String) -> Bool { !name.isEmpty && name != "." && name != ".." && !name.contains("/") && !name.utf8.contains(0) }
    private static func deduplicated(_ url: URL, fileManager: FileManager) -> URL {
        guard fileManager.fileExists(atPath: url.path) else { return url }
        let ext = url.pathExtension; let stem = url.deletingPathExtension().lastPathComponent; let folder = url.deletingLastPathComponent()
        var n = 2
        while true { let name = ext.isEmpty ? "\(stem) \(n)" : "\(stem) \(n).\(ext)"; let candidate = folder.appendingPathComponent(name); if !fileManager.fileExists(atPath: candidate.path) { return candidate }; n += 1 }
    }
}
