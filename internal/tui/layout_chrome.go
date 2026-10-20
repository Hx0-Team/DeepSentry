package tui

import (
	"fmt"
	"strings"
	"time"
)

func (m AgentModel) renderStatusLine(w int, now time.Time) string {
	inner := max(1, w-styleStatusBar.GetHorizontalFrameSize())
	parts := []string{m.statusLine, m.statusContextText()}
	if m.scheduleStatus != "" {
		parts = append(parts, m.scheduleStatus)
	}
	if m.running && m.currentStep > 0 {
		parts = append(parts, fmt.Sprintf("Step %d/%d", m.currentStep, m.maxSteps))
	}
	if scroll := m.scrollPositionText(); scroll != "" {
		parts = append(parts, scroll)
	}
	parts = append(parts, now.Format("15:04:05"))
	return styleStatusBar.Width(w).Render(truncateDisplay(sanitizeTUIText(strings.Join(parts, "  │  ")), inner, "…"))
}

func (m AgentModel) fitFooterHelp(full string, width int) string {
	if displayWidth(full) <= width {
		return full
	}
	if m.copyToast != "" || m.pendingConfirm != nil || m.pendingAsk != nil {
		return truncateDisplay(full, width, "…")
	}
	compact := "Tab 输入 · Ctrl+C 复制 · /copy 原文 · PgUp/Dn 翻页 · /help"
	minimal := "Tab 输入 · /help 帮助"
	if m.inputFocused() {
		compact = "Enter 发送 · Shift+Enter 换行 · Tab 浏览 · /help"
		minimal = "Enter 发送 · Tab 浏览 · /help"
	}
	if m.running {
		compact = "Esc 停止 · " + compact
		minimal = "Esc 停止 · " + minimal
	}
	for _, candidate := range []string{compact, minimal} {
		if displayWidth(candidate) <= width {
			return candidate
		}
	}
	return truncateDisplay(minimal, width, "…")
}
