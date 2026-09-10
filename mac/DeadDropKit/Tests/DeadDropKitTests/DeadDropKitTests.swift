import Foundation
import XCTest
@testable import DeadDropKit

final class MockURLProtocol: URLProtocol, @unchecked Sendable {
    nonisolated(unsafe) static var handler: ((URLRequest) throws -> (HTTPURLResponse, Data))!
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        do { let (response, data) = try Self.handler(request); client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed); client?.urlProtocol(self, didLoad: data); client?.urlProtocolDidFinishLoading(self) }
        catch { client?.urlProtocol(self, didFailWithError: error) }
    }
    override func stopLoading() {}
}

final class DeadDropKitTests: XCTestCase {
    private func session() -> URLSession { let config = URLSessionConfiguration.ephemeral; config.protocolClasses = [MockURLProtocol.self]; return URLSession(configuration: config) }
    private func response(_ request: URLRequest, _ status: Int = 200, headers: [String: String] = [:]) -> HTTPURLResponse { HTTPURLResponse(url: request.url!, statusCode: status, httpVersion: "HTTP/1.1", headerFields: headers)! }

    func testDiscoveryParsesLinuxPeersAndIPv6Fallback() throws {
        let json = #"{"Peer":{"a":{"HostName":"vps","DNSName":"vps.tail.ts.net.","TailscaleIPs":["100.65.2.3","fd7a::1"],"OS":"linux","Online":true},"b":{"HostName":"mac","TailscaleIPs":["100.66.3.4"],"OS":"macOS","Online":true},"c":{"HostName":"off","TailscaleIPs":["100.67.4.5"],"OS":"linux","Online":false},"d":{"HostName":"v6","TailscaleIPs":["fd7a:115c:a1e0::2"],"OS":"linux","Online":true}}}"#
        XCTAssertEqual(try TailscaleStatusParser().parse(Data(json.utf8)), [DiscoveredHost(name: "off", address: "100.67.4.5", status: .offline), DiscoveredHost(name: "v6", address: "fd7a:115c:a1e0::2", status: .daemonUnavailable), DiscoveredHost(name: "vps", address: "100.65.2.3", status: .daemonUnavailable)])
    }

    func testDiscoveryUsesDNSNameAndKeepsOfflineLinuxPeers() throws {
        let json = #"{"BackendState":"Running","Peer":{"a":{"HostName":"ubuntu-8gb-hel1-3","DNSName":"athena.tail.ts.net.","TailscaleIPs":["100.75.180.128"],"OS":"linux","Online":true},"b":{"HostName":"pihole","DNSName":"pihole.tail.ts.net.","TailscaleIPs":["100.123.187.79"],"OS":"linux","Online":false}}}"#
        let hosts = try TailscaleStatusParser().parse(Data(json.utf8))
        XCTAssertEqual(hosts.map(\.name), ["athena", "pihole"])
        XCTAssertEqual(hosts.map(\.status), [.daemonUnavailable, .offline])
    }

