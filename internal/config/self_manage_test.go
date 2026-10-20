package config

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/spf13/viper"
)

func selfConfigFixture(t *testing.T) string {
	t.Helper()
	old := GlobalConfig
	viper.Reset()
	t.Cleanup(func() { GlobalConfig = old; viper.Reset() })
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := `# preserve this comment
provider: deepseek
api_key: private-fixture-secret
api_url: https://api.deepseek.com
model_name: deepseek-flash
context_window_tokens: 32768
reserved_output_tokens: 16000
history_budget_tokens: 24000
terminal_theme: dark
balance_query:
  enabled: true
  interval_sec: 60
  headers:
    X-Credential: header-fixture-secret
`
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	if err := InitConfig(path); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSelfConfigAtomicPatchAndReadback(t *testing.T) {
	path := selfConfigFixture(t)
	// Either half would violate the old context window; the pair is valid.
	out, err := ManageConfig(map[string]string{"action": "patch", "changes": `{"context_window_tokens":8192,"reserved_output_tokens":2048,"balance_query.enabled":false,"execution_budget.max_steps":120}`})
	if err != nil {
		t.Fatal(err)
	}
	if GlobalConfig.ContextWindowTokens != 8192 || GlobalConfig.ReservedOutputTokens != 2048 || GlobalConfig.BalanceQuery.Enabled == nil || *GlobalConfig.BalanceQuery.Enabled || GlobalConfig.ExecutionBudget.MaxSteps != 120 {
		t.Fatal("patch not reloaded as a whole")
	}
	for _, want := range []string{"已回读核对", "32768 → 8192", "16000 → 2048"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %s", want)
		}
	}
	if strings.Contains(out, "fixture-secret") {
		t.Fatal("secret exposed")
	}
	data, _ := os.ReadFile(path)
	for _, want := range []string{"# preserve this comment", "private-fixture-secret", "header-fixture-secret", "terminal_theme: dark"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("unrelated setting lost: %s", want)
		}
	}
	backups, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".deepsentry_backups", "*.yaml"))
	if len(backups) != 1 {
		t.Fatal("expected one backup")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("config permissions")
	}
	get, err := ManageConfig(map[string]string{"action": "get", "key": "history_budget_tokens"})
	if err != nil || !strings.Contains(get, "24000") || strings.Contains(get, "***") {
		t.Fatal("token budget incorrectly treated as secret")
	}
	get, err = ManageConfig(map[string]string{"action": "get", "key": "balance_query.headers"})
	if err != nil || strings.Contains(get, "fixture-secret") || !strings.Contains(get, "***") {
		t.Fatal("nested headers exposed")
	}
}

func TestSelfConfigPreviewFailureAndClear(t *testing.T) {
	path := selfConfigFixture(t)
	before, _ := os.ReadFile(path)
	old := GlobalConfig.HistoryBudgetTokens
	for _, changes := range []string{`{"context_window_tokens":4096}`, `{"execution_budget.total_steps":-1}`, `{"balance_query.interval_sec":2}`, `{"unknown_setting":1}`, `{"temperature":"NaN"}`, `{"max_steps":3.5}`, `{"balance_query.enabled":null}`} {
		if _, err := ManageConfig(map[string]string{"action": "patch", "changes": changes}); err == nil {
			t.Fatalf("accepted invalid patch %s", changes)
		}
	}
	out, err := ManageConfig(map[string]string{"action": "patch", "changes": `{"history_budget_tokens":12000}`, "dry_run": "true"})
	if err != nil || !strings.Contains(out, "未写入") {
		t.Fatal("preview failed", err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) || GlobalConfig.HistoryBudgetTokens != old {
		t.Fatal("preview or invalid patch mutated config")
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(path), ".deepsentry_backups")); !os.IsNotExist(err) {
		t.Fatal("preview created backup")
	}
	if _, err := ManageConfig(map[string]string{"action": "set", "key": "controller_proxy", "value": ""}); err != nil {
		t.Fatal("cannot clear optional value", err)
	}
	if _, err := ManageConfig(map[string]string{"action": "set", "key": "api_key"}); err == nil {
		t.Fatal("missing value silently erased key")
	}
}

