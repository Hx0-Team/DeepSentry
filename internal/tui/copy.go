package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"ai-edr/internal/harness"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

func (m *AgentModel) mouseToContentCoord(x, y int) (row, col int, ok bool) {
	bodyH := m.viewport.Height
	if bodyH <= 0 {
		bodyH = max(4, m.height-6)
	}
	top := m.frameLayout().headerHeight
	if y < top || y >= top+bodyH {
		return 0, 0, false
	}
	row = m.viewport.YOffset + (y - top)
	col = x - 1
	if col < 0 {
		col = 0
	}
	lines := strings.Split(m.viewportPlain, "\n")
	if row < 0 || row >= len(lines) {
		return 0, 0, false
	}
	lineW := runewidth.StringWidth(lines[row])
	if lineW == 0 {
		return row, 0, true
	}
	if col >= lineW {
		col = lineW - 1
	}
	return row, col, true
}

func (m *AgentModel) noteCopyCommand(action *harness.AgentAction) {
	if action == nil || strings.TrimSpace(action.Command) == "" || len(m.lines) == 0 {
		return
	}
	redacted := harness.RedactedAction(*action)
	m.lines[len(m.lines)-1].copyCommand = sanitizeCopyPayload(redacted.Command)
}

func (m AgentModel) lastCopyPayload(kind string) string {
	if kind != "code" {
		for i := len(m.lines) - 1; i >= 0; i-- {
			if m.lines[i].copyCommand != "" {
				return m.lines[i].copyCommand
			}
		}
		if kind == "command" {
			return ""
		}
	}
	for i := len(m.lines) - 1; i >= 0; i-- {
		if m.lines[i].kind != "success" && m.lines[i].kind != "ask" {
			continue
		}
		text := m.lines[i].raw
		if text == "" {
			text = m.lines[i].content
		}
		if code := lastFencedCode(text); code != "" {
			return code
		}
	}
	return ""
}

func lastFencedCode(text string) string {
	var code []string
	last := ""
	inFence := false
	for _, line := range strings.Split(sanitizeCopyPayload(text), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			if inFence {
				last = strings.Join(code, "\n")
			}
			inFence = !inFence
			code = nil
			continue
		}
		if inFence {
			code = append(code, line)
		}
	}
	return last
}

func sanitizeCopyPayload(text string) string {
	text = strings.ReplaceAll(ansi.Strip(text), "\r\n", "\n")
	var out strings.Builder
	for _, r := range text {
		// Tabs are source indentation. Rendering expands them to cells, but
		// original-copy must not change them into spaces or add UI padding.
		if r == '\n' || r == '\t' || (!unicode.IsControl(r) && r != 0x7f && !isUnsafeFormatRune(r)) {
			out.WriteRune(r)
		}
	}
	return out.String()
}

func (m *AgentModel) hasCopySelection() bool {
	if !m.selActive {
		return false
	}
	return strings.TrimSpace(m.selectedPlainText()) != ""
}

func (m *AgentModel) selectedPlainText() string {
	return extractPlainSelection(m.viewportPlain, m.selRow1, m.selCol1, m.selRow2, m.selCol2)
}

func (m *AgentModel) copyTextCmd(text string) tea.Cmd {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return tea.Batch(
		func() tea.Msg {
			if err := copyToClipboard(text); err != nil {
				return copyToastMsg{err: err.Error()}
			}
			return copyToastMsg{chars: selectionCharCount(text)}
		},
		tea.Tick(3*time.Second, func(time.Time) tea.Msg { return copyToastClearMsg{} }),
	)
}

func (m *AgentModel) copyToastText(msg copyToastMsg) string {
	if msg.err != "" {
		return fmt.Sprintf("copy failed: %s", msg.err)
	}
	return fmt.Sprintf("copied %d chars to clipboard · 鼠标拖选或 Ctrl+C 复制", msg.chars)
}

func (m AgentModel) viewportView() string {
	if !m.selActive {
		return m.viewport.View()
	}
	return renderSelectionStyled(m.viewport.View(), m.viewportPlain, m.viewport.YOffset, m.viewport.Height, m.selRow1, m.selCol1, m.selRow2, m.selCol2)
}
