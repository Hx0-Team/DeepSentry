package tui

import (
	"ai-edr/internal/analyzer"
	"ai-edr/internal/harness"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestRestoreConversationHistoryShowsDialogueWithoutToolFeedback(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, false, false, StartupInfo{})
	finishRaw, err := json.Marshal(harness.AgentAction{Type: harness.ActionFinish, FinalReport: "最终结论：系统正常"})
	if err != nil {
		t.Fatal(err)
	}
	m.restoreConversationHistory([]analyzer.Message{
		{Role: "user", Content: "需求：检查系统版本"},
		{Role: "assistant", Content: `{"action":"execute","command":"uname -a"}`},
		{Role: "user", Content: "Output:\nDarwin secret-internal-output"},
		{Role: "assistant", Content: string(finishRaw)},
		{Role: "system", Content: "【系统】本轮已结束"},
	})
	m.width, m.height = 100, 30
	m.recalcLayout()
	m.refreshViewport()
	view := stripANSIForTest(m.viewport.View())
	for _, want := range []string{"已恢复 2 条历史对话记录", "检查系统版本", "最终结论：系统正常"} {
		if !strings.Contains(view, want) {
			t.Fatalf("restored view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "secret-internal-output") || strings.Contains(view, "uname -a") {
		t.Fatalf("tool feedback/action should stay hidden from restored dialogue:\n%s", view)
	}
}

func TestMemoryCluesSlashShowsAndClearsSessionBoard(t *testing.T) {
	state := harness.NewAgentState(t.TempDir())
	state.ObserveCoreClues("关键结论：攻击源 192.0.2.44", "test")
	agent := &harness.DeepAgent{State: state}
	m := NewAgentModel(nil, "model", "local", 30, false, false, StartupInfo{})
	m.ctrl = newSessionController(SessionConfig{Agent: agent})
	m.handleMemorySlash("clues")
	m.refreshViewport()
	if view := stripANSIForTest(m.viewport.View()); !strings.Contains(view, "192.0.2.44") {
		t.Fatalf("/memory clues did not render clue board:\n%s", view)
	}
	m.handleMemorySlash("clues clear")
	if len(state.CoreCluesSnapshot()) != 0 {
		t.Fatal("/memory clues clear did not clear session board")
	}
}

func TestRenderWrappedKeepsToolOutputWithinViewport(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, true, false, StartupInfo{})
	m.width = 48
	m.height = 18
	m.recalcLayout()

	longPorts := strings.Repeat(":8080|:8081|:8082|:8083|:8084|", 8)
	m.appendLine("tool", "Shell · ss -tuln | grep -E '"+longPorts+"'", longPorts)
	m.refreshViewport()

	for _, line := range strings.Split(m.viewport.View(), "\n") {
		if got := lipgloss.Width(line); got > m.viewport.Width {
			t.Fatalf("rendered line width = %d, want <= %d: %q", got, m.viewport.Width, stripANSIForTest(line))
		}
	}
}

func TestInputFocusedAbsorbsGlobalShortcuts(t *testing.T) {
	shortcuts := []string{"q", "e", "j", "k"}
	for _, k := range shortcuts {
		m := NewAgentModel(nil, "model", "local", 30, true, false, StartupInfo{})
		if !m.inputFocused() {
			t.Fatalf("key %q: expected input focused", k)
		}
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
		model := updated.(AgentModel)
		if model.quitting {
			t.Fatalf("key %q should not quit when input focused", k)
		}
		if !strings.Contains(model.input.Value(), k) {
			t.Fatalf("key %q should be typed into input, got %q", k, model.input.Value())
		}
	}
}

func TestGlobalQuitOnlyWhenInputBlurred(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, false, false, StartupInfo{})
	m.done = true
	m.input.Blur()
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	model := updated.(AgentModel)
	if !model.quitting {
		t.Fatal("q should quit when input blurred and session idle")
	}
	if cmd == nil {
		t.Fatal("expected quit command")
	}
}

func TestQDoesNotDiscardRunningTask(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, false, false, StartupInfo{})
	m.running = true
	m.sessionLive = true
	m.input.Blur()
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	model := updated.(AgentModel)
	if model.quitting || cmd != nil {
		t.Fatal("q must not quit and discard an active task; Esc is the stop command")
	}
}

func TestSlashCommandRestoresViewportAfterSuggestionMenu(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, true, false, StartupInfo{})
	m.width, m.height = 80, 24
	m.input.SetValue("/")
	m.recalcLayout()
	withMenu := m.viewport.Height

	m.handleSlashCommand("/status")
	if m.input.Value() != "" {
		t.Fatalf("slash command should clear input, got %q", m.input.Value())
	}
	if m.viewport.Height <= withMenu {
		t.Fatalf("viewport height remained reserved for closed suggestions: before=%d after=%d", withMenu, m.viewport.Height)
	}
}

func TestSudoResultRestoresTerminalBeforeAgentResumes(t *testing.T) {
	tests := []struct {
		name string
		err  error
		ok   bool
	}{
		{name: "success", ok: true},
		{name: "failure", err: errors.New("exit status 1"), ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewAgentModel(nil, "model", "local", 30, false, false, StartupInfo{})
			m.width, m.height = 100, 30
			m.selecting = true
			m.selActive = true
			respCh := make(chan bool, 1)

			updated, cmd := m.Update(sudoAuthResultMsg{respCh: respCh, err: tt.err})
			m = updated.(AgentModel)
			if cmd == nil {
				t.Fatal("sudo result must schedule alternate-screen, mouse-mode and repaint restoration")
			}
			select {
			case <-respCh:
				t.Fatal("Agent resumed before the terminal restoration sequence completed")
			default:
			}

			updated, _ = m.Update(sudoTerminalRestoredMsg{respCh: respCh, ok: tt.ok})
			m = updated.(AgentModel)
			if m.selecting || m.selActive {
				t.Fatal("stale mouse selection survived sudo terminal restoration")
			}
			select {
			case got := <-respCh:
				if got != tt.ok {
					t.Fatalf("sudo response = %v, want %v", got, tt.ok)
				}
			default:
				t.Fatal("Agent was not resumed after terminal restoration")
			}
		})
	}
}

