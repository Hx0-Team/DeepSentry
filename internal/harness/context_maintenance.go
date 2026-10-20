package harness

import (
	"ai-edr/internal/analyzer"
	"ai-edr/internal/ui"
	"fmt"
)

func contextMaintenanceMessage(event analyzer.ContextMaintenanceEvent) string {
	label := "模型上下文不足或摘要失败，已机械保留目标/线索/最近步骤"
	suffix := ""
	switch event.Kind {
	case analyzer.ContextLocalPrune:
		label = "已本地清理较早工具输出"
		suffix = "；未调用摘要模型"
	case analyzer.ContextModelSummary:
		label = "已调用模型生成分层上下文摘要"
	}
	return fmt.Sprintf("%s%s（约 %d → %d tokens%s）", ui.Prefix("🧠", "[MEM]"), label, event.BeforeTokens, event.AfterTokens, suffix)
}