    func testDiscoveryRejectsStoppedBackend() {
        XCTAssertThrowsError(try TailscaleStatusParser().parse(Data(#"{"BackendState":"Stopped","Peer":{}}"#.utf8)))
    }

    func testProbePreservesFriendlyNameAndPriorInfoAcrossUnavailableAndReconnect() async {
        let priorInfo = HostInfo(name: "daemon-internal", version: "0.1", protocol: 1, roots: ["/srv"], user: "me")
        let host = DiscoveredHost(name: "athena", address: "100.75.180.128", status: .daemonUnavailable, info: priorInfo)
        MockURLProtocol.handler = { _ in throw URLError(.cannotConnectToHost) }
        let unavailable = await TailscaleDiscovery.probe(host, session: session())
        XCTAssertEqual(unavailable.name, "athena")
        XCTAssertEqual(unavailable.status, .daemonUnavailable)
        XCTAssertEqual(unavailable.info, priorInfo)

        MockURLProtocol.handler = { request in
            (self.response(request), Data(#"{"name":"daemon-overwrite-attempt","version":"0.1","protocol":1,"roots":["/srv"],"user":"me"}"#.utf8))
        }
        let reconnected = await TailscaleDiscovery.probe(unavailable, session: session())
        XCTAssertEqual(reconnected.name, "athena")
        XCTAssertEqual(reconnected.status, .reachable)
        XCTAssertEqual(reconnected.info?.name, "daemon-overwrite-attempt")
    }

    @MainActor func testOlderRefreshCannotOverwriteNewerResults() async throws {
        let statuses = DiscoveryStatusSequence()
        let discovery = TailscaleDiscovery(statusLoader: { try await statuses.next() }, hostProbe: { host in
            var host = host; host.status = .reachable; return host
        })
        let first = Task { await discovery.refresh() }
        try await Task.sleep(for: .milliseconds(20))
        await discovery.refresh()
        await first.value
        XCTAssertEqual(discovery.hosts.map(\.name), ["hestia"])
    }

    @MainActor func testOlderFailedRefreshCannotOverwriteNewerReadyState() async throws {
        let statuses = FailingDiscoveryStatusSequence()
        let discovery = TailscaleDiscovery(statusLoader: { try await statuses.next() }, hostProbe: { host in
            var host = host; host.status = .reachable; return host
        })
        let first = Task { await discovery.refresh() }
        try await Task.sleep(for: .milliseconds(20))
        await discovery.refresh()
        await first.value
        XCTAssertEqual(discovery.state, .ready)
        XCTAssertEqual(discovery.hosts.map(\.name), ["hestia"])
    }

    @MainActor func testTailscaleCandidateWinsOverDuplicateManualHost() async {
        let status = Data(#"{"BackendState":"Running","Peer":{"x":{"HostName":"raw","DNSName":"athena.tail.ts.net.","TailscaleIPs":["100.75.180.128"],"OS":"linux","Online":false}}}"#.utf8)
        let discovery = TailscaleDiscovery(statusLoader: { status }, hostProbe: { $0 })
        discovery.addManualHost("100.75.180.128", name: "Manual label")
        await discovery.refresh()
        for _ in 0..<20 where discovery.hosts.isEmpty { try? await Task.sleep(for: .milliseconds(10)) }
        XCTAssertEqual(discovery.hosts.count, 1)
        XCTAssertEqual(discovery.hosts.first?.name, "athena")
        XCTAssertEqual(discovery.hosts.first?.status, .offline)
    }

    func testHostClientDecodesListingAndEscapesPath() async throws {
        MockURLProtocol.handler = { request in
            XCTAssertEqual(request.url?.path, "/v1/ls"); XCTAssertEqual(URLComponents(url: request.url!, resolvingAgainstBaseURL: false)?.queryItems?.first?.value, "/home/me/a b")
            return (self.response(request), Data(#"{"path":"/home/me/a b","entries":[{"name":"x","type":"file","size":4,"mtime":"2026-09-10T18:02:11Z","mode":"-rw-r--r--"}]}"#.utf8))
        }
        let listing = try await HostClient(baseURL: URL(string: "http://100.64.0.1:7477")!, session: session()).list(path: "/home/me/a b")
        XCTAssertEqual(listing.entries.first?.name, "x"); XCTAssertEqual(listing.entries.first?.size, 4)
    }

    func testHostClientMapsAllProtocolErrors() async {
        let cases: [(String, HostError)] = [("not_found", .notFound("no")), ("forbidden", .forbidden("no")), ("exists", .exists("no")), ("invalid_path", .invalidPath("no")), ("io", .io("no")), ("bad_request", .badRequest("no"))]
        for (code, expected) in cases {
            MockURLProtocol.handler = { request in (self.response(request, 400), Data("{\"error\":{\"code\":\"\(code)\",\"message\":\"no\"}}".utf8)) }
            do { _ = try await HostClient(baseURL: URL(string: "http://100.64.0.1")!, session: session()).stat(path: "/x"); XCTFail("Expected \(code)") }
            catch let error as HostError { XCTAssertEqual(error, expected) }
            catch { XCTFail("Unexpected \(error)") }
        }
    }

    func testPartialSizeAndUploadHeaders() async throws {
        var sawUpload = false
        MockURLProtocol.handler = { request in
            if request.httpMethod == "HEAD" { return (self.response(request, headers: ["X-Partial-Size": "7"]), Data()) }
            sawUpload = true; XCTAssertEqual(request.value(forHTTPHeaderField: "X-Expected-Size"), "10")
            XCTAssertEqual(URLComponents(url: request.url!, resolvingAgainstBaseURL: false)?.queryItems?.first(where: { $0.name == "offset" })?.value, "7")
            return (self.response(request), Data())
        }
        let client = HostClient(baseURL: URL(string: "http://100.64.0.1")!, session: session())
        let partialSize = try await client.partialSize(path: "/x")
        XCTAssertEqual(partialSize, 7)
        try await client.uploadChunk(Data([1, 2, 3]), path: "/x", offset: 7, expectedSize: 10, overwrite: true)
        XCTAssertTrue(sawUpload)
    }

    func testAddressMayIncludePortAndExplicitPortWins() async {
        let included = await HostClient(address: "100.108.183.16:17477").baseURL
        let overridden = await HostClient(address: "[fd7a::1]:17477", port: 7477).baseURL
        XCTAssertEqual(included.absoluteString, "http://100.108.183.16:17477")
        XCTAssertEqual(overridden.absoluteString, "http://[fd7a::1]:7477")
    }

    func testInvalidAddressReturnsTypedErrorWithoutCrashing() async {
        do { _ = try await HostClient(address: "[").info(); XCTFail("Expected invalid address") }
        catch let error as HostError {
            guard case .invalidPath = error else { return XCTFail("Unexpected error: \(error)") }
        } catch { XCTFail("Unexpected error: \(error)") }
    }

    func testOnlyTailnetAndLoopbackAddressesAreAllowed() async {
        XCTAssertTrue(HostClient.isAllowedTailnetAddress("100.64.0.1"))
        XCTAssertTrue(HostClient.isAllowedTailnetAddress("100.127.255.254"))
        XCTAssertFalse(HostClient.isAllowedTailnetAddress("100.128.0.1"))
        XCTAssertFalse(HostClient.isAllowedTailnetAddress("8.8.8.8"))
        XCTAssertTrue(HostClient.isAllowedTailnetAddress("fd7a:115c:a1e0::1"))
        XCTAssertFalse(HostClient.isAllowedTailnetAddress("fd7a:115c:a1e1::1"))
        XCTAssertTrue(HostClient.isAllowedTailnetAddress("127.0.0.1"))
        XCTAssertTrue(HostClient.isAllowedTailnetAddress("::1"))
        do { _ = try await HostClient(address: "8.8.8.8").info(); XCTFail("Expected public IP rejection") }
        catch let error as HostError { guard case .invalidPath = error else { return XCTFail("Unexpected \(error)") } }
        catch { XCTFail("Unexpected \(error)") }
    }

    @MainActor func testTransferManagerResumesUploadFromPartialSize() async throws {
        let bytes = Data(repeating: 42, count: 70_000); let local = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try bytes.write(to: local); defer { try? FileManager.default.removeItem(at: local) }
        let mock = TransferClientMock()
        let host = DiscoveredHost(name: "vps", address: "100.1.2.3", status: .reachable)
        let manager = TransferManager(chunkSize: 65_536, clientFactory: { _ in mock })
        manager.enqueueUpload(urls: [local], host: host, remoteDirectory: "/srv")
        for _ in 0..<100 { if manager.transfers.first?.state == .completed { break }; try await Task.sleep(for: .milliseconds(20)) }
        let offsets = await mock.offsets
        XCTAssertEqual(manager.transfers.first?.state, .completed); XCTAssertEqual(offsets, [1000, 66_536]); XCTAssertEqual(manager.transfers.first?.transferred, 70_000)
    }

    @MainActor func testTransferQueueLimitsConcurrencyAndCancels() async throws {
        let folder = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
        let files = (0..<4).map { folder.appendingPathComponent("\($0).bin") }
        for file in files { try Data(repeating: 1, count: 70_000).write(to: file) }
        defer { try? FileManager.default.removeItem(at: folder) }
        let mock = QueueClientMock(); let host = DiscoveredHost(name: "vps", address: "100.1.2.3")
        let manager = TransferManager(maxConcurrentPerHost: 2, chunkSize: 70_000, clientFactory: { _ in mock })
        manager.enqueueUpload(urls: files, host: host, remoteDirectory: "/srv")
        try await Task.sleep(for: .milliseconds(20))
        XCTAssertEqual(manager.transfers.filter { $0.state == .running }.count, 2)
        let cancelledID = manager.transfers.first { $0.state == .running }!.id
        manager.cancel(cancelledID)
        for _ in 0..<100 { if manager.transfers.filter({ $0.state == .completed }).count == 3 { break }; try await Task.sleep(for: .milliseconds(20)) }
        XCTAssertEqual(manager.transfers.first { $0.id == cancelledID }?.state, .cancelled)
        XCTAssertEqual(manager.transfers.filter { $0.state == .completed }.count, 3)
        let maximumConcurrent = await mock.maximumConcurrent
        XCTAssertLessThanOrEqual(maximumConcurrent, 2)
    }
}

private actor DiscoveryStatusSequence {
    private var call = 0
    func next() async throws -> Data {
        call += 1
        let name: String
        if call == 1 { try await Task.sleep(for: .milliseconds(100)); name = "athena" } else { name = "hestia" }
        return Data("{\"BackendState\":\"Running\",\"Peer\":{\"x\":{\"HostName\":\"raw\",\"DNSName\":\"\(name).tail.ts.net.\",\"TailscaleIPs\":[\"100.75.180.128\"],\"OS\":\"linux\",\"Online\":true}}}".utf8)
    }
}

private actor FailingDiscoveryStatusSequence {
    private var call = 0
    func next() async throws -> Data {
        call += 1
        if call == 1 { try await Task.sleep(for: .milliseconds(100)); throw HostError.transport("old failure") }
        return Data(#"{"BackendState":"Running","Peer":{"x":{"HostName":"raw","DNSName":"hestia.tail.ts.net.","TailscaleIPs":["100.75.180.128"],"OS":"linux","Online":true}}}"#.utf8)
    }
}

private actor TransferClientMock: HostClientProtocol {
    private(set) var offsets: [Int64] = []
    func list(path: String) async throws -> DirectoryListing { DirectoryListing(path: path, entries: []) }
    func stat(path: String) async throws -> RemoteEntry { RemoteEntry(name: path, type: .file, size: 0, mtime: .now, mode: "-rw-r--r--") }
    func mkdir(path: String) async throws -> RemoteEntry { RemoteEntry(name: path, type: .dir, size: 0, mtime: .now, mode: "drwxr-xr-x") }
    func partialSize(path: String) async throws -> Int64 { 1000 }
    func uploadChunk(_ data: Data, path: String, offset: Int64, expectedSize: Int64, overwrite: Bool) async throws { offsets.append(offset) }
    func download(path: String, offset: Int64) async throws -> URL { throw HostError.notFound(path) }
    func cancelPartial(path: String) async throws {}
}

private actor QueueClientMock: HostClientProtocol {
    private var current = 0
    private(set) var maximumConcurrent = 0
    func list(path: String) async throws -> DirectoryListing { DirectoryListing(path: path, entries: []) }
    func stat(path: String) async throws -> RemoteEntry { RemoteEntry(name: path, type: .file, size: 0, mtime: .now, mode: "-rw-r--r--") }
    func mkdir(path: String) async throws -> RemoteEntry { RemoteEntry(name: path, type: .dir, size: 0, mtime: .now, mode: "drwxr-xr-x") }
    func partialSize(path: String) async throws -> Int64 { 0 }
    func uploadChunk(_ data: Data, path: String, offset: Int64, expectedSize: Int64, overwrite: Bool) async throws {
        current += 1; maximumConcurrent = max(maximumConcurrent, current)
        defer { current -= 1 }
        try await Task.sleep(for: .milliseconds(80))
    }
    func download(path: String, offset: Int64) async throws -> URL { throw HostError.notFound(path) }
    func cancelPartial(path: String) async throws {}
}