func TestWrappedInputFocusChangesRecalculateFooter(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, true, false, StartupInfo{})
	m.width, m.height = 80, 24
	value := strings.Repeat("中文输入", 80)
	m.input.SetValue(value)
	m.input.SetCursor(len([]rune(value)))
	m.recalcLayout()
	focusedHeight := m.viewport.Height

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(AgentModel)
	if m.inputFocused() {
		t.Fatal("Esc should blur input")
	}
	if m.viewport.Height <= focusedHeight {
		t.Fatalf("blurred wrapped input left stale footer space: focused=%d blurred=%d", focusedHeight, m.viewport.Height)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(AgentModel)
	if !m.inputFocused() {
		t.Fatal("Tab should focus input")
	}
	if m.viewport.Height != focusedHeight {
		t.Fatalf("refocused wrapped input layout=%d want %d", m.viewport.Height, focusedHeight)
	}
}

func TestTruncateStrIsUnicodeSafe(t *testing.T) {
	got := truncateStr("这个任务会检查 Windows Terminal 里的 emoji ✅ 和中文宽度", 18)
	if !utf8.ValidString(got) {
		t.Fatalf("truncateStr returned invalid UTF-8: %q", got)
	}
	if width := lipgloss.Width(got); width > 18 {
		t.Fatalf("truncateStr width = %d, want <= 18: %q", width, got)
	}
}

func TestTargetStatusUpdatesCurrentTarget(t *testing.T) {
	m := NewAgentModel(nil, "model", "Fleet 多目标: 2 台", 30, true, false, StartupInfo{})
	m.applyEvent(harness.UIEvent{
		Kind:           harness.EventTargetStatus,
		Status:         "running",
		Message:        "fleet_exec uptime",
		TargetName:     "web-01",
		TargetProtocol: "ssh",
		TargetHost:     "10.0.0.1:22",
	})
	if !strings.Contains(m.currentTarget, "web-01") {
		t.Fatalf("current target not updated: %q", m.currentTarget)
	}
	if len(m.lines) == 0 || m.lines[len(m.lines)-1].kind != "target" {
		t.Fatalf("target event not appended: %#v", m.lines)
	}
}

func TestFormatActionLineShowsExecutionTarget(t *testing.T) {
	local := FormatActionLine(&harness.AgentAction{Type: harness.ActionExecute, Command: "date", TargetProtocol: "local"})
	if !strings.Contains(local, "控制端本机") || !strings.Contains(local, "date") {
		t.Fatalf("local action line missing target: %q", local)
	}
	if strings.Contains(local, "远端") {
		t.Fatalf("local action line should not be remote: %q", local)
	}

	localRun := FormatActionLine(&harness.AgentAction{Type: harness.ActionExecute, Command: "local_run hostname", TargetProtocol: "local", TargetHost: "198.51.100.42:2222"})
	if !strings.Contains(localRun, "控制端本机") || strings.Contains(localRun, "远端") {
		t.Fatalf("local_run action line should stay local even with stale host: %q", localRun)
	}

	remote := FormatActionLine(&harness.AgentAction{Type: harness.ActionExecute, Command: "id", TargetProtocol: "ssh", TargetHost: "198.51.100.42:2222"})
	for _, want := range []string{"远端 SSH", "198.51.100.42:2222", "id"} {
		if !strings.Contains(remote, want) {
			t.Fatalf("remote action line missing %q: %q", want, remote)
		}
	}

	fleet := FormatActionLine(&harness.AgentAction{Type: harness.ActionExecute, Command: "uptime", TargetName: "web-01", TargetProtocol: "ssh", TargetHost: "10.0.0.1:22"})
	for _, want := range []string{"远端 SSH", "web-01", "10.0.0.1:22", "uptime"} {
		if !strings.Contains(fleet, want) {
			t.Fatalf("fleet action line missing %q: %q", want, fleet)
		}
	}
}

func TestFormatActionLineShowsIncompleteSubAgentTask(t *testing.T) {
	line := FormatActionLine(&harness.AgentAction{Type: harness.ActionTask})
	if !strings.Contains(line, "参数不完整") {
		t.Fatalf("empty sub-agent task should show incomplete params: %q", line)
	}
	if strings.Contains(line, "->") || strings.Contains(line, "→") {
		t.Fatalf("empty sub-agent task should not render an empty arrow: %q", line)
	}
}

func TestCommandOutputGroupCollapsesAndExpands(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, false, false, StartupInfo{})
	m.width = 100
	m.height = 24
	m.recalcLayout()

	m.applyEvent(harness.UIEvent{Kind: harness.EventAction, Action: &harness.AgentAction{Type: harness.ActionExecute, Command: "cat /etc/passwd"}})
	m.applyEvent(harness.UIEvent{Kind: harness.EventCommandOutput, Message: "root:x:0:0:root:/root:/bin/bash\n"})
	m.applyEvent(harness.UIEvent{Kind: harness.EventCommandOutput, Message: "daemon:x:1:1:daemon:/usr/sbin:/usr/sbin/nologin\n"})
	group := m.activeCmdGroup
	if group <= 0 {
		t.Fatal("expected active command output group")
	}

	m.collapseCommandOutputGroup(group)
	m.refreshViewport()
	view := stripANSIForTest(m.viewport.View())
	if !strings.Contains(view, "命令输出已折叠") || !strings.Contains(view, "[e 全部展开]") {
		t.Fatalf("collapsed command output summary missing:\n%s", view)
	}
	if strings.Contains(view, "daemon:x") {
		t.Fatalf("collapsed command output should hide later lines:\n%s", view)
	}

	m.toggleLastCollapsible()
	m.refreshViewport()
	view = stripANSIForTest(m.viewport.View())
	if !strings.Contains(view, "root:x") || !strings.Contains(view, "daemon:x") {
		t.Fatalf("expanded command output should show original lines:\n%s", view)
	}
}