func TestSelfConfigModelChainTargetsActualPrimary(t *testing.T) {
	path := selfConfigFixture(t)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("models:\n  - id: spare\n    role: fallback\n    model_name: deepseek-pro\n  - id: active\n    role: primary\n    model_name: deepseek-flash\n")
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := InitConfig(path); err != nil {
		t.Fatal(err)
	}
	out, err := ManageConfig(map[string]string{"action": "status"})
	if err != nil || !strings.Contains(out, "id=active") {
		t.Fatal("wrong primary", err)
	}
	if _, err := ManageConfig(map[string]string{"action": "set", "key": "model_name", "value": "deepseek-pro"}); err == nil {
		t.Fatal("silent root shadow allowed")
	}
	out, err = ManageConfig(map[string]string{"action": "patch", "model_id": "primary", "changes": `{"model_name":"deepseek-pro","temperature":0.2}`})
	if err != nil {
		t.Fatal(err)
	}
	if GlobalConfig.ModelName != "deepseek-flash" || GlobalConfig.EffectiveModels()[0].ModelName != "deepseek-pro" || GlobalConfig.Models[0].ModelName != "deepseek-pro" || GlobalConfig.Models[0].Temperature != 0 {
		t.Fatal("wrong model modified")
	}
	if strings.Contains(out, "fixture-secret") {
		t.Fatal("model secret exposed")
	}
	out, err = ManageConfig(map[string]string{"action": "get", "model_id": "active", "key": "model_name"})
	if err != nil || !strings.Contains(out, "deepseek-pro") {
		t.Fatal("model readback wrong", err)
	}
}

