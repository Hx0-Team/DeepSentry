package analyzer

import (
	"testing"

	deepsentrytools "ai-edr/internal/tools"
)

func TestWebSearchSchemaIsDeferredAndSelectedForLiveWebIntent(t *testing.T) {
	deepsentrytools.ConfigureEnabled(nil, nil)
	deepsentrytools.SetWebSearchEnabled(true)
	t.Cleanup(func() {
		deepsentrytools.SetWebSearchEnabled(true)
		deepsentrytools.ConfigureEnabled(nil, nil)
	})

	if alwaysVisibleNativeTool("web_search") {
		t.Fatal("web_search must not be a fixed control tool")
	}
	base := AgentToolDefinitionsForContextWithPinned(1, "查看本机内存", nil)
	for _, def := range base {
		if def.Function.Name == "web_search" {
			t.Fatal("web_search must not appear for an unrelated memory task")
		}
	}

	found := false
	for _, def := range AgentToolDefinitionsForContextWithPinned(runtimeV3DeferredToolLimit, "联网搜索最新的 CVE 漏洞公告和新闻", nil) {
		if def.Function.Name == "web_search" {
			found = true
			params := def.Function.Parameters
			required, _ := params["required"].([]string)
			if len(required) != 1 || required[0] != "query" {
				t.Fatalf("unexpected web_search schema: %#v", params)
			}
		}
	}
	if !found {
		t.Fatal("web_search was not selected for a live-web intent")
	}
}