func TestETogglesEveryCollapsibleAsOneGlobalState(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, false, false, StartupInfo{})
	m.width, m.height = 100, 24
	m.recalcLayout()
	m.lines = []logLine{
		{kind: "result", content: "工具摘要一", raw: "工具完整结果一", collapsed: true},
		{kind: "result", content: "工具摘要二", raw: "工具完整结果二"},
		{kind: "cmdout", content: "group one / line one", raw: "group one / line one", group: 1, groupHead: true, collapsed: true},
		{kind: "cmdout", content: "group one / line two", raw: "group one / line two", group: 1, collapsed: true},
		{kind: "cmdout", content: "group two", raw: "group two", group: 2, groupHead: true},
		{kind: "subagent_result", content: "子 Agent 结果", raw: "子 Agent 完整结果", collapsed: true},
		{kind: "stream", content: "已完成思考", raw: "已完成思考完整内容", settled: true},
		{kind: "stream", content: "正在思考", raw: "正在思考完整内容"},
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	m = updated.(AgentModel)
	for i := 0; i < len(m.lines)-1; i++ {
		if m.lines[i].collapsed {
			t.Fatalf("first e should expand every collapsible; line %d remains collapsed: %#v", i, m.lines[i])
		}
	}
	if m.lines[len(m.lines)-1].collapsed {
		t.Fatal("active reasoning stream must not be changed by e")
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	m = updated.(AgentModel)
	for i := 0; i < len(m.lines)-1; i++ {
		if !m.lines[i].collapsed {
			t.Fatalf("second e should collapse every collapsible; line %d remains expanded: %#v", i, m.lines[i])
		}
	}
	if m.lines[len(m.lines)-1].collapsed {
		t.Fatal("active reasoning stream must remain visible after the second e")
	}
}

func TestCommandCompletionSchedulesCollapse(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, false, false, StartupInfo{})
	m.applyEvent(harness.UIEvent{Kind: harness.EventAction, Action: &harness.AgentAction{Type: harness.ActionExecute, Command: "seq 3"}})
	m.applyEvent(harness.UIEvent{Kind: harness.EventCommandOutput, Message: "1\n"})

	updated, cmd := m.Update(uiEventMsg(harness.UIEvent{Kind: harness.EventResult, Message: "命令执行完成"}))
	model := updated.(AgentModel)
	if cmd == nil {
		t.Fatal("expected command output collapse timer")
	}
	if model.activeCmdGroup != 0 {
		t.Fatalf("active command group should reset after command completion, got %d", model.activeCmdGroup)
	}
}

func TestAutomaticCommandCollapsePreservesShortOutputAndReadingPosition(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, false, false, StartupInfo{})
	m.startCommandOutputGroup()
	group := m.activeCmdGroup
	m.appendCommandOutputLine("short result", "short result")
	updated, _ := m.Update(cmdOutputCollapseMsg{group: group})
	m = updated.(AgentModel)
	if m.lines[len(m.lines)-1].collapsed {
		t.Fatal("short command output should remain visible")
	}

	for i := 0; i < 20; i++ {
		m.appendCommandOutputLine(fmt.Sprintf("long line %d", i), fmt.Sprintf("long line %d", i))
	}
	m.autoScroll = false
	updated, _ = m.Update(cmdOutputCollapseMsg{group: group})
	m = updated.(AgentModel)
	if m.lines[len(m.lines)-1].collapsed {
		t.Fatal("automatic collapse must not shift content while user is reading above")
	}

	m.autoScroll = true
	updated, _ = m.Update(cmdOutputCollapseMsg{group: group})
	m = updated.(AgentModel)
	if !m.lines[len(m.lines)-1].collapsed {
		t.Fatal("long output should auto-collapse while following the live tail")
	}
}

func TestCommandOutputRepaintsAreCoalesced(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, false, false, StartupInfo{})

	updated, firstCmd := m.Update(uiEventMsg(harness.UIEvent{Kind: harness.EventCommandOutput, Message: "line 1\n"}))
	m = updated.(AgentModel)
	if firstCmd == nil || !m.commandTick {
		t.Fatal("first command line should schedule a coalesced viewport repaint")
	}

	updated, secondCmd := m.Update(uiEventMsg(harness.UIEvent{Kind: harness.EventCommandOutput, Message: "line 2\n"}))
	m = updated.(AgentModel)
	if secondCmd != nil {
		t.Fatal("additional command lines in the same window should reuse the pending repaint")
	}

	updated, _ = m.Update(commandOutputRefreshMsg{})
	m = updated.(AgentModel)
	if m.commandTick {
		t.Fatal("refresh tick should clear the coalescing flag")
	}
	view := stripANSIForTest(m.viewport.View())
	if !strings.Contains(view, "line 1") || !strings.Contains(view, "line 2") {
		t.Fatalf("coalesced repaint lost command output:\n%s", view)
	}
}

func TestStreamCollapseControlsAppearOnlyAfterCompletion(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, false, false, StartupInfo{})
	m.width, m.height = 90, 20
	m.recalcLayout()
	raw := `{"action":"finish","final_report":"最终答案"}`
	m.appendStreamDelta(raw)
	m.refreshViewport()
	view := stripANSIForTest(m.viewport.View())
	if !strings.Contains(view, "正在整理最终报告") {
		t.Fatalf("active stream progress missing:\n%s", view)
	}
	if strings.Contains(view, "[e 全部折叠]") || strings.Contains(view, "[e 全部展开]") {
		t.Fatalf("active stream must not show collapse controls:\n%s", view)
	}
	m.toggleLastCollapsible()
	if m.lines[len(m.lines)-1].collapsed {
		t.Fatal("e must not collapse an active stream")
	}

	m.finalizeStream(raw)
	m.refreshViewport()
	view = stripANSIForTest(m.viewport.View())
	if strings.Contains(view, "[e 全部折叠]") || strings.Contains(view, "[e 全部展开]") {
		t.Fatalf("stream end alone must not expose controls before its result is rendered:\n%s", view)
	}
	m.toggleLastCollapsible()
	if m.lines[len(m.lines)-1].collapsed {
		t.Fatal("e must not collapse a completed stream before its result is rendered")
	}

	updated, _ := m.Update(uiEventMsg(harness.UIEvent{Kind: harness.EventFinish, Message: "最终答案"}))
	m = updated.(AgentModel)
	view = stripANSIForTest(m.viewport.View())
	if !strings.Contains(view, "最终答案") || !strings.Contains(view, "[e 全部折叠]") {
		t.Fatalf("stream should become collapsible only with its rendered result:\n%s", view)
	}
	m.toggleLastCollapsible()
	m.refreshViewport()
	view = stripANSIForTest(m.viewport.View())
	if !strings.Contains(view, "[e 全部展开]") || strings.Contains(view, "[e 全部折叠]") {
		t.Fatalf("collapsed completed stream should expose expand control:\n%s", view)
	}
}

