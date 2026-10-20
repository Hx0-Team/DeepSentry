package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ai-edr/internal/analyzer"
	"ai-edr/internal/balance"
	"ai-edr/internal/harness"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

func TestCodeRowsSeparateHeadingsAndPreserveExecutableText(t *testing.T) {
	command := `printf '%s\n' '🖥系统|a#b' | sed 's/#/##/' # 保留管道和引号`
	md := "```sh\n-- 🖥系统与进程\n" + command + "\n-- ⚙️服务自愈\n```"
	plain := stripANSIForTest(renderMarkdownBlocks(md, 160))
	for _, want := range []string{"-- 🖥 系统与进程", "-- ⚙️ 服务自愈", command} {
		if !strings.Contains(plain, want) {
			t.Fatalf("missing original command or spaced caption %q: %s", want, plain)
		}
	}
	if strings.Contains(plain, "│") {
		t.Fatal("code block has copy-polluting vertical borders")
	}
	if at := shellCommentStart(command); at != strings.Index(command, "# 保留") {
		t.Fatal("treated a quoted # / URL fragment as a comment", at)
	}
	if at := shellCommentStart(`curl https://example.test/page#anchor`); at != -1 {
		t.Fatal("treated URL fragment as comment")
	}
	inline := stripANSIForTest(renderInlineMarkdown("执行 `printf '🖥系统|a'` 后查看 ✅结果"))
	if !strings.Contains(inline, "printf '🖥系统|a'") || !strings.Contains(inline, "✅ 结果") {
		t.Fatal("prose spacing modified inline command", inline)
	}
}

func TestToolPanelAndRawCopyExcludeChromeWithoutStrippingPipes(t *testing.T) {
	command := "python3 -c \"\nimport json\nfor k,v in {'x|y':1}.items():\n    print(k, v)\n\""
	m := NewAgentModel(nil, "test", "local", 30, true, false, StartupInfo{})
	m.width, m.height = 80, 24
	m.recalcLayout()
	m.applyEvent(harness.UIEvent{Kind: harness.EventAction, Action: &harness.AgentAction{Type: harness.ActionExecute, Command: command, TargetProtocol: "local"}})
	m.refreshViewport()
	if strings.ContainsAny(m.viewportPlain, "│╭╮╰╯") {
		t.Fatalf("invocation still has vertical copy decoration: %s", m.viewportPlain)
	}
	if got := m.lastCopyPayload("command"); got != command {
		t.Fatalf("raw copy changed indentation / pipes / newlines: %q", got)
	}
	md := "原文\n```python\nif x:\n    print('a|b')\n```"
	m.appendLine("success", md, md)
	if got := m.lastCopyPayload("code"); got != "if x:\n    print('a|b')" {
		t.Fatalf("code copy added markers: %q", got)
	}
	if got := m.lastCopyPayload(""); got != command {
		t.Fatal("default copy must prefer original command")
	}
	tabbed := "if x:\n\tprint('a|b')"
	if got := lastFencedCode("```python\n" + tabbed + "\n```"); got != tabbed {
		t.Fatalf("source-copy expanded tabs: %q", got)
	}
	if got := sanitizeCopyPayload("\x1b]0;title\a" + tabbed + "\x1b[2J"); got != tabbed {
		t.Fatalf("unsafe terminal control copied: %q", got)
	}
	m.lines = nil
	m.startCommandOutputGroup()
	m.appendCommandOutput("first output\n    second output")
	m.refreshViewport()
	if strings.Contains(m.viewportPlain, "[输出]") || !strings.Contains(m.viewportPlain, "└") || !strings.Contains(m.viewportPlain, "    second output") {
		t.Fatal("classic output style or indentation changed", m.viewportPlain)
	}
	confirmation := stripANSIForTest(renderMarkdownConfirm("```python\nif x:\n    print('a|b')\n```", "[13:00:00] ", 80))
	if strings.ContainsAny(confirmation, "│╭╮╰╯") || !strings.Contains(confirmation, "a|b") || !strings.Contains(confirmation, "N/Esc 拒绝") {
		t.Fatal("confirmation code includes side borders or lost action choices")
	}
}

