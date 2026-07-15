package logger

import (
	"ai-edr/internal/analyzer"
	"ai-edr/internal/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTitleFromHistory(t *testing.T) {
	history := []analyzer.Message{
		{Role: "user", Content: "需求：检查 SSH 登录"},
		{Role: "user", Content: "Output:\nroot 1234"},
		{Role: "user", Content: "【系统】本轮已结束。"},
		{Role: "system", Content: "ignored"},
	}
	got := TitleFromHistory(history)
	if got != "检查 SSH 登录报告" {
		t.Fatalf("title=%q", got)
	}
}

func TestReporterRedactsSecretsInToolAndCommandLogs(t *testing.T) {
	tmp := t.TempDir()
	oldwd, _ := os.Getwd()
	defer os.Chdir(oldwd)
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	old := config.GlobalConfig
	config.GlobalConfig.ApiKey = "api-secret-value-123"
	config.GlobalConfig.Targets = []config.TargetConfig{{Password: "ssh-secret-value-456"}}
	defer func() { config.GlobalConfig = old }()

	reporter, path, err := NewReporter()
	if err != nil {
		t.Fatal(err)
	}
	reporter.Log("tool", "api_key: api-secret-value-123 password: ssh-secret-value-456")
	reporter.LogCommand("sshpass -p 'ssh-secret-value-456' ssh host", "Authorization: Bearer api-secret-value-123")
	reporter.Close()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"api-secret-value-123", "ssh-secret-value-456"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("report leaked %q:\n%s", secret, raw)
		}
	}
}

func TestReporterSetTitle(t *testing.T) {
	tmp := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldwd)
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}

	reporter, path, err := NewReporter()
	if err != nil {
		t.Fatal(err)
	}
	defer reporter.Close()

	if err := reporter.SetTitle("查看当前系统服务"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(tmp, path))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(data), "\xEF\xBB\xBF# 查看当前系统服务报告\n") {
		t.Fatalf("unexpected report header: %q", string(data[:80]))
	}
}

func TestReporterUsesPrivateUniqueFiles(t *testing.T) {
	tmp := t.TempDir()
	oldwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(oldwd)
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}

	r1, p1, err := NewReporter()
	if err != nil {
		t.Fatal(err)
	}
	defer r1.Close()
	r2, p2, err := NewReporter()
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Close()
	if p1 == p2 {
		t.Fatalf("concurrent reporters collided at %s", p1)
	}
	for _, path := range []string{p1, p2} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("report %s mode=%o want 600", path, got)
		}
	}
}

func TestMarkdownFenceSurvivesEmbeddedCodeFence(t *testing.T) {
	if got := markdownFence("before ``` nested ```` block"); got != "`````" {
		t.Fatalf("fence=%q", got)
	}
}