func TestTrimmedCommandGroupStillHasCollapsibleHead(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, false, false, StartupInfo{})
	m.appendLine("user", "You: 最初的对话", "最初的对话")
	m.startCommandOutputGroup()
	for i := 0; i < 1020; i++ {
		m.appendCommandOutputLine(fmt.Sprintf("line %03d", i), fmt.Sprintf("line %03d", i))
	}
	if len(m.lines) != 1000 {
		t.Fatalf("retained lines=%d want 1000", len(m.lines))
	}
	if m.lines[0].kind != "user" || !strings.Contains(m.lines[0].content, "最初的对话") {
		t.Fatalf("initial user conversation was evicted: %#v", m.lines[0])
	}
	if !m.lines[1].groupHead {
		t.Fatal("first retained command row must become the group head after trimming")
	}
	m.collapseCommandOutputGroup(m.activeCmdGroup)
	m.width, m.height = 80, 20
	m.recalcLayout()
	m.refreshViewport()
	if view := stripANSIForTest(m.viewport.View()); !strings.Contains(view, "命令输出已折叠：999 行") {
		t.Fatalf("trimmed group disappeared after collapse:\n%s", view)
	}
}

func TestToolResultKeepsFullDetailAndCanExpand(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, false, false, StartupInfo{})
	m.width, m.height = 90, 24
	m.recalcLayout()
	detail := strings.Join([]string{
		"finding 01", "finding 02", "finding 03", "finding 04", "finding 05",
		"finding 06", "finding 07", "finding 08", "finding 09", "FINAL_EVIDENCE",
	}, "\n")
	m.applyEvent(harness.UIEvent{Kind: harness.EventResult, Message: "tool returned 10 findings...", Detail: detail})
	m.refreshViewport()
	view := stripANSIForTest(m.viewport.View())
	if !strings.Contains(view, "展开完整结果") || strings.Contains(view, "FINAL_EVIDENCE") {
		t.Fatalf("long tool result should start collapsed:\n%s", view)
	}
	m.toggleLastCollapsible()
	m.refreshViewport()
	view = stripANSIForTest(m.viewport.View())
	if !strings.Contains(view, "FINAL_EVIDENCE") || !strings.Contains(view, "[e 全部折叠]") {
		t.Fatalf("expanded tool result lost full detail:\n%s", view)
	}
}

func TestSubAgentResultExpansionShowsFullDetail(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, false, false, StartupInfo{})
	m.width, m.height = 90, 24
	m.recalcLayout()
	detail := strings.Join(append([]string{"summary"}, make([]string, 15)...), "\n") + "\nSUBAGENT_FINAL_EVIDENCE"
	m.applyEvent(harness.UIEvent{Kind: harness.EventSubAgentResult, Message: "worker", Detail: detail})
	m.refreshViewport()
	if strings.Contains(stripANSIForTest(m.viewport.View()), "SUBAGENT_FINAL_EVIDENCE") {
		t.Fatal("sub-agent result should start collapsed")
	}
	m.toggleLastCollapsible()
	m.refreshViewport()
	view := stripANSIForTest(m.viewport.View())
	if !strings.Contains(view, "SUBAGENT_FINAL_EVIDENCE") || !strings.Contains(view, "[e 全部折叠]") {
		t.Fatalf("expanded sub-agent result lost full detail:\n%s", view)
	}
}

func TestStatusContextAvoidsNoActivityPlaceholder(t *testing.T) {
	m := NewAgentModel(nil, "model", "SSH -> 1.2.3.4:22", 30, true, false, StartupInfo{})
	if got := m.statusContextText(); got != "就绪/等待任务" {
		t.Fatalf("idle status=%q", got)
	}
	m.running = true
	if got := m.statusContextText(); got != "执行中" {
		t.Fatalf("running status=%q", got)
	}
	m.thinking = true
	if got := m.statusContextText(); got != "模型思考中" {
		t.Fatalf("thinking status=%q", got)
	}
	m.currentTarget = "web-01 (ssh 10.0.0.1:22)"
	if got := m.statusContextText(); !strings.Contains(got, "web-01") {
		t.Fatalf("target status=%q", got)
	}
}

func TestAwaitUserEventAndAskMsgDeduplicate(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, false, false, StartupInfo{})
	action := &harness.AgentAction{
		Type:     harness.ActionAskUser,
		Question: "请选择 CPU 告警阈值",
		Options:  []string{"CPU阈值 80%", "CPU阈值 90%"},
	}
	m.applyEvent(harness.UIEvent{
		Kind:    harness.EventAwaitUser,
		Message: "请选择 CPU 告警阈值\n\n可选：\n1. CPU阈值 80%\n2. CPU阈值 90%",
		Action:  action,
	})
	// The sink and direct interaction queue can interleave a thought between
	// EventAwaitUser and askMsg. This is the ordering seen in the screenshot.
	m.applyEvent(harness.UIEvent{Kind: harness.EventThought, Message: "先引导用户说明具体需求"})
	updated, _ := m.Update(askMsg{
		action:  action,
		prompt:  action.Question,
		options: action.Options,
		respCh:  make(chan string, 1),
	})
	model := updated.(AgentModel)
	askLines := 0
	for _, line := range model.lines {
		if line.kind == "ask" {
			askLines++
			if strings.Count(line.content, "CPU阈值 80%") != 1 {
				t.Fatalf("option rendered more than once: %q", line.content)
			}
		}
	}
	if askLines != 1 {
		t.Fatalf("expected one ask line, got %d: %#v", askLines, model.lines)
	}
	if model.lines[len(model.lines)-1].kind != "ask" {
		t.Fatalf("deduplicated question should be immediately above input: %#v", model.lines)
	}
}

