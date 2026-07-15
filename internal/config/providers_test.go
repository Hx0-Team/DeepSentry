package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
)

func TestNormalizeChatURL(t *testing.T) {
	cases := map[string]string{
		"https://token-plan-cn.xiaomimimo.com/v1":           "https://token-plan-cn.xiaomimimo.com/v1/chat/completions",
		"https://qianfan.baidubce.com/v2/coding":            "https://qianfan.baidubce.com/v2/coding/chat/completions",
		"https://ark.cn-beijing.volces.com/api/coding/v3":   "https://ark.cn-beijing.volces.com/api/coding/v3/chat/completions",
		"https://api.deepseek.com/v1/chat/completions":      "https://api.deepseek.com/v1/chat/completions",
		"https://api.anthropic.com/v1":                      "https://api.anthropic.com/v1/messages",
		"https://dashscope.aliyuncs.com/compatible-mode/v1": "https://dashscope.aliyuncs.com/compatible-mode/v1/chat/completions",
		"https://api.hunyuan.cloud.tencent.com/v1":          "https://api.hunyuan.cloud.tencent.com/v1/chat/completions",
	}
	for in, want := range cases {
		got := NormalizeChatURL(in)
		if got != want {
			t.Fatalf("%s => %s, want %s", in, got, want)
		}
	}
}

func TestApplyProviderDefaultsMimo(t *testing.T) {
	cfg := &Config{Provider: "mimo", ApiURL: "", ModelName: ""}
	ApplyProviderDefaults(cfg)
	if cfg.ModelName != "mimo-v2.5-pro" {
		t.Fatalf("unexpected default model: %s", cfg.ModelName)
	}
	if cfg.ApiURL != "https://token-plan-cn.xiaomimimo.com/v1/chat/completions" {
		t.Fatalf("unexpected url: %s", cfg.ApiURL)
	}
}

func TestProviderDefaultsChineseOpenAICompatible(t *testing.T) {
	cases := []struct {
		provider string
		urlPart  string
		model    string
	}{
		{"qwen", "dashscope.aliyuncs.com", "qwen-plus"},
		{"qianfan", "qianfan.baidubce.com", "qianfan-code-latest"},
		{"volcengine", "ark.cn-beijing.volces.com", "ark-code-latest"},
		{"hunyuan", "hunyuan.cloud.tencent.com", "hunyuan-turbos-latest"},
		{"tencent_hy", "hunyuan.cloud.tencent.com", "hunyuan-turbos-latest"},
		{"teleai", "ctyun.cn", "GLM-5-Pro"},
		{"ctyun", "ctyun.cn", "GLM-5-Pro"},
	}
	for _, tc := range cases {
		cfg := &Config{Provider: tc.provider}
		ApplyProviderDefaults(cfg)
		if !contains(cfg.ApiURL, tc.urlPart) || !contains(cfg.ApiURL, "chat/completions") {
			t.Fatalf("%s unexpected url: %s", tc.provider, cfg.ApiURL)
		}
		if cfg.ModelName != tc.model || cfg.APIProtocol != ProtocolOpenAIChat {
			t.Fatalf("%s unexpected defaults: %+v", tc.provider, cfg)
		}
	}
}

func TestCodingPlanProviderPresets(t *testing.T) {
	tests := []struct {
		id, displayName string
	}{
		{"qianfan", "百度千帆 Coding Plan"},
		{"volcengine", "火山方舟 Coding Plan"},
		{"mimo", "Xiaomi MiMo Token Plan / MiMo Claw"},
	}
	for _, test := range tests {
		preset, ok := FindProvider(test.id)
		if !ok {
			t.Fatalf("provider %s not found", test.id)
		}
		if preset.DisplayName != test.displayName || preset.Protocol != ProtocolOpenAIChat || !preset.NativeTools {
			t.Fatalf("unexpected %s preset: %+v", test.id, preset)
		}
	}
}

func TestProviderDefaultsXAIAndLMStudio(t *testing.T) {
	xai := &Config{Provider: "xai"}
	ApplyProviderDefaults(xai)
	if xai.ModelName == "" || !contains(xai.ApiURL, "api.x.ai") || xai.APIProtocol != ProtocolOpenAIChat {
		t.Fatalf("unexpected xai defaults: %+v", xai)
	}

	lm := &Config{Provider: "lmstudio"}
	ApplyProviderDefaults(lm)
	if !contains(lm.ApiURL, "localhost:1234") || lm.APIProtocol != ProtocolOpenAIChat {
		t.Fatalf("unexpected lmstudio defaults: %+v", lm)
	}
}

func TestResponsesURLPreserved(t *testing.T) {
	in := "https://api.openai.com/v1/responses"
	if got := NormalizeChatURL(in); got != in {
		t.Fatalf("responses url changed: %s", got)
	}
}

func TestInitConfigAcceptsUppercaseBenchmarkKeys(t *testing.T) {
	old := GlobalConfig
	defer func() {
		GlobalConfig = old
		viper.Reset()
	}()

	path := filepath.Join(t.TempDir(), "config.yaml")
	data := []byte("provider: custom\napi_url: http://example.test/v1\napi_key: test\nmodel_name: test\nBENCHMARK_BASE_URL: https://tsecbench.example\nBENCHMARK_TOKEN: token-123\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	viper.Reset()
	if err := InitConfig(path); err != nil {
		t.Fatalf("InitConfig: %v", err)
	}
	if GlobalConfig.BenchmarkBaseURL != "https://tsecbench.example" || GlobalConfig.BenchmarkToken != "token-123" {
		t.Fatalf("benchmark config not loaded: %#v", GlobalConfig)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
