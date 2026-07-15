package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
)

// displayWidth follows grapheme clusters instead of adding rune widths. This
// keeps emoji presentation selectors, skin tones, keycaps and ZWJ sequences
// aligned with how terminals render them.
func displayWidth(s string) int {
	return ansi.StringWidth(s)
}

func truncateDisplay(s string, width int, tail string) string {
	if width <= 0 {
		return ""
	}
	return ansi.Truncate(s, width, tail)
}

func wrapDisplayClusters(s string, width int) string {
	if width <= 0 {
		return s
	}
	// Hardwrap with preserveSpace keeps every byte of commands/code intact;
	// only display newlines are inserted.
	return ansi.Hardwrap(s, width, true)
}

func firstDisplayCluster(s string) (cluster, rest string) {
	cluster, rest, _, _ = uniseg.FirstGraphemeClusterInString(s, -1)
	return cluster, rest
}

// Width styles do not truncate content. This final guard prevents one
// overlong grapheme from pushing a bordered panel's right edge to a new row.
func fitRenderedBlock(s string, width int) string {
	if width <= 0 {
		return ""
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = truncateDisplay(line, width, "")
	}
	return strings.Join(lines, "\n")
}

// Lipgloss Width includes padding but excludes borders and margins. Keep this
// separate from the actual content width (which excludes the entire frame).
func styleRenderWidth(style lipgloss.Style, totalWidth int) int {
	return max(1, totalWidth-style.GetHorizontalMargins()-style.GetHorizontalBorderSize())
}