func TestHistoryNavigationJumpsToTopAndBottom(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, false, false, StartupInfo{})
	m.width, m.height = 80, 14
	m.recalcLayout()
	for i := 0; i < 80; i++ {
		m.appendLine("info", fmt.Sprintf("history line %03d", i), "history")
	}
	m.refreshViewport()
	if m.viewport.YOffset == 0 {
		t.Fatal("test requires scrollable history at the bottom")
	}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	m = updated.(AgentModel)
	if m.viewport.YOffset != 0 || m.autoScroll {
		t.Fatalf("g should jump to history top: offset=%d auto=%v", m.viewport.YOffset, m.autoScroll)
	}
	if got := m.scrollPositionText(); got != "滚动 顶部" {
		t.Fatalf("top scroll status=%q", got)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'G'}})
	m = updated.(AgentModel)
	if !m.viewport.AtBottom() || !m.autoScroll {
		t.Fatalf("G should return to live tail: offset=%d auto=%v", m.viewport.YOffset, m.autoScroll)
	}
	if got := m.scrollPositionText(); got != "滚动 底部" {
		t.Fatalf("bottom scroll status=%q", got)
	}

	// Waiting-for-answer mode keeps the input focused; navigation must still
	// provide direct top/bottom jumps without discarding the draft.
	m.input.Focus()
	m.input.SetValue("draft answer")
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlHome})
	m = updated.(AgentModel)
	if m.viewport.YOffset != 0 || m.input.Value() != "draft answer" {
		t.Fatalf("Ctrl+Home should browse without changing input: offset=%d value=%q", m.viewport.YOffset, m.input.Value())
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlEnd})
	m = updated.(AgentModel)
	if !m.viewport.AtBottom() || m.input.Value() != "draft answer" {
		t.Fatalf("Ctrl+End should return to tail without changing input: offset=%d value=%q", m.viewport.YOffset, m.input.Value())
	}
}

func TestHistoryTrimmingPreservesConversationAndShowsNotice(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, false, false, StartupInfo{})
	m.width, m.height = 90, 20
	m.recalcLayout()
	m.appendLine("user", "You: ORIGINAL_USER_MESSAGE", "ORIGINAL_USER_MESSAGE")
	m.appendLine("ask", "ORIGINAL_QUESTION", "ORIGINAL_QUESTION")
	m.appendLine("success", "ORIGINAL_FINAL_REPORT", "ORIGINAL_FINAL_REPORT")
	for i := 0; i < 1100; i++ {
		m.appendLine("info", fmt.Sprintf("volatile log %04d", i), "volatile")
	}
	for _, want := range []string{"ORIGINAL_USER_MESSAGE", "ORIGINAL_QUESTION", "ORIGINAL_FINAL_REPORT"} {
		found := false
		for _, line := range m.lines {
			if strings.Contains(line.raw, want) || strings.Contains(line.content, want) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("preserved conversation entry %q was trimmed", want)
		}
	}
	if m.trimmedLines == 0 {
		t.Fatal("expected volatile logs to be compacted")
	}
	m.refreshViewport()
	m.viewport.GotoTop()
	if view := stripANSIForTest(m.viewport.View()); !strings.Contains(view, "用户对话、询问和最终结论已保留") {
		t.Fatalf("history compaction notice missing:\n%s", view)
	}
}

func TestInputViewRendersFocusedInputAboveHelpLine(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, true, false, StartupInfo{})
	m.width = 80
	m.height = 24
	m.recalcLayout()
	m.input.SetValue("ni")
	m.input.SetCursor(2)

	view := m.View()
	row, col, ok := m.inputCursorAnchor()
	if !ok {
		t.Fatal("focused input should expose terminal cursor anchor")
	}
	if strings.Contains(view, fmt.Sprintf("\x1b[%d;%dH", row, col)) {
		t.Fatalf("view should not embed cursor movement; anchor must run after renderer")
	}
	if row <= 0 || col <= 0 {
		t.Fatalf("invalid cursor anchor row=%d col=%d", row, col)
	}
	m.scheduleInputCursorAnchor()
	lines := strings.Split(stripANSIForTest(view), "\n")
	var inputLine string
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.Contains(lines[i], "ni") {
			inputLine = lines[i]
			break
		}
	}
	if inputLine == "" {
		t.Fatalf("focused input should render in bordered box, got footer %#v", lines[len(lines)-4:])
	}
	lastLine := strings.TrimRight(lines[len(lines)-1], " ")
	if !strings.Contains(lastLine, "Enter 发送") {
		t.Fatalf("help line should stay below input, got %q", lastLine)
	}
}

func TestFocusedInputCursorAnchorUsesDisplayColumns(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, true, false, StartupInfo{})
	m.width = 80
	m.height = 24
	m.recalcLayout()
	m.input.SetValue("你好abc")
	m.input.SetCursor(2)

	_, col, ok := m.inputCursorAnchor()
	if !ok {
		t.Fatal("expected cursor anchor")
	}
	if col != 7 {
		t.Fatalf("cursor col = %d, want 7 for two CJK runes plus input chrome", col)
	}
	content := m.renderFocusedInputContent(20)
	if !strings.Contains(stripANSIForTest(content), "你好abc") {
		t.Fatalf("focused input should preserve text, got %q", stripANSIForTest(content))
	}
}

