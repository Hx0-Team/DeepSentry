package analyzer

import (
	"ai-edr/internal/config"
	"context"
)

// Local estimates explain request composition, never substitute for billed
// provider usage. The caller publishes a snapshot at the model boundary.
type ContextUsage struct {
	SystemTokens        int `json:"system_tokens"`
	ToolSchemaTokens    int `json:"tool_schema_tokens"`
	HistoryTokens       int `json:"history_tokens"`
	ImageTokens         int `json:"image_tokens"` // included in history_tokens
	ToolCount           int `json:"tool_count"`
	TotalTokens         int `json:"total_tokens"`
	WindowTokens        int `json:"window_tokens"`
	HistoryBudgetTokens int `json:"history_budget_tokens"`
}

func inspectContextUsageForRequest(ctx context.Context, system string, history []Message, native bool, budget int) ContextUsage {
	u := ContextUsage{SystemTokens: EstimateTextTokens(system), HistoryTokens: EstimateMessagesTokens(history), HistoryBudgetTokens: budget, WindowTokens: config.GlobalConfig.EffectiveModelCapabilities().ContextWindowTokens}
	for _, message := range history {
		for _, attachment := range message.Attachments {
			u.ImageTokens += estimateImageAttachmentTokens(attachment)
		}
	}
	if native && config.GlobalConfig.IsOpenAICompatible() {
		messages := append([]Message{{Role: "system", Content: system}}, history...)
		if prepared, err := prepareNativeTools(ctx, config.GlobalConfig, messages); err == nil {
			u.ToolCount = prepared.count
			u.ToolSchemaTokens = prepared.tokens
		}
	}
	u.TotalTokens = u.SystemTokens + u.ToolSchemaTokens + u.HistoryTokens
	return u
}
