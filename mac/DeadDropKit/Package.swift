// swift-tools-version: 6.0
import PackageDescription

let package = Package(
    name: "DeadDropKit",
    platforms: [.macOS(.v14)],
    products: [.library(name: "DeadDropKit", targets: ["DeadDropKit"])],
    targets: [
        .target(name: "DeadDropKit"),
        .testTarget(name: "DeadDropKitTests", dependencies: ["DeadDropKit"]),
    ]
)
