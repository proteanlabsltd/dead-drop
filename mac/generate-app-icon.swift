import AppKit

let output = URL(fileURLWithPath: CommandLine.arguments.dropFirst().first ?? "DeadDrop/Assets.xcassets/AppIcon.appiconset", isDirectory: true)
let variants: [(String, Int)] = [
    ("icon_16x16.png", 16), ("icon_16x16@2x.png", 32),
    ("icon_32x32.png", 32), ("icon_32x32@2x.png", 64),
    ("icon_128x128.png", 128), ("icon_128x128@2x.png", 256),
    ("icon_256x256.png", 256), ("icon_256x256@2x.png", 512),
    ("icon_512x512.png", 512), ("icon_512x512@2x.png", 1024)
]

func makeIcon(pixels: Int) throws -> Data {
    guard let bitmap = NSBitmapImageRep(
        bitmapDataPlanes: nil, pixelsWide: pixels, pixelsHigh: pixels,
        bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true, isPlanar: false,
        colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0
    ) else { throw CocoaError(.fileWriteUnknown) }
    NSGraphicsContext.saveGraphicsState()
    NSGraphicsContext.current = NSGraphicsContext(bitmapImageRep: bitmap)
    defer { NSGraphicsContext.restoreGraphicsState() }

    let bounds = NSRect(x: 0, y: 0, width: pixels, height: pixels)
    let inset = CGFloat(pixels) * 0.055
    let tile = NSBezierPath(roundedRect: bounds.insetBy(dx: inset, dy: inset), xRadius: CGFloat(pixels) * 0.22, yRadius: CGFloat(pixels) * 0.22)
    NSGradient(colors: [NSColor(calibratedRed: 0.31, green: 0.18, blue: 0.72, alpha: 1), NSColor(calibratedRed: 0.12, green: 0.39, blue: 0.78, alpha: 1)])!.draw(in: tile, angle: -55)

    let s = CGFloat(pixels)
    let stroke = max(1.4, s * 0.045)
    NSColor.white.setStroke()
    NSColor.white.withAlphaComponent(0.13).setFill()
    let box = NSBezierPath()
    box.move(to: NSPoint(x: s * 0.24, y: s * 0.59))
    box.line(to: NSPoint(x: s * 0.50, y: s * 0.73))
    box.line(to: NSPoint(x: s * 0.76, y: s * 0.59))
    box.line(to: NSPoint(x: s * 0.76, y: s * 0.29))
    box.line(to: NSPoint(x: s * 0.50, y: s * 0.16))
    box.line(to: NSPoint(x: s * 0.24, y: s * 0.29))
    box.close()
    box.lineWidth = stroke
    box.lineJoinStyle = .round
    box.fill(); box.stroke()

    let seams = NSBezierPath()
    seams.move(to: NSPoint(x: s * 0.24, y: s * 0.59)); seams.line(to: NSPoint(x: s * 0.50, y: s * 0.45)); seams.line(to: NSPoint(x: s * 0.76, y: s * 0.59))
    seams.move(to: NSPoint(x: s * 0.50, y: s * 0.45)); seams.line(to: NSPoint(x: s * 0.50, y: s * 0.16))
    seams.lineWidth = stroke; seams.lineJoinStyle = .round; seams.stroke()

    let arrow = NSBezierPath()
    arrow.move(to: NSPoint(x: s * 0.50, y: s * 0.87)); arrow.line(to: NSPoint(x: s * 0.50, y: s * 0.61))
    arrow.move(to: NSPoint(x: s * 0.40, y: s * 0.70)); arrow.line(to: NSPoint(x: s * 0.50, y: s * 0.60)); arrow.line(to: NSPoint(x: s * 0.60, y: s * 0.70))
    arrow.lineWidth = stroke; arrow.lineCapStyle = .round; arrow.lineJoinStyle = .round; arrow.stroke()

    guard let png = bitmap.representation(using: .png, properties: [:]) else {
        throw CocoaError(.fileWriteUnknown)
    }
    return png
}

try FileManager.default.createDirectory(at: output, withIntermediateDirectories: true)
for (name, pixels) in variants {
    try makeIcon(pixels: pixels).write(to: output.appendingPathComponent(name), options: .atomic)
}
