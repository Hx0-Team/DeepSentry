package tui

import "fmt"

func (m AgentModel) cacheUsageText() string {
	if m.tokenUsage.CacheCalls == 0 {
		return "\n缓存用量：服务未返回缓存明细"
	}
	return fmt.Sprintf("\n缓存读取: %d token · 缓存写入: %d token · 返回缓存明细的调用: %d/%d · reasoning: %d token（已包含在 completion 中）",
		m.tokenUsage.CacheReadTokens, m.tokenUsage.CacheWriteTokens, m.tokenUsage.CacheCalls, m.tokenUsage.Calls, m.tokenUsage.ReasoningTokens)
}

func (m AgentModel) contextStatsText() string {
	u := m.contextUsage
	if u == nil {
		return "尚无模型请求的上下文快照；/context 不调用模型。实际累计消耗请用 /cost 查看。"
	}
	return fmt.Sprintf("最近模型请求的上下文（本地估算，不是计费用量）：\n系统提示: ~%d · 工具 schema: ~%d（%d个）· 历史: ~%d（其中图片 ~%d）\n合计: ~%d / 模型窗口 %d（%.1f%%）· 工作历史预算: %d\n这是请求边界快照；运行中不读取正在修改的历史。实际 token 与缓存明细请看 /cost。",
		u.SystemTokens, u.ToolSchemaTokens, u.ToolCount, u.HistoryTokens, u.ImageTokens, u.TotalTokens, u.WindowTokens,
		100*float64(u.TotalTokens)/float64(max(1, u.WindowTokens)), u.HistoryBudgetTokens)
}
