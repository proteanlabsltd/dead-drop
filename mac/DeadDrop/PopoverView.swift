import AppKit
import DeadDropKit
import UniformTypeIdentifiers
import SwiftUI

struct PopoverView: View {
    @ObservedObject var model: AppModel
    @ObservedObject var settings: SettingsStore
    @Environment(\.openSettings) private var openSettings
    @State private var manualHost = ""
    @State private var showManualHost = false
    @State private var showNewFolder = false
    @State private var newFolderName = ""
    @State private var dropTargeted = false

    var body: some View {
        VStack(spacing: 0) {
            header
            Divider()
            content
            Divider()
            footer
        }
        .background(dropTargeted ? Color.accentColor.opacity(0.08) : Color.clear)
        .dropDestination(for: URL.self) { urls, _ in model.prepareUpload(urls); return true } isTargeted: { dropTargeted = $0 }
        .alert("Add host manually", isPresented: $showManualHost) {
            TextField("MagicDNS name or IP", text: $manualHost)
            Button("Add") { if model.addManualHost(manualHost) { manualHost = "" } }
            Button("Cancel", role: .cancel) {}
        } message: { Text("Enter a host running deaddrop on port 7477.") }
        .alert("New folder", isPresented: $showNewFolder) {
            TextField("Folder name", text: $newFolderName)
            Button("Create") { let name = newFolderName; newFolderName = ""; Task { await model.createFolder(named: name) } }
            Button("Cancel", role: .cancel) {}
        }
        .confirmationDialog("Files already at the destination", isPresented: $model.showOverwritePrompt) {
            Button("Replace matching files") { model.finishUpload(overwrite: true) }
            Button("Skip matching files") { model.finishUpload(overwrite: false) }
            Button("Cancel", role: .cancel) { model.pendingUploads = [] }
        } message: { Text("Choose how Dead Drop should handle names that already exist.") }
        .task {
            for host in settings.manualHosts { model.discovery.addManualHost(host) }
            await model.refreshHosts()
        }
    }

    private var header: some View {
        VStack(spacing: 8) {
            HStack {
                Menu {
                    ForEach(model.discovery.hosts) { host in
                        Button { model.select(host) } label: { Label(host.name, systemImage: host.reachable ? "circle.fill" : "circle") }
                    }
                    Divider()
                    Button("Add host manually…") { showManualHost = true }
                    Button("Refresh") { Task { await model.refreshHosts() } }
                } label: {
                    HStack(spacing: 6) {
                        Circle().fill(hostColor(model.selectedHost)).frame(width: 7, height: 7)
                        Text(model.selectedHost?.name ?? "Choose a host").lineLimit(1)
                        Image(systemName: "chevron.down").font(.caption2)
                    }.padding(.horizontal, 10).padding(.vertical, 5).background(.quaternary, in: Capsule())
                }.menuStyle(.borderlessButton)
                Spacer()
                Menu {
                    Button("Upload files…") { chooseUploads() }
                    Button("New folder") { showNewFolder = true }
                } label: { Image(systemName: "plus").frame(width: 24, height: 24) }
                    .disabled(model.selectedHost?.reachable != true)
            }
            HStack {
                Menu {
                    if let roots = model.selectedHost?.info?.roots { ForEach(roots, id: \.self) { root in Button(root) { model.go(to: root) } } }
                    if !model.parentPaths.isEmpty {
                        Divider()
                        ForEach(model.parentPaths, id: \.self) { parent in
                            Button(parent) { model.go(to: parent) }
                        }
                        Divider()
                        Button("Go Up") { model.goUp() }.keyboardShortcut(.upArrow, modifiers: .command)
                    }
                } label: {
                    Label(model.path.replacingOccurrences(of: FileManager.default.homeDirectoryForCurrentUser.path, with: "~"), systemImage: "folder")
                        .lineLimit(1).truncationMode(.middle)
                }.menuStyle(.borderlessButton)
                Spacer()
                if model.isLoading { ProgressView().controlSize(.small) }
            }
            TextField("Filter files", text: $model.filter).textFieldStyle(.roundedBorder)
        }.padding(12)
    }

    @ViewBuilder private var content: some View {
        if model.showTransfers { TransfersView(manager: model.transfers) }
        else if case .tailscaleUnavailable = model.discovery.state { tailscaleUnavailable }
        else if let error = model.error { errorView(error) }
        else if model.discovery.hosts.isEmpty && !model.isLoading { noHosts }
        else if model.selectedHost?.reachable == false { empty("Host offline", "Dead Drop will reconnect automatically.", "wifi.slash") }
        else if model.visibleEntries.isEmpty && !model.isLoading { empty(model.filter.isEmpty ? "This folder is empty" : "No matching files", model.filter.isEmpty ? "Drop files here to upload them." : "Try another filter.", "folder") }
        else {
            List(model.visibleEntries) { entry in EntryRow(entry: entry, model: model, settings: settings) }
                .listStyle(.plain)
        }
    }

