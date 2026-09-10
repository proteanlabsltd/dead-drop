import Foundation
import ServiceManagement

@MainActor
final class SettingsStore: ObservableObject {
    @Published var downloadDirectory: URL {
        didSet { defaults.set(downloadDirectory.path, forKey: Keys.downloadDirectory) }
    }
    @Published var refreshInterval: Double {
        didSet { defaults.set(refreshInterval, forKey: Keys.refreshInterval) }
    }
    @Published var tailscalePath: String {
        didSet { defaults.set(tailscalePath, forKey: Keys.tailscalePath) }
    }
    @Published var launchAtLogin: Bool = false

    private let defaults: UserDefaults
    private enum Keys {
        static let downloadDirectory = "downloadDirectory"
        static let refreshInterval = "refreshInterval"
        static let tailscalePath = "tailscalePath"
        static let selectedHost = "selectedHost"
        static let manualHosts = "manualHosts"
    }

    init(defaults: UserDefaults = .standard) {
        self.defaults = defaults
        downloadDirectory = URL(fileURLWithPath: defaults.string(forKey: Keys.downloadDirectory) ?? FileManager.default.urls(for: .downloadsDirectory, in: .userDomainMask)[0].path)
        let savedInterval = defaults.double(forKey: Keys.refreshInterval)
        refreshInterval = savedInterval == 0 ? 30 : savedInterval
        tailscalePath = defaults.string(forKey: Keys.tailscalePath) ?? ""
        launchAtLogin = SMAppService.mainApp.status == .enabled
    }

    func setLaunchAtLogin(_ enabled: Bool) {
        do {
            if enabled { try SMAppService.mainApp.register() } else { try SMAppService.mainApp.unregister() }
            launchAtLogin = enabled
        } catch {
            launchAtLogin = SMAppService.mainApp.status == .enabled
        }
    }

    var selectedHostID: String? {
        get { defaults.string(forKey: Keys.selectedHost) }
        set { defaults.set(newValue, forKey: Keys.selectedHost) }
    }

    var manualHosts: [String] {
        get { defaults.stringArray(forKey: Keys.manualHosts) ?? [] }
        set { defaults.set(newValue, forKey: Keys.manualHosts) }
    }
}
