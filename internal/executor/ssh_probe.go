package executor

import (
	"ai-edr/internal/config"
	"context"
	"golang.org/x/crypto/ssh"
	"strings"
)

// ProbeSSH authenticates and closes without starting a session, executing a
// remote command or replacing the active executor. Host-key policy is shared
// with normal connections, including accept-new known_hosts persistence.
func ProbeSSH(ctx context.Context, cfg config.Config) error {
	_, err := ProbeSSHDetails(ctx, cfg)
	return err
}

type SSHProbeInfo struct {
	ServerVersion string
	Algorithms    ssh.NegotiatedAlgorithms
}

func ProbeSSHDetails(ctx context.Context, cfg config.Config) (SSHProbeInfo, error) {
	client, err := dialSSHClientContext(ctx, cfg)
	if err != nil {
		return SSHProbeInfo{}, err
	}
	defer client.Close()
	info := SSHProbeInfo{ServerVersion: strings.TrimSpace(string(client.ServerVersion()))}
	if negotiated, ok := client.Conn.(ssh.AlgorithmsConnMetadata); ok {
		info.Algorithms = negotiated.Algorithms()
	}
	return info, nil
}