    private var footer: some View {
        HStack {
            Button { model.showTransfers.toggle() } label: {
                let active = model.transfers.transfers.filter { $0.state == .running || $0.state == .queued }.count
                Label(active == 0 ? "Transfers" : "\(active) transfer\(active == 1 ? "" : "s")", systemImage: "arrow.up.arrow.down")
            }.buttonStyle(.plain)
            Spacer()
            Button { openSettings() } label: { Image(systemName: "gearshape") }.buttonStyle(.plain)
        }.padding(.horizontal, 12).frame(height: 36)
    }

    private var tailscaleUnavailable: some View {
        VStack(spacing: 12) {
            empty("Tailscale not running", model.discovery.error ?? "Dead Drop could not find Tailscale.", "network.slash")
            Button("Open Tailscale") { NSWorkspace.shared.open(URL(fileURLWithPath: "/Applications/Tailscale.app")) }
            Button("Add host manually…") { showManualHost = true }.buttonStyle(.link)
        }.frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    private var noHosts: some View {
        VStack(spacing: 12) {
            empty("No Dead Drop hosts", "Install deaddrop on a Linux host in your tailnet.", "shippingbox")
            Text("curl -fsSL https://github.com/protean-labs/dead-drop/releases/latest/download/install.sh | sh")
                .font(.caption.monospaced()).textSelection(.enabled).padding(8).background(.quaternary, in: RoundedRectangle(cornerRadius: 6)).padding(.horizontal)
            Button("Add host manually…") { showManualHost = true }.buttonStyle(.link)
        }.frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    private func errorView(_ error: AppModel.ErrorState) -> some View {
        empty(error.kind == .forbidden ? "Not allowed" : "Couldn’t load this folder", error.message, error.kind == .forbidden ? "lock" : "exclamationmark.triangle")
    }

    private func empty(_ title: String, _ detail: String, _ icon: String) -> some View {
        ContentUnavailableView(title, systemImage: icon, description: Text(detail))
    }

    private func chooseUploads() {
        let panel = NSOpenPanel(); panel.canChooseFiles = true; panel.canChooseDirectories = true; panel.allowsMultipleSelection = true
        if panel.runModal() == .OK { model.prepareUpload(panel.urls) }
    }

    private func hostColor(_ host: DiscoveredHost?) -> Color {
        switch host?.status {
        case .reachable: .green
        case .forbidden: .orange
        case .offline, .none: .gray
        }
    }
}

private struct EntryRow: View {
    let entry: RemoteEntry
    @ObservedObject var model: AppModel
    @ObservedObject var settings: SettingsStore

    var body: some View {
        HStack(spacing: 10) {
            Image(systemName: entry.type == .dir ? "folder.fill" : "doc")
                .foregroundColor(entry.type == .dir ? .accentColor : .secondary)
                .frame(width: 20)
            VStack(alignment: .leading, spacing: 2) {
                Text(entry.name).lineLimit(1)
                Text(entry.type == .dir ? entry.mtime.formatted(date: .abbreviated, time: .shortened) : "\(entry.size.formatted(.byteCount(style: .file))) · \(entry.mtime.formatted(date: .abbreviated, time: .omitted))")
                    .font(.caption).foregroundStyle(.secondary)
            }
            Spacer()
            if entry.type == .dir { Image(systemName: "chevron.right").font(.caption).foregroundStyle(.tertiary) }
        }
        .contentShape(Rectangle()).onTapGesture { model.open(entry) }
        .contextMenu {
            Button("Download") { model.download(entry, to: settings.downloadDirectory) }
            Button("Download to…") { chooseDestination() }
            Divider()
            Button("Copy Path") { NSPasteboard.general.clearContents(); NSPasteboard.general.setString(AppModel.join(model.path, entry.name), forType: .string) }
            Button("Copy scp Command") { copySCP() }
        }
    }

    private func chooseDestination() {
        let panel = NSOpenPanel(); panel.canChooseFiles = false; panel.canChooseDirectories = true
        if panel.runModal() == .OK, let url = panel.url { model.download(entry, to: url) }
    }

    private func copySCP() {
        guard let host = model.selectedHost else { return }
        let remote = AppModel.join(model.path, entry.name).replacingOccurrences(of: "'", with: "'\\''")
        NSPasteboard.general.clearContents(); NSPasteboard.general.setString("scp '\(host.address):\(remote)' .", forType: .string)
    }
}
