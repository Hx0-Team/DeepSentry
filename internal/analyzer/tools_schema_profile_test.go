package analyzer

import "testing"

func TestAgentToolDefinitionsForCompactContextLimitsAndRanks(t *testing.T) {
	definitions := AgentToolDefinitionsForContext(8, "请离线 analyze pcap 并检查 DNS 会话")
	if len(definitions) != 10 { // agent_action + tool_catalog + 8 built-ins
		t.Fatalf("compact definitions=%d want 10", len(definitions))
	}
	want := map[string]bool{"agent_action": false, "tool_catalog": false, "config_manage": false, "pcap_analyze": false}
	for _, definition := range definitions {
		if _, ok := want[definition.Function.Name]; ok {
			want[definition.Function.Name] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Fatalf("compact native schema lost %s", name)
		}
	}
}

func TestAgentToolDefinitionsFullContextKeepsEveryTool(t *testing.T) {
	if got, want := len(AgentToolDefinitionsForContext(0, "")), len(AgentToolDefinitions()); got != want {
		t.Fatalf("full profile definitions=%d want %d", got, want)
	}
}
