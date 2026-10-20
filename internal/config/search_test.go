package config

import (
	"testing"

	"github.com/spf13/viper"
)

func TestNormalizeSearchURL(t *testing.T) {
	cases := map[string]string{
		"https://api.deepseek.com":                             "https://api.deepseek.com/v1/tools/search",
		"https://api.deepseek.com/":                            "https://api.deepseek.com/v1/tools/search",
		"https://api.deepseek.com/v1":                          "https://api.deepseek.com/v1/tools/search",
		"https://api.deepseek.com/v1/chat/completions":         "https://api.deepseek.com/v1/tools/search",
		"https://api.deepseek.com/v1/tools/search":             "https://api.deepseek.com/v1/tools/search",
		"https://gateway.example/deepseek/v1/chat/completions": "https://gateway.example/deepseek/v1/tools/search",
		"": "",
	}
	for input, want := range cases {
		if got := NormalizeSearchURL(input); got != want {
			t.Errorf("NormalizeSearchURL(%q)=%q, want %q", input, got, want)
		}
	}
}

func TestSearchEffectiveEnabledOnlyReusesDeepSeekCredentials(t *testing.T) {
	deepseek := Config{Provider: "deepseek", ApiURL: "https://api.deepseek.com", ApiKey: "sk-ds", ModelName: "deepseek-flash"}
	search := SearchConfig{Enabled: true}
	if !search.EffectiveEnabled(deepseek) {
		t.Fatal("DeepSeek primary model should enable search by default")
	}
	if got := search.EffectiveBaseURL(deepseek); got != "https://api.deepseek.com/v1/tools/search" {
		t.Fatalf("unexpected endpoint: %s", got)
	}
	if got := search.EffectiveAPIKey(deepseek); got != "sk-ds" {
		t.Fatalf("DeepSeek key was not reused: %q", got)
	}

	qwen := Config{Provider: "qwen", ApiURL: "https://dashscope.aliyuncs.com/compatible-mode/v1", ApiKey: "sk-qwen", ModelName: "qwen3.8-max"}
	if search.EffectiveEnabled(qwen) {
		t.Fatal("an unrelated provider key must not enable DeepSeek Search")
	}
	if got := search.EffectiveAPIKey(qwen); got != "" {
		t.Fatalf("Qwen key leaked to Search: %q", got)
	}

	explicit := SearchConfig{Enabled: true, BaseURL: "https://api.deepseek.com/v1", APIKey: "sk-search"}
	if !explicit.EffectiveEnabled(qwen) {
		t.Fatal("explicit search credentials should work with any primary model")
	}
	explicitKeyOnly := SearchConfig{Enabled: true, APIKey: "sk-search"}
	if !explicitKeyOnly.EffectiveEnabled(qwen) {
		t.Fatal("an explicit search.api_key should default the endpoint to DeepSeek")
	}

	models := Config{
		Provider:  "qwen",
		ApiKey:    "sk-qwen",
		ModelName: "qwen3.8-max",
		Models: []ModelConfig{
			{ID: "primary", Role: "primary", Provider: "deepseek", APIURL: "https://api.deepseek.com", APIKey: "sk-ds", ModelName: "deepseek-flash"},
		},
	}
	if !search.EffectiveEnabled(models) {
		t.Fatal("DeepSeek primary model in models chain should enable search")
	}

	inherited := Config{
		Provider:  "deepseek",
		ApiURL:    "https://api.deepseek.com",
		ApiKey:    "sk-ds",
		ModelName: "deepseek-flash",
		Models: []ModelConfig{
			{ID: "primary", Role: "primary", Provider: "deepseek", ModelName: "deepseek-flash"},
		},
	}
	if got := search.EffectiveAPIKey(inherited); got != "sk-ds" {
		t.Fatalf("same-provider inherited key should be reused: %q", got)
	}

	leak := Config{
		Provider:  "qwen",
		ApiURL:    "https://dashscope.aliyuncs.com/compatible-mode/v1",
		ApiKey:    "sk-qwen",
		ModelName: "qwen3.8-max",
		Models: []ModelConfig{
			{ID: "primary", Role: "primary", Provider: "deepseek", APIURL: "https://api.deepseek.com", ModelName: "deepseek-flash"},
		},
	}
	if got := search.EffectiveAPIKey(leak); got != "" {
		t.Fatalf("cross-provider inherited key must not be sent to DeepSeek: %q", got)
	}
}

func TestSearchDefaultsAreEnabledForDeepSeek(t *testing.T) {
	v := viper.New()
	setReaderDefaults(v)
	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		t.Fatal(err)
	}
	if !cfg.Search.Enabled {
		t.Fatal("search.enabled should default to true")
	}
	if cfg.Search.Count != DefaultSearchCount || cfg.Search.TimeoutSec != DefaultSearchTimeoutSec {
		t.Fatalf("unexpected search defaults: %#v", cfg.Search)
	}
}
