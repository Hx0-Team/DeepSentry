package analyzer

import (
	"ai-edr/internal/config"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestProviderCacheUsageNormalization(t *testing.T) {
	for _, raw := range []string{
		`{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120,"prompt_cache_hit_tokens":80,"prompt_cache_miss_tokens":20}`,
		`{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120,"prompt_tokens_details":{"cached_tokens":80},"completion_tokens_details":{"reasoning_tokens":10}}`,
	} {
		var u TokenUsage
		if err := json.Unmarshal([]byte(raw), &u); err != nil {
			t.Fatal(err)
		}
		if u.PromptTokens != 100 || u.CacheReadTokens != 80 || u.TotalTokens != 120 || !u.CacheUsageReported {
			t.Fatalf("usage=%#v", u)
		}
	}
}

func TestSummaryProjectionPreservesCallsUserAndEvidenceWithoutReplay(t *testing.T) {
	call := ToolCall{ID: "c1"}
	call.Function.Name = "execute"
	call.Function.Arguments = `{"command":"check /var/log/auth.log"}`
	user := strings.Repeat("用户要求保留的原文", 600)
	history := []Message{{Role: "user", Content: user}, {Role: "assistant", ReasoningContent: strings.Repeat("secret-thinking", 1000), ToolCalls: []ToolCall{call}},
		{Role: "tool", Name: "execute", ToolCallID: "c1", Content: "HEAD-EVIDENCE" + strings.Repeat("日志", 30000) + "TAIL-ERROR /tmp/output_step1.txt"}}
	copy := append([]Message(nil), history...)
	projected := summaryHistoryProjection(history)
	joined := joinHistoryContent(projected)
	for _, want := range []string{user, "check /var/log/auth.log", "HEAD-EVIDENCE", "TAIL-ERROR", "output_step1.txt"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("lost %q", want[:min(30, len(want))])
		}
	}
	if strings.Contains(joined, "secret-thinking") || len([]rune(projected[2].Content)) > 2200 {
		t.Fatal("summary replayed oversized evidence/reasoning")
	}
	if !reflect.DeepEqual(history, copy) {
		t.Fatal("projection mutated history")
	}
}

func TestNativeFinishKeepsVisibleReport(t *testing.T) {
	old := config.GlobalConfig
	t.Cleanup(func() { config.GlobalConfig = old })
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"已核验 /debug 返回200，公开调试信息；建议限制访问。","tool_calls":[{"id":"c","type":"function","function":{"name":"agent_action","arguments":"{\"action\":\"finish\",\"final_report\":\"见上\"}"}}]}}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`))
	}))
	defer s.Close()
	config.GlobalConfig = config.Config{Provider: "custom", ApiURL: s.URL, ModelName: "test", UseNativeTools: true}
	history := []Message{{Role: "user", Content: "核验调试页"}}
	r, err := RunAgentStepWithOptions(StepOptions{Context: context.Background(), History: &history, UseNativeTools: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.FinalReport, "返回200") || r.ToolCallArguments == "" {
		t.Fatalf("report/arguments lost: %#v", r)
	}
}

func TestUsageIncludesBilledEmptyResponseAndSuccess(t *testing.T) {
	old := config.GlobalConfig
	t.Cleanup(func() { config.GlobalConfig = old })
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		var request map[string]any
		_ = json.NewDecoder(r.Body).Decode(&request)
		if request["model"] == "empty" {
			_, _ = w.Write([]byte(`{"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":5,"total_tokens":105}}`))
		} else {
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":120,"completion_tokens":10,"total_tokens":130}}`))
		}
	}))
	defer s.Close()
	config.GlobalConfig = config.Config{Provider: "custom", ApiURL: s.URL, ModelName: "empty", LLMRetries: 0}
	// A billed empty response must be reported even when the call fails.
	var usages []TokenUsage
	_, err := callLLMWithUsageContext(context.Background(), []Message{{Role: "user", Content: "test"}}, false, nil, func(u TokenUsage) { usages = append(usages, u) })
	if err == nil || len(usages) != 1 || usages[0].TotalTokens != 105 {
		t.Fatalf("lost billed failed response: %#v, %v", usages, err)
	}
	config.GlobalConfig.ModelName = "working"
	_, err = callLLMWithUsageContext(context.Background(), []Message{{Role: "user", Content: "test"}}, false, nil, func(u TokenUsage) { usages = append(usages, u) })
	if err != nil || len(usages) != 2 || usages[1].TotalTokens != 130 {
		t.Fatalf("lost successful response: %#v, %v", usages, err)
	}
}

func TestResponsesCacheAndBilledEmptyOutput(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"output":[],"usage":{"input_tokens":100,"output_tokens":20,"total_tokens":120,"input_tokens_details":{"cached_tokens":80},"output_tokens_details":{"reasoning_tokens":15}}}`))
	}))
	defer s.Close()
	r, err := callOpenAIResponses(context.Background(), config.Config{Provider: "openai", ModelName: "test", ApiURL: s.URL}, []Message{{Role: "user", Content: "test"}}, false)
	if err == nil || r.Usage.TotalTokens != 120 || r.Usage.CacheReadTokens != 80 || r.Usage.ReasoningTokens != 15 || !r.Usage.CacheUsageReported {
		t.Fatalf("missing Responses usage: %#v (%v)", r, err)
	}
}

func TestAnthropicRefusalUsageAndZeroCacheDetails(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"refused"}],"stop_reason":"refusal","usage":{"input_tokens":100,"output_tokens":5,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}`))
	}))
	defer s.Close()
	r, err := callAnthropic(context.Background(), config.Config{Provider: "anthropic", ModelName: "test", ApiURL: s.URL}, []Message{{Role: "user", Content: "test"}}, false)
	if err == nil || r.Usage.TotalTokens != 105 || !r.Usage.CacheUsageReported {
		t.Fatalf("missing billed refusal/zero cache details: %#v (%v)", r, err)
	}
}

func TestUnsupportedNativeToolsUsesExplicitJSONActionProtocol(t *testing.T) {
	var native, fallback bool
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if _, present := body["tools"]; present {
			native = true
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":"tools unsupported parameter"}`))
			return
		}
		encoded, _ := json.Marshal(body["messages"])
		fallback = strings.Contains(string(encoded), "JSON动作兼容")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"action\":\"finish\",\"final_report\":\"已核验\"}"}}]}`))
	}))
	defer s.Close()
	cfg := config.Config{Provider: "custom", ModelName: "test-fallback", ApiURL: s.URL}
	messages := []Message{{Role: "system", Content: "原生函数直接执行"}, {Role: "user", Content: "test"}}
	result, err := callLLMOnce(context.Background(), cfg, messages, true, nil)
	if err != nil || !native || !fallback || !strings.Contains(result.Content, "finish") || len(messages) != 2 {
		t.Fatalf("fallback missing or mutated history: native=%t fallback=%t result=%#v err=%v", native, fallback, result, err)
	}
}
