package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ai-edr/internal/config"
	"ai-edr/internal/tools"

	"github.com/spf13/viper"
)

func TestSearchSlashStatusShowsSwitchAndCredentialSource(t *testing.T) {
	original := config.GlobalConfig
	t.Cleanup(func() { config.GlobalConfig = original })
	config.GlobalConfig = config.Config{
		Provider:  "deepseek",
		ApiURL:    "https://api.deepseek.com",
		ApiKey:    "test-only-key",
		ModelName: "deepseek-flash",
		Search:    config.SearchConfig{Enabled: true, Count: 8},
	}
	m := AgentModel{}
	m.handleSlashCommand("/search status")
	if len(m.lines) == 0 {
		t.Fatal("no status output")
	}
	out := m.lines[len(m.lines)-1].content
	for _, want := range []string{"联网搜索: 已开启", "https://api.deepseek.com/v1/tools/search", "复用主模型 DeepSeek API Key", "按需加载"} {
		if !strings.Contains(out, want) {
			t.Fatalf("status missing %q:\n%s", want, out)
		}
	}
}

func TestSearchSlashExplainsClosedSwitch(t *testing.T) {
	original := config.GlobalConfig
	t.Cleanup(func() { config.GlobalConfig = original })
	config.GlobalConfig = config.Config{Provider: "deepseek", ApiURL: "https://api.deepseek.com", ApiKey: "test-only-key", ModelName: "deepseek-flash"}
	m := AgentModel{}
	m.handleSlashCommand("/search")
	out := m.lines[len(m.lines)-1].content
	if !strings.Contains(out, "已关闭") || !strings.Contains(out, "/search on") {
		t.Fatalf("closed status did not explain the switch:\n%s", out)
	}
}

func TestSearchSlashTogglePersistsAndAppliesImmediately(t *testing.T) {
	original := config.GlobalConfig
	viper.Reset()
	t.Cleanup(func() {
		config.GlobalConfig = original
		tools.SetWebSearchEnabled(true)
		viper.Reset()
	})
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := `provider: deepseek
api_key: private-fixture-key
api_url: https://api.deepseek.com
model_name: deepseek-flash
search:
  enabled: true
`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := config.InitConfig(path); err != nil {
		t.Fatal(err)
	}
	tools.SetWebSearchEnabled(true)

	m := AgentModel{}
	m.handleSlashCommand("/search off")
	if tools.WebSearchEnabled() {
		t.Fatal("web_search stayed available after /search off")
	}
	if !strings.Contains(readSearchTestFile(t, path), "enabled: false") {
		t.Fatal("/search off did not persist")
	}

	m.handleSlashCommand("/search on")
	if !tools.WebSearchEnabled() {
		t.Fatal("web_search was not restored after /search on")
	}
	if !strings.Contains(readSearchTestFile(t, path), "enabled: true") {
		t.Fatal("/search on did not persist")
	}
	if strings.Contains(strings.Join(searchTestLineContents(m), "\n"), "private-fixture-key") {
		t.Fatal("slash output leaked the API key")
	}
}

func readSearchTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func searchTestLineContents(m AgentModel) []string {
	out := make([]string, 0, len(m.lines))
	for _, line := range m.lines {
		out = append(out, line.content)
	}
	return out
}
