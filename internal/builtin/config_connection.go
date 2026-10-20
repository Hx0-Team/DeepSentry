package builtin

import (
	"ai-edr/internal/config"
	"ai-edr/internal/executor"
	"ai-edr/internal/security"
	"fmt"
	"net/http"
	"strings"
	"time"
)

func manageConfigConnection(args map[string]string, rt Runtime) (string, error) {
	cfg, err := config.ManagedConnectionConfig(args)
	if err != nil {
		return "", err
	}
	switch strings.ToLower(args["action"]) {
	case "probe_ssh":
		if name := args["name"]; name != "" {
			found := false
			for _, target := range cfg.Targets {
				if target.Name == name && (target.Protocol == "ssh" || target.Protocol == "sftp") {
					cfg.SSHHost = target.Host
					cfg.SSHUser = target.User
					cfg.SSHPassword = target.Password
					cfg.SSHKeyPath = target.KeyPath
					cfg.SSHKeyPassphrase = target.KeyPassphrase
					found = true
					break
				}
			}
			if !found {
				return "", fmt.Errorf("未找到该 SSH 目标；用 fleet_inventory 核对 name")
			}
		}
		if cfg.SSHHost == "" {
			return "", fmt.Errorf("尚未配置 SSH；先 set_ssh 或指定已保存目标 name")
		}
		if err := rt.context().Err(); err != nil {
			return "", err
		}
		// Bound a failed handshake; current executor remains usable throughout.
		if cfg.SSHConnectTimeoutSec == 0 || cfg.SSHConnectTimeoutSec > 10 {
			cfg.SSHConnectTimeoutSec = 10
		}
		info, probeErr := executor.ProbeSSHDetails(rt.context(), cfg)
		if err := probeErr; err != nil {
			return "", fmt.Errorf("SSH 认证探测失败（配置未修改，当前连接保留）: %s", security.RedactSensitiveText(err.Error()))
		}
		return fmt.Sprintf("[OK] SSH 握手和认证通过: %s@%s；未执行远程命令、未切换当前执行器。主机密钥策略沿用配置（accept-new 可写入 known_hosts）。\n协议=%s KEX=%s host_key=%s cipher=%s MAC=%s", cfg.SSHUser, cfg.SSHHost, info.ServerVersion, info.Algorithms.KeyExchange, info.Algorithms.HostKey, info.Algorithms.Read.Cipher, info.Algorithms.Read.MAC), nil
	case "probe_proxy":
		proxy := args["value"]
		if _, ok := args["value"]; !ok {
			proxy = cfg.ControllerProxy
		}
		client, err := config.HTTPClientForProxy(proxy, 8*time.Second)
		if err != nil {
			return "", fmt.Errorf("代理格式无效；需要 http(s)/socks5(h)://host:port")
		}
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		primary := cfg.ConfigForModel(cfg.EffectiveModels()[0])
		request, err := http.NewRequestWithContext(rt.context(), http.MethodHead, primary.ApiURL, nil)
		if err != nil {
			return "", fmt.Errorf("模型接口地址无效")
		}
		// HEAD has no model payload or API credential. HTTP errors still prove
		// that the proxy can reach the configured endpoint, not authentication.
		response, err := client.Do(request)
		if err != nil {
			return "", fmt.Errorf("代理到模型接口的探测失败；未写入、未切换代理。请检查监听、认证、网络或证书")
		}
		defer response.Body.Close()
		route := "直连"
		if proxy != "" {
			route = config.ControllerProxySummary(proxy)
		}
		return fmt.Sprintf("[OK] 控制端路由 %s 到模型接口已收到 HTTP %d；HEAD 未调用模型、不计模型 tokens，未发送 API key。未验证模型认证、额度或 SSH 路由；尚未修改配置。", route, response.StatusCode), nil
	}
	return "", fmt.Errorf("未知连接探测")
}

func executorMode(ex executor.Executor) string {
	if r, ok := ex.(executor.ModeReporter); ok {
		return r.Mode()
	}
	if ex.IsRemote() {
		return "remote"
	}
	return "local"
}
