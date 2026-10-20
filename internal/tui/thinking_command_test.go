package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ai-edr/internal/config"

	"github.com/spf13/viper"
)

func thinkingTestConfig(t *testing.T) string {
	t.Helper()
	original := config.GlobalConfig
	viper.Reset()
	t.Cleanup(func() {
		config.GlobalConfig = original
		viper.Reset()
	})
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := `provider: deepseek
api_key: private-fixture-key
api_url: https://api.deepseek.com
model_name: deepseek-flash
thinking_mode: auto
reasoning_effort: auto
`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := config.InitConfig(path); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestThinkingSlashStatusAndToggle(t *testing.T) {
	path := thinkingTestConfig(t)
	m := AgentModel{}
	m.handleSlashCommand("/thinking status")
	out := m.lines[len(m.lines)-1].content
	for _, want := range []string{"思考模式: 开启", "默认 high", "64K"} {
		if !strings.Contains(out, want) {
			t.Fatalf("status missing %q:\n%s", want, out)
		}
	}

	m.handleSlashCommand("/thinking off")
	out = m.lines[len(m.lines)-1].content
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "thinking_mode: disabled") || !strings.Contains(out, "思考模式: 关闭") {
		t.Fatalf("off not persisted or reported:\n%s\n%s", data, out)
	}

	m.handleSlashCommand("/thinking max")
	out = m.lines[len(m.lines)-1].content
	data, _ = os.ReadFile(path)
	if !strings.Contains(string(data), "reasoning_effort: max") || !strings.Contains(out, "128K") {
		t.Fatalf("max not persisted or reported:\n%s\n%s", data, out)
	}
	m.handleSlashCommand("/thinking budget 96k")
	out = m.lines[len(m.lines)-1].content
	data, _ = os.ReadFile(path)
	if !strings.Contains(string(data), "reserved_output_tokens: 98304") || !strings.Contains(out, "96K") || !strings.Contains(out, "显式") {
		t.Fatalf("budget not persisted or reported:\n%s\n%s", data, out)
	}
	m.handleSlashCommand("/thinking budget auto")
	out = m.lines[len(m.lines)-1].content
	data, _ = os.ReadFile(path)
	if !strings.Contains(string(data), "reserved_output_tokens: 0") || !strings.Contains(out, "自动") {
		t.Fatalf("auto budget not persisted or reported:\n%s\n%s", data, out)
	}
	if !strings.Contains(string(data), "private-fixture-key") {
		t.Fatal("config patch dropped the unrelated API key")
	}
}

func TestParseOutputBudget(t *testing.T) {
	for input, want := range map[string]int{
		"auto": 0, "": 0, "0": 0, "512": 512, "64k": 65_536, "128K": 131_072, "1m": 1_048_576,
	} {
		got, err := parseOutputBudget(input)
		if err != nil || got != want {
			t.Fatalf("parseOutputBudget(%q)=%d,%v want %d", input, got, err, want)
		}
	}
	for _, input := range []string{"abc", "10x", "511", "2m", "999999999"} {
		if _, err := parseOutputBudget(input); err == nil {
			t.Fatalf("parseOutputBudget(%q) should fail", input)
		}
	}
}