func TestInputCursorAnchorStaysOnVisibleInputAcrossResizeAndWrap(t *testing.T) {
	for _, size := range [][2]int{{30, 12}, {80, 24}, {120, 30}} {
		m := NewAgentModel(nil, "model", "local", 30, true, false, StartupInfo{})
		m.width, m.height = size[0], size[1]
		value := strings.Repeat("你好abc", 12)
		m.input.SetValue(value)
		m.input.SetCursor(len([]rune(value)))
		m.recalcLayout()
		row, col, ok := m.inputCursorAnchor()
		if !ok {
			t.Fatalf("size=%v missing cursor anchor", size)
		}
		if row < 1 || row > size[1] || col < 1 || col > size[0] {
			t.Fatalf("size=%v cursor outside terminal: row=%d col=%d", size, row, col)
		}
		// The last visible input row sits directly above the input bottom border
		// and help row, regardless of how many wrapped draft rows are visible.
		if row != size[1]-2 {
			t.Fatalf("size=%v cursor row=%d want %d", size, row, size[1]-2)
		}
	}
}

func TestFocusedInputWrapsAndGrowsWithLongText(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, true, false, StartupInfo{})
	m.width = 36
	m.height = 24
	m.recalcLayout()
	longText := strings.Repeat("你好世界", 8)
	m.input.SetValue(longText)
	m.input.SetCursor(len([]rune(longText)))
	m.recalcLayout()

	rows, cursorRow, _ := m.focusedInputRows(ChromeContentWidth(m.width) - 2)
	if len(rows) < 2 {
		t.Fatalf("expected long focused input to wrap into multiple rows, got %d: %q", len(rows), stripANSIForTest(strings.Join(rows, "\n")))
	}
	if cursorRow != len(rows)-1 {
		t.Fatalf("cursor should stay on visible tail row, cursorRow=%d rows=%d", cursorRow, len(rows))
	}

	view := stripANSIForTest(m.View())
	if !strings.Contains(view, "你好世界") {
		t.Fatalf("wrapped input should render original text, got:\n%s", view)
	}
}

func TestLargePasteStillUsesSummaryInsteadOfExpandingInput(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, true, false, StartupInfo{})
	m.width = 80
	m.height = 24
	m.recalcLayout()
	m.acceptPaste(strings.Repeat("line\n", 30))
	m.recalcLayout()

	rows, _, _ := m.focusedInputRows(ChromeContentWidth(m.width) - 2)
	if len(rows) > 2 {
		t.Fatalf("large paste summary should stay compact, rows=%d text=%q", len(rows), stripANSIForTest(strings.Join(rows, "\n")))
	}
	if !strings.Contains(m.input.Value(), "已粘贴") {
		t.Fatalf("large paste should keep summary, got %q", m.input.Value())
	}
}

func TestPasteInsertsAtCursorAndMovesCursorToEndOfPaste(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, true, false, StartupInfo{})
	m.input.SetValue("hello world")
	m.input.SetCursor(6)

	m.acceptPaste("dear ")

	if got, want := m.input.Value(), "hello dear world"; got != want {
		t.Fatalf("paste should insert at cursor, got %q want %q", got, want)
	}
	if got, want := m.input.Position(), len([]rune("hello dear ")); got != want {
		t.Fatalf("cursor should move after pasted text, got %d want %d", got, want)
	}
}

func TestSlashCommandTabCompletesFirstSuggestion(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, true, false, StartupInfo{})
	m.width = 80
	m.height = 24
	m.recalcLayout()
	m.input.SetValue("/c")
	m.input.SetCursor(2)
	m.recalcLayout()
	withSuggestionsHeight := m.viewport.Height

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	model := updated.(AgentModel)
	if got := model.input.Value(); got != "/clear" {
		t.Fatalf("tab completion = %q, want /clear", got)
	}
	if model.hasSlashSuggestions() {
		t.Fatalf("slash suggestions should close after exact completion")
	}
	if model.slashSuggestionLineCount() != 0 {
		t.Fatalf("suggestion rows should be gone after completion")
	}
	if model.viewport.Height <= withSuggestionsHeight {
		t.Fatalf("viewport height should return after suggestions close: got=%d before=%d", model.viewport.Height, withSuggestionsHeight)
	}
}

func TestSlashCommandSuggestionsRenderAboveInput(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, true, false, StartupInfo{})
	m.width = 80
	m.height = 24
	m.input.SetValue("/c")
	m.input.SetCursor(2)
	m.recalcLayout()

	view := stripANSIForTest(m.View())
	if !strings.Contains(view, "/clear") {
		t.Fatalf("slash suggestions should include /clear: %q", view)
	}
	lines := strings.Split(view, "\n")
	clearIdx, inputIdx, helpIdx := -1, -1, -1
	for i, line := range lines {
		if clearIdx < 0 && strings.Contains(line, "/clear") {
			clearIdx = i
		}
		if inputIdx < 0 && strings.Contains(line, "/c") && !strings.Contains(line, "/clear") {
			inputIdx = i
		}
		if strings.Contains(line, "Enter 发送") {
			helpIdx = i
		}
	}
	if clearIdx < 0 || inputIdx < 0 || helpIdx < 0 {
		t.Fatalf("missing suggestion/input/help rows: clear=%d input=%d help=%d\n%s", clearIdx, inputIdx, helpIdx, view)
	}
	if !(clearIdx < inputIdx && inputIdx < helpIdx) {
		t.Fatalf("expected suggestions above input and help below input, got clear=%d input=%d help=%d", clearIdx, inputIdx, helpIdx)
	}
}

func TestExactSlashCommandDoesNotRenderSuggestionBox(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, true, false, StartupInfo{})
	m.width = 80
	m.height = 24
	m.input.SetValue("/clear")
	m.input.SetCursor(6)
	m.recalcLayout()

	view := stripANSIForTest(m.View())
	if count := strings.Count(view, "/clear"); count != 1 {
		t.Fatalf("exact slash command should appear only in input, got %d occurrences:\n%s", count, view)
	}
	if got, want := m.slashSuggestionLineCount(), 0; got != want {
		t.Fatalf("slash suggestion line count=%d want %d", got, want)
	}
}

