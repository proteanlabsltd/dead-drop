import Sparkle
import SwiftUI

@main
struct DeadDropApp: App {
    @StateObject private var settings: SettingsStore
    @StateObject private var model: AppModel
    private let updater: SPUStandardUpdaterController?

    init() {
        let settings = SettingsStore()
        _settings = StateObject(wrappedValue: settings)
        _model = StateObject(wrappedValue: AppModel(settings: settings))
        let key = Bundle.main.object(forInfoDictionaryKey: "SUPublicEDKey") as? String
        if let key, !key.isEmpty, !key.contains("$(") {
            updater = SPUStandardUpdaterController(startingUpdater: true, updaterDelegate: nil, userDriverDelegate: nil)
        } else {
            updater = nil
        }
    }

    var body: some Scene {
        MenuBarExtra("Dead Drop", systemImage: "shippingbox") {
            PopoverView(model: model, settings: settings)
                .frame(width: 360, height: 480)
        }
        .menuBarExtraStyle(.window)

        Settings {
            SettingsView(settings: settings, updater: updater?.updater)
                .frame(width: 480)
        }
    }
}
