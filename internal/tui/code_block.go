package tui

import (
	"strings"
	"unicode"
)

func renderCodeBlockLanguage(lines []string, width int, language string) string {
	if len(lines) == 0 {
		return ""
	}
	width = max(1, width)
	out := make([]string, 0, len(lines))
	for _, raw := range lines {
		line := sanitizeTUIText(raw)
		if heading, ok := fencedSectionHeading(line); ok {
			out = append(out, strings.Split(wrapDisplay(mdH2Style.Render(heading), width), "\n")...)
			continue
		}
		styled := mdCodeBodyStyle.Render(line)
		if isShellCodeLanguage(language) {
			if at := shellCommentStart(line); at >= 0 {
				styled = mdCodeBodyStyle.Render(line[:at]) + mdCodeCommentStyle.Render(line[at:])
			}
		}
		// Preserve command bytes: do not insert gutters, continuation glyphs,
		// padding or emoji spaces into executable rows and quoted strings.
		out = append(out, strings.Split(wrapDisplay(styled, width), "\n")...)
	}
	return strings.Join(out, "\n")
}

func fencedSectionHeading(line string) (string, bool) {
	trimmed := strings.TrimLeft(line, " ")
	for _, prefix := range []string{"-- ", "# ", "## ", "### "} {
		if !strings.HasPrefix(trimmed, prefix) {
			continue
		}
		text := strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
		cluster, rest := firstDisplayCluster(text)
		// Model-produced cheat sheets sometimes put emoji section captions
		// inside a fence. Only those captions receive prose spacing.
		if !isEmojiCluster(cluster) || strings.TrimSpace(rest) == "" {
			return "", false
		}
		return line[:len(line)-len(trimmed)] + prefix + normalizeMarkdownHeadingText(text), true
	}
	return "", false
}

func isShellCodeLanguage(language string) bool {
	switch strings.ToLower(strings.TrimSpace(language)) {
	case "", "sh", "shell", "bash", "zsh", "shellscript", "console":
		return true
	default:
		return false
	}
}

func shellCommentStart(line string) int {
	var quote rune
	escaped := false
	var previous rune
	for i, r := range line {
		if escaped {
			escaped = false
			previous = r
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped = true
			previous = r
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			}
			previous = r
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			previous = r
			continue
		}
		if r == '#' && (i == 0 || unicode.IsSpace(previous)) {
			return i
		}
		previous = r
	}
	return -1
}