func TestClassicBlueChromeAndMouseSelectionFollowExactHeaderHeight(t *testing.T) {
	history := []analyzer.Message{{Role: "user", Content: "test"}}
	m := NewAgentModel(newSessionController(SessionConfig{History: &history}), "deepseek / deepseek-flash · ctx=1.00M[配置]", "SSH → 192.0.2.10:2222", 30, true, false, StartupInfo{})
	m.balance.result = balance.Result{Status: balance.Ready, Entries: []balance.Entry{{Amount: 12.34, Unit: "CNY"}}}
	m.width, m.height = 120, 30
	m.recalcLayout()
	m.viewportPlain = "first\nsecond"
	m.viewport.SetContent(m.viewportPlain)
	header := stripANSIForTest(m.renderHeader(TerminalRenderWidth(m.width)))
	rows := strings.Split(header, "\n")
	if len(rows) != 1 || !strings.Contains(rows[0], "余额 12.34 CNY") || !strings.Contains(rows[0], "tok") || !strings.Contains(rows[0], "DeepSentry Agent") || strings.Contains(header, "//") {
		t.Fatal("classic single header or balance missing", header)
	}
	if _, _, ok := m.mouseToContentCoord(2, 0); ok {
		t.Fatal("header treated as selectable body")
	}
	if row, col, ok := m.mouseToContentCoord(2, 1); !ok || row != 0 || col != 1 {
		t.Fatalf("selection shifted with classic header: %d/%d/%t", row, col, ok)
	}
	// Check the classic header with the tallest normal footer variants.
	for _, draft := range []string{"/", strings.Repeat("多行输入 ", 30) + "\n第二行\n第三行"} {
		m.width, m.height = 80, 18
		m.input.SetValue(encodeInputValue(draft))
		m.recalcLayout()
		m.refreshViewport()
		if got := lipgloss.Height(m.View()); got != 18 {
			t.Fatalf("classic header and draft/footer overflow: %d", got)
		}
	}
	m.input.SetValue("")
	for _, size := range [][2]int{{80, 24}, {120, 30}, {168, 36}, {60, 18}, {80, 12}, {30, 12}} {
		m.width, m.height = size[0], size[1]
		m.recalcLayout()
		m.refreshViewport()
		view := m.View()
		if lipgloss.Height(view) != size[1] {
			t.Fatalf("footer clipped / frame height mismatch at %v", size)
		}
		for _, line := range strings.Split(view, "\n") {
			if displayWidth(line) >= size[0] {
				t.Fatalf("row overflow at %v", size)
			}
		}
	}
	m.width, m.height = 120, 30
	m.currentTarget = ""
	m.running = true
	m.pendingConfirm = &confirmState{}
	status := stripANSIForTest(m.renderStatusLine(TerminalRenderWidth(m.width), time.Date(2026, 10, 14, 13, 0, 0, 0, time.UTC)))
	if !strings.Contains(status, "等待确认") || !strings.Contains(status, "13:00:00") {
		t.Fatal("target hid actionable status or right-side clock", status)
	}
}

// Save frames produced by the actual production renderer for visual review.
// The sample account and address are fixtures; no network or model is called.
func TestUILayoutReviewFrames(t *testing.T) {
	dir := os.Getenv("DEEPSENTRY_UI_REVIEW_DIR")
	if dir == "" {
		t.Skip("visual review artifact export not requested")
	}
	t.Setenv("DEEPSENTRY_FANCY", "1")
	t.Setenv("DEEPSENTRY_COLOR", "1")
	t.Setenv("DEEPSENTRY_PLAIN", "")
	t.Setenv("TERM", "xterm-256color")
	oldProfile := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(oldProfile); applyTerminalPalette(darkTerminalPalette()) })
	report := "## 排查结论\n服务已恢复，以下命令用于后续复核。\n\n```sh\n-- 🐳Docker 排查\ndocker ps -a                    # 容器状态\ndocker stats --no-stream         # CPU / 内存\n\n-- 🖥系统与进程\ntop -b -n1 | head -20            # 负载快照\nps -eo pid,user,%cpu,%mem,command --sort=-%cpu | head -15\n\n-- ⚙️服务自愈\nsystemctl status example.service\n```\n\n建议确认重启次数和应用健康检查。"
	command := "python3 -c \"\nimport json\nfor k,v in {'x|y':1}.items():\n    print(k, v)\n\""
	for _, theme := range []string{"dark", "light"} {
		if theme == "dark" {
			applyTerminalPalette(darkTerminalPalette())
		} else {
			applyTerminalPalette(lightTerminalPalette())
		}
		for _, size := range [][2]int{{80, 24}, {120, 30}, {168, 36}} {
			history := []analyzer.Message{{Role: "user", Content: "请排查服务"}}
			m := NewAgentModel(newSessionController(SessionConfig{History: &history}), "deepseek / deepseek-flash · ctx=1.00M[配置]", "SSH → 192.0.2.10:2222", 30, true, false, StartupInfo{})
			m.width, m.height = size[0], size[1]
			m.done = true
			m.awaitGoal = false
			m.input.Blur()
			m.balance.result = balance.Result{Status: balance.Ready, Entries: []balance.Entry{{Amount: 12.34, Unit: "CNY"}}}
			m.recalcLayout()
			m.appendLine("success", report, report)
			m.refreshViewport()
			m.viewport.GotoTop()
			_, frame := stripFrameMarkers([]byte(m.View()))
			name := fmt.Sprintf("%s-%dx%d", theme, size[0], size[1])
			if err := os.MkdirAll(dir, 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, name+".ansi"), frame, 0644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, name+".txt"), []byte(stripANSIForTest(string(frame))), 0644); err != nil {
				t.Fatal(err)
			}
			m.lines = nil
			m.applyEvent(harness.UIEvent{Kind: harness.EventAction, Action: &harness.AgentAction{Type: harness.ActionExecute, Command: command, TargetProtocol: "local"}})
			m.refreshViewport()
			_, frame = stripFrameMarkers([]byte(m.View()))
			if err := os.WriteFile(filepath.Join(dir, name+"-command.ansi"), frame, 0644); err != nil {
				t.Fatal(err)
			}
			m.startCommandOutputGroup()
			m.appendCommandOutput("service=example.service\nstate=active\n    process: example-worker")
			m.appendLine("subagent_start", "Sub-agent · log-review → 检查近期错误", "log-review")
			m.appendLine("info", "[信息] 已保留原始输出，可继续复核。", "info")
			m.refreshViewport()
			m.viewport.GotoTop()
			_, frame = stripFrameMarkers([]byte(m.View()))
			if err := os.WriteFile(filepath.Join(dir, name+"-modules.ansi"), frame, 0644); err != nil {
				t.Fatal(err)
			}
		}
	}
}
