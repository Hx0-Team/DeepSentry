package analyzer

import (
	"strings"
	"testing"
)

func TestExtractJSONPayload_MarkdownBlock(t *testing.T) {
	raw := "The lastb output is too massive for SSH pipe processing.\n\n```json\n" +
		`{"action":"tool","tool_name":"read_log","tool_args":{"path":"/var/log/auth.log","lines":500,"pattern":"Failed"}}` +
		"\n```"

	jsonPart, prose := extractJSONPayload(raw)
	if jsonPart == "" {
		t.Fatal("expected JSON extracted")
	}
	if !strings.Contains(jsonPart, `"action":"tool"`) {
		t.Fatalf("unexpected json: %s", jsonPart)
	}
	if prose == "" {
		t.Fatal("expected prose before json block")
	}
}

func TestExtractBalancedJSONObject(t *testing.T) {
	s := `prefix {"action":"execute","command":"ls"} suffix`
	obj := extractBalancedJSONObject(s)
	if obj != `{"action":"execute","command":"ls"}` {
		t.Fatalf("got %q", obj)
	}
}

func TestCleanJSON_MixedResponse(t *testing.T) {
	raw := "Let me read auth.log\n\n```json\n{\"action\":\"tool\",\"tool_name\":\"grep\"}\n```"
	out, prose := cleanJSON(raw)
	if !strings.Contains(out, `"tool_name"`) {
		t.Fatalf("cleanJSON failed: %q prose=%q", out, prose)
	}
	if prose == "" {
		t.Fatal("expected prose")
	}
}

func TestExtractClarificationQuestion(t *testing.T) {
	raw := "您好！为了设置每10分钟监控CPU使用率并发送到钉钉机器人，请提供 Webhook URL。"
	q := extractClarificationQuestion(raw)
	if q == "" {
		t.Fatal("expected clarification question to be detected")
	}
}

func TestRecoverPlainTextResponseExtractsPendingShellBlock(t *testing.T) {
	raw := "让我重新来，先从硬件和存储开始。\n\n```bash\n$ system_profiler SPHardwareDataType | head -80\n```\n"
	resp, ok := recoverPlainTextResponse(raw)
	if !ok {
		t.Fatal("expected plain text response to be recovered")
	}
	if resp.Action != "execute" || resp.Command != "system_profiler SPHardwareDataType | head -80" {
		t.Fatalf("unexpected recovered response: %#v", resp)
	}
	if resp.IsFinished {
		t.Fatal("pending shell step must not be treated as a final report")
	}
}

func TestRecoverPlainTextResponsePresentsNaturalLanguageConclusion(t *testing.T) {
	raw := "检查完成：系统运行正常。\n\n- CPU 正常\n- 磁盘正常"
	resp, ok := recoverPlainTextResponse(raw)
	if !ok || !resp.IsFinished || resp.Action != "finish" {
		t.Fatalf("plain conclusion should finish cleanly: %#v", resp)
	}
	if resp.FinalReport != raw || strings.Contains(resp.FinalReport, "解析失败") {
		t.Fatalf("plain conclusion was not preserved: %q", resp.FinalReport)
	}
}

func TestRecoverPlainTextResponseRetriesTruncatedJSON(t *testing.T) {
	raw := `{"action":"execute","command":"uname -a"`
	resp, ok := recoverPlainTextResponse(raw)
	if !ok || resp.Action != "" || resp.IsFinished {
		t.Fatalf("truncated JSON should request a retry: %#v", resp)
	}
	if !strings.Contains(resp.Thought, "自动") {
		t.Fatalf("retry thought should explain recovery: %q", resp.Thought)
	}
}

func TestRecoverPlainTextResponseKeepsClarificationAsQuestion(t *testing.T) {
	raw := "继续连接前，请提供服务器地址和用户名。"
	resp, ok := recoverPlainTextResponse(raw)
	if !ok || resp.Action != "ask_user" || resp.Question != raw {
		t.Fatalf("clarification should remain interactive: %#v", resp)
	}
}

