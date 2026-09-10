import AppKit
import Sparkle
import SwiftUI

struct SettingsView: View {
    @ObservedObject var settings: SettingsStore
    let updater: SPUUpdater?

    var body: some View {
        Form {
            Toggle("Launch Dead Drop at login", isOn: Binding(
                get: { settings.launchAtLogin },
                set: { settings.setLaunchAtLogin($0) }
            ))

            LabeledContent("Download location") {
                HStack {
                    Text(settings.downloadDirectory.path(percentEncoded: false))
                        .lineLimit(1)
                        .truncationMode(.middle)
                    Button("Choose…") { chooseDownloadDirectory() }
                }
            }

            Picker("Refresh hosts", selection: $settings.refreshInterval) {
                Text("Every 15 seconds").tag(15.0)
                Text("Every 30 seconds").tag(30.0)
                Text("Every minute").tag(60.0)
                Text("Every 5 minutes").tag(300.0)
            }

            LabeledContent("Tailscale command") {
                TextField("Find automatically", text: $settings.tailscalePath)
                    .frame(width: 260)
            }

            HStack {
                Button("Check for Updates…") { updater?.checkForUpdates() }
                    .disabled(updater == nil)
                Spacer()
                Text(updater == nil ? "Updates are available in signed releases" : "Dead Drop 0.1.0")
                    .foregroundStyle(.secondary)
            }
        }
        .formStyle(.grouped)
        .padding()
    }

    private func chooseDownloadDirectory() {
        let panel = NSOpenPanel()
        panel.canChooseFiles = false
        panel.canChooseDirectories = true
        panel.allowsMultipleSelection = false
        panel.directoryURL = settings.downloadDirectory
        if panel.runModal() == .OK, let url = panel.url {
            settings.downloadDirectory = url
        }
    }
}
