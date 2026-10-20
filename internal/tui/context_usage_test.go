package tui

import (
	"ai-edr/internal/analyzer"
	"ai-edr/internal/harness"
	"strings"
	"testing"
)

func TestContextAndCacheDiagnosticsUseBoundarySnapshots(t *testing.T) {
	m := NewAgentModel(nil, "model", "local", 30, true, false, StartupInfo{})
	u := analyzer.ContextUsage{SystemTokens: 1000, ToolSchemaTokens: 500, HistoryTokens: 2000, ImageTokens: 1500, ToolCount: 4, TotalTokens: 3500, WindowTokens: 32000, HistoryBudgetTokens: 8000}
	m.applyEvent(harness.UIEvent{Kind: harness.EventContextUsage, ContextUsage: &u})
	u.TotalTokens = 1
	text := m.contextStatsText()
	for _, want := range []string{"3500", "1500", "8000", "本地估算"} {
		if !strings.Contains(text, want) {
			t.Fatal(text)
		}
	}
	m.recordTokenUsage(harness.UIEvent{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120, CacheReadTokens: 80, CacheUsageReported: true})
	if !strings.Contains(m.cacheUsageText(), "80 token") || m.tokenUsage.TotalTokens != 120 {
		t.Fatal("cache counted twice or missing")
	}
}
