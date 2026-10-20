package executor

import (
	"ai-edr/internal/config"
	"context"
	"net"
	"testing"
	"time"
)

func TestSSHProbeAuthenticatesAndKeepsCurrentExecutor(t *testing.T) {
	addr, cleanup := startMockHuaweiSSHServer(t)
	defer cleanup()
	old := Current
	defer func() { Current = old }()
	current := &LocalExecutor{}
	Current = current
	cfg := config.Config{SSHHost: addr, SSHUser: "hexin", SSHPassword: "secret", SSHHostKeyPolicy: "insecure", SSHConnectTimeoutSec: 2}
	if err := ProbeSSH(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if Current != current {
		t.Fatal("probe switched current executor")
	}
}

func TestSSHProbeCancellationInterruptsHandshake(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			accepted <- conn
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = ProbeSSH(ctx, config.Config{SSHHost: listener.Addr().String(), SSHUser: "lab", SSHPassword: "lab", SSHHostKeyPolicy: "insecure", SSHConnectTimeoutSec: 10})
	if err == nil || time.Since(start) > time.Second {
		t.Fatal("handshake was not canceled promptly")
	}
	select {
	case conn := <-accepted:
		conn.Close()
	default:
	}
}

func TestSSHReconnectKeepsOriginalTargetAfterConfigMutation(t *testing.T) {
	old := config.GlobalConfig
	defer func() { config.GlobalConfig = old }()
	connected := config.Config{TargetProtocol: "ssh", SSHHost: "192.0.2.1:22", SSHUser: "old-user", SSHPassword: "old-secret", SSHPrompt: "old-prompt"}
	ex := &SSHExecutor{connectionConfig: &connected}
	config.GlobalConfig = config.Config{TargetProtocol: "local", SSHHost: "192.0.2.2:2222", SSHUser: "new-user", SSHPassword: "new-secret", ControllerProxy: "http://127.0.0.1:8080"}
	retry, err := sshReconnectConfig(ex)
	if err != nil || retry.SSHHost != connected.SSHHost || retry.SSHUser != connected.SSHUser || retry.SSHPassword != connected.SSHPassword || retry.SSHPrompt != connected.SSHPrompt || retry.TargetProtocol != "ssh" || retry.ControllerProxy != config.GlobalConfig.ControllerProxy {
		t.Fatal("retry was redirected to saved target", err)
	}
	if _, err := sshReconnectConfig(&SSHExecutor{}); err == nil {
		t.Fatal("missing snapshot used unrelated global target")
	}
}
