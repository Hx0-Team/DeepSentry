package harness

import (
	"ai-edr/internal/analyzer"
	"strings"
)

// Keep decision and safety rules in the stable prefix. Full desktop, coding,
// and scheduling playbooks are useful only when those capabilities are used.
const taskExecutionCore = `
【执行效率与验收】
- 优先已有 CLI/API、专用原生工具和文件工具。明确浏览网页时遵守鹰眼路由；只有桌面才能完成的操作用 computer_use，禁止临时编写鼠标/键盘/OCR 驱动。副作用必须遵守授权和风险确认，不能为省轮次合并未经核验的前置动作。
- 同一目的的独立只读调用尽量同轮提出；有依赖的操作必须等待前置结果。已知用法不重复查询 tool_catalog；简单任务不额外 todo/remember。
- 只核验用户要求的目标与字段。当前证据已涵盖这些验收项时立即 finish；不要追加首页、无关统计、重读、截图或另一通道复验来“固定证据”。有矛盾、截断、错误或确实缺少证据时才继续；失败与未核验项必须如实报告。
- 修改本地已有文件用唯一 edit_file，禁止 write_file 覆盖源码；修改后做相关编译/测试，凭据不得写进代码或日志。
- 长任务用 task_context 保存阶段、证据路径和下一步；恢复先核对状态，不重放已提交/发送/删除的动作。等待用 task_wait，不轮询模型；未来/周期要求用 schedule_task 持久化，不能用长 sleep 冒充。file_exists 只证明存在，file_ready 必须校验 expected_size/sha256。不得声称无限上下文或机器关闭后仍会执行。
`

const selfConfigurationGuidance = `
【自身配置任务】
- 用户说“你自己的设置/模型/配置”指控制端 DeepSentry，用 config_manage；不委派子 Agent，不用目标机 Shell/文件工具。
- 不知道当前配置先 status；不知道键名/类型用 describe。知道键与目标值时直接 set/patch，禁止猜键名或重写整份 YAML。关联字段用 patch 的 changes 对象一次提交；模型链用 model_id=primary 或 status 返回的 ID，不能只改顶层字段就宣称主模型已切换。
- patch/set 保留其他设置并自动备份、整体验证、原子写入和回读。dry_run=true 只预览，不能作为已完成的修改。工具报错必须修正实际原因，不能绕过 config_manage。
- SSH 同主机局部修改用 set_ssh（如只改 port），不传的用户名/认证/设备提示符会保留；新主机不要擅自复用旧凭据。SSH 要求测试时用 probe_ssh，只做握手认证、不执行远程命令且不切换当前连接。代理配置是控制端出站 controller_proxy；不是 socks5_proxy/http_proxy 监听服务。更换代理前用 probe_proxy 测候选 value；HEAD 回应只证明路由，不能当作模型认证成功。
- 保存新 SSH 目标后，当前 execute 仍指向原连接；禁止用当前命令结果声称新目标已连接。自动断线重连保留原目标与认证，显式切换需重启后建立新连接。
- 字段和值已明确时直接执行修改，不额外 describe、status 或 dry_run；只有用户要求预览/尚有歧义时才预览。工具已给出“已回读核对”和要求字段的变更/保留证据即可结束；缺少验收字段时才用 get（同样的 key/model_id）补充，批量读取同轮提出。不要重复备份、重复提交或打印完整配置/密钥。接口连通性、MCP 已连接、当前目标已切换必须有实际运行证据；需要重启的项目如实说明，禁止自行重启杀掉当前会话。
`

func taskExecutionGuidance(history []analyzer.Message) string {
	var task strings.Builder
	var invoked strings.Builder
	// Recent user requirements survive long histories without letting large
	// attachments or diagnostic output crowd out the actual task.
	remaining := 12000
	for i := len(history) - 1; i >= 0 && remaining > 0; i-- {
		message := history[i]
		if message.Role == "user" {
			content := message.Content
			if len(content) > remaining {
				content = content[:remaining]
			}
			task.WriteString(content)
			task.WriteByte('\n')
			remaining -= len(content)
		}
		if message.Role == "assistant" {
			for _, call := range message.ToolCalls {
				invoked.WriteString(call.Function.Name)
				invoked.WriteByte('\n')
			}
		}
		if message.Role == "tool" {
			invoked.WriteString(message.Name)
			invoked.WriteByte('\n')
		}
	}
	if strings.TrimSpace(task.String()) == "" {
		return executionEfficiencyPrompt + codingCraftPrompt + longTaskPrompt
	}
	query := strings.ToLower(task.String() + invoked.String())
	has := func(markers ...string) bool {
		for _, marker := range markers {
			if strings.Contains(query, marker) {
				return true
			}
		}
		return false
	}
	guidance := taskExecutionCore
	if analyzer.SelfConfigIntent(task.String()) || has("config_manage") {
		guidance += selfConfigurationGuidance
	}
	if has("computer_use", "桌面", "鼠标", "键盘", "屏幕", "点击", "截图", "ocr", "微信", "客户端", "desktop", "click", "screen") {
		guidance += executionEfficiencyPrompt
	}
	if has("代码", "源码", "编程", "编写", "脚本", "编译", "重构", "edit_file", "write_file", "code", "implement", "refactor", "bug", "patch", "develop") {
		guidance += codingCraftPrompt
	}
	if has("schedule_task", "task_wait", "task_context", "定时", "周期", "每分钟", "每小时", "分钟后", "小时后", "提醒", "等待", "下载", "恢复", "继续", "长任务", "schedule", "download", "wait", "resume") {
		guidance += longTaskPrompt
	}
	return guidance
}
