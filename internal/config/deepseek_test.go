package config

import "testing"

func TestDeepSeekThinkingEffortAndOutputDefaults(t *testing.T) {
	base := Config{Provider: "deepseek", ModelName: "deepseek-flash"}
	if got := base.EffectiveThinkingMode(); got != "enabled" {
		t.Fatalf("DeepSeek default thinking=%q", got)
	}
	if got := base.EffectiveReasoningEffort(); got != "" {
		t.Fatalf("auto effort should defer to server default, got %q", got)
	}
	if got := base.EffectiveModelCapabilities().ReservedOutputTokens; got != 65_536 {
		t.Fatalf("thinking output budget=%d, want 65536", got)
	}

	maxEffort := base
	maxEffort.ReasoningEffort = "max"
	if got := maxEffort.EffectiveReasoningEffort(); got != "max" {
		t.Fatalf("max effort mapped to %q", got)
	}
	if got := maxEffort.EffectiveModelCapabilities().ReservedOutputTokens; got != 131_072 {
		t.Fatalf("max-effort output budget=%d, want 131072", got)
	}

	disabled := base
	disabled.ThinkingMode = "disabled"
	if got := disabled.EffectiveModelCapabilities().ReservedOutputTokens; got != 16_384 {
		t.Fatalf("non-thinking output budget=%d, want 16384", got)
	}

	none := base
	none.ReasoningEffort = "none"
	if got := none.EffectiveThinkingMode(); got != "disabled" {
		t.Fatalf("reasoning_effort=none should disable thinking, got %q", got)
	}

	mapped := base
	mapped.ReasoningEffort = "medium"
	if got := mapped.EffectiveReasoningEffort(); got != "high" {
		t.Fatalf("medium should map to high, got %q", got)
	}
}

func TestDeepSeekAnthropicModelAliasesUseDocumentedCapabilities(t *testing.T) {
	for _, test := range []struct {
		model  string
		vision bool
		id     string
	}{
		{"claude-opus-5-5", false, "deepseek-v4-pro"},
		{"claude-sonnet-5", true, "deepseek-flash"},
		{"claude-haiku-4-5", true, "deepseek-flash"},
		{"deepseek-v4-pro", false, "deepseek-v4-pro"},
	} {
		info, ok := LookupDeepSeekModel("deepseek", test.model)
		if !ok || info.ID != test.id || info.SupportsVision != test.vision || info.ContextWindowTokens != 1_048_576 {
			t.Fatalf("%s -> %#v, ok=%v", test.model, info, ok)
		}
	}
}

func TestDeepSeekUserIDValidation(t *testing.T) {
	for _, value := range []string{"", "user_01", "team-a"} {
		if err := ValidateRuntimeConfig(Config{Provider: "deepseek", ApiURL: "https://api.deepseek.com", ModelName: "deepseek-flash", UserID: value, SSHHostKeyPolicy: "accept-new", SSHKnownHostsPath: "~/.deepsentry/known_hosts"}); err != nil {
			t.Fatalf("valid user_id %q rejected: %v", value, err)
		}
	}
	for _, value := range []string{"含中文", "has space", "x/y"} {
		if err := ValidateRuntimeConfig(Config{Provider: "deepseek", ApiURL: "https://api.deepseek.com", ModelName: "deepseek-flash", UserID: value, SSHHostKeyPolicy: "accept-new", SSHKnownHostsPath: "~/.deepsentry/known_hosts"}); err == nil {
			t.Fatalf("invalid user_id %q accepted", value)
		}
	}
}
