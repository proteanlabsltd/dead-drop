import Foundation
import XCTest
@testable import DeadDropKit

final class IntegrationTests: XCTestCase {
    @MainActor
    func testRealTailscaleDiscovery() async throws {
        guard let expectedAddress = ProcessInfo.processInfo.environment["DEADDROP_DISCOVERY_HOST"] else {
            throw XCTSkip("Set DEADDROP_DISCOVERY_HOST to run discovery integration tests.")
        }
        let discovery = TailscaleDiscovery()
        let clock = ContinuousClock(); let start = clock.now
        await discovery.refresh()
        let elapsed = start.duration(to: clock.now)
        guard let host = discovery.hosts.first(where: { $0.address == expectedAddress }) else {
            return XCTFail("Expected \(expectedAddress), got \(discovery.hosts.map { "\($0.address):\($0.status.rawValue)" }) state=\(discovery.state)")
        }
        XCTAssertEqual(host.status, .reachable)
        XCTAssertEqual(host.info?.protocol, 1)
        XCTAssertLessThan(elapsed, .seconds(30))
        print("Discovered \(host.info?.name ?? host.name) at \(host.address) in \(elapsed)")
    }

    @MainActor
    func testRealDaemonRoundTrip() async throws {
        let environment = ProcessInfo.processInfo.environment
        guard let address = environment["DEADDROP_TEST_HOST"], let root = environment["DEADDROP_TEST_ROOT"] else {
            throw XCTSkip("Set DEADDROP_TEST_HOST and DEADDROP_TEST_ROOT to run daemon integration tests.")
        }

        let host = DiscoveredHost(name: "integration", address: address, status: .reachable)
        let client = HostClient(host: host)
        let info = try await client.info()
        XCTAssertEqual(info.protocol, 1)
        _ = try await client.list(path: root)

        let runName = "kit-\(UUID().uuidString.lowercased())"
        let remoteDirectory = root + "/" + runName
        _ = try await client.mkdir(path: remoteDirectory)

        let payload = Data((0..<(1024 * 1024)).map { UInt8(truncatingIfNeeded: $0 &* 31) })
        let directPath = remoteDirectory + "/direct.bin"
        for offset in stride(from: 0, to: payload.count, by: 128 * 1024) {
            let end = min(payload.count, offset + 128 * 1024)
            try await client.uploadChunk(payload[offset..<end], path: directPath, offset: Int64(offset), expectedSize: Int64(payload.count), overwrite: false)
        }
        let directStat = try await client.stat(path: directPath)
        XCTAssertEqual(directStat.size, Int64(payload.count))
        let directDownload = try await client.download(path: directPath, offset: 0)
        defer { try? FileManager.default.removeItem(at: directDownload) }
        XCTAssertEqual(try Data(contentsOf: directDownload), payload)

        let cancelledPath = remoteDirectory + "/cancelled.bin"
        try await client.uploadChunk(payload.prefix(64 * 1024), path: cancelledPath, offset: 0, expectedSize: Int64(payload.count), overwrite: false)
        let partialBeforeCancel = try await client.partialSize(path: cancelledPath)
        XCTAssertEqual(partialBeforeCancel, 64 * 1024)
        try await client.cancelPartial(path: cancelledPath)
        let partialAfterCancel = try await client.partialSize(path: cancelledPath)
        XCTAssertEqual(partialAfterCancel, 0)

        let localDirectory = FileManager.default.temporaryDirectory.appendingPathComponent(runName, isDirectory: true)
        try FileManager.default.createDirectory(at: localDirectory, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: localDirectory) }
        let uploadURL = localDirectory.appendingPathComponent("managed.bin")
        try payload.write(to: uploadURL)
        let manager = TransferManager(clientFactory: { _ in client })
        manager.enqueueUpload(urls: [uploadURL], host: host, remoteDirectory: remoteDirectory)
        try await waitForTransfer(manager, direction: .upload)
        XCTAssertEqual(manager.transfers.last?.state, .completed)

        let managedPath = remoteDirectory + "/managed.bin"
        let managedEntry = try await client.stat(path: managedPath)
        let downloadDirectory = localDirectory.appendingPathComponent("downloads", isDirectory: true)
        manager.enqueueDownload(entry: managedEntry, host: host, remotePath: managedPath, destination: downloadDirectory)
        try await waitForTransfer(manager, direction: .download)
        XCTAssertEqual(manager.transfers.last?.state, .completed)
        XCTAssertEqual(try Data(contentsOf: downloadDirectory.appendingPathComponent("managed.bin")), payload)
        let finalListing = try await client.list(path: remoteDirectory)
        XCTAssertTrue(finalListing.entries.contains { $0.name == "managed.bin" })
    }

    @MainActor
    private func waitForTransfer(_ manager: TransferManager, direction: TransferDirection) async throws {
        let deadline = ContinuousClock.now + .seconds(60)
        while ContinuousClock.now < deadline {
            if let state = manager.transfers.last(where: { $0.direction == direction })?.state {
                switch state {
                case .completed: return
                case let .failed(message): XCTFail(message); return
                case .cancelled: XCTFail("Transfer was cancelled"); return
                default: break
                }
            }
            try await Task.sleep(for: .milliseconds(100))
        }
        XCTFail("Timed out waiting for \(direction.rawValue)")
    }
}