func TestDecodeJSONUnicodeEscapesInCommand(t *testing.T) {
	got := decodeJSONUnicodeEscapes(`chmod +x /opt/scripts/cpu_monitor.sh \u0026\u0026 ls -la /opt/scripts/cpu_monitor.sh`)
	if got != "chmod +x /opt/scripts/cpu_monitor.sh && ls -la /opt/scripts/cpu_monitor.sh" {
		t.Fatalf("unexpected command: %q", got)
	}
}

func TestTodoItemAcceptsNumericIDAndTitleDetail(t *testing.T) {
	raw := `{"id":1,"title":"修复签名逻辑","status":"in_progress","detail":"将 printf '%s' 改为 printf '%s\n'"}`
	var item TodoItem
	if err := item.UnmarshalJSON([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	if item.ID != "1" {
		t.Fatalf("id=%q", item.ID)
	}
	if !strings.Contains(item.Content, "修复签名逻辑") || !strings.Contains(item.Content, "printf") {
		t.Fatalf("content not normalized: %q", item.Content)
	}
	if item.Status != "in_progress" {
		t.Fatalf("status=%q", item.Status)
	}
}

func TestAgentToolSchemaConstrainsSubAgentTasks(t *testing.T) {
	defs := AgentToolDefinitions()
	if len(defs) == 0 {
		t.Fatal("expected agent tool definitions")
	}
	props, ok := defs[0].Function.Parameters["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("properties missing or wrong type: %#v", defs[0].Function.Parameters["properties"])
	}
	action, ok := props["action"].(map[string]interface{})
	if !ok {
		t.Fatalf("action schema missing enum-friendly shape: %#v", props["action"])
	}
	if _, ok := action["enum"].([]string); !ok {
		t.Fatalf("action enum missing: %#v", action)
	}
	parallel, ok := props["parallel_tasks"].(map[string]interface{})
	if !ok {
		t.Fatalf("parallel_tasks schema missing: %#v", props["parallel_tasks"])
	}
	items, ok := parallel["items"].(map[string]interface{})
	if !ok {
		t.Fatalf("parallel_tasks items missing: %#v", parallel["items"])
	}
	required, ok := items["required"].([]string)
	if !ok {
		t.Fatalf("parallel_tasks items required missing: %#v", items["required"])
	}
	if len(required) != 2 || required[0] != "task_name" || required[1] != "task_prompt" {
		t.Fatalf("unexpected parallel task required fields: %#v", required)
	}
}

func TestAgentToolDefinitionsExposeBuiltinsDirectly(t *testing.T) {
	defs := AgentToolDefinitions()
	byName := map[string]FunctionDef{}
	for _, def := range defs {
		byName[def.Function.Name] = def.Function
	}
	for _, name := range []string{"agent_action", "tool_catalog", "config_manage", "fleet_inventory", "fleet_exec", "fleet_file"} {
		if _, ok := byName[name]; !ok {
			t.Fatalf("native tool %s missing; got %d definitions", name, len(defs))
		}
	}
	configProps, ok := byName["config_manage"].Parameters["properties"].(map[string]interface{})
	if !ok {
		t.Fatalf("config_manage properties missing: %#v", byName["config_manage"].Parameters)
	}
	if _, ok := configProps["port"]; !ok {
		t.Fatalf("config_manage native schema must teach host+port: %#v", configProps)
	}
	for _, def := range defs {
		if got := len([]rune(def.Function.Description)); got > 1024 {
			t.Fatalf("native description for %s too large for compatible providers: %d runes", def.Function.Name, got)
		}
	}
}

func TestParseNamedToolCallMapsToHarnessAction(t *testing.T) {
	resp, err := ParseNamedToolCall("fleet_exec", `{"selector":"prod","command":"uptime","concurrency":3}`)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Action != "tool" || resp.ToolName != "fleet_exec" || resp.ToolArgs["concurrency"] != "3" {
		t.Fatalf("unexpected mapped response: %#v", resp)
	}
}
