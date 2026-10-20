package config

import (
	"net/url"
	"strings"
)

// DeepSeekModelInfo mirrors the per-model metadata returned by DeepSeek's
// GET /models endpoint. Keeping it local avoids a startup network dependency
// while still using the documented context/output limits.
type DeepSeekModelInfo struct {
	ID                          string
	ContextWindowTokens         int
	MaxOutputTokens             int
	SupportsVision              bool
	ReasoningEfforts            []string
	DefaultReasoningEffort      string
	AnthropicSystemPromptUpdate string
}

var deepSeekModelCatalog = map[string]DeepSeekModelInfo{
	"deepseek-flash": {
		ID: "deepseek-flash", ContextWindowTokens: 1_048_576, MaxOutputTokens: 393_216,
		SupportsVision: true, ReasoningEfforts: []string{"low", "high", "max"},
		DefaultReasoningEffort: "high", AnthropicSystemPromptUpdate: "in-history",
	},
	"deepseek-v4-pro": {
		ID: "deepseek-v4-pro", ContextWindowTokens: 1_048_576, MaxOutputTokens: 393_216,
		ReasoningEfforts:       []string{"low", "high", "max"},
		DefaultReasoningEffort: "high", AnthropicSystemPromptUpdate: "leading-only",
	},
}

// LookupDeepSeekModel resolves current API IDs, documented historical aliases
// and the Anthropic-compatible claude-* mapping. The mapping is deliberately
// only consulted for DeepSeek endpoints.
func LookupDeepSeekModel(provider, model string) (DeepSeekModelInfo, bool) {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return DeepSeekModelInfo{}, false
	}
	if strings.HasPrefix(model, "claude-opus") {
		return deepSeekModelCatalog["deepseek-v4-pro"], true
	}
	if strings.HasPrefix(model, "claude-") {
		return deepSeekModelCatalog["deepseek-flash"], true
	}
	switch model {
	case "deepseek-flash", "deepseek-v4-flash", "deepseek-v4-flash-vision-exp",
		"deepseek-v4.1-flash", "deepseek-v4.1-flash-expires-on-0910":
		return deepSeekModelCatalog["deepseek-flash"], true
	case "deepseek-v4-pro":
		return deepSeekModelCatalog["deepseek-v4-pro"], true
	default:
		return DeepSeekModelInfo{}, false
	}
}

// IsDeepSeek recognizes both the explicit provider and a custom gateway whose
// endpoint is the official DeepSeek host. Provider-specific request fields are
// only emitted for this scope.
func (c Config) IsDeepSeek() bool {
	if strings.EqualFold(strings.TrimSpace(c.Provider), string(ProviderDeepSeek)) {
		return true
	}
	u, err := url.Parse(strings.TrimSpace(c.ApiURL))
	return err == nil && strings.EqualFold(u.Hostname(), "api.deepseek.com")
}

// EffectiveThinkingMode resolves the DeepSeek thinking switch. DeepSeek's
// documented default is enabled, so auto keeps that behavior.
func (c Config) EffectiveThinkingMode() string {
	if strings.EqualFold(strings.TrimSpace(c.ReasoningEffort), "none") {
		return "disabled"
	}
	mode := strings.ToLower(strings.TrimSpace(c.ThinkingMode))
	switch mode {
	case "disabled", "disable", "off", "false":
		return "disabled"
	case "enabled", "enable", "on", "true":
		return "enabled"
	}
	return "enabled"
}

// EffectiveReasoningEffort maps user-facing values to DeepSeek's documented
// low/high/max levels. Empty means "let the server use its default" (high).
func (c Config) EffectiveReasoningEffort() string {
	if c.EffectiveThinkingMode() == "disabled" {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(c.ReasoningEffort)) {
	case "", "auto":
		return ""
	case "minimal", "low":
		return "low"
	case "medium", "high", "xhigh":
		return "high"
	case "max", "ultra":
		return "max"
	default:
		return ""
	}
}

// deepSeekReservedOutputTokens applies DeepSeek's documented defaults:
// thinking 64K, max effort 128K, non-thinking 16K. An explicit
// reserved_output_tokens always wins.
func (c Config) deepSeekReservedOutputTokens(window int) int {
	if c.ReservedOutputTokens > 0 {
		return c.ReservedOutputTokens
	}
	reserved := 16_384
	if c.EffectiveThinkingMode() == "enabled" {
		reserved = 65_536
		if c.EffectiveReasoningEffort() == "max" {
			reserved = 131_072
		}
	}
	if info, ok := LookupDeepSeekModel(c.Provider, c.ModelName); ok && info.MaxOutputTokens > 0 {
		reserved = minInt(reserved, info.MaxOutputTokens)
	}
	if window > 0 {
		reserved = minInt(reserved, maxInt(1_024, window/4))
	}
	return maxInt(512, reserved)
}
