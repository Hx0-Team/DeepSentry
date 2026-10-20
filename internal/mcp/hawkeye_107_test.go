package mcp

import (
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"strings"
	"testing"
)

func TestHawkEye107DefaultsFollowSchemaAndPreserveExplicitChoices(t *testing.T) {
	schema := map[string]interface{}{"type": "object", "properties": map[string]interface{}{"text": map[string]interface{}{"type": "string"}, "limit": map[string]interface{}{"type": "integer"}}, "additionalProperties": false}
	args := applyHawkEyeCallDefaultsForSchema("browser_find", schema, map[string]string{"text": "test"})
	if _, bad := args["context_budget_chars"]; bad {
		t.Fatal("unsupported default injected")
	}
	if args["limit"] != "12" {
		t.Fatal(args)
	}
	if _, err := validateAndCoerceMCPArgs(schema, args); err != nil {
		t.Fatal(err)
	}
	catalog := applyHawkEyeCallDefaults("hawkeye_codec", map[string]string{"action": "catalog"})
	if catalog["summary"] != "true" || catalog["includeMeta"] != "false" {
		t.Fatal(catalog)
	}
	catalog = applyHawkEyeCallDefaults("hawkeye_codec", map[string]string{"action": "catalog", "summary": "false", "includeMeta": "true"})
	if catalog["summary"] != "false" || catalog["includeMeta"] != "true" {
		t.Fatal("explicit catalog choices changed")
	}
}

func TestHawkEye107RiskDistinguishesReadingMutationAndAI(t *testing.T) {
	for _, test := range []struct {
		name string
		args map[string]string
		want string
	}{
		{"browser_bookmarks", map[string]string{"action": "list"}, MCPRiskLow},
		{"browser_bookmarks", map[string]string{"action": "remove"}, MCPRiskHigh},
		{"hawkeye_script_manage", map[string]string{"action": "get"}, MCPRiskLow},
		{"hawkeye_script_manage", map[string]string{"action": "global_enable"}, MCPRiskHigh},
		{"hawkeye_codec", map[string]string{"action": "catalog"}, MCPRiskLow},
		{"hawkeye_codec", map[string]string{"action": "workbench", "command": "save_file"}, MCPRiskMedium},
		{"hawkeye_codec", map[string]string{"action": "smartdecode", "options": `{"ai":true}`}, MCPRiskHigh},
		{"hawkeye_script_manage", map[string]string{"page_token": "opaque_continuation_token_for_cached_result"}, MCPRiskLow},
	} {
		tool := ExternalTool{OriginalName: test.name, Server: "hx0-hawkeye"}
		got, _, ok := HawkEyeToolRisk(&tool, test.args)
		if !ok || got != test.want {
			t.Fatalf("%s %#v: %s", test.name, test.args, got)
		}
	}
}

func TestHawkEye107PagedUTF8IsNotTruncatedAgain(t *testing.T) {
	text := strings.Repeat("中文证据", 5000) + "END-OF-THIS-PAGE"
	structured := map[string]any{"context": map[string]any{"budget_chars": 24000, "immutable": true, "complete": false, "next_page_token": "next-page"}}
	out := formatHawkEyeMCPContent("browser_snapshot", []sdkmcp.Content{&sdkmcp.TextContent{Text: text}}, structured, nil)
	if !strings.Contains(out, text) || !strings.Contains(out, "next_page_token=next-page") {
		t.Fatal("lost server page middle/tail or continuation")
	}
	payload := map[string]any{"value": "codec result"}
	duplicated := formatHawkEyeMCPContent("hawkeye_codec", []sdkmcp.Content{&sdkmcp.TextContent{Text: `{"value":"codec result"}`}}, payload, nil)
	if strings.Count(duplicated, "codec result") != 1 {
		t.Fatal("duplicated structured JSON")
	}
}