func TestSelfConfigReloadFailureRestoresFile(t *testing.T) {
	path := selfConfigFixture(t)
	before, _ := os.ReadFile(path)
	// Emulate a startup override that makes reload invalid after valid YAML.
	viper.Set("terminal_theme", "invalid-override")
	_, err := ManageConfig(map[string]string{"action": "patch", "changes": `{"history_budget_tokens":12000}`})
	if err == nil || !strings.Contains(err.Error(), "已恢复") {
		t.Fatal("reload failure was not rolled back", err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) || GlobalConfig.HistoryBudgetTokens != 24000 {
		t.Fatal("reload failure left partial settings")
	}
}

func TestSelfConfigConcurrentUpdatesAreNotLost(t *testing.T) {
	path := selfConfigFixture(t)
	var wg sync.WaitGroup
	errors := make(chan error, 2)
	for key, value := range map[string]string{"max_steps": "22", "subagent_max_steps": "11"} {
		wg.Add(1)
		go func(key, value string) {
			defer wg.Done()
			_, err := ManageConfig(map[string]string{"action": "set", "config_path": path, "key": key, "value": value})
			errors <- err
		}(key, value)
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	if GlobalConfig.MaxSteps != 22 || GlobalConfig.SubAgentMaxSteps != 11 {
		t.Fatal("lost update")
	}
}

func TestSelfConfigDefaultsMatchStartupAndDescribe(t *testing.T) {
	selfConfigFixture(t)
	out, err := ManageConfig(map[string]string{"action": "describe"})
	if err != nil || !strings.Contains(out, "execution_budget.max_steps") || !strings.Contains(out, "balance_query.enabled") || strings.Contains(out, "fixture-secret") {
		t.Fatal("incomplete field guidance", err)
	}
	out, err = ManageConfig(map[string]string{"action": "set", "key": "use_native_tools", "value": "off"})
	if err != nil || GlobalConfig.UseNativeTools {
		t.Fatal("bool normalization failed", err)
	}
	if !strings.Contains(out, "需重启") {
		t.Fatal("restart requirement hidden")
	}
}

func TestSelfConfigSSHPortAndFleetPartialUpdatePreserveCredentials(t *testing.T) {
	path := selfConfigFixture(t)
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	_, err := f.WriteString("ssh_host: 192.0.2.5:22\nssh_user: ops\nssh_password: ssh-fixture-secret\nssh_device_type: huawei\nssh_prompt: '<device>'\nssh_enable_password: enable-fixture-secret\ntargets:\n - name: edge\n   protocol: ssh\n   host: 192.0.2.8:22\n   user: netops\n   key_path: /tmp/lab-id\n   key_passphrase: key-fixture-secret\n   tags: [lab]\n")
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := InitConfig(path); err != nil {
		t.Fatal(err)
	}
	out, err := ManageConfig(map[string]string{"action": "set_ssh", "port": "2222"})
	if err != nil {
		t.Fatal(err)
	}
	if GlobalConfig.SSHHost != "192.0.2.5:2222" || GlobalConfig.SSHUser != "ops" || GlobalConfig.SSHPassword != "ssh-fixture-secret" || GlobalConfig.SSHDeviceType != "huawei" || GlobalConfig.SSHPrompt != "<device>" || GlobalConfig.SSHEnablePassword != "enable-fixture-secret" {
		t.Fatal("port update erased SSH fields")
	}
	if strings.Contains(out, "fixture-secret") {
		t.Fatal("SSH secret exposed")
	}
	_, err = ManageConfig(map[string]string{"action": "add_target", "name": "edge", "port": "2223"})
	if err != nil {
		t.Fatal(err)
	}
	target := GlobalConfig.Targets[0]
	if target.Host != "192.0.2.8:2223" || target.User != "netops" || target.KeyPath != "/tmp/lab-id" || target.KeyPassphrase != "key-fixture-secret" || len(target.Tags) != 1 {
		t.Fatal("Fleet partial update erased fields")
	}
	_, err = ManageConfig(map[string]string{"action": "set_ssh", "host": "192.0.2.9", "key_path": "/tmp/new-key"})
	if err != nil {
		t.Fatal(err)
	}
	if GlobalConfig.SSHPassword != "" || GlobalConfig.SSHKeyPassphrase != "" {
		t.Fatal("unrelated host inherited credentials")
	}
}

func TestSelfConfigProxyCredentialsAndGroupsRemainPrivate(t *testing.T) {
	selfConfigFixture(t)
	for _, group := range []string{"balance_query", "execution_budget", "models"} {
		out, err := ManageConfig(map[string]string{"action": "describe", "key": group})
		if err != nil || !strings.Contains(out, "字段") {
			t.Fatal("group description missing", err)
		}
	}
	out, err := ManageConfig(map[string]string{"action": "patch", "changes": `{"controller_proxy":"http://proxyuser:proxy-fixture-secret@127.0.0.1:8888"}`})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "proxy-fixture-secret") || strings.Contains(out, "proxyuser") {
		t.Fatal("proxy credentials exposed")
	}
	out, err = ManageConfig(map[string]string{"action": "get", "key": "controller_proxy"})
	if err != nil || strings.Contains(out, "proxy-fixture-secret") {
		t.Fatal("proxy get exposed secret")
	}
}

func TestSelfConfigPreviewWhitespaceAndMalformedRootStayReadOnly(t *testing.T) {
	path := selfConfigFixture(t)
	before, _ := os.ReadFile(path)
	if _, err := ManageConfig(map[string]string{"action": "set", "key": "max_steps", "value": "22", "dry_run": " true "}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatal("whitespace preview wrote config")
	}
	bad := []byte("- this-is-not-a-config-map\n")
	if err := os.WriteFile(path, bad, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ManageConfig(map[string]string{"action": "set", "key": "max_steps", "value": "22"}); err == nil {
		t.Fatal("malformed root was silently reset")
	}
	after, _ = os.ReadFile(path)
	if string(after) != string(bad) {
		t.Fatal("malformed source changed")
	}
}

func TestSelfConfigAuthenticationWhitespaceIsPreserved(t *testing.T) {
	selfConfigFixture(t)
	if _, err := ManageConfig(map[string]string{"action": "set_ssh", "host": "192.0.2.1", "user": "ops", "password": " leading trailing "}); err != nil {
		t.Fatal(err)
	}
	if GlobalConfig.SSHPassword != " leading trailing " {
		t.Fatal("password whitespace was removed")
	}
	if _, err := ManageConfig(map[string]string{"action": "add_target", "protocol": "ssh", "host": "192.0.2.2", "user": "ops", "key_path": "/tmp/lab-key", "key_passphrase": " passphrase "}); err != nil {
		t.Fatal(err)
	}
	if GlobalConfig.Targets[0].KeyPassphrase != " passphrase " {
		t.Fatal("key passphrase whitespace was removed")
	}
}

func TestManagedSFTPModeAndFleetPreserveProtocol(t *testing.T) {
	selfConfigFixture(t)
	_, err := ManageConfig(map[string]string{"action": "patch", "changes": `{"target_protocol":"sftp","ssh_host":"192.0.2.3:2222","ssh_user":"files","ssh_password":"file-fixture-secret"}`})
	if err != nil {
		t.Fatal(err)
	}
	_, err = ManageConfig(map[string]string{"action": "enable_fleet"})
	if err != nil {
		t.Fatal(err)
	}
	if len(GlobalConfig.Targets) != 1 || GlobalConfig.Targets[0].Protocol != "sftp" || GlobalConfig.Targets[0].User != "files" {
		t.Fatal("SFTP promoted as shell SSH")
	}
}
