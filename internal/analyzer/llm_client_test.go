package analyzer

import (
	"ai-edr/internal/collector"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai-edr/internal/config"
)

func TestOpenAICompatibleParsesUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"content":"{\"action\":\"finish\",\"final_report\":\"ok\"}"}}],
			"usage":{"prompt_tokens":123,"completion_tokens":45,"total_tokens":168}
		}`))
	}))
	defer server.Close()

	result, err := callOpenAICompatible(context.Background(), config.Config{
		ApiURL:    server.URL + "/v1/chat/completions",
		ModelName: "test-model",
		ApiKey:    "none",
	}, []Message{{Role: "user", Content: "hi"}}, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Usage.PromptTokens != 123 || result.Usage.CompletionTokens != 45 || result.Usage.TotalTokens != 168 {
		t.Fatalf("usage not parsed: %#v", result.Usage)
	}
}

func TestOpenAICompatibleSendsNativeBuiltinSchemasAndParsesName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.ToolChoice != "auto" {
			t.Fatalf("tool_choice=%#v, want auto", request.ToolChoice)
		}
		if request.MaxTokens != 4096 {
			t.Fatalf("max_tokens=%d want local adaptive 4096", request.MaxTokens)
		}
		if len(request.Tools) > 10 {
			t.Fatalf("local compact model received too many native schemas: %d", len(request.Tools))
		}
		found := false
		for _, def := range request.Tools {
			if def.Function.Name == "config_manage" {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("config_manage native definition missing from %d tools", len(request.Tools))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"config_manage","arguments":"{\"action\":\"status\"}"}}]}}]}`))
	}))
	defer server.Close()

	result, err := callOpenAICompatible(context.Background(), config.Config{
		ApiURL: server.URL, ModelName: "test-model", ApiKey: "none",
	}, []Message{{Role: "user", Content: "show config status"}}, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.ToolCallName != "config_manage" || result.ToolCallArgs != `{"action":"status"}` {
		t.Fatalf("unexpected tool result: %#v", result)
	}
}

func TestOpenAICompatibleRetriesWithoutUnsupportedMaxTokens(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		var request ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if requests == 1 {
			if request.MaxTokens == 0 {
				t.Fatal("first request should use adaptive max_tokens")
			}
			http.Error(w, `{"error":"unknown parameter max_tokens"}`, http.StatusBadRequest)
			return
		}
		if request.MaxTokens != 0 {
			t.Fatalf("compatibility retry still sent max_tokens=%d", request.MaxTokens)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"action\":\"finish\",\"final_report\":\"ok\"}"}}]}`))
	}))
	defer server.Close()

	_, err := callOpenAICompatible(context.Background(), config.Config{
		ApiURL: server.URL, ModelName: "local-model", ApiKey: "none",
	}, []Message{{Role: "user", Content: "hi"}}, false, nil)
	if err != nil || requests != 2 {
		t.Fatalf("max_tokens compatibility fallback failed: requests=%d err=%v", requests, err)
	}
}

func TestRunAgentStepRetriesWithSmallerHistoryAfterContextLimit(t *testing.T) {
	original := config.GlobalConfig
	t.Cleanup(func() { config.GlobalConfig = original })

	requestSizes := make([]int, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		requestSizes = append(requestSizes, len(request.Messages))
		if len(requestSizes) == 1 {
			http.Error(w, `{"error":{"message":"maximum context length exceeded"}}`, http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"{\"action\":\"finish\",\"final_report\":\"recovered\"}"}}]}`))
	}))
	defer server.Close()

	config.GlobalConfig = config.Config{
		Provider:            "custom",
		APIProtocol:         "openai_chat",
		ApiURL:              server.URL,
		ApiKey:              "none",
		ModelName:           "local-14b",
		ModelParameterB:     14,
		ContextWindowTokens: 32_768,
	}
	history := []Message{{Role: "user", Content: "排查异常登录"}}
	for i := 0; i < 12; i++ {
		history = append(history, Message{Role: "user", Content: fmt.Sprintf("步骤 %d: %s", i, strings.Repeat("evidence ", 30))})
	}
	response, err := RunAgentStepWithOptions(StepOptions{
		Context:        context.Background(),
		SysCtx:         collector.SystemContext{},
		History:        &history,
		UseNativeTools: false,
	})
	if err != nil || response.Action != "finish" || response.FinalReport != "recovered" {
		t.Fatalf("context-limit recovery failed: response=%#v err=%v", response, err)
	}
	if len(requestSizes) != 2 || requestSizes[1] >= requestSizes[0] {
		t.Fatalf("retry did not shrink history: %v", requestSizes)
	}
}

func TestOpenAICompatibleRequestCanBeCancelled(t *testing.T) {
	releaseHandler := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-releaseHandler:
		}
	}))
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	start := time.Now()
	_, err := callOpenAICompatible(ctx, config.Config{
		ApiURL:    server.URL + "/v1/chat/completions",
		ModelName: "test-model",
		ApiKey:    "none",
	}, []Message{{Role: "user", Content: "hi"}}, false, nil)
	close(releaseHandler)
	server.Close()
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("cancelled request err=%v elapsed=%s", err, time.Since(start))
	}
}

func TestReadLimitedResponseBodyRejectsOversize(t *testing.T) {
	body, err := readLimitedResponseBody(strings.NewReader("1234"), 4)
	if err != nil || string(body) != "1234" {
		t.Fatalf("exact limit rejected: body=%q err=%v", body, err)
	}
	if _, err := readLimitedResponseBody(strings.NewReader("12345"), 4); err == nil {
		t.Fatal("oversized LLM response should be rejected")
	}
}

func TestLLMRetryDelayUsesBoundedJitter(t *testing.T) {
	if got := llmRetryDelay(0); got != 0 {
		t.Fatalf("attempt 0 delay=%s, want 0", got)
	}
	for attempt := 1; attempt <= 4; attempt++ {
		base := time.Duration(attempt*attempt) * time.Second
		got := llmRetryDelay(attempt)
		if got < base || got > base+500*time.Millisecond {
			t.Fatalf("attempt %d delay=%s, want %s..%s", attempt, got, base, base+500*time.Millisecond)
		}
	}
}
