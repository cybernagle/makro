import Foundation

enum ANSI {
    private static let escapePattern = "\u{1B}\\][^\u{07}]*\u{07}|\u{1B}\\[[0-9;?]*[A-Za-z]|\u{1B}[()][AB012]|\u{1B}[=>]|\u{1B}O[A-Za-z]"
    private static let escapeRegex = try? NSRegularExpression(pattern: escapePattern)

    /// Lines that are purely decorative separators.
    /// Covers ASCII (- = *) and Unicode dashes/box-drawing horizontals.
    /// Whitespace class includes Unicode spaces (nbsp, en/em space, ideographic space).
    private static let separatorChars = "-=*\u{2010}-\u{2015}\u{2500}\u{2501}\u{2504}\u{2505}\u{2508}\u{2509}\u{254C}\u{254D}\u{2550}\u{2551}"
    private static let ws = "\\s\\u00A0\\u2000-\\u200A\\u202F\\u205F\\u3000"
    private static let separatorPattern = "^[" + ws + "]*[" + separatorChars + "](?:[" + ws + "]*[" + separatorChars + "]){2,}[" + ws + "]*$"
    private static let separatorRegex = try? NSRegularExpression(pattern: separatorPattern)

    /// TUI chrome lines — status bar, auto-mode hint, box-drawing borders.
    /// Anything in here gets dropped completely.
    private static let boxChars = "\u{2500}\u{2501}\u{2502}\u{2503}\u{250C}\u{250F}\u{2510}\u{2513}\u{2514}\u{2517}\u{2518}\u{251B}\u{251C}\u{2523}\u{2524}\u{252B}\u{252C}\u{2533}\u{2534}\u{253B}\u{253C}\u{254B}\u{2550}\u{2551}\u{2552}\u{2553}\u{2554}\u{2555}\u{2556}\u{2557}\u{2558}\u{2559}\u{255A}\u{255B}\u{255C}\u{255D}\u{255E}\u{255F}\u{2560}\u{2561}\u{2562}\u{2563}\u{2564}\u{2565}\u{2566}\u{2567}\u{2568}\u{2569}\u{256A}\u{256B}\u{256C}"
    private static let borderPattern = "^[" + ws + boxChars + "]+$"
    private static let borderRegex = try? NSRegularExpression(pattern: borderPattern)

    /// Status bar / chrome hint lines from the agent TUI (Claude Code / makro shell).
    /// Drop the whole line.
    private static let chromePatterns: [String] = [
        "^\\s*\\[OMC#",                          // [OMC#4.14.4] -> ... status bar (may be indented)
        "^\\s*\u{23F5}\u{23F5}\\s+auto mode",    // ⏵⏵ auto mode on ...
        "shift\\+tab to cycle",                  // auto-mode hint tail
        "new task\\?\\s*/clear to save",         // token hint
        "^\\s*\u{2756}\\s*$",                    // ❖ lone decorative marker
        "^\\s*\u{2756}\\s+\u{2756}\\s*$",        // ❖ ❖ decorator
    ]
    private static let chromeRegexes: [NSRegularExpression] = chromePatterns.compactMap {
        try? NSRegularExpression(pattern: $0)
    }

    /// Markers we strip from the start/end of lines (preserve the content).
    private static let edgeMarkerPattern = "^[\\s\u{2502}\u{2503}\u{23F5}\u{23F6}\u{23F7}\u{23F8}\u{23F9}\u{23FA}\u{23FB}\u{23FC}\u{23FD}\u{23FE}\u{23FF}\u{25B8}\u{25B6}\u{203A}\u{2022}]+|[\\s\u{2502}\u{2503}]+$"
    private static let edgeMarkerRegex = try? NSRegularExpression(pattern: edgeMarkerPattern)

    static func strip(_ s: String) -> String {
        guard let regex = escapeRegex else { return s }
        let range = NSRange(location: 0, length: (s as NSString).length)
        return regex.stringByReplacingMatches(in: s, range: range, withTemplate: "")
    }

    static func clean(_ s: String, maxLines: Int = 500) -> String {
        let stripped = strip(s)
        var result: [String] = []
        result.reserveCapacity(min(stripped.count, maxLines))
        var prevBlank = false
        for raw in stripped.components(separatedBy: "\n") {
            var line = raw
            if isChrome(line) { continue }
            line = stripBorders(line: line)
            var trimmed = line
            while trimmed.last?.isWhitespace == true { trimmed.removeLast() }
            while trimmed.first?.isWhitespace == true { trimmed.removeFirst() }
            if isSeparator(trimmed) { continue }
            let isBlank = trimmed.isEmpty
            if isBlank && prevBlank { continue }
            result.append(trimmed)
            prevBlank = isBlank
        }
        if result.count > maxLines {
            result = Array(result.suffix(maxLines))
        }
        return result.joined(separator: "\n")
    }

    private static func isSeparator(_ line: String) -> Bool {
        guard let re = separatorRegex else { return false }
        let range = NSRange(location: 0, length: (line as NSString).length)
        return re.firstMatch(in: line, range: range) != nil
    }

    private static func isChrome(_ line: String) -> Bool {
        let range = NSRange(location: 0, length: (line as NSString).length)
        if let re = borderRegex, re.firstMatch(in: line, range: range) != nil { return true }
        for re in chromeRegexes {
            if re.firstMatch(in: line, range: range) != nil { return true }
        }
        return false
    }

    /// Strip leading/trailing box-drawing vertical bars and edge markers, keeping content.
    /// `│ foo │ bar │` → `foo │ bar`
    private static func stripBorders(line: String) -> String {
        guard let re = edgeMarkerRegex else { return line }
        let range = NSRange(location: 0, length: (line as NSString).length)
        return re.stringByReplacingMatches(in: line, range: range, withTemplate: "")
    }
}
