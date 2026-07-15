package harness

import (
	"strings"

	"ai-edr/internal/analyzer"
)

const multiTurnFollowUpPrompt = `
【多轮会话 · 追问模式】
用户在同一 Session 内延续上一轮对话（类似 Claude Code 连续追问）。
- 结合上文结论、命令输出与 final_report，勿从零重复已完成工作。
- 若用户仅追问细节/解释，可基于已有证据直接 finish。
- 若需新取证，继续 execute/tool；finish 仅表示本回合答复完毕，Session 仍可继续。
`

// MultiTurnExtraPrompt 多轮追问时注入的 system 补充
func MultiTurnExtraPrompt(multiTurn bool, history *[]analyzer.Message) string {
	if !multiTurn || history == nil || CountUserTurns(*history) < 2 {
		return ""
	}
	return multiTurnFollowUpPrompt
}

// CountUserTurns 统计用户发言轮次
func CountUserTurns(history []analyzer.Message) int {
	n := 0
	for _, m := range history {
		if isRealUserTurn(m) {
			n++
		}
	}
	return n
}

// isRealUserTurn separates actual user input from tool feedback and control
// messages. Those observations intentionally use role=user for chat API
// compatibility, but they are not conversation turns.
func isRealUserTurn(m analyzer.Message) bool {
	if m.Role != "user" {
		return false
	}
	content := strings.TrimSpace(m.Content)
	for _, prefix := range []string{
		"Output:",
		"系统警告:",
		"【系统】",
		"上一步执行失败:",
		"用户拒绝执行",
	} {
		if strings.HasPrefix(content, prefix) {
			return false
		}
	}
	return content != ""
}

// CommitFinishToHistory 将 finish 结论写入 history，供下一轮追问引用
func CommitFinishToHistory(history *[]analyzer.Message, action AgentAction, report string) {
	if history == nil {
		return
	}
	report = strings.TrimSpace(report)
	if report == "" {
		report = strings.TrimSpace(action.Thought)
	}
	*history = append(*history, analyzer.Message{
		Role:    "assistant",
		Content: actionToJSON(action),
	})
	if report != "" {
		*history = append(*history, analyzer.Message{
			Role:    "system",
			Content: "【系统】本轮已结束。以下是结论摘要，供后续追问参考：\n" + report,
		})
	}
}
