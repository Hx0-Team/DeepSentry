package analyzer

import (
	"ai-edr/internal/config"
	"ai-edr/internal/mcp"
	"ai-edr/internal/tools"
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestRequestToolsReuseAndCanonicalWireOrder(t *testing.T) {
	ctx := withRequestToolsCache(context.Background())
	cfg := config.Config{Provider: "custom", ModelName: "large", NativeToolLimit: 4}
	messages := []Message{{Role: "user", Content: "http_fetch http_probe"}}
	a, err := prepareNativeTools(ctx, cfg, messages)
	if err != nil {
		t.Fatal(err)
	}
	b, err := prepareNativeTools(ctx, cfg, messages)
	if err != nil {
		t.Fatal(err)
	}
	if &a.encoded[0] != &b.encoded[0] {
		t.Fatal("same step constructed schemas again")
	}
	messages = append(messages, Message{Role: "assistant", Content: "http_probe"})
	c, err := prepareNativeTools(ctx, cfg, messages)
	if err != nil {
		t.Fatal(err)
	}
	if string(a.encoded) != string(c.encoded) {
		t.Fatal("score-only change reordered the same selected schemas")
	}
	encoded, err := json.Marshal(ChatRequest{Model: "test", preparedTools: a.encoded})
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(encoded, &raw)
	if !reflect.DeepEqual([]byte(raw["tools"]), []byte(a.encoded)) {
		t.Fatal("prepared tools were dropped or double-encoded")
	}
}

func TestRequestToolsInvalidatesDisabledToolsAndChangedCatalog(t *testing.T) {
	tool, _ := tools.Get("http_fetch")
	enabled := tool.Enabled
	t.Cleanup(func() { tools.SetEnabled("http_fetch", enabled) })
	ctx := withRequestToolsCache(context.Background())
	cfg := config.Config{Provider: "custom", ModelName: "large", NativeToolLimit: 8}
	messages := []Message{{Role: "user", Content: "http_fetch"}}
	a, _ := prepareNativeTools(ctx, cfg, messages)
	if !strings.Contains(string(a.encoded), `"name":"http_fetch"`) {
		t.Fatal("initial schema absent")
	}
	tools.SetEnabled("http_fetch", false)
	b, _ := prepareNativeTools(ctx, cfg, messages)
	if strings.Contains(string(b.encoded), `"name":"http_fetch"`) {
		t.Fatal("disabled capability remained cached")
	}

	const server = "request-cache-test"
	const name = "request-cache-test__probe"
	t.Cleanup(func() { mcp.Global().UnregisterServer(server) })
	register := func(valueType string) {
		mcp.Global().RegisterHandler(name, mcp.ExternalTool{Name: name, Server: server, OriginalName: "probe", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"value": map[string]any{"type": valueType}}, "required": []string{"value"}}}, nil)
	}
	messages = []Message{{Role: "user", Content: name}}
	register("string")
	first, _ := prepareNativeTools(ctx, cfg, messages)
	register("boolean")
	second, _ := prepareNativeTools(ctx, cfg, messages)
	if string(first.encoded) == string(second.encoded) || !strings.Contains(string(second.encoded), `"type":"boolean"`) {
		t.Fatal("catalog update reused stale schema")
	}
	mcp.Global().UnregisterServer(server)
	third, _ := prepareNativeTools(ctx, cfg, messages)
	if strings.Contains(string(third.encoded), `"name":"`+name+`"`) {
		t.Fatal("removed provider remained cached")
	}
}

func TestToolRetrievalKeepsUserGoalAcrossLargeUnrelatedResults(t *testing.T) {
	ctx := withRequestToolsCache(context.Background())
	cfg := config.Config{Provider: "custom", ModelName: "large", NativeToolLimit: 16}
	history := []Message{{Role: "user", Content: "核验 http_fetch 的 JSON 响应正文"}}
	before, err := prepareNativeTools(ctx, cfg, history)
	if err != nil {
		t.Fatal(err)
	}
	history = append(history, Message{Role: "tool", Name: "agent_action", Content: strings.Repeat("word报告 生成excel 创建工作表 computer_use 桌面操作 每分钟 定时任务\n", 1000)})
	after, err := prepareNativeTools(ctx, cfg, history)
	if err != nil {
		t.Fatal(err)
	}
	if string(before.encoded) != string(after.encoded) {
		t.Fatal("unrelated evidence displaced the user task's schemas")
	}
	history = append(history, Message{Role: "user", Content: "现在读取附件 input.docx 并生成 Word 报告，用 document_create"})
	changed, err := prepareNativeTools(ctx, cfg, history)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"document_parse", "document_create"} {
		if !strings.Contains(string(changed.encoded), `"name":"`+name+`"`) {
			t.Fatalf("new user requirement did not retrieve %s", name)
		}
	}
}
