// axdump — Dump the macOS Accessibility tree for a given PID as JSON.
//
// Usage: axdump --pid <PID> [--max-depth 10]
// Output: JSON array of AXNode to stdout
//
// Build: swiftc main.swift -o axdump -framework ApplicationServices
//
// Requires: System Settings > Privacy & Security > Accessibility permission
// for the terminal or binary running this command.

import ApplicationServices
import Foundation

// MARK: - Data Model

struct AXNode: Codable {
    let role: String
    let label: String?
    let value: String?
    let bounds: [Double] // [x, y, w, h] normalized to screen
    let children: [AXNode]
}

// MARK: - Screen Dimensions

/// Returns (width, height) of the main display for bounds normalization.
func screenSize() -> (Double, Double) {
    let mainDisplay = CGMainDisplayID()
    let w = Double(CGDisplayPixelsWide(mainDisplay))
    let h = Double(CGDisplayPixelsHigh(mainDisplay))
    return (max(w, 1), max(h, 1))
}

// MARK: - AX Attribute Helpers

func axStringAttribute(_ element: AXUIElement, _ attribute: String) -> String? {
    var value: AnyObject?
    let result = AXUIElementCopyAttributeValue(element, attribute as CFString, &value)
    guard result == .success, let str = value as? String, !str.isEmpty else {
        return nil
    }
    return str
}

func axBounds(_ element: AXUIElement, screenW: Double, screenH: Double) -> [Double] {
    var posValue: AnyObject?
    var sizeValue: AnyObject?

    let posResult = AXUIElementCopyAttributeValue(element, kAXPositionAttribute as String as CFString, &posValue)
    let sizeResult = AXUIElementCopyAttributeValue(element, kAXSizeAttribute as String as CFString, &sizeValue)

    guard posResult == .success, sizeResult == .success else {
        return [0, 0, 0, 0]
    }

    var point = CGPoint.zero
    var size = CGSize.zero

    // AXValue wrappers for position and size
    if let posRef = posValue {
        AXValueGetValue(posRef as! AXValue, .cgPoint, &point)
    }
    if let sizeRef = sizeValue {
        AXValueGetValue(sizeRef as! AXValue, .cgSize, &size)
    }

    // Normalize to [0,1] relative to screen dimensions.
    let x = Double(point.x) / screenW
    let y = Double(point.y) / screenH
    let w = Double(size.width) / screenW
    let h = Double(size.height) / screenH

    return [
        (x * 1000).rounded() / 1000,
        (y * 1000).rounded() / 1000,
        (w * 1000).rounded() / 1000,
        (h * 1000).rounded() / 1000
    ]
}

func axChildren(_ element: AXUIElement) -> [AXUIElement] {
    var value: AnyObject?
    let result = AXUIElementCopyAttributeValue(element, kAXChildrenAttribute as String as CFString, &value)
    guard result == .success, let children = value as? [AXUIElement] else {
        return []
    }
    return children
}

// MARK: - Recursive Tree Walk

func walkElement(_ element: AXUIElement, depth: Int, maxDepth: Int, screenW: Double, screenH: Double) -> AXNode? {
    let role = axStringAttribute(element, kAXRoleAttribute as String) ?? "AXUnknown"

    // Skip system-internal roles that add noise without semantic value.
    let skipRoles: Set<String> = ["AXScrollBar", "AXGrowArea", "AXBusyIndicator"]
    if skipRoles.contains(role) {
        return nil
    }

    let label = axStringAttribute(element, kAXTitleAttribute as String)
        ?? axStringAttribute(element, kAXDescriptionAttribute as String)
    let value = axStringAttribute(element, kAXValueAttribute as String)
    let bounds = axBounds(element, screenW: screenW, screenH: screenH)

    var childNodes: [AXNode] = []
    if depth < maxDepth {
        for child in axChildren(element) {
            if let node = walkElement(child, depth: depth + 1, maxDepth: maxDepth, screenW: screenW, screenH: screenH) {
                childNodes.append(node)
            }
        }
    }

    return AXNode(role: role, label: label, value: value, bounds: bounds, children: childNodes)
}

// MARK: - CLI

func printUsage() {
    FileHandle.standardError.write("Usage: axdump --pid <PID> [--max-depth 10]\n".data(using: .utf8)!)
}

func main() {
    let args = CommandLine.arguments
    var pid: pid_t?
    var maxDepth = 10

    var i = 1
    while i < args.count {
        switch args[i] {
        case "--pid":
            i += 1
            guard i < args.count, let p = Int32(args[i]) else {
                FileHandle.standardError.write("Error: --pid requires an integer argument\n".data(using: .utf8)!)
                exit(1)
            }
            pid = p
        case "--max-depth":
            i += 1
            guard i < args.count, let d = Int(args[i]) else {
                FileHandle.standardError.write("Error: --max-depth requires an integer argument\n".data(using: .utf8)!)
                exit(1)
            }
            maxDepth = d
        case "--help", "-h":
            printUsage()
            exit(0)
        default:
            FileHandle.standardError.write("Unknown argument: \(args[i])\n".data(using: .utf8)!)
            printUsage()
            exit(1)
        }
        i += 1
    }

    guard let targetPID = pid else {
        FileHandle.standardError.write("Error: --pid is required\n".data(using: .utf8)!)
        printUsage()
        exit(1)
    }

    // Check accessibility permission.
    let trusted = AXIsProcessTrusted()
    if !trusted {
        FileHandle.standardError.write("Error: accessibility permission not granted. Enable in System Settings > Privacy & Security > Accessibility.\n".data(using: .utf8)!)
        exit(2)
    }

    let (screenW, screenH) = screenSize()
    let appElement = AXUIElementCreateApplication(targetPID)

    // Set a timeout to avoid blocking on hung apps.
    AXUIElementSetMessagingTimeout(appElement, 3.0)

    // Walk the tree. The app element is the root; extract its children.
    var roots: [AXNode] = []
    for child in axChildren(appElement) {
        if let node = walkElement(child, depth: 0, maxDepth: maxDepth, screenW: screenW, screenH: screenH) {
            roots.append(node)
        }
    }

    // Also capture the app element itself if it has useful attributes.
    if let appNode = walkElement(appElement, depth: 0, maxDepth: 0, screenW: screenW, screenH: screenH) {
        // Replace with children expanded
        let fullNode = AXNode(role: appNode.role, label: appNode.label, value: appNode.value, bounds: appNode.bounds, children: roots)
        roots = [fullNode]
    }

    // Encode as JSON and write to stdout.
    let encoder = JSONEncoder()
    encoder.outputFormatting = [.sortedKeys]
    do {
        let data = try encoder.encode(roots)
        FileHandle.standardOutput.write(data)
        FileHandle.standardOutput.write("\n".data(using: .utf8)!)
    } catch {
        FileHandle.standardError.write("Error encoding JSON: \(error)\n".data(using: .utf8)!)
        exit(3)
    }
}

main()
