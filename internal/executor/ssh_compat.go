package executor

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"ai-edr/internal/config"

	"golang.org/x/crypto/ssh"
)

// sshLegacyCompatEnabled is the product default: old OpenSSH, Dropbear,
// Cisco/Huawei/H3C appliances still speak ssh-rsa, DH-SHA1 and CBC.
// Operators can set ssh_legacy_compat=false to offer only modern algorithms.
func sshLegacyCompatEnabled(cfg config.Config) bool {
	raw := strings.TrimSpace(strings.ToLower(cfg.SSHLegacyCompat))
	return raw == "" || raw == "true" || raw == "1" || raw == "yes" || raw == "on"
}

func sshConnectTimeout(cfg config.Config) time.Duration {
	return secondsOrDefault(cfg.SSHConnectTimeoutSec, 25)
}

func buildSSHClientConfig(cfg config.Config, hostKeyCallback ssh.HostKeyCallback) (*ssh.ClientConfig, error) {
	authMethods, err := sshAuthMethods(cfg)
	if err != nil {
		return nil, err
	}
	client := &ssh.ClientConfig{
		User:            firstNonEmpty(cfg.SSHUser, "root"),
		Auth:            authMethods,
		HostKeyCallback: hostKeyCallback,
		Timeout:         sshConnectTimeout(cfg),
	}
	if sshLegacyCompatEnabled(cfg) {
		kex, ciphers, macs, hostKeys := sshLegacyAlgorithmLists()
		client.KeyExchanges = kex
		client.Ciphers = ciphers
		client.MACs = macs
		client.HostKeyAlgorithms = preferPinnedSSHHostKeyAlgorithms(cfg, hostKeys)
	} else {
		modern := ssh.SupportedAlgorithms()
		client.KeyExchanges = modern.KeyExchanges
		client.Ciphers = modern.Ciphers
		client.MACs = modern.MACs
		client.HostKeyAlgorithms = preferPinnedSSHHostKeyAlgorithms(cfg, ssh.SupportedAlgorithms().HostKeys)
	}
	return client, nil
}

func sshAuthMethods(cfg config.Config) ([]ssh.AuthMethod, error) {
	var methods []ssh.AuthMethod
	if strings.TrimSpace(cfg.SSHKeyPath) != "" {
		signer, err := parseSSHPrivateKey(cfg.SSHKeyPath, cfg.SSHKeyPassphrase)
		if err != nil {
			return nil, err
		}
		if !sshLegacyCompatEnabled(cfg) {
			//lint:ignore SA1019 Explicitly reject legacy DSA outside compatibility mode.
			if signer.PublicKey().Type() == ssh.InsecureKeyAlgoDSA {
				return nil, fmt.Errorf("DSA 私钥需要 ssh_legacy_compat=true")
			}
			if rsaSigner, ok := signer.(ssh.AlgorithmSigner); ok && signer.PublicKey().Type() == ssh.KeyAlgoRSA {
				signer, err = ssh.NewSignerWithAlgorithms(rsaSigner, []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256})
				if err != nil {
					return nil, err
				}
			}
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if password := cfg.SSHPassword; password != "" {
		methods = append(methods, ssh.Password(password))
		methods = append(methods, ssh.KeyboardInteractive(sshPasswordKeyboardInteractive(password)))
	}
	if len(methods) == 0 {
		return nil, fmt.Errorf("SSH 未配置认证方式：请提供 ssh_password 或 ssh_key_path")
	}
	return methods, nil
}

func parseSSHPrivateKey(path, passphrase string) (ssh.Signer, error) {
	path = expandLocalPath(path)
	key, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取私钥失败: %w", err)
	}
	signer, err := ssh.ParsePrivateKey(key)
	if err != nil {
		var missing *ssh.PassphraseMissingError
		if errors.As(err, &missing) {
			if passphrase == "" {
				return nil, fmt.Errorf("私钥已加密，请配置 ssh_key_passphrase")
			}
			signer, err = ssh.ParsePrivateKeyWithPassphrase(key, []byte(passphrase))
		}
	}
	if err != nil {
		return nil, fmt.Errorf("解析私钥失败: %w", err)
	}
	return wrapLegacySSHSigner(signer), nil
}

func wrapLegacySSHSigner(signer ssh.Signer) ssh.Signer {
	algo, ok := signer.(ssh.AlgorithmSigner)
	if !ok || signer.PublicKey().Type() != ssh.KeyAlgoRSA {
		return signer
	}
	wrapped, err := ssh.NewSignerWithAlgorithms(algo, []string{ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256, ssh.KeyAlgoRSA})
	if err != nil {
		return signer
	}
	return wrapped
}

func sshPasswordKeyboardInteractive(password string) ssh.KeyboardInteractiveChallenge {
	return func(name, instruction string, questions []string, echos []bool) ([]string, error) {
		answers := make([]string, len(questions))
		for i := range questions {
			answers[i] = password
		}
		return answers, nil
	}
}

func sshLegacyAlgorithmLists() (kex, ciphers, macs, hostKeys []string) {
	supported := ssh.SupportedAlgorithms()
	insecure := ssh.InsecureAlgorithms()
	return uniqueKeepOrder(supported.KeyExchanges, insecure.KeyExchanges),
		uniqueKeepOrder(supported.Ciphers, insecure.Ciphers),
		uniqueKeepOrder(supported.MACs, insecure.MACs),
		uniqueKeepOrder(supported.HostKeys, insecure.HostKeys)
}

func uniqueKeepOrder(groups ...[]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, group := range groups {
		for _, value := range group {
			if value == "" || seen[value] {
				continue
			}
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}

func formatSSHHandshakeError(addr string, err error) error {
	return formatSSHHandshakeErrorForConfig(addr, err, config.Config{})
}

func formatSSHHandshakeErrorForConfig(addr string, err error, cfg config.Config) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	hint := ""
	lower := strings.ToLower(msg)
	var negotiation *ssh.AlgorithmNegotiationError
	if errors.As(err, &negotiation) {
		mode := "旧协议兼容已开启，现代算法优先"
		if !sshLegacyCompatEnabled(cfg) {
			mode = "旧协议兼容关闭；该设备需要旧算法时可配置 ssh_legacy_compat=true"
		}
		return fmt.Errorf("SSH算法协商失败 (%s)，类别=%s，对端提供=%s；%s；不要用关闭主机密钥验证解决算法不匹配: %w", addr, negotiation.What, strings.Join(negotiation.RequestedAlgorithms, ","), mode, err)
	}
	switch {
	case strings.Contains(lower, "no common algorithm") || strings.Contains(lower, "no matching"):
		hint = "；请检查具体协商算法与 ssh_legacy_compat；这类错误不等同于密码错误"
	case strings.Contains(lower, "unable to authenticate") || strings.Contains(lower, "permission denied"):
		hint = "；仅尝试已配置的认证方式，密码优先于 keyboard-interactive；核对用户名、认证方式和私钥口令"
	case strings.Contains(lower, "unsupported protocol version") || strings.Contains(lower, "ssh-1."):
		hint = "；仅支持 SSH2；SSH1-only 服务需升级协议，不属于密钥协商兼容开关范围"
	}
	return fmt.Errorf("SSH握手失败 (%s): %w%s", addr, err, hint)
}