func TestSplitSlashCommandKeepsArgument(t *testing.T) {
	cmd, arg := splitSlashCommand("/new 排查 nginx 和 php-fpm")
	if cmd != "new" {
		t.Fatalf("cmd=%q want new", cmd)
	}
	if arg != "排查 nginx 和 php-fpm" {
		t.Fatalf("arg=%q", arg)
	}
}

func TestSlashCommandNamesIncludeNewAndCost(t *testing.T) {
	names := slashCommandNames()
	for _, want := range []string{"/new", "/restart", "/cost", "/tsecbench", "/sudo", "/mcp", "/skill", "/exit", "/quit"} {
		if !strings.Contains(names, want) {
			t.Fatalf("slash commands missing %s: %s", want, names)
		}
	}
}

func TestTSecBenchModePrompt(t *testing.T) {
	prompt := tsecbenchModePrompt("先跑 easy")
	for _, want := range []string{"TSecBench", "tsecbench", "benchmark_base_url", "benchmark_token", "不要明文输出", "先跑 easy"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("tsecbench prompt missing %q: %s", want, prompt)
		}
	}
}

func TestTokenUsagePrefersRealUsageOverEstimate(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, true, false, StartupInfo{})
	m.applyEvent(harness.UIEvent{
		Kind:             harness.EventTokenUsage,
		PromptTokens:     1000,
		CompletionTokens: 250,
		TotalTokens:      1250,
	})
	got := m.tokenUsageLabel(SessionStats{ApproxTokens: 99})
	if got != "1.2k tok" {
		t.Fatalf("token label=%q", got)
	}
}

func TestHeaderStatsExpandsWhenWidthAllows(t *testing.T) {
	stats := SessionStats{
		SessionID:    "session_example_001",
		Turns:        5,
		Messages:     8,
		ApproxTokens: 14200,
	}
	wide := formatHeaderStats(stats, false, "~14.2k tok", 90)
	for _, want := range []string{"会话 sid example_001", "状态 idle", "轮次 5", "消息 8", "token ~14.2k"} {
		if !strings.Contains(wide, want) {
			t.Fatalf("wide header missing %q: %q", want, wide)
		}
	}

	narrow := formatHeaderStats(stats, false, "~14.2k tok", 42)
	if strings.Contains(narrow, "状态") || strings.Contains(narrow, "轮次") {
		t.Fatalf("narrow header should compact labels, got %q", narrow)
	}
	if !strings.Contains(narrow, "sid 001") {
		t.Fatalf("narrow header should keep short session id, got %q", narrow)
	}
}

func TestHeaderShowsModelContextLabel(t *testing.T) {
	title := "custom / qianfan-code-latest · ctx≈131.1K[安全默认]"
	m := NewAgentModel(nil, title, "local", 30, false, false, StartupInfo{})
	header := stripANSIForTest(m.renderHeader(165))
	if !strings.Contains(header, title) {
		t.Fatalf("header lost model context label: %q", header)
	}
}

func TestAgentViewFitsTerminalAcrossSizesAndStates(t *testing.T) {
	type stateCase struct {
		name  string
		setup func(*AgentModel)
	}
	states := []stateCase{
		{
			name: "focused slash menu",
			setup: func(m *AgentModel) {
				m.awaitGoal = true
				m.input.Focus()
				m.input.SetValue("/")
			},
		},
		{
			name: "running noisy command",
			setup: func(m *AgentModel) {
				m.running = true
				m.input.Blur()
				m.applyEvent(harness.UIEvent{Kind: harness.EventAction, Action: &harness.AgentAction{Type: harness.ActionExecute, Command: strings.Repeat("sqlmap --url http://target/?id=1 ", 5)}})
				m.applyEvent(harness.UIEvent{Kind: harness.EventCommandOutput, Message: "10%\r50%\r\x1b[2J100% injectable\x1b[999;1H\n"})
				m.thinking = true
			},
		},
		{
			name: "markdown ask",
			setup: func(m *AgentModel) {
				action := &harness.AgentAction{Type: harness.ActionAskUser, Question: "**请选择目标**\n\n| 名称 | 状态 |\n|---|---|\n| a-05 | available |", Options: []string{"启动 a-05", "取消"}}
				m.applyEvent(harness.UIEvent{Kind: harness.EventAwaitUser, Action: action})
			},
		},
		{
			name: "risk confirmation",
			setup: func(m *AgentModel) {
				m.pendingConfirm = &confirmState{prompt: "**高风险命令**\n\n```sh\n" + strings.Repeat("rm -rf /tmp/example ", 8) + "\n```"}
				m.appendConfirmLine(m.pendingConfirm.prompt)
			},
		},
	}

	for _, width := range []int{1, 2, 4, 7, 20, 30, 48, 79, 80, 120} {
		for _, height := range []int{2, 5, 12, 18, 30} {
			for _, state := range states {
				t.Run(fmt.Sprintf("%s/%dx%d", state.name, width, height), func(t *testing.T) {
					m := NewAgentModel(nil, "provider / model-with-a-long-name", "Fleet 多目标: 12 台远程服务器", 100, true, false, StartupInfo{
						Version: "2.0", ModelInfo: "provider / model-with-a-long-name", ConnInfo: "Fleet 多目标: 12 台", AwaitGoal: true,
					})
					m.width, m.height = width, height
					state.setup(&m)
					m.recalcLayout()
					m.refreshViewport()
					view := m.View()
					if got := lipgloss.Height(view); got > height {
						t.Fatalf("view height=%d exceeds terminal height=%d\n%s", got, height, stripANSIForTest(view))
					}
					for row, line := range strings.Split(view, "\n") {
						if got := lipgloss.Width(line); (width > 1 && got >= width) || got > width {
							t.Fatalf("row %d width=%d reaches terminal autowrap column=%d: %q", row, got, width, stripANSIForTest(line))
						}
					}
				})
			}
		}
	}
}

