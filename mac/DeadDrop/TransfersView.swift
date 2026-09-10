import DeadDropKit
import SwiftUI

struct TransfersView: View {
    @ObservedObject var manager: TransferManager

    var body: some View {
        if manager.transfers.isEmpty {
            ContentUnavailableView("No transfers", systemImage: "arrow.up.arrow.down", description: Text("Uploads and downloads appear here."))
        } else {
            List(manager.transfers.reversed()) { transfer in
                VStack(alignment: .leading, spacing: 6) {
                    HStack {
                        Image(systemName: transfer.direction == .upload ? "arrow.up" : "arrow.down")
                        Text(transfer.localURL.lastPathComponent).lineLimit(1)
                        Spacer()
                        stateLabel(transfer.state).font(.caption).foregroundStyle(.secondary)
                    }
                    ProgressView(value: transfer.progress)
                    HStack {
                        Text(transfer.host.name).font(.caption).foregroundStyle(.secondary)
                        Spacer()
                        controls(transfer)
                    }
                }.padding(.vertical, 3)
            }.listStyle(.plain)
        }
    }

    @ViewBuilder private func controls(_ transfer: Transfer) -> some View {
        switch transfer.state {
        case .running, .queued:
            Button { manager.pause(transfer.id) } label: { Image(systemName: "pause.fill") }.buttonStyle(.borderless)
            Button { manager.cancel(transfer.id) } label: { Image(systemName: "xmark") }.buttonStyle(.borderless)
        case .paused:
            Button { manager.resume(transfer.id) } label: { Image(systemName: "play.fill") }.buttonStyle(.borderless)
            Button { manager.cancel(transfer.id) } label: { Image(systemName: "xmark") }.buttonStyle(.borderless)
        case .failed:
            Button("Retry") { manager.retry(transfer.id) }.buttonStyle(.borderless)
        case .completed, .cancelled:
            EmptyView()
        }
    }

    private func stateLabel(_ state: TransferState) -> Text {
        switch state {
        case .queued: Text("Waiting")
        case .running: Text("Transferring")
        case .paused: Text("Paused")
        case .completed: Text("Done")
        case .cancelled: Text("Cancelled")
        case .failed(let message): Text(message)
        }
    }
}
