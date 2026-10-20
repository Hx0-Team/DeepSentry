package builtin

import (
	"ai-edr/internal/config"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestProxyProbeUsesCandidateWithoutSendingAPIKeyOrCommitting(t *testing.T) {
	old := config.GlobalConfig
	defer func() { config.GlobalConfig = old }()
	requests := 0
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != http.MethodHead || r.Header.Get("Authorization") != "" || r.Host != "model.example" {
			t.Error("probe included credentials or changed target")
		}
		w.WriteHeader(401)
	}))
	defer proxy.Close()
	config.GlobalConfig = config.Config{Provider: "custom", APIProtocol: "openai_chat", ApiURL: "http://model.example/v1", ModelName: "lab", ApiKey: "private-probe-key"}
	out, err := Run("config_manage", map[string]string{"action": "probe_proxy", "value": proxy.URL}, Runtime{Context: context.Background()})
	if err != nil || requests != 1 || !strings.Contains(out, "HTTP 401") || !strings.Contains(out, "尚未修改") || config.GlobalConfig.ControllerProxy != "" || strings.Contains(out, "private-probe-key") {
		t.Fatal("probe result or state incorrect", err)
	}
}

func TestProxyProbeFailsClosedAndHonorsCancellation(t *testing.T) {
	old := config.GlobalConfig
	defer func() { config.GlobalConfig = old }()
	config.GlobalConfig = config.Config{Provider: "custom", APIProtocol: "openai_chat", ApiURL: "http://model.example/v1", ModelName: "lab"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Run("config_manage", map[string]string{"action": "probe_proxy", "value": "http://127.0.0.1:1"}, Runtime{Context: ctx}); err == nil {
		t.Fatal("canceled probe succeeded")
	}
	if _, err := Run("config_manage", map[string]string{"action": "probe_proxy", "value": "http://bad:proxy-secret@127.0.0.1:1"}, Runtime{}); err == nil || strings.Contains(err.Error(), "proxy-secret") {
		t.Fatal("failed probe exposed credentials", err)
	}
}
