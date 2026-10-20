package analyzer

import (
	"ai-edr/internal/config"
	"context"
	"testing"
)

func BenchmarkNativeToolDefinitionsForRequest(b *testing.B) {
	cfg := config.Config{AgentRuntime: "v3", Provider: "custom", APIProtocol: config.ProtocolOpenAIChat, ApiURL: "https://example.com/v1", ModelName: "large", ContextWindowTokens: 1_048_576}
	messages := []Message{
		{Role: "system", Content: "【本任务已验证工具】后续轮次优先保留这些工具的完整 schema: pcap_analyze, webshell_hunt"},
		{Role: "user", Content: "继续分析服务器日志并检查异常网络连接"},
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if defs := nativeToolDefinitionsForRequest(cfg, messages); len(defs) == 0 {
			b.Fatal("empty tool definitions")
		}
	}
}

func BenchmarkRequestToolPreparation(b *testing.B) {
	cfg := config.Config{AgentRuntime: "v3", Provider: "custom", ModelName: "large", ContextWindowTokens: 1_048_576}
	messages := []Message{{Role: "system", Content: "【本任务已验证工具】schema: pcap_analyze, webshell_hunt"}, {Role: "user", Content: "继续分析服务器日志并检查异常网络连接"}}
	for _, reuse := range []bool{false, true} {
		name := "Uncached"
		if reuse {
			name = "PerStepReuse"
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				ctx := context.Background()
				if reuse {
					ctx = withRequestToolsCache(ctx)
				}
				for phase := 0; phase < 3; phase++ {
					prepared, err := prepareNativeTools(ctx, cfg, messages)
					if err != nil || prepared.count == 0 {
						b.Fatal(err)
					}
				}
			}
		})
	}
}
