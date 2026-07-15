package executor

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ai-edr/internal/config"

	"golang.org/x/crypto/ssh"
)

func TestSSHHostKeyAcceptNewPinsAndRejectsChangedKey(t *testing.T) {
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	callback, err := sshHostKeyCallback(config.Config{
		SSHHostKeyPolicy:  "accept-new",
		SSHKnownHostsPath: knownHosts,
	})
	if err != nil {
		t.Fatalf("sshHostKeyCallback: %v", err)
	}

	hostname := "[127.0.0.1]:2222"
	remote := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 2222}
	key1 := newTestSSHPublicKey(t)
	if err := callback(hostname, remote, key1); err != nil {
		t.Fatalf("first-use key should be accepted: %v", err)
	}
	if err := callback(hostname, remote, key1); err != nil {
		t.Fatalf("pinned key should be accepted: %v", err)
	}
	if err := callback(hostname, remote, newTestSSHPublicKey(t)); err == nil || !strings.Contains(err.Error(), "主机密钥校验失败") {
		t.Fatalf("changed key should be rejected, got %v", err)
	}

	info, err := os.Stat(knownHosts)
	if err != nil {
		t.Fatalf("stat known_hosts: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("known_hosts mode=%o want 600", got)
	}
}

func TestSSHHostKeyStrictRequiresPinnedHost(t *testing.T) {
	knownHosts := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(knownHosts, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	callback, err := sshHostKeyCallback(config.Config{
		SSHHostKeyPolicy:  "strict",
		SSHKnownHostsPath: knownHosts,
	})
	if err != nil {
		t.Fatalf("sshHostKeyCallback: %v", err)
	}
	remote := &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 22}
	if err := callback("127.0.0.1:22", remote, newTestSSHPublicKey(t)); err == nil {
		t.Fatal("strict mode must reject an unpinned host")
	}
}

func TestSSHHostKeyPolicyRejectsUnknownValue(t *testing.T) {
	if _, err := sshHostKeyCallback(config.Config{SSHHostKeyPolicy: "trust-everything"}); err == nil {
		t.Fatal("unknown host key policy should fail closed")
	}
}

func newTestSSHPublicKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	return key
}