func TestRunningInputFooterSurvivesLongOutputAndScrolling(t *testing.T) {
	m := NewAgentModel(nil, "provider / model", "local", 100, true, false, StartupInfo{})
	m.width, m.height = 152, 44
	m.running = true
	m.awaitGoal = false
	m.sessionLive = true
	m.input.Blur()
	m.recalcLayout()

	m.applyEvent(harness.UIEvent{
		Kind: harness.EventAction,
		Action: &harness.AgentAction{
			Type:    harness.ActionExecute,
			Command: strings.Repeat("sqlmap --batch --url http://target/?id=1 ", 6),
		},
	})
	for i := 0; i < 120; i++ {
		m.applyEvent(harness.UIEvent{
			Kind:    harness.EventCommandOutput,
			Message: fmt.Sprintf("[%03d] %s 数据库探测结果 🚦\r", i, strings.Repeat("column=value ", 16)),
		})
	}
	m.thinking = true
	m.refreshViewport()

	assertRunningFooterVisible(t, m)

	updated, _ := m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	m = updated.(AgentModel)
	assertRunningFooterVisible(t, m)

	for range 8 {
		updated, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown})
		m = updated.(AgentModel)
	}
	assertRunningFooterVisible(t, m)
}

func TestIdleFocusedInputFooterSurvivesHistoryScrolling(t *testing.T) {
	m := NewAgentModel(nil, "provider / model", "local", 100, true, false, StartupInfo{AwaitGoal: true})
	m.width, m.height = 152, 44
	m.input.Focus()
	for i := 0; i < 160; i++ {
		m.lines = append(m.lines, logLine{
			kind:    "info",
			content: fmt.Sprintf("session_%03d · step %d · restored history", i, i%100),
		})
	}
	m.recalcLayout()
	m.refreshViewport()

	assertFocusedInputFooterVisible(t, m)
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	m = updated.(AgentModel)
	assertFocusedInputFooterVisible(t, m)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	m = updated.(AgentModel)
	assertFocusedInputFooterVisible(t, m)
}

func assertFocusedInputFooterVisible(t *testing.T, m AgentModel) {
	t.Helper()
	view := stripANSIForTest(m.View())
	lines := strings.Split(view, "\n")
	footerStart := max(0, len(lines)-5)
	footer := strings.Join(lines[footerStart:], "\n")
	if !strings.Contains(footer, "task, Enter to start") {
		t.Fatalf("focused input footer is missing from the bottom of the view:\n%s", footer)
	}
}

func assertRunningFooterVisible(t *testing.T, m AgentModel) {
	t.Helper()
	view := m.View()
	lines := strings.Split(view, "\n")
	footerStart := max(0, len(lines)-5)
	footer := stripANSIForTest(strings.Join(lines[footerStart:], "\n"))
	if !strings.Contains(footer, "Agent 执行中...") {
		t.Fatalf("running input footer is missing from the bottom of the view:\n%s", footer)
	}
	for row, line := range lines {
		if got := lipgloss.Width(line); got >= m.width {
			t.Fatalf("row %d width=%d reaches terminal autowrap column=%d", row, got, m.width)
		}
	}
}

func TestConfirmPromptResolvesInline(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, false, false, StartupInfo{})
	m.width, m.height = 80, 20
	m.recalcLayout()
	ch := make(chan bool, 1)
	updated, _ := m.Update(confirmMsg{prompt: "**高风险命令**\n\n`rm -rf /tmp/example`", respCh: ch})
	m = updated.(AgentModel)
	m.refreshViewport()
	if view := stripANSIForTest(m.viewport.View()); !strings.Contains(view, "需要确认") || !strings.Contains(view, "rm -rf") {
		t.Fatalf("confirmation should be visible inside viewport:\n%s", view)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = updated.(AgentModel)
	if approved := <-ch; approved {
		t.Fatal("n should reject confirmation")
	}
	view := stripANSIForTest(m.viewport.View())
	if strings.Contains(view, "Y 批准") || !strings.Contains(view, "已拒绝高风险操作") {
		t.Fatalf("resolved confirmation should become compact history:\n%s", view)
	}
}

func TestConfirmAcceptsUppercaseAndEnterSafelyRejects(t *testing.T) {
	for _, tc := range []struct {
		name     string
		key      tea.KeyMsg
		approved bool
	}{
		{name: "uppercase approve", key: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'Y'}}, approved: true},
		{name: "enter rejects", key: tea.KeyMsg{Type: tea.KeyEnter}, approved: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewAgentModel(nil, "model", "local", 30, false, false, StartupInfo{})
			m.width, m.height = 80, 20
			m.recalcLayout()
			m.input.Focus()
			m.input.SetValue("preserved draft")
			ch := make(chan bool, 1)
			updated, _ := m.Update(confirmMsg{prompt: "confirm", respCh: ch})
			m = updated.(AgentModel)
			if m.inputFocused() {
				t.Fatal("confirmation should temporarily take input focus")
			}
			updated, _ = m.Update(tc.key)
			m = updated.(AgentModel)
			if got := <-ch; got != tc.approved {
				t.Fatalf("approval=%v want %v", got, tc.approved)
			}
			if !m.inputFocused() || m.input.Value() != "preserved draft" {
				t.Fatalf("previous input focus/draft was not restored: focused=%v value=%q", m.inputFocused(), m.input.Value())
			}
		})
	}
}

func TestNormalizeAskAnswerRequiresExactOptionNumber(t *testing.T) {
	options := []string{"first", "second"}
	if got := normalizeAskAnswer("2", options); got != "second" {
		t.Fatalf("exact option number=%q", got)
	}
	if got := normalizeAskAnswer("2abc", options); got != "2abc" {
		t.Fatalf("mixed text must not be mistaken for option number: %q", got)
	}
}

func stripANSIForTest(s string) string {
	var b strings.Builder
	inSeq := false
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if inSeq {
			if (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') {
				inSeq = false
			}
			continue
		}
		if ch == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			inSeq = true
			i++
			continue
		}
		b.WriteByte(ch)
	}
	return b.String()
}
