package tui

import (
	"fmt"
	"strings"

	"ai-edr/internal/config"
	"ai-edr/internal/tools"
)

func (m *AgentModel) handleSearchSlash(arg string) {
	action := strings.ToLower(strings.TrimSpace(arg))
	// Keep the live tool gate aligned with the current config even if the
	// switch was changed through config_manage instead of /search.
	tools.SetWebSearchEnabled(config.GlobalConfig.Search.EffectiveEnabled(config.GlobalConfig))
	switch action {
	case "", "status":
		m.appendLine("info", searchStatusText(config.GlobalConfig), "search status")
	case "on", "enable", "开启":
		if !config.GlobalConfig.Search.Configured(config.GlobalConfig) {
			m.appendLine("info", "无法开启联网搜索：缺少可用的 DeepSeek Search 凭据。\n"+searchCredentialGuidance(), "search on")
			return
		}
		out, err := config.ManageConfig(map[string]string{"action": "set", "key": "search.enabled", "value": "true"})
		if err != nil {
			m.appendLine("error", "开启联网搜索失败: "+err.Error(), err.Error())
			return
		}
		config.GlobalConfig.Search.Enabled = true
		tools.SetWebSearchEnabled(true)
		m.appendLine("result", "已开启 DeepSeek 联网搜索。Agent 会在需要实时网页证据时按需发现并调用 web_search。\n"+searchStatusText(config.GlobalConfig), out)
	case "off", "disable", "关闭":
		out, err := config.ManageConfig(map[string]string{"action": "set", "key": "search.enabled", "value": "false"})
		if err != nil {
			m.appendLine("error", "关闭联网搜索失败: "+err.Error(), err.Error())
			return
		}
		config.GlobalConfig.Search.Enabled = false
		tools.SetWebSearchEnabled(false)
		m.appendLine("result", "已关闭 DeepSeek 联网搜索；web_search 不再出现在工具目录中，也不会发起外部搜索请求。", out)
	default:
		m.appendLine("info", "用法: /search [status|on|off]\n/search 查看当前状态；/search on 开启；/search off 关闭。开关会写入 config.yaml，立即生效。", arg)
	}
}

func searchStatusText(cfg config.Config) string {
	state := "已关闭"
	switch {
	case cfg.Search.Enabled && cfg.Search.Configured(cfg):
		state = "已开启"
	case cfg.Search.Enabled:
		state = "已开启但未生效（缺少凭据或端点）"
	}
	if cfg.Search.EffectiveEnabled(cfg) {
		if _, ok := tools.Get("web_search"); ok {
			state += " · 工具可用"
		} else {
			state += " · 工具被 enabled_tools/disabled_tools 过滤"
		}
	}
	endpoint := cfg.Search.EffectiveBaseURL(cfg)
	if endpoint == "" {
		endpoint = "未解析（需 DeepSeek 主模型或 search.base_url）"
	}
	sourceLabel := "未配置"
	if strings.TrimSpace(cfg.Search.APIKey) != "" {
		sourceLabel = "search.api_key"
	} else if cfg.Search.EffectiveAPIKey(cfg) != "" {
		sourceLabel = "复用主模型 DeepSeek API Key"
	}
	content := "关闭"
	if cfg.Search.NeedContent {
		content = "开启"
	}
	return fmt.Sprintf(`联网搜索: %s
接口: POST %s
凭据: %s
默认条数: %d · 正文: %s · 超时: %s · 计费: 5 元/1000 次成功请求
加载方式: 按需加载，仅在工具目录检索命中时注入 schema；不会每轮占用上下文
数据: 搜索词与网页结果会发送到 DeepSeek
切换: /search on | /search off`, state, endpoint, sourceLabel, cfg.Search.EffectiveCount(), content, cfg.Search.EffectiveTimeout())
}

func searchCredentialGuidance() string {
	return "主模型为 DeepSeek 时会自动复用 api_key；其它主模型请在 config.yaml 的 search 段设置 api_key（base_url 默认 https://api.deepseek.com），或让 Agent 用 config_manage patch {\"search.api_key\":\"...\"} 写入（尽量在本地编辑，避免密钥进入对话）。"
}
