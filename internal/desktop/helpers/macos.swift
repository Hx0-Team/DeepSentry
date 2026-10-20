import AppKit
import ApplicationServices
import Foundation
import ImageIO
import UniformTypeIdentifiers

func fail(_ message: String) -> Never {
    FileHandle.standardError.write(Data(message.utf8)); exit(1)
}
let input = FileHandle.standardInput.readDataToEndOfFile()
guard let r = try? JSONSerialization.jsonObject(with: input) as? [String: Any], let action = r["action"] as? String else { fail("Invalid helper request") }
let display = CGMainDisplayID()
let bounds = CGDisplayBounds(display)
func number(_ name: String) -> Int { (r[name] as? NSNumber)?.intValue ?? 0 }
func frontApp() -> NSRunningApplication? { NSWorkspace.shared.frontmostApplication }
func appID() -> String {
    guard let app = frontApp() else { return "" }
    if let id = app.bundleIdentifier, !id.isEmpty { return "\(id):pid:\(app.processIdentifier)" }
    return "pid:\(app.processIdentifier)"
}
func foreground() -> String {
    guard let app = frontApp() else { return "" }
    let element = AXUIElementCreateApplication(app.processIdentifier)
    var window: CFTypeRef?
    var title: CFTypeRef?
    if AXUIElementCopyAttributeValue(element, kAXFocusedWindowAttribute as CFString, &window) == .success, let window {
        _ = AXUIElementCopyAttributeValue(window as! AXUIElement, kAXTitleAttribute as CFString, &title)
    }
    return "\(app.processIdentifier):\(title as? String ?? "")"
}
// Window numbers do not need Screen Recording permission and stay stable
// while a title changes, so they tell a new window of the same app apart.
func windowID() -> String {
    guard let app = frontApp() else { return "" }
    let list = CGWindowListCopyWindowInfo([.optionOnScreenOnly, .excludeDesktopElements], kCGNullWindowID) as? [[String: Any]] ?? []
    for info in list {
        guard (info[kCGWindowOwnerPID as String] as? NSNumber)?.int32Value == app.processIdentifier,
              (info[kCGWindowLayer as String] as? NSNumber)?.intValue == 0,
              let number = info[kCGWindowNumber as String] as? NSNumber else { continue }
        return number.stringValue
    }
    return ""
}
func copyAttribute(_ element: AXUIElement, _ name: CFString) -> CFTypeRef? {
    var value: CFTypeRef?
    guard AXUIElementCopyAttributeValue(element, name, &value) == .success else { return nil }
    return value
}
func axString(_ element: AXUIElement, _ name: CFString) -> String {
    copyAttribute(element, name) as? String ?? ""
}
func clipName(_ raw: String) -> String {
    let trimmed = raw.trimmingCharacters(in: .whitespacesAndNewlines)
    // Keep one boundary character beyond the query limit so clipping cannot
    // turn a prefix into a false exact match.
    if trimmed.unicodeScalars.count <= 161 { return trimmed }
    return String(trimmed.unicodeScalars.prefix(161))
}
func elementName(_ element: AXUIElement) -> String {
    for attr in [kAXTitleAttribute as CFString, kAXDescriptionAttribute as CFString] {
        let name = clipName(axString(element, attr))
        if !name.isEmpty { return name }
    }
    return ""
}
func axPressable(_ element: AXUIElement) -> Bool {
    var names: CFArray?
    guard AXUIElementCopyActionNames(element, &names) == .success, let names else { return false }
    return ((names as? [String]) ?? []).contains(kAXPressAction as String)
}
func axSettable(_ element: AXUIElement) -> Bool {
    if axString(element, kAXSubroleAttribute as CFString) == "AXSecureTextField" { return false }
    var settable = DarwinBoolean(false)
    return AXUIElementIsAttributeSettable(element, kAXValueAttribute as CFString, &settable) == .success && settable.boolValue
}
func axFrame(_ element: AXUIElement) -> CGRect? {
    guard let position = copyAttribute(element, kAXPositionAttribute as CFString),
          let size = copyAttribute(element, kAXSizeAttribute as CFString),
          CFGetTypeID(position) == AXValueGetTypeID(),
          CFGetTypeID(size) == AXValueGetTypeID() else { return nil }
    var point = CGPoint.zero
    var box = CGSize.zero
    guard AXValueGetValue(position as! AXValue, .cgPoint, &point), AXValueGetValue(size as! AXValue, .cgSize, &box) else { return nil }
    return CGRect(origin: point, size: box)
}
func focusedWindow() -> AXUIElement? {
    guard let app = frontApp() else { return nil }
    let axApp = AXUIElementCreateApplication(app.processIdentifier)
    var window: CFTypeRef?
    guard AXUIElementCopyAttributeValue(axApp, kAXFocusedWindowAttribute as CFString, &window) == .success, let window else { return nil }
    return (window as! AXUIElement)
}
func collectElements(from window: AXUIElement, query: Bool) -> [String: Any] {
    var out: [[String: Any]] = []
    let maxNodes = query ? min(2000, max(1, number("max_nodes"))) : 500
    let limit = query ? min(80, max(1, number("limit"))) : 80
    let deadline = ProcessInfo.processInfo.systemUptime + 2
    var queue: [(AXUIElement, String, Int)] = [(window, "", 0)]
    var seen: Set<AXUIElement> = [window]
    var head = 0, visited = 0
    var stop = ""
    let text = (r["text"] as? String ?? "").lowercased()
    func normalizedRole(_ value: String) -> String {
        let lower = value.lowercased()
        return lower.hasPrefix("ax") ? String(lower.dropFirst(2)) : lower
    }
    let wantedRole = normalizedRole(r["role"] as? String ?? "")
    while head < queue.count {
        if ProcessInfo.processInfo.systemUptime >= deadline { stop = "timeout"; break }
        if visited >= maxNodes { stop = "max_nodes"; break }
        let (node, path, depth) = queue[head]; head += 1; visited += 1
        AXUIElementSetMessagingTimeout(node, 0.2)
        let role = axString(node, kAXRoleAttribute as CFString)
        let secure = axString(node, kAXSubroleAttribute as CFString) == "AXSecureTextField"
        let name = elementName(node)
        let value = secure ? "" : clipName(axString(node, kAXValueAttribute as CFString))
        let press = axPressable(node), settable = !secure && axSettable(node)
        let enabled = (copyAttribute(node, kAXEnabledAttribute as CFString) as? Bool) ?? true
        let fields = [name, value, clipName(axString(node, kAXDescriptionAttribute as CFString))]
        let matchesText = text.isEmpty || fields.contains {
            (r["match_mode"] as? String) == "exact" ? $0.lowercased() == text : $0.lowercased().contains(text)
        }
        let matches = !query || (matchesText && (wantedRole.isEmpty || normalizedRole(role) == wantedRole))
        if ProcessInfo.processInfo.systemUptime >= deadline { stop = "timeout"; break }
        if !path.isEmpty, matches, let frame = axFrame(node), frame.width > 0, frame.height > 0,
           frame.intersects(bounds), !name.isEmpty || !value.isEmpty || press || settable {
            out.append([
                "index": out.count, "role": role, "name": name, "value": value,
                "description": fields[2],
                "identity": axString(node, kAXIdentifierAttribute as CFString),
                "enabled": enabled, "focused": (copyAttribute(node, kAXFocusedAttribute as CFString) as? Bool) ?? false,
                "secure": secure, "press": press, "settable": settable,
                "x": Int(frame.minX), "y": Int(frame.minY), "w": Int(frame.width), "h": Int(frame.height), "path": path,
            ])
            if out.count >= limit { stop = "limit"; break }
        }
        // Page children rather than reading an arbitrarily large AX array.
        var count: CFIndex = 0
        let childStatus = AXUIElementGetAttributeValueCount(node, kAXChildrenAttribute as CFString, &count)
        if childStatus != .success {
            if childStatus != .attributeUnsupported && stop.isEmpty { stop = "ax_error" }
            continue
        }
        if depth >= 16 { if count > 0 && stop.isEmpty { stop = "max_depth" }; continue }
        let capacity = max(0, maxNodes - seen.count)
        let pageSize = min(count, capacity)
        if count > pageSize && stop.isEmpty { stop = "max_nodes" }
        guard pageSize > 0 else { continue }
        var page: CFArray?
        if AXUIElementCopyAttributeValues(node, kAXChildrenAttribute as CFString, 0, pageSize, &page) != .success {
            if stop.isEmpty { stop = "ax_error" }; continue
        }
        for (i, child) in ((page as? [AXUIElement]) ?? []).enumerated() where seen.insert(child).inserted {
            queue.append((child, path.isEmpty ? "\(i)" : "\(path)/\(i)", depth + 1))
        }
    }
    return ["elements": out, "visited_nodes": visited, "truncated": !stop.isEmpty, "stop_reason": stop]
}
func elementAt(_ path: String) -> AXUIElement? {
    guard let window = focusedWindow(), !path.isEmpty else { return nil }
    var current = window
    for part in path.split(separator: "/") {
        guard let index = Int(part) else { return nil }
        if index < 0 || index >= 2000 { return nil }
        var page: CFArray?
        guard AXUIElementCopyAttributeValues(current, kAXChildrenAttribute as CFString, index, 1, &page) == .success,
              let child = (page as? [AXUIElement])?.first else { return nil }
        current = child
    }
    return current
}
func verify(_ element: AXUIElement) {
    let role = axString(element, kAXRoleAttribute as CFString)
    let name = elementName(element)
    if role != (r["expect_role"] as? String ?? "") || name != (r["expect_name"] as? String ?? "") {
        fail("element changed; observe again")
    }
    if axString(element, kAXIdentifierAttribute as CFString) != (r["expect_identity"] as? String ?? "") {
        fail("element identity changed; observe/query again")
    }
    if (copyAttribute(element, kAXEnabledAttribute as CFString) as? Bool) == false { fail("element disabled") }
    if axString(element, kAXSubroleAttribute as CFString) == "AXSecureTextField" { fail("authentication control requires user") }
    if clipName(axString(element, kAXValueAttribute as CFString)) != (r["expect_value"] as? String ?? "") { fail("element value changed; observe/query again") }
    if clipName(axString(element, kAXDescriptionAttribute as CFString)) != (r["expect_description"] as? String ?? "") { fail("element description changed; observe/query again") }
    guard let frame = axFrame(element), let expected = r["expect_frame"] as? [Int], expected.count == 4,
          [Int(frame.minX), Int(frame.minY), Int(frame.width), Int(frame.height)] == expected else {
        fail("element geometry changed; observe/query again")
    }
    verifyDesktop()
}
func verifyDesktop() {
    if let expected = r["expect_app_id"] as? String, !expected.isEmpty, appID() != expected { fail("foreground app changed; observe/query again") }
    if let expected = r["expect_window_id"] as? String, !expected.isEmpty, windowID() != expected { fail("foreground window changed; observe/query again") }
}
if action == "request_permissions" {
    let options = [kAXTrustedCheckOptionPrompt.takeUnretainedValue() as String: true] as CFDictionary
    _ = AXIsProcessTrustedWithOptions(options)
    _ = CGRequestScreenCaptureAccess()
    exit(0)
}
let trusted = AXIsProcessTrusted()
let screen = CGPreflightScreenCaptureAccess()
if action == "status" {
    let state: [String: Any] = ["driver":"macos-coregraphics", "ready":trusted && screen,
      "reason":trusted && screen ? "" : "Enable Accessibility and Screen Recording for computer-use-helper / DeepSentry in System Settings, then restart. Permissions are not bypassed.",
      "x":Int(bounds.minX), "y":Int(bounds.minY), "width":Int(bounds.width), "height":Int(bounds.height),
      "foreground":foreground(), "app_id":appID(), "window_id":windowID(), "surface":"primary display"]
    FileHandle.standardOutput.write(try JSONSerialization.data(withJSONObject:state)); exit(0)
}
if action == "capture" {
    guard screen, let path = r["path"] as? String else { fail("Screen Recording permission or output path missing") }
    let task = Process(); task.executableURL = URL(fileURLWithPath:"/usr/sbin/screencapture")
    task.arguments = ["-x", "-D", "1", "-t", "png", path]
    try task.run(); task.waitUntilExit(); if task.terminationStatus != 0 { fail("screencapture failed") }; exit(0)
}
if action == "elements" || action == "query" {
    guard trusted else { fail("Accessibility permission missing") }
    verifyDesktop()
    guard let window = focusedWindow() else { fail("focused accessibility window unavailable") }
    AXUIElementSetMessagingTimeout(window, 0.2)
    let payload = collectElements(from: window, query: action == "query")
    verifyDesktop()
    FileHandle.standardOutput.write(try JSONSerialization.data(withJSONObject:payload)); exit(0)
}
guard trusted else { fail("Accessibility permission missing") }
if action != "release" { verifyDesktop() }
func mouse(_ type: CGEventType, _ point: CGPoint, _ button: CGMouseButton, _ count: Int64 = 1) {
    guard let e = CGEvent(mouseEventSource:nil, mouseType:type, mouseCursorPosition:point, mouseButton:button) else { fail("Cannot allocate mouse event") }
    e.setIntegerValueField(.mouseEventClickState, value:count); e.post(tap:.cghidEventTap)
}
let point = CGPoint(x:number("x"),y:number("y"))
let button: CGMouseButton = (r["button"] as? String)=="right" ? .right : ((r["button"] as? String)=="middle" ? .center : .left)
let down: CGEventType = button == .right ? .rightMouseDown : (button == .center ? .otherMouseDown : .leftMouseDown)
let up: CGEventType = button == .right ? .rightMouseUp : (button == .center ? .otherMouseUp : .leftMouseUp)
let dragged: CGEventType = button == .right ? .rightMouseDragged : (button == .center ? .otherMouseDragged : .leftMouseDragged)
let keys: [String:CGKeyCode] = ["A":0,"S":1,"D":2,"F":3,"H":4,"G":5,"Z":6,"X":7,"C":8,"V":9,"B":11,"Q":12,"W":13,"E":14,"R":15,"Y":16,"T":17,"1":18,"2":19,"3":20,"4":21,"6":22,"5":23,"9":25,"7":26,"8":28,"0":29,"O":31,"U":32,"I":34,"P":35,"L":37,"J":38,"K":40,"N":45,"M":46,"ENTER":36,"TAB":48,"SPACE":49,"BACKSPACE":51,"ESCAPE":53,"DELETE":117,"HOME":115,"END":119,"PAGEUP":116,"PAGEDOWN":121,"LEFT":123,"RIGHT":124,"DOWN":125,"UP":126]
switch action {
case "move":mouse(.mouseMoved,point,button)
case "click", "double_click":
    let count = action == "double_click" ? 2 : 1
    for i in 1...count { mouse(down,point,button,Int64(i)); mouse(up,point,button,Int64(i)); if i < count { usleep(80000) } }
case "drag":
    mouse(down,point,button)
    let target = CGPoint(x:number("to_x"), y:number("to_y"))
    for i in 1...10 { let f = Double(i)/10;mouse(dragged,CGPoint(x:point.x+(target.x-point.x)*f,y:point.y+(target.y-point.y)*f),button);usleep(10000) }
    mouse(up,target,button)
case "scroll":
    if (r["has_point"] as? Bool) == true { mouse(.mouseMoved, point, .left) }
    guard let event = CGEvent(scrollWheelEvent2Source:nil,units:.line,wheelCount:1,wheel1:Int32(-number("amount")),wheel2:0,wheel3:0) else { fail("Cannot allocate scroll event") }
    event.post(tap:.cghidEventTap)
case "type":
    guard let text = r["text"] as? String else { fail("Missing text") }
    // Send Unicode directly; do not overwrite the user's clipboard.
    for scalar in text { let chars = Array(String(scalar).utf16)
        for pressed in [true,false] { do {
            guard let e=CGEvent(keyboardEventSource:nil,virtualKey:0,keyDown:pressed) else { fail("Cannot allocate Unicode event") }
            chars.withUnsafeBufferPointer { e.keyboardSetUnicodeString(stringLength:chars.count,unicodeString:$0.baseAddress!) };e.post(tap:.cghidEventTap)
        } }
    }
case "key":
    let parts = (r["key"] as? String ?? "").uppercased().split(separator:"+").map(String.init)
    guard let last=parts.last, let code=keys[last] else {fail("Unsupported key")}
    var flags: CGEventFlags = []
    for modifier in parts.dropLast() { switch modifier {case "CTRL":flags.insert(.maskControl);case "ALT":flags.insert(.maskAlternate);case "SHIFT":flags.insert(.maskShift);case "META":flags.insert(.maskCommand);default:fail("Invalid modifier")} }
    for pressed in [true,false] {
        guard let e=CGEvent(keyboardEventSource:nil,virtualKey:code,keyDown:pressed) else { fail("Cannot allocate keyboard event") }
        e.flags=flags;e.post(tap:.cghidEventTap)
    }
case "press_element":
    guard let path = r["element_path"] as? String, let element = elementAt(path) else { fail("element changed; observe again") }
    verify(element)
    if AXUIElementPerformAction(element, kAXPressAction as CFString) != .success { fail("AXPress failed") }
case "click_element":
    guard let path = r["element_path"] as? String, let element = elementAt(path) else { fail("element changed; observe again") }
    verify(element)
    mouse(down,point,button); mouse(up,point,button)
case "set_value":
    guard let path = r["element_path"] as? String, let element = elementAt(path) else { fail("element changed; observe again") }
    verify(element)
    guard axSettable(element), let text = r["text"] as? String else { fail("element is not settable") }
    if AXUIElementSetAttributeValue(element, kAXValueAttribute as CFString, text as CFTypeRef) != .success { fail("AX set value failed") }
case "release":
    let current=CGEvent(source:nil)?.location ?? .zero
    mouse(.leftMouseUp,current,.left);mouse(.rightMouseUp,current,.right);mouse(.otherMouseUp,current,.center)
    for code: CGKeyCode in [55,56,58,59] {CGEvent(keyboardEventSource:nil,virtualKey:code,keyDown:false)?.post(tap:.cghidEventTap)}
default:fail("Unsupported action")
}
