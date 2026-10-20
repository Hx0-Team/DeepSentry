package harness

import (
	"ai-edr/internal/analyzer"
	"ai-edr/internal/config"
	"ai-edr/internal/skills"
	"encoding/json"
	"fmt"
	"testing"
)

// A deterministic payload audit, without paying for provider calls. Keep the
// fixture unchanged so release notes can compare request sizes across versions.
func TestTokenFootprint(t *testing.T) {
	old := config.GlobalConfig
	t.Cleanup(func() { config.GlobalConfig = old })
	catalog := &skills.SkillCatalog{}
	for i := 0; i < 100; i++ {
		catalog.Skills = append(catalog.Skills, skills.SkillMeta{Name: fmt.Sprintf("skill-%03d", i), Description: "Inspect a specific target and return concise verified evidence", AllowImplicit: true})
	}
	for _, mode := range []string{"legacy", "v3"} {
		config.GlobalConfig = config.Config{Provider: "custom", APIProtocol: config.ProtocolOpenAIChat, ModelName: "large", ContextWindowTokens: 1_048_576, UseNativeTools: true, AgentRuntime: mode}
		agent := &DeepAgent{UseNativeTools: true, State: NewAgentState(""), Middleware: defaultMiddlewareStack(catalog, nil)}
		prompt := agent.BuildSystemPrompt("")
		limit := config.GlobalConfig.EffectiveModelCapabilities().NativeToolLimit
		if limit == 0 {
			limit = 16
		}
		definitions := analyzer.AgentToolDefinitionsForContext(limit, "检查服务器监听端口与进程")
		encoded, err := json.Marshal(definitions)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("%s: prompt=%d estimated tokens, tools=%d / %d estimated tokens, total=%d", mode, analyzer.EstimateTextTokens(prompt), len(definitions), analyzer.EstimateTextTokens(string(encoded)), analyzer.EstimateTextTokens(prompt)+analyzer.EstimateTextTokens(string(encoded)))
	}
}

func TestBatchHistoryPreservesNativeArgumentTypes(t *testing.T) {
	response := analyzer.AgentResponse{Action: "tool_batch", NativeToolCalls: []analyzer.NativeToolCall{{ID: "c1", Name: "document_create", Arguments: `{"path":"/tmp/report.pptx","spec":{"title":"报告","slides":[]},"limit":12,"enabled":false}`}}}
	action := ParseAction(response)
	var history []analyzer.Message
	appendActionResultHistory(&history, action, &ActionResult{ToolResults: []ToolCallResult{{ID: "c1", Name: "document_create", Output: "ok"}}})
	args := history[0].ToolCalls[0].Function.Arguments
	var decoded map[string]any
	if err := json.Unmarshal([]byte(args), &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded["spec"].(map[string]any); !ok || decoded["limit"] != float64(12) || decoded["enabled"] != false {
		t.Fatalf("native argument types changed: %s", args)
	}
	redacted := RedactedAction(action)
	if redacted.ToolCalls[0].Arguments != "" || action.ToolCalls[0].Arguments == "" {
		t.Fatal("raw args leaked to UI or redaction mutated executable action")
	}
}
