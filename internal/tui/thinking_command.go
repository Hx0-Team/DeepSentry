package tui

import (
	"fmt"
	"strconv"
	"strings"

	"ai-edr/internal/config"
)

func (m *AgentModel) handleThinkingSlash(arg string) {
	action := strings.ToLower(strings.TrimSpace(arg))
	if !config.GlobalConfig.IsDeepSeek() {
		m.appendLine("info", "当前主模型不是 DeepSeek；thinking_mode/reasoning_effort 只会对 DeepSeek 端点生效。", "thinking")
		return
	}
	fields := strings.Fields(action)
	verb := ""
	if len(fields) > 0 {
		verb = fields[0]
	}
	rest := ""
	if len(fields) > 1 {
		rest = fields[1]
	}
	switch verb {
	case "", "status":
		m.appendLine("info", thinkingStatusText(config.GlobalConfig), "thinking status")
	case "on", "enable", "开启":
		m.applyThinkingPatch("enabled", "auto")
	case "off", "disable", "关闭":
		m.applyThinkingPatch("disabled", "auto")
	case "auto":
		m.applyThinkingPatch("auto", "auto")
	case "low", "high", "max":
		m.applyThinkingPatch("enabled", verb)
	case "budget", "output", "max-tokens", "max_tokens":
		if rest == "" {
			m.appendLine("info", "用法: /thinking budget <tokens|64k|128k|auto>\n"+thinkingStatusText(config.GlobalConfig), arg)
			return
		}
		m.applyOutputBudget(rest)
	case "mode":
		switch rest {
		case "on", "enabled", "开启":
			m.applyThinkingPatch("enabled", "auto")
		case "off", "disabled", "关闭":
			m.applyThinkingPatch("disabled", "auto")
		case "auto":
			m.applyThinkingPatch("auto", "auto")
		default:
			m.appendLine("info", "用法: /thinking mode on|off|auto", arg)
		}
	case "effort":
		switch rest {
		case "low", "high", "max":
			m.applyThinkingPatch("enabled", rest)
		case "none":
			m.applyThinkingPatch("disabled", "auto")
		case "auto", "":
			m.applyThinkingPatch("enabled", "auto")
		default:
			m.appendLine("info", "用法: /thinking effort low|high|max|none|auto", arg)
		}
	default:
		m.appendLine("info", "用法: /thinking [status|on|off|auto|low|high|max|budget <tokens|auto>]\n"+thinkingStatusText(config.GlobalConfig), arg)
	}
}

func (m *AgentModel) applyThinkingPatch(mode, effort string) {
	out, err := config.ManageConfig(map[string]string{
		"action":  "patch",
		"changes": fmt.Sprintf(`{"thinking_mode":%q,"reasoning_effort":%q}`, mode, effort),
	})
	if err != nil {
		m.appendLine("error", "更新思考模式失败: "+err.Error(), err.Error())
		return
	}
	m.appendLine("result", "已更新 DeepSeek 思考模式。\n"+thinkingStatusText(config.GlobalConfig), out)
}

func (m *AgentModel) applyOutputBudget(raw string) {
	value, err := parseOutputBudget(raw)
	if err != nil {
		m.appendLine("error", err.Error(), raw)
		return
	}
	out, err := config.ManageConfig(map[string]string{
		"action": "set",
		"key":    "reserved_output_tokens",
		"value":  strconv.Itoa(value),
	})
	if err != nil {
		m.appendLine("error", "更新输出预算失败: "+err.Error(), err.Error())
		return
	}
	m.appendLine("result", "已更新 DeepSeek 输出预算。\n"+thinkingStatusText(config.GlobalConfig), out)
}

func parseOutputBudget(raw string) (int, error) {
	raw = strings.ToLower(strings.TrimSpace(raw))
	if raw == "" || raw == "auto" || raw == "0" {
		return 0, nil
	}
	multiplier := 1
	if strings.HasSuffix(raw, "k") {
		multiplier = 1024
		raw = strings.TrimSpace(strings.TrimSuffix(raw, "k"))
	} else if strings.HasSuffix(raw, "m") {
		multiplier = 1024 * 1024
		raw = strings.TrimSpace(strings.TrimSuffix(raw, "m"))
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("输出预算必须是正整数、64k/128k 或 auto，收到 %q", raw)
	}
	if n > 1_048_576 {
		return 0, fmt.Errorf("输出预算必须是 0(auto) 或 512-1048576 tokens")
	}
	value := n * multiplier
	if value != 0 && (value < 512 || value > 1_048_576) {
		return 0, fmt.Errorf("输出预算必须是 0(auto) 或 512-1048576 tokens，收到 %d", value)
	}
	return value, nil
}

func thinkingStatusText(cfg config.Config) string {
	mode := "开启"
	if cfg.EffectiveThinkingMode() == "disabled" {
		mode = "关闭"
	}
	effort := cfg.EffectiveReasoningEffort()
	if effort == "" {
		effort = "默认 high"
	}
	caps := cfg.EffectiveModelCapabilities()
	output := fmt.Sprintf("%d", caps.ReservedOutputTokens)
	if caps.ReservedOutputTokens >= 1024 {
		output = fmt.Sprintf("%.0fK", float64(caps.ReservedOutputTokens)/1024)
	}
	budgetSource := "自动"
	if cfg.ReservedOutputTokens > 0 {
		budgetSource = "显式"
	}
	modelLimit := ""
	if info, ok := config.LookupDeepSeekModel(cfg.Provider, cfg.ModelName); ok && info.MaxOutputTokens > 0 {
		modelLimit = fmt.Sprintf(" · 模型上限 %.0fK", float64(info.MaxOutputTokens)/1024)
	}
	timeoutAdvice := ""
	if cfg.EffectiveReasoningEffort() == "max" && cfg.EffectiveLLMTimeout() < 300 {
		timeoutAdvice = "\n提示: max effort 推理较慢，建议将 llm_timeout_sec 设为 300 或更高。"
	}
	return fmt.Sprintf(`DeepSeek 思考模式: %s
推理强度: %s · 输出预算: %s（%s）%s · 上下文: %d · LLM 超时: %ds%s
切换: /thinking on | off | low | high | max | auto | budget 64k | budget auto
说明: 思考模式不支持 temperature；关闭思考后 temperature/top_p 才会生效。响应中的 reasoning_content 会在工具调用链中完整回传。`, mode, effort, output, budgetSource, modelLimit, caps.ContextWindowTokens, cfg.EffectiveLLMTimeout(), timeoutAdvice)
}
