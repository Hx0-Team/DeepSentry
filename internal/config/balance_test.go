package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"
)

func TestBalanceConfigLoadsDisabledAndModelOverrides(t *testing.T) {
	v := viper.New()
	v.SetConfigType("yaml")
	err := v.ReadConfig(strings.NewReader(`provider: deepseek
api_url: https://api.deepseek.com
api_key: placeholder
balance_query:
  enabled: false
  interval_sec: 90
  path: /balance
  remaining_field: data.remaining
  headers:
    X-Query-Key: private-test-key
models:
  - role: primary
    provider: custom
    api_url: https://gateway.example/v1
    balance_query:
      enabled: true
      path: /account
      remaining_field: credit
      unit: USD
      scale: 0.0001
`))
	if err != nil {
		t.Fatal(err)
	}
	var c Config
	if err = v.Unmarshal(&c); err != nil {
		t.Fatal(err)
	}
	if c.BalanceQuery.Enabled == nil || *c.BalanceQuery.Enabled || c.BalanceQuery.IntervalSec != 90 || c.BalanceQuery.Headers["x-query-key"] != "private-test-key" {
		t.Fatal("balance config did not load")
	}
	model := c.ConfigForModel(c.EffectiveModels()[0])
	if model.BalanceQuery.Enabled == nil || !*model.BalanceQuery.Enabled || model.BalanceQuery.Path != "/account" || model.BalanceQuery.Scale != 0.0001 || len(model.BalanceQuery.Headers) != 0 {
		t.Fatal("per-model config did not override root")
	}
	c.Models = nil
	if single := c.ConfigForModel(c.EffectiveModels()[0]); single.BalanceQuery.Enabled == nil || *single.BalanceQuery.Enabled {
		t.Fatal("single model lost root query configuration")
	}
}

func TestBalanceConfigGetHidesAllAuthenticationHeaders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := `balance_query:
  path: /balance
  remaining_field: data.remaining
  headers:
    Authorization: Bearer private-test-secret
    X-Custom-Credential: another-private-test-secret
`
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	text, err := configGet(path, map[string]string{"key": "balance_query"})
	if err != nil || strings.Contains(text, "private-test-secret") || !strings.Contains(text, "***") || !strings.Contains(text, "data.remaining") {
		t.Fatal("custom credentials exposed", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != raw {
		t.Fatal("viewing config modified credentials")
	}
}

func TestBalanceProxySnapshotFailsClosed(t *testing.T) {
	client, err := HTTPClientForProxy("http://127.0.0.1:8080", 8*time.Second)
	if err != nil || client == nil || client.Timeout != 8*time.Second {
		t.Fatal(err)
	}
	client.CloseIdleConnections()
	if _, err := HTTPClientForProxy("not-a-proxy", time.Second); err == nil {
		t.Fatal("invalid proxy fell back to direct network")
	}
}
