package analyzer

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"ai-edr/internal/config"
)

// Opt-in integration checks. They call the real, billable DeepSeek API and are
// skipped in normal test runs.
func deepSeekLiveConfig(t *testing.T) config.Config {
	t.Helper()
	if os.Getenv("DEEPSENTRY_LIVE_DEEPSEEK") != "1" {
		t.Skip("set DEEPSENTRY_LIVE_DEEPSEEK=1 to call the real (billable) DeepSeek API")
	}
	if _, err := os.Stat("../../config.yaml"); err != nil {
		t.Skip("config.yaml is not available in this checkout")
	}
	original := config.GlobalConfig
	t.Cleanup(func() { config.GlobalConfig = original })
	if err := config.InitConfig("../../config.yaml"); err != nil {
		t.Fatal(err)
	}
	return config.GlobalConfig
}

func TestDeepSeekLiveNativeToolProtocols(t *testing.T) {
	cfg := deepSeekLiveConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	messages := []Message{{Role: "user", Content: "请必须调用 tool_catalog 工具，参数 name=ping，查看用法后再回答。"}}

	t.Run("chat", func(t *testing.T) {
		result, err := callOpenAICompatible(ctx, cfg, messages, true, nil)
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(result.ToolCallName) == "" || len(result.ToolCalls) == 0 {
			t.Fatalf("no native tool call: content=%q reasoning=%q", result.Content, result.ReasoningContent)
		}
	})

	toHistoryToolCall := func(call LLMToolCall) ToolCall {
		out := ToolCall{ID: call.ID, Type: "function"}
		out.Function.Name = call.Name
		out.Function.Arguments = call.Arguments
		return out
	}

	t.Run("responses", func(t *testing.T) {
		responsesCfg := cfg
		responsesCfg.APIProtocol = config.ProtocolOpenAIResponses
		result, err := callOpenAIResponses(ctx, responsesCfg, messages, true)
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(result.ToolCallName) == "" || len(result.ToolCalls) == 0 {
			t.Fatalf("no Responses function_call: content=%q", result.Content)
		}
		follow := append([]Message(nil), messages...)
		follow = append(follow,
			Message{Role: "assistant", ReasoningContent: result.ReasoningContent, ToolCalls: []ToolCall{toHistoryToolCall(result.ToolCalls[0])}},
			Message{Role: "tool", ToolCallID: result.ToolCalls[0].ID, Name: result.ToolCalls[0].Name, Content: "tool_catalog(name=ping): ping host=... count=4"},
		)
		followed, err := callOpenAIResponses(ctx, responsesCfg, follow, true)
		if err != nil {
			t.Fatal("Responses tool follow-up:", err)
		}
		if strings.TrimSpace(followed.Content) == "" && len(followed.ToolCalls) == 0 {
			t.Fatal("Responses tool follow-up returned no content")
		}
	})

	t.Run("anthropic", func(t *testing.T) {
		anthropicCfg := cfg
		anthropicCfg.APIProtocol = config.ProtocolAnthropicMessages
		result, err := callAnthropic(ctx, anthropicCfg, messages, true)
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(result.ToolCallName) == "" || len(result.ToolCalls) == 0 {
			t.Fatalf("no Anthropic tool_use: content=%q", result.Content)
		}
		follow := append([]Message(nil), messages...)
		follow = append(follow,
			Message{Role: "assistant", ReasoningContent: result.ReasoningContent, ToolCalls: []ToolCall{toHistoryToolCall(result.ToolCalls[0])}},
			Message{Role: "tool", ToolCallID: result.ToolCalls[0].ID, Name: result.ToolCalls[0].Name, Content: "tool_catalog(name=ping): ping host=... count=4"},
		)
		followed, err := callAnthropic(ctx, anthropicCfg, follow, true)
		if err != nil {
			t.Fatal("Anthropic tool follow-up:", err)
		}
		if strings.TrimSpace(followed.Content) == "" && len(followed.ToolCalls) == 0 {
			t.Fatal("Anthropic tool follow-up returned no content")
		}
	})
}
